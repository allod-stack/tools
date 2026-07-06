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
run_ok issue create -t "Organized issue" -l bug -m "July batch"
assert_equal "$(request_count)" "3" "resolves organization fields before issue creation"
assert_request 1 GET "/api/v1/repos/acme/widget/labels?limit=100" \
  "resolves issue create labels"
assert_request 2 GET "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100" \
  "resolves issue create milestone"
assert_request 3 POST "/api/v1/repos/acme/widget/issues" "creates an organized issue"
assert_json 3 '. == {title: "Organized issue", body: "", labels: [1], milestone: 3}' \
  "sends labels and milestone on issue creation"

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
run_ok issue edit 20 --milestone "July batch"
assert_request 1 GET "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100" \
  "resolves issue edit milestone"
assert_request 2 PATCH "/api/v1/repos/acme/widget/issues/20" \
  "updates an issue milestone"
assert_json 2 '. == {milestone: 3}' "sends milestone ID on issue edit"

reset_requests
run_ok issue edit 20 --clear-milestone
assert_request 1 PATCH "/api/v1/repos/acme/widget/issues/20" \
  "clears an issue milestone"
assert_json 1 '. == {milestone: 0}' "sends zero milestone when clearing"

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

reset_requests
run_ok issue labels 20 --add triage
assert_request 1 POST "/api/v1/repos/acme/widget/issues/20/labels" \
  "adds an issue label"
assert_json 1 '. == {labels: ["triage"]}' "sends label names for issue label add"

reset_requests
run_ok issue labels 20 --add 1
assert_request 1 POST "/api/v1/repos/acme/widget/issues/20/labels" \
  "adds an issue label by ID"
assert_json 1 '. == {labels: [1]}' "sends numeric IDs for issue label add"

reset_requests
run_ok issue labels 20 --set triage
assert_request 1 PUT "/api/v1/repos/acme/widget/issues/20/labels" \
  "replaces issue labels"
assert_json 1 '. == {labels: ["triage"]}' "sends label names for issue label replace"

reset_requests
run_ok issue labels 20 --remove bug
assert_request 1 DELETE "/api/v1/repos/acme/widget/issues/20/labels/bug" \
  "removes an issue label"
assert_request 2 GET "/api/v1/repos/acme/widget/issues/20/labels" \
  "fetches labels after removal"

reset_requests
run_ok issue labels 20 --clear
assert_request 1 DELETE "/api/v1/repos/acme/widget/issues/20/labels" \
  "clears issue labels"

reset_requests
run_ok issue milestone 20 "July batch"
assert_request 1 GET "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100" \
  "resolves issue milestone title"
assert_request 2 PATCH "/api/v1/repos/acme/widget/issues/20" \
  "sets issue milestone"
assert_json 2 '. == {milestone: 3}' "sends milestone ID for issue milestone"

reset_requests
run_ok issue milestone 20 --clear
assert_request 1 PATCH "/api/v1/repos/acme/widget/issues/20" \
  "clears issue milestone through helper command"
assert_json 1 '. == {milestone: 0}' "sends zero milestone through helper command"

reset_requests
run_ok label create -n "area/nix" -c 123456 -d "Nix area"
assert_request 1 POST "/api/v1/repos/acme/widget/labels" \
  "creates a label"
assert_json 1 '. == {name: "area/nix", color: "#123456", description: "Nix area"}' \
  "sends label creation fields"

reset_requests
run_ok label edit bug -n defect -c 0000ff --exclusive
assert_request 1 GET "/api/v1/repos/acme/widget/labels?limit=100" \
  "resolves label name before edit"
assert_request 2 PATCH "/api/v1/repos/acme/widget/labels/1" \
  "updates a label"
assert_json 2 '. == {name: "defect", color: "#0000ff", exclusive: true}' \
  "sends label edit fields"

reset_requests
run_ok label delete bug
assert_request 1 GET "/api/v1/repos/acme/widget/labels?limit=100" \
  "resolves label name before delete"
assert_request 2 DELETE "/api/v1/repos/acme/widget/labels/1" \
  "deletes a label"

reset_requests
run_ok milestone create -t "August batch" -d "August work" --due 2026-08-31
assert_request 1 POST "/api/v1/repos/acme/widget/milestones" \
  "creates a milestone"
assert_json 1 '. == {title: "August batch", description: "August work", due_on: "2026-08-31T00:00:00Z"}' \
  "sends milestone creation fields"

reset_requests
run_ok milestone edit "July batch" -s closed
assert_request 1 GET "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100" \
  "resolves milestone title before edit"
assert_request 2 PATCH "/api/v1/repos/acme/widget/milestones/3" \
  "updates a milestone"
assert_json 2 '. == {state: "closed"}' "sends milestone state edit"

reset_requests
run_ok milestone delete "July batch"
assert_request 1 GET "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100" \
  "resolves milestone title before delete"
assert_request 2 DELETE "/api/v1/repos/acme/widget/milestones/3" \
  "deletes a milestone"

finish_tests "Forge mutation"
