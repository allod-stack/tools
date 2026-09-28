package main

// These use site_preview_test.go's stub and fixtures, and one more seam: the
// process replacing itself with ssh.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Beside the registry usePreviewRegistry wrote, which is where production looks.
func usePreviewVMs(t *testing.T, machines map[string][]string) {
	t.Helper()
	specs := map[string]map[string][]string{}
	for name, repos := range machines {
		specs[name] = map[string][]string{"repos": repos}
	}
	body, err := json.Marshal(specs)
	if err != nil {
		t.Fatal(err)
	}
	previewWrite(t, filepath.Join(os.Getenv("INVENTORY"), "scripts", "vm-specs.json"), string(body))
}

func useExecStub(t *testing.T, captured *[]string) {
	t.Helper()
	previous := sitePreviewExec
	sitePreviewExec = func(argv []string) { *captured = argv }
	t.Cleanup(func() { sitePreviewExec = previous })
}

func wantSiteViewForward(vm string, port int) []string {
	return []string{"ssh", "-N", "-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port), "--", vm}
}

// The address is printed once, by this machine: the VM's own is meaningless here
// until the forward is up.
func TestSiteViewForwardsThePort(t *testing.T) {
	stub := &previewStub{}
	usePreviewStub(t, stub)
	usePreviewRegistry(t, previewCheckout, "18601")
	usePreviewVMs(t, map[string][]string{"vm-one": {previewSiteID}, "vm-two": {"allod/other"}})
	var forward []string
	useExecStub(t, &forward)

	out, errText, code := runAllod(t, "site", "view", previewSiteID)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
	}
	if want := "http://127.0.0.1:18601\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	stub.pinCommand(t, "ssh", "-o", "ControlMaster=no", "-o", "ControlPath=none", "--", "vm-one",
		"allod", "site", "preview", "--port", "'18601'", "'"+previewSiteID+"'")
	if want := wantSiteViewForward("vm-one", 18601); fmt.Sprint(forward) != fmt.Sprint(want) {
		t.Errorf("forward =\n%v\nwant\n%v", forward, want)
	}
}

func TestSiteViewVMLookup(t *testing.T) {
	tests := []struct {
		name     string
		machines map[string][]string
		args     []string
		vm       string
		says     []string
	}{
		{name: "the one machine that lists it", machines: map[string][]string{"vm-one": {previewSiteID}}, vm: "vm-one"},
		{name: "--vm overrides the lookup", machines: map[string][]string{"vm-one": {previewSiteID}},
			args: []string{"--vm", "vm-other"}, vm: "vm-other"},
		{name: "no machine lists it", machines: map[string][]string{"vm-one": {"allod/other"}},
			says: []string{"0 machines in "}},
		{name: "several machines are counted and named", says: []string{"2 machines in ", "(vm-one vm-two)"},
			machines: map[string][]string{"vm-two": {previewSiteID}, "vm-one": {previewSiteID}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &previewStub{}
			usePreviewStub(t, stub)
			usePreviewRegistry(t, previewCheckout, "18601")
			usePreviewVMs(t, test.machines)
			var forward []string
			useExecStub(t, &forward)

			_, errText, code := runAllod(t, append(append([]string{"site", "view"}, test.args...), previewSiteID)...)
			if test.vm == "" {
				if code != 1 {
					t.Errorf("exit code = %d, want 1", code)
				}
				for _, says := range test.says {
					if !strings.Contains(errText, says) {
						t.Errorf("stderr does not contain %q\ngot: %q", says, errText)
					}
				}
				if len(forward) != 0 {
					t.Errorf("a refused view still replaced itself: %v", forward)
				}
				return
			}
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
			}
			stub.pinCommand(t, "ssh", "-o", "ControlMaster=no", "-o", "ControlPath=none", "--", test.vm,
				"allod", "site", "preview", "--port", "'18601'", "'"+previewSiteID+"'")
			if want := wantSiteViewForward(test.vm, 18601); fmt.Sprint(forward) != fmt.Sprint(want) {
				t.Errorf("forward =\n%v\nwant\n%v", forward, want)
			}
		})
	}
}

// A preview_port nothing can use is read only where it is needed, so '--port'
// replaces it here as it does in the VM.
func TestSiteViewPortFlagOverridesABadRegistryPort(t *testing.T) {
	stub := &previewStub{}
	usePreviewStub(t, stub)
	usePreviewRegistry(t, previewCheckout, `"18650"`)
	usePreviewVMs(t, map[string][]string{"vm-one": {previewSiteID}})
	var forward []string
	useExecStub(t, &forward)

	out, errText, code := runAllod(t, "site", "view", "--port", "18601", previewSiteID)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
	}
	if want := "http://127.0.0.1:18601\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	if want := wantSiteViewForward("vm-one", 18601); fmt.Sprint(forward) != fmt.Sprint(want) {
		t.Errorf("forward =\n%v\nwant\n%v", forward, want)
	}
}

// A preview that did not start in the VM has already said why there, so this exits
// with its code and forwards nothing.
func TestSiteViewStopsWhenTheRemoteStartFails(t *testing.T) {
	stub := &previewStub{remote: 3}
	usePreviewStub(t, stub)
	usePreviewRegistry(t, previewCheckout, "18601")
	usePreviewVMs(t, map[string][]string{"vm-one": {previewSiteID}})
	var forward []string
	useExecStub(t, &forward)

	out, _, code := runAllod(t, "site", "view", previewSiteID)
	if code != 3 {
		t.Errorf("exit code = %d, want 3, the remote command's own", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty: no address for a preview that did not start", out)
	}
	if len(forward) != 0 {
		t.Errorf("view replaced itself although the preview did not start: %v", forward)
	}
}

// Every refusal. None of them forwards anything.
func TestSiteViewRefusals(t *testing.T) {
	tests := []struct {
		name string
		args []string
		port string
		says string
	}{
		{name: "no port", args: []string{previewSiteID}, says: "no preview port for " + previewSiteID},
		{name: "unknown site", args: []string{"allod/absent"}, port: "18601", says: "unknown site: allod/absent"},
		{name: "no site", port: "18601", says: "site view needs a site id"},
		{name: "vm name begins with a dash", args: []string{"--vm", "-x", previewSiteID}, port: "18601",
			says: "--vm requires a value"},
		{name: "stop is preview's", args: []string{"--stop", previewSiteID}, port: "18601",
			says: "unknown option for site view: --stop"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &previewStub{}
			usePreviewStub(t, stub)
			usePreviewRegistry(t, previewCheckout, test.port)
			usePreviewVMs(t, map[string][]string{"vm-one": {previewSiteID}})
			var forward []string
			useExecStub(t, &forward)

			_, errText, code := runAllod(t, append([]string{"site", "view"}, test.args...)...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1; stderr: %q", code, errText)
			}
			if !strings.Contains(errText, test.says) {
				t.Errorf("stderr does not contain %q\ngot: %q", test.says, errText)
			}
			if len(forward) != 0 {
				t.Errorf("a refused view still replaced itself: %v", forward)
			}
			if started := len(stub.matching("ssh")); started != 0 {
				t.Errorf("%d remote commands ran, want 0", started)
			}
		})
	}
}

func TestSiteViewHelp(t *testing.T) {
	out, errText, code := runAllod(t, "site", "view", "--help")
	if code != 0 || errText != "" {
		t.Fatalf("exit=%d stderr=%q, want success with empty stderr", code, errText)
	}
	for _, want := range []string{"allod site view [--vm <name>] [--port <n>] <site>",
		"Ctrl-C", "keeps running", "allod site preview --stop --vm <name> <site>"} {
		if !strings.Contains(out, want) {
			t.Errorf("help does not contain %q\ngot: %q", want, out)
		}
	}
}
