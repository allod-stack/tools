#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

# --- API failures report the status and the server's own message ---
# api() previously ran curl -sf, which discarded the response body and collapsed
# every status >= 400 into a bare exit 22 with no output at all, leaving a 403
# indistinguishable from a 404 or a rejected payload.

reset_requests
run_fail "HTTP 403" "reports the HTTP status on an API error" \
  issue view 403
run_fail "user should have a permission to write to a repo" \
  "reports the server's own error message" \
  issue view 403
run_fail "GET /repos/acme/widget/issues/403" \
  "names the method and path that failed" \
  issue view 403

# --- Successful calls are unaffected by the status-splitting ---

reset_requests
output=$(run_capture -R acme/widget issue view 20)
assert_contains "$output" "Fix backup" "a successful read still returns its body"
assert_contains "$output" "Issue note" "requests after the first still run"

finish_tests "Forge API error"
