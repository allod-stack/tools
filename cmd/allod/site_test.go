package main

// Tests for the site namespace, in the shape cmd/forge/harness_test.go
// established: the CLI is run in process with the stdout/stderr seams swapped
// for buffers, and every effect on the outside world is replaced by a stub.
//
//	out, errText, code := runAllod(t, "site", "deploy", "--dry-run")
//
// The package-level seams these helpers swap are shared mutable state, so no
// test here calls t.Parallel.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Harness ---

func swapStreams(out, errOut io.Writer) func() {
	previousOut, previousErr := stdout, stderr
	stdout, stderr = out, errOut
	return func() { stdout, stderr = previousOut, previousErr }
}

// runAllod runs the CLI with the given arguments and returns everything it
// printed plus its exit status. Nothing reaches the test process's own streams.
func runAllod(t *testing.T, args ...string) (stdoutText, stderrText string, code int) {
	t.Helper()
	var outBuffer, errBuffer bytes.Buffer
	restore := swapStreams(&outBuffer, &errBuffer)
	code = run(args)
	restore()
	return outBuffer.String(), errBuffer.String(), code
}

// deployStub records what the command asked the world to do and answers with
// whatever the test set up.
type deployStub struct {
	buildDir     string
	storePath    string
	buildStatus  int
	syncArgs     []string
	syncCalls    int
	syncStatus   int
	verifyURL    string
	verifyCalls  int
	verifyStatus int
	verifyErr    error
}

// useDeployStub installs the stub over the three seams in site.go and puts
// stub 'nix' and 'rclone' executables on PATH so the LookPath preflight passes
// without a nix daemon or an rclone installation. It returns the directory
// PATH was set to, which is the whole of PATH for the rest of the test.
func useDeployStub(t *testing.T, stub *deployStub) string {
	t.Helper()
	previousBuild, previousSync, previousVerify := siteBuild, siteSync, siteVerify
	siteBuild = func(root string) (string, int) {
		stub.buildDir = root
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
	t.Cleanup(func() { siteBuild, siteSync, siteVerify = previousBuild, previousSync, previousVerify })

	binDir := t.TempDir()
	for _, name := range []string{"nix", "rclone"} {
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
		{"no command prints usage to stderr", []string{"site"}, 1, "", "Usage: allod site deploy", false},
		{"--help prints usage to stdout", []string{"site", "--help"}, 0, "Usage: allod site deploy", "", true},
		{"-h prints usage to stdout", []string{"site", "-h"}, 0, "Usage: allod site deploy", "", true},
		{"deploy --help prints usage to stdout", []string{"site", "deploy", "--help"}, 0, "Usage: allod site deploy", "", true},
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
			useSiteRepo(t, "domain = \"hashpool.dev\"\n")

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
		{"the one key", "domain = \"hashpool.dev\"\n", "hashpool.dev", ""},
		{"no trailing newline", "domain = \"hashpool.dev\"", "hashpool.dev", ""},
		{"no spaces around equals", "domain=\"hashpool.dev\"\n", "hashpool.dev", ""},
		{"leading whitespace", "\t  domain = \"hashpool.dev\"\n", "hashpool.dev", ""},
		{"comments and blank lines", "# the site\n\ndomain = \"hashpool.dev\" # inline\n\n", "hashpool.dev", ""},
		{"literal string", "domain = 'hashpool.dev'\n", "hashpool.dev", ""},
		// Forward compatibility: a file written for a later version of this
		// command still deploys with this one.
		{"unknown scalar key", "domain = \"hashpool.dev\"\nredirect_www = true\n", "hashpool.dev", ""},
		{"unknown key before domain", "cache_seconds = 300\ndomain = \"hashpool.dev\"\n", "hashpool.dev", ""},
		{"unknown key with an array value", "domain = \"hashpool.dev\"\nheaders = [\"a\", \"b\"]\n", "hashpool.dev", ""},
		{"unknown table", "domain = \"hashpool.dev\"\n\n[redirects]\nold = \"new\"\n", "hashpool.dev", ""},
		{"unknown table with its own domain", "domain = \"hashpool.dev\"\n\n[staging]\ndomain = \"staging.hashpool.dev\"\n", "hashpool.dev", ""},
		// Errors, all about the one key that is read.
		{"empty file", "", "", "no domain key"},
		{"only comments", "# nothing here\n", "", "no domain key"},
		{"domain only under a table", "[staging]\ndomain = \"staging.hashpool.dev\"\n", "", "no domain key"},
		{"unquoted", "domain = hashpool.dev\n", "", "must be a quoted string"},
		{"unterminated", "domain = \"hashpool.dev\n", "", "must be a quoted string"},
		{"trailing junk after the string", "domain = \"hashpool.dev\" oops\n", "", "must be a quoted string"},
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
		"hashpool.dev",
		"www.hashpool.dev",
		"a-b.example.co.uk",
		"xn--80ak6aa92e.com",
		"123.example",
	}
	invalid := []string{
		"",
		"localhost",
		"hashpool.dev/../other.example",
		"hashpool.dev/public_html",
		"../other.example",
		"..",
		"shared:domains/other.example",
		"hashpool..dev",
		".hashpool.dev",
		"hashpool.dev.",
		"-hashpool.dev",
		"hashpool-.dev",
		"hash pool.dev",
		"hashpool.dev\nother.example",
		"hashpool.dev\x00",
		"*.hashpool.dev",
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
	useSiteRepo(t, "domain = \"hashpool.dev/../other.example\"\n")

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
	root := useSiteRepo(t, "# hashpool\ndomain = \"hashpool.dev\"\n")

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
		"sync", "/nix/store/aaa-site", "shared:domains/hashpool.dev/public_html",
		"--filter-from", filter,
		"--backup-dir", "shared:deploy-trash/hashpool.dev",
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
	if stub.verifyURL != "https://hashpool.dev/" {
		t.Errorf("verified %q, want %q", stub.verifyURL, "https://hashpool.dev/")
	}
	for _, want := range []string{
		"Domain: hashpool.dev\n",
		"Source: /nix/store/aaa-site\n",
		"Target: shared:domains/hashpool.dev/public_html\n",
		"Verified: https://hashpool.dev/ returned 200\n",
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
	useSiteRepo(t, "domain = \"hashpool.dev\"\n")

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
	useSiteRepo(t, "domain = \"hashpool.dev\"\n")

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
	if stub.syncArgs[2] != "shared:domains/hashpool.dev/public_html" {
		t.Errorf("rclone destination = %q, want %q", stub.syncArgs[2], "shared:domains/hashpool.dev/public_html")
	}
	if stub.verifyCalls != 0 {
		t.Errorf("verification ran %d times on a dry run, want 0", stub.verifyCalls)
	}
	if !strings.Contains(out, "Dry run: shared:domains/hashpool.dev/public_html was not modified\n") {
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
			useSiteRepo(t, "domain = \"hashpool.dev\"\n")

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
	useSiteRepo(t, "domain = \"hashpool.dev\"\n")

	_, errText, code := runAllod(t, "site", "deploy")
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if !strings.Contains(errText, "rclone sync failed") {
		t.Errorf("stderr does not contain %q\ngot: %q", "rclone sync failed", errText)
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
			useSiteRepo(t, "domain = \"hashpool.dev\"\n")

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
			useSiteRepo(t, "domain = \"hashpool.dev\"\n")

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
