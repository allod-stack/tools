#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_home preflight
write_direct_lock "$HOME/work/dirty-repo"
export MOCK_SCENARIO=dirty
run_fail "Pre-flight checks failed — no changes made." \
  "blocks the cascade when preflight fails" demo
assert_equal "$(grep -c '^nix' "$MOCK_LOG" || true)" "0" \
  "does not invoke Nix after a preflight failure"

new_home combined-errors
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=wrong-branch MOCK_FAIL_GIT='diff --quiet'
output=$(run_cascade demo 2>&1) && status=0 || status=$?
assert_equal "$status" "1" "combined preflight errors return the oracle status"
assert_contains "$output" "expected 'master'),dirty working tree (unstaged changes)" \
  "preserves the oracle separator for combined preflight errors"
unset MOCK_FAIL_GIT
assert_equal "$(grep -c '^nix' "$MOCK_LOG" || true)" "0" \
  "does not invoke Nix after combined preflight failures"

new_home skips
mkdir -p "$HOME/work/no-lock/.git"
mkdir -p "$HOME/work/no-input/.git"
: > "$HOME/work/no-lock/.git/HEAD"
: > "$HOME/work/no-input/.git/HEAD"
printf '%s\n' '{"nodes":{"root":{"inputs":{}}}}' > "$HOME/work/no-input/flake.lock"
write_follow_lock "$HOME/work/follows"
write_direct_lock "$HOME/work/protected"
printf '%s\n' "work/protected master" > "$HOME/.config/git/protected-branches"
write_direct_lock "$HOME/work/active"
printf '%s\n' "active" > "$HOME/.config/git/active-pr-branches"
export MOCK_SCENARIO=skips
output=$(run_cascade demo)
assert_contains "$output" "no flake.lock, skipping" \
  "skips repositories without a lock file"
assert_contains "$output" "no directly pinned demo input found, skipping" \
  "skips repositories without a reachable direct pin"
assert_contains "$output" "protected branch (master)" \
  "skips protected branches in direct mode"
assert_contains "$output" "listed in active-pr-branches (GPG-signed commits required), skipping — handle manually" \
  "skips repositories listed in active-pr-branches"
assert_equal "$(grep -c '^nix' "$MOCK_LOG" || true)" "0" \
  "does not invoke Nix when every repository is skipped"

finish_tests "flake-update-cascade preflight"
