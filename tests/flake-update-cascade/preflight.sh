#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_home preflight
write_direct_lock "$HOME/work/dirty-repo"
export MOCK_SCENARIO=dirty
run_fail "Pre-flight checks failed — no changes made." \
  "blocks the cascade when preflight fails" demo
assert_equal "$(grep -c '^nix' "$MOCK_LOG" || true)" "0" \
  "does not invoke Nix after a preflight failure"

new_home skips
mkdir -p "$HOME/work/no-lock/.git"
mkdir -p "$HOME/work/no-input/.git"
printf '%s\n' '{"nodes":{"root":{"inputs":{}}}}' > "$HOME/work/no-input/flake.lock"
write_follow_lock "$HOME/work/follows"
write_direct_lock "$HOME/work/protected"
printf '%s\n' "work/protected master" > "$HOME/.config/git/protected-branches"
export MOCK_SCENARIO=skips
output=$(bash "$ROOT/flake-update-cascade" demo)
assert_contains "$output" "no flake.lock, skipping" \
  "skips repositories without a lock file"
assert_contains "$output" "no directly pinned demo input found, skipping" \
  "skips repositories without a reachable direct pin"
assert_contains "$output" "protected branch (master)" \
  "skips protected branches in direct mode"
assert_equal "$(grep -c '^nix' "$MOCK_LOG" || true)" "0" \
  "does not invoke Nix when every repository is skipped"

finish_tests "flake-update-cascade preflight"
