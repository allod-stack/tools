#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

# --- token verify: stdin valid token ---

reset_requests
export FORGEJO_TOKEN=test-token
output=$(printf 'valid-token' | run_capture token verify)
assert_contains "$output" "authenticated as testuser" "stdin valid token reports user"
assert_contains "$output" "Token valid" "stdin valid token shows success"
assert_request 1 GET "/api/v1/user" "requests user endpoint"

# --- token verify: stdin invalid token ---

reset_requests
output=$(printf 'bad-token' | run_capture token verify 2>&1) || true
assert_contains "$output" "Token invalid" "stdin invalid token reports failure"
assert_contains "$output" "unauthorized" "stdin invalid token shows reason"

# --- token verify: no stdin ---

reset_requests
output=$(printf '' | run_capture token verify 2>&1) || true
assert_contains "$output" "no token on stdin" "empty stdin gives clear error"

# --- token verify: rejects --token ---

reset_requests
output=$(run_capture token verify --token foo 2>&1) || true
assert_contains "$output" "not supported" "rejects --token flag"

# --- token verify: rejects --token-file ---

reset_requests
output=$(run_capture token verify --token-file /dev/null 2>&1) || true
assert_contains "$output" "not supported" "rejects --token-file flag"

# --- token verify: strips trailing newline from stdin ---

reset_requests
output=$(printf 'valid-token\n' | run_capture token verify)
assert_contains "$output" "authenticated as testuser" "handles trailing newline"

finish_tests "token"
