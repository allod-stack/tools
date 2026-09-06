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
	"testing"
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
	siteSync = func(args []string) int {
		stub.syncArgs, stub.syncCalls = args, stub.syncCalls+1
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

// stubTools puts unusable executables of the given names on an otherwise empty
// PATH, so the LookPath preflights pass and anything that actually ran one of
// them would fail loudly. It returns the directory PATH was set to.
func stubTools(t *testing.T, names ...string) string {
	t.Helper()
	binDir := t.TempDir()
	for _, name := range names {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
			t.Fatalf("could not write stub %s: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir)
	return binDir
}

// useSiteRepo creates a site repository with the given site.toml and makes it
// the current directory.
func useSiteRepo(t *testing.T, config string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, siteConfigName), []byte(config), 0644); err != nil {
		t.Fatalf("could not write %s: %v", siteConfigName, err)
	}
	t.Chdir(root)
	// t.TempDir can hand back a path through a symlink (/tmp -> /private/tmp
	// and the like); the command reports the resolved one.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("could not resolve %s: %v", root, err)
	}
	return resolved
}

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

// --- Dispatch and usage ---

func TestSiteUsage(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		code       int
		outHas     string
		errHas     string
		errIsEmpty bool
	}{
		{"namespace listed in top-level usage", []string{}, 1, "site     Deploy a static site", "", true},
		{"no command prints usage to stderr", []string{"site"}, 1, "", "allod site deploy [--dry-run]", false},
		{"--help prints usage to stdout", []string{"site", "--help"}, 0, "allod site deploy [--dry-run]", "", true},
		{"-h prints usage to stdout", []string{"site", "-h"}, 0, "allod site config [--force]", "", true},
		{"deploy --help prints usage to stdout", []string{"site", "deploy", "--help"}, 0, "allod site deploy [--dry-run]", "", true},
		{"config --help prints usage to stdout", []string{"site", "config", "--help"}, 0, "allod site config [--force]", "", true},
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

// TestDeployFilterText pins the exclusion list itself. Losing an entry here is
// the failure this command exists to prevent, and /.well-known/** is the one
// whose loss shows up weeks later as an expired certificate rather than as a
// broken deploy.
func TestDeployFilterText(t *testing.T) {
	want := "- /.well-known/**\n- /.htaccess\n- /stats/**\n- /cgi-bin/**\n"
	if got := deployFilterText(); got != want {
		t.Errorf("filter text =\n%q\nwant\n%q", got, want)
	}
}

// --- Deploying ---

func TestSiteDeploy(t *testing.T) {
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

	filter := argAfter(t, stub.syncArgs, "--filter-from")
	want := []string{
		"sync", "/nix/store/aaa-site", "shared:domains/example.com/public_html",
		"--filter-from", filter,
		"--backup-dir", "shared:deploy-trash/example.com",
		"--verbose",
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

// TestSiteDeployWritesTheFilter proves the list rclone reads is this command's
// list, not one the site repository supplied.
func TestSiteDeployWritesTheFilter(t *testing.T) {
	var contents string
	stub := &deployStub{storePath: "/nix/store/aaa-site", verifyStatus: 200}
	useDeployStub(t, stub)
	previousSync := siteSync
	siteSync = func(args []string) int {
		stub.syncArgs, stub.syncCalls = args, stub.syncCalls+1
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
	if contents != deployFilterText() {
		t.Errorf("filter file =\n%q\nwant\n%q", contents, deployFilterText())
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
	if last := stub.syncArgs[len(stub.syncArgs)-1]; last != "--dry-run" {
		t.Errorf("last rclone arg = %q, want %q\ngot: %v", last, "--dry-run", stub.syncArgs)
	}
	// The destination is the same one a real deploy would use.
	if stub.syncArgs[2] != "shared:domains/example.com/public_html" {
		t.Errorf("rclone destination = %q, want %q", stub.syncArgs[2], "shared:domains/example.com/public_html")
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
			"allod: the stored username or password for 'shared' was rejected; run 'allod site config --force' to replace it\n",
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
