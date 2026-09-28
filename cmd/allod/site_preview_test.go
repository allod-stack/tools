package main

// The seam these tests swap (sitePreviewRun) is the exec boundary and is handed
// the whole command production code built, so the lines written out here pin the
// real ones. The sandbox has no systemd and no nix but does have loopback, so the
// wait's connection attempt is real. The seams are package state: no t.Parallel.

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
	previewSiteID   = "allod/site-example"
	previewUnit     = "allod-preview-allod-site-example"
	previewCheckout = "sites/example"
	previewSibling  = "allod/sibling"
	previewLogTail  = "the unit's own log tail"
)

type previewStub struct {
	// calls holds each command run, the program first. active answers the is-active
	// calls in turn, its last character over again once they run out: 'A' active,
	// 'N' no such unit, anything else a manager that could not be asked, which are
	// the codes measured for that command. absent is what the nix eval prints,
	// gitCommon what the git rev-parse prints, remote the status an ssh returns.
	calls     [][]string
	active    string
	absent    string
	gitCommon string
	remote    int
}

func usePreviewStub(t *testing.T, stub *previewStub) {
	t.Helper()
	run, timeout := sitePreviewRun, sitePreviewTimeout
	t.Cleanup(func() { sitePreviewRun, sitePreviewTimeout = run, timeout })
	sitePreviewTimeout = 0
	sitePreviewRun = func(name string, args []string, out io.Writer) int {
		stub.calls = append(stub.calls, append([]string{name}, args...))
		switch {
		case name == "nix":
			fmt.Fprint(out, stub.absent)
		case name == "journalctl":
			fmt.Fprint(out, previewLogTail+"\n")
		case name == "git":
			fmt.Fprintln(out, stub.gitCommon)
		case name == "ssh":
			return stub.remote
		case name == "systemctl" && args[1] == "is-active":
			answer := stub.active[0]
			if len(stub.active) > 1 {
				stub.active = stub.active[1:]
			}
			switch answer {
			case 'A':
				return 0
			case 'N':
				return 4
			default:
				return 1
			}
		}
		return 0
	}
}

func (stub *previewStub) matching(prefix ...string) [][]string {
	var found [][]string
	for _, call := range stub.calls {
		if len(call) >= len(prefix) && fmt.Sprint(call[:len(prefix)]) == fmt.Sprint(prefix) {
			found = append(found, call)
		}
	}
	return found
}

// Three words in, every command this program runs is distinct, so that prefix
// selects the one to compare.
func (stub *previewStub) pinCommand(t *testing.T, want ...string) {
	t.Helper()
	found := stub.matching(want[:3]...)
	if len(found) != 1 {
		t.Fatalf("%d commands begin %v, want 1: %v", len(found), want[:3], stub.calls)
	}
	if fmt.Sprint(found[0]) != fmt.Sprint(want) {
		t.Errorf("command =\n%v\nwant\n%v", found[0], want)
	}
}

// usePreviewRegistry writes a registry of previewSiteID at the checkout given,
// with port as its preview_port unless that is empty, and previewSibling, which
// has none. It returns the first checkout's root.
func usePreviewRegistry(t *testing.T, checkout, port string) string {
	t.Helper()
	// Resolved, because the command resolves the current directory: a registry
	// naming a checkout through a symlink could never match it.
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry := fmt.Sprintf("{%q: {\"checkout\": %q", previewSiteID, checkout)
	if port != "" {
		entry += ", \"preview_port\": " + port
	}
	entry += fmt.Sprintf("}, %q: {\"checkout\": \"sites/sibling\"}}", previewSibling)
	root := filepath.Join(work, checkout)
	previewWrite(t, filepath.Join(root, siteConfigName), "domain = \"example.invalid\"\n")
	previewWrite(t, filepath.Join(work, "scripts", "repositories.json"), `{"repositories": `+entry+"}")
	t.Setenv("WORK_DIR", work)
	t.Setenv("INVENTORY", work)
	return root
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

// A listener when serving, so the wait connects; nothing when not, so it is
// refused at once.
func previewListener(t *testing.T, serving bool) int {
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

// The same site named by id and found from the current directory must reach one
// start, and so one unit name.
func TestSitePreviewStart(t *testing.T) {
	byID := &previewStub{active: "NA"}
	usePreviewStub(t, byID)
	port := previewListener(t, true)
	root := usePreviewRegistry(t, previewCheckout, fmt.Sprint(port))

	out, errText, code := runAllod(t, "site", "preview", previewSiteID)
	if code != 0 {
		t.Fatalf("by id: exit code = %d, want 0; stderr: %q", code, errText)
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d\n", port); out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	byID.pinCommand(t, "nix", "eval", "--impure", "--raw", "--expr",
		`let flake = builtins.getFlake "`+root+`"; system = builtins.currentSystem; `+
			`in if ((flake.apps or {}).${system} or {}) ? preview then "" else "apps.${system}.preview"`)
	byID.pinCommand(t, "systemd-run", "--user", "--collect", "--quiet", "--unit", previewUnit,
		"--working-directory", root, "-E", fmt.Sprintf("ALLOD_PREVIEW_PORT=%d", port),
		"-E", "ALLOD_PREVIEW_INTERFACE=127.0.0.1", "--", "nix", "run", root+"#preview")

	fromDirectory := &previewStub{active: "NA"}
	usePreviewStub(t, fromDirectory)
	t.Chdir(root)
	if _, errText, code := runAllod(t, "site", "preview"); code != 0 {
		t.Fatalf("from the checkout: exit code = %d, want 0; stderr: %q", code, errText)
	}
	got, want := fromDirectory.matching("systemd-run"), byID.matching("systemd-run")
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("start from the checkout =\n%v\nwant the same as by id\n%v", got, want)
	}
}

// ssh hands the words after the host to a shell, so a site id with a space or a
// quote must arrive there as one argument.
func TestSitePreviewVM(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		remote []string
	}{
		{"by id", []string{previewSiteID}, []string{"'" + previewSiteID + "'"}},
		{"with a port and a stop", []string{"--port", "18601", "--stop", previewSiteID},
			[]string{"--port", "'18601'", "--stop", "'" + previewSiteID + "'"}},
		{"a site id with a space", []string{"a site"}, []string{"'a site'"}},
		{"a site id with a single quote", []string{"it's"}, []string{`'it'\''s'`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &previewStub{active: "N"}
			usePreviewStub(t, stub)
			usePreviewRegistry(t, previewCheckout, "18601")

			args := append([]string{"site", "preview", "--vm", "vm-one"}, test.args...)
			if _, errText, code := runAllod(t, args...); code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
			}
			want := []string{"ssh", "-o", "ControlMaster=no", "-o", "ControlPath=none", "--", "vm-one",
				"allod", "site", "preview"}
			stub.pinCommand(t, append(want, test.remote...)...)
			if local := len(stub.calls) - 1; local != 0 {
				t.Errorf("%d commands ran on this machine, want 0: %v", local, stub.calls)
			}
		})
	}
}

// A worktree previews as its site: the repository it belongs to supplies the id,
// the port and the unit name, while the working directory and the flake stay the
// worktree's.
func TestSitePreviewWorktree(t *testing.T) {
	stub := &previewStub{active: "NA"}
	usePreviewStub(t, stub)
	port := previewListener(t, true)
	repository := usePreviewRegistry(t, previewCheckout, fmt.Sprint(port))
	worktree := filepath.Join(filepath.Dir(filepath.Dir(repository)), "changes", "example-branch")
	previewWrite(t, filepath.Join(worktree, siteConfigName), "domain = \"example.invalid\"\n")
	stub.gitCommon = filepath.Join(repository, ".git")
	t.Chdir(worktree)

	out, errText, code := runAllod(t, "site", "preview")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d\n", port); out != want {
		t.Errorf("stdout = %q, want %q: the port is the registry entry's", out, want)
	}
	stub.pinCommand(t, "git", "-C", worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	stub.pinCommand(t, "systemd-run", "--user", "--collect", "--quiet", "--unit", previewUnit,
		"--working-directory", worktree, "-E", fmt.Sprintf("ALLOD_PREVIEW_PORT=%d", port),
		"-E", "ALLOD_PREVIEW_INTERFACE=127.0.0.1", "--", "nix", "run", worktree+"#preview")
}

// Each row runs against a real loopback port. The false green to avoid is either
// of the "unit gone" rows reporting success.
func TestSitePreviewOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		active  string
		serving bool
		code    int
		starts  bool
		logged  bool
		errHas  string
	}{
		{name: "already active starts nothing", active: "A", serving: true, code: 0},
		{name: "unit gone with the port silent", active: "N", code: 1, starts: true,
			logged: true, errHas: previewLogTail},
		{name: "unit gone although the port answered", active: "N", serving: true, code: 1,
			starts: true, logged: true, errHas: previewLogTail},
		{name: "still starting", active: "NA", code: 3, starts: true,
			errHas: "journalctl --user -f -u " + previewUnit},
		{name: "systemd could not be asked", active: "X", serving: true, code: 1,
			errHas: "could not ask systemd about " + previewUnit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &previewStub{active: test.active}
			usePreviewStub(t, stub)
			port := previewListener(t, test.serving)
			usePreviewRegistry(t, previewCheckout, fmt.Sprint(port))

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
			if started := len(stub.matching("systemd-run")) != 0; started != test.starts {
				t.Errorf("started = %v, want %v: %v", started, test.starts, stub.calls)
			}
			// The flake is asked for its app only when a start is about to happen:
			// an active unit may serve a worktree the named checkout knows nothing
			// about.
			if asked := len(stub.matching("nix")) != 0; asked != test.starts {
				t.Errorf("the nix eval ran = %v, want %v", asked, test.starts)
			}
			if test.logged {
				stub.pinCommand(t, "journalctl", "--user", "-u", previewUnit, "-n", "20", "--no-pager")
			} else if shown := len(stub.matching("journalctl")); shown != 0 {
				t.Errorf("journalctl ran %d times, want 0: there is no failure to show", shown)
			}
		})
	}
}

// A stop needs neither the port nor the app.
func TestSitePreviewStopRunning(t *testing.T) {
	stub := &previewStub{active: "A"}
	usePreviewStub(t, stub)
	usePreviewRegistry(t, previewCheckout, "")

	if _, errText, code := runAllod(t, "site", "preview", "--stop", previewSiteID); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
	}
	stub.pinCommand(t, "systemctl", "--user", "is-active", "--quiet", previewUnit)
	stub.pinCommand(t, "systemctl", "--user", "stop", previewUnit)
	if asked := len(stub.matching("nix")); asked != 0 {
		t.Errorf("nix ran %d times, want 0", asked)
	}
}

// A preview_port nothing can use is read where it is needed and nowhere else, so
// it neither blocks a stop nor outlives the '--port' that replaces it.
func TestSitePreviewBadRegistryPortIsReadOnlyWhenUsed(t *testing.T) {
	t.Run("stop", func(t *testing.T) {
		stub := &previewStub{active: "A"}
		usePreviewStub(t, stub)
		usePreviewRegistry(t, previewCheckout, `"18650"`)

		if _, errText, code := runAllod(t, "site", "preview", "--stop", previewSiteID); code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
		}
		stub.pinCommand(t, "systemctl", "--user", "stop", previewUnit)
	})

	t.Run("--port overrides it", func(t *testing.T) {
		stub := &previewStub{active: "NA"}
		usePreviewStub(t, stub)
		port := previewListener(t, true)
		usePreviewRegistry(t, previewCheckout, `"18650"`)

		out, errText, code := runAllod(t, "site", "preview", "--port", fmt.Sprint(port), previewSiteID)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
		}
		if want := fmt.Sprintf("http://127.0.0.1:%d\n", port); out != want {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	})
}

// Every refusal, plus the stop of a preview that is not running, which is not one:
// a stop alone could not report that, since it exits 5 on a unit never loaded. No
// row starts or stops anything.
func TestSitePreviewRefusals(t *testing.T) {
	site := []string{previewSiteID}
	tests := []struct {
		name     string
		args     []string
		checkout string
		port     string
		absent   string
		active   string
		code     int
		says     string
		sibling  bool
	}{
		{name: "no app", args: site, port: "18601", absent: "apps.x86_64-linux.preview", code: 1,
			says: "has no apps.x86_64-linux.preview; docs/allod-site-preview.md"},
		{name: "no port", args: site, code: 1, says: "no preview port for " + previewSiteID},
		{name: "null port is no port", args: site, port: "null", code: 1, says: "no preview port for "},
		{name: "unknown site", args: []string{"allod/absent"}, port: "18601", code: 1, says: "unknown site: allod/absent"},
		{name: "port value begins with a dash", args: []string{"--port", "-1"}, code: 1, says: "--port requires a value"},
		{name: "port with no value", args: []string{"--port"}, code: 1, says: "--port requires a value"},
		{name: "port outside the range", args: []string{"--port", "65536"}, code: 1, says: "1024 to 65535, not 65536"},
		{name: "stop when not running", args: []string{"--stop", previewSiteID}, says: previewUnit + " is not running"},
		{name: "site id begins with a dash", args: []string{"-x"}, code: 1, says: "unknown option for site preview: -x"},
		{name: "--vm with no site", args: []string{"--vm", "vm-one"}, code: 1, says: "--vm needs a site id"},
		{name: "vm name begins with a dash", args: []string{"--vm", "-x", previewSiteID}, code: 1, says: "--vm requires a value"},
		{name: "second positional", args: []string{previewSiteID, "extra"}, code: 1, says: "unexpected argument for site preview: extra"},
		{name: "preview_port that is not a number", args: site, port: `"18650"`, code: 1,
			says: `preview_port "18650" for ` + previewSiteID, sibling: true},
		{name: "systemd cannot be asked for a stop", args: []string{"--stop", previewSiteID}, active: "X", code: 1,
			says: "could not ask systemd about " + previewUnit},
		{name: "a checkout path nix cannot be given", args: site, checkout: `sites/ex"ample`, port: "18601", code: 1,
			says: `cannot be given to nix: it contains '"'`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			active := test.active
			if active == "" {
				active = "N"
			}
			stub := &previewStub{active: active, absent: test.absent}
			usePreviewStub(t, stub)
			checkout := previewCheckout
			if test.checkout != "" {
				checkout = test.checkout
			}
			usePreviewRegistry(t, checkout, test.port)

			out, errText, code := runAllod(t, append([]string{"site", "preview"}, test.args...)...)
			if code != test.code {
				t.Errorf("exit code = %d, want %d; stderr: %q", code, test.code, errText)
			}
			if !strings.Contains(out+errText, test.says) {
				t.Errorf("output does not contain %q\ngot: %q %q", test.says, out, errText)
			}
			if started := len(stub.matching("systemd-run")); started != 0 {
				t.Errorf("systemd-run ran %d times, want 0", started)
			}
			if stops := len(stub.matching("systemctl", "--user", "stop")); stops != 0 {
				t.Errorf("stop ran %d times, want 0", stops)
			}
			// One entry's mistyped preview_port must not cost the other lookups in
			// the file, including the ones no site command makes.
			if test.sibling {
				if checkout, ok := registryCheckout(previewSibling); !ok || checkout != "sites/sibling" {
					t.Errorf("registryCheckout(%q) = %q, %v, want its checkout", previewSibling, checkout, ok)
				}
			}
		})
	}
}

// The help says what an operator needs and names no generator.
func TestSitePreviewHelp(t *testing.T) {
	out, errText, code := runAllod(t, "site", "preview", "--help")
	if code != 0 || errText != "" {
		t.Fatalf("exit=%d stderr=%q, want success with empty stderr", code, errText)
	}
	for _, want := range []string{"allod site preview [--port <n>] [--stop] [--vm <name>] [<site>]",
		"journalctl --user -u allod-preview-<slug>", "127.0.0.1", "'preview_port'"} {
		if !strings.Contains(out, want) {
			t.Errorf("help does not contain %q\ngot: %q", want, out)
		}
	}
	for _, generator := range []string{"zola", "vite", "Vite", "hugo", "Hugo"} {
		if strings.Contains(out, generator) {
			t.Errorf("help names the generator %q", generator)
		}
	}
}
