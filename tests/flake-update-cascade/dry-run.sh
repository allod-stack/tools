#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_home dry-run
write_direct_lock "$HOME/work/app"
before=$(sha256sum "$HOME/work/app/flake.lock")
export MOCK_SCENARIO=dry-run
output=$(bash "$ROOT/flake-update-cascade" demo --dry-run)
assert_contains "$output" "aaaaaaa → bbbbbbb  (dry-run: no changes made)" \
  "reports the proposed revision change"
after=$(sha256sum "$HOME/work/app/flake.lock")
assert_equal "$after" "$before" "leaves the repository lock file unchanged"
assert_log_contains \
  $'nix\tflake update demo --flake '"$HOME/work/app"' --output-lock-file /tmp/app.flake.lock.new' \
  "writes the proposed update to a temporary lock file"

finish_tests "flake-update-cascade dry-run"
