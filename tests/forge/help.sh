#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

# --- Command-level --help exits zero and prints usage ---

output=$(run_capture pr create --help)
assert_contains "$output" "--title" "pr create --help shows --title flag"
assert_contains "$output" "--head" "pr create --help shows --head flag"

output=$(run_capture pr create -h)
assert_contains "$output" "--title" "pr create -h shows --title flag"

output=$(run_capture issue create --help)
assert_contains "$output" "--title" "issue create --help shows --title flag"
assert_contains "$output" "--body-file" "issue create --help shows --body-file flag"

output=$(run_capture pr close --help)
assert_contains "$output" "--comment" "pr close --help shows --comment flag"
assert_contains "$output" "--delete-branch" "pr close --help shows --delete-branch flag"

output=$(run_capture issue close --help)
assert_contains "$output" "--comment" "issue close --help shows --comment flag"
assert_contains "$output" "--reason" "issue close --help shows --reason flag"
assert_contains "$output" "--duplicate-of" "issue close --help shows --duplicate-of flag"

output=$(run_capture pr edit --help)
assert_contains "$output" "--title" "pr edit --help shows --title flag"

output=$(run_capture pr comment --help)
assert_contains "$output" "--body" "pr comment --help shows --body flag"

output=$(run_capture pr reply --help)
assert_contains "$output" "--body" "pr reply --help shows --body flag"
assert_contains "$output" "<comment-id>" "pr reply --help shows comment-id positional"

# --- Read-only commands ---

output=$(run_capture pr list --help)
assert_contains "$output" "--repo" "pr list --help shows --repo flag"

output=$(run_capture issue view -h)
assert_contains "$output" "<number>" "issue view -h shows number positional"

output=$(run_capture pr find-by-head --help)
assert_contains "$output" "<branch>" "pr find-by-head --help shows branch positional"

output=$(run_capture issue edit -h)
assert_contains "$output" "--title" "issue edit -h shows --title flag"
assert_contains "$output" "--body-file" "issue edit -h shows --body-file flag"

output=$(run_capture issue list --help)
assert_contains "$output" "--repo" "issue list --help shows --repo flag"

output=$(run_capture pr review-comments -h)
assert_contains "$output" "<number>" "pr review-comments -h shows number positional"

output=$(run_capture token verify --help)
assert_contains "$output" "stdin" "token verify --help mentions stdin"

output=$(run_capture auth status -h)
assert_contains "$output" "credential" "auth status -h mentions credential"

# --- Resource-level help exits zero ---

output=$(run_capture pr --help)
assert_contains "$output" "PR commands" "pr --help shows PR commands section"

output=$(run_capture issue -h)
assert_contains "$output" "Issue commands" "issue -h shows Issue commands section"

# --- No API calls made ---

assert_equal "$(request_count)" "0" "help commands make no API requests"

finish_tests "Forge command-level help"
