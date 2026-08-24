package main

// Retained help scenarios from the retired shell suite.
//
// Bash-test cases intentionally omitted here because
// cmd/forge/harness_test.go's TestHelpRouting already pins them
// byte-for-byte (same args, same "contains" substring, same usage text):
//   - "pr --help" / "issue -h" / "label -h" / "milestone -h" (resource-level
//     help; help.sh lines 108-118)
//   - "project --help" (help.sh line 94-95; TestHelpRouting also covers the
//     "project <command> --help" form)
//   - "token verify --help" (help.sh line 100-101)
//   - "auth status -h" (help.sh line 103-104)
//   - "help commands make no API requests" (help.sh line 122); TestHelpRouting
//     asserts the same thing for its own set of invocations, and this file's
//     TestCommandHelp repeats the check for the command-level invocations
//     below rather than skip it outright.
//
// Everything else in help.sh — every command-level --help/-h text — is
// ported below as one table.

import (
	"strings"
	"testing"
)

func TestCommandHelp(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{})
	useServer(t, srv)
	useToken(t, fakeToken)

	tests := []struct {
		name     string
		args     []string
		key      string
		contains []string
	}{
		// --- Command-level --help exits zero and prints usage ---
		{"pr create --help", []string{"pr", "create", "--help"}, "pr create", []string{"--title", "--head"}},
		{"pr create -h", []string{"pr", "create", "-h"}, "pr create", []string{"--title"}},
		{"issue create --help", []string{"issue", "create", "--help"}, "issue create", []string{"--title", "--body-file", "--label", "--milestone"}},
		{"pr close --help", []string{"pr", "close", "--help"}, "pr close", []string{"--comment", "--delete-branch"}},
		{"issue close --help", []string{"issue", "close", "--help"}, "issue close", []string{"--comment", "--reason", "--duplicate-of"}},
		{"pr edit --help", []string{"pr", "edit", "--help"}, "pr edit", []string{"--title"}},
		{"pr comment --help", []string{"pr", "comment", "--help"}, "pr comment", []string{"--body"}},
		{"issue comment --help", []string{"issue", "comment", "--help"}, "issue comment", []string{"--body"}},
		{"pr reply --help", []string{"pr", "reply", "--help"}, "pr reply", []string{"--body", "<comment-id>"}},

		// --- Read-only commands ---
		{"pr list --help", []string{"pr", "list", "--help"}, "pr list", []string{"--repo"}},
		{"pr snapshot --help", []string{"pr", "snapshot", "--help"}, "pr snapshot", []string{"<number>", "versioned JSON"}},
		{"issue view -h", []string{"issue", "view", "-h"}, "issue view", []string{"<number>"}},
		{"pr find-by-head --help", []string{"pr", "find-by-head", "--help"}, "pr find-by-head", []string{"<branch>"}},
		{"issue edit -h", []string{"issue", "edit", "-h"}, "issue edit", []string{"--title", "--body-file", "--remove-milestone", "--add-label"}},
		{"issue labels -h", []string{"issue", "labels", "-h"}, "issue labels", []string{"--add-label", "--clear"}},
		{"issue milestone -h", []string{"issue", "milestone", "-h"}, "issue milestone", []string{"--clear"}},
		{"issue list --help", []string{"issue", "list", "--help"}, "issue list", []string{"--repo", "--limit", "--label"}},
		{"label list --help", []string{"label", "list", "--help"}, "label list", []string{"repository labels", "--search", "--sort"}},
		{"label create --help", []string{"label", "create", "--help"}, "label create", []string{"--color", "--force"}},
		{"label edit --help", []string{"label", "edit", "--help"}, "label edit", []string{"--no-exclusive"}},
		{"label delete --help", []string{"label", "delete", "--help"}, "label delete", []string{"--yes"}},
		{"milestone list --help", []string{"milestone", "list", "--help"}, "milestone list", []string{"--state"}},
		{"milestone create --help", []string{"milestone", "create", "--help"}, "milestone create", []string{"--due"}},
		{"milestone edit --help", []string{"milestone", "edit", "--help"}, "milestone edit", []string{"--state"}},
		{"pr review-comments -h", []string{"pr", "review-comments", "-h"}, "pr review-comments", []string{"<number>"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := commandUsageTexts[tt.key]
			if want == "" {
				t.Fatalf("usage.go has no commandUsageTexts entry for %q", tt.key)
			}
			out, errText, code := runForge(t, tt.args...)
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			if out != want {
				t.Errorf("stdout mismatch\ngot:  %q\nwant: %q", truncate(out), truncate(want))
			}
			if errText != "" {
				t.Errorf("stderr = %q, want empty", errText)
			}
			for _, sub := range tt.contains {
				if !strings.Contains(out, sub) {
					t.Errorf("stdout does not contain %q\ngot: %q", sub, truncate(out))
				}
			}
		})
	}

	// help.sh: "help commands make no API requests" (line 122), reasserted for
	// this file's own set of invocations.
	if n := srv.count(); n != 0 {
		t.Errorf("command help made %d API requests, want 0", n)
	}
}
