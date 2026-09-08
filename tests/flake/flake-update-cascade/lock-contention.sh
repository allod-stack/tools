#!/usr/bin/env bash
# Two runs never update one repository at once: the second finds the
# per-repository lock held, reports it, touches nothing there, and ends with
# status 1 after processing the rest.
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

lock_file=/tmp/flake-update-cascade-app.lock
# One process holds the lock on its own descriptor, so killing it releases it.
( exec 8>"$lock_file"; flock 8; exec sleep 60 ) &
holder=$!
trap 'kill "$holder" 2>/dev/null || true; rm -rf "$TMP"' EXIT
for _ in $(seq 50); do
  flock -n "$lock_file" true 2>/dev/null || break
  sleep 0.1
done
flock -n "$lock_file" true 2>/dev/null && fail "could not arrange a held lock for the fixture"

new_home contention
write_direct_lock "$HOME/work/app"
write_direct_lock "$HOME/work/other"
export MOCK_SCENARIO=dry-run
output=$(run_cascade demo --dry-run 2>&1) && status=0 || status=$?
assert_equal "$status" "1" "a held repository lock fails the run"
assert_contains "$output" "another instance is running for app, skipping" \
  "reports the held lock"
assert_equal "$(grep -Ec $'^(git\tapp\tpull|nix\t.*/app( |$))' "$MOCK_LOG" || true)" "0" \
  "never pulls or updates the locked repository"
assert_log_contains $'nix\tflake update demo --flake '"$HOME/work/other" \
  "still updates the unlocked repository"

kill "$holder" 2>/dev/null
wait "$holder" 2>/dev/null || true
export MOCK_SCENARIO=dry-run
: > "$MOCK_LOG"
output=$(run_cascade demo --dry-run 2>&1)
assert_contains "$output" "dry-run: no changes made" "proceeds once the lock is released"

finish_tests "flake-update-cascade lock-contention"
