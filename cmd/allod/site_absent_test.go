//go:build !site

package main

// The other half of the build-tag proof. site_test.go asserts that a build
// made with -tags site carries all four site commands; this file asserts
// that a build without it carries 'preview' alone — not that 'deploy',
// 'check', and 'config' fail, but that those three words mean nothing, which
// is the difference between a machine that cannot deploy and a machine that
// can deploy badly.
//
// Both files are compiled by 'go test' runs that exclude each other, so the
// pair only holds if both runs happen: 'go test ./...' and
// 'go test -tags site ./...'.

import (
	"strings"
	"testing"
)

// TestSiteNamespaceExistsWithPreviewOnly pins the shape an untagged build
// carries: the 'site' namespace is registered — unlike before preview
// existed, when an untagged build had no 'site' word at all — but its
// command table holds exactly 'preview'.
func TestSiteNamespaceExistsWithPreviewOnly(t *testing.T) {
	if _, ok := lookupNamespace("site"); !ok {
		t.Fatal("the site namespace is not registered in an untagged build")
	}
	if got := len(siteCommands); got != 1 {
		t.Fatalf("siteCommands has %d entries in an untagged build, want 1: %+v", got, siteCommands)
	}
	if siteCommands[0].name != "preview" {
		t.Errorf("the one untagged site command is %q, want %q", siteCommands[0].name, "preview")
	}
}

// TestUntaggedSiteDeployCheckConfigAreUnknown pins the behaviour an operator
// sees: 'allod site deploy', 'check', and 'config' on a machine that does
// not publish sites fail exactly the way a typo does, saying so in the same
// words 'unknown site command' always has and then, like any unknown site
// command, printing the short usage rather than nothing.
func TestUntaggedSiteDeployCheckConfigAreUnknown(t *testing.T) {
	for _, command := range []string{"deploy", "check", "config"} {
		t.Run(command, func(t *testing.T) {
			out, errText, code := runAllod(t, "site", command)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			want := "allod: unknown site command: " + command + "\n" + siteShortUsage()
			if errText != want {
				t.Errorf("stderr = %q, want %q", errText, want)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
		})
	}
}

// TestUntaggedSiteDeployWithArgsIsStillUnknown checks that the same is true
// with trailing flags: the command name is unknown before its own arguments
// are ever parsed, so a deploy-shaped invocation fails as a typo would, not
// as a broken deploy would.
func TestUntaggedSiteDeployWithArgsIsStillUnknown(t *testing.T) {
	out, errText, code := runAllod(t, "site", "deploy", "--dry-run")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "allod: unknown site command: deploy\n" + siteShortUsage(); errText != want {
		t.Errorf("stderr = %q, want %q", errText, want)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
}

// TestUntaggedSiteUsageListsOnlyPreview pins that an untagged build's own
// 'allod site' usage advertises exactly what it carries: the deploy, check,
// and config usage lines and summaries are absent, and the preview ones are
// present.
func TestUntaggedSiteUsageListsOnlyPreview(t *testing.T) {
	for _, args := range [][]string{{"site"}, {"site", "--help"}, {"site", "-h"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdoutText, stderrText, _ := runAllod(t, args...)
			out := stdoutText + stderrText
			for _, want := range []string{
				"allod site preview ",
				"preview  Serve a site locally",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("usage does not contain %q\ngot: %q", want, out)
				}
			}
			for _, absent := range []string{
				"allod site deploy",
				"allod site check",
				"allod site config",
				"deploy   Build the site repo",
				"check    Verify the stored hosting credential",
				"config   Create, inspect, update, or replace",
			} {
				if strings.Contains(out, absent) {
					t.Errorf("untagged usage names a command this build does not carry: %q\ngot: %q", absent, out)
				}
			}
		})
	}
}

// TestUntaggedSiteBareInvocationHasNoDetailProse pins the short-usage
// contract in the build that carries only 'preview': a bare 'allod site'
// stays short even with one command registered, and '--help' still carries
// the detail prose the bare form omits.
func TestUntaggedSiteBareInvocationHasNoDetailProse(t *testing.T) {
	const detailOnly = "mirror zola's own flags of the same names"

	_, errText, code := runAllod(t, "site")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if strings.Contains(errText, detailOnly) {
		t.Errorf("bare invocation printed preview's detail prose %q\ngot: %q", detailOnly, errText)
	}
	if want := "\nRun 'allod site --help' for details.\n"; !strings.HasSuffix(errText, want) {
		t.Errorf("bare invocation stderr does not end with %q\ngot: %q", want, errText)
	}

	out, errText, code := runAllod(t, "site", "--help")
	if code != 0 || errText != "" {
		t.Fatalf("exit=%d stderr=%q, want success with empty stderr", code, errText)
	}
	if !strings.Contains(out, detailOnly) {
		t.Errorf("'site --help' is missing preview's detail prose %q\ngot: %q", detailOnly, out)
	}
}

// TestUntaggedSitePreviewArgumentErrorPrintsOwnUsageOnly pins the
// per-command argument-error contract in the build that carries only
// 'preview': an unknown option prints the one-line message, preview's own
// Usage: line, and the pointer to preview's own '--help', but none of
// preview's detail prose — that stays behind '--help', the same way it does
// for the namespace itself.
func TestUntaggedSitePreviewArgumentErrorPrintsOwnUsageOnly(t *testing.T) {
	_, errText, code := runAllod(t, "site", "preview", "--bogus")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{
		"allod: unknown option for site preview: --bogus\n",
		"Usage:\n  allod site preview ",
		"\nRun 'allod site preview --help' for details.\n",
	} {
		if !strings.Contains(errText, want) {
			t.Errorf("argument error does not contain %q\ngot: %q", want, errText)
		}
	}
	if detailOnly := "mirror zola's own flags of the same names"; strings.Contains(errText, detailOnly) {
		t.Errorf("argument error printed preview's detail prose %q\ngot: %q", detailOnly, errText)
	}
}

// TestSiteIsListedInTopLevelUsage checks that the bare 'allod' usage still
// names the site namespace — it is registered in every build now — without
// claiming this build carries deploy, check, or config.
func TestSiteIsListedInTopLevelUsage(t *testing.T) {
	out, _, _ := runAllod(t)
	if !strings.Contains(out, "site") {
		t.Errorf("top-level usage does not mention the site namespace\ngot: %q", out)
	}
}
