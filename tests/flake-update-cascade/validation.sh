#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_home validation
run_fail "missing required argument"
run_fail "unknown option: --bad" demo --bad
run_fail "invalid input name" "bad/input"

echo "flake-update-cascade validation tests passed"
