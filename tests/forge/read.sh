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
assert_equal "$output" "12" "finds an open pull request by its head branch"
output=$(run_capture -R acme/widget pr find-by-head feature)
assert_equal "$output" "31" "matches the head ref, not merely the first open PR"
output=$(run_capture -R acme/widget pr find-by-head no-such-branch)
assert_equal "$output" "" "returns nothing when no open PR has that head"

reset_requests
output=$(run_capture issue list)
assert_contains "$output" "Fix backup" "lists an open issue"
assert_contains "$output" "bob" "shows the issue author"
assert_contains "$output" "bug" "shows issue labels"
assert_contains "$output" "July batch" "shows issue milestones"
assert_request 1 GET "/api/v1/repos/acme/widget/issues?type=issues&state=open&limit=30" \
  "infers the repository for issue listing"

reset_requests
output=$(run_capture issue list --state closed --label bug --milestone "July batch" --limit 5 --search backup)
assert_contains "$output" "Closed backup" "filters issues with gh-style list flags"
assert_request 1 GET "/api/v1/repos/acme/widget/issues?type=issues&state=closed&limit=5&labels=bug&milestones=July%20batch&q=backup" \
  "passes issue list filters to the API"

reset_requests
output=$(run_capture issue list --repo acme/gadget)
assert_contains "$output" "Gadget issue" "lists issues with command-level repo"
assert_request 1 GET "/api/v1/repos/acme/gadget/issues?type=issues&state=open&limit=30" \
  "uses command-level repo for issue listing"

reset_requests
output=$(run_capture -R acme/widget issue view 20)
assert_contains "$output" "Issue #20: Fix backup" "shows the issue header"
assert_contains "$output" "Issue body" "shows the issue body"
assert_contains "$output" "Issue note" "shows issue comments"
assert_contains "$output" "Labels:    bug" "shows issue view labels"
assert_contains "$output" "Milestone: July batch" "shows issue view milestone"

reset_requests
output=$(run_capture -R acme/widget label list)
assert_contains "$output" "bug" "lists repository labels"
assert_contains "$output" "Problem" "shows label descriptions"
assert_request 1 GET "/api/v1/repos/acme/widget/labels?limit=30" \
  "requests repository labels"

reset_requests
output=$(run_capture -R acme/widget label list --search tri --sort name --order desc --limit 30)
assert_contains "$output" "triage" "filters labels with gh-style list flags"
assert_request 1 GET "/api/v1/repos/acme/widget/labels?limit=30" \
  "passes label list limit"

reset_requests
output=$(run_capture -R acme/widget milestone list)
assert_contains "$output" "July batch" "lists repository milestones"
assert_contains "$output" "2026-07-31" "shows milestone due date"
assert_request 1 GET "/api/v1/repos/acme/widget/milestones?state=open&limit=100" \
  "requests open milestones"

reset_requests
output=$(run_capture -R acme/widget milestone view "July batch")
assert_contains "$output" "Milestone #3: July batch" "shows milestone details by title"
assert_contains "$output" "July work" "shows milestone description"
assert_request 1 GET "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100" \
  "resolves milestone title"
assert_request 2 GET "/api/v1/repos/acme/widget/milestones/3" \
  "requests milestone details"

reset_requests
output=$(run_capture -R acme/widget issue labels 20)
assert_contains "$output" "Issue #20 labels: bug" "lists issue labels"
assert_request 1 GET "/api/v1/repos/acme/widget/issues/20/labels" \
  "requests issue labels"

reset_requests
output=$(run_capture -R acme/widget issue milestone 20)
assert_contains "$output" "Issue #20 milestone: July batch" "shows issue milestone"
assert_request 1 GET "/api/v1/repos/acme/widget/issues/20" \
  "requests issue for milestone view"

finish_tests "Forge read"
