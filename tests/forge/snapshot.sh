#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

assert_output_json() {
  local output="$1" expression="$2" description="$3"
  if jq -e "$expression" <<< "$output" >/dev/null; then
    pass "$description"
  else
    fail "$description" "JSON assertion: $expression" "actual output:" "$output"
  fi
}

reset_requests
output=$(run_capture -R acme/widget pr snapshot 12)
assert_output_json "$output" '
  keys == ["base", "head", "pull_request", "schema_version"]
  and .schema_version == 1
  and .pull_request == {
    number: 12,
    url: "https://forge.example/acme/widget/pulls/12",
    title: "Improve tool",
    body: "PR body"
  }
  and .base == {
    repository: {
      owner: "acme",
      name: "widget",
      full_name: "acme/widget",
      clone_url: "https://forge.example/acme/widget.git"
    },
    ref: "master",
    sha: "1111111111111111111111111111111111111111"
  }
  and .head == {
    repository: {
      owner: "acme",
      name: "widget",
      full_name: "acme/widget",
      clone_url: "https://forge.example/acme/widget.git"
    },
    ref: "topic",
    sha: "2222222222222222222222222222222222222222"
  }
' "projects the stable same-repository snapshot schema"
assert_request 1 GET "/api/v1/repos/acme/widget/pulls/12" \
  "requests only the pull request snapshot resource"
assert_equal "$(request_count)" "1" "fetches one resource for a snapshot"

reset_requests
output=$(run_capture -R acme/widget pr snapshot 41)
assert_output_json "$output" '
  .pull_request.number == 41
  and .pull_request.body == ""
  and .base.repository.full_name == "acme/widget"
  and .base.repository.clone_url == "https://forge.example/acme/widget.git"
  and .head.repository == {
    owner: "contributor",
    name: "widget-fork",
    full_name: "contributor/widget-fork",
    clone_url: "https://forge.example/contributor/widget-fork.git"
  }
  and .head.ref == "topic"
  and .head.sha == "4444444444444444444444444444444444444444"
' "keeps independent repository metadata for a fork head"

reset_requests
run_fail "missing or has malformed snapshot fields" \
  "rejects a malformed object ID" pr snapshot 42
assert_equal "$(request_count)" "1" "fetches once before rejecting malformed snapshot data"

reset_requests
run_fail "missing or has malformed snapshot fields" \
  "rejects an API response for a different pull request number" pr snapshot 43

reset_requests
run_fail "missing or has malformed snapshot fields" \
  "rejects a missing pull request ref" pr snapshot 44

reset_requests
run_fail "missing or has malformed snapshot fields" \
  "rejects missing repository clone metadata" pr snapshot 45

finish_tests "Forge PR snapshot"
