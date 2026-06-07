#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

reset_requests
run_ok issue close 20
assert_equal "$(request_count)" "1" "plain close makes one API request"
assert_request 1 PATCH "/api/v1/repos/acme/widget/issues/20" \
  "plain close updates the target issue"
assert_json 1 '. == {state: "closed"}' "plain close sends the closed state"

reset_requests
run_ok issue close 20 -c "Implemented"
assert_equal "$(request_count)" "2" "close with a comment makes two API requests"
assert_request 1 POST "/api/v1/repos/acme/widget/issues/20/comments" \
  "posts the closing comment first"
assert_json 1 '. == {body: "Implemented"}' "sends the requested closing comment"
assert_request 2 PATCH "/api/v1/repos/acme/widget/issues/20" \
  "closes the issue after commenting"
assert_json 2 '. == {state: "closed"}' "sends the closed state after commenting"

reset_requests
run_ok issue close https://forge.example/acme/widget/issues/20 \
  --comment "Already tracked" --duplicate-of 12
assert_equal "$(request_count)" "2" "duplicate close makes two API requests"
assert_request 1 POST "/api/v1/repos/acme/widget/issues/20/comments" \
  "accepts an issue URL as the close target"
assert_json 1 '. == {body: "Already tracked\n\nDuplicate of #12."}' \
  "records the duplicate target in the closing comment"
assert_request 2 PATCH "/api/v1/repos/acme/widget/issues/20" \
  "closes the duplicate issue"
assert_json 2 '. == {state: "closed"}' "sends the closed state for a duplicate"

reset_requests
run_ok issue close 20 --reason "not planned"
assert_request 1 POST "/api/v1/repos/acme/widget/issues/20/comments" \
  "records a not-planned reason as a comment"
assert_json 1 '. == {body: "Closed as not planned."}' \
  "uses the expected not-planned comment"
assert_request 2 PATCH "/api/v1/repos/acme/widget/issues/20" \
  "closes an issue marked not planned"
assert_json 2 '. == {state: "closed"}' "sends the closed state for not planned"

finish_tests "Forge issue-close"
