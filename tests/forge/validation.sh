#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

printf 'body\n' > "$TMP/body.md"
reset_requests
run_fail "cannot be combined" "rejects conflicting body options" \
  pr create -t title -b one -F "$TMP/body.md"
run_fail "specified more than once" "rejects a duplicate title option" \
  pr create -t one --title two
run_fail "requires a value" "rejects an option without a value" pr create --title
run_fail "requires a value" "rejects a command-level repo flag without a value" \
  issue list --repo
run_fail "--head cannot be empty" "rejects an empty head branch" \
  pr create --title title --head ""
run_fail "--base cannot be empty" "rejects an empty base branch" \
  pr create --title title --base ""
run_fail "requires --title" "requires a title when creating an issue" \
  issue create --body body
run_fail "issue edit requires" \
  "requires a change when editing an issue" issue edit 20
run_fail "cannot be combined" "rejects conflicting issue milestone edit flags" \
  issue edit 20 --milestone "July batch" --clear-milestone
run_fail "requires --body or --body-file" "requires a pull request comment body" \
  pr comment 12
run_fail "requires --body or --body-file" "requires an issue comment body" \
  issue comment 20
run_fail "--clear cannot be combined" "rejects clear plus issue label changes" \
  issue labels 20 --clear --add bug
run_fail "--set cannot be combined" "rejects set plus additive label changes" \
  issue labels 20 --set bug --remove triage
run_fail "cannot be combined" "rejects clear plus issue milestone value" \
  issue milestone 20 "July batch" --clear
run_fail "label create requires --color" "requires color when creating a label" \
  label create --name bug
run_fail "color must be a 6-digit hex value" "rejects invalid label color" \
  label create --name bug --color zzzzzz
run_fail "label edit requires a change" "requires a label edit change" \
  label edit bug
run_fail "--state must be one of" "rejects invalid milestone list state" \
  milestone list --state invalid
run_fail "milestone create requires --title" "requires title when creating a milestone" \
  milestone create --due 2026-08-31
run_fail "milestone edit requires a change" "requires a milestone edit change" \
  milestone edit "July batch"
run_fail "project commands are unavailable" "reports unavailable project API" \
  project list
run_fail "unexpected argument" "rejects legacy positional PR creation" \
  pr create "legacy title" topic
run_fail "unknown option" "rejects an unknown PR edit option" \
  pr edit 12 --unknown value
run_fail "usage: forge issue close" "requires an issue close target" issue close
run_fail "--reason must be one of" "rejects an invalid close reason" \
  issue close 20 --reason invalid
run_fail "cannot be combined" "rejects conflicting duplicate close options" \
  issue close 20 --reason completed --duplicate-of 12
run_fail "issue target must be a number or URL" "rejects a foreign issue URL" \
  issue close https://other.example/acme/widget/issues/20
run_fail "duplicate target must be a number or URL" \
  "rejects an invalid duplicate target" issue close 20 --duplicate-of nope
run_fail "usage: forge pr close" "requires a PR close target" pr close
run_fail "unknown option" "rejects an unknown PR close option" \
  pr close 12 --unknown
run_fail "PR target must be a number, URL" "rejects a foreign PR URL" \
  pr close https://other.example/acme/widget/pulls/12
run_fail "PR target must be a number, URL" "rejects a malformed PR URL" \
  pr close https://forge.example/acme/widget/issues/12
run_fail "--comment cannot be empty" "rejects an empty PR closing comment" \
  pr close 12 -c ""
run_fail "specified more than once" "rejects duplicate pr close comment flag" \
  pr close 12 -c "first" -c "second"
assert_equal "$(request_count)" "0" "makes no API requests for invalid commands"

finish_tests "Forge validation"
