#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_home validation
run_fail "missing required argument" "rejects a missing input name"
run_fail "unknown option: --bad" "rejects an unknown option" demo --bad
run_fail "invalid input name" "rejects an invalid input name" "bad/input"

finish_tests "flake-update-cascade validation"
