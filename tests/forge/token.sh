#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

# --- token verify: default token (valid) ---

reset_requests
export FORGEJO_TOKEN=valid-token
output=$(run_capture token verify)
assert_contains "$output" "authenticated as testuser" "reports authenticated user"
assert_contains "$output" "configured token" "shows default source"
assert_request 1 GET "/api/v1/user" "requests user endpoint"

# --- token verify: default token (invalid) ---

reset_requests
export FORGEJO_TOKEN=bad-token
output=$(run_capture token verify 2>&1) || true
assert_contains "$output" "Token invalid" "reports invalid token"
assert_contains "$output" "unauthorized" "shows reason"

# --- token verify: --token flag (valid) ---

reset_requests
export FORGEJO_TOKEN=bad-token
output=$(run_capture token verify --token valid-token)
assert_contains "$output" "authenticated as testuser" "--token overrides default"
assert_contains "$output" "provided token" "--token shows source as provided"

# --- token verify: --token flag (invalid) ---

reset_requests
export FORGEJO_TOKEN=valid-token
output=$(run_capture token verify --token bad-token 2>&1) || true
assert_contains "$output" "Token invalid" "--token with bad value fails"

# --- token verify: --token-file flag ---

reset_requests
token_file="$TMP/test-token"
printf '%s' "valid-token" > "$token_file"
output=$(run_capture token verify --token-file "$token_file")
assert_contains "$output" "authenticated as testuser" "--token-file verifies token from file"
assert_contains "$output" "$token_file" "--token-file shows file path as source"

# --- token verify: --token-file with invalid token ---

reset_requests
printf '%s' "bad-token" > "$token_file"
output=$(run_capture token verify --token-file "$token_file" 2>&1) || true
assert_contains "$output" "Token invalid" "--token-file with bad token fails"

# --- token verify: --token-file with missing file ---

reset_requests
output=$(run_capture token verify --token-file /nonexistent 2>&1) || true
assert_contains "$output" "cannot read token file" "missing file gives clear error"

# --- token verify: mutual exclusion ---

reset_requests
output=$(run_capture token verify --token foo --token-file /dev/null 2>&1) || true
assert_contains "$output" "cannot be combined" "rejects --token with --token-file"

reset_requests
printf '%s' "valid-token" > "$token_file"
output=$(run_capture token verify --token-file "$token_file" --token foo 2>&1) || true
assert_contains "$output" "cannot be combined" "rejects --token-file with --token"

finish_tests "token"
