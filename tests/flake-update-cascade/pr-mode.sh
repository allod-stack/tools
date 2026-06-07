#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_home pr
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=pr
output=$(bash "$ROOT/flake-update-cascade" demo --pr)
[[ "$output" == *"aaaaaaa → bbbbbbb"* ]] || fail "PR update revision output missing"
[[ "$output" == *"PR #42 updated"* ]] || fail "existing PR output missing"
grep -Fq $'git\tapp\tcheckout -B agent/flake-update-demo' "$MOCK_LOG"
grep -Fq $'git\tapp\tcommit -m flake.lock: update demo' "$MOCK_LOG"
grep -Fq $'git\tapp\tpush --force-with-lease origin agent/flake-update-demo' "$MOCK_LOG"
grep -Fq $'forge\t-R acme/app pr find-by-head agent/flake-update-demo' "$MOCK_LOG"
grep -Fq $'git\tapp\tcheckout master' "$MOCK_LOG"

echo "flake-update-cascade PR-mode tests passed"
