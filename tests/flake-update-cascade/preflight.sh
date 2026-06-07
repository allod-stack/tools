#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_home preflight
write_direct_lock "$HOME/work/dirty-repo"
export MOCK_SCENARIO=dirty
run_fail "Pre-flight checks failed — no changes made." demo
[[ "$(grep -c '^nix' "$MOCK_LOG" || true)" == 0 ]] ||
  fail "preflight failure invoked nix"

new_home skips
mkdir -p "$HOME/work/no-lock/.git"
mkdir -p "$HOME/work/no-input/.git"
printf '%s\n' '{"nodes":{"root":{"inputs":{}}}}' > "$HOME/work/no-input/flake.lock"
write_follow_lock "$HOME/work/follows"
write_direct_lock "$HOME/work/protected"
printf '%s\n' "work/protected master" > "$HOME/.config/git/protected-branches"
export MOCK_SCENARIO=skips
output=$(bash "$ROOT/flake-update-cascade" demo)
[[ "$output" == *"no flake.lock, skipping"* ]] || fail "no-lock skip missing"
[[ "$output" == *"demo not an input, skipping"* ]] || fail "no-input skip missing"
[[ "$output" == *"demo is a follows, not directly pinned, skipping"* ]] ||
  fail "follows skip missing"
[[ "$output" == *"protected branch (master)"* ]] || fail "protected skip missing"
[[ "$(grep -c '^nix' "$MOCK_LOG" || true)" == 0 ]] || fail "skip-only run invoked nix"

echo "flake-update-cascade preflight tests passed"
