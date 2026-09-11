package main

// Pagination scenarios for allod/tools#89: list and resolver commands must
// page through enough results to find what was asked for instead of
// silently stopping at the server's 50-item page cap.
//
// filler* builds server pages of items that deliberately do not match
// whatever a test resolves, so a request sequence proves the second page was
// actually fetched (not merely that the total item count happened to work
// out).

import (
	"fmt"
	"strings"
	"testing"
)

func fillerLabels(n int) []any {
	items := make([]any, n)
	for i := 0; i < n; i++ {
		items[i] = map[string]any{"id": i + 1, "name": fmt.Sprintf("filler-label-%d", i+1)}
	}
	return items
}

func fillerMilestones(n int) []any {
	items := make([]any, n)
	for i := 0; i < n; i++ {
		items[i] = map[string]any{"id": i + 1, "title": fmt.Sprintf("filler-milestone-%d", i+1)}
	}
	return items
}

func fillerPulls(n, startNumber int) []any {
	items := make([]any, n)
	for i := 0; i < n; i++ {
		items[i] = map[string]any{
			"number": startNumber + i,
			"title":  fmt.Sprintf("Filler PR %d", startNumber+i),
			"user":   map[string]any{"login": "filler"},
			"head":   map[string]any{"label": "acme:filler", "ref": fmt.Sprintf("filler-branch-%d", startNumber+i)},
			"base":   map[string]any{"label": "master", "ref": "master"},
		}
	}
	return items
}

func fillerIssues(n, startNumber int) []any {
	items := make([]any, n)
	for i := 0; i < n; i++ {
		items[i] = map[string]any{
			"number": startNumber + i,
			"title":  fmt.Sprintf("Filler issue %d", startNumber+i),
			"user":   map[string]any{"login": "filler"},
		}
	}
	return items
}

// --- label resolution across pages ---

func TestLabelResolutionAcrossPages(t *testing.T) {
	t.Run("a name on page 2 still resolves", func(t *testing.T) {
		page2 := []any{map[string]any{"id": 99, "name": "bug"}}
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/labels?limit=50&page=1": mustBody(t, fillerLabels(50)),
			"GET /api/v1/repos/acme/widget/labels?limit=50&page=2": mustBody(t, page2),
			"DELETE /api/v1/repos/acme/widget/labels/99":           {},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "label", "delete", "bug", "--yes")
		if code != 0 {
			t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/labels?limit=50&page=1")
		srv.assertRequest(t, 1, "GET", "/api/v1/repos/acme/widget/labels?limit=50&page=2")
		srv.assertRequest(t, 2, "DELETE", "/api/v1/repos/acme/widget/labels/99")
	})

	t.Run("a name missing from every page still fails, after fetching every page", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/labels?limit=50&page=1": mustBody(t, fillerLabels(50)),
			"GET /api/v1/repos/acme/widget/labels?limit=50&page=2": mustBody(t, fillerLabels(5)),
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "label", "delete", "missing-label", "--yes")
		if code == 0 {
			t.Fatalf("unexpectedly succeeded\nstdout: %s", out)
		}
		if !strings.Contains(errText, "label not found: missing-label") {
			t.Errorf("stderr = %q, want it to report the label not found", errText)
		}
		if n := srv.count(); n != 2 {
			t.Errorf("request count = %d, want 2 (both pages fetched, no delete attempted)", n)
		}
	})
}

// --- milestone resolution across pages ---

func TestMilestoneResolutionAcrossPages(t *testing.T) {
	t.Run("a title on page 2 still resolves", func(t *testing.T) {
		page2 := []any{map[string]any{
			"id": 77, "title": "July batch", "state": "open",
			"open_issues": 2, "closed_issues": 1, "due_on": "2026-07-31T00:00:00Z",
		}}
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=50&page=1": mustBody(t, fillerMilestones(50)),
			"GET /api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=50&page=2": mustBody(t, page2),
			"GET /api/v1/repos/acme/widget/milestones/77": {
				Body: `{"id":77,"title":"July batch","state":"open","description":"July work","open_issues":2,"closed_issues":1,"due_on":"2026-07-31T00:00:00Z"}`,
			},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "milestone", "view", "July batch")
		if code != 0 {
			t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
		}
		if !containsAll(out, "Milestone #77: July batch") {
			t.Errorf("stdout = %q, want the resolved milestone", out)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=50&page=1")
		srv.assertRequest(t, 1, "GET", "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=50&page=2")
		srv.assertRequest(t, 2, "GET", "/api/v1/repos/acme/widget/milestones/77")
	})

	t.Run("a title missing from every page still fails, after fetching every page", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/milestones?state=all&name=Nonexistent&limit=50&page=1": mustBody(t, fillerMilestones(50)),
			"GET /api/v1/repos/acme/widget/milestones?state=all&name=Nonexistent&limit=50&page=2": mustBody(t, fillerMilestones(5)),
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "milestone", "view", "Nonexistent")
		if code == 0 {
			t.Fatalf("unexpectedly succeeded\nstdout: %s", out)
		}
		if !strings.Contains(errText, "milestone not found: Nonexistent") {
			t.Errorf("stderr = %q, want it to report the milestone not found", errText)
		}
		if n := srv.count(); n != 2 {
			t.Errorf("request count = %d, want 2 (both pages fetched)", n)
		}
	})
}

// --- pr find-by-head and pr close's branch resolution across pages ---

func TestPRHeadResolutionAcrossPages(t *testing.T) {
	t.Run("find-by-head matches a branch on page 2", func(t *testing.T) {
		page2 := []any{map[string]any{
			"number": 501, "title": "Queued PR",
			"user": map[string]any{"login": "alice"},
			"head": map[string]any{"label": "acme:queued", "ref": "feature/queued"},
			"base": map[string]any{"label": "master", "ref": "master"},
		}}
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/pulls?state=open&limit=50&page=1": mustBody(t, fillerPulls(50, 1)),
			"GET /api/v1/repos/acme/widget/pulls?state=open&limit=50&page=2": mustBody(t, page2),
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "find-by-head", "feature/queued")
		if code != 0 || out != "501\n" {
			t.Errorf("got (%q, %q, %d), want (\"501\\n\", nil, 0)", out, errText, code)
		}
		if n := srv.count(); n != 2 {
			t.Errorf("request count = %d, want 2 (both pages fetched)", n)
		}
	})

	t.Run("find-by-head reports nothing after fetching every page with no match", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/pulls?state=open&limit=50&page=1": mustBody(t, fillerPulls(50, 1)),
			"GET /api/v1/repos/acme/widget/pulls?state=open&limit=50&page=2": mustBody(t, fillerPulls(5, 51)),
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "find-by-head", "no-such-branch")
		if code != 0 || out != "" || errText != "" {
			t.Errorf("got (%q, %q, %d), want (\"\", \"\", 0)", out, errText, code)
		}
		if n := srv.count(); n != 2 {
			t.Errorf("request count = %d, want 2 (both pages fetched)", n)
		}
	})

	t.Run("pr close resolves a branch target on page 2", func(t *testing.T) {
		page2 := []any{map[string]any{
			"number": 777, "title": "Queued PR",
			"user": map[string]any{"login": "alice"},
			"head": map[string]any{"label": "acme:queued", "ref": "queued-branch"},
			"base": map[string]any{"label": "master", "ref": "master"},
		}}
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/pulls?state=open&limit=50&page=1": mustBody(t, fillerPulls(50, 1)),
			"GET /api/v1/repos/acme/widget/pulls?state=open&limit=50&page=2": mustBody(t, page2),
			"PATCH /api/v1/repos/acme/widget/pulls/777":                      {Body: `{"html_url":"https://forge.example/acme/widget/pulls/777"}`},
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "close", "queued-branch")
		if code != 0 {
			t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/pulls?state=open&limit=50&page=1")
		srv.assertRequest(t, 1, "GET", "/api/v1/repos/acme/widget/pulls?state=open&limit=50&page=2")
		srv.assertRequest(t, 2, "PATCH", "/api/v1/repos/acme/widget/pulls/777")
	})

	t.Run("pr close fails on a branch missing from every page, after fetching every page", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/pulls?state=open&limit=50&page=1": mustBody(t, fillerPulls(50, 1)),
			"GET /api/v1/repos/acme/widget/pulls?state=open&limit=50&page=2": mustBody(t, fillerPulls(5, 51)),
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "close", "no-such-branch")
		if code == 0 {
			t.Fatalf("unexpectedly succeeded\nstdout: %s", out)
		}
		if !strings.Contains(out+errText, "no open PR found for branch") {
			t.Errorf("output = %q, want it to report no matching branch", out+errText)
		}
		if n := srv.count(); n != 2 {
			t.Errorf("request count = %d, want 2 (both pages fetched)", n)
		}
	})
}

// --- pr list across pages ---

func TestPRListAcrossPages(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"GET /api/v1/repos/acme/widget/pulls?state=open&limit=50&page=1": mustBody(t, fillerPulls(50, 1)),
		"GET /api/v1/repos/acme/widget/pulls?state=open&limit=50&page=2": mustBody(t, fillerPulls(3, 51)),
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	out, errText, code := runForge(t, "-R", "acme/widget", "pr", "list")
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
	}
	if !containsAll(out, "Filler PR 1", "Filler PR 50", "Filler PR 53") {
		t.Errorf("stdout = %q, want every row from both pages", out)
	}
	if got := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1; got != 53 {
		t.Errorf("printed %d rows, want 53 (every PR across both pages)", got)
	}
	srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/pulls?state=open&limit=50&page=1")
	srv.assertRequest(t, 1, "GET", "/api/v1/repos/acme/widget/pulls?state=open&limit=50&page=2")
}

// --- issue list respects --limit while paging ---

func TestIssueListPagination(t *testing.T) {
	t.Run("-L 3 sends exactly one request and prints 3 rows", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/issues?type=issues&state=open&limit=3&page=1": mustBody(t, fillerIssues(3, 1)),
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "issue", "list", "-L", "3")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		if n := srv.count(); n != 1 {
			t.Errorf("request count = %d, want 1", n)
		}
		if got := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1; got != 3 {
			t.Errorf("printed %d rows, want 3", got)
		}
	})

	t.Run("-L 60 fetches a 50-row page and a 20-row page and prints 60 rows", func(t *testing.T) {
		srv := newRecordingServer(t, map[string]cannedResponse{
			"GET /api/v1/repos/acme/widget/issues?type=issues&state=open&limit=50&page=1": mustBody(t, fillerIssues(50, 1)),
			"GET /api/v1/repos/acme/widget/issues?type=issues&state=open&limit=50&page=2": mustBody(t, fillerIssues(20, 51)),
		})
		useServer(t, srv)
		useToken(t, fakeToken)
		useNoInferRepo(t)

		out, errText, code := runForge(t, "-R", "acme/widget", "issue", "list", "-L", "60")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/issues?type=issues&state=open&limit=50&page=1")
		srv.assertRequest(t, 1, "GET", "/api/v1/repos/acme/widget/issues?type=issues&state=open&limit=50&page=2")
		if got := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1; got != 60 {
			t.Errorf("printed %d rows, want 60 (truncated to the limit)", got)
		}
	})
}
