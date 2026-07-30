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

# --- The exit code itself is part of the contract ---
# run_fail asserts only a non-zero exit, so it stays green if 22 degrades to 1.
# docs/forge.md promises 22 for an HTTP error and curl's own code for a transport
# failure; assert both directly or neither is actually covered.

reset_requests
rc=0
"$ROOT/forge" -R acme/widget issue view 403 >/dev/null 2>&1 || rc=$?
assert_equal "$rc" "22" "an HTTP error exits 22"

reset_requests
rc=0
"$ROOT/forge" -R acme/widget issue view 599 >/dev/null 2>&1 || rc=$?
assert_equal "$rc" "6" "a transport failure returns curl's own exit code"
run_fail "curl exit 6" "names curl's exit code when the request never completed" \
  issue view 599

# --- A redirect is a failure, not an empty success ---
# No -L is passed, so a 3xx body is never the requested resource. Treating one as
# success is how an http:// base URL silently produces an empty answer.

reset_requests
run_fail "HTTP 308" "reports a redirect instead of returning an empty body" \
  issue view 308

# --- Successful calls are unaffected by the status-splitting ---

reset_requests
output=$(run_capture -R acme/widget issue view 20)
assert_contains "$output" "Fix backup" "a successful read still returns its body"
assert_contains "$output" "Issue note" "requests after the first still run"

finish_tests "Forge API error"
