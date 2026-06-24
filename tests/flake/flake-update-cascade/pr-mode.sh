#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_home pr
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=pr
output=$(bash "$ROOT/flake/flake-update-cascade" demo --pr)
assert_contains "$output" "aaaaaaa → bbbbbbb" "reports the updated revision"
assert_contains "$output" "PR #42 updated" "reports an existing PR update"
assert_log_contains $'git\tapp\tcheckout -B agent/flake-update-demo' \
  "creates or resets the update branch"
assert_log_contains $'git\tapp\tcommit -m flake.lock: update demo' \
  "commits the updated lock file"
assert_log_contains $'git\tapp\tpush --force-with-lease origin agent/flake-update-demo' \
  "pushes the update branch with lease protection"
assert_log_contains $'forge\t-R acme/app pr find-by-head agent/flake-update-demo' \
  "looks up an existing PR for the update branch"
assert_log_contains $'git\tapp\tcheckout master' \
  "returns to the default branch"

finish_tests "flake-update-cascade PR-mode"
