package main

// Retained shell-suite scenarios for pr close's target resolution (number, URL,
// branch), the optional closing comment, and the guarded branch deletion.
// Request order is the point of this file: resolve target -> optional
// comment POST -> PATCH state -> optional GET-then-DELETE branch.
//
// Shared scaffolding (expReq, assertScenario, runOK, runFail, assertJSONBody)
// lives in mutations_test.go, in this same package.

import "testing"

func TestPRClose(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"PATCH /api/v1/repos/acme/widget/pulls/12":          {Body: `{"html_url":"https://forge.example/acme/widget/pulls/12"}`},
		"POST /api/v1/repos/acme/widget/issues/12/comments": {Body: `{"html_url":"https://forge.example/acme/widget/issues/12#comment-1"}`},
		// Two open PRs with distinct head branches, matching the retired fixture:
		// the real pulls list endpoint ignores head/base filters, so the client
		// matches head.ref itself.
		"GET /api/v1/repos/acme/widget/pulls?state=open&limit=50&page=1": {
			Body: `[{"number":12,"title":"Improve tool","user":{"login":"alice"},"head":{"label":"acme:topic","ref":"topic"},"base":{"label":"master","ref":"master"}},` +
				`{"number":31,"title":"Branch PR","user":{"login":"alice"},"head":{"label":"acme:feature","ref":"feature"},"base":{"label":"master","ref":"master"}}]`,
		},
		"PATCH /api/v1/repos/acme/gadget/pulls/5": {Body: `{"html_url":"https://forge.example/acme/gadget/pulls/5"}`},
		"GET /api/v1/repos/acme/widget/pulls/12": {
			Body: `{"title":"Improve tool","state":"open","body":"PR body","user":{"login":"alice"},"head":{"label":"acme:topic","ref":"topic"},"base":{"label":"master","ref":"master"}}`,
		},
		"DELETE /api/v1/repos/acme/widget/branches/topic": {},
		"GET /api/v1/repos/acme/widget/pulls/99": {
			Body: `{"title":"Self PR","state":"open","body":"","user":{"login":"alice"},"head":{"label":"acme:master","ref":"master"},"base":{"label":"master","ref":"master"}}`,
		},
		"PATCH /api/v1/repos/acme/widget/pulls/99": {Body: `{"html_url":"https://forge.example/acme/widget/pulls/99"}`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t) // every scenario supplies -R, explicitly or via runOK's prefix

	closedState := map[string]any{"state": "closed"}

	t.Run("plain close makes one API request", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"PATCH", "/api/v1/repos/acme/widget/pulls/12", closedState},
		}, func() {
			runOK(t, "pr", "close", "12")
		})
	})

	t.Run("close with a comment posts it before closing", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/issues/12/comments", map[string]any{"body": "Superseded"}},
			{"PATCH", "/api/v1/repos/acme/widget/pulls/12", closedState},
		}, func() {
			runOK(t, "pr", "close", "12", "-c", "Superseded")
		})
	})

	t.Run("accepts a PR URL as the close target", func(t *testing.T) {
		// resolve_pr_target compares the target against $FORGE_URL, which
		// useServer points at srv.URL; a bash-suite-style
		// "https://forge.example/..." literal would never match here.
		assertScenario(t, srv, []expReq{
			{"PATCH", "/api/v1/repos/acme/widget/pulls/12", closedState},
		}, func() {
			runOK(t, "pr", "close", srv.URL+"/acme/widget/pulls/12")
		})
	})

	t.Run("branch target looks up the PR first", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/pulls?state=open&limit=50&page=1", nil},
			{"PATCH", "/api/v1/repos/acme/widget/pulls/12", closedState},
		}, func() {
			runOK(t, "pr", "close", "topic")
		})
	})

	t.Run("uses command-level repo override", func(t *testing.T) {
		// run_capture, not run_ok: the scenario supplies its own -R.
		assertScenario(t, srv, []expReq{
			{"PATCH", "/api/v1/repos/acme/gadget/pulls/5", nil},
		}, func() {
			out, errText, code := runForge(t, "pr", "close", "-R", "acme/gadget", "5")
			if code != 0 {
				t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
			}
		})
	})

	t.Run("delete-branch fetches PR details then deletes after closing", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"PATCH", "/api/v1/repos/acme/widget/pulls/12", closedState},
			{"DELETE", "/api/v1/repos/acme/widget/branches/topic", nil},
		}, func() {
			runOK(t, "pr", "close", "12", "-d")
		})
	})

	t.Run("comment and delete-branch together: comment first, branch last", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/issues/12/comments", nil},
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"PATCH", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"DELETE", "/api/v1/repos/acme/widget/branches/topic", nil},
		}, func() {
			runOK(t, "pr", "close", "12", "--comment", "Done", "--delete-branch")
		})
	})

	t.Run("branch target with delete-branch resolves branch before fetching details", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/pulls?state=open&limit=50&page=1", nil},
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"PATCH", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"DELETE", "/api/v1/repos/acme/widget/branches/topic", nil},
		}, func() {
			runOK(t, "pr", "close", "topic", "-d")
		})
	})

	t.Run("rejects a branch with no matching PR", func(t *testing.T) {
		runFail(t, "no open PR found for branch", "pr", "close", "nonexistent")
	})

	t.Run("reports no match for a slashed branch name", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/pulls?state=open&limit=50&page=1", nil},
		}, func() {
			runFail(t, "no open PR found", "pr", "close", "feat/sub")
		})
	})

	t.Run("skips branch deletion when head equals base", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/pulls/99", nil},
			{"PATCH", "/api/v1/repos/acme/widget/pulls/99", nil},
		}, func() {
			runOK(t, "pr", "close", "99", "-d")
		})
	})
}
