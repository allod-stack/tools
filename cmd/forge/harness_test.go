package main

// Shared test harness for cmd/forge, plus the foundation tests.
//
// Every command test in this package is expected to use these helpers rather
// than rolling its own; they exist so that no test ever spawns a process,
// touches the real forge, or reads the real credential.
//
// # Running the CLI
//
//	stdout, stderr, code := runForge(t, "issue", "list", "-R", "acme/widget")
//
// runForge swaps the package-level stdout/stderr seams for buffers, calls
// run(argv), restores the seams, and hands back what was printed and the exit
// status. Nothing is written to the test process's own streams.
//
//	stdout, stderr, code := runCall(t, func() { api("GET", "/repos/a/b", nil) })
//
// runCall does the same for one helper called directly, recovering the cliExit
// panic that die()/exit() raise. code is the status the CLI would have exited
// with, or 0 if the function returned normally. Use it to pin the behaviour of
// a helper that no stub-free command reaches yet.
//
// # Seams
//
//	useStdin(t, "text")               // stdin seam, restored on cleanup
//	useInferRepo(t, "acme/widget", nil) // gitremote seam: slug, error
//	useNoInferRepo(t)                 // fails the test if repo inference runs
//
// # Environment
//
//	useToken(t, fakeToken)            // useTokenFile with the fake token
//	useTokenFile(t, "value")          // writes a temp file, sets FORGE_TOKEN_FILE
//	useNoCredentials(t)               // no token file; FORGEJO_TOKEN empty
//	useServer(t, srv)                 // FORGE_URL
//
// All of these go through t.Setenv, so they are undone automatically and must
// not be combined with t.Parallel. TestMain already points HOME, FORGE_URL and
// FORGE_TOKEN_FILE at unusable values, so a test that forgets to set anything
// still cannot reach a real credential or a real server.
//
// # Recording server
//
//	srv := newRecordingServer(t, map[string]cannedResponse{
//	        "/api/v1/repos/acme/widget/labels?limit=100": {Body: `[]`},
//	        "POST /api/v1/repos/acme/widget/labels":      {Status: 201, Body: `{"id":4}`},
//	})
//	useServer(t, srv)
//	...
//	srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/labels?limit=100")
//	got := srv.requests()[0].Body
//
// Routes are matched on "METHOD /path?query" first and then on "/path?query"
// alone; the path is the request URI exactly as sent, so a test asserts the
// same string bash's mock curl matched on. srv.enqueue(...) instead answers
// requests in order, for the same path answering differently twice. An
// unmatched request fails the test and gets a 500.
//
// Recorded requests carry the method, the request URI, the Content-Type, the
// body bytes and the Authorization header. A test may compare the
// Authorization value against a fake token to prove which credential was used,
// but must never print it: use authKind() in failure messages.
//
// # Rules
//
//   - No t.Parallel anywhere in this package: the seams and the bash globals
//     are package-level state.
//   - Exactly one TestMain, here. Do not add another.
//   - Never put a real token in a test; fakeToken and its friends are enough.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"forge.anarch.diy/allod/tools/internal/gitremote"
)

// Fake credentials. The bash suite used "test-token" and "valid-token"; these
// are the same idea with a name no real token could collide with.
const (
	fakeToken      = "fake-token-for-tests"
	fakeOtherToken = "fake-other-token-for-tests"
)

// TestMain makes the whole package hermetic: HOME, FORGE_TOKEN_FILE and
// FORGE_URL point somewhere unusable, so a test that sets nothing cannot read
// ~/.config/git/forgejo-token or talk to a real forge.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "forge-cmd-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, "harness: cannot create temp home:", err)
		os.Exit(1)
	}
	os.Setenv("HOME", dir)
	os.Setenv("FORGEJO_TOKEN", "")
	os.Setenv("FORGE_TOKEN_FILE", filepath.Join(dir, "no-such-token-file"))
	os.Setenv("FORGE_URL", "http://127.0.0.1:1")

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// --- Running the CLI ---

// runForge runs the CLI with the given arguments and returns everything it
// printed plus its exit status.
func runForge(t *testing.T, args ...string) (stdoutText, stderrText string, code int) {
	t.Helper()
	var status int
	stdoutText, stderrText, _ = runCall(t, func() { status = run(args) })
	return stdoutText, stderrText, status
}

// runCall calls fn with the output seams captured, recovering the cliExit
// panic that die() and exit() raise. Any other panic propagates.
func runCall(t *testing.T, fn func()) (stdoutText, stderrText string, code int) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	restore := swapStreams(&outBuf, &errBuf)
	defer func() {
		restore()
		stdoutText, stderrText = outBuf.String(), errBuf.String()
	}()
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		e, ok := r.(cliExit)
		if !ok {
			panic(r)
		}
		code = e.code
	}()

	resetState()
	fn()
	return "", "", 0
}

func swapStreams(out, errOut io.Writer) func() {
	prevOut, prevErr := stdout, stderr
	stdout, stderr = out, errOut
	return func() { stdout, stderr = prevOut, prevErr }
}

// --- Seams ---

// useStdin installs text as the CLI's standard input.
func useStdin(t *testing.T, text string) {
	t.Helper()
	prev := stdin
	stdin = strings.NewReader(text)
	t.Cleanup(func() { stdin = prev })
}

// useInferRepo makes repo inference return a fixed answer instead of running
// git. Pass gitremote.ErrNoOrigin to model "not in a git repo", or ("", nil)
// to model a remote URL with no owner/repo tail.
func useInferRepo(t *testing.T, slug string, err error) {
	t.Helper()
	prev := inferRepo
	inferRepo = func() (string, error) { return slug, err }
	t.Cleanup(func() { inferRepo = prev })
}

// useNoInferRepo fails the test if repo inference is attempted at all.
func useNoInferRepo(t *testing.T) {
	t.Helper()
	prev := inferRepo
	inferRepo = func() (string, error) {
		t.Error("repo inference ran when -R was given")
		return "", gitremote.ErrNoOrigin
	}
	t.Cleanup(func() { inferRepo = prev })
}

// --- Environment ---

// useToken installs tok through a temp token file. The file is the only
// credential source (allod/tools#57); FORGEJO_TOKEN is refused, and a test
// that wants the refusal sets the variable itself.
func useToken(t *testing.T, tok string) string {
	t.Helper()
	return useTokenFile(t, tok)
}

// useTokenFile writes contents to a temp file and points FORGE_TOKEN_FILE at
// it, with FORGEJO_TOKEN cleared so a developer's shell cannot trip the
// refusal. It returns the path, which appears verbatim in several messages.
func useTokenFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forgejo-token")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing token file: %v", err)
	}
	t.Setenv("FORGEJO_TOKEN", "")
	t.Setenv("FORGE_TOKEN_FILE", path)
	return path
}

// useNoCredentials points FORGE_TOKEN_FILE at an absent file, with
// FORGEJO_TOKEN empty, and returns the path that will be reported as missing.
func useNoCredentials(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "absent-token")
	t.Setenv("FORGEJO_TOKEN", "")
	t.Setenv("FORGE_TOKEN_FILE", path)
	return path
}

// useServer points FORGE_URL at a test server.
func useServer(t *testing.T, srv *recordingServer) {
	t.Helper()
	t.Setenv("FORGE_URL", srv.URL)
}

// --- Recording server ---

// cannedResponse is one reply. A zero Status means 200.
type cannedResponse struct {
	Status int
	Body   string
	Header map[string]string
}

// recordedRequest is one request as the server saw it.
type recordedRequest struct {
	Method string
	// Path is r.URL.RequestURI(): the path and query exactly as sent, so
	// percent-encoding can be asserted.
	Path string
	// Authorization is the header value as received. Compare it, never print
	// it; authKind() is the safe thing to put in a failure message.
	Authorization string
	ContentType   string
	Body          []byte
}

// hasAuth reports whether an Authorization header arrived at all.
func (r recordedRequest) hasAuth() bool { return r.Authorization != "" }

// authKind describes the credential without revealing it.
func (r recordedRequest) authKind() string {
	switch {
	case r.Authorization == "":
		return "absent"
	case strings.HasPrefix(r.Authorization, "token "):
		return "token <redacted>"
	default:
		return "other <redacted>"
	}
}

type recordingServer struct {
	*httptest.Server

	t      *testing.T
	routes map[string]cannedResponse

	mu    sync.Mutex
	reqs  []recordedRequest
	queue []cannedResponse
}

// newRecordingServer starts an HTTP server that records every request and
// answers from routes. A route key is either "METHOD /path?query" or
// "/path?query"; the method-qualified form wins.
func newRecordingServer(t *testing.T, routes map[string]cannedResponse) *recordingServer {
	t.Helper()
	s := &recordingServer{t: t, routes: routes}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

// enqueue queues responses that are handed out in order, ahead of any route
// match. Use it when the same path must answer differently on consecutive
// calls.
func (s *recordingServer) enqueue(responses ...cannedResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, responses...)
}

// requests returns a copy of everything recorded so far, in order.
func (s *recordingServer) requests() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedRequest(nil), s.reqs...)
}

// count returns the number of requests recorded so far.
func (s *recordingServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// assertRequest checks the method and request URI of the i'th request,
// counting from zero.
func (s *recordingServer) assertRequest(t *testing.T, i int, method, path string) {
	t.Helper()
	reqs := s.requests()
	if i >= len(reqs) {
		t.Fatalf("request %d not made; only %d requests recorded", i, len(reqs))
	}
	if reqs[i].Method != method || reqs[i].Path != path {
		t.Errorf("request %d = %s %s, want %s %s", i, reqs[i].Method, reqs[i].Path, method, path)
	}
}

func (s *recordingServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec := recordedRequest{
		Method:        r.Method,
		Path:          r.URL.RequestURI(),
		Authorization: r.Header.Get("Authorization"),
		ContentType:   r.Header.Get("Content-Type"),
		Body:          body,
	}

	s.mu.Lock()
	s.reqs = append(s.reqs, rec)
	resp, ok := s.responseFor(rec)
	s.mu.Unlock()

	if !ok {
		// t.Errorf is safe from a handler goroutine; t.Fatalf would not be.
		s.t.Errorf("recording server: no canned response for %s %s", rec.Method, rec.Path)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"message":"no canned response for %s %s"}`, rec.Method, rec.Path)
		return
	}

	for k, v := range resp.Header {
		w.Header().Set(k, v)
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	io.WriteString(w, resp.Body)
}

// responseFor must be called with the lock held.
func (s *recordingServer) responseFor(rec recordedRequest) (cannedResponse, bool) {
	if len(s.queue) > 0 {
		resp := s.queue[0]
		s.queue = s.queue[1:]
		return resp, true
	}
	if resp, ok := s.routes[rec.Method+" "+rec.Path]; ok {
		return resp, true
	}
	resp, ok := s.routes[rec.Path]
	return resp, ok
}

// userRoutes answers GET /api/v1/user with a login, the shape both credential
// commands read.
func userRoutes(login string) map[string]cannedResponse {
	return map[string]cannedResponse{
		"/api/v1/user": {Body: `{"login":"` + login + `"}`},
	}
}

// ============================ Foundation tests ============================

// --- Help routing (retired shell-suite scenarios) ---

func TestHelpRouting(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{})
	useServer(t, srv)
	useToken(t, fakeToken)

	tests := []struct {
		name     string
		args     []string
		want     string
		contains string
	}{
		{"no arguments", nil, usageText, "Usage: forge"},
		{"help resource", []string{"help"}, usageText, "Resources:"},
		{"--help", []string{"--help"}, usageText, "Resources:"},
		{"-h", []string{"-h"}, usageText, "Resources:"},
		{"pr --help", []string{"pr", "--help"}, usageText, "PR commands"},
		{"issue -h", []string{"issue", "-h"}, usageText, "Issue commands"},
		{"label -h", []string{"label", "-h"}, usageText, "Label commands"},
		{"milestone -h", []string{"milestone", "-h"}, usageText, "Milestone commands"},
		{"token -h", []string{"token", "-h"}, usageText, "Resources:"},
		{"auth --help", []string{"auth", "--help"}, usageText, "Auth commands"},
		{"repo flag before help", []string{"-R", "acme/widget", "pr", "-h"}, usageText, "PR commands"},
		{"project --help", []string{"project", "--help"}, commandUsageTexts["project"], "unavailable"},
		{"project subcommand --help", []string{"project", "list", "--help"}, commandUsageTexts["project"], "unavailable"},
		{"token verify --help", []string{"token", "verify", "--help"}, commandUsageTexts["token verify"], "stdin"},
		{"auth status -h", []string{"auth", "status", "-h"}, commandUsageTexts["auth status"], "FORGE_TOKEN_FILE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errText, code := runForge(t, tt.args...)
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			if out != tt.want {
				t.Errorf("stdout mismatch\ngot:  %q\nwant: %q", truncate(out), truncate(tt.want))
			}
			if errText != "" {
				t.Errorf("stderr = %q, want empty", errText)
			}
			if tt.want == "" {
				t.Error("expected usage text is empty; usage.go key missing")
			}
			if !strings.Contains(out, tt.contains) {
				t.Errorf("stdout does not contain %q", tt.contains)
			}
		})
	}

	// help.sh: "help commands make no API requests".
	if n := srv.count(); n != 0 {
		t.Errorf("help made %d API requests, want 0", n)
	}
}

// --- Dispatch asymmetries (forge lines 2400-2537) ---

func TestUnknownCommandPrintsUsageToStdout(t *testing.T) {
	for _, resource := range []string{"pr", "issue", "label", "milestone", "token", "auth"} {
		t.Run(resource, func(t *testing.T) {
			out, errText, code := runForge(t, resource, "bogus")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if out != usageText {
				t.Errorf("stdout mismatch\ngot: %q", truncate(out))
			}
			if errText != "" {
				t.Errorf("stderr = %q, want empty", errText)
			}
		})
	}
}

// A bare resource with no command takes the same path as an unknown one.
func TestBareResourcePrintsUsageAndExits1(t *testing.T) {
	out, errText, code := runForge(t, "pr")
	if code != 1 || out != usageText || errText != "" {
		t.Errorf("forge pr = (%q, %q, %d), want usage on stdout and exit 1", truncate(out), errText, code)
	}
}

func TestUnknownResourcePrintsEverythingToStderr(t *testing.T) {
	out, errText, code := runForge(t, "bogus")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", truncate(out))
	}
	want := "forge: unknown resource 'bogus'\n" + usageText
	if errText != want {
		t.Errorf("stderr mismatch\ngot:  %q\nwant: %q", truncate(errText), truncate(want))
	}
}

func TestGlobalRepoFlagRequiresValue(t *testing.T) {
	for _, flag := range []string{"-R", "--repo"} {
		out, errText, code := runForge(t, flag)
		if code != 1 {
			t.Errorf("%s: exit code = %d, want 1", flag, code)
		}
		if out != "" {
			t.Errorf("%s: stdout = %q, want empty", flag, out)
		}
		if want := "forge: " + flag + " requires a value\n"; errText != want {
			t.Errorf("%s: stderr = %q, want %q", flag, errText, want)
		}
	}
}

func TestAuthTokenIsNotACommand(t *testing.T) {
	out, errText, code := runForge(t, "auth", "token")
	want := "forge: auth token is not a command; use 'forge auth status' to check credentials\n"
	if code != 1 || out != "" || errText != want {
		t.Errorf("got (%q, %q, %d), want (%q, %q, 1)", out, errText, code, "", want)
	}
}

func TestProjectCommandsUnavailable(t *testing.T) {
	want := "forge: project commands are unavailable: this Forgejo API does not expose repository project endpoints\n"
	for _, args := range [][]string{{"project"}, {"project", "list"}, {"project", "view", "3"}} {
		out, errText, code := runForge(t, args...)
		if code != 1 || out != "" || errText != want {
			t.Errorf("forge %v = (%q, %q, %d), want (%q, %q, 1)", args, out, errText, code, "", want)
		}
	}
}

// --- Token loading (retired shell-suite scenarios) ---

func TestLoadTokenErrors(t *testing.T) {
	t.Run("no credential source", func(t *testing.T) {
		path := useNoCredentials(t)
		out, errText, code := runForge(t, "auth", "status")
		want := "forge: no token found — ensure " + path + " exists or point FORGE_TOKEN_FILE at a mode-0600 token file\n"
		if code != 1 || out != "" || errText != want {
			t.Errorf("got (%q, %q, %d), want (%q, %q, 1)", out, errText, code, "", want)
		}
	})

	invalid := []struct {
		name  string
		value string
	}{
		{"newline", "valid\ninjection"},
		{"quote", `valid"injection`},
		{"backslash", `valid\injection`},
	}
	for _, tt := range invalid {
		t.Run("file "+tt.name, func(t *testing.T) {
			path := useTokenFile(t, tt.value)
			_, errText, code := runForge(t, "auth", "status")
			want := "forge: " + path + " contains invalid characters (newline, quote, or backslash)\n"
			if code != 1 || errText != want {
				t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
			}
		})
	}

	t.Run("empty file", func(t *testing.T) {
		path := useTokenFile(t, "\n\n")
		_, errText, code := runForge(t, "auth", "status")
		want := "forge: " + path + " is empty\n"
		if code != 1 || errText != want {
			t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
		}
	})

	// An empty FORGEJO_TOKEN is not a credential and not a refusal: only a
	// set, non-empty variable is refused (allod/tools#57), so the file is
	// consulted as usual.
	t.Run("empty env falls through to file", func(t *testing.T) {
		path := useTokenFile(t, "")
		t.Setenv("FORGEJO_TOKEN", "")
		_, errText, code := runForge(t, "auth", "status")
		want := "forge: " + path + " is empty\n"
		if code != 1 || errText != want {
			t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
		}
	})
}

// diverges from bash: allod/tools#57
//
// The token is read only from the token file. A set, non-empty FORGEJO_TOKEN
// is refused before dispatch, with one sentence naming the variable and the
// file, even when a valid token file is also present: the refusal is what
// stops anyone believing the variable is in use when it is not. The check
// sits ahead of every command, so it also covers token verify (which never
// loads the configured credential) and repo inference (which would spawn git,
// and so hand the variable to a child process, before any credential is
// asked for).
func TestForgejoTokenEnvRefused(t *testing.T) {
	commands := [][]string{
		{"auth", "status"},
		{"-R", "acme/widget", "label", "list"},
		{"token", "verify"},
		{"label", "list"}, // no -R: would infer the repo from git first
		{"pr", "bogus"},   // unknown command: usage would print, refusal wins
	}
	for _, args := range commands {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			path := useTokenFile(t, fakeToken)
			t.Setenv("FORGEJO_TOKEN", fakeToken)
			srv := newRecordingServer(t, map[string]cannedResponse{})
			useServer(t, srv)
			useStdin(t, fakeToken+"\n")
			prev := inferRepo
			inferRepo = func() (string, error) {
				t.Error("repo inference (a git child process) ran before the FORGEJO_TOKEN refusal")
				return "", gitremote.ErrNoOrigin
			}
			t.Cleanup(func() { inferRepo = prev })

			out, errText, code := runForge(t, args...)
			want := "forge: FORGEJO_TOKEN is no longer read; unset it and put the token in a mode-0600 file named by FORGE_TOKEN_FILE (currently " + path + ")\n"
			if code != 1 || out != "" || errText != want {
				t.Errorf("got (%q, %q, %d), want (%q, %q, 1)", out, errText, code, "", want)
			}
			if n := srv.count(); n != 0 {
				t.Errorf("recording server received %d requests, want 0", n)
			}
		})
	}

	// Help output is the one exemption: it makes no request and spawns
	// nothing, and a user whose environment is wrong still needs to read it.
	t.Run("help is still printed", func(t *testing.T) {
		useTokenFile(t, fakeToken)
		t.Setenv("FORGEJO_TOKEN", fakeToken)
		for _, args := range [][]string{{}, {"--help"}, {"auth", "status", "-h"}, {"pr", "list", "--help"}} {
			out, errText, code := runForge(t, args...)
			if code != 0 || out == "" || errText != "" {
				t.Errorf("forge %v = (%q, %q, %d), want usage on stdout and 0", args, truncate(out), errText, code)
			}
		}
	})

	// The same token in the file, and nothing in the environment, is the
	// working path: the header carries the file's contents.
	t.Run("token file alone is accepted", func(t *testing.T) {
		path := useTokenFile(t, fakeToken)
		srv := newRecordingServer(t, userRoutes("testuser"))
		useServer(t, srv)

		out, errText, code := runForge(t, "auth", "status")
		if want := "Authenticated as testuser (" + path + ")\n"; code != 0 || out != want || errText != "" {
			t.Errorf("got (%q, %q, %d), want (%q, %q, 0)", out, errText, code, want, "")
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/user")
		if got := srv.requests()[0].Authorization; got != "token "+fakeToken {
			t.Errorf("authorization = %s, want the token file contents", srv.requests()[0].authKind())
		}
	})
}

// diverges from bash: allod/tools#57
//
// The messages promise a mode-0600 token file, so loadToken checks it: any
// group or other permission bit is refused before the contents are read or a
// request is made. os.Stat follows symlinks, which is how the agenix-delivered
// file is reached, so the target's mode is what counts.
func TestTokenFilePermissions(t *testing.T) {
	loose := []struct {
		name string
		mode os.FileMode
	}{
		{"group readable", 0o640},
		{"other readable", 0o604},
		{"world readable", 0o644},
		{"group writable", 0o620},
	}
	for _, tt := range loose {
		t.Run(tt.name, func(t *testing.T) {
			path := useTokenFile(t, fakeToken)
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			srv := newRecordingServer(t, map[string]cannedResponse{})
			useServer(t, srv)

			out, errText, code := runForge(t, "auth", "status")
			want := "forge: token file " + path + " is readable by group or others; run chmod 600 on it\n"
			if code != 1 || out != "" || errText != want {
				t.Errorf("got (%q, %q, %d), want (%q, %q, 1)", out, errText, code, "", want)
			}
			if n := srv.count(); n != 0 {
				t.Errorf("recording server received %d requests, want 0", n)
			}
		})
	}

	for _, mode := range []os.FileMode{0o600, 0o400} {
		t.Run(fmt.Sprintf("owner-only %04o is accepted", mode), func(t *testing.T) {
			path := useTokenFile(t, fakeToken)
			if err := os.Chmod(path, mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			srv := newRecordingServer(t, userRoutes("testuser"))
			useServer(t, srv)

			out, errText, code := runForge(t, "auth", "status")
			if want := "Authenticated as testuser (" + path + ")\n"; code != 0 || out != want || errText != "" {
				t.Errorf("got (%q, %q, %d), want (%q, %q, 0)", out, errText, code, want, "")
			}
		})
	}

	// A symlink is judged by its target: a 0777 link to a 0600 file is fine,
	// and a link to a 0644 file is refused with the link's own path, which is
	// the one the user configured.
	t.Run("symlink follows to the target mode", func(t *testing.T) {
		target := useTokenFile(t, fakeToken)
		link := filepath.Join(t.TempDir(), "forgejo-token-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		t.Setenv("FORGE_TOKEN_FILE", link)

		srv := newRecordingServer(t, userRoutes("testuser"))
		useServer(t, srv)
		out, _, code := runForge(t, "auth", "status")
		if want := "Authenticated as testuser (" + link + ")\n"; code != 0 || out != want {
			t.Errorf("0600 target through a link: got (%q, %d), want (%q, 0)", out, code, want)
		}

		if err := os.Chmod(target, 0o644); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		_, errText, code := runForge(t, "auth", "status")
		want := "forge: token file " + link + " is readable by group or others; run chmod 600 on it\n"
		if code != 1 || errText != want {
			t.Errorf("0644 target through a link: got (%q, %d), want (%q, 1)", errText, code, want)
		}
		if n := srv.count(); n != 1 {
			t.Errorf("recording server received %d requests, want only the first run's", n)
		}
	})
}

// --- auth status (retired shell-suite scenarios) ---

func TestAuthStatus(t *testing.T) {
	t.Run("valid token", func(t *testing.T) {
		srv := newRecordingServer(t, userRoutes("testuser"))
		useServer(t, srv)
		path := useToken(t, fakeToken)

		out, errText, code := runForge(t, "auth", "status")
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		if want := "Authenticated as testuser (" + path + ")\n"; out != want {
			t.Errorf("stdout = %q, want %q", out, want)
		}
		if errText != "" {
			t.Errorf("stderr = %q, want empty", errText)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/user")

		req := srv.requests()[0]
		if req.Authorization != "token "+fakeToken {
			t.Errorf("authorization = %s, want the configured token", req.authKind())
		}
		// verify_token_http passes no -H Content-Type, unlike api().
		if req.ContentType != "" {
			t.Errorf("Content-Type = %q, want absent", req.ContentType)
		}
	})

	t.Run("carriage return is stripped", func(t *testing.T) {
		srv := newRecordingServer(t, userRoutes("testuser"))
		useServer(t, srv)
		path := useToken(t, fakeToken+"\r")

		out, _, code := runForge(t, "auth", "status")
		if code != 0 || out != "Authenticated as testuser ("+path+")\n" {
			t.Errorf("got (%q, %d), want the authenticated line and 0", out, code)
		}
		if got := srv.requests()[0].Authorization; got != "token "+fakeToken {
			t.Errorf("carriage return reached the header (kind %s)", srv.requests()[0].authKind())
		}
	})

	t.Run("token file names the path", func(t *testing.T) {
		srv := newRecordingServer(t, userRoutes("testuser"))
		useServer(t, srv)
		path := useTokenFile(t, fakeToken+"\n")

		out, _, code := runForge(t, "auth", "status")
		if want := "Authenticated as testuser (" + path + ")\n"; out != want || code != 0 {
			t.Errorf("got (%q, %d), want (%q, 0)", out, code, want)
		}
	})

	failures := []struct {
		status int
		reason string
	}{
		{401, "unauthorized"},
		{403, "forbidden"},
		{500, "HTTP 500"},
	}
	for _, tt := range failures {
		t.Run(fmt.Sprintf("status %d", tt.status), func(t *testing.T) {
			srv := newRecordingServer(t, map[string]cannedResponse{
				"/api/v1/user": {Status: tt.status, Body: `{"message":"Unauthorized"}`},
			})
			useServer(t, srv)
			path := useToken(t, fakeToken)

			out, errText, code := runForge(t, "auth", "status")
			want := "Authentication failed: " + tt.reason + " (" + path + ")\n"
			if code != 1 || out != "" || errText != want {
				t.Errorf("got (%q, %q, %d), want (%q, %q, 1)", out, errText, code, "", want)
			}
		})
	}

	t.Run("rejects options and arguments", func(t *testing.T) {
		useToken(t, fakeToken)
		_, errText, code := runForge(t, "auth", "status", "--token", "foo")
		if want := "forge: unknown option for auth status: --token\n"; errText != want || code != 1 {
			t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
		}
		_, errText, code = runForge(t, "auth", "status", "extra")
		if want := "forge: unexpected argument for auth status: extra\n"; errText != want || code != 1 {
			t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
		}
	})
}

// --- token verify (retired shell-suite scenarios) ---

func TestTokenVerify(t *testing.T) {
	t.Run("valid token from stdin", func(t *testing.T) {
		srv := newRecordingServer(t, userRoutes("testuser"))
		useServer(t, srv)
		// The configured credential must be ignored entirely.
		useToken(t, fakeOtherToken)
		useStdin(t, fakeToken)

		out, errText, code := runForge(t, "token", "verify")
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		if want := "Token valid: authenticated as testuser\n"; out != want {
			t.Errorf("stdout = %q, want %q", out, want)
		}
		if errText != "" {
			t.Errorf("stderr = %q, want empty", errText)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/user")
		if got := srv.requests()[0].Authorization; got != "token "+fakeToken {
			t.Error("token verify sent the configured token instead of the candidate")
		}
	})

	t.Run("trailing whitespace is stripped", func(t *testing.T) {
		for _, suffix := range []string{"\n", "\r", "\n\n"} {
			srv := newRecordingServer(t, userRoutes("testuser"))
			useServer(t, srv)
			useStdin(t, fakeToken+suffix)

			out, _, code := runForge(t, "token", "verify")
			if code != 0 || out != "Token valid: authenticated as testuser\n" {
				t.Errorf("suffix %q: got (%q, %d)", suffix, out, code)
			}
			if got := srv.requests()[0].Authorization; got != "token "+fakeToken {
				t.Errorf("suffix %q: trailing bytes reached the header", suffix)
			}
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"/api/v1/user": {Status: 401, Body: `{"message":"Unauthorized"}`},
		})
		useServer(t, srv)
		useStdin(t, fakeToken)

		out, errText, code := runForge(t, "token", "verify")
		if code != 1 || out != "" || errText != "Token invalid: unauthorized\n" {
			t.Errorf("got (%q, %q, %d)", out, errText, code)
		}
	})

	t.Run("empty stdin", func(t *testing.T) {
		useStdin(t, "")
		_, errText, code := runForge(t, "token", "verify")
		if want := "forge: stdin is empty\n"; errText != want || code != 1 {
			t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
		}
	})

	invalid := []struct{ name, value string }{
		{"newline", "valid\ninjection"},
		{"quote", `valid"injection`},
		{"backslash", `valid\injection`},
	}
	for _, tt := range invalid {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			useStdin(t, tt.value)
			_, errText, code := runForge(t, "token", "verify")
			want := "forge: stdin contains invalid characters (newline, quote, or backslash)\n"
			if errText != want || code != 1 {
				t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
			}
		})
	}

	t.Run("works without a configured credential", func(t *testing.T) {
		srv := newRecordingServer(t, userRoutes("testuser"))
		useServer(t, srv)
		useNoCredentials(t)
		useStdin(t, fakeToken)

		out, _, code := runForge(t, "token", "verify")
		if code != 0 || out != "Token valid: authenticated as testuser\n" {
			t.Errorf("got (%q, %d)", out, code)
		}
	})

	t.Run("rejects options and arguments", func(t *testing.T) {
		useStdin(t, fakeToken)
		_, errText, code := runForge(t, "token", "verify", "--token", "foo")
		if want := "forge: unknown option for token verify: --token\n"; errText != want || code != 1 {
			t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
		}
		_, errText, code = runForge(t, "token", "verify", "extra")
		if want := "forge: unexpected argument for token verify: extra\n"; errText != want || code != 1 {
			t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
		}
	})
}

// --- api() error rendering (retired shell-suite scenarios) ---

func TestAPIErrorRendering(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/issues/403": {
			Status: 403,
			Body:   `{"message":"user should have a permission to write to a repo","url":"https://forge.example/api/swagger"}`,
		},
		"/api/v1/repos/acme/widget/issues/308": {Status: 308, Header: map[string]string{"Location": "https://elsewhere/x"}},
		"/api/v1/repos/acme/widget/issues/422": {Status: 422, Body: `{"errors":["nope"]}`},
		"/api/v1/repos/acme/widget/issues/500": {Status: 500},
		"/api/v1/repos/acme/widget/issues/502": {
			Status: 502,
			Body:   "<html>\n<body>\x07\x1b[31mred\r\n</body>\n</html>\n",
		},
		"/api/v1/repos/acme/widget/issues/413": {Status: 400, Body: `{"message":"` + strings.Repeat("A", 300) + `"}`},
		"/api/v1/repos/acme/widget/issues/20":  {Body: `{"title":"Fix backup"}` + "\n"},
	})
	useServer(t, srv)
	useToken(t, fakeToken)

	tests := []struct {
		name     string
		path     string
		wantErr  string
		wantCode int
	}{
		{
			"server message",
			"/repos/acme/widget/issues/403",
			"forge: GET /repos/acme/widget/issues/403 failed: HTTP 403: user should have a permission to write to a repo\n",
			22,
		},
		{
			"redirect is a failure",
			"/repos/acme/widget/issues/308",
			"forge: GET /repos/acme/widget/issues/308 failed: HTTP 308\n",
			22,
		},
		{
			"body without a message falls back to the raw body",
			"/repos/acme/widget/issues/422",
			"forge: GET /repos/acme/widget/issues/422 failed: HTTP 422: {\"errors\":[\"nope\"]}\n",
			22,
		},
		{
			"empty body leaves just the status",
			"/repos/acme/widget/issues/500",
			"forge: GET /repos/acme/widget/issues/500 failed: HTTP 500\n",
			22,
		},
		{
			"control bytes are stripped and newlines flattened",
			"/repos/acme/widget/issues/502",
			"forge: GET /repos/acme/widget/issues/502 failed: HTTP 502: <html> <body>[31mred  </body> </html>\n",
			22,
		},
		{
			"message is truncated to 200 bytes",
			"/repos/acme/widget/issues/413",
			"forge: GET /repos/acme/widget/issues/413 failed: HTTP 400: " + strings.Repeat("A", 200) + "\n",
			22,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errText, code := runCall(t, func() { api("GET", tt.path, nil) })
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			if errText != tt.wantErr {
				t.Errorf("stderr mismatch\ngot:  %q\nwant: %q", errText, tt.wantErr)
			}
		})
	}

	// api-errors.sh: a successful read still returns its body, and requests
	// after the first still run.
	t.Run("success returns the body", func(t *testing.T) {
		var got []byte
		_, errText, code := runCall(t, func() { got = api("GET", "/repos/acme/widget/issues/20", nil) })
		if code != 0 || errText != "" {
			t.Errorf("got (%q, %d), want no error", errText, code)
		}
		// The trailing newline is stripped, exactly as command substitution
		// would have stripped it.
		if want := `{"title":"Fix backup"}`; string(got) != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})

	// The Content-Type header goes out on every api() call, GET included.
	last := srv.requests()[srv.count()-1]
	if last.ContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", last.ContentType)
	}
	if !last.hasAuth() {
		t.Error("api() sent no Authorization header")
	}
}

func TestAPITransportFailure(t *testing.T) {
	// A server that is started only to be closed gives a port nothing listens
	// on, which is connection refused: curl exit 7.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	t.Setenv("FORGE_URL", base)
	useToken(t, fakeToken)

	out, errText, code := runCall(t, func() { api("GET", "/repos/acme/widget/issues/599", nil) })
	if code != 7 {
		t.Errorf("exit code = %d, want 7 (curl's failed-to-connect)", code)
	}
	if code == 22 {
		t.Error("a transport failure must not report the HTTP failure code")
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
	want := "forge: GET /repos/acme/widget/issues/599 failed: curl exit 7\n"
	if errText != want {
		t.Errorf("stderr = %q, want %q", errText, want)
	}
}

// A URL curl's own parser refuses never becomes a request. Found by
// differential testing against the bash tool, where
//
//	FORGE_URL=http://127.0.0.1:9 ./forge -R acme/widget pr view "1 2"
//
// prints the line below and exits 3 with nothing on the wire; Go would have
// percent-encoded the space and sent the request to the real forge. The path
// in the message is the raw one, spaces and all, because bash interpolates
// what it passed curl.
func TestCurlRejectedURLMakesNoRequest(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			"space in a positional number",
			[]string{"-R", "acme/widget", "pr", "view", "1 2"},
			"forge: GET /repos/acme/widget/pulls/1 2 failed: curl exit 3\n",
		},
		{
			"space in the repo slug",
			[]string{"-R", "acme/wid get", "pr", "list"},
			"forge: GET /repos/acme/wid get/pulls?state=open&limit=50&page=1 failed: curl exit 3\n",
		},
		// The reject class is every byte up to and including the space, plus
		// DEL -- not the space alone.
		{
			"tab in the repo slug",
			[]string{"-R", "acme/wid\tget", "pr", "list"},
			"forge: GET /repos/acme/wid\tget/pulls?state=open&limit=50&page=1 failed: curl exit 3\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newRecordingServer(t, map[string]cannedResponse{})
			useServer(t, srv)
			useToken(t, fakeToken)
			useNoInferRepo(t)

			out, errText, code := runForge(t, tt.args...)
			if code != 3 {
				t.Errorf("exit code = %d, want 3 (curl's malformed URL)", code)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			if errText != tt.want {
				t.Errorf("stderr = %q, want %q", errText, tt.want)
			}
			if n := srv.count(); n != 0 {
				t.Errorf("%d requests recorded, want 0: curl rejects the URL before it connects", n)
			}
		})
	}
}

// diverges from bash: curl's URL globbing is not emulated.
//
// To curl, `{1,2}` in a URL is an expansion: two requests with the braces
// gone, decided before its URL parser ever runs (and an unbalanced `{` is its
// own exit 3). bash therefore reports `curl exit 7` here after two refused
// connections. Go percent-encodes the braces and sends exactly one request, so
// a forge that has no such pull request answers 404 and the run exits 22.
// Emulating the glob engine is out of scope; this pins the divergence so it is
// visible if it ever moves.
func TestURLGlobCharactersAreSentAsOnePercentEncodedRequest(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{})
	srv.enqueue(cannedResponse{Status: 404, Body: `{"message":"pull request does not exist"}`})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	_, errText, code := runForge(t, "-R", "acme/widget", "pr", "view", "{1,2}")
	if code != 22 {
		t.Errorf("exit code = %d, want 22: the forge answered", code)
	}
	want := "forge: GET /repos/acme/widget/pulls/{1,2} failed: HTTP 404: pull request does not exist\n"
	if errText != want {
		t.Errorf("stderr = %q, want %q", errText, want)
	}
	if n := srv.count(); n != 1 {
		t.Fatalf("%d requests recorded, want 1: the braces must not expand", n)
	}
	srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/pulls/%7B1,2%7D")
}

func TestCurlExitCodeMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"dns failure", &url.Error{Op: "Get", URL: "http://x/", Err: &net.DNSError{Err: "no such host", IsNotFound: true}}, 6},
		{"malformed url", &url.Error{Op: "parse", URL: "://x", Err: errors.New("missing protocol scheme")}, 3},
		// curl reports a resolution timeout as a resolution failure, not as
		// its generic timeout.
		{"dns timeout", &url.Error{Op: "Get", URL: "http://x/", Err: &net.DNSError{Err: "timeout", IsTimeout: true}}, 6},
		{"unknown", errors.New("something else"), 7},
	}
	for _, tt := range tests {
		if got := curlExitCode(tt.err); got != tt.want {
			t.Errorf("%s: curlExitCode = %d, want %d", tt.name, got, tt.want)
		}
	}
}

// apiTry is what a guarded bash call site gets: the failure line, but no exit.
func TestAPITryReportsWithoutExiting(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"DELETE /api/v1/repos/acme/widget/branches/topic": {Status: 404, Body: `{"message":"branch does not exist"}`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)

	var rc int
	out, errText, code := runCall(t, func() {
		_, rc = apiTry("DELETE", "/repos/acme/widget/branches/topic", nil)
		fmt.Fprint(stdout, "still running")
	})
	if code != 0 {
		t.Errorf("runCall code = %d, want 0: apiTry must not exit", code)
	}
	if rc != 22 {
		t.Errorf("apiTry rc = %d, want 22", rc)
	}
	if out != "still running" {
		t.Errorf("stdout = %q, want the caller to have continued", out)
	}
	want := "forge: DELETE /repos/acme/widget/branches/topic failed: HTTP 404: branch does not exist\n"
	if errText != want {
		t.Errorf("stderr = %q, want %q", errText, want)
	}
}

// The queue answers the same path differently on consecutive calls, which is
// how a test models a resource changing between requests.
func TestRecordingServerEnqueue(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{})
	srv.enqueue(
		cannedResponse{Body: `{"state":"open"}`},
		cannedResponse{Body: `{"state":"closed"}`},
	)
	useServer(t, srv)
	useToken(t, fakeToken)

	var first, second []byte
	_, _, code := runCall(t, func() {
		first = api("GET", "/repos/acme/widget/issues/20", nil)
		second = api("GET", "/repos/acme/widget/issues/20", nil)
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if string(first) != `{"state":"open"}` || string(second) != `{"state":"closed"}` {
		t.Errorf("queued responses out of order: %q then %q", first, second)
	}
	if srv.count() != 2 {
		t.Errorf("recorded %d requests, want 2", srv.count())
	}
	srv.assertRequest(t, 1, "GET", "/api/v1/repos/acme/widget/issues/20")
}

// --- Repo inference (internal/gitremote wiring) ---

func TestRequireRepo(t *testing.T) {
	t.Run("uses the inferred slug", func(t *testing.T) {
		useInferRepo(t, "acme/widget", nil)
		out, errText, code := runCall(t, func() {
			requireRepo()
			fmt.Fprint(stdout, repoOpt)
		})
		if code != 0 || out != "acme/widget" || errText != "" {
			t.Errorf("got (%q, %q, %d)", out, errText, code)
		}
	})

	t.Run("explicit repo wins", func(t *testing.T) {
		useNoInferRepo(t)
		out, _, code := runCall(t, func() {
			repoOpt = "other/repo"
			requireRepo()
			fmt.Fprint(stdout, repoOpt)
		})
		if code != 0 || out != "other/repo" {
			t.Errorf("got (%q, %d)", out, code)
		}
	})

	t.Run("no git repo", func(t *testing.T) {
		useInferRepo(t, "", gitremote.ErrNoOrigin)
		out, errText, code := runCall(t, func() { requireRepo() })
		want := "forge: not in a git repo and --repo not specified\n"
		if code != 1 || out != "" || errText != want {
			t.Errorf("got (%q, %q, %d), want (%q, %q, 1)", out, errText, code, "", want)
		}
	})

	t.Run("remote url without an owner/repo tail", func(t *testing.T) {
		// bash: grep printed nothing, so the assignment failed under errexit
		// and the run ended with no message at all.
		useInferRepo(t, "", nil)
		out, errText, code := runCall(t, func() { requireRepo() })
		if code != 1 || out != "" || errText != "" {
			t.Errorf("got (%q, %q, %d), want silence and 1", out, errText, code)
		}
	})
}

// --- Helper parity ---

// jqURI must agree with `jq -sRr @uri`; the expectations were taken from jq.
func TestJQURI(t *testing.T) {
	tests := []struct{ in, want string }{
		{"July batch", "July%20batch"},
		{"a/b", "a%2Fb"},
		{"a+b c", "a%2Bb%20c"},
		{"ünïcødé", "%C3%BCn%C3%AFc%C3%B8d%C3%A9"},
		{"x~_-.y", "x~_-.y"},
		{"100%", "100%25"},
		{"quote'", "quote%27"},
		{"*!()", "%2A%21%28%29"},
		{"a\nb", "a%0Ab"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := jqURI(tt.in); got != tt.want {
			t.Errorf("jqURI(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsInteger(t *testing.T) {
	yes := []string{"0", "1", "007", "99999999999999999999"}
	no := []string{"", "-1", "1.0", "1a", " 1", "1 ", "+1", "1\n2"}
	for _, s := range yes {
		if !isInteger(s) {
			t.Errorf("isInteger(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isInteger(s) {
			t.Errorf("isInteger(%q) = true, want false", s)
		}
	}
}

func TestParsePositiveInt(t *testing.T) {
	tests := []struct {
		value string
		want  string
		fails bool
	}{
		{"30", "30", false},
		{"1", "1", false},
		// bash arithmetic reads a leading zero as octal, and the original text
		// is what reaches the URL.
		{"0755", "0755", false},
		{"007", "007", false},
		// "08" is not a valid octal literal, so the comparison fails.
		{"08", "", true},
		{"0", "", true},
		{"00", "", true},
		{"abc", "", true},
		{"-1", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		out, errText, code := runCall(t, func() { fmt.Fprint(stdout, parsePositiveInt("-L", tt.value)) })
		if tt.fails {
			if code != 1 || errText != "forge: -L must be a positive integer\n" {
				t.Errorf("parsePositiveInt(%q) = (%q, %q, %d), want the positive-integer error", tt.value, out, errText, code)
			}
			continue
		}
		if code != 0 || out != tt.want {
			t.Errorf("parsePositiveInt(%q) = (%q, %q, %d), want %q", tt.value, out, errText, code, tt.want)
		}
	}
}

func TestNormalizeColor(t *testing.T) {
	ok := []struct{ in, want string }{
		{"#FF0000", "#ff0000"},
		{"ff0000", "#ff0000"},
		{"AbCdEf", "#abcdef"},
	}
	for _, tt := range ok {
		out, _, code := runCall(t, func() { fmt.Fprint(stdout, normalizeColor(tt.in)) })
		if code != 0 || out != tt.want {
			t.Errorf("normalizeColor(%q) = (%q, %d), want %q", tt.in, out, code, tt.want)
		}
	}
	for _, bad := range []string{"#GGGGGG", "12345", "1234567", "", "##ff0000"} {
		_, errText, code := runCall(t, func() { normalizeColor(bad) })
		if code != 1 || errText != "forge: color must be a 6-digit hex value\n" {
			t.Errorf("normalizeColor(%q) = (%q, %d), want the hex error", bad, errText, code)
		}
	}
}

func TestRandomLabelColor(t *testing.T) {
	got := randomLabelColor()
	if len(got) != 7 || got[0] != '#' {
		t.Fatalf("randomLabelColor() = %q, want a 7-character #rrggbb", got)
	}
	for _, c := range got[1:] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("randomLabelColor() = %q, want lower-case hex", got)
		}
	}
}

func TestNormalizeDueOn(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2026-07-31", "2026-07-31T00:00:00Z"},
		{"2026-07-31T12:00:00Z", "2026-07-31T12:00:00Z"},
		{"tomorrow", "tomorrow"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizeDueOn(tt.in); got != tt.want {
			t.Errorf("normalizeDueOn(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAppendCSVValues(t *testing.T) {
	t.Run("splits on commas", func(t *testing.T) {
		var dest []string
		_, _, code := runCall(t, func() { appendCSVValues(&dest, "-l", "bug,triage") })
		if code != 0 || strings.Join(dest, "|") != "bug|triage" {
			t.Errorf("dest = %v, code = %d", dest, code)
		}
	})

	t.Run("an empty value adds nothing", func(t *testing.T) {
		var dest []string
		_, _, code := runCall(t, func() { appendCSVValues(&dest, "-l", "") })
		if code != 0 || len(dest) != 0 {
			t.Errorf("dest = %v, code = %d, want no items and no error", dest, code)
		}
	})

	for _, bad := range []string{"bug,", ",bug", "bug,,triage", ","} {
		_, errText, code := runCall(t, func() {
			var dest []string
			appendCSVValues(&dest, "-l", bad)
		})
		if code != 1 || errText != "forge: -l cannot contain an empty value\n" {
			t.Errorf("appendCSVValues(%q) = (%q, %d), want the empty-value error", bad, errText, code)
		}
	}
}

func TestJoinCSVValues(t *testing.T) {
	if got := joinCSVValues([]string{"bug", "triage"}); got != "bug,triage" {
		t.Errorf("joinCSVValues = %q", got)
	}
	if got := joinCSVValues(nil); got != "" {
		t.Errorf("joinCSVValues(nil) = %q, want empty", got)
	}
}

func TestJSONMixedLabelArrayFromArgs(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{nil, "[]"},
		{[]string{"1", "2"}, "[1,2]"},
		{[]string{"bug"}, `["bug"]`},
		{[]string{"1", "bug"}, `[1,"bug"]`},
		{[]string{`say "hi"`}, `["say \"hi\""]`},
		{[]string{"a<b>c&d"}, `["a<b>c&d"]`},
	}
	for _, tt := range tests {
		if got := jsonMixedLabelArrayFromArgs(tt.in); got != tt.want {
			t.Errorf("jsonMixedLabelArrayFromArgs(%v) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestContainsHelpFlag(t *testing.T) {
	if !containsHelpFlag([]string{"5", "--help"}) || !containsHelpFlag([]string{"-h"}) {
		t.Error("help flags not detected")
	}
	if containsHelpFlag([]string{"-help", "help", "--h"}) {
		t.Error("non-help arguments detected as help")
	}
}

func TestSetBodyOption(t *testing.T) {
	t.Run("inline body", func(t *testing.T) {
		_, _, code := runCall(t, func() {
			setBodyOption("-b", "hello")
			fmt.Fprint(stdout, bodyOpt)
		})
		if code != 0 || bodyOpt != "hello" {
			t.Errorf("bodyOpt = %q, code = %d", bodyOpt, code)
		}
	})

	t.Run("body file keeps trailing newlines", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "body.md")
		if err := os.WriteFile(path, []byte("one\ntwo\n\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var got string
		_, _, code := runCall(t, func() {
			setBodyOption("--body-file", path)
			got = bodyOpt
		})
		if code != 0 || got != "one\ntwo\n\n" {
			t.Errorf("bodyOpt = %q, code = %d", got, code)
		}
	})

	t.Run("body file from stdin", func(t *testing.T) {
		useStdin(t, "piped\n\n")
		var got string
		_, _, code := runCall(t, func() {
			setBodyOption("-F", "-")
			got = bodyOpt
		})
		if code != 0 || got != "piped\n\n" {
			t.Errorf("bodyOpt = %q, code = %d", got, code)
		}
	})

	t.Run("unreadable body file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope.md")
		_, errText, code := runCall(t, func() { setBodyOption("-F", missing) })
		if want := "forge: cannot read body file: " + missing + "\n"; errText != want || code != 1 {
			t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
		}
	})

	t.Run("body options do not combine", func(t *testing.T) {
		_, errText, code := runCall(t, func() {
			setBodyOption("-b", "hello")
			setBodyOption("-F", "-")
		})
		if want := "forge: -F cannot be combined with -b\n"; errText != want || code != 1 {
			t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
		}
	})
}

func TestParseRepoArgs(t *testing.T) {
	t.Run("repo only", func(t *testing.T) {
		_, _, code := runCall(t, func() { parseRepoOnlyArgs("pr list", []string{"-R", "acme/widget"}) })
		if code != 0 || repoOpt != "acme/widget" {
			t.Errorf("repoOpt = %q, code = %d", repoOpt, code)
		}
	})

	t.Run("rejects options and arguments", func(t *testing.T) {
		_, errText, code := runCall(t, func() { parseRepoOnlyArgs("pr list", []string{"--nope"}) })
		if want := "forge: unknown option for pr list: --nope\n"; errText != want || code != 1 {
			t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
		}
		_, errText, code = runCall(t, func() { parseRepoOnlyArgs("pr list", []string{"stray"}) })
		if want := "forge: unexpected argument for pr list: stray\n"; errText != want || code != 1 {
			t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
		}
	})

	t.Run("missing repo value", func(t *testing.T) {
		_, errText, code := runCall(t, func() { parseRepoOnlyArgs("pr list", []string{"--repo"}) })
		if want := "forge: --repo requires a value\n"; errText != want || code != 1 {
			t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
		}
	})

	t.Run("positionals", func(t *testing.T) {
		var got []string
		_, _, code := runCall(t, func() {
			parseRepoPositionals("pr view", []string{"12", "-R", "acme/widget", "extra"})
			got = positionalArgs
		})
		if code != 0 || strings.Join(got, "|") != "12|extra" || repoOpt != "acme/widget" {
			t.Errorf("positionalArgs = %v, repoOpt = %q, code = %d", got, repoOpt, code)
		}
	})
}

func truncate(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}
