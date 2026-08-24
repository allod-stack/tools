package main

// Retained validation scenarios from the retired shell suite.
//
// Bash-test cases intentionally omitted here because
// cmd/forge/harness_test.go already pins them byte-for-byte:
//   - "rejects a command-level repo flag without a value" (validation.sh
//     line 11-12, `issue list --repo`): the message ("forge: --repo requires
//     a value") is produced by setRepoOption/requireOptionValue regardless of
//     which command's argument loop calls it, and TestParseRepoArgs's
//     "missing repo value" subtest plus TestGlobalRepoFlagRequiresValue
//     already pin that exact mechanism and exact text.
//   - "reports unavailable project API" (validation.sh line 49-50, `project
//     list`): TestProjectCommandsUnavailable already runs exactly this and
//     asserts the exact stderr text.
//
// Every other validation.sh case is ported below as one table. bash's
// run_fail helper always runs the CLI as `forge -R acme/widget <args...>`, so
// -R acme/widget is prepended here the same way; requireRepo() never needs to
// infer anything (useNoInferRepo enforces that), and no scenario reaches the
// network (bash's own final assertion — "makes no API requests for invalid
// commands" — is reasserted at the bottom via the recording server's request
// count).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidation(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("body\n"), 0o600); err != nil {
		t.Fatalf("writing body fixture: %v", err)
	}

	// A URL that shares FORGE_URL's host but names the wrong resource type,
	// built from the recording server's own URL so it genuinely matches the
	// "$base/*" case in resolve_pr_target instead of merely failing the
	// leading prefix check (validation.sh line 69-70: the fixture forge is
	// literally FORGE_URL, so the same distinction applies there).
	malformedPRURL := srv.URL + "/acme/widget/issues/12"

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"rejects conflicting body options", []string{"pr", "create", "-t", "title", "-b", "one", "-F", bodyFile}, "cannot be combined"},
		{"rejects a duplicate title option", []string{"pr", "create", "-t", "one", "--title", "two"}, "specified more than once"},
		{"rejects an option without a value", []string{"pr", "create", "--title"}, "requires a value"},
		{"rejects an empty head branch", []string{"pr", "create", "--title", "title", "--head", ""}, "--head cannot be empty"},
		{"rejects an empty base branch", []string{"pr", "create", "--title", "title", "--base", ""}, "--base cannot be empty"},
		{"requires a title when creating an issue", []string{"issue", "create", "--body", "body"}, "requires --title"},
		{"requires a change when editing an issue", []string{"issue", "edit", "20"}, "issue edit requires"},
		{"rejects conflicting issue milestone edit flags", []string{"issue", "edit", "20", "--milestone", "July batch", "--remove-milestone"}, "cannot be combined"},
		{"requires a positive pull request snapshot number", []string{"pr", "snapshot", "nope"}, "PR number must be a positive integer"},
		{"requires one pull request snapshot number", []string{"pr", "snapshot"}, "usage: forge pr snapshot"},
		{"requires a pull request comment body", []string{"pr", "comment", "12"}, "requires --body or --body-file"},
		{"requires an issue comment body", []string{"issue", "comment", "20"}, "requires --body or --body-file"},
		{"rejects clear plus issue label changes", []string{"issue", "labels", "20", "--clear", "--add-label", "bug"}, "--clear cannot be combined"},
		{"rejects set plus additive label changes", []string{"issue", "labels", "20", "--set", "bug", "--remove-label", "triage"}, "--set cannot be combined"},
		{"rejects trailing comma-separated label values", []string{"issue", "edit", "20", "--add-label", "bug,"}, "cannot contain an empty value"},
		{"rejects clear plus issue milestone value", []string{"issue", "milestone", "20", "July batch", "--clear"}, "cannot be combined"},
		{"requires a name when creating a label", []string{"label", "create", "--color", "123456"}, "label create requires a name"},
		{"rejects invalid label color", []string{"label", "create", "bug", "--color", "zzzzzz"}, "color must be a 6-digit hex value"},
		{"requires a label edit change", []string{"label", "edit", "bug"}, "label edit requires a change"},
		{"requires confirmation when deleting a label", []string{"label", "delete", "bug"}, "label delete requires --yes"},
		{"rejects invalid milestone list state", []string{"milestone", "list", "--state", "invalid"}, "--state must be one of"},
		{"requires title when creating a milestone", []string{"milestone", "create", "--due", "2026-08-31"}, "milestone create requires --title"},
		{"requires a milestone edit change", []string{"milestone", "edit", "July batch"}, "milestone edit requires a change"},
		{"rejects legacy positional PR creation", []string{"pr", "create", "legacy title", "topic"}, "unexpected argument"},
		{"rejects an unknown PR edit option", []string{"pr", "edit", "12", "--unknown", "value"}, "unknown option"},
		{"requires an issue close target", []string{"issue", "close"}, "usage: forge issue close"},
		{"rejects an invalid close reason", []string{"issue", "close", "20", "--reason", "invalid"}, "--reason must be one of"},
		{"rejects conflicting duplicate close options", []string{"issue", "close", "20", "--reason", "completed", "--duplicate-of", "12"}, "cannot be combined"},
		{"rejects a foreign issue URL", []string{"issue", "close", "https://other.example/acme/widget/issues/20"}, "issue target must be a number or URL"},
		{"rejects an invalid duplicate target", []string{"issue", "close", "20", "--duplicate-of", "nope"}, "duplicate target must be a number or URL"},
		{"requires a PR close target", []string{"pr", "close"}, "usage: forge pr close"},
		{"rejects an unknown PR close option", []string{"pr", "close", "12", "--unknown"}, "unknown option"},
		{"rejects a foreign PR URL", []string{"pr", "close", "https://other.example/acme/widget/pulls/12"}, "PR target must be a number, URL"},
		{"rejects a malformed PR URL", []string{"pr", "close", malformedPRURL}, "PR target must be a number, URL"},
		{"rejects an empty PR closing comment", []string{"pr", "close", "12", "-c", ""}, "--comment cannot be empty"},
		{"rejects duplicate pr close comment flag", []string{"pr", "close", "12", "-c", "first", "-c", "second"}, "specified more than once"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			full := append([]string{"-R", "acme/widget"}, tt.args...)
			out, errText, code := runForge(t, full...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", truncate(out))
			}
			if !strings.Contains(errText, tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errText, tt.wantErr)
			}
		})
	}

	// validation.sh line 75: "makes no API requests for invalid commands".
	if n := srv.count(); n != 0 {
		t.Errorf("validation failures made %d API requests, want 0", n)
	}
}
