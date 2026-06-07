#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_home dry-run
write_direct_lock "$HOME/work/app"
before=$(sha256sum "$HOME/work/app/flake.lock")
export MOCK_SCENARIO=dry-run
output=$(bash "$ROOT/flake-update-cascade" demo --dry-run)
[[ "$output" == *"aaaaaaa → bbbbbbb  (dry-run: no changes made)"* ]] ||
  fail "dry-run revision output missing"
after=$(sha256sum "$HOME/work/app/flake.lock")
[[ "$before" == "$after" ]] || fail "dry-run changed flake.lock"
grep -Fq $'nix\tflake update demo --flake '"$HOME/work/app"' --output-lock-file /tmp/app.flake.lock.new' \
  "$MOCK_LOG"

echo "flake-update-cascade dry-run tests passed"
