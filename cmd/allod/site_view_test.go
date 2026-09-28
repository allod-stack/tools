package main

// Tests for 'allod site view', which runs on the hypervisor and so compiles into
// every build. They use site_preview_test.go's stub and fixtures, and add a seam
// standing in for the process replacing itself with ssh.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// usePreviewVMs writes a vm-specs.json beside the registry usePreviewRegistry
// wrote, giving each machine named the repository list given.
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

// useExecStub captures the command that would have replaced this process.
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

// TestSiteViewForwardsThePort pins both commands a view runs — the remote start
// over a connection of its own, then the forward it replaces itself with — and
// that the address is printed once, by this machine rather than the VM, whose own
// loopback address means nothing here until the forward is up.
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

// TestSiteViewVMLookup pins which machine is used: the one whose repository list
// holds the site, '--vm' over that, and a refusal that counts and names what was
// found when it is not exactly one.
func TestSiteViewVMLookup(t *testing.T) {
	tests := []struct {
		name     string
		machines map[string][]string
		args     []string
		vm       string
		says     string
	}{
		{name: "the one machine that lists it", machines: map[string][]string{"vm-one": {previewSiteID}}, vm: "vm-one"},
		{name: "--vm overrides the lookup", machines: map[string][]string{"vm-one": {previewSiteID}},
			args: []string{"--vm", "vm-other"}, vm: "vm-other"},
		{name: "no machine lists it", machines: map[string][]string{"vm-one": {"allod/other"}},
			says: "0 machines in "},
		{name: "several machines list it", says: "2 machines in ",
			machines: map[string][]string{"vm-one": {previewSiteID}, "vm-two": {previewSiteID}}},
		{name: "several machines are named", says: "[vm-one vm-two]",
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
				if !strings.Contains(errText, test.says) {
					t.Errorf("stderr does not contain %q\ngot: %q", test.says, errText)
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

// TestSiteViewStopsWhenTheRemoteStartFails pins that a view whose preview could
// not be started in the VM exits with that command's own code and forwards
// nothing: the remote command has already said why on standard error.
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

// TestSiteViewRefusals covers each refusal. None of them forwards anything.
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

// TestSiteViewHelp checks that the help says what Ctrl-C does and does not end,
// and how to stop the server from here.
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
