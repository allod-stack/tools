//go:build site

package main

// Tests for the site namespace. They run the CLI in process through runAllod
// from main_test.go, with every effect on the outside world replaced by a
// stub:
//
//	out, errText, code := runAllod(t, "site", "deploy", "--dry-run")
//
// This file is compiled only with -tags site, so it is also half the proof
// that the namespace exists exactly in the builds that asked for it; the other
// half is site_absent_test.go.
//
// The package-level seams these helpers swap are shared mutable state, so no
// test here calls t.Parallel.

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// --- Harness ---

// deployStub records what the command asked the world to do and answers with
// whatever the test set up.
type deployStub struct {
	remoteResult     siteRemoteCheckResult
	remoteConfigPath string
	remoteCalls      int
	buildDir         string
	buildCalls       int
	storePath        string
	buildStatus      int
	syncArgs         []string
	syncCalls        int
	syncStatus       int
	syncConfigPath   string
	verifyURL        string
	verifyCalls      int
	verifyStatus     int
	verifyErr        error
}

// useDeployStub installs the stub over the four seams in site.go and puts stub
// 'nix' and 'rclone' executables on PATH so the LookPath preflight passes
// without a nix daemon or an rclone installation. It returns the directory
// PATH was set to, which is the whole of PATH for the rest of the test.
func useDeployStub(t *testing.T, stub *deployStub) string {
	t.Helper()
	// A caller that exercises another profile sets it after installing the
	// stub. Keeping the default explicit makes the suite independent of linker
	// flags used to build the test binary.
	useSiteHostingProfile(t, defaultSiteHostingProfile)
	previousRemoteCheck, previousBuild := siteRemoteCheck, siteBuild
	previousSync, previousVerify := siteSync, siteVerify
	siteRemoteCheck = func(configPath string) siteRemoteCheckResult {
		stub.remoteConfigPath, stub.remoteCalls = configPath, stub.remoteCalls+1
		return stub.remoteResult
	}
	siteBuild = func(root string) (string, int) {
		stub.buildDir, stub.buildCalls = root, stub.buildCalls+1
		return stub.storePath, stub.buildStatus
	}
	siteSync = func(configPath string, args []string) int {
		stub.syncConfigPath, stub.syncArgs, stub.syncCalls = configPath, args, stub.syncCalls+1
		return stub.syncStatus
	}
	siteVerify = func(url string) (int, error) {
		stub.verifyURL, stub.verifyCalls = url, stub.verifyCalls+1
		return stub.verifyStatus, stub.verifyErr
	}
	t.Cleanup(func() {
		siteRemoteCheck, siteBuild = previousRemoteCheck, previousBuild
		siteSync, siteVerify = previousSync, previousVerify
	})
	return stubTools(t, "nix", "rclone")
}

func useSiteHostingProfile(t *testing.T, name string) {
	t.Helper()
	previous := siteHostingProfileName
	siteHostingProfileName = name
	t.Cleanup(func() { siteHostingProfileName = previous })
}

func useSiteHostingProfiles(t *testing.T, profiles []siteHostingProfile) {
	t.Helper()
	previous := siteHostingProfiles
	siteHostingProfiles = profiles
	t.Cleanup(func() { siteHostingProfiles = previous })
}

func cloneSiteHostingProfiles(profiles []siteHostingProfile) []siteHostingProfile {
	clone := append([]siteHostingProfile(nil), profiles...)
	for index := range clone {
		clone[index].deployFilterRules = append([]string(nil), clone[index].deployFilterRules...)
	}
	return clone
}

// stubTools lives in main_test.go: it is not specific to the tagged
// commands, and site_preview_test.go needs it in an untagged build too.

// useSiteRepo lives in main_test.go: it is not specific to the tagged
// commands, and site_preview_test.go needs it in an untagged build too.

// argAfter returns the value following flag in args.
func argAfter(t *testing.T, args []string, flag string) string {
	t.Helper()
	for index, arg := range args {
		if arg == flag {
			if index+1 >= len(args) {
				t.Fatalf("%s has no value in %v", flag, args)
			}
			return args[index+1]
		}
	}
	t.Fatalf("%s not present in %v", flag, args)
	return ""
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// --- Dispatch and usage ---

// TestSiteTaggedBuildCarriesAllFourCommands pins the shape a site-tagged
// build carries: 'preview' from the untagged files plus the three this
// file's init() adds, and nothing else.
func TestSiteTaggedBuildCarriesAllFourCommands(t *testing.T) {
	want := []string{"preview", "deploy", "check", "config"}
	if got := len(siteCommands); got != len(want) {
		t.Fatalf("siteCommands has %d entries in a tagged build, want %d: %+v", got, len(want), siteCommands)
	}
	for _, name := range want {
		found := false
		for _, entry := range siteCommands {
			if entry.name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("siteCommands has no %q entry: %+v", name, siteCommands)
		}
	}
}

func TestSiteUsage(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		code       int
		outHas     string
		errHas     string
		errIsEmpty bool
	}{
		{"namespace listed in top-level usage", []string{}, 1, "site     Preview a static site", "", true},
		{"no command prints usage to stderr", []string{"site"}, 1, "", "allod site deploy [--config <path>] [--dry-run]", false},
		{"--help prints usage to stdout", []string{"site", "--help"}, 0, "allod site deploy [--config <path>] [--dry-run]", "", true},
		{"-h prints usage to stdout", []string{"site", "-h"}, 0, "allod site config [--config <path>] [--force]", "", true},
		{"deploy --help prints usage to stdout", []string{"site", "deploy", "--help"}, 0, "allod site deploy [--config <path>] [--dry-run]", "", true},
		{"check --help prints usage to stdout", []string{"site", "check", "--help"}, 0, "allod site check [--config <path>]", "", true},
		{"config --help prints usage to stdout", []string{"site", "config", "--help"}, 0, "allod site config [--config <path>] [--force]", "", true},
		{"config help discovers updates", []string{"site", "config", "update", "--help"}, 0, "allod site config update {host|user|password}", "", true},
		{"help states update needs a stanza", []string{"site", "--help"}, 0, "Update requires an existing 'shared' stanza", "", true},
		{"help also lists preview", []string{"site", "--help"}, 0, "allod site preview ", "", true},
		{"unknown command", []string{"site", "publish"}, 1, "", "unknown site command: publish", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, errText, code := runAllod(t, test.args...)
			if code != test.code {
				t.Errorf("exit code = %d, want %d", code, test.code)
			}
			if test.outHas != "" && !strings.Contains(out, test.outHas) {
				t.Errorf("stdout does not contain %q\ngot: %q", test.outHas, out)
			}
			if test.errHas != "" && !strings.Contains(errText, test.errHas) {
				t.Errorf("stderr does not contain %q\ngot: %q", test.errHas, errText)
			}
			if test.errIsEmpty && errText != "" {
				t.Errorf("stderr = %q, want empty", errText)
			}
		})
	}
}

// siteDetailOnlySentences names one sentence from each command's detail
// prose that appears nowhere in any Usage: or Commands: line, so its
// presence or absence distinguishes the short usage from the long one.
var siteDetailOnlySentences = map[string]string{
	"deploy":  "deployment-owned hosting layout compiled in",
	"check":   "A rejected FTP login can identify only the username or password",
	"config":  "Passwords are not echoed as they are typed",
	"preview": "mirror zola's own flags of the same names",
}

// TestSiteBareInvocationHasNoDetailProse pins the short-usage contract: a
// bare 'allod site' prints only the Usage: and Commands: blocks and the
// pointer to '--help', never any command's detail prose, so an operator who
// mistypes a command gets one screen, not the whole manual.
func TestSiteBareInvocationHasNoDetailProse(t *testing.T) {
	out, errText, code := runAllod(t, "site")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
	for command, sentence := range siteDetailOnlySentences {
		if strings.Contains(errText, sentence) {
			t.Errorf("bare invocation printed %s's detail prose %q\ngot: %q", command, sentence, errText)
		}
	}
	if want := "\nRun 'allod site --help' for details.\n"; !strings.HasSuffix(errText, want) {
		t.Errorf("bare invocation stderr does not end with %q\ngot: %q", want, errText)
	}
}

// TestSiteHelpIncludesDetailProse is the other half of the contract above:
// 'allod site --help' prints every command's detail prose, which is exactly
// what the bare invocation must not.
func TestSiteHelpIncludesDetailProse(t *testing.T) {
	out, errText, code := runAllod(t, "site", "--help")
	if code != 0 || errText != "" {
		t.Fatalf("exit=%d stderr=%q, want success with empty stderr", code, errText)
	}
	for command, sentence := range siteDetailOnlySentences {
		if !strings.Contains(out, sentence) {
			t.Errorf("'site --help' is missing %s's detail prose %q\ngot: %q", command, sentence, out)
		}
	}
}

// TestSiteCommandHelpMentionsOnlyItsOwnCommand pins the per-command help
// contract: 'allod site <command> --help' prints that command's own usage
// lines and detail, and none of any other command's own usage line — its
// exact registered 'allod site <name> ...' syntax line, not merely the words
// 'allod site <name>', since a command's own detail may legitimately point
// the reader at another command by its full invocation, e.g. check's detail
// naming 'allod site config update user'.
func TestSiteCommandHelpMentionsOnlyItsOwnCommand(t *testing.T) {
	ownUsage := map[string]string{
		"preview": "allod site preview [--port <n>] [--interface <addr>] [--base-url <url>] [--drafts] [--open] [-- <zola args>...]",
		"deploy":  "allod site deploy [--config <path>] [--dry-run]",
		"check":   "allod site check [--config <path>]",
		"config":  "allod site config [--config <path>] [--force]",
	}
	for command, usage := range ownUsage {
		t.Run(command, func(t *testing.T) {
			out, errText, code := runAllod(t, "site", command, "--help")
			if code != 0 || errText != "" {
				t.Fatalf("exit=%d stderr=%q, want success with empty stderr", code, errText)
			}
			if !strings.Contains(out, usage) {
				t.Errorf("'site %s --help' does not contain its own usage %q\ngot: %q", command, usage, out)
			}
			if sentence := siteDetailOnlySentences[command]; !strings.Contains(out, sentence) {
				t.Errorf("'site %s --help' does not contain its own detail prose %q\ngot: %q", command, sentence, out)
			}
			for other, otherUsage := range ownUsage {
				if other == command {
					continue
				}
				if strings.Contains(out, otherUsage) {
					t.Errorf("'site %s --help' mentions %s's usage %q\ngot: %q", command, other, otherUsage, out)
				}
				if sentence := siteDetailOnlySentences[other]; strings.Contains(out, sentence) {
					t.Errorf("'site %s --help' mentions %s's detail prose %q\ngot: %q", command, other, sentence, out)
				}
			}
		})
	}
}

// TestSiteArgumentErrorPrintsOwnUsageOnly pins the per-command
// argument-error contract: an unknown option, an unexpected argument, or a
// missing flag value prints the one-line message, that command's own
// Usage: lines, and a pointer to that command's own '--help', but never any
// command's detail prose — not even that command's own. gh shows the reader
// the options next to the mistake, not the whole manual.
func TestSiteArgumentErrorPrintsOwnUsageOnly(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		message  string
		ownUsage string
		pointer  string
	}{
		{
			"deploy unknown option",
			[]string{"site", "deploy", "--bogus"},
			"allod: unknown option for site deploy: --bogus\n",
			"Usage:\n  allod site deploy ",
			"\nRun 'allod site deploy --help' for details.\n",
		},
		{
			"check unexpected argument",
			[]string{"site", "check", "extra"},
			"allod: unexpected argument for site check: extra\n",
			"Usage:\n  allod site check ",
			"\nRun 'allod site check --help' for details.\n",
		},
		{
			"config unknown option",
			[]string{"site", "config", "--bogus"},
			"allod: unknown option for site config: --bogus\n",
			"Usage:\n  allod site config ",
			"\nRun 'allod site config --help' for details.\n",
		},
		{
			"preview missing flag value",
			[]string{"site", "preview", "--port"},
			"allod: --port requires a value\n",
			"Usage:\n  allod site preview ",
			"\nRun 'allod site preview --help' for details.\n",
		},
		{
			"deploy --config missing value",
			[]string{"site", "deploy", "--config"},
			"allod: --config requires a path for site deploy\n",
			"Usage:\n  allod site deploy ",
			"\nRun 'allod site deploy --help' for details.\n",
		},
		{
			"config --force specified twice",
			[]string{"site", "config", "--force", "--force"},
			"allod: --force may only be specified once for site config\n",
			"Usage:\n  allod site config ",
			"\nRun 'allod site config --help' for details.\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &deployStub{}
			useDeployStub(t, stub)
			useSiteRepo(t, "domain = \"example.com\"\n")

			out, errText, code := runAllod(t, test.args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			for _, want := range []string{test.message, test.ownUsage, test.pointer} {
				if !strings.Contains(errText, want) {
					t.Errorf("argument error does not contain %q\ngot: %q", want, errText)
				}
			}
			// No command's detail prose belongs in an argument error, not
			// even the offending command's own: the point of the short
			// error form is that it fits without any of it.
			for command, sentence := range siteDetailOnlySentences {
				if strings.Contains(errText, sentence) {
					t.Errorf("argument error printed %s's detail prose %q\ngot: %q", command, sentence, errText)
				}
			}
		})
	}
}

// TestSiteSharedDetailAppearsWhereItApplies pins the '--config <path>'
// shared-detail contract: deploy, check, and config each carry it in their
// own '--help', it appears exactly once in the long-form 'site --help', and
// preview — which takes no '--config' — carries it nowhere.
func TestSiteSharedDetailAppearsWhereItApplies(t *testing.T) {
	const marker = "accepted by deploy, check, and config"

	for _, command := range []string{"deploy", "check", "config"} {
		t.Run(command, func(t *testing.T) {
			out, errText, code := runAllod(t, "site", command, "--help")
			if code != 0 || errText != "" {
				t.Fatalf("exit=%d stderr=%q, want success with empty stderr", code, errText)
			}
			if !strings.Contains(out, marker) {
				t.Errorf("'site %s --help' is missing the shared '--config' detail\ngot: %q", command, out)
			}
		})
	}

	t.Run("preview", func(t *testing.T) {
		out, errText, code := runAllod(t, "site", "preview", "--help")
		if code != 0 || errText != "" {
			t.Fatalf("exit=%d stderr=%q, want success with empty stderr", code, errText)
		}
		if strings.Contains(out, marker) {
			t.Errorf("'site preview --help' carries the '--config' detail it does not accept\ngot: %q", out)
		}
	})

	t.Run("long form once", func(t *testing.T) {
		out, errText, code := runAllod(t, "site", "--help")
		if code != 0 || errText != "" {
			t.Fatalf("exit=%d stderr=%q, want success with empty stderr", code, errText)
		}
		if got := strings.Count(out, marker); got != 1 {
			t.Errorf("'site --help' contains the shared '--config' detail %d times, want 1\ngot: %q", got, out)
		}
	})
}

func TestSiteUsageKeepsHostingSelectionAtBuildTime(t *testing.T) {
	out, errText, code := runAllod(t, "site", "--help")
	if code != 0 || errText != "" {
		t.Fatalf("help: exit=%d stderr=%q, want success with empty stderr", code, errText)
	}
	for _, want := range []string{
		"deployment-owned hosting layout compiled in",
		"-X main.siteHostingProfileName=public-html",
		"No flag, environment variable, or site.toml key",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help does not contain %q\ngot: %q", want, out)
		}
	}
	if strings.Contains(out, "ALLOD_SITE_HOSTING_PROFILE") {
		t.Errorf("help still advertises runtime environment selection: %q", out)
	}
}

// Check is exactly the authenticated preflight made available on its own. It
// needs no site repository and has no path to build, sync, verify, or publish.
func TestSiteCheckDoesNothingButProbeTheRemote(t *testing.T) {
	stub := &deployStub{}
	useDeployStub(t, stub)

	out, errText, code := runAllod(t, "site", "check")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if out != "Remote 'shared' is ready.\n" || errText != "" {
		t.Errorf("stdout=%q stderr=%q, want one safe success line", out, errText)
	}
	if stub.remoteCalls != 1 || stub.remoteConfigPath != "" {
		t.Errorf("remote check calls=%d config=%q, want one default-config probe", stub.remoteCalls, stub.remoteConfigPath)
	}
	if stub.buildCalls != 0 || stub.syncCalls != 0 || stub.verifyCalls != 0 {
		t.Errorf("check had deploy effects: build=%d sync=%d verify=%d", stub.buildCalls, stub.syncCalls, stub.verifyCalls)
	}
}

// An explicit path reaches the shared probe unchanged. The file content is a
// plaintext-equivalent credential, so neither stream may reveal any of it.
func TestSiteCheckPropagatesExplicitConfigWithoutReadingItAloud(t *testing.T) {
	stub := &deployStub{}
	useDeployStub(t, stub)
	configPath := filepath.Join(t.TempDir(), "runtime credentials", "rclone.conf")
	credential := "[shared]\npass = NEVER-PRINT-THIS\n"
	writeExistingConfig(t, configPath, credential)

	out, errText, code := runAllod(t, "site", "check", "--config", configPath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if stub.remoteCalls != 1 || stub.remoteConfigPath != configPath {
		t.Errorf("remote check calls=%d config=%q, want one probe with %q", stub.remoteCalls, stub.remoteConfigPath, configPath)
	}
	if strings.Contains(out, "NEVER-PRINT-THIS") || strings.Contains(errText, "NEVER-PRINT-THIS") {
		t.Errorf("check exposed config content: stdout=%q stderr=%q", out, errText)
	}
	if stub.buildCalls != 0 || stub.syncCalls != 0 || stub.verifyCalls != 0 {
		t.Errorf("check had deploy effects: build=%d sync=%d verify=%d", stub.buildCalls, stub.syncCalls, stub.verifyCalls)
	}
}

func TestSiteCheckReports530AsUsernameOrPassword(t *testing.T) {
	stub := &deployStub{remoteResult: siteRemoteCheckResult{problem: siteRemoteCredentialRejected, status: 4}}
	useDeployStub(t, stub)

	out, errText, code := runAllod(t, "site", "check")
	if code != 4 {
		t.Errorf("exit code = %d, want rclone status 4", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
	for _, want := range []string{"username or password", "site config update user", "site config update password"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr does not contain %q: %q", want, errText)
		}
	}
	if strings.Contains(errText, "the password was rejected") {
		t.Errorf("530 was over-classified as a password failure: %q", errText)
	}
	if stub.buildCalls != 0 || stub.syncCalls != 0 || stub.verifyCalls != 0 {
		t.Errorf("failed check had deploy effects: build=%d sync=%d verify=%d", stub.buildCalls, stub.syncCalls, stub.verifyCalls)
	}
}

func TestSiteCheckRejectedSymlinkPointsToItsSourceCredential(t *testing.T) {
	stub := &deployStub{remoteResult: siteRemoteCheckResult{problem: siteRemoteCredentialRejected, status: 4}}
	useDeployStub(t, stub)
	dir := t.TempDir()
	target := filepath.Join(dir, "generation-1.conf")
	selected := filepath.Join(dir, "active.conf")
	writeExistingConfig(t, target, "[shared]\npass = NEVER-PRINT-THIS\n")
	if err := os.Symlink(target, selected); err != nil {
		t.Fatalf("could not create config symlink: %v", err)
	}

	out, errText, code := runAllod(t, "site", "check", "--config", selected)
	if code != 4 || out != "" {
		t.Errorf("rejected symlink: exit=%d stdout=%q stderr=%q", code, out, errText)
	}
	for _, want := range []string{"source credential behind that read-only symlink", "allod site check", "same --config path"} {
		if !strings.Contains(errText, want) {
			t.Errorf("symlink remedy does not contain %q: %q", want, errText)
		}
	}
	if strings.Contains(errText, "site config update") {
		t.Errorf("symlink remedy recommends a refused mutation: %q", errText)
	}
	if strings.Contains(out+errText, "NEVER-PRINT-THIS") {
		t.Errorf("symlink remedy exposed config content: stdout=%q stderr=%q", out, errText)
	}
	if stub.remoteCalls != 1 || stub.remoteConfigPath != selected {
		t.Errorf("remote check calls=%d config=%q, want one probe with %q", stub.remoteCalls, stub.remoteConfigPath, selected)
	}
	if stub.buildCalls != 0 || stub.syncCalls != 0 || stub.verifyCalls != 0 {
		t.Errorf("failed symlink check had deploy effects: build=%d sync=%d verify=%d", stub.buildCalls, stub.syncCalls, stub.verifyCalls)
	}
}

func TestSiteCheckRejectedDefaultSymlinkPointsToItsSourceCredential(t *testing.T) {
	tests := []struct {
		name     string
		selected func(t *testing.T) string
	}{
		{
			"RCLONE_CONFIG",
			func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "active.conf")
				t.Setenv("RCLONE_CONFIG", path)
				return path
			},
		},
		{
			"XDG default",
			func(t *testing.T) string {
				base := t.TempDir()
				t.Setenv("RCLONE_CONFIG", "")
				t.Setenv("XDG_CONFIG_HOME", base)
				path := filepath.Join(base, "rclone", "rclone.conf")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatalf("could not create default config directory: %v", err)
				}
				return path
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &deployStub{remoteResult: siteRemoteCheckResult{problem: siteRemoteCredentialRejected, status: 4}}
			useDeployStub(t, stub)
			selected := test.selected(t)
			target := filepath.Join(t.TempDir(), "generation-1.conf")
			writeExistingConfig(t, target, "[shared]\npass = NEVER-PRINT-THIS\n")
			if err := os.Symlink(target, selected); err != nil {
				t.Fatalf("could not create default config symlink: %v", err)
			}

			out, errText, code := runAllod(t, "site", "check")
			if code != 4 || out != "" {
				t.Errorf("rejected default symlink: exit=%d stdout=%q stderr=%q", code, out, errText)
			}
			for _, want := range []string{"source credential behind rclone's read-only configuration symlink", "allod site check"} {
				if !strings.Contains(errText, want) {
					t.Errorf("default-symlink remedy does not contain %q: %q", want, errText)
				}
			}
			for _, refused := range []string{"site config update", "--config", selected, "NEVER-PRINT-THIS"} {
				if strings.Contains(out+errText, refused) {
					t.Errorf("default-symlink remedy contains %q: stdout=%q stderr=%q", refused, out, errText)
				}
			}
			if stub.remoteCalls != 1 || stub.remoteConfigPath != "" {
				t.Errorf("remote check calls=%d config=%q, want one implicit-config probe", stub.remoteCalls, stub.remoteConfigPath)
			}
			if stub.buildCalls != 0 || stub.syncCalls != 0 || stub.verifyCalls != 0 {
				t.Errorf("failed default-symlink check had deploy effects: build=%d sync=%d verify=%d", stub.buildCalls, stub.syncCalls, stub.verifyCalls)
			}
		})
	}
}

func TestSiteCheckRejectsUnexpectedArguments(t *testing.T) {
	for _, args := range [][]string{
		{"site", "check", "shared"},
		{"site", "check", "--publish"},
		{"site", "check", "--config"},
		{"site", "check", "--config", "/one", "--config", "/two"},
	} {
		t.Run(strings.Join(args[2:], " "), func(t *testing.T) {
			stub := &deployStub{}
			useDeployStub(t, stub)
			_, errText, code := runAllod(t, args...)
			if code != 1 || !strings.Contains(errText, "site check") {
				t.Errorf("exit=%d stderr=%q, want a site-check parse failure", code, errText)
			}
			if stub.remoteCalls != 0 || stub.buildCalls != 0 || stub.syncCalls != 0 || stub.verifyCalls != 0 {
				t.Errorf("bad arguments had effects: probe=%d build=%d sync=%d verify=%d", stub.remoteCalls, stub.buildCalls, stub.syncCalls, stub.verifyCalls)
			}
		})
	}
}

// TestSiteDeployTakesNoTarget pins the safety property: nothing on the command
// line may name a domain, a docroot, or a remote, because one hosting account
// owns every docroot and a redirected sync would delete a sibling site. If a
// future option makes any of these parse, it must not be one that redirects
// the deploy.
func TestSiteDeployTakesNoTarget(t *testing.T) {
	rejected := [][]string{
		{"site", "deploy", "--domain", "other.example"},
		{"site", "deploy", "--docroot", "shared:domains/other.example/public_html"},
		{"site", "deploy", "--remote", "elsewhere"},
		{"site", "deploy", "--target", "other.example"},
		{"site", "deploy", "other.example"},
		{"site", "deploy", "shared:domains/other.example/public_html"},
		{"site", "deploy", "--config"},
		{"site", "deploy", "--config", "--dry-run"},
		{"site", "deploy", "--config="},
		{"site", "deploy", "--config=/run/credential\nallod: forged"},
		{"site", "deploy", "--config", "/one", "--config", "/two"},
	}

	for _, args := range rejected {
		t.Run(strings.Join(args[2:], " "), func(t *testing.T) {
			stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
			useDeployStub(t, stub)
			useSiteRepo(t, "domain = \"example.com\"\n")

			_, errText, code := runAllod(t, args...)
			if code == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			if !strings.Contains(errText, "site deploy") {
				t.Errorf("stderr does not name the command\ngot: %q", errText)
			}
			if stub.syncCalls != 0 {
				t.Errorf("rclone ran %d times, want 0", stub.syncCalls)
			}
		})
	}
}

// --- Configuration ---

func TestFindSiteRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "src", "content")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("could not create %s: %v", nested, err)
	}
	if _, ok := findSiteRoot(nested); ok {
		t.Errorf("found a site root with no %s present", siteConfigName)
	}
	if err := os.WriteFile(filepath.Join(root, siteConfigName), []byte("domain = \"a.example\"\n"), 0644); err != nil {
		t.Fatalf("could not write %s: %v", siteConfigName, err)
	}
	found, ok := findSiteRoot(nested)
	if !ok {
		t.Fatalf("no site root found from %s", nested)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("could not resolve %s: %v", root, err)
	}
	if got, err := filepath.EvalSymlinks(found); err != nil || got != want {
		t.Errorf("site root = %q, want %q", found, want)
	}
}

func TestSiteDeployWithoutConfig(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	// A directory with no site.toml above it: t.TempDir sits under the system
	// temp dir, which no site repository owns.
	t.Chdir(t.TempDir())

	_, errText, code := runAllod(t, "site", "deploy")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{"no site.toml", "to fix:"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
		}
	}
	if stub.syncCalls != 0 {
		t.Errorf("rclone ran %d times, want 0", stub.syncCalls)
	}
}

func TestParseSiteConfig(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		domain string
		errHas string
	}{
		{"the one key", "domain = \"example.com\"\n", "example.com", ""},
		{"no trailing newline", "domain = \"example.com\"", "example.com", ""},
		{"no spaces around equals", "domain=\"example.com\"\n", "example.com", ""},
		{"leading whitespace", "\t  domain = \"example.com\"\n", "example.com", ""},
		{"comments and blank lines", "# the site\n\ndomain = \"example.com\" # inline\n\n", "example.com", ""},
		{"literal string", "domain = 'example.com'\n", "example.com", ""},
		// Forward compatibility: a file written for a later version of this
		// command still deploys with this one.
		{"unknown scalar key", "domain = \"example.com\"\nredirect_www = true\n", "example.com", ""},
		{"unknown key before domain", "cache_seconds = 300\ndomain = \"example.com\"\n", "example.com", ""},
		{"unknown key with an array value", "domain = \"example.com\"\nheaders = [\"a\", \"b\"]\n", "example.com", ""},
		{"unknown table", "domain = \"example.com\"\n\n[redirects]\nold = \"new\"\n", "example.com", ""},
		{"unknown table with its own domain", "domain = \"example.com\"\n\n[staging]\ndomain = \"staging.example.com\"\n", "example.com", ""},
		// Errors, all about the one key that is read.
		{"empty file", "", "", "no domain key"},
		{"only comments", "# nothing here\n", "", "no domain key"},
		{"domain only under a table", "[staging]\ndomain = \"staging.example.com\"\n", "", "no domain key"},
		{"unquoted", "domain = example.com\n", "", "must be a quoted string"},
		{"unterminated", "domain = \"example.com\n", "", "must be a quoted string"},
		{"trailing junk after the string", "domain = \"example.com\" oops\n", "", "must be a quoted string"},
		{"set twice", "domain = \"a.example\"\ndomain = \"b.example\"\n", "", "set more than once"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := parseSiteConfig(test.text)
			if test.errHas != "" {
				if err == nil {
					t.Fatalf("error = nil, want one containing %q", test.errHas)
				}
				if !strings.Contains(err.Error(), test.errHas) {
					t.Errorf("error = %q, want one containing %q", err, test.errHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
			if config.domain != test.domain {
				t.Errorf("domain = %q, want %q", config.domain, test.domain)
			}
		})
	}
}

// TestValidDomain covers the check that keeps a site.toml from aiming the sync
// somewhere other than its own docroot.
func TestValidDomain(t *testing.T) {
	valid := []string{
		"example.com",
		"www.example.com",
		"a-b.example.co.uk",
		"xn--80ak6aa92e.com",
		"123.example",
	}
	invalid := []string{
		"",
		"localhost",
		"example.com/../other.example",
		"example.com/public_html",
		"../other.example",
		"..",
		"shared:domains/other.example",
		"example..com",
		".example.com",
		"example.com.",
		"-example.com",
		"example-.com",
		"hash pool.dev",
		"example.com\nother.example",
		"example.com\x00",
		"*.example.com",
		strings.Repeat("a", 64) + ".dev",
		strings.Repeat("a.", 130) + "dev",
	}

	for _, domain := range valid {
		if !validDomain(domain) {
			t.Errorf("validDomain(%q) = false, want true", domain)
		}
	}
	for _, domain := range invalid {
		if validDomain(domain) {
			t.Errorf("validDomain(%q) = true, want false", domain)
		}
	}
}

func TestSiteDeployRejectsInvalidDomain(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	useSiteRepo(t, "domain = \"example.com/../other.example\"\n")

	_, errText, code := runAllod(t, "site", "deploy")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errText, "invalid domain") {
		t.Errorf("stderr does not contain %q\ngot: %q", "invalid domain", errText)
	}
	if stub.syncCalls != 0 {
		t.Errorf("rclone ran %d times, want 0", stub.syncCalls)
	}
}

// --- The filter list ---

// TestDirectAdminProfileData pins both values in the default hosting profile.
// Losing a filter entry is the failure this command exists to prevent, and
// /.well-known/** is the one whose loss shows up weeks later as an expired
// certificate rather than as a broken deploy.
func TestDirectAdminProfileData(t *testing.T) {
	useSiteHostingProfile(t, defaultSiteHostingProfile)
	profile := selectedSiteHostingProfile()
	if profile.name != "directadmin" {
		t.Fatalf("default profile = %q, want directadmin", profile.name)
	}
	if got, want := profile.docroot("example.com"), "shared:domains/example.com/public_html"; got != want {
		t.Errorf("docroot = %q, want %q", got, want)
	}
	want := "- /.well-known/**\n- /.htaccess\n- /stats/**\n- /cgi-bin/**\n"
	if got := deployFilterText(profile.deployFilterRules); got != want {
		t.Errorf("filter text =\n%q\nwant\n%q", got, want)
	}
}

// An inherited environment cannot change the destructive sync target. Profile
// selection belongs to the package that built the site-enabled binary.
func TestRuntimeEnvironmentCannotSelectHostingProfile(t *testing.T) {
	useSiteHostingProfile(t, defaultSiteHostingProfile)
	t.Setenv("ALLOD_SITE_HOSTING_PROFILE", "public-html")
	if got := selectedSiteHostingProfile().name; got != "directadmin" {
		t.Errorf("profile = %q, want directadmin", got)
	}
}

func TestValidateSiteHostingProfiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]siteHostingProfile) []siteHostingProfile
		errHas string
	}{
		{"built-ins", func(profiles []siteHostingProfile) []siteHostingProfile { return profiles }, ""},
		{"empty table", func([]siteHostingProfile) []siteHostingProfile { return nil }, "no profiles are defined"},
		{"invalid name", func(profiles []siteHostingProfile) []siteHostingProfile {
			profiles[1].name = "public_html"
			return profiles
		}, "invalid profile name"},
		{"duplicate name", func(profiles []siteHostingProfile) []siteHostingProfile {
			profiles[1].name = profiles[0].name
			return profiles
		}, "defined more than once"},
		{"missing default", func(profiles []siteHostingProfile) []siteHostingProfile {
			profiles[0].name = "another"
			return profiles
		}, "default profile"},
		{"missing placeholder", func(profiles []siteHostingProfile) []siteHostingProfile {
			profiles[0].docrootPattern = "domains/public_html"
			return profiles
		}, "unsafe docroot pattern"},
		{"two placeholders", func(profiles []siteHostingProfile) []siteHostingProfile {
			profiles[0].docrootPattern = "domains/<domain>/<domain>"
			return profiles
		}, "unsafe docroot pattern"},
		{"absolute docroot", func(profiles []siteHostingProfile) []siteHostingProfile {
			profiles[0].docrootPattern = "/domains/<domain>"
			return profiles
		}, "unsafe docroot pattern"},
		{"empty segment", func(profiles []siteHostingProfile) []siteHostingProfile {
			profiles[0].docrootPattern = "domains//<domain>"
			return profiles
		}, "unsafe docroot pattern"},
		{"dot segment", func(profiles []siteHostingProfile) []siteHostingProfile {
			profiles[0].docrootPattern = "domains/./<domain>"
			return profiles
		}, "unsafe docroot pattern"},
		{"parent segment", func(profiles []siteHostingProfile) []siteHostingProfile {
			profiles[0].docrootPattern = "domains/../<domain>"
			return profiles
		}, "unsafe docroot pattern"},
		{"docroot control", func(profiles []siteHostingProfile) []siteHostingProfile {
			profiles[0].docrootPattern = "domains\n/<domain>"
			return profiles
		}, "unsafe docroot pattern"},
		{"inclusion rule", func(profiles []siteHostingProfile) []siteHostingProfile {
			profiles[0].deployFilterRules[0] = "+ /.well-known/**"
			return profiles
		}, "invalid exclusion rule"},
		{"multiline exclusion", func(profiles []siteHostingProfile) []siteHostingProfile {
			profiles[0].deployFilterRules[0] = "- /.well-known/**\n- /**"
			return profiles
		}, "invalid exclusion rule"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profiles := test.mutate(cloneSiteHostingProfiles(siteHostingProfiles))
			err := validateSiteHostingProfiles(profiles)
			if test.errHas == "" {
				if err != nil {
					t.Fatalf("validateSiteHostingProfiles() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.errHas) {
				t.Errorf("validateSiteHostingProfiles() = %v, want error containing %q", err, test.errHas)
			}
		})
	}
}

// --- Deploying ---

// TestSiteDeployDirectAdminDefaultIsUnchanged is the generated-command
// regression for deployments that do not select a profile.
func TestSiteDeployDirectAdminDefaultIsUnchanged(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	root := useSiteRepo(t, "# the site\ndomain = \"example.com\"\n")

	out, errText, code := runAllod(t, "site", "deploy")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if errText != "" {
		t.Errorf("stderr = %q, want empty", errText)
	}

	if built, err := filepath.EvalSymlinks(stub.buildDir); err != nil || built != root {
		t.Errorf("nix build ran in %q, want %q", stub.buildDir, root)
	}
	if stub.syncCalls != 1 {
		t.Fatalf("rclone ran %d times, want 1", stub.syncCalls)
	}
	if stub.remoteConfigPath != "" || stub.syncConfigPath != "" {
		t.Errorf("default deploy selected an explicit rclone config: preflight=%q sync=%q", stub.remoteConfigPath, stub.syncConfigPath)
	}

	filter := argAfter(t, stub.syncArgs, "--filter-from")
	want := []string{
		"sync", "/nix/store/aaa-site", "shared:domains/example.com/public_html",
		"--filter-from", filter,
		"--backup-dir", "shared:deploy-trash/example.com",
		"--verbose",
		"--transfers", "2",
		"--checkers", "2",
		"--ftp-concurrency", "5",
	}
	if fmt.Sprint(stub.syncArgs) != fmt.Sprint(want) {
		t.Errorf("rclone args =\n%v\nwant\n%v", stub.syncArgs, want)
	}
	for _, arg := range stub.syncArgs {
		if arg == "--dry-run" {
			t.Errorf("rclone got --dry-run without the flag: %v", stub.syncArgs)
		}
	}

	if stub.verifyCalls != 1 {
		t.Errorf("verification ran %d times, want 1", stub.verifyCalls)
	}
	if stub.verifyURL != "https://example.com/" {
		t.Errorf("verified %q, want %q", stub.verifyURL, "https://example.com/")
	}
	for _, want := range []string{
		"Domain: example.com\n",
		"Source: /nix/store/aaa-site\n",
		"Target: shared:domains/example.com/public_html\n",
		"Verified: https://example.com/ returned 200\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not contain %q\ngot: %q", want, out)
		}
	}

	// The filter is a per-run temporary file, and the run cleans it up.
	if _, err := os.Stat(filter); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("filter file %s survived the run (err = %v)", filter, err)
	}
}

// A named config gives both rclone calls one path instead of letting either
// resolve a default independently. It deliberately does not promise that the
// contents remain one snapshot across the build; the test below pins that
// rotation boundary separately.
func TestSiteDeployUsesNamedRcloneConfigThroughout(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")

	configPath := filepath.Join(t.TempDir(), "runtime credentials", "rclone.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatalf("could not create config directory: %v", err)
	}
	secret := "pass = plaintext-equivalent-test-value\n"
	if err := os.WriteFile(configPath, []byte("[shared]\n"+secret), 0400); err != nil {
		t.Fatalf("could not write named config: %v", err)
	}

	out, errText, code := runAllod(t, "site", "deploy", "--dry-run", "--config", configPath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	for operation, selectedPath := range map[string]string{
		"preflight": stub.remoteConfigPath,
		"sync":      stub.syncConfigPath,
	} {
		if selectedPath != configPath {
			t.Errorf("%s config = %q, want %q", operation, selectedPath, configPath)
		}
	}
	for stream, text := range map[string]string{"stdout": out, "stderr": errText} {
		if strings.Contains(text, "plaintext-equivalent-test-value") {
			t.Errorf("%s contains the credential from the named file: %q", stream, text)
		}
	}
}

// Rclone opens the selected path once for the preflight and again for sync.
// Activation may rotate the file during the intervening build, so the contract
// is path identity rather than content identity: sync can see a newer value.
func TestSiteDeployNamedConfigCanRotateDuringBuild(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")
	configPath := filepath.Join(t.TempDir(), "rclone.conf")
	if err := os.WriteFile(configPath, []byte("generation-one"), 0400); err != nil {
		t.Fatalf("could not write first config generation: %v", err)
	}

	var preflightContents, syncContents string
	siteRemoteCheck = func(path string) siteRemoteCheckResult {
		preflightContents = readFile(t, path)
		return siteRemoteCheckResult{problem: siteRemoteReady}
	}
	siteBuild = func(string) (string, int) {
		next := configPath + ".next"
		if err := os.WriteFile(next, []byte("generation-two"), 0400); err != nil {
			t.Fatalf("could not rotate config during build: %v", err)
		}
		if err := os.Rename(next, configPath); err != nil {
			t.Fatalf("could not activate rotated config during build: %v", err)
		}
		return stub.storePath, 0
	}
	siteSync = func(path string, _ []string) int {
		syncContents = readFile(t, path)
		return 0
	}

	if _, errText, code := runAllod(t, "site", "deploy", "--dry-run", "--config", configPath); code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if preflightContents != "generation-one" || syncContents != "generation-two" {
		t.Errorf("config generations: preflight=%q sync=%q", preflightContents, syncContents)
	}
}

func TestSiteDeployAcceptsSymlinkToRegularNamedConfig(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")
	dir := t.TempDir()
	target := filepath.Join(dir, "generation-1.conf")
	selected := filepath.Join(dir, "active.conf")
	if err := os.WriteFile(target, []byte("[shared]\n"), 0400); err != nil {
		t.Fatalf("could not write activation target: %v", err)
	}
	if err := os.Symlink(target, selected); err != nil {
		t.Fatalf("could not create activation symlink: %v", err)
	}

	if _, errText, code := runAllod(t, "site", "deploy", "--dry-run", "--config", selected); code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if stub.remoteConfigPath != selected || stub.syncConfigPath != selected {
		t.Errorf("selected symlink was resolved or lost: preflight=%q sync=%q", stub.remoteConfigPath, stub.syncConfigPath)
	}
}

func TestSiteDeployNamedConfigFIFODoesNotBlock(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")
	configPath := filepath.Join(t.TempDir(), "rclone.conf")
	if err := syscall.Mkfifo(configPath, 0600); err != nil {
		t.Fatalf("could not create config FIFO: %v", err)
	}

	type result struct {
		errText string
		code    int
	}
	done := make(chan result, 1)
	go func() {
		_, errText, code := runAllod(t, "site", "deploy", "--config", configPath)
		done <- result{errText: errText, code: code}
	}()

	var got result
	select {
	case got = <-done:
	case <-time.After(time.Second):
		// Release a regressed blocking read before failing so the test leaves no
		// goroutine holding the shared CLI harness state.
		writer, err := os.OpenFile(configPath, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = writer.Close()
		}
		got = <-done
		t.Fatalf("deploy blocked while opening a FIFO (eventual stderr: %s)", got.errText)
	}
	if got.code != 1 || !strings.Contains(got.errText, "not a regular file") {
		t.Errorf("FIFO refusal: code=%d stderr=%q", got.code, got.errText)
	}
	if stub.remoteCalls != 0 || stub.buildCalls != 0 || stub.syncCalls != 0 {
		t.Errorf("work ran with a FIFO config: remote-check=%d build=%d sync=%d",
			stub.remoteCalls, stub.buildCalls, stub.syncCalls)
	}
}

// A missing declarative credential is a setup failure, not an rclone failure
// after a build. The message names both ways forward and no child operation
// starts.
func TestSiteDeployRejectsMissingNamedRcloneConfig(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")
	configPath := filepath.Join(t.TempDir(), "not-materialised.conf")

	_, errText, code := runAllod(t, "site", "deploy", "--config", configPath)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{"does not exist", configPath, "materialise", "omit --config"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
		}
	}
	if stub.remoteCalls != 0 || stub.buildCalls != 0 || stub.syncCalls != 0 {
		t.Errorf("work ran with a missing named config: remote-check=%d build=%d sync=%d",
			stub.remoteCalls, stub.buildCalls, stub.syncCalls)
	}
}

func TestSiteDeployRejectsUnreadableNamedRcloneConfig(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")
	configPath := filepath.Join(t.TempDir(), "unreadable.conf")
	if err := os.WriteFile(configPath, []byte("[shared]\npass = secret\n"), 0600); err != nil {
		t.Fatalf("could not write named config: %v", err)
	}
	if err := os.Chmod(configPath, 0000); err != nil {
		t.Fatalf("could not make named config unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(configPath, 0600) })
	if file, err := os.Open(configPath); err == nil {
		file.Close()
		t.Skip("test account can still open a mode-000 file")
	}

	_, errText, code := runAllod(t, "site", "deploy", "--config", configPath)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{"not readable", configPath} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
		}
	}
	if strings.Contains(errText, "pass = secret") {
		t.Errorf("stderr contains config contents: %q", errText)
	}
	if stub.remoteCalls != 0 || stub.buildCalls != 0 || stub.syncCalls != 0 {
		t.Errorf("work ran with an unreadable named config: remote-check=%d build=%d sync=%d",
			stub.remoteCalls, stub.buildCalls, stub.syncCalls)
	}
}

func TestSiteDeployNamedRemoteFailureKeepsSelectedConfig(t *testing.T) {
	stub := &deployStub{
		remoteResult: siteRemoteCheckResult{problem: siteRemoteCredentialRejected, status: 3},
		storePath:    "/nix/store/aaa-site",
		verifyStatus: 200,
	}
	useDeployStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")
	configPath := filepath.Join(t.TempDir(), "rclone.conf")
	if err := os.WriteFile(configPath, []byte("[shared]\n"), 0400); err != nil {
		t.Fatalf("could not write named config: %v", err)
	}

	_, errText, code := runAllod(t, "site", "deploy", "--config", configPath)
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if !strings.Contains(errText, fmt.Sprintf("in %q", configPath)) {
		t.Errorf("credential remedy does not preserve the selected config: %q", errText)
	}
	if strings.Contains(errText, "allod site config --force") {
		t.Errorf("credential remedy redirects to the default config: %q", errText)
	}
	if stub.remoteConfigPath != configPath || stub.buildCalls != 0 || stub.syncCalls != 0 {
		t.Errorf("failure flow: preflight-path=%q build=%d sync=%d",
			stub.remoteConfigPath, stub.buildCalls, stub.syncCalls)
	}
}

func TestRcloneConfigSelectionBuildsChildArguments(t *testing.T) {
	command := []string{"sync", "/source with spaces", "shared:target"}
	tests := []struct {
		name       string
		configPath string
		want       []string
	}{
		{"rclone resolves its default", "", command},
		{
			"explicit path is one argv value",
			"-runtime config/rclone.conf",
			[]string{"--config", "-runtime config/rclone.conf", "sync", "/source with spaces", "shared:target"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := fmt.Sprint(rcloneArgs(test.configPath, command...)); got != fmt.Sprint(test.want) {
				t.Errorf("rclone args = %s, want %s", got, fmt.Sprint(test.want))
			}
		})
	}
}

// TestSiteDeployWritesTheFilter proves the list rclone reads is this command's
// list, not one the site repository supplied.
func TestSiteDeployWritesTheFilter(t *testing.T) {
	var contents string
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	previousSync := siteSync
	siteSync = func(configPath string, args []string) int {
		stub.syncConfigPath, stub.syncArgs, stub.syncCalls = configPath, args, stub.syncCalls+1
		data, err := os.ReadFile(argAfter(t, args, "--filter-from"))
		if err != nil {
			t.Errorf("could not read the filter file: %v", err)
		}
		contents = string(data)
		return 0
	}
	t.Cleanup(func() { siteSync = previousSync })
	useSiteRepo(t, "domain = \"example.com\"\n")

	if _, errText, code := runAllod(t, "site", "deploy"); code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	profile := selectedSiteHostingProfile()
	if want := deployFilterText(profile.deployFilterRules); contents != want {
		t.Errorf("filter file =\n%q\nwant\n%q", contents, want)
	}
}

// The second profile changes both pieces of hosting data while leaving the
// remote, backup directory, and verification behavior alone. The misleading
// site.toml key is ignored: profile selection belongs to the deployment.
func TestSiteDeployPublicHTMLProfile(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	useSiteHostingProfile(t, "public-html")
	useSiteRepo(t, "domain = \"example.com\"\nhosting_profile = \"directadmin\"\n")
	configPath := filepath.Join(t.TempDir(), "hosting.conf")
	if err := os.WriteFile(configPath, []byte("[shared]\n"), 0600); err != nil {
		t.Fatalf("could not write named rclone config: %v", err)
	}

	out, errText, code := runAllod(t, "site", "deploy", "--config", configPath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	want := []string{
		"sync", "/nix/store/aaa-site", "shared:public_html/example.com",
		"--backup-dir", "shared:deploy-trash/example.com",
		"--verbose",
		"--transfers", "2",
		"--checkers", "2",
		"--ftp-concurrency", "5",
	}
	if fmt.Sprint(stub.syncArgs) != fmt.Sprint(want) {
		t.Errorf("rclone args =\n%v\nwant\n%v", stub.syncArgs, want)
	}
	if hasArg(stub.syncArgs, "--filter-from") {
		t.Errorf("rclone got --filter-from for a profile with no exclusions: %v", stub.syncArgs)
	}
	if stub.remoteConfigPath != configPath || stub.syncConfigPath != configPath {
		t.Errorf("named config paths: preflight=%q sync=%q, want %q", stub.remoteConfigPath, stub.syncConfigPath, configPath)
	}
	if !strings.Contains(out, "Target: shared:public_html/example.com\n") {
		t.Errorf("stdout does not name the selected target\ngot: %q", out)
	}
	if stub.verifyCalls != 1 || stub.verifyURL != "https://example.com/" {
		t.Errorf("verification = %d calls to %q, want one call to https://example.com/", stub.verifyCalls, stub.verifyURL)
	}
}

// A bad build-time selection must fail before even the remote preflight. A
// silent DirectAdmin fallback here could sync to the wrong but valid path.
func TestSiteDeployRejectsBadBuildTimeHostingProfileBeforeEffects(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		errHas string
	}{
		{"unknown", "cpanel", "unknown build-time hosting profile"},
		{"malformed", "../public-html", "invalid build-time hosting profile"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
			useDeployStub(t, stub)
			useSiteHostingProfile(t, test.value)
			useSiteRepo(t, "domain = \"example.com\"\n")

			_, errText, code := runAllod(t, "site", "deploy")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			for _, want := range []string{test.errHas, "directadmin or public-html"} {
				if !strings.Contains(errText, want) {
					t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
				}
			}
			if stub.remoteCalls != 0 || stub.buildCalls != 0 || stub.syncCalls != 0 || stub.verifyCalls != 0 {
				t.Errorf("effects after rejected profile: remote=%d build=%d sync=%d verify=%d", stub.remoteCalls, stub.buildCalls, stub.syncCalls, stub.verifyCalls)
			}
		})
	}
}

// The profile table is code, but it still controls a destructive destination
// and filter. Treat a malformed table as unsafe input and stop before any
// authenticated remote check, build, sync, or verification.
func TestSiteDeployRejectsInvalidHostingProfileTableBeforeEffects(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	sabotaged := cloneSiteHostingProfiles(siteHostingProfiles)
	sabotaged[1].name = sabotaged[0].name
	useSiteHostingProfiles(t, sabotaged)
	useSiteRepo(t, "domain = \"example.com\"\n")

	_, errText, code := runAllod(t, "site", "deploy")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{"invalid built-in hosting profile table", "defined more than once"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
		}
	}
	if stub.remoteCalls != 0 || stub.buildCalls != 0 || stub.syncCalls != 0 || stub.verifyCalls != 0 {
		t.Errorf("effects after rejected profile table: remote=%d build=%d sync=%d verify=%d",
			stub.remoteCalls, stub.buildCalls, stub.syncCalls, stub.verifyCalls)
	}
}

func TestSiteDeployDryRun(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")

	out, errText, code := runAllod(t, "site", "deploy", "--dry-run")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if stub.syncCalls != 1 {
		t.Fatalf("rclone ran %d times, want 1", stub.syncCalls)
	}
	// --dry-run is the only difference from a real deploy's argv: the same
	// connection budget applies to the listing a dry run does.
	filter := argAfter(t, stub.syncArgs, "--filter-from")
	want := []string{
		"sync", "/nix/store/aaa-site", "shared:domains/example.com/public_html",
		"--filter-from", filter,
		"--backup-dir", "shared:deploy-trash/example.com",
		"--verbose",
		"--transfers", "2",
		"--checkers", "2",
		"--ftp-concurrency", "5",
		"--dry-run",
	}
	if fmt.Sprint(stub.syncArgs) != fmt.Sprint(want) {
		t.Errorf("rclone args =\n%v\nwant\n%v", stub.syncArgs, want)
	}
	if stub.verifyCalls != 0 {
		t.Errorf("verification ran %d times on a dry run, want 0", stub.verifyCalls)
	}
	if !strings.Contains(out, "Dry run: shared:domains/example.com/public_html was not modified\n") {
		t.Errorf("stdout does not report the dry run\ngot: %q", out)
	}
}

func TestSiteDeployBuildFailures(t *testing.T) {
	tests := []struct {
		name        string
		storePath   string
		buildStatus int
		code        int
		errHas      string
	}{
		{"non-zero exit", "", 2, 2, "nix build failed"},
		{"no output path", "", 0, 1, "printed no output path"},
		{"two output paths", "/nix/store/aaa-site\n/nix/store/bbb-site", 0, 1, "more than one output path"},
		{"relative path", "result", 0, 1, "unusable output path"},
		{"path holding a colon", "/nix/store/aaa:site", 0, 1, "unusable output path"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &deployStub{storePath: test.storePath, buildStatus: test.buildStatus, verifyStatus: 200}
			useDeployStub(t, stub)
			useSiteRepo(t, "domain = \"example.com\"\n")

			_, errText, code := runAllod(t, "site", "deploy")
			if code != test.code {
				t.Errorf("exit code = %d, want %d", code, test.code)
			}
			if !strings.Contains(errText, test.errHas) {
				t.Errorf("stderr does not contain %q\ngot: %q", test.errHas, errText)
			}
			if stub.syncCalls != 0 {
				t.Errorf("rclone ran %d times after a failed build, want 0", stub.syncCalls)
			}
		})
	}
}

func TestSiteDeploySyncFailure(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", syncStatus: 3, verifyStatus: 200}
	useDeployStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")

	_, errText, code := runAllod(t, "site", "deploy")
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if !strings.Contains(errText, "rclone sync failed") {
		t.Errorf("stderr does not contain %q\ngot: %q", "rclone sync failed", errText)
	}
	if !strings.Contains(errText, "deployment to shared:domains/example.com/public_html did not complete") {
		t.Errorf("stderr does not say the deployment did not complete\ngot: %q", errText)
	}
	if strings.Contains(errText, "partially updated") {
		t.Errorf("sync failure claims a partial update that may not have happened\ngot: %q", errText)
	}
	if stub.verifyCalls != 0 {
		t.Errorf("verification ran %d times after a failed sync, want 0", stub.verifyCalls)
	}
}

func TestSiteDeployVerificationFailure(t *testing.T) {
	tests := []struct {
		name   string
		status int
		err    error
		errHas string
	}{
		{"not found", 404, nil, "returned 404, not 200"},
		{"redirect is not a pass", 301, nil, "returned 301, not 200"},
		{"unreachable", 0, errors.New("connection refused"), "could not be reached"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: test.status, verifyErr: test.err}
			useDeployStub(t, stub)
			useSiteRepo(t, "domain = \"example.com\"\n")

			_, errText, code := runAllod(t, "site", "deploy")
			if code != siteVerifyExit {
				t.Errorf("exit code = %d, want %d", code, siteVerifyExit)
			}
			if !strings.Contains(errText, test.errHas) {
				t.Errorf("stderr does not contain %q\ngot: %q", test.errHas, errText)
			}
			// The sync did happen: the message has to say so, or an operator
			// reads the failure as "nothing was deployed".
			if !strings.Contains(errText, "deployed, but") {
				t.Errorf("stderr does not say the deploy happened\ngot: %q", errText)
			}
			if stub.syncCalls != 1 {
				t.Errorf("rclone ran %d times, want 1", stub.syncCalls)
			}
		})
	}
}

// TestProbeSite exercises the real HTTP check rather than the seam, because
// the one thing it has to get right — not following redirects — is a property
// of the client the stub never runs.
func TestProbeSite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, "hello")
		case "/moved":
			http.Redirect(w, r, "/ok", http.StatusMovedPermanently)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tests := []struct {
		path string
		code int
	}{
		{"/ok", 200},
		{"/moved", 301},
		{"/gone", 404},
	}
	for _, test := range tests {
		code, err := probeSite(server.URL + test.path)
		if err != nil {
			t.Errorf("probeSite(%s) error = %v, want nil", test.path, err)
			continue
		}
		if code != test.code {
			t.Errorf("probeSite(%s) = %d, want %d", test.path, code, test.code)
		}
	}

	server.Close()
	if _, err := probeSite(server.URL + "/ok"); err == nil {
		t.Error("probeSite on a closed server returned nil error")
	}
}

func TestSiteDeployMissingTools(t *testing.T) {
	for _, missing := range []string{"nix", "rclone"} {
		t.Run("no "+missing, func(t *testing.T) {
			stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
			binDir := useDeployStub(t, stub)
			if err := os.Remove(filepath.Join(binDir, missing)); err != nil {
				t.Fatalf("could not remove the stub %s: %v", missing, err)
			}
			useSiteRepo(t, "domain = \"example.com\"\n")

			_, errText, code := runAllod(t, "site", "deploy")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if want := "'" + missing + "' not found on PATH"; !strings.Contains(errText, want) {
				t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
			}
		})
	}
}

// A failed dry run must not claim the docroot may be partially updated. A dry
// run writes nothing, so that wording sends the reader looking for damage that
// cannot exist -- which is exactly what it did the first time a missing rclone
// remote made the sync fail.
func TestSiteDeployDryRunFailureDoesNotClaimPartialUpdate(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", syncStatus: 1, verifyStatus: 200}
	useDeployStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")

	_, errText, code := runAllod(t, "site", "deploy", "--dry-run")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if strings.Contains(errText, "partially updated") {
		t.Errorf("dry-run failure claims a partial update\ngot: %q", errText)
	}
	if !strings.Contains(errText, "was not modified") {
		t.Errorf("stderr does not contain %q\ngot: %q", "was not modified", errText)
	}
	if stub.verifyCalls != 0 {
		t.Errorf("verification ran %d times after a failed dry run, want 0", stub.verifyCalls)
	}
}

// --- The rclone remote ---

// TestSiteNamespaceIsRegistered is the tagged half of the build-tag proof; the
// untagged half is in site_absent_test.go.
func TestSiteNamespaceIsRegistered(t *testing.T) {
	entry, ok := lookupNamespace("site")
	if !ok {
		t.Fatal("the site namespace is not registered in a build made with -tags site")
	}
	if entry.summary == "" {
		t.Error("the site namespace has no usage summary")
	}
}

func TestClassifySiteRemoteProblem(t *testing.T) {
	tests := []struct {
		name       string
		diagnostic string
		want       siteRemoteProblem
	}{
		{
			"remote missing",
			`CRITICAL: Failed to create file system for "shared:": didn't find section in config file ("shared")`,
			siteRemoteMissing,
		},
		{
			"config unreadable",
			`CRITICAL: Failed to load config file "/run/credentials/rclone.conf": permission denied`,
			siteRemoteConfigUnreadable,
		},
		{
			"credential rejected",
			`NewFs: failed to make FTP connection to "host.example:21": 530 Login authentication failed`,
			siteRemoteCredentialRejected,
		},
		{
			"host unreachable",
			`NewFs: failed to make FTP connection to "missing.example:21": dial tcp: lookup missing.example: no such host`,
			siteRemoteHostUnreachable,
		},
		{"unknown rclone failure", "CRITICAL: an unfamiliar backend failure", siteRemoteUnknown},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifySiteRemoteProblem(test.diagnostic); got != test.want {
				t.Errorf("classifySiteRemoteProblem(%q) = %d, want %d", test.diagnostic, got, test.want)
			}
		})
	}
}

// Every remote failure stops before the expensive build and produces one
// sentence naming the cause and its remedy. Exact stderr checks also prove
// that no raw rclone diagnostic leaks through the shared reporter.
func TestSiteDeployReportsRemoteFailuresBeforeBuilding(t *testing.T) {
	tests := []struct {
		name   string
		result siteRemoteCheckResult
		code   int
		want   string
	}{
		{
			"remote missing",
			siteRemoteCheckResult{problem: siteRemoteMissing, status: 2},
			2,
			"allod: no 'shared' rclone remote is configured; run 'allod site config' to create it\n",
		},
		{
			"config unreadable",
			siteRemoteCheckResult{problem: siteRemoteConfigUnreadable, status: 3},
			3,
			"allod: the rclone configuration is unreadable; check its path and permissions\n",
		},
		{
			"credential rejected",
			siteRemoteCheckResult{problem: siteRemoteCredentialRejected, status: 4},
			4,
			"allod: the stored username or password for 'shared' was rejected; run 'allod site config update user' or 'allod site config update password'\n",
		},
		{
			"host unreachable",
			siteRemoteCheckResult{problem: siteRemoteHostUnreachable, status: 5},
			5,
			"allod: the host for 'shared' is unreachable; check its address, the network connection, and the hosting service\n",
		},
		{
			"unknown failure",
			siteRemoteCheckResult{problem: siteRemoteUnknown, status: 6},
			6,
			"allod: the 'shared' hosting remote could not be checked (rclone exited 6)\n",
		},
		{
			"reserved deployed-but-unhealthy status",
			siteRemoteCheckResult{problem: siteRemoteCredentialRejected, status: siteVerifyExit},
			1,
			"allod: the stored username or password for 'shared' was rejected; run 'allod site config update user' or 'allod site config update password'\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &deployStub{
				remoteResult: test.result,
				storePath:    "/nix/store/aaa-site",
				verifyStatus: 200,
			}
			useDeployStub(t, stub)
			useSiteRepo(t, "domain = \"example.com\"\n")

			_, errText, code := runAllod(t, "site", "deploy")
			if code != test.code {
				t.Errorf("exit code = %d, want %d", code, test.code)
			}
			if errText != test.want {
				t.Errorf("stderr = %q, want %q", errText, test.want)
			}
			if stub.remoteCalls != 1 {
				t.Errorf("the remote was checked %d times, want 1", stub.remoteCalls)
			}
			if stub.remoteConfigPath != "" {
				t.Errorf("remote config path = %q, want the rclone default", stub.remoteConfigPath)
			}
			if stub.buildCalls != 0 {
				t.Errorf("nix build ran %d times after the remote failed, want 0", stub.buildCalls)
			}
			if stub.syncCalls != 0 {
				t.Errorf("rclone sync ran %d times after the remote failed, want 0", stub.syncCalls)
			}
			if test.result.status == siteVerifyExit {
				if code == siteVerifyExit {
					t.Errorf("pre-build failure returned reserved post-deploy status %d", siteVerifyExit)
				}
				if strings.Contains(errText, "deployed") {
					t.Errorf("pre-build failure claims a deploy happened: %q", errText)
				}
			}
		})
	}
}

// An explicit config is a different source of truth from rclone's default.
// Every remedy that concerns stored configuration must carry that same path,
// and must quote it so control characters cannot forge another output line.
func TestReportSiteRemoteFailureUsesSelectedConfig(t *testing.T) {
	configPath := "/run/credentials/site \"primary\"\nrclone.conf"
	quotedPath := fmt.Sprintf("%q", configPath)
	tests := []struct {
		name               string
		result             siteRemoteCheckResult
		want               string
		usesLifecycleRoute bool
	}{
		{
			"remote missing",
			siteRemoteCheckResult{problem: siteRemoteMissing, status: 1},
			"allod: no 'shared' rclone remote is configured in " + quotedPath + "; create it in that selected configuration\n",
			false,
		},
		{
			"config unreadable",
			siteRemoteCheckResult{problem: siteRemoteConfigUnreadable, status: 1},
			"allod: the selected rclone configuration " + quotedPath + " is unreadable; check that path and its permissions\n",
			false,
		},
		{
			"credential rejected",
			siteRemoteCheckResult{problem: siteRemoteCredentialRejected, status: 1},
			"allod: the stored username or password for 'shared' in " + quotedPath + " was rejected; run 'allod site config update user' or 'allod site config update password' with that same --config path\n",
			true,
		},
		{
			"unknown failure",
			siteRemoteCheckResult{problem: siteRemoteUnknown, status: 8},
			"allod: the 'shared' hosting remote using " + quotedPath + " could not be checked (rclone exited 8)\n",
			false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var message strings.Builder
			previousErr := stderr
			stderr = &message
			defer func() { stderr = previousErr }()

			reportSiteRemoteFailure(test.result, configPath)
			if got := message.String(); got != test.want {
				t.Errorf("message = %q, want %q", got, test.want)
			}
			if !test.usesLifecycleRoute && strings.Contains(message.String(), "allod site config") {
				t.Errorf("selected-config remedy redirects to the default configuration: %q", message.String())
			}
			if test.usesLifecycleRoute && !strings.Contains(message.String(), "same --config path") {
				t.Errorf("selected-config lifecycle remedy loses its source path: %q", message.String())
			}
			if strings.Count(message.String(), "\n") != 1 {
				t.Errorf("selected-config remedy is not one safe line: %q", message.String())
			}
		})
	}
}

// A working credential gets past the check, and pays for exactly one probe.
func TestSiteDeployChecksTheRemoteOnce(t *testing.T) {
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	useSiteRepo(t, "domain = \"example.com\"\n")

	if _, errText, code := runAllod(t, "site", "deploy"); code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if stub.remoteCalls != 1 {
		t.Errorf("the remote was checked %d times, want 1", stub.remoteCalls)
	}
	if stub.buildCalls != 1 {
		t.Errorf("nix build ran %d times, want 1", stub.buildCalls)
	}
}

// TestRcloneSiteRemoteCheck exercises the real process boundary. It pins the
// exact read-only, authenticating argv, proves stdout and raw diagnostics stay
// suppressed, and keeps an explicit config path available for the named-path
// credential work without changing today's default invocation.
func TestRcloneSiteRemoteCheck(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh to write a stub rclone with")
	}
	binDir := t.TempDir()
	record := filepath.Join(t.TempDir(), "rclone-call")
	script := "#!/bin/sh\n" +
		"printf 'config=%s\\n' \"$RCLONE_CONFIG\" >\"$STUB_RECORD\"\n" +
		"for arg do printf 'arg=%s\\n' \"$arg\" >>\"$STUB_RECORD\"; done\n" +
		"printf 'directory listing that must stay hidden\\n'\n" +
		"printf '%s' \"$STUB_DIAGNOSTIC\" >&2\n" +
		"exit ${STUB_STATUS:-0}\n"
	if err := os.WriteFile(filepath.Join(binDir, "rclone"), []byte(script), 0755); err != nil {
		t.Fatalf("could not write the stub rclone: %v", err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("STUB_RECORD", record)
	t.Setenv("RCLONE_CONFIG", "/environment/rclone.conf")

	result := rcloneSiteRemoteCheck("")
	if result != (siteRemoteCheckResult{problem: siteRemoteReady}) {
		t.Errorf("result = %+v, want ready", result)
	}
	if got := readFile(t, record); got != "config=/environment/rclone.conf\narg=lsd\narg=shared:\n" {
		t.Errorf("rclone call = %q, want one 'lsd shared:' using the inherited config", got)
	}

	t.Setenv("STUB_STATUS", "7")
	t.Setenv("STUB_DIAGNOSTIC", "530 Login authentication failed")
	result = rcloneSiteRemoteCheck("")
	if result != (siteRemoteCheckResult{problem: siteRemoteCredentialRejected, status: 7}) {
		t.Errorf("result = %+v, want rejected credential with status 7", result)
	}

	t.Setenv("STUB_STATUS", "0")
	t.Setenv("STUB_DIAGNOSTIC", "")
	result = rcloneSiteRemoteCheck("/run/credentials/rclone.conf")
	if result != (siteRemoteCheckResult{problem: siteRemoteReady}) {
		t.Errorf("result = %+v, want ready", result)
	}
	if got := readFile(t, record); got != "config=/environment/rclone.conf\narg=--config\narg=/run/credentials/rclone.conf\narg=lsd\narg=shared:\n" {
		t.Errorf("rclone call with config = %q, want one explicit-config 'lsd shared:'", got)
	}
}
