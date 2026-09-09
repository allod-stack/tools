package main

// Retained API-error scenarios from the retired shell suite, plus tests that
// pin the corrected allod/tools#144 behavior.
//
// Historical shell cases intentionally omitted as already covered (see
// cmd/forge/harness_test.go: TestAPIErrorRendering, TestAPITransportFailure,
// TestCurlExitCodeMapping, TestRecordingServerEnqueue; and
// internal/forgeapi/client_test.go for the transport layer beneath them):
//
//   - "reports the HTTP status on an API error"
//   - "reports the server's own error message"
//   - "names the method and path that failed"
//   - "an HTTP error exits 22"
//     All four bash assertions above run `issue view 403` and check
//     substrings of one stderr line and the exit code. issueView (issue.go)
//     calls api("GET", "/repos/"+repoOpt+"/issues/"+number, nil) as its very
//     first action and nothing runs before it, so on a non-2xx response the
//     CLI-level failure text is exactly what api() itself produced.
//     TestAPIErrorRendering's "server message" sub-test already pins that
//     text byte-exact (stderr and exit 22) for this same status, message and
//     path shape, which is strictly stronger than bash's substring checks.
//   - "reports a redirect instead of returning an empty body" (issue view 308)
//     Same reasoning: TestAPIErrorRendering's "redirect is a failure"
//     sub-test pins the exact 308 text and exit 22.
//   - "a transport failure returns curl's own exit code" (issue view 599, rc==6)
//   - "names curl's exit code when the request never completed" (contains "curl exit 6")
//     Bash's mock curl fakes exit 6 (a DNS-resolution-failure shape) for a
//     fixed URL. Go's classifier (curlExitCode) can only be driven to 6 by a
//     genuine DNS resolution failure, which needs real network access and
//     is not hermetic: depending on the sandbox's resolver it may resolve
//     unexpectedly, hang, or fail with a different error shape entirely.
//     Per the brief, the classifier is pinned directly instead of attempting
//     that end-to-end: TestCurlExitCodeMapping (harness_test.go) proves
//     curlExitCode maps a *net.DNSError to 6 (both for IsNotFound and for
//     IsTimeout), and TestAPITransportFailure proves the end-to-end
//     rendering ("forge: %s %s failed: curl exit %d\n") for a different,
//     hermetically-producible transport failure (connection refused, rc 7).
//     There is exactly one call site for that Fprintf (apiTry, api.go), so
//     the two together cover the same code this scenario exercises.
//   - "a successful read still returns its body" / "requests after the
//     first still run" (issue view 20 contains "Fix backup" and "Issue note")
//     The api()-level half — a successful call returns its body unchanged,
//     and a second call to the same path still executes and is recorded —
//     is pinned by TestAPIErrorRendering's "success returns the body"
//     sub-test and by TestRecordingServerEnqueue. The rest of this scenario
//     is a check that `issue view`'s full rendering (issue title, then a
//     second request for comments) was not broken by splitting status from
//     body; that behavior is pinned by read_test.go, not this file.
//
// That accounts for every retired API-error scenario. What follows pins
// behavior the shell suite never covered.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// diverges from bash: allod/tools#144
//
// An empty token file must fail before any network request is attempted, for
// every API command — not just the credential commands harness_test.go's
// TestLoadTokenErrors exercises through `auth status`. `label list` stands in
// for "any API command": it reaches api() by the shortest path.
func TestEmptyTokenFileMakesNoRequest(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{})
	useServer(t, srv)
	path := useTokenFile(t, "")

	out, errText, code := runForge(t, "-R", "acme/widget", "label", "list")
	want := "forge: " + path + " is empty\n"
	if code != 1 || out != "" || errText != want {
		t.Errorf("got (%q, %q, %d), want (%q, %q, 1)", out, errText, code, "", want)
	}
	if n := srv.count(); n != 0 {
		t.Errorf("recording server received %d requests, want 0", n)
	}
}

// diverges from bash: allod/tools#144
//
// A token file that exists but cannot be read must be treated exactly like a
// missing one (bash: `[ -r "$FORGE_TOKEN_FILE" ]`, mirrored by
// fileReadable), not like an empty one — "no token found", not "is empty" —
// and must likewise fail before any network request. Representable without
// spawning any subprocess: os.Chmod alone does it, and this process runs
// unprivileged, so removing all permission bits genuinely blocks its own
// read.
func TestUnreadableTokenFileMakesNoRequest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod cannot make a file unreadable to its owner")
	}
	path := useTokenFile(t, fakeToken)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	srv := newRecordingServer(t, map[string]cannedResponse{})
	useServer(t, srv)

	out, errText, code := runForge(t, "-R", "acme/widget", "label", "list")
	want := "forge: no token found — ensure " + path + " exists or point FORGE_TOKEN_FILE at a mode-0600 token file\n"
	if code != 1 || out != "" || errText != want {
		t.Errorf("got (%q, %q, %d), want (%q, %q, 1)", out, errText, code, "", want)
	}
	if n := srv.count(); n != 0 {
		t.Errorf("recording server received %d requests, want 0", n)
	}
}

// diverges from bash: allod/tools#144
//
// A transport failure while resolving a label by name must report the
// transport failure, not "label not found": resolveLabelID's not-found die
// (resolvers.go) is a distinct code path from api()'s own exit, and must
// never be reached when the lookup request never got an answer at all.
func TestLabelEditByNameTransportFailureIsNotNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // refused, not merely unrouted: guarantees curl exit 7.

	t.Setenv("FORGE_URL", base)
	useToken(t, fakeToken)

	out, errText, code := runForge(t, "-R", "acme/widget", "label", "edit", "bug", "--color", "ff0000")
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
	want := "forge: GET /repos/acme/widget/labels?limit=100 failed: curl exit 7\n"
	if errText != want {
		t.Errorf("stderr = %q, want %q", errText, want)
	}
	if strings.Contains(errText, "not found") {
		t.Error("stderr must not claim the label was not found")
	}
}

// diverges from bash: allod/tools#144
//
// Same as above for resolving a milestone by title (resolveMilestoneID,
// resolvers.go): a transport failure must not be reported as "milestone not
// found".
func TestMilestoneViewByTitleTransportFailureIsNotNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	t.Setenv("FORGE_URL", base)
	useToken(t, fakeToken)

	out, errText, code := runForge(t, "-R", "acme/widget", "milestone", "view", "July batch")
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
	want := "forge: GET /repos/acme/widget/milestones?state=all&name=July%20batch&limit=100 failed: curl exit 7\n"
	if errText != want {
		t.Errorf("stderr = %q, want %q", errText, want)
	}
	if strings.Contains(errText, "not found") {
		t.Error("stderr must not claim the milestone was not found")
	}
}
