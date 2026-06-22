#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

# --- auth status: valid configured token (env var) ---

reset_requests
export FORGEJO_TOKEN=valid-token
output=$(run_capture auth status)
assert_contains "$output" "Authenticated as testuser" "reports authenticated user"
assert_contains "$output" "FORGEJO_TOKEN" "shows env var as source"
assert_request 1 GET "/api/v1/user" "requests user endpoint"

# --- auth status: invalid configured token ---

reset_requests
export FORGEJO_TOKEN=bad-token
output=$(run_capture auth status 2>&1) || true
assert_contains "$output" "Authentication failed" "reports failure for bad token"
assert_contains "$output" "unauthorized" "shows reason"

# --- auth status: valid token from file ---

reset_requests
unset FORGEJO_TOKEN
token_file="$TMP/test-token-file"
printf '%s' "valid-token" > "$token_file"
export FORGE_TOKEN_FILE="$token_file"
output=$(run_capture auth status)
assert_contains "$output" "Authenticated as testuser" "token file auth succeeds"
assert_contains "$output" "$token_file" "shows token file path as source"
export FORGEJO_TOKEN=test-token
unset FORGE_TOKEN_FILE

# --- auth status: no credential source ---

reset_requests
unset FORGEJO_TOKEN
export FORGE_TOKEN_FILE=/nonexistent
output=$(run_capture auth status 2>&1) || true
assert_contains "$output" "no token found" "errors when no credential source exists"
export FORGEJO_TOKEN=test-token
unset FORGE_TOKEN_FILE

# --- auth status: rejects --token ---

reset_requests
output=$(run_capture auth status --token foo 2>&1) || true
assert_contains "$output" "does not accept" "rejects --token flag"

# --- auth status: rejects --token-file ---

reset_requests
output=$(run_capture auth status --token-file /dev/null 2>&1) || true
assert_contains "$output" "does not accept" "rejects --token-file flag"

# --- auth status: rejects --show-token ---

reset_requests
output=$(run_capture auth status --show-token 2>&1) || true
assert_contains "$output" "does not accept" "rejects --show-token flag"

# --- negative: no auth token command ---

reset_requests
output=$(run_capture auth token 2>&1) || true
assert_contains "$output" "not a command" "auth token is not a command"

finish_tests "auth"
