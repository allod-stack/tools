package main

// Port of tests/forge/issue-close.sh: issue close's reason/duplicate-of
// comment composition and the comment-then-PATCH request order.
//
// Shared scaffolding (expReq, assertScenario, runOK, assertJSONBody) lives in
// mutations_test.go, in this same package.

import "testing"

func TestIssueClose(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"PATCH /api/v1/repos/acme/widget/issues/20":         {Body: `{"html_url":"https://forge.example/acme/widget/issues/20"}`},
		"POST /api/v1/repos/acme/widget/issues/20/comments": {Body: `{"html_url":"https://forge.example/acme/widget/issues/20#comment-2"}`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t) // every scenario supplies -R, explicitly or via runOK's prefix

	closedState := map[string]any{"state": "closed"}

	t.Run("plain close sends the closed state", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"PATCH", "/api/v1/repos/acme/widget/issues/20", closedState},
		}, func() {
			runOK(t, "issue", "close", "20")
		})
	})

	t.Run("close with a comment posts it before closing", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/issues/20/comments", map[string]any{"body": "Implemented"}},
			{"PATCH", "/api/v1/repos/acme/widget/issues/20", closedState},
		}, func() {
			runOK(t, "issue", "close", "20", "-c", "Implemented")
		})
	})

	t.Run("duplicate close records the duplicate target in the comment", func(t *testing.T) {
		// resolve_issue_target and normalize_issue_reference both compare
		// against $FORGE_URL, which useServer points at srv.URL; a
		// bash-suite-style "https://forge.example/..." literal would never
		// match here.
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/issues/20/comments", map[string]any{
				"body": "Already tracked\n\nDuplicate of #12.",
			}},
			{"PATCH", "/api/v1/repos/acme/widget/issues/20", closedState},
		}, func() {
			runOK(t, "issue", "close", srv.URL+"/acme/widget/issues/20", "--comment", "Already tracked", "--duplicate-of", "12")
		})
	})

	t.Run("not-planned reason is recorded as a comment", func(t *testing.T) {
		assertScenario(t, srv, []expReq{
			{"POST", "/api/v1/repos/acme/widget/issues/20/comments", map[string]any{
				"body": "Closed as not planned.",
			}},
			{"PATCH", "/api/v1/repos/acme/widget/issues/20", closedState},
		}, func() {
			runOK(t, "issue", "close", "20", "--reason", "not planned")
		})
	})
}
