#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

reset_requests
output=$(run_capture -R acme/widget pr list)
assert_contains "$output" "Improve tool" "lists an open pull request"
assert_contains "$output" "acme:topic → master" "shows pull request branches"
assert_request 1 GET "/api/v1/repos/acme/widget/pulls?state=open&limit=50" \
  "requests open pull requests"
assert_auth 1 "test-token" "transmits auth header on normal API call"

reset_requests
output=$(run_capture -R acme/widget pr view 12)
assert_contains "$output" "PR #12: Improve tool" "shows the pull request header"
assert_contains "$output" "General note" "shows general pull request comments"
assert_contains "$output" "Inline note" "shows inline review comments"
assert_equal "$(request_count)" "4" "fetches all pull request view resources"

reset_requests
output=$(run_capture -R acme/widget pr review-comments 12)
assert_contains "$output" "id 99" "shows the inline comment identifier"
assert_contains "$output" "carol on forge line 4" "shows inline comment location"
assert_contains "$output" "Inline note" "shows inline comment body"

reset_requests
output=$(run_capture -R acme/widget pr find-by-head topic)
assert_equal "$output" "31" "finds an open pull request by head branch"

reset_requests
output=$(run_capture issue list)
assert_contains "$output" "Fix backup" "lists an open issue"
assert_contains "$output" "bob" "shows the issue author"
assert_request 1 GET "/api/v1/repos/acme/widget/issues?type=issues&state=open&limit=50" \
  "infers the repository for issue listing"

reset_requests
output=$(run_capture issue list --repo acme/gadget)
assert_contains "$output" "Gadget issue" "lists issues with command-level repo"
assert_request 1 GET "/api/v1/repos/acme/gadget/issues?type=issues&state=open&limit=50" \
  "uses command-level repo for issue listing"

reset_requests
output=$(run_capture -R acme/widget issue view 20)
assert_contains "$output" "Issue #20: Fix backup" "shows the issue header"
assert_contains "$output" "Issue body" "shows the issue body"
assert_contains "$output" "Issue note" "shows issue comments"

finish_tests "Forge read"
