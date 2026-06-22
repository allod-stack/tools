#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
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

cat > "$MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

method=GET
url=""
data=""
write_out=""
auth_header=""
config_file=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    -X) method="$2"; shift 2 ;;
    -H)
      case "$2" in
        Authorization:*)
          echo "SECURITY: Authorization header passed in argv" >&2
          exit 99
          ;;
      esac
      shift 2
      ;;
    --config)
      config_file="$2"
      shift 2
      ;;
    -d) data="$2"; shift 2 ;;
    -w) write_out="$2"; shift 2 ;;
    -sf|-fs|-s|-f) shift ;;
    http://*|https://*) url="$1"; shift ;;
    *) echo "unexpected mocked curl argument: $1" >&2; exit 1 ;;
  esac
done

if [[ -n "${config_file:-}" && -r "$config_file" ]]; then
  while IFS= read -r line; do
    case "$line" in
      header\ =\ \"Authorization:*)
        auth_header="${line#header = \"}"
        auth_header="${auth_header%\"}"
        ;;
    esac
  done < "$config_file"
fi

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
  */api/v1/repos/acme/gadget/pulls)
    printf '%s\n' '{"html_url":"https://forge.example/acme/gadget/pulls/1"}'
    ;;
  */api/v1/repos/acme/gadget/issues\?type=issues\&state=open\&limit=50)
    printf '%s\n' '[{"number":21,"title":"Gadget issue","user":{"login":"zoe"}}]'
    ;;
  */api/v1/repos/acme/gadget/issues)
    printf '%s\n' '{"html_url":"https://forge.example/acme/gadget/issues/21"}'
    ;;
  */api/v1/user)
    if [[ "$auth_header" == *"valid-token"* ]]; then
      printf '%s' '{"login":"testuser"}'
      [[ -z "$write_out" ]] || printf '\n200'
    else
      printf '%s' '{"message":"Unauthorized"}'
      [[ -z "$write_out" ]] || printf '\n401'
    fi
    ;;
  *)
    echo "unexpected mocked URL: $url" >&2
    exit 1
    ;;
esac
EOF

chmod +x "$MOCK_BIN/git" "$MOCK_BIN/curl"
export PATH="$MOCK_BIN:$PATH"
export FORGEJO_TOKEN=test-token
export FORGE_URL=https://forge.example
export MOCK_REQUEST_DIR="$REQUEST_DIR"
test_number=0

pass() {
  test_number=$((test_number + 1))
  printf '✅ %d - %s\n' "$test_number" "$1"
}

fail() {
  test_number=$((test_number + 1))
  printf '❌ %d - %s\n' "$test_number" "$1" >&2
  shift
  printf '%s\n' "$@" >&2
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
  local number="$1" method="$2" url_suffix="$3" description="$4"
  local actual_method actual_url
  actual_method=$(<"$REQUEST_DIR/$number.method")
  actual_url=$(<"$REQUEST_DIR/$number.url")
  if [[ "$actual_method" == "$method" && "$actual_url" == *"$url_suffix" ]]; then
    pass "$description"
  else
    fail "$description" "expected: $method ...$url_suffix" \
      "actual: $actual_method $actual_url"
  fi
}

assert_json() {
  local number="$1" expression="$2" description="$3"
  if jq -e "$expression" "$REQUEST_DIR/$number.data" >/dev/null; then
    pass "$description"
  else
    fail "$description" "JSON assertion: $expression" \
      "actual payload:" "$(cat "$REQUEST_DIR/$number.data")"
  fi
}

run_ok() {
  local output
  output=$("$ROOT/forge" -R acme/widget "$@" 2>&1) ||
    fail "runs forge -R acme/widget $*" "command output:" "$output"
}

run_capture() {
  "$ROOT/forge" "$@" 2>&1
}

run_fail() {
  local expected="$1" description="$2"
  shift 2
  local output
  if output=$("$ROOT/forge" -R acme/widget "$@" 2>&1); then
    fail "$description" "command unexpectedly succeeded: forge -R acme/widget $*" "$output"
  elif [[ "$output" == *"$expected"* ]]; then
    pass "$description"
  else
    fail "$description" "expected failure to contain: $expected" "actual output:" "$output"
  fi
}

assert_contains() {
  local actual="$1" expected="$2" description="$3"
  if [[ "$actual" == *"$expected"* ]]; then
    pass "$description"
  else
    fail "$description" "expected output to contain: $expected" "actual output:" "$actual"
  fi
}

assert_equal() {
  local actual="$1" expected="$2" description="$3"
  if [[ "$actual" == "$expected" ]]; then
    pass "$description"
  else
    fail "$description" "expected: $expected" "actual: $actual"
  fi
}

finish_tests() {
  local suite="$1"
  printf '\nTests run: %d\n' "$test_number"
  printf '✅ All %d %s tests passed.\n' "$test_number" "$suite"
}
