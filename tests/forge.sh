#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

MOCK_BIN="$TMP/bin"
REQUEST_DIR="$TMP/requests"
mkdir -p "$MOCK_BIN" "$REQUEST_DIR"

cat > "$MOCK_BIN/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "$*" in
  "branch --show-current")
    printf '%s\n' "${MOCK_GIT_BRANCH:-feature/current}"
    ;;
  "remote get-url origin")
    printf '%s\n' "ssh://git@forge.example:2222/acme/widget.git"
    ;;
  *)
    echo "unexpected mocked git invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$MOCK_BIN/git"

cat > "$MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

method=GET
url=""
data=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -X)
      method="$2"
      shift 2
      ;;
    -H)
      shift 2
      ;;
    -d)
      data="$2"
      shift 2
      ;;
    -sf|-fs|-s|-f)
      shift
      ;;
    http://*|https://*)
      url="$1"
      shift
      ;;
    *)
      echo "unexpected mocked curl argument: $1" >&2
      exit 1
      ;;
  esac
done

count_file="$MOCK_REQUEST_DIR/count"
count=0
[[ ! -f "$count_file" ]] || count=$(<"$count_file")
count=$((count + 1))
printf '%s' "$count" > "$count_file"
printf '%s' "$method" > "$MOCK_REQUEST_DIR/$count.method"
printf '%s' "$url" > "$MOCK_REQUEST_DIR/$count.url"
printf '%s' "$data" > "$MOCK_REQUEST_DIR/$count.data"

case "$url" in
  */api/v1/repos/acme/widget)
    printf '%s\n' '{"default_branch":"master"}'
    ;;
  */api/v1/repos/acme/widget/pulls\?state=open\&limit=50\&head=topic)
    printf '%s\n' '[{"number":31}]'
    ;;
  */api/v1/repos/acme/widget/pulls\?state=open\&limit=50)
    printf '%s\n' '[{"number":12,"title":"Improve tool","user":{"login":"alice"},"head":{"label":"acme:topic"},"base":{"label":"master"}}]'
    ;;
  */api/v1/repos/acme/widget/issues\?type=issues\&state=open\&limit=50)
    printf '%s\n' '[{"number":20,"title":"Fix backup","user":{"login":"bob"}}]'
    ;;
  */api/v1/repos/acme/widget/pulls/12/reviews)
    printf '%s\n' '[{"id":7,"comments_count":1}]'
    ;;
  */api/v1/repos/acme/widget/pulls/12/reviews/7/comments)
    if [[ "$method" == GET ]]; then
      printf '%s\n' '[{"id":99,"path":"forge","line":4,"position":4,"diff_hunk":"@@ -1 +1 @@","body":"Inline note","created_at":"2026-06-01T00:00:00Z","user":{"login":"carol"}}]'
    else
      printf '%s\n' '{"html_url":"https://forge.example/acme/widget/pulls/12#comment-100"}'
    fi
    ;;
  */api/v1/repos/acme/widget/pulls)
    printf '%s\n' '{"html_url":"https://forge.example/acme/widget/pulls/1"}'
    ;;
  */api/v1/repos/acme/widget/pulls/12)
    if [[ "$method" == GET ]]; then
      printf '%s\n' '{"title":"Improve tool","state":"open","body":"PR body","user":{"login":"alice"},"head":{"label":"acme:topic"},"base":{"label":"master"}}'
    else
      printf '%s\n' '{"html_url":"https://forge.example/acme/widget/pulls/12"}'
    fi
    ;;
  */api/v1/repos/acme/widget/issues/12/comments)
    if [[ "$method" == GET ]]; then
      printf '%s\n' '[{"body":"General note","created_at":"2026-06-02T00:00:00Z","user":{"login":"dave"}}]'
    else
      printf '%s\n' '{"html_url":"https://forge.example/acme/widget/issues/12#comment-1"}'
    fi
    ;;
  */api/v1/repos/acme/widget/issues/20/comments)
    printf '%s\n' '[{"body":"Issue note","created_at":"2026-06-03T00:00:00Z","user":{"login":"erin"}}]'
    ;;
  */api/v1/repos/acme/widget/issues)
    printf '%s\n' '{"html_url":"https://forge.example/acme/widget/issues/20"}'
    ;;
  */api/v1/repos/acme/widget/issues/20)
    if [[ "$method" == GET ]]; then
      printf '%s\n' '{"title":"Fix backup","state":"open","body":"Issue body","user":{"login":"bob"}}'
    else
      printf '%s\n' '{"html_url":"https://forge.example/acme/widget/issues/20"}'
    fi
    ;;
  *)
    echo "unexpected mocked URL: $url" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$MOCK_BIN/curl"

export PATH="$MOCK_BIN:$PATH"
export FORGEJO_TOKEN=test-token
export FORGE_URL=https://forge.example
export MOCK_REQUEST_DIR="$REQUEST_DIR"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

reset_requests() {
  rm -f "$REQUEST_DIR"/*
}

request_count() {
  if [[ -f "$REQUEST_DIR/count" ]]; then
    cat "$REQUEST_DIR/count"
  else
    printf '0'
  fi
}

assert_request() {
  local number="$1"
  local method="$2"
  local url_suffix="$3"
  [[ "$(<"$REQUEST_DIR/$number.method")" == "$method" ]] || {
    fail "request $number method was not $method"
  }
  [[ "$(<"$REQUEST_DIR/$number.url")" == *"$url_suffix" ]] || {
    fail "request $number URL did not end with $url_suffix"
  }
}

assert_json() {
  local number="$1"
  local expression="$2"
  jq -e "$expression" "$REQUEST_DIR/$number.data" >/dev/null || {
    fail "request $number JSON assertion failed: $expression"
  }
}

run_ok() {
  local output
  output=$("$ROOT/forge" -R acme/widget "$@" 2>&1) || {
    fail "command failed: forge -R acme/widget $*"$'\n'"$output"
  }
}

run_capture() {
  "$ROOT/forge" "$@" 2>&1
}

run_fail() {
  local expected="$1"
  shift
  local output
  if output=$("$ROOT/forge" -R acme/widget "$@" 2>&1); then
    fail "command unexpectedly succeeded: forge -R acme/widget $*"
  fi
  [[ "$output" == *"$expected"* ]] || {
    fail "failure did not contain '$expected': $output"
  }
}

# pr create: long flags, current head, repository default base, and body file.
reset_requests
printf 'line one\n`code`\n\n' > "$TMP/pr-body.md"
run_ok pr create --title "Create PR" --body-file "$TMP/pr-body.md"
[[ "$(request_count)" == 2 ]] || fail "pr create should make two requests"
assert_request 1 GET "/api/v1/repos/acme/widget"
assert_request 2 POST "/api/v1/repos/acme/widget/pulls"
assert_json 2 '. == {
  title: "Create PR",
  head: "feature/current",
  base: "master",
  body: "line one\n`code`\n\n"
}'

# pr create: gh short aliases and explicit head/base avoid the metadata request.
reset_requests
run_ok pr create -t "Short flags" -H topic -B develop -b "body"
[[ "$(request_count)" == 1 ]] || fail "explicit pr create should make one request"
assert_request 1 POST "/api/v1/repos/acme/widget/pulls"
assert_json 1 '. == {title: "Short flags", head: "topic", base: "develop", body: "body"}'

# pr edit: an explicitly empty body must be sent, without a title field.
reset_requests
run_ok pr edit 12 --body ""
assert_request 1 PATCH "/api/v1/repos/acme/widget/pulls/12"
assert_json 1 '. == {body: ""}'

# pr comment: body-file preserves multiline content.
reset_requests
run_ok pr comment 12 -F "$TMP/pr-body.md"
assert_request 1 POST "/api/v1/repos/acme/widget/issues/12/comments"
assert_json 1 '.body == "line one\n`code`\n\n"'

# pr reply: retain the review lookup flow and use structured body JSON.
reset_requests
run_ok pr reply 12 99 --body "thread reply"
[[ "$(request_count)" == 3 ]] || fail "pr reply should make three requests"
assert_request 1 GET "/api/v1/repos/acme/widget/pulls/12/reviews"
assert_request 2 GET "/api/v1/repos/acme/widget/pulls/12/reviews/7/comments"
assert_request 3 POST "/api/v1/repos/acme/widget/pulls/12/reviews/7/comments"
assert_json 3 '. == {path: "forge", new_position: 4, body: "thread reply"}'

# Read commands: list/view/review output and repository inference.
reset_requests
output=$(run_capture -R acme/widget pr list)
[[ "$output" == *"12"*"Improve tool"*"alice"*"acme:topic → master"* ]] || fail "pr list output incorrect"
assert_request 1 GET "/api/v1/repos/acme/widget/pulls?state=open&limit=50"

reset_requests
output=$(run_capture -R acme/widget pr view 12)
[[ "$output" == *"PR #12: Improve tool"* ]] || fail "pr view header missing"
[[ "$output" == *"General note"* ]] || fail "pr comment missing"
[[ "$output" == *"Inline note"* ]] || fail "inline review comment missing"
[[ "$(request_count)" == 4 ]] || fail "pr view should make four requests"

reset_requests
output=$(run_capture -R acme/widget pr review-comments 12)
[[ "$output" == *"id 99"*"carol on forge line 4"*"Inline note"* ]] || fail "review comment output incorrect"

reset_requests
output=$(run_capture -R acme/widget pr find-by-head topic)
[[ "$output" == "31" ]] || fail "find-by-head output incorrect"

reset_requests
output=$(run_capture issue list)
[[ "$output" == *"20"*"Fix backup"*"bob"* ]] || fail "issue list or repo inference failed"
assert_request 1 GET "/api/v1/repos/acme/widget/issues?type=issues&state=open&limit=50"

reset_requests
output=$(run_capture -R acme/widget issue view 20)
[[ "$output" == *"Issue #20: Fix backup"* ]] || fail "issue view header missing"
[[ "$output" == *"Issue body"*"Issue note"* ]] || fail "issue view content missing"

# issue create: title is required; an omitted body is sent as empty.
reset_requests
run_ok issue create -t "New issue"
assert_request 1 POST "/api/v1/repos/acme/widget/issues"
assert_json 1 '. == {title: "New issue", body: ""}'

# issue edit: stdin body and title can be updated together.
reset_requests
printf 'stdin body\n\n' | run_ok issue edit 20 --title "Updated" --body-file -
assert_request 1 PATCH "/api/v1/repos/acme/widget/issues/20"
assert_json 1 '. == {title: "Updated", body: "stdin body\n\n"}'

# issue close: without flags, closing is a single issue update.
reset_requests
run_ok issue close 20
[[ "$(request_count)" == 1 ]] || fail "plain issue close should make one request"
assert_request 1 PATCH "/api/v1/repos/acme/widget/issues/20"
assert_json 1 '. == {state: "closed"}'

# issue close: the gh-compatible short comment flag posts before closing.
reset_requests
run_ok issue close 20 -c "Implemented"
[[ "$(request_count)" == 2 ]] || fail "issue close with comment should make two requests"
assert_request 1 POST "/api/v1/repos/acme/widget/issues/20/comments"
assert_json 1 '. == {body: "Implemented"}'
assert_request 2 PATCH "/api/v1/repos/acme/widget/issues/20"
assert_json 2 '. == {state: "closed"}'

# issue close: issue URLs and gh close reasons are accepted. Forgejo records
# non-completed reasons in the closing comment because it has no reason field.
reset_requests
run_ok issue close https://forge.example/acme/widget/issues/20 \
  --comment "Already tracked" --duplicate-of 12
[[ "$(request_count)" == 2 ]] || fail "duplicate issue close should make two requests"
assert_request 1 POST "/api/v1/repos/acme/widget/issues/20/comments"
assert_json 1 '. == {body: "Already tracked\n\nDuplicate of #12."}'
assert_request 2 PATCH "/api/v1/repos/acme/widget/issues/20"
assert_json 2 '. == {state: "closed"}'

reset_requests
run_ok issue close 20 --reason "not planned"
assert_request 1 POST "/api/v1/repos/acme/widget/issues/20/comments"
assert_json 1 '. == {body: "Closed as not planned."}'
assert_request 2 PATCH "/api/v1/repos/acme/widget/issues/20"
assert_json 2 '. == {state: "closed"}'

# Invalid invocations must fail before any API request.
reset_requests
run_fail "cannot be combined" pr create -t title -b one -F "$TMP/pr-body.md"
run_fail "specified more than once" pr create -t one --title two
run_fail "requires a value" pr create --title
run_fail "--head cannot be empty" pr create --title title --head ""
run_fail "--base cannot be empty" pr create --title title --base ""
run_fail "requires --title" issue create --body body
run_fail "requires --title, --body, or --body-file" issue edit 20
run_fail "requires --body or --body-file" pr comment 12
run_fail "unexpected argument" pr create "legacy title" topic
run_fail "unknown option" pr edit 12 --unknown value
run_fail "usage: forge issue close" issue close
run_fail "--reason must be one of" issue close 20 --reason invalid
run_fail "cannot be combined" issue close 20 --reason completed --duplicate-of 12
run_fail "issue target must be a number or URL" issue close https://other.example/acme/widget/issues/20
run_fail "duplicate target must be a number or URL" issue close 20 --duplicate-of nope
[[ "$(request_count)" == 0 ]] || fail "invalid commands made an API request"

echo "forge tests passed"
