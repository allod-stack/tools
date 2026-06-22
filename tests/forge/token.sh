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
assert_contains "$output" "stdin is empty" "empty stdin gives clear error"

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

# --- token verify: works without configured token ---

reset_requests
unset FORGEJO_TOKEN
export FORGE_TOKEN_FILE=/nonexistent
output=$(printf 'valid-token' | run_capture token verify)
assert_contains "$output" "authenticated as testuser" "works without configured token"
export FORGEJO_TOKEN=test-token
unset FORGE_TOKEN_FILE

# --- token verify: rejects newline in token ---

reset_requests
output=$(printf 'valid\ninjection' | run_capture token verify 2>&1) || true
assert_contains "$output" "invalid characters" "rejects token with embedded newline"

# --- token verify: rejects double quote in token ---

reset_requests
output=$(printf 'valid"injection' | run_capture token verify 2>&1) || true
assert_contains "$output" "invalid characters" "rejects token with embedded quote"

# --- token verify: rejects backslash in token ---

reset_requests
output=$(printf 'valid\\injection' | run_capture token verify 2>&1) || true
assert_contains "$output" "invalid characters" "rejects token with embedded backslash"

# --- token verify: strips carriage return ---

reset_requests
output=$(printf 'valid-token\r' | run_capture token verify)
assert_contains "$output" "authenticated as testuser" "strips trailing carriage return"

# --- token verify: TTY detection ---

reset_requests
output=$(script -qc "$ROOT/forge token verify" /dev/null 2>&1) || true
assert_contains "$output" "reads from stdin" "rejects bare invocation on TTY"

finish_tests "token"
