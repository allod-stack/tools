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
  */api/v1/repos/acme/widget/pulls/12/reviews)
    printf '%s\n' '[{"id":7,"comments_count":1}]'
    ;;
  */api/v1/repos/acme/widget/pulls/12/reviews/7/comments)
    if [[ "$method" == GET ]]; then
      printf '%s\n' '[{"id":99,"path":"forge","position":4}]'
    else
      printf '%s\n' '{"html_url":"https://forge.example/acme/widget/pulls/12#comment-100"}'
    fi
    ;;
  */api/v1/repos/acme/widget/pulls)
    printf '%s\n' '{"html_url":"https://forge.example/acme/widget/pulls/1"}'
    ;;
  */api/v1/repos/acme/widget/pulls/*)
    printf '%s\n' '{"html_url":"https://forge.example/acme/widget/pulls/12"}'
    ;;
  */api/v1/repos/acme/widget/issues/*/comments)
    printf '%s\n' '{"html_url":"https://forge.example/acme/widget/issues/12#comment-1"}'
    ;;
  */api/v1/repos/acme/widget/issues)
    printf '%s\n' '{"html_url":"https://forge.example/acme/widget/issues/20"}'
    ;;
  */api/v1/repos/acme/widget/issues/*)
    printf '%s\n' '{"html_url":"https://forge.example/acme/widget/issues/20"}'
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
[[ "$(request_count)" == 0 ]] || fail "invalid commands made an API request"

echo "forge content flag tests passed"
