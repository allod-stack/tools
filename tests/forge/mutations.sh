#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

printf 'line one\n`code`\n\n' > "$TMP/pr-body.md"

reset_requests
run_ok pr create --title "Create PR" --body-file "$TMP/pr-body.md"
assert_equal "$(request_count)" "2" "infers default branch before PR creation"
assert_request 1 GET "/api/v1/repos/acme/widget" "requests repository metadata"
assert_request 2 POST "/api/v1/repos/acme/widget/pulls" "creates the pull request"
assert_json 2 '. == {
  title: "Create PR",
  head: "feature/current",
  base: "master",
  body: "line one\n`code`\n\n"
}' "sends inferred branches and multiline body"

reset_requests
run_ok pr create -t "Short flags" -H topic -B develop -b "body"
assert_equal "$(request_count)" "1" "avoids metadata lookup with explicit branches"
assert_request 1 POST "/api/v1/repos/acme/widget/pulls" \
  "creates a pull request with short flags"
assert_json 1 '. == {title: "Short flags", head: "topic", base: "develop", body: "body"}' \
  "sends explicit pull request fields"

reset_requests
run_capture pr create -R acme/gadget -t "Command repo" -H topic -B master -b "body" >/dev/null
assert_request 1 POST "/api/v1/repos/acme/gadget/pulls" \
  "creates a pull request with command-level repo"
assert_json 1 '. == {title: "Command repo", head: "topic", base: "master", body: "body"}' \
  "uses command-level repo for pull request creation"

reset_requests
run_ok pr edit 12 --body ""
assert_request 1 PATCH "/api/v1/repos/acme/widget/pulls/12" "updates a pull request"
assert_json 1 '. == {body: ""}' "preserves an explicitly empty body"

reset_requests
run_ok pr comment 12 -F "$TMP/pr-body.md"
assert_request 1 POST "/api/v1/repos/acme/widget/issues/12/comments" \
  "posts a pull request comment"
assert_json 1 '.body == "line one\n`code`\n\n"' \
  "preserves multiline comment content"

reset_requests
run_ok pr reply 12 99 --body "thread reply"
assert_equal "$(request_count)" "3" "looks up the original inline comment before replying"
assert_request 1 GET "/api/v1/repos/acme/widget/pulls/12/reviews" \
  "requests pull request reviews"
assert_request 2 GET "/api/v1/repos/acme/widget/pulls/12/reviews/7/comments" \
  "requests inline review comments"
assert_request 3 POST "/api/v1/repos/acme/widget/pulls/12/reviews/7/comments" \
  "posts the inline reply"
assert_json 3 '. == {path: "forge", new_position: 4, body: "thread reply"}' \
  "reuses the original comment position"

reset_requests
run_ok issue create -t "New issue"
assert_request 1 POST "/api/v1/repos/acme/widget/issues" "creates an issue"
assert_json 1 '. == {title: "New issue", body: ""}' \
  "sends an empty body when omitted"

reset_requests
run_capture issue create -R acme/gadget -t "Command repo issue" >/dev/null
assert_request 1 POST "/api/v1/repos/acme/gadget/issues" \
  "creates an issue with command-level repo"
assert_json 1 '. == {title: "Command repo issue", body: ""}' \
  "uses command-level repo for issue creation"

reset_requests
printf 'stdin body\n\n' | run_ok issue edit 20 --title "Updated" --body-file -
assert_request 1 PATCH "/api/v1/repos/acme/widget/issues/20" "updates an issue"
assert_json 1 '. == {title: "Updated", body: "stdin body\n\n"}' \
  "preserves issue body content read from stdin"

reset_requests
run_ok issue comment 20 -b "issue comment body"
assert_request 1 POST "/api/v1/repos/acme/widget/issues/20/comments" \
  "posts an issue comment"
assert_json 1 '.body == "issue comment body"' \
  "sends the issue comment body"

reset_requests
run_ok issue comment 20 -F "$TMP/pr-body.md"
assert_request 1 POST "/api/v1/repos/acme/widget/issues/20/comments" \
  "posts an issue comment from file"
assert_json 1 '.body == "line one\n`code`\n\n"' \
  "preserves multiline issue comment content from file"

finish_tests "Forge mutation"
