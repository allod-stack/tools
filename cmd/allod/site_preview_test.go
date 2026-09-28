package main

// Tests for 'allod site preview'. This file carries no build tag, unlike
// site_test.go, because preview compiles into every build.
//
// The seam they swap (sitePreviewRun) is at the exec boundary: it is handed the
// whole argument list production code built, so the lists written out below pin
// the real ones, and nothing here rebuilds a list by calling production code.
// The sandbox has no systemd and no nix; it does have loopback, so the wait's
// connection attempt is the real one, against a listener a test opened or a
// port it has closed. The seams are package state: no test calls t.Parallel.

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	previewSiteID  = "allod/site-example"
	previewUnit    = "allod-preview-allod-site-example"
	previewLogTail = "the unit's own log tail"
)

type previewCall struct {
	name string
	args []string
}

type previewStub struct {
	calls []previewCall
	// active answers each 'systemctl is-active' in turn, the last answer over
	// again once they run out. absent is what the nix eval prints: the empty
	// string when the flake has the app.
	active []bool
	absent string
}

func usePreviewStub(t *testing.T, stub *previewStub) {
	t.Helper()
	run, timeout, poll := sitePreviewRun, sitePreviewTimeout, sitePreviewPoll
	t.Cleanup(func() { sitePreviewRun, sitePreviewTimeout, sitePreviewPoll = run, timeout, poll })
	// Zeroed, so the wait reaches every branch it would with a real bound and
	// no test sleeps.
	sitePreviewTimeout, sitePreviewPoll = 0, 0
	sitePreviewRun = func(name string, args []string, out io.Writer) int {
		stub.calls = append(stub.calls, previewCall{name, args})
		switch {
		case name == "nix":
			fmt.Fprint(out, stub.absent)
		case name == "journalctl":
			fmt.Fprint(out, previewLogTail+"\n")
		case name == "systemctl" && args[1] == "is-active":
			answer := stub.active[0]
			if len(stub.active) > 1 {
				stub.active = stub.active[1:]
			}
			if !answer {
				return 4
			}
		}
		return 0
	}
}

// matching returns the argument lists of every recorded call to program whose
// second argument is verb, or to program alone when verb is empty.
func (stub *previewStub) matching(program, verb string) [][]string {
	var found [][]string
	for _, call := range stub.calls {
		if call.name == program && (verb == "" || len(call.args) > 1 && call.args[1] == verb) {
			found = append(found, call.args)
		}
	}
	return found
}

func (stub *previewStub) only(t *testing.T, program, verb string) []string {
	t.Helper()
	found := stub.matching(program, verb)
	if len(found) != 1 {
		t.Fatalf("%d calls to %s %s, want 1: %+v", len(found), program, verb, stub.calls)
	}
	return found[0]
}

func samePreviewArgs(t *testing.T, label string, got, want []string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s argv =\n%v\nwant\n%v", label, got, want)
	}
}

// usePreviewRegistry writes a registry whose one entry is previewSiteID, with
// checkout "sites/example" and, unless port is zero, that preview_port. It also
// creates the checkout with a site.toml, and returns its root.
func usePreviewRegistry(t *testing.T, port int) string {
	t.Helper()
	work, inventory := previewTempDir(t), previewTempDir(t)
	root := filepath.Join(work, "sites", "example")
	previewWrite(t, filepath.Join(root, siteConfigName), "domain = \"example.invalid\"\n")
	entry := fmt.Sprintf("{%q: {\"checkout\": \"sites/example\"", previewSiteID)
	if port != 0 {
		entry += fmt.Sprintf(", \"preview_port\": %d", port)
	}
	previewWrite(t, filepath.Join(inventory, "scripts", "repositories.json"), `{"repositories": `+entry+"}}}")
	t.Setenv("WORK_DIR", work)
	t.Setenv("INVENTORY", inventory)
	return root
}

// previewTempDir resolves the symlinks t.TempDir may hand back: the command
// resolves the current directory, so a registry naming an unresolved checkout
// would never match the directory the preview was started from.
func previewTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func previewWrite(t *testing.T, path, text string) {
	t.Helper()
	err := os.MkdirAll(filepath.Dir(path), 0755)
	if err == nil {
		err = os.WriteFile(path, []byte(text), 0644)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// previewPort returns a loopback port, with a listener on it when serving is
// true so the wait's connection succeeds, and with nothing on it when serving
// is false so the connection is refused at once.
func previewPort(t *testing.T, serving bool) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if serving {
		t.Cleanup(func() { listener.Close() })
	} else if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// TestSitePreviewStart pins every argument list a start builds, the port and
// checkout the registry supplies for a named site, and that the same site found
// by walking up from the current directory reaches the same start command, and
// so the same unit: that is what lets a '--stop' issued elsewhere name the unit
// this machine started.
func TestSitePreviewStart(t *testing.T) {
	byID := &previewStub{active: []bool{false}}
	usePreviewStub(t, byID)
	port := previewPort(t, true)
	root := usePreviewRegistry(t, port)

	out, errText, code := runAllod(t, "site", "preview", previewSiteID)
	if code != 0 {
		t.Fatalf("by id: exit code = %d, want 0; stderr: %q", code, errText)
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d\n", port); out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	samePreviewArgs(t, "nix", byID.only(t, "nix", ""), []string{"eval", "--impure", "--raw", "--expr",
		`let flake = builtins.getFlake "` + root + `"; system = builtins.currentSystem; ` +
			`in if ((flake.apps or {}).${system} or {}) ? preview then "" else "apps.${system}.preview"`})
	samePreviewArgs(t, "is-active", byID.only(t, "systemctl", "is-active"),
		[]string{"--user", "is-active", "--quiet", previewUnit})
	samePreviewArgs(t, "systemd-run", byID.only(t, "systemd-run", ""),
		[]string{"--user", "--collect", "--quiet", "--unit", previewUnit, "--working-directory", root,
			"-E", fmt.Sprintf("ALLOD_PREVIEW_PORT=%d", port), "-E", "ALLOD_PREVIEW_INTERFACE=127.0.0.1",
			"--", "nix", "run", root + "#preview"})

	fromDirectory := &previewStub{active: []bool{false}}
	usePreviewStub(t, fromDirectory)
	t.Chdir(root)
	if _, errText, code := runAllod(t, "site", "preview"); code != 0 {
		t.Fatalf("from the checkout: exit code = %d, want 0; stderr: %q", code, errText)
	}
	samePreviewArgs(t, "start from the checkout", fromDirectory.only(t, "systemd-run", ""),
		byID.only(t, "systemd-run", ""))
}

// TestSitePreviewPortFlagWins pins that '--port' overrides the site's
// preview_port: the registry's port here has nothing listening on it, so a run
// that read the registry instead would never reach the serving outcome.
func TestSitePreviewPortFlagWins(t *testing.T) {
	stub := &previewStub{active: []bool{false}}
	usePreviewStub(t, stub)
	port := previewPort(t, true)
	usePreviewRegistry(t, 18601)

	out, errText, code := runAllod(t, "site", "preview", "--port", fmt.Sprint(port), previewSiteID)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d\n", port); out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
}

// TestSitePreviewOutcomes pins what the wait reports and whether anything is
// started, each row against a real loopback port, answered or closed, and
// against 'is-active' answers taken in turn. The false green to avoid is the
// 'unit gone' row reporting success.
func TestSitePreviewOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		active  []bool
		serving bool
		code    int
		starts  bool
		errHas  string
	}{
		{"started and serving", []bool{false}, true, 0, true, ""},
		{"already active serves without a second start", []bool{true}, true, 0, false, ""},
		{"unit gone", []bool{false}, false, 1, true, previewLogTail},
		{"still starting", []bool{false, true}, false, 3, true, "journalctl --user -f -u " + previewUnit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &previewStub{active: test.active}
			usePreviewStub(t, stub)
			port := previewPort(t, test.serving)
			usePreviewRegistry(t, port)

			out, errText, code := runAllod(t, "site", "preview", previewSiteID)
			if code != test.code {
				t.Fatalf("exit code = %d, want %d; stderr: %q", code, test.code, errText)
			}
			address := ""
			if test.code == 0 {
				address = fmt.Sprintf("http://127.0.0.1:%d\n", port)
			}
			if out != address {
				t.Errorf("stdout = %q, want %q", out, address)
			}
			if test.errHas != "" && !strings.Contains(errText, test.errHas) {
				t.Errorf("stderr does not contain %q\ngot: %q", test.errHas, errText)
			}
			starts := len(stub.matching("systemd-run", ""))
			if (starts != 0) != test.starts {
				t.Errorf("systemd-run ran %d times, want started = %v", starts, test.starts)
			}
			if test.code == 1 {
				samePreviewArgs(t, "journalctl", stub.only(t, "journalctl", ""),
					[]string{"--user", "-u", previewUnit, "-n", "20", "--no-pager"})
			} else if shown := len(stub.matching("journalctl", "")); shown != 0 {
				t.Errorf("journalctl ran %d times, want 0: there is no failure to show", shown)
			}
		})
	}
}

// TestSitePreviewStop pins the stop command, that a stop needs neither the port
// nor the app, and that a preview which is not running is reported rather than
// stopped: 'systemctl --user stop' exits 5 on a unit that was never loaded.
func TestSitePreviewStop(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(fmt.Sprintf("running=%v", running), func(t *testing.T) {
			stub := &previewStub{active: []bool{running}}
			usePreviewStub(t, stub)
			usePreviewRegistry(t, 0)

			out, errText, code := runAllod(t, "site", "preview", "--stop", previewSiteID)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
			}
			if running {
				samePreviewArgs(t, "stop", stub.only(t, "systemctl", "stop"), []string{"--user", "stop", previewUnit})
			} else {
				if stops := len(stub.matching("systemctl", "stop")); stops != 0 {
					t.Errorf("stop ran %d times, want 0", stops)
				}
				if !strings.Contains(out, previewUnit+" is not running") {
					t.Errorf("stdout does not report the preview as not running\ngot: %q", out)
				}
			}
			if asked := len(stub.matching("nix", "")); asked != 0 {
				t.Errorf("nix ran %d times, want 0: a stop needs neither the port nor the app", asked)
			}
		})
	}
}

// TestSitePreviewRefusals covers each refusal: no app, no port, a site the
// registry does not list, a value that begins with '-', a port outside the range
// the registry validation enforces, a second positional. None starts anything.
func TestSitePreviewRefusals(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		registryPort int
		absent       string
		message      string
	}{
		{"no app", []string{previewSiteID}, 18601, "apps.x86_64-linux.preview",
			"has no apps.x86_64-linux.preview; docs/allod-site-preview.md"},
		{"no port", []string{previewSiteID}, 0, "", "no preview port for " + previewSiteID},
		{"unknown site", []string{"allod/absent"}, 18601, "", "unknown site: allod/absent"},
		{"port value begins with a dash", []string{"--port", "-1"}, 18601, "", "--port requires a value"},
		{"port with no value", []string{"--port"}, 18601, "", "--port requires a value"},
		{"port below the range", []string{"--port", "1023"}, 18601, "", "1024 to 65535, not 1023"},
		{"port above the range", []string{"--port", "65536"}, 18601, "", "1024 to 65535, not 65536"},
		{"port not a whole number", []string{"--port", "eighty"}, 18601, "", "1024 to 65535, not eighty"},
		{"site id begins with a dash", []string{"-x"}, 18601, "", "unknown option for site preview: -x"},
		{"second positional", []string{previewSiteID, "extra"}, 18601, "", "unexpected argument for site preview: extra"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &previewStub{active: []bool{false}, absent: test.absent}
			usePreviewStub(t, stub)
			usePreviewRegistry(t, test.registryPort)

			_, errText, code := runAllod(t, append([]string{"site", "preview"}, test.args...)...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1; stderr: %q", code, errText)
			}
			if !strings.Contains(errText, test.message) {
				t.Errorf("stderr does not contain %q\ngot: %q", test.message, errText)
			}
			if starts := len(stub.matching("systemd-run", "")); starts != 0 {
				t.Errorf("systemd-run ran %d times, want 0: the command refused", starts)
			}
		})
	}
}

// TestSitePreviewHelp checks that the help says what an operator needs and
// names no generator: which tool builds a site is the site flake's business.
func TestSitePreviewHelp(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		out, errText, code := runAllod(t, "site", "preview", flag)
		if code != 0 || errText != "" {
			t.Fatalf("%s: exit=%d stderr=%q, want success with empty stderr", flag, code, errText)
		}
		for _, want := range []string{
			"allod site preview [--port <n>] [--stop] [<site>]",
			"journalctl --user -u allod-preview-<slug>",
			"127.0.0.1",
			"'preview_port'",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: help does not contain %q\ngot: %q", flag, want, out)
			}
		}
		for _, generator := range []string{"zola", "Vite", "vite", "hugo", "Hugo"} {
			if strings.Contains(out, generator) {
				t.Errorf("%s: help names the generator %q", flag, generator)
			}
		}
	}
}

func TestSitePreviewSlugReplacesEveryOtherCharacter(t *testing.T) {
	for name, want := range map[string]string{
		"allod/site-example": "allod-site-example",
		"work/sites/a b.c_d": "work-sites-a-b.c_d",
	} {
		if got := sitePreviewSlug(name); got != want {
			t.Errorf("slug of %q = %q, want %q", name, got, want)
		}
	}
}
