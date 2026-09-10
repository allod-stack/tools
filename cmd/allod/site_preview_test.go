package main

// Tests for 'allod site preview'. This file carries no build tag, unlike
// site_test.go, because preview compiles into every build: both
// 'go test ./...' and 'go test -tags site ./...' must run it.
//
// The seam these tests swap is package-level mutable state, so no test here
// calls t.Parallel.

import (
	"strings"
	"testing"
)

// previewStub records the argv sitePreview asked to run and answers with
// whatever status the test set up.
type previewStub struct {
	root     string
	zolaArgs []string
	calls    int
	status   int
}

func usePreviewStub(t *testing.T, stub *previewStub) string {
	t.Helper()
	previous := sitePreviewRun
	sitePreviewRun = func(root string, zolaArgs []string) int {
		stub.root, stub.zolaArgs, stub.calls = root, zolaArgs, stub.calls+1
		return stub.status
	}
	t.Cleanup(func() { sitePreviewRun = previous })
	return stubTools(t, "nix")
}

func TestSitePreviewOutsideSiteRepo(t *testing.T) {
	stub := &previewStub{}
	usePreviewStub(t, stub)
	// A directory with no site.toml above it: t.TempDir sits under the system
	// temp dir, which no site repository owns.
	t.Chdir(t.TempDir())

	_, errText, code := runAllod(t, "site", "preview")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{"no site.toml", "to fix:"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
		}
	}
	if stub.calls != 0 {
		t.Errorf("preview ran %d times, want 0", stub.calls)
	}
}

// TestSitePreviewDefaultArgv pins the argv built when no flags are given:
// the root, and nothing the user did not ask for.
func TestSitePreviewDefaultArgv(t *testing.T) {
	stub := &previewStub{}
	usePreviewStub(t, stub)
	root := useSiteRepo(t, "domain = \"example.com\"\n")

	_, errText, code := runAllod(t, "site", "preview")
	if code != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", code, errText)
	}
	if stub.calls != 1 {
		t.Fatalf("preview ran %d times, want 1", stub.calls)
	}
	if stub.root != root {
		t.Errorf("root = %q, want %q", stub.root, root)
	}
	if len(stub.zolaArgs) != 0 {
		t.Errorf("zola args = %v, want none: a flag the user did not give must not appear", stub.zolaArgs)
	}
}

// TestSitePreviewFlags pins the exact argv built for each flag zola accepts,
// individually and combined.
func TestSitePreviewFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"port", []string{"--port", "2111"}, []string{"--port", "2111"}},
		{"interface", []string{"--interface", "0.0.0.0"}, []string{"--interface", "0.0.0.0"}},
		{"base-url", []string{"--base-url", "http://example.test"}, []string{"--base-url", "http://example.test"}},
		{"drafts", []string{"--drafts"}, []string{"--drafts"}},
		{"open", []string{"--open"}, []string{"--open"}},
		{
			"all combined",
			[]string{"--port", "2111", "--interface", "0.0.0.0", "--base-url", "http://example.test", "--drafts", "--open"},
			[]string{"--port", "2111", "--interface", "0.0.0.0", "--base-url", "http://example.test", "--drafts", "--open"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &previewStub{}
			usePreviewStub(t, stub)
			useSiteRepo(t, "domain = \"example.com\"\n")

			args := append([]string{"site", "preview"}, test.args...)
			_, errText, code := runAllod(t, args...)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
			}
			if stub.calls != 1 {
				t.Fatalf("preview ran %d times, want 1", stub.calls)
			}
			if !equalArgs(stub.zolaArgs, test.want) {
				t.Errorf("zola args = %v, want %v", stub.zolaArgs, test.want)
			}
		})
	}
}

// TestSitePreviewPassthrough checks that everything after '--' reaches zola
// verbatim, appended after any mapped flags.
func TestSitePreviewPassthrough(t *testing.T) {
	stub := &previewStub{}
	usePreviewStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")

	_, errText, code := runAllod(t, "site", "preview", "--port", "2111", "--", "--extra-flag", "value")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
	}
	want := []string{"--port", "2111", "--extra-flag", "value"}
	if !equalArgs(stub.zolaArgs, want) {
		t.Errorf("zola args = %v, want %v", stub.zolaArgs, want)
	}
}

// TestSitePreviewPassthroughOnly checks that a bare '--' with nothing mapped
// before it still passes everything after it through, including a value that
// looks like an option this command knows.
func TestSitePreviewPassthroughOnly(t *testing.T) {
	stub := &previewStub{}
	usePreviewStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")

	_, errText, code := runAllod(t, "site", "preview", "--", "--port", "9999")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %q", code, errText)
	}
	want := []string{"--port", "9999"}
	if !equalArgs(stub.zolaArgs, want) {
		t.Errorf("zola args = %v, want %v", stub.zolaArgs, want)
	}
}

func TestSitePreviewRejectsUnknownOption(t *testing.T) {
	stub := &previewStub{}
	usePreviewStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")

	_, errText, code := runAllod(t, "site", "preview", "--publish")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "unknown option for site preview: --publish"; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
	}
	if stub.calls != 0 {
		t.Errorf("preview ran %d times, want 0", stub.calls)
	}
}

func TestSitePreviewRejectsUnexpectedArgument(t *testing.T) {
	stub := &previewStub{}
	usePreviewStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")

	_, errText, code := runAllod(t, "site", "preview", "extra")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "unexpected argument for site preview: extra"; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
	}
	if stub.calls != 0 {
		t.Errorf("preview ran %d times, want 0", stub.calls)
	}
}

func TestSitePreviewRejectsMissingFlagValue(t *testing.T) {
	for _, flag := range []string{"--port", "--interface", "--base-url"} {
		t.Run(flag, func(t *testing.T) {
			stub := &previewStub{}
			usePreviewStub(t, stub)
			useSiteRepo(t, "domain = \"example.com\"\n")

			_, errText, code := runAllod(t, "site", "preview", flag)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if want := flag + " requires a value"; !strings.Contains(errText, want) {
				t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
			}
			if stub.calls != 0 {
				t.Errorf("preview ran %d times, want 0", stub.calls)
			}
		})
	}
}

// TestSitePreviewPropagatesExitCode checks that zola's own exit code, as
// reported through the seam, becomes allod's exit code.
func TestSitePreviewPropagatesExitCode(t *testing.T) {
	stub := &previewStub{status: 3}
	usePreviewStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")

	_, _, code := runAllod(t, "site", "preview")
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestSitePreviewMissingNix(t *testing.T) {
	stub := &previewStub{}
	previous := sitePreviewRun
	sitePreviewRun = func(root string, zolaArgs []string) int {
		stub.calls++
		return 0
	}
	t.Cleanup(func() { sitePreviewRun = previous })
	t.Setenv("PATH", t.TempDir()) // empty: no 'nix' anywhere on it
	useSiteRepo(t, "domain = \"example.com\"\n")

	_, errText, code := runAllod(t, "site", "preview")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "'nix' not found on PATH"; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
	}
	if stub.calls != 0 {
		t.Errorf("preview ran %d times, want 0", stub.calls)
	}
}

func TestSitePreviewHelp(t *testing.T) {
	out, errText, code := runAllod(t, "site", "preview", "--help")
	if code != 0 || errText != "" {
		t.Fatalf("exit=%d stderr=%q, want success with empty stderr", code, errText)
	}
	if want := "allod site preview "; !strings.Contains(out, want) {
		t.Errorf("stdout does not contain %q\ngot: %q", want, out)
	}
}

func equalArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
