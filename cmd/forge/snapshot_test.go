package main

// Retained snapshot scenarios from the retired shell suite. The response
// bodies preserve its mock Forgejo objects, so each contract case remains
// pinned against the Go command.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const snapshot12 = `{"number":12,"html_url":"https://forge.example/acme/widget/pulls/12","title":"Improve tool","state":"open","body":"PR body","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","closed_at":null,"merged":false,"merged_at":null,"user":{"login":"alice"},"head":{"label":"acme:topic","ref":"topic","sha":"2222222222222222222222222222222222222222","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}},"base":{"label":"master","ref":"master","sha":"1111111111111111111111111111111111111111","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`

const snapshot12Output = `{
  "schema_version": 1,
  "pull_request": {
    "number": 12,
    "url": "https://forge.example/acme/widget/pulls/12",
    "title": "Improve tool",
    "body": "PR body",
    "state": "open",
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-02T00:00:00Z",
    "closed_at": null,
    "merged": false,
    "merged_at": null
  },
  "base": {
    "repository": {
      "owner": "acme",
      "name": "widget",
      "full_name": "acme/widget",
      "clone_url": "https://forge.example/acme/widget.git"
    },
    "ref": "master",
    "sha": "1111111111111111111111111111111111111111"
  },
  "head": {
    "repository": {
      "owner": "acme",
      "name": "widget",
      "full_name": "acme/widget",
      "clone_url": "https://forge.example/acme/widget.git"
    },
    "ref": "topic",
    "sha": "2222222222222222222222222222222222222222"
  }
}
`

func TestPRSnapshotProjectsStableSchema(t *testing.T) {
	srv, out, errText, code := runSnapshotFixture(t, "12", snapshot12)
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want the snapshot and success", truncate(out), errText, code)
	}
	if out != snapshot12Output {
		t.Errorf("snapshot output mismatch\ngot:\n%s\nwant:\n%s", out, snapshot12Output)
	}
	if n := srv.count(); n != 1 {
		t.Fatalf("request count = %d, want 1", n)
	}
	srv.assertRequest(t, 0, "GET", "/api/v1/repos/acme/widget/pulls/12")
}

func TestPRSnapshotKeepsForkRepositoryIdentity(t *testing.T) {
	body := `{"number":41,"html_url":"https://forge.example/acme/widget/pulls/41","title":"Fork contribution","state":"open","body":null,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","closed_at":null,"merged":false,"merged_at":null,"head":{"label":"contributor:topic","ref":"topic","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"contributor"},"name":"widget-fork","full_name":"contributor/widget-fork","clone_url":"https://forge.example/contributor/widget-fork.git"}},"base":{"label":"master","ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`
	_, out, errText, code := runSnapshotFixture(t, "41", body)
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want the fork snapshot and success", truncate(out), errText, code)
	}

	var got prSnapshot
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding snapshot output: %v", err)
	}
	if got.PullRequest.Body != "" || got.Base.Repository.FullName != "acme/widget" ||
		got.Head.Repository != (snapshotRepository{
			Owner: "contributor", Name: "widget-fork", FullName: "contributor/widget-fork",
			CloneURL: "https://forge.example/contributor/widget-fork.git",
		}) || got.Head.Ref != "topic" || got.Head.SHA != "4444444444444444444444444444444444444444" {
		t.Errorf("fork projection = %#v", got)
	}
}

func TestPRSnapshotRejectsMalformedOrContradictoryMetadata(t *testing.T) {
	tests := []struct {
		number string
		name   string
		body   string
	}{
		{"42", "malformed object ID", `{"number":42,"html_url":"https://forge.example/acme/widget/pulls/42","title":"Malformed snapshot","body":"Bad head object ID","head":{"ref":"topic","sha":"not-an-object-id","repo":{"owner":{"login":"contributor"},"name":"widget-fork","full_name":"contributor/widget-fork","clone_url":"https://forge.example/contributor/widget-fork.git"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
		{"43", "different pull request number", `{"number":99,"html_url":"https://forge.example/acme/widget/pulls/43","title":"Wrong pull request","body":"Mismatched number","head":{"ref":"topic","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
		{"44", "missing pull request ref", `{"number":44,"html_url":"https://forge.example/acme/widget/pulls/44","title":"Missing ref","body":"Empty head ref","head":{"ref":"","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"contributor"},"name":"widget-fork","full_name":"contributor/widget-fork","clone_url":"https://forge.example/contributor/widget-fork.git"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
		{"45", "missing repository clone metadata", `{"number":45,"html_url":"https://forge.example/acme/widget/pulls/45","title":"Missing repository metadata","body":"No head clone URL","head":{"ref":"topic","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"contributor"},"name":"widget-fork","full_name":"contributor/widget-fork"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
		{"46", "credential-bearing clone URL", `{"number":46,"html_url":"https://forge.example/acme/widget/pulls/46","title":"Credential-bearing clone URL","body":"Unsafe transport metadata","head":{"ref":"topic","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"contributor"},"name":"widget-fork","full_name":"contributor/widget-fork","clone_url":"https://token@forge.example/contributor/widget-fork.git"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
		{"47", "control characters in terminal-facing text", `{"number":47,"html_url":"https://forge.example/acme/widget/pulls/47","title":"Spoof\u000aRunner: claude","body":"Unsafe terminal text","head":{"ref":"topic","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"contributor"},"name":"widget-fork","full_name":"contributor/widget-fork","clone_url":"https://forge.example/contributor/widget-fork.git"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
		{"48", "mixed Git object formats", `{"number":48,"html_url":"https://forge.example/acme/widget/pulls/48","title":"Mixed object formats","body":"Impossible fork metadata","head":{"ref":"topic","sha":"4444444444444444444444444444444444444444444444444444444444444444","repo":{"owner":{"login":"contributor"},"name":"widget-fork","full_name":"contributor/widget-fork","clone_url":"https://forge.example/contributor/widget-fork.git"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
		{"49", "query-bearing clone URL", `{"number":49,"html_url":"https://forge.example/acme/widget/pulls/49","title":"Credential query","body":"Unsafe transport metadata","head":{"ref":"topic","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"contributor"},"name":"widget-fork","full_name":"contributor/widget-fork","clone_url":"https://forge.example/contributor/widget-fork.git?token=credential-material"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
		{"50", "fork clone URL contradicting repository identity", `{"number":50,"html_url":"https://forge.example/acme/widget/pulls/50","title":"Misdirected fork","body":"Unsafe repository binding","head":{"ref":"topic","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"contributor"},"name":"widget-fork","full_name":"contributor/widget-fork","clone_url":"https://outside.example/unrelated.git"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
		{"56", "malformed created_at timestamp", `{"number":56,"html_url":"https://forge.example/acme/widget/pulls/56","title":"Bad timestamp","body":"Not RFC 3339","state":"open","created_at":"not-a-timestamp","updated_at":"2026-01-02T00:00:00Z","closed_at":null,"merged":false,"merged_at":null,"head":{"ref":"topic","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
		{"57", "merged true with a null merged_at", `{"number":57,"html_url":"https://forge.example/acme/widget/pulls/57","title":"Contradictory merge state","body":"merged without a date","state":"closed","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","closed_at":"2026-01-03T00:00:00Z","merged":true,"merged_at":null,"head":{"ref":"topic","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
		{"58", "merged_at set with merged false", `{"number":58,"html_url":"https://forge.example/acme/widget/pulls/58","title":"Contradictory merge state","body":"a date without merged","state":"closed","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","closed_at":"2026-01-03T00:00:00Z","merged":false,"merged_at":"2026-01-03T00:00:00Z","head":{"ref":"topic","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
		{"59", "closed_at on an open pull request", `{"number":59,"html_url":"https://forge.example/acme/widget/pulls/59","title":"Contradictory close state","body":"closed_at while open","state":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","closed_at":"2026-01-03T00:00:00Z","merged":false,"merged_at":null,"head":{"ref":"topic","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, out, errText, code := runSnapshotFixture(t, tt.number, tt.body)
			if code != 1 || out != "" || !strings.Contains(errText, "missing or has malformed snapshot fields") {
				t.Errorf("got (%q, %q, %d), want the malformed-snapshot error and exit 1", out, errText, code)
			}
			if n := srv.count(); n != 1 {
				t.Errorf("request count = %d, want 1 before validation fails", n)
			}
		})
	}
}

func TestPRSnapshotAGitHeadRefPolicy(t *testing.T) {
	tests := []struct {
		number string
		name   string
		head   string
		base   string
		ok     bool
	}{
		{"51", "accepts this pull request's AGit head ref", "refs/pull/51/head", "master", true},
		{"52", "rejects an AGit head ref naming another pull request", "refs/pull/999/head", "master", false},
		{"53", "rejects an explicit ref outside the AGit namespace", "refs/heads/evil", "master", false},
		{"54", "rejects a head ref beginning with a dash", "-x", "master", false},
		{"55", "rejects an AGit-shaped base ref", "topic", "refs/pull/55/head", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"number":%s,"html_url":"https://forge.example/acme/widget/pulls/%s","title":"AGit fixture","body":"ref policy","state":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","closed_at":null,"merged":false,"merged_at":null,"head":{"ref":%q,"sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}},"base":{"ref":%q,"sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`, tt.number, tt.number, tt.head, tt.base)
			_, out, errText, code := runSnapshotFixture(t, tt.number, body)
			if tt.ok {
				if code != 0 || errText != "" || !strings.Contains(out, `"ref": "refs/pull/51/head"`) {
					t.Errorf("got (%q, %q, %d), want the AGit head in a successful snapshot", truncate(out), errText, code)
				}
				return
			}
			if code != 1 || out != "" || !strings.Contains(errText, "missing or has malformed snapshot fields") {
				t.Errorf("got (%q, %q, %d), want the malformed-snapshot error and exit 1", out, errText, code)
			}
		})
	}
}

// TestPRSnapshotProjectsMergeAndCloseState covers the two closed shapes the
// open case in TestPRSnapshotProjectsStableSchema does not: a closed pull
// request that was never merged, and one that was.
func TestPRSnapshotProjectsMergeAndCloseState(t *testing.T) {
	tests := []struct {
		number    string
		name      string
		state     string
		merged    bool
		mergedAt  string // "" renders JSON null
		wantMerge string
	}{
		{"60", "closed and unmerged", "closed", false, "", `"merged": false,
    "merged_at": null`},
		{"61", "closed and merged", "closed", true, "2026-01-03T00:00:00Z", `"merged": true,
    "merged_at": "2026-01-03T00:00:00Z"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mergedAtJSON := "null"
			if tt.mergedAt != "" {
				mergedAtJSON = `"` + tt.mergedAt + `"`
			}
			body := fmt.Sprintf(`{"number":%s,"html_url":"https://forge.example/acme/widget/pulls/%s","title":"Close fixture","body":"close/merge state","state":%q,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","closed_at":"2026-01-03T00:00:00Z","merged":%t,"merged_at":%s,"head":{"ref":"topic","sha":"4444444444444444444444444444444444444444","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}},"base":{"ref":"master","sha":"3333333333333333333333333333333333333333","repo":{"owner":{"login":"acme"},"name":"widget","full_name":"acme/widget","clone_url":"https://forge.example/acme/widget.git"}}}`,
				tt.number, tt.number, tt.state, tt.merged, mergedAtJSON)

			_, out, errText, code := runSnapshotFixture(t, tt.number, body)
			if code != 0 || errText != "" {
				t.Fatalf("got (%q, %q, %d), want success", truncate(out), errText, code)
			}
			if !strings.Contains(out, `"state": "closed"`) ||
				!strings.Contains(out, `"closed_at": "2026-01-03T00:00:00Z"`) ||
				!strings.Contains(out, tt.wantMerge) {
				t.Errorf("snapshot = %s, want it to carry state/closed_at/merged fields matching the fixture", out)
			}
		})
	}
}

func runSnapshotFixture(t *testing.T, number, body string) (*recordingServer, string, string, int) {
	t.Helper()
	path := "/api/v1/repos/acme/widget/pulls/" + number
	srv := newRecordingServer(t, map[string]cannedResponse{path: {Body: body}})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)
	out, errText, code := runForge(t, "-R", "acme/widget", "pr", "snapshot", number)
	return srv, out, errText, code
}
