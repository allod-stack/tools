package main

// Retained shell-suite scenarios for every mutating pr/issue/label/milestone
// command, pinning the exact request sequence and JSON payload each sends.
//
// bash's mock curl is one static case statement shared by the whole file, and
// each scenario resets only the request log (reset_requests) before running;
// this mirrors that with one recordingServer holding every fixture the file
// needs, and per-scenario request-count baselines instead of a reset.
//
// bash's assert_json compares with jq's `==` against a pretty-printed
// request body; the Go client sends compact JSON, so assertJSONBody decodes
// both sides and compares the resulting values instead of the raw text.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// --- Scenario helpers shared with pr_close_test.go and issue_close_test.go ---

// expReq is one expected request within a scenario: the method and the
// request URI exactly as sent (path + query), and optionally the JSON body
// it must equal. A nil json skips the body check, for requests bash's suite
// only pinned by method and path.
type expReq struct {
	method string
	path   string
	json   any
}

// assertScenario runs fn and checks that it made exactly len(want) requests
// against srv, in order, matching bash's reset_requests + run_ok/run_fail +
// assert_request/assert_json block. base is recorded before fn runs so the
// same shared server can be reused, scenario after scenario, the way bash
// reuses one mock curl across the whole file.
func assertScenario(t *testing.T, srv *recordingServer, want []expReq, fn func()) {
	t.Helper()
	base := srv.count()
	fn()
	if got := srv.count() - base; got != len(want) {
		t.Errorf("request count = %d, want %d", got, len(want))
	}
	for i, w := range want {
		idx := base + i
		srv.assertRequest(t, idx, w.method, w.path)
		if w.json != nil {
			reqs := srv.requests()
			if idx < len(reqs) {
				assertJSONBody(t, reqs[idx].Body, w.json, w.method+" "+w.path)
			}
		}
	}
}

// assertJSONBody mirrors `assert_json N '. == {...}' desc`: full-object
// equality against want (typically a map[string]any literal), field presence
// exact, key order irrelevant. Both sides are round-tripped through
// encoding/json before comparison so a Go int in want and a JSON number in
// body land on the same representation (float64), and so this is immune to
// bash's pretty-printed-vs-Go's-compact-JSON formatting difference.
func assertJSONBody(t *testing.T, body []byte, want any, desc string) {
	t.Helper()
	wantBytes, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("%s: test bug: cannot marshal want value: %v", desc, err)
	}
	var gotVal, wantVal any
	if err := json.Unmarshal(body, &gotVal); err != nil {
		t.Errorf("%s: request body is not JSON: %v (body: %s)", desc, err, body)
		return
	}
	if err := json.Unmarshal(wantBytes, &wantVal); err != nil {
		t.Fatalf("%s: test bug: %v", desc, err)
	}
	if !reflect.DeepEqual(gotVal, wantVal) {
		t.Errorf("%s: body mismatch\ngot:  %s\nwant: %s", desc, body, wantBytes)
	}
}

// runOK mirrors run_ok: runs the CLI with "-R acme/widget" ahead of args,
// exactly as run_ok always prepends "-R acme/widget" to the command under
// test, and fails the (sub)test if it exits non-zero.
func runOK(t *testing.T, args ...string) {
	t.Helper()
	full := append([]string{"-R", "acme/widget"}, args...)
	out, errText, code := runForge(t, full...)
	if code != 0 {
		t.Fatalf("forge %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(full, " "), code, out, errText)
	}
}

// runFail mirrors run_fail: runs with "-R acme/widget" ahead of args, and
// requires both a non-zero exit and that stdout+stderr together contain
// want, the same substring check `[[ "$output" == *"$expected"* ]]` makes
// over 2>&1-merged output.
func runFail(t *testing.T, want string, args ...string) {
	t.Helper()
	full := append([]string{"-R", "acme/widget"}, args...)
	out, errText, code := runForge(t, full...)
	if code == 0 {
		t.Fatalf("forge %s: unexpectedly succeeded\nstdout: %s", strings.Join(full, " "), out)
	}
	combined := out + errText
	if !strings.Contains(combined, want) {
		t.Errorf("forge %s: output = %q, want substring %q", strings.Join(full, " "), combined, want)
	}
}

// writeTempFile mirrors `printf ... > "$TMP/pr-body.md"`: a scratch file a
// -F/--body-file flag can read.
func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing temp body file: %v", err)
	}
	return path
}

// useCurrentBranch overrides the `git branch --show-current` seam that
// pr_create's head-branch default reads (forge line 721,
// gitremote.CurrentBranch via the currentBranch var in pr.go). harness_test.go
// has no seam helper for it, so it lives here; only pr create's
// no-explicit---head scenario needs it.
func useCurrentBranch(t *testing.T, name string) {
	t.Helper()
	prev := currentBranch
	currentBranch = func() string { return name }
	t.Cleanup(func() { currentBranch = prev })
}

// --- TestMutations ---

func TestMutations(t *testing.T) {
	// prBodyContent is `printf 'line one\n`+"`code`"+`\n\n'`: bash single
	// quotes leave the backticks literal, and printf turns \n into real
	// newlines, so the file (and every -F read of it) holds this exact text,
	// trailing blank line included.
	const prBodyContent = "line one\n`code`\n\n"
	prBodyFile := writeTempFile(t, prBodyContent)

	bugLabel := map[string]any{"id": 1, "name": "bug", "color": "ff0000", "description": "Problem", "exclusive": false, "is_archived": false}
	triageLabel := map[string]any{"id": 2, "name": "triage", "color": "00ff00", "description": "", "exclusive": false, "is_archived": false}
	julyBatch := map[string]any{"id": 3, "title": "July batch", "state": "open", "open_issues": 2, "closed_issues": 1, "due_on": "2026-07-31T00:00:00Z"}

	srv := newRecordingServer(t, map[string]cannedResponse{
		// pr create / edit / comment / reply
		"/api/v1/repos/acme/widget":                         {Body: `{"default_branch":"master"}`},
		"POST /api/v1/repos/acme/widget/pulls":              {Body: `{"html_url":"https://forge.example/acme/widget/pulls/1"}`},
		"PATCH /api/v1/repos/acme/widget/pulls/12":          {Body: `{"html_url":"https://forge.example/acme/widget/pulls/12"}`},
		"POST /api/v1/repos/acme/widget/issues/12/comments": {Body: `{"html_url":"https://forge.example/acme/widget/issues/12#comment-1"}`},
		"GET /api/v1/repos/acme/widget/pulls/12/reviews":    {Body: `[{"id":7,"comments_count":1}]`},
		"GET /api/v1/repos/acme/widget/pulls/12/reviews/7/comments": {
			Body: `[{"id":99,"path":"forge","line":4,"position":4,"diff_hunk":"@@ -1 +1 @@","body":"Inline note","created_at":"2026-06-01T00:00:00Z","user":{"login":"carol"}}]`,
		},
		"POST /api/v1/repos/acme/widget/pulls/12/reviews/7/comments": {Body: `{"html_url":"https://forge.example/acme/widget/pulls/12#comment-100"}`},

		// command-level repo override (run_capture scenarios)
		"POST /api/v1/repos/acme/gadget/pulls":  {Body: `{"html_url":"https://forge.example/acme/gadget/pulls/1"}`},
		"POST /api/v1/repos/acme/gadget/issues": {Body: `{"html_url":"https://forge.example/acme/gadget/issues/21"}`},

		// issue create / edit / comment / labels / milestone
		"POST /api/v1/repos/acme/widget/issues":                                          {Body: `{"html_url":"https://forge.example/acme/widget/issues/20"}`},
		"GET /api/v1/repos/acme/widget/labels?limit=100":                                 mustBody(t, []any{bugLabel, triageLabel}),
		"GET /api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100": mustBody(t, []any{julyBatch}),
		"PATCH /api/v1/repos/acme/widget/issues/20":                                      {Body: `{"html_url":"https://forge.example/acme/widget/issues/20"}`},
		"GET /api/v1/repos/acme/widget/issues/20": {
			Body: `{"html_url":"https://forge.example/acme/widget/issues/20","title":"Fix backup","state":"open","body":"Issue body","user":{"login":"bob"},"labels":[{"id":1,"name":"bug","color":"ff0000"}],"milestone":{"id":3,"title":"July batch"}}`,
		},
		"POST /api/v1/repos/acme/widget/issues/20/comments":     {Body: `{"html_url":"https://forge.example/acme/widget/issues/20#comment-2"}`},
		"POST /api/v1/repos/acme/widget/issues/20/labels":       {Body: `[{"id":1,"name":"bug","color":"ff0000"},{"id":2,"name":"triage","color":"00ff00"}]`},
		"PUT /api/v1/repos/acme/widget/issues/20/labels":        {Body: `[{"id":2,"name":"triage","color":"00ff00"}]`},
		"GET /api/v1/repos/acme/widget/issues/20/labels":        {Body: `[{"id":1,"name":"bug","color":"ff0000"}]`},
		"DELETE /api/v1/repos/acme/widget/issues/20/labels":     {},
		"DELETE /api/v1/repos/acme/widget/issues/20/labels/bug": {},

		// label create / edit / delete
		"POST /api/v1/repos/acme/widget/labels":     {Body: `{"id":4,"name":"area/nix","color":"123456","description":"Nix area","exclusive":false,"is_archived":false}`},
		"PATCH /api/v1/repos/acme/widget/labels/1":  {Body: `{"id":1,"name":"defect","color":"0000ff","description":"Problem","exclusive":true,"is_archived":false}`},
		"DELETE /api/v1/repos/acme/widget/labels/1": {},

		// milestone create / edit / delete
		"POST /api/v1/repos/acme/widget/milestones":     {Body: `{"id":5,"title":"August batch","state":"open","open_issues":0,"closed_issues":0,"due_on":"2026-08-31T00:00:00Z"}`},
		"PATCH /api/v1/repos/acme/widget/milestones/3":  {Body: `{"id":3,"title":"July batch","state":"closed","description":"July work","open_issues":0,"closed_issues":3,"due_on":"2026-07-31T00:00:00Z"}`},
		"DELETE /api/v1/repos/acme/widget/milestones/3": {},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t) // -R is always given, explicitly or via run_ok's prefix
	useCurrentBranch(t, "feature/current")

	t.Run("pr create infers default branch and posts multiline body", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget", nil},
			{"POST", "/api/v1/repos/acme/widget/pulls", map[string]any{
				"title": "Create PR", "head": "feature/current", "base": "master", "body": prBodyContent,
			}},
		}, func() {
			runOK(t, "pr", "create", "--title", "Create PR", "--body-file", prBodyFile)
		})
	})

	t.Run("pr create with short flags avoids metadata lookup", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/pulls", map[string]any{
				"title": "Short flags", "head": "topic", "base": "develop", "body": "body",
			}},
		}, func() {
			runOK(t, "pr", "create", "-t", "Short flags", "-H", "topic", "-B", "develop", "-b", "body")
		})
	})

	t.Run("pr create uses command-level repo", func(t *testing.T) {
		// run_capture, not run_ok: the scenario supplies its own -R, so no
		// "-R acme/widget" prefix belongs here.
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/gadget/pulls", map[string]any{
				"title": "Command repo", "head": "topic", "base": "master", "body": "body",
			}},
		}, func() {
			out, errText, code := runForge(t, "pr", "create", "-R", "acme/gadget", "-t", "Command repo", "-H", "topic", "-B", "master", "-b", "body")
			if code != 0 {
				t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
			}
		})
	})

	t.Run("pr edit preserves an explicitly empty body", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"PATCH", "/api/v1/repos/acme/widget/pulls/12", map[string]any{"body": ""}},
		}, func() {
			runOK(t, "pr", "edit", "12", "--body", "")
		})
	})

	t.Run("pr comment preserves multiline content", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/issues/12/comments", map[string]any{"body": prBodyContent}},
		}, func() {
			runOK(t, "pr", "comment", "12", "-F", prBodyFile)
		})
	})

	t.Run("pr reply looks up the original inline comment first", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/pulls/12/reviews", nil},
			{"GET", "/api/v1/repos/acme/widget/pulls/12/reviews/7/comments", nil},
			{"POST", "/api/v1/repos/acme/widget/pulls/12/reviews/7/comments", map[string]any{
				"path": "forge", "new_position": 4, "body": "thread reply",
			}},
		}, func() {
			runOK(t, "pr", "reply", "12", "99", "--body", "thread reply")
		})
	})

	t.Run("issue create sends an empty body when omitted", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/issues", map[string]any{"title": "New issue", "body": ""}},
		}, func() {
			runOK(t, "issue", "create", "-t", "New issue")
		})
	})

	t.Run("issue create resolves labels and milestone first", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/labels?limit=100", nil},
			{"GET", "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100", nil},
			{"POST", "/api/v1/repos/acme/widget/issues", map[string]any{
				"title": "Organized issue", "body": "", "labels": []int{1}, "milestone": 3,
			}},
		}, func() {
			runOK(t, "issue", "create", "-t", "Organized issue", "-l", "bug", "-m", "July batch")
		})
	})

	t.Run("issue create uses command-level repo", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/gadget/issues", map[string]any{"title": "Command repo issue", "body": ""}},
		}, func() {
			out, errText, code := runForge(t, "issue", "create", "-R", "acme/gadget", "-t", "Command repo issue")
			if code != 0 {
				t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
			}
		})
	})

	t.Run("issue edit preserves body content read from stdin", func(t *testing.T) {
		useStdin(t, "stdin body\n\n")
		assertScenario(t, srv, []expReq{
			{"PATCH", "/api/v1/repos/acme/widget/issues/20", map[string]any{"title": "Updated", "body": "stdin body\n\n"}},
		}, func() {
			runOK(t, "issue", "edit", "20", "--title", "Updated", "--body-file", "-")
		})
	})

	t.Run("issue edit resolves milestone before updating", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100", nil},
			{"PATCH", "/api/v1/repos/acme/widget/issues/20", map[string]any{"milestone": 3}},
		}, func() {
			runOK(t, "issue", "edit", "20", "--milestone", "July batch")
		})
	})

	t.Run("issue edit clears a milestone", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"PATCH", "/api/v1/repos/acme/widget/issues/20", map[string]any{"milestone": 0}},
		}, func() {
			runOK(t, "issue", "edit", "20", "--remove-milestone")
		})
	})

	t.Run("issue edit adds and removes labels gh-style", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/issues/20/labels", map[string]any{"labels": []string{"triage"}}},
			{"DELETE", "/api/v1/repos/acme/widget/issues/20/labels/bug", nil},
			{"GET", "/api/v1/repos/acme/widget/issues/20", nil},
		}, func() {
			runOK(t, "issue", "edit", "20", "--add-label", "triage", "--remove-label", "bug")
		})
	})

	t.Run("issue comment posts the body", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/issues/20/comments", map[string]any{"body": "issue comment body"}},
		}, func() {
			runOK(t, "issue", "comment", "20", "-b", "issue comment body")
		})
	})

	t.Run("issue comment preserves multiline content from file", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/issues/20/comments", map[string]any{"body": prBodyContent}},
		}, func() {
			runOK(t, "issue", "comment", "20", "-F", prBodyFile)
		})
	})

	t.Run("issue labels adds a label by name", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/issues/20/labels", map[string]any{"labels": []string{"triage"}}},
		}, func() {
			runOK(t, "issue", "labels", "20", "--add-label", "triage")
		})
	})

	t.Run("issue labels adds a label by numeric ID", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/issues/20/labels", map[string]any{"labels": []int{1}}},
		}, func() {
			runOK(t, "issue", "labels", "20", "--add", "1")
		})
	})

	t.Run("issue labels replaces the label set", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"PUT", "/api/v1/repos/acme/widget/issues/20/labels", map[string]any{"labels": []string{"triage"}}},
		}, func() {
			runOK(t, "issue", "labels", "20", "--set", "triage")
		})
	})

	t.Run("issue labels removes a label then re-fetches", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"DELETE", "/api/v1/repos/acme/widget/issues/20/labels/bug", nil},
			{"GET", "/api/v1/repos/acme/widget/issues/20/labels", nil},
		}, func() {
			runOK(t, "issue", "labels", "20", "--remove-label", "bug")
		})
	})

	t.Run("issue labels clears every label", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"DELETE", "/api/v1/repos/acme/widget/issues/20/labels", nil},
		}, func() {
			runOK(t, "issue", "labels", "20", "--clear")
		})
	})

	t.Run("issue milestone resolves the title then sets it", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100", nil},
			{"PATCH", "/api/v1/repos/acme/widget/issues/20", map[string]any{"milestone": 3}},
		}, func() {
			runOK(t, "issue", "milestone", "20", "July batch")
		})
	})

	t.Run("issue milestone clears through the helper command", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"PATCH", "/api/v1/repos/acme/widget/issues/20", map[string]any{"milestone": 0}},
		}, func() {
			runOK(t, "issue", "milestone", "20", "--clear")
		})
	})

	t.Run("label create sends name color and description", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/labels", map[string]any{
				"name": "area/nix", "color": "#123456", "description": "Nix area",
			}},
		}, func() {
			runOK(t, "label", "create", "area/nix", "-c", "123456", "-d", "Nix area")
		})
	})

	t.Run("label create --force updates an existing label", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/labels?limit=100", nil},
			{"PATCH", "/api/v1/repos/acme/widget/labels/1", map[string]any{
				"name": "bug", "color": "#0000ff", "description": "Updated",
			}},
		}, func() {
			runOK(t, "label", "create", "bug", "-c", "0000ff", "-d", "Updated", "--force")
		})
	})

	t.Run("label edit resolves name then updates", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/labels?limit=100", nil},
			{"PATCH", "/api/v1/repos/acme/widget/labels/1", map[string]any{
				"name": "defect", "color": "#0000ff", "exclusive": true,
			}},
		}, func() {
			runOK(t, "label", "edit", "bug", "-n", "defect", "-c", "0000ff", "--exclusive")
		})
	})

	t.Run("label delete resolves name then deletes", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/labels?limit=100", nil},
			{"DELETE", "/api/v1/repos/acme/widget/labels/1", nil},
		}, func() {
			runOK(t, "label", "delete", "bug", "--yes")
		})
	})

	t.Run("milestone create sends title description and due date", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/milestones", map[string]any{
				"title": "August batch", "description": "August work", "due_on": "2026-08-31T00:00:00Z",
			}},
		}, func() {
			runOK(t, "milestone", "create", "-t", "August batch", "-d", "August work", "--due", "2026-08-31")
		})
	})

	t.Run("milestone edit resolves title then updates state", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100", nil},
			{"PATCH", "/api/v1/repos/acme/widget/milestones/3", map[string]any{"state": "closed"}},
		}, func() {
			runOK(t, "milestone", "edit", "July batch", "-s", "closed")
		})
	})

	t.Run("milestone delete resolves title then deletes", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100", nil},
			{"DELETE", "/api/v1/repos/acme/widget/milestones/3", nil},
		}, func() {
			runOK(t, "milestone", "delete", "July batch")
		})
	})
}

// mustBody marshals v to a cannedResponse body. Used for fixtures assembled
// from Go values (so the label/milestone objects above are declared once and
// reused verbatim) rather than typed out as JSON text a second time.
func mustBody(t *testing.T, v any) cannedResponse {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustBody: %v", err)
	}
	return cannedResponse{Body: string(b)}
}
