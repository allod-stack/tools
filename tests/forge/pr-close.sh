#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

reset_requests
run_ok pr close 12
assert_equal "$(request_count)" "1" "plain close makes one API request"
assert_request 1 PATCH "/api/v1/repos/acme/widget/pulls/12" \
  "plain close updates the target PR"
assert_json 1 '. == {state: "closed"}' "plain close sends the closed state"

reset_requests
run_ok pr close 12 -c "Superseded"
assert_equal "$(request_count)" "2" "close with a comment makes two API requests"
assert_request 1 POST "/api/v1/repos/acme/widget/issues/12/comments" \
  "posts the closing comment first"
assert_json 1 '. == {body: "Superseded"}' "sends the requested closing comment"
assert_request 2 PATCH "/api/v1/repos/acme/widget/pulls/12" \
  "closes the PR after commenting"
assert_json 2 '. == {state: "closed"}' "sends the closed state after commenting"

reset_requests
run_ok pr close https://forge.example/acme/widget/pulls/12
assert_equal "$(request_count)" "1" "URL close makes one API request"
assert_request 1 PATCH "/api/v1/repos/acme/widget/pulls/12" \
  "accepts a PR URL as the close target"
assert_json 1 '. == {state: "closed"}' "sends the closed state for URL target"

reset_requests
run_ok pr close topic
assert_equal "$(request_count)" "2" "branch close looks up the PR first"
assert_request 1 GET "/api/v1/repos/acme/widget/pulls?state=open&limit=50&head=topic" \
  "finds the PR by head branch"
assert_request 2 PATCH "/api/v1/repos/acme/widget/pulls/31" \
  "closes the PR found by branch lookup"
assert_json 2 '. == {state: "closed"}' "sends the closed state for branch target"

reset_requests
output=$(run_capture pr close -R acme/gadget 5) ||
  fail "runs pr close with repo override" "command output:" "$output"
assert_request 1 PATCH "/api/v1/repos/acme/gadget/pulls/5" \
  "uses command-level repo override for pr close"

reset_requests
run_ok pr close 12 -d
assert_equal "$(request_count)" "3" "close with delete-branch makes three API requests"
assert_request 1 GET "/api/v1/repos/acme/widget/pulls/12" \
  "fetches PR details to find the head branch"
assert_request 2 PATCH "/api/v1/repos/acme/widget/pulls/12" \
  "closes the PR before deleting the branch"
assert_json 2 '. == {state: "closed"}' "sends the closed state before branch deletion"
assert_request 3 DELETE "/api/v1/repos/acme/widget/branches/topic" \
  "deletes the remote head branch"

reset_requests
run_ok pr close 12 --comment "Done" --delete-branch
assert_equal "$(request_count)" "4" "close with comment and delete-branch makes four API requests"
assert_request 1 POST "/api/v1/repos/acme/widget/issues/12/comments" \
  "posts closing comment before fetching PR details"
assert_request 2 GET "/api/v1/repos/acme/widget/pulls/12" \
  "fetches PR details for branch deletion"
assert_request 3 PATCH "/api/v1/repos/acme/widget/pulls/12" \
  "closes the PR"
assert_request 4 DELETE "/api/v1/repos/acme/widget/branches/topic" \
  "deletes the branch after closing"

reset_requests
run_ok pr close topic -d
assert_equal "$(request_count)" "4" "branch target with delete-branch makes four API requests"
assert_request 1 GET "/api/v1/repos/acme/widget/pulls?state=open&limit=50&head=topic" \
  "resolves branch name before fetching PR details"
assert_request 2 GET "/api/v1/repos/acme/widget/pulls/31" \
  "fetches PR details for branch-resolved target"
assert_request 3 PATCH "/api/v1/repos/acme/widget/pulls/31" \
  "closes the branch-resolved PR"
assert_request 4 DELETE "/api/v1/repos/acme/widget/branches/topic" \
  "deletes the branch for branch-resolved target"

reset_requests
run_fail "no open PR found for branch" \
  "rejects a branch with no matching PR" pr close nonexistent

reset_requests
run_fail "no open PR found" \
  "URL-encodes slashes in branch lookup" pr close "feat/sub"
assert_request 1 GET "/api/v1/repos/acme/widget/pulls?state=open&limit=50&head=feat%2Fsub" \
  "encodes slash as %2F in query parameter"

reset_requests
run_ok pr close 99 -d
assert_equal "$(request_count)" "2" "skips branch deletion when head equals base"
assert_request 1 GET "/api/v1/repos/acme/widget/pulls/99" \
  "fetches PR to check head vs base"
assert_request 2 PATCH "/api/v1/repos/acme/widget/pulls/99" \
  "still closes the PR when branch deletion is skipped"

finish_tests "Forge pr-close"
