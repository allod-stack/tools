package main

import (
	"strings"
	"testing"
)

const (
	mergeHeadSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mergeSHA     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mergeHTMLURL = "https://forge.example/acme/widget/pulls/12"
)

const openMergeablePR = `{"number":12,"html_url":"` + mergeHTMLURL + `","state":"open","merged":false,"mergeable":true,"head":{"sha":"` + mergeHeadSHA + `","ref":"topic"},"base":{"ref":"master"}}`

// mergedPR is what the read-back's second GET sees once the merge landed.
const mergedPR = `{"number":12,"state":"open","merged":true,"merge_commit_sha":"` + mergeSHA + `","head":{"sha":"` + mergeHeadSHA + `","ref":"topic"},"base":{"ref":"master"}}`

func baseBranchAt(sha string) cannedResponse {
	return cannedResponse{Body: `{"commit":{"id":"` + sha + `"}}`}
}

func newPRMergeServer(t *testing.T) *recordingServer {
	t.Helper()
	srv := newRecordingServer(t, map[string]cannedResponse{})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)
	return srv
}

func TestPRMerge(t *testing.T) {
	t.Run("--help prints the usage", func(t *testing.T) {
		srv := newPRMergeServer(t)
		want := commandUsageTexts["pr merge"]
		if want == "" {
			t.Fatal("usage.go has no commandUsageTexts entry for \"pr merge\"")
		}
		out, errText, code := runForge(t, "pr", "merge", "--help")
		if code != 0 || out != want || errText != "" {
			t.Errorf("got (%q, %q, %d), want (usage text, \"\", 0)", truncate(out), errText, code)
		}
		if srv.count() != 0 {
			t.Errorf("--help made %d requests, want 0", srv.count())
		}
	})

	// --- Each merge method, full happy path ---

	for _, tc := range []struct{ flag, do string }{
		{"-m", "merge"}, {"-r", "rebase"}, {"-s", "squash"},
	} {
		t.Run("method "+tc.flag, func(t *testing.T) {
			srv := newPRMergeServer(t)
			srv.enqueue(
				cannedResponse{Body: openMergeablePR},
				cannedResponse{},
				cannedResponse{Body: mergedPR},
				baseBranchAt(mergeSHA),
			)

			var out, errText string
			var code int
			assertScenario(t, srv, []expReq{
				{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
				{"POST", "/api/v1/repos/acme/widget/pulls/12/merge", map[string]any{
					"Do": tc.do, "head_commit_id": mergeHeadSHA, "delete_branch_after_merge": false,
				}},
				{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
				{"GET", "/api/v1/repos/acme/widget/branches/master", nil},
			}, func() {
				out, errText, code = runForge(t, "-R", "acme/widget", "pr", "merge", "12", tc.flag)
			})

			want := "PR merged: " + mergeHTMLURL + " (master at " + mergeSHA + ")\n"
			if code != 0 || out != want || errText != "" {
				t.Errorf("got (%q, %q, %d), want (%q, \"\", 0)", out, errText, code, want)
			}
		})
	}

	// --- Flag validation: no request reaches the network ---

	t.Run("flag validation", func(t *testing.T) {
		srv := newPRMergeServer(t)

		cases := []struct {
			name    string
			args    []string
			wantErr string
		}{
			{"no method given", []string{"pr", "merge", "12"}, "one of -m/--merge, -r/--rebase, -s/--squash is required"},
			{"two methods given", []string{"pr", "merge", "12", "-m", "-r"}, "cannot be combined"},
			{"unknown option", []string{"pr", "merge", "12", "--bogus"}, "unknown option for pr merge"},
			{"missing target", []string{"pr", "merge", "-m"}, "usage: forge pr merge"},
			{"body options combined", []string{"pr", "merge", "12", "-m", "-b", "one", "-F", "two"}, "cannot be combined"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				full := append([]string{"-R", "acme/widget"}, tc.args...)
				out, errText, code := runForge(t, full...)
				if code == 0 {
					t.Fatalf("unexpectedly succeeded, stdout: %s", out)
				}
				if !strings.Contains(errText, tc.wantErr) {
					t.Errorf("stderr = %q, want substring %q", errText, tc.wantErr)
				}
			})
		}

		if srv.count() != 0 {
			t.Errorf("flag validation made %d requests, want 0", srv.count())
		}
	})

	// --- PR-state refusals: one read, then die, never a POST ---

	t.Run("refuses a closed PR", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(cannedResponse{Body: `{"number":12,"state":"closed","merged":false,"mergeable":true,"head":{"sha":"` + mergeHeadSHA + `","ref":"topic"},"base":{"ref":"master"}}`})

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m")
		if code == 0 {
			t.Fatalf("unexpectedly succeeded, stdout: %s", out)
		}
		if !strings.Contains(errText, "PR #12 is not open") {
			t.Errorf("stderr = %q, want \"PR #12 is not open\"", errText)
		}
		if srv.count() != 1 {
			t.Errorf("made %d requests, want 1 (no POST)", srv.count())
		}
	})

	t.Run("refuses an already-merged PR", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(cannedResponse{Body: `{"number":12,"state":"open","merged":true,"mergeable":true,"head":{"sha":"` + mergeHeadSHA + `","ref":"topic"},"base":{"ref":"master"}}`})

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m")
		if code == 0 {
			t.Fatalf("unexpectedly succeeded, stdout: %s", out)
		}
		if !strings.Contains(errText, "PR #12 is already merged") {
			t.Errorf("stderr = %q, want \"PR #12 is already merged\"", errText)
		}
		if srv.count() != 1 {
			t.Errorf("made %d requests, want 1 (no POST)", srv.count())
		}
	})

	t.Run("refuses an unmergeable PR", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(cannedResponse{Body: `{"number":12,"state":"open","merged":false,"mergeable":false,"head":{"sha":"` + mergeHeadSHA + `","ref":"topic"},"base":{"ref":"master"}}`})

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m")
		if code == 0 {
			t.Fatalf("unexpectedly succeeded, stdout: %s", out)
		}
		if !strings.Contains(errText, "PR #12 is not mergeable") {
			t.Errorf("stderr = %q, want \"PR #12 is not mergeable\"", errText)
		}
		if srv.count() != 1 {
			t.Errorf("made %d requests, want 1 (no POST)", srv.count())
		}
	})

	t.Run("refuses a PR whose mergeable field is missing", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(cannedResponse{Body: `{"number":12,"state":"open","merged":false,"head":{"sha":"` + mergeHeadSHA + `","ref":"topic"},"base":{"ref":"master"}}`})

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m")
		if code == 0 {
			t.Fatalf("unexpectedly succeeded, stdout: %s", out)
		}
		if !strings.Contains(errText, "PR #12 is not mergeable") {
			t.Errorf("stderr = %q, want \"PR #12 is not mergeable\"", errText)
		}
		if srv.count() != 1 {
			t.Errorf("made %d requests, want 1 (no POST)", srv.count())
		}
	})

	// --- --match-head-commit ---

	t.Run("match-head-commit mismatch refuses before any POST", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(cannedResponse{Body: openMergeablePR})

		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m", "--match-head-commit", "cccccccccccccccccccccccccccccccccccccccc")
		if code == 0 {
			t.Fatalf("unexpectedly succeeded, stdout: %s", out)
		}
		if !strings.Contains(errText, "PR #12 head commit is "+mergeHeadSHA) {
			t.Errorf("stderr = %q, want it to name the head commit", errText)
		}
		if srv.count() != 1 {
			t.Errorf("made %d requests, want 1 (no POST)", srv.count())
		}
	})

	t.Run("match-head-commit match proceeds", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{},
			cannedResponse{Body: mergedPR},
			baseBranchAt(mergeSHA),
		)
		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m", "--match-head-commit", mergeHeadSHA)
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
	})

	// --- The merge POST fails and the forge's own message surfaces ---

	t.Run("POST failure surfaces the forge's message", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{Status: 405, Body: `{"message":"branch protection blocks direct merges"}`},
		)
		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m")
		if code == 0 {
			t.Fatalf("unexpectedly succeeded, stdout: %s", out)
		}
		if !strings.Contains(errText, "branch protection blocks direct merges") {
			t.Errorf("stderr = %q, want the forge message", errText)
		}
		if srv.count() != 2 {
			t.Errorf("made %d requests, want 2 (no read-back)", srv.count())
		}
	})

	// --- allod/tools#246: the merged flag is never trusted alone ---

	t.Run("read-back where the PR still says merged false", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{},
			cannedResponse{Body: `{"number":12,"state":"open","merged":false,"head":{"sha":"` + mergeHeadSHA + `","ref":"topic"},"base":{"ref":"master"}}`},
		)
		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m")
		if code == 0 {
			t.Fatalf("unexpectedly succeeded, stdout: %s", out)
		}
		if !strings.Contains(errText, "merge not confirmed") {
			t.Errorf("stderr = %q, want \"merge not confirmed\"", errText)
		}
		if srv.count() != 3 {
			t.Errorf("made %d requests, want 3 (no branch/compare read)", srv.count())
		}
	})

	t.Run("read-back where the base tip differs but compare says 0 ahead", func(t *testing.T) {
		srv := newPRMergeServer(t)
		staleTip := "cccccccccccccccccccccccccccccccccccccccc"
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{},
			cannedResponse{Body: mergedPR},
			baseBranchAt(staleTip),
			cannedResponse{Body: `{"total_commits":0}`},
		)

		var out, errText string
		var code int
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"POST", "/api/v1/repos/acme/widget/pulls/12/merge", nil},
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"GET", "/api/v1/repos/acme/widget/branches/master", nil},
			{"GET", "/api/v1/repos/acme/widget/compare/master..." + mergeSHA, nil},
		}, func() {
			out, errText, code = runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m")
		})
		want := "PR merged: " + mergeHTMLURL + " (master at " + mergeSHA + ")\n"
		if code != 0 || out != want || errText != "" {
			t.Errorf("got (%q, %q, %d), want (%q, \"\", 0)", out, errText, code, want)
		}
	})

	t.Run("read-back where compare says 1 ahead exits non-zero with both hashes", func(t *testing.T) {
		srv := newPRMergeServer(t)
		staleTip := "cccccccccccccccccccccccccccccccccccccccc"
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{},
			cannedResponse{Body: mergedPR},
			baseBranchAt(staleTip),
			cannedResponse{Body: `{"total_commits":1}`},
		)
		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m")
		if code == 0 {
			t.Fatalf("unexpectedly succeeded, stdout: %s", out)
		}
		if !strings.Contains(errText, "merged flag set but master does not contain "+mergeSHA) {
			t.Errorf("stderr = %q, want the allod/tools#246 message", errText)
		}
		if !strings.Contains(errText, staleTip) {
			t.Errorf("stderr = %q, want the stale base tip hash too", errText)
		}
	})

	// --- -d / --delete-branch ---

	t.Run("delete-branch: branch is gone (404)", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{},
			cannedResponse{Body: mergedPR},
			baseBranchAt(mergeSHA),
			cannedResponse{Status: 404, Body: `{"message":"branch does not exist"}`},
		)
		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m", "-d")
		want := "PR merged: " + mergeHTMLURL + " (master at " + mergeSHA + ") (branch topic deleted)\n"
		if code != 0 || out != want {
			t.Errorf("got (%q, %d), want (%q, 0)", out, code, want)
		}
		if errText != "" {
			t.Errorf("stderr = %q, want empty on a clean delete", errText)
		}
		if body := string(srv.requests()[1].Body); !strings.Contains(body, `"delete_branch_after_merge":true`) {
			t.Errorf("merge POST body = %s, want delete_branch_after_merge true", body)
		}
	})

	t.Run("delete-branch: existence check fails with another status", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{},
			cannedResponse{Body: mergedPR},
			baseBranchAt(mergeSHA),
			cannedResponse{Status: 500, Body: `{"message":"internal"}`},
		)
		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m", "-d")
		want := "PR merged: " + mergeHTMLURL + " (master at " + mergeSHA + ")\n"
		if code != 0 || out != want {
			t.Errorf("got (%q, %d), want (%q, 0)", out, code, want)
		}
		if !strings.Contains(errText, "could not confirm branch topic deleted (HTTP 500)") {
			t.Errorf("stderr = %q, want the unconfirmed-deletion warning with the status", errText)
		}
	})

	t.Run("read-back: base branch response without a commit id fails", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{},
			cannedResponse{Body: mergedPR},
			cannedResponse{Body: `{"name":"master"}`},
			cannedResponse{Body: `{"total_commits":0}`},
		)
		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m")
		if code == 0 {
			t.Fatalf("unexpectedly succeeded, stdout: %s", out)
		}
		if !strings.Contains(errText, "no commit id in the master branch response") {
			t.Errorf("stderr = %q, want the missing-tip message", errText)
		}
		if srv.count() != 4 {
			t.Errorf("made %d requests, want 4 (no compare on a malformed tip)", srv.count())
		}
	})

	t.Run("delete-branch: branch still present", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{},
			cannedResponse{Body: mergedPR},
			baseBranchAt(mergeSHA),
			cannedResponse{Body: `{"name":"topic"}`},
		)
		out, errText, code := runForge(t, "-R", "acme/widget", "pr", "merge", "12", "-m", "-d")
		want := "PR merged: " + mergeHTMLURL + " (master at " + mergeSHA + ")\n"
		if code != 0 || out != want {
			t.Errorf("got (%q, %d), want (%q, 0)", out, code, want)
		}
		if !strings.Contains(errText, "forge: warning: branch topic not deleted") {
			t.Errorf("stderr = %q, want the not-deleted warning", errText)
		}
	})

	// --- Title and body options reach the POST body ---

	t.Run("-t sets MergeTitleField", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{},
			cannedResponse{Body: mergedPR},
			baseBranchAt(mergeSHA),
		)
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"POST", "/api/v1/repos/acme/widget/pulls/12/merge", map[string]any{
				"Do": "merge", "head_commit_id": mergeHeadSHA, "delete_branch_after_merge": false,
				"MergeTitleField": "Custom title",
			}},
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"GET", "/api/v1/repos/acme/widget/branches/master", nil},
		}, func() {
			runOK(t, "pr", "merge", "12", "-m", "-t", "Custom title")
		})
	})

	t.Run("-b sets MergeMessageField", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{},
			cannedResponse{Body: mergedPR},
			baseBranchAt(mergeSHA),
		)
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"POST", "/api/v1/repos/acme/widget/pulls/12/merge", map[string]any{
				"Do": "merge", "head_commit_id": mergeHeadSHA, "delete_branch_after_merge": false,
				"MergeMessageField": "Because reasons",
			}},
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"GET", "/api/v1/repos/acme/widget/branches/master", nil},
		}, func() {
			runOK(t, "pr", "merge", "12", "-m", "-b", "Because reasons")
		})
	})

	t.Run("-F file sets MergeMessageField", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{},
			cannedResponse{Body: mergedPR},
			baseBranchAt(mergeSHA),
		)
		path := writeTempFile(t, "From a file\n")
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"POST", "/api/v1/repos/acme/widget/pulls/12/merge", map[string]any{
				"Do": "merge", "head_commit_id": mergeHeadSHA, "delete_branch_after_merge": false,
				"MergeMessageField": "From a file\n",
			}},
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"GET", "/api/v1/repos/acme/widget/branches/master", nil},
		}, func() {
			runOK(t, "pr", "merge", "12", "-m", "-F", path)
		})
	})

	t.Run("-F - reads the message from stdin", func(t *testing.T) {
		srv := newPRMergeServer(t)
		srv.enqueue(
			cannedResponse{Body: openMergeablePR},
			cannedResponse{},
			cannedResponse{Body: mergedPR},
			baseBranchAt(mergeSHA),
		)
		useStdin(t, "From stdin")
		assertScenario(t, srv, []expReq{
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"POST", "/api/v1/repos/acme/widget/pulls/12/merge", map[string]any{
				"Do": "merge", "head_commit_id": mergeHeadSHA, "delete_branch_after_merge": false,
				"MergeMessageField": "From stdin",
			}},
			{"GET", "/api/v1/repos/acme/widget/pulls/12", nil},
			{"GET", "/api/v1/repos/acme/widget/branches/master", nil},
		}, func() {
			runOK(t, "pr", "merge", "12", "-m", "-F", "-")
		})
	})
}
