package main

// Retained read-command scenarios from the retired shell suite. The foundation
// tests in harness_test.go exercise auth, token verification, and api()/apiTry
// error rendering, not the read commands' own output or request shapes, so all
// former scenarios remain here.
//
// Fixture bodies preserve the retired mock Forgejo responses so the same output
// substrings remain covered. The shell assertions used 1-based request numbers
// counting from the last reset_requests; srv.assertRequest and
// srv.requests()[i] are 0-based, so bash's request N is index N-1 here.

import (
	"strings"
	"testing"
)

// --- pr list ---

func TestReadPRList(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/pulls?state=open&limit=50": {Body: `[{"number":12,"title":"Improve tool","user":{"login":"alice"},"head":{"label":"acme:topic","ref":"topic"},"base":{"label":"master","ref":"master"}},{"number":31,"title":"Branch PR","user":{"login":"alice"},"head":{"label":"acme:feature","ref":"feature"},"base":{"label":"master","ref":"master"}}]`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	out, errText, code := runForge(t, "-R", "acme/widget", "pr", "list")
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
	}
	if !containsAll(out, "Improve tool", "acme:topic → master") {
		t.Errorf("stdout = %q, want it to contain the pull request title and its branches", out)
	}

	srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/pulls?state=open&limit=50")
	if got := srv.requests()[0].Authorization; got != "token "+fakeToken {
		t.Errorf("authorization = %s, want the configured token on a normal API call", srv.requests()[0].authKind())
	}
}

// TestReadPRListState covers the -s/--state filter and the merged-marker
// status column (allod/tools#193): -s closed tells a merged pull request
// from an abandoned one, -s all still shows "open" for an open one, and the
// request path carries the chosen state and limit.
func TestReadPRListState(t *testing.T) {
	t.Run("closed distinguishes merged from abandoned", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"/api/v1/repos/acme/widget/pulls?state=closed&limit=50": {Body: `[{"number":40,"title":"Merged one","user":{"login":"alice"},"head":{"label":"acme:m","ref":"m"},"base":{"label":"master","ref":"master"},"state":"closed","merged":true,"merged_at":"2026-09-10T12:00:00Z"},{"number":41,"title":"Abandoned one","user":{"login":"alice"},"head":{"label":"acme:a","ref":"a"},"base":{"label":"master","ref":"master"},"state":"closed","merged":false,"merged_at":null}]`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "list", "-s", "closed")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		if !containsAll(out, "Merged one", "merged 2026-09-10", "Abandoned one", "closed") {
			t.Errorf("stdout = %q, want a merged row with its date and a closed row without one", out)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/pulls?state=closed&limit=50")
	})

	t.Run("all still shows open for an open pull request", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"/api/v1/repos/acme/widget/pulls?state=all&limit=50": {Body: `[{"number":42,"title":"Still open","user":{"login":"alice"},"head":{"label":"acme:o","ref":"o"},"base":{"label":"master","ref":"master"},"state":"open"}]`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "list", "-s", "all")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		if !containsAll(out, "Still open", "open") {
			t.Errorf("stdout = %q, want the open status cell", out)
		}
	})

	t.Run("limit changes the request", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"/api/v1/repos/acme/widget/pulls?state=open&limit=5": {Body: `[]`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "list", "-L", "5")
		if code != 0 || errText != "" || out != "No open pull requests in acme/widget\n" {
			t.Fatalf("got (%q, %q, %d), want success and the empty-open message", out, errText, code)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/pulls?state=open&limit=5")
	})

	t.Run("closed with none prints the closed empty message", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"/api/v1/repos/acme/widget/pulls?state=closed&limit=50": {Body: `[]`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, _, code := runForge(t, "-R", "acme/widget", "pr", "list", "-s", "closed")
		if code != 0 || out != "No closed pull requests in acme/widget\n" {
			t.Errorf("got (%q, %d), want the closed empty message", out, code)
		}
	})

	t.Run("bogus state dies", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "list", "-s", "bogus")
		if code != 1 || errText != "forge: --state must be one of: open, closed, all\n" {
			t.Errorf("got (%q, %q, %d), want the state error", out, errText, code)
		}
		if n := srv.count(); n != 0 {
			t.Errorf("request count = %d, want 0", n)
		}
	})

	t.Run("-L 0 dies", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "list", "-L", "0")
		if code != 1 || errText != "forge: -L must be a positive integer\n" {
			t.Errorf("got (%q, %q, %d), want the positive-integer error", out, errText, code)
		}
	})
}

// --- pr view ---

func TestReadPRView(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/pulls/12":                    {Body: `{"title":"Improve tool","state":"open","body":"PR body","user":{"login":"alice"},"head":{"label":"acme:topic","ref":"topic"},"base":{"label":"master","ref":"master"}}`},
		"/api/v1/repos/acme/widget/issues/12/comments":          {Body: `[{"body":"General note","created_at":"2026-06-02T00:00:00Z","user":{"login":"dave"}}]`},
		"/api/v1/repos/acme/widget/pulls/12/reviews":            {Body: `[{"id":7,"comments_count":1}]`},
		"/api/v1/repos/acme/widget/pulls/12/reviews/7/comments": {Body: `[{"id":99,"path":"forge","line":4,"position":4,"diff_hunk":"@@ -1 +1 @@","body":"Inline note","created_at":"2026-06-01T00:00:00Z","user":{"login":"carol"}}]`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	out, errText, code := runForge(t, "-R", "acme/widget", "pr", "view", "12")
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
	}
	if !containsAll(out, "PR #12: Improve tool", "General note", "Inline note") {
		t.Errorf("stdout = %q, want the PR header, the general comment, and the inline comment", out)
	}
	if strings.Contains(out, "Merged:") {
		t.Errorf("stdout = %q, want no Merged line for an open pull request", out)
	}
	if n := srv.count(); n != 4 {
		t.Errorf("request count = %d, want 4 (PR, comments, reviews, one review's comments)", n)
	}
}

// TestReadPRViewMerged covers the Merged: line and the merged/mergedAt
// --json fields (allod/tools#193).
func TestReadPRViewMerged(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/pulls/13":           {Body: `{"title":"Landed","state":"closed","body":"","user":{"login":"alice"},"head":{"label":"acme:topic","ref":"topic"},"base":{"label":"master","ref":"master"},"merged":true,"merged_at":"2026-09-10T08:00:00Z"}`},
		"/api/v1/repos/acme/widget/issues/13/comments": {Body: `[]`},
		"/api/v1/repos/acme/widget/pulls/13/reviews":   {Body: `[]`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	t.Run("rendered view shows the merge date", func(t *testing.T) {
		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "view", "13")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		if !strings.Contains(out, "Merged: 2026-09-10") {
			t.Errorf("stdout = %q, want a Merged: 2026-09-10 line", out)
		}
	})

	t.Run("--json exposes merged and mergedAt", func(t *testing.T) {
		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "view", "13", "--json", "merged,mergedAt")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		if !containsAll(out, `"merged":true`, `"mergedAt":"2026-09-10T08:00:00Z"`) {
			t.Errorf("stdout = %q, want merged and mergedAt in the JSON object", out)
		}
	})
}

// --- pr review-comments ---

func TestReadPRReviewComments(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/pulls/12/reviews":            {Body: `[{"id":7,"comments_count":1}]`},
		"/api/v1/repos/acme/widget/pulls/12/reviews/7/comments": {Body: `[{"id":99,"path":"forge","line":4,"position":4,"diff_hunk":"@@ -1 +1 @@","body":"Inline note","created_at":"2026-06-01T00:00:00Z","user":{"login":"carol"}}]`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	out, errText, code := runForge(t, "-R", "acme/widget", "pr", "review-comments", "12")
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
	}
	if !containsAll(out, "id 99", "carol on forge line 4", "Inline note") {
		t.Errorf("stdout = %q, want the comment id, its location, and its body", out)
	}
}

// --- pr find-by-head ---

func TestReadPRFindByHead(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/pulls?state=open&limit=50": {Body: `[{"number":12,"title":"Improve tool","user":{"login":"alice"},"head":{"label":"acme:topic","ref":"topic"},"base":{"label":"master","ref":"master"}},{"number":31,"title":"Branch PR","user":{"login":"alice"},"head":{"label":"acme:feature","ref":"feature"},"base":{"label":"master","ref":"master"}}]`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	t.Run("finds an open pull request by its head branch", func(t *testing.T) {
		out, _, code := runForge(t, "-R", "acme/widget", "pr", "find-by-head", "topic")
		if code != 0 || out != "12\n" {
			t.Errorf("got (%q, %d), want (\"12\\n\", 0)", out, code)
		}
	})
	t.Run("matches the head ref, not merely the first open PR", func(t *testing.T) {
		out, _, code := runForge(t, "-R", "acme/widget", "pr", "find-by-head", "feature")
		if code != 0 || out != "31\n" {
			t.Errorf("got (%q, %d), want (\"31\\n\", 0)", out, code)
		}
	})
	t.Run("returns nothing when no open PR has that head", func(t *testing.T) {
		out, _, code := runForge(t, "-R", "acme/widget", "pr", "find-by-head", "no-such-branch")
		if code != 0 || out != "" {
			t.Errorf("got (%q, %d), want (\"\", 0)", out, code)
		}
	})
}

// --- issue list ---

func TestReadIssueList(t *testing.T) {
	t.Run("lists an open issue via inferred repo", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"/api/v1/repos/acme/widget/issues?type=issues&state=open&limit=30": {Body: `[{"number":20,"title":"Fix backup","user":{"login":"bob"},"labels":[{"id":1,"name":"bug","color":"ff0000"}],"milestone":{"id":3,"title":"July batch"}}]`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useInferRepo(t, "acme/widget", nil)

		out, errText, code := runForge(t, "issue", "list")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		if !containsAll(out, "Fix backup", "bob", "bug", "July batch") {
			t.Errorf("stdout = %q, want the issue title, author, label, and milestone", out)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/issues?type=issues&state=open&limit=30")
	})

	t.Run("filters issues with gh-style list flags", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"/api/v1/repos/acme/widget/issues?type=issues&state=closed&limit=5&labels=bug&milestones=July%20batch&q=backup": {Body: `[{"number":19,"title":"Closed backup","user":{"login":"bob"},"labels":[{"id":1,"name":"bug","color":"ff0000"}],"milestone":{"id":3,"title":"July batch"}}]`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useInferRepo(t, "acme/widget", nil)

		out, errText, code := runForge(t, "issue", "list", "--state", "closed", "--label", "bug", "--milestone", "July batch", "--limit", "5", "--search", "backup")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		if !containsAll(out, "Closed backup") {
			t.Errorf("stdout = %q, want the filtered issue", out)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/issues?type=issues&state=closed&limit=5&labels=bug&milestones=July%20batch&q=backup")
	})

	t.Run("uses command-level repo for issue listing", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"/api/v1/repos/acme/gadget/issues?type=issues&state=open&limit=30": {Body: `[{"number":21,"title":"Gadget issue","user":{"login":"zoe"}}]`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "issue", "list", "--repo", "acme/gadget")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		if !containsAll(out, "Gadget issue") {
			t.Errorf("stdout = %q, want the gadget repo's issue", out)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/gadget/issues?type=issues&state=open&limit=30")
	})
}

// --- issue view ---

func TestReadIssueView(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/issues/20":          {Body: `{"html_url":"https://forge.example/acme/widget/issues/20","title":"Fix backup","state":"open","body":"Issue body","user":{"login":"bob"},"labels":[{"id":1,"name":"bug","color":"ff0000"}],"milestone":{"id":3,"title":"July batch"}}`},
		"/api/v1/repos/acme/widget/issues/20/comments": {Body: `[{"body":"Issue note","created_at":"2026-06-03T00:00:00Z","user":{"login":"erin"}}]`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	out, errText, code := runForge(t, "-R", "acme/widget", "issue", "view", "20")
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
	}
	if !containsAll(out, "Issue #20: Fix backup", "Issue body", "Issue note", "Labels:    bug", "Milestone: July batch") {
		t.Errorf("stdout = %q, want the issue header, body, comment, labels, and milestone", out)
	}
}

// --- label list ---

func TestReadLabelList(t *testing.T) {
	fixture := map[string]cannedResponse{
		"/api/v1/repos/acme/widget/labels?limit=30": {Body: `[{"id":1,"name":"bug","color":"ff0000","description":"Problem","exclusive":false,"is_archived":false},{"id":2,"name":"triage","color":"00ff00","description":"","exclusive":false,"is_archived":false}]`},
	}

	t.Run("lists repository labels", func(t *testing.T) {
		srv := newRecordingServer(t, fixture)
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "label", "list")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		if !containsAll(out, "bug", "Problem") {
			t.Errorf("stdout = %q, want the label name and description", out)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/labels?limit=30")
	})

	t.Run("filters labels with gh-style list flags", func(t *testing.T) {
		srv := newRecordingServer(t, fixture)
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "label", "list", "--search", "tri", "--sort", "name", "--order", "desc", "--limit", "30")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		if !containsAll(out, "triage") {
			t.Errorf("stdout = %q, want the filtered label", out)
		}
		// Search/sort/order are applied client-side; the request itself is
		// unchanged from the plain list (limit was 30 either way).
		srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/labels?limit=30")
	})
}

// --- milestone list ---

func TestReadMilestoneList(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/milestones?state=open&limit=100": {Body: `[{"id":3,"title":"July batch","state":"open","open_issues":2,"closed_issues":1,"due_on":"2026-07-31T00:00:00Z"}]`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	out, errText, code := runForge(t, "-R", "acme/widget", "milestone", "list")
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
	}
	if !containsAll(out, "July batch", "2026-07-31") {
		t.Errorf("stdout = %q, want the milestone title and due date", out)
	}
	srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/milestones?state=open&limit=100")
}

// --- milestone view ---

func TestReadMilestoneView(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100": {Body: `[{"id":3,"title":"July batch","state":"open","open_issues":2,"closed_issues":1,"due_on":"2026-07-31T00:00:00Z"}]`},
		"/api/v1/repos/acme/widget/milestones/3":                                     {Body: `{"id":3,"title":"July batch","state":"open","description":"July work","open_issues":2,"closed_issues":1,"due_on":"2026-07-31T00:00:00Z"}`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	out, errText, code := runForge(t, "-R", "acme/widget", "milestone", "view", "July batch")
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
	}
	if !containsAll(out, "Milestone #3: July batch", "July work") {
		t.Errorf("stdout = %q, want the milestone header and description", out)
	}
	srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100")
	srv.assertRequest(t, 1, "GET", "/api/v1/repos/acme/widget/milestones/3")
}

// --- issue labels (no flags: list) ---

func TestReadIssueLabels(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/issues/20/labels": {Body: `[{"id":1,"name":"bug","color":"ff0000"}]`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	out, errText, code := runForge(t, "-R", "acme/widget", "issue", "labels", "20")
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
	}
	if !containsAll(out, "Issue #20 labels: bug") {
		t.Errorf("stdout = %q, want the issue's labels", out)
	}
	srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/issues/20/labels")
}

// --- issue milestone (no flags: show) ---

func TestReadIssueMilestone(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/issues/20": {Body: `{"html_url":"https://forge.example/acme/widget/issues/20","title":"Fix backup","state":"open","body":"Issue body","user":{"login":"bob"},"labels":[{"id":1,"name":"bug","color":"ff0000"}],"milestone":{"id":3,"title":"July batch"}}`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	out, errText, code := runForge(t, "-R", "acme/widget", "issue", "milestone", "20")
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
	}
	if !containsAll(out, "Issue #20 milestone: July batch") {
		t.Errorf("stdout = %q, want the issue's milestone", out)
	}
	srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/issues/20")
}

// --- helpers ---

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
