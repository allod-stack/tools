package main

// Differential-parity tests.
//
// Everything here was found by running the retired Bash `forge` and the Go
// port side by side against the same local responder. Every expectation is the
// byte-exact historical output for the same argv and fixture, not what the port
// "should" do. These are corners where shell machinery was observable and the
// old shell suite had no coverage.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Command substitution drops NUL bytes (forge lines 222-228) ---
//
// set_body_option reads through `body_with_sentinel="$(cat; printf '\x1f')"`.
// The \x1f sentinel saves the trailing newlines, but nothing saves a NUL:
// command substitution builds a C string, so every NUL is gone before BODY is
// assigned and `printf 'a\0b' | forge ... -F -` puts "ab" on the wire.
//
// bash also writes "warning: command substitution: ignored null byte in
// input" to stderr for each such read. That line names the script and the line
// number inside it, so it is not reproducible here and stays absent; the
// payload is what this pins.
func TestBodyReadsStripNULBytes(t *testing.T) {
	const want = `{"body":"ab"}`

	t.Run("from stdin", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"POST /api/v1/repos/acme/widget/issues/1/comments": {Body: `{"html_url":"u"}`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)
		useStdin(t, "a\x00b")

		if _, errText, code := runForge(t, "-R", "acme/widget", "issue", "comment", "1", "-F", "-"); code != 0 {
			t.Fatalf("exit = %d, stderr = %q", code, errText)
		}
		if got := string(srv.requests()[0].Body); got != want {
			t.Errorf("request body = %s, want %s", got, want)
		}
	})

	t.Run("from a file", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"POST /api/v1/repos/acme/widget/issues/1/comments": {Body: `{"html_url":"u"}`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		path := filepath.Join(t.TempDir(), "body.md")
		if err := os.WriteFile(path, []byte("a\x00b"), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, errText, code := runForge(t, "-R", "acme/widget", "issue", "comment", "1", "-F", path); code != 0 {
			t.Fatalf("exit = %d, stderr = %q", code, errText)
		}
		if got := string(srv.requests()[0].Body); got != want {
			t.Errorf("request body = %s, want %s", got, want)
		}
	})

	// The sentinel still does its job: trailing newlines survive, NULs do not.
	t.Run("trailing newlines still survive", func(t *testing.T) {
		useStdin(t, "one\x00\ntwo\n\n")
		var got string
		if _, _, code := runCall(t, func() { setBodyOption("-F", "-"); got = bodyOpt }); code != 0 {
			t.Fatalf("exit = %d", code)
		}
		if want := "one\ntwo\n\n"; got != want {
			t.Errorf("bodyOpt = %q, want %q", got, want)
		}
	})
}

// --- jq's --argjson normalizes leading zeroes (forge lines 279-288, 642) ---
//
// jq's JSON parser accepts leading zeroes where the grammar does not, and
// prints the number back canonically. Verified against jq 1.8.1 directly
// (`jq -n --argjson v 001 '$v'` answers 1, `--argjson v 00` answers 0,
// `--argjson v 0009007199254740993` answers 9007199254740993) and end to end
// against ./forge, whose `issue labels 2 --add 001` puts {"labels":[1]} on the
// wire and exits 0.
func TestArgJSONNormalizesLeadingZeroes(t *testing.T) {
	t.Run("label array", func(t *testing.T) {
		tests := []struct{ in, want string }{
			{"001", "[1]"},
			{"007", "[7]"},
			{"00", "[0]"},
			{"0000", "[0]"},
			// jq keeps every remaining digit, past the range of a double.
			{"0009007199254740993", "[9007199254740993]"},
		}
		for _, tt := range tests {
			if got := jsonMixedLabelArrayFromArgs([]string{tt.in}); got != tt.want {
				t.Errorf("jsonMixedLabelArrayFromArgs([%q]) = %s, want %s", tt.in, got, tt.want)
			}
		}
		// A non-integer is still a jq --arg string, zeroes and all.
		if got := jsonMixedLabelArrayFromArgs([]string{"007-bug"}); got != `["007-bug"]` {
			t.Errorf("jsonMixedLabelArrayFromArgs = %s, want [\"007-bug\"]", got)
		}
	})

	t.Run("issue labels --add sends a normalized number", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"POST /api/v1/repos/acme/widget/issues/2/labels": {Body: `[]`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "issue", "labels", "2", "--add", "001")
		if code != 0 || out != "Issue #2 labels: -\n" || errText != "" {
			t.Fatalf("got (%q, %q, %d), want the labels line and 0", out, errText, code)
		}
		if got, want := string(srv.requests()[0].Body), `{"labels":[1]}`; got != want {
			t.Errorf("request body = %s, want %s", got, want)
		}
	})

	t.Run("issue milestone sends a normalized number", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"PATCH /api/v1/repos/acme/widget/issues/2": {Body: `{"html_url":"u"}`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		if _, errText, code := runForge(t, "-R", "acme/widget", "issue", "milestone", "2", "007"); code != 0 {
			t.Fatalf("exit = %d, stderr = %q", code, errText)
		}
		if got, want := string(srv.requests()[0].Body), `{"milestone":7}`; got != want {
			t.Errorf("request body = %s, want %s", got, want)
		}
	})

	t.Run("issue create normalizes both label and milestone IDs", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"POST /api/v1/repos/acme/widget/issues": {Body: `{"html_url":"u"}`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		if _, errText, code := runForge(t, "-R", "acme/widget", "issue", "create", "-t", "T", "-l", "0012", "-m", "007"); code != 0 {
			t.Fatalf("exit = %d, stderr = %q", code, errText)
		}
		if got, want := string(srv.requests()[0].Body), `{"title":"T","body":"","labels":[12],"milestone":7}`; got != want {
			t.Errorf("request body = %s, want %s", got, want)
		}
	})

	// A numeric ID keeps its leading zeroes everywhere it is *not* --argjson:
	// resolve_milestone_id prints "$identifier" and the caller interpolates it
	// straight into the URL.
	t.Run("the URL keeps the literal spelling", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/milestones/007": {Body: `{"id":7,"title":"T","state":"open"}`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		if _, errText, code := runForge(t, "-R", "acme/widget", "milestone", "view", "007"); code != 0 {
			t.Fatalf("exit = %d, stderr = %q", code, errText)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/milestones/007")
	})

	t.Run("pr reply accepts a leading-zero comment id", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/pulls/1/reviews":             {Body: `[{"id":7,"comments_count":1}]`},
			"GET /api/v1/repos/acme/widget/pulls/1/reviews/7/comments":  {Body: `[{"id":1,"path":"x","position":2}]`},
			"POST /api/v1/repos/acme/widget/pulls/1/reviews/7/comments": {Body: `{"html_url":"ok"}`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "reply", "1", "001", "-b", "hi")
		if code != 0 || out != "Reply posted: ok\n" || errText != "" {
			t.Errorf("got (%q, %q, %d), want (\"Reply posted: ok\\n\", \"\", 0)", out, errText, code)
		}
	})
}

// --- jq keeps integer precision past 2^53 (forge line 642) ---
//
// `select(.id == $cid)` compares literals, not doubles: jq 1.8.1 answers
// nothing for `[{"id":9007199254740992}] | first(.[] | select(.id ==
// 9007199254740993))`, so bash reports the comment as missing and exits 1.
func TestPRReplyCommentIDComparisonKeepsIntegerPrecision(t *testing.T) {
	routes := func() map[string]cannedResponse {
		return map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/pulls/1/reviews":             {Body: `[{"id":7,"comments_count":1}]`},
			"GET /api/v1/repos/acme/widget/pulls/1/reviews/7/comments":  {Body: `[{"id":9007199254740992,"path":"x","position":2}]`},
			"POST /api/v1/repos/acme/widget/pulls/1/reviews/7/comments": {Body: `{"html_url":"WRONG"}`},
		}
	}

	t.Run("the neighbouring id is not a match", func(t *testing.T) {
		srv := newRecordingServer(t, routes())
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "reply", "1", "9007199254740993", "-b", "hi")
		want := "forge: comment 9007199254740993 not found on PR #1\n"
		if code != 1 || out != "" || errText != want {
			t.Errorf("got (%q, %q, %d), want (%q, %q, 1)", out, errText, code, "", want)
		}
		if n := srv.count(); n != 2 {
			t.Errorf("%d requests, want 2: nothing may be posted for a comment that was not found", n)
		}
	})

	t.Run("the exact id still matches", func(t *testing.T) {
		srv := newRecordingServer(t, routes())
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, _, code := runForge(t, "-R", "acme/widget", "pr", "reply", "1", "9007199254740992", "-b", "hi")
		if code != 0 || out != "Reply posted: WRONG\n" {
			t.Errorf("got (%q, %d), want the posted line and 0", out, code)
		}
	})

	// jq's == still ignores spelling for values a double can hold: 99 == 99.0.
	t.Run("integer and decimal spellings of one value are equal", func(t *testing.T) {
		if !jqEqualValues(mustJSON([]byte("99")), mustJSON([]byte("99.0"))) {
			t.Error("jqEqualValues(99, 99.0) = false, want true")
		}
	})
}

// --- `read` sees only the first line (forge lines 1309, 1334, 771) ---
//
// resolve_issue_target and normalize_issue_reference both split the URL with
// `IFS=/ read -r owner repo resource number extra <<< "$path"`, and `read`
// stops at the first newline. bash therefore accepts
// $'…/issues/20\nignored' as issue 20 and goes on to PATCH it; verified
// against ./forge, which reports the PATCH failing rather than refusing the
// target. resolve_pr_target uses the identical construct.
func TestURLTargetsIgnoreEverythingAfterTheFirstLine(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"PATCH /api/v1/repos/acme/widget/issues/20":         {Body: `{"html_url":"u"}`},
		"POST /api/v1/repos/acme/widget/issues/20/comments": {Body: `{"html_url":"c"}`},
		"PATCH /api/v1/repos/acme/widget/pulls/12":          {Body: `{"html_url":"p"}`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	t.Run("issue close", func(t *testing.T) {
		base := srv.count()
		out, errText, code := runForge(t, "issue", "close", srv.URL+"/acme/widget/issues/20\nignored")
		if code != 0 || out != "Issue closed: u\n" || errText != "" {
			t.Fatalf("got (%q, %q, %d), want the closed line and 0", out, errText, code)
		}
		srv.assertRequest(t, base, "PATCH", "/api/v1/repos/acme/widget/issues/20")
	})

	t.Run("pr close", func(t *testing.T) {
		base := srv.count()
		out, errText, code := runForge(t, "pr", "close", srv.URL+"/acme/widget/pulls/12\nignored")
		if code != 0 || out != "PR closed: p\n" || errText != "" {
			t.Fatalf("got (%q, %q, %d), want the closed line and 0", out, errText, code)
		}
		srv.assertRequest(t, base, "PATCH", "/api/v1/repos/acme/widget/pulls/12")
	})

	// normalize_issue_reference validates the first line but prints "$target",
	// so the junk lands in the closing comment verbatim.
	t.Run("duplicate-of keeps the whole target in the comment", func(t *testing.T) {
		base := srv.count()
		if _, errText, code := runForge(t, "-R", "acme/widget", "issue", "close", "20",
			"--duplicate-of", srv.URL+"/acme/widget/issues/20\nignored"); code != 0 {
			t.Fatalf("exit = %d, stderr = %q", code, errText)
		}
		got := string(srv.requests()[base].Body)
		want := `{"body":"Duplicate of ` + srv.URL + `/acme/widget/issues/20\nignored."}`
		if got != want {
			t.Errorf("comment body = %s, want %s", got, want)
		}
	})

	// The first line is still the one that has to be valid.
	t.Run("a bad first line is still rejected", func(t *testing.T) {
		_, errText, code := runForge(t, "issue", "close", srv.URL+"/acme/widget/nope/20\nissues/20")
		if code != 1 || !strings.Contains(errText, "issue target must be a number or URL") {
			t.Errorf("got (%q, %d), want the target error and 1", errText, code)
		}
	})
}

// --- Command substitution chomps trailing newlines ---
//
// `x=$(... | jq -r '.f')` removes every trailing newline from what jq printed,
// which is one more than the newline jq itself appends. A JSON string field
// that ends in "\n" therefore reaches the output with it already gone.
func TestCapturedFieldsAreChomped(t *testing.T) {
	// forge lines 548, 588: url=$(api ... | jq -r '.html_url')
	t.Run("comment html_url", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"POST /api/v1/repos/acme/widget/issues/1/comments": {Body: "{\"html_url\":\"ok\\n\"}"},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, _, code := runForge(t, "-R", "acme/widget", "issue", "comment", "1", "-b", "hi")
		if want := "Comment posted: ok\n"; out != want || code != 0 {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	})

	// forge lines 428-437: every pr_view header field is its own capture.
	t.Run("pr view header fields", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/pulls/1": {Body: "{\"title\":\"hello\\n\\n\",\"state\":\"open\",\"body\":\"\"," +
				"\"user\":{\"login\":\"alice\"},\"head\":{\"label\":\"h\"},\"base\":{\"label\":\"b\"}}"},
			"GET /api/v1/repos/acme/widget/issues/1/comments": {Body: `[]`},
			"GET /api/v1/repos/acme/widget/pulls/1/reviews":   {Body: `[]`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, _, code := runForge(t, "-R", "acme/widget", "pr", "view", "1")
		want := "PR #1: hello\n  State:    open\n  Author:   alice\n  Created:  null\n  Updated:  null\n  Branch:   h \u2192 b\n\n"
		if out != want || code != 0 {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	})

	// forge line 264: the message is chomped before the sanitize pipeline, so
	// a trailing newline disappears instead of becoming a trailing space.
	t.Run("api error message", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"/api/v1/repos/acme/widget/issues?type=issues&state=open&limit=30": {
				Status: 400, Body: "{\"message\":\"bad\\n\"}",
			},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		_, errText, code := runForge(t, "-R", "acme/widget", "issue", "list")
		want := "forge: GET /repos/acme/widget/issues?type=issues&state=open&limit=30 failed: HTTP 400: bad\n"
		if errText != want || code != 22 {
			t.Errorf("stderr = %q (exit %d), want %q", errText, code, want)
		}
	})

	// A message that is nothing but newlines chomps to empty, which takes the
	// `[[ -n "$message" ]] || message="$body"` fallback: the raw body text is
	// printed instead. Confirmed by running bash lines 264-270 verbatim on
	// this body.
	t.Run("a newline-only message falls back to the raw body", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"/api/v1/repos/acme/widget/issues?type=issues&state=open&limit=30": {
				Status: 400, Body: "{\"message\":\"\\n\"}",
			},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		_, errText, _ := runForge(t, "-R", "acme/widget", "issue", "list")
		want := "forge: GET /repos/acme/widget/issues?type=issues&state=open&limit=30 failed: HTTP 400: " +
			`{"message":"\n"}` + "\n"
		if errText != want {
			t.Errorf("stderr = %q, want %q", errText, want)
		}
	})

	// forge line 1950: VERIFY_LOGIN=$(printf '%s' "$body" | jq -r '.login')
	t.Run("auth status login", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"/api/v1/user": {Body: "{\"login\":\"alice\\n\"}"},
		})
		useServer(t, srv)
		path := useToken(t, fakeToken)

		out, _, code := runForge(t, "auth", "status")
		if want := "Authenticated as alice (" + path + ")\n"; out != want || code != 0 {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	})
}

// --- `echo "$body"` reads option words (forge lines 443, 969) ---
//
// Both view commands print the body with the `echo` builtin, which takes a
// leading -[neE]+ argument as options rather than as data. A body of exactly
// "-n" prints nothing at all and suppresses echo's own newline; "-e" prints
// the newline only. bash was run for each case below.
func TestViewBodiesGoThroughTheEchoBuiltin(t *testing.T) {
	issueRoutes := func(body string) map[string]cannedResponse {
		return map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/issues/3": {Body: `{"title":"T","state":"open","body":"` + body +
				`","user":{"login":"u"},"labels":[],"milestone":null}`},
			"GET /api/v1/repos/acme/widget/issues/3/comments": {Body: `[]`},
		}
	}
	const header = "Issue #3: T\n  State:     open\n  Author:    u\n  Created:   null\n  Updated:   null\n  Labels:    -\n  Milestone: -\n\n"

	tests := []struct {
		body string
		want string
	}{
		// echo swallows the word and its newline: the header's blank line is
		// followed only by the `echo ""` that comes after the body.
		{"-n", header + "\n"},
		// echo swallows the word but still prints its newline.
		{"-e", header + "\n\n"},
		{"-nE", header + "\n"},
		// Not an option word: printed as data.
		{"-x", header + "-x\n\n"},
		{"-n hi", header + "-n hi\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			srv := newRecordingServer(t, issueRoutes(tt.body))
			useServer(t, srv)
			useToken(t, fakeToken)
			useNoInferRepo(t)

			out, errText, code := runForge(t, "-R", "acme/widget", "issue", "view", "3")
			if code != 0 || errText != "" {
				t.Fatalf("got (%q, %d), want success", errText, code)
			}
			if out != tt.want {
				t.Errorf("stdout = %q, want %q", out, tt.want)
			}
		})
	}
}

// --- `[ -t 0 ]` is isatty, not a device-type guess (forge line 1964) ---
//
// stdinIsTerminal issues TCGETS, exactly as bash's -t does. Every character
// device that is not a terminal must answer false: /dev/null so that
// `token verify </dev/null` reaches "stdin is empty", and /dev/zero so that it
// reads (forever) rather than printing the hint — which is what bash does,
// verified with `timeout .2 ./forge token verify </dev/zero` against both.
//
// The true branch is not reachable in-process: it needs a real terminal on the
// stdin seam, which means allocating a PTY, which means a subprocess — and
// this package spawns none. bash's own suite reaches it only through
// script(1). So the hint's text is pinned by TestTokenVerify's argument cases
// and the false branch is pinned here.
func TestStdinIsTerminal(t *testing.T) {
	for _, name := range []string{os.DevNull, "/dev/zero"} {
		f, err := os.Open(name)
		if err != nil {
			t.Skipf("cannot open %s: %v", name, err)
		}
		prev := stdin
		stdin = f
		got := stdinIsTerminal()
		stdin = prev
		f.Close()

		if got {
			t.Errorf("stdinIsTerminal() = true for %s, want false: it is a character device, not a terminal", name)
		}
	}

	// A non-file reader — what every other test installs — is never a
	// terminal, and never touches an ioctl.
	useStdin(t, "text")
	if stdinIsTerminal() {
		t.Error("stdinIsTerminal() = true for a strings.Reader, want false")
	}
}

// bash's command substitution strips NUL bytes from the token-verify stdin
// capture just as it does for -F body reads; a NUL-carrying candidate must
// verify as its NUL-free self rather than being refused by the header writer.
func TestTokenVerifyStripsNULBytesFromStdin(t *testing.T) {
	srv := newRecordingServer(t, userRoutes("testuser"))
	useServer(t, srv)
	useStdin(t, "tok\x00en123\n")

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
}
