#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

export HOME="$TMP/home"
export MOCK_LOG="$TMP/git.log"
mkdir -p "$HOME/work/group/nested" "$TMP/bin"

for repo in clean dirty local unpushed switched pull-fail group/nested/repo; do
  mkdir -p "$HOME/work/$repo/.git"
done

cat > "$TMP/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[[ "$1" == "-C" ]] || { echo "expected git -C" >&2; exit 1; }
repo=$(basename "$2")
shift 2
printf '%s\t%s\n' "$repo" "$*" >> "$MOCK_LOG"
command="$*"

case "$command" in
  "symbolic-ref refs/remotes/origin/HEAD")
    if [[ "$repo" == local ]]; then
      exit 1
    fi
    printf 'refs/remotes/origin/master\n'
    ;;
  "branch --show-current")
    case "$repo" in
      local|unpushed|switched) printf 'feature\n' ;;
      *) printf 'master\n' ;;
    esac
    ;;
  "diff --quiet")
    if [[ "$repo" == dirty ]]; then
      exit 1
    fi
    ;;
  "diff --cached --quiet")
    ;;
  "rev-parse --abbrev-ref @{u}")
    if [[ "$repo" == local ]]; then
      exit 1
    fi
    printf 'origin/feature\n'
    ;;
  "rev-list HEAD...@{u} --count")
    if [[ "$repo" == unpushed ]]; then
      printf '2\n'
    else
      printf '0\n'
    fi
    ;;
  "checkout master")
    ;;
  "pull")
    if [[ "$repo" == pull-fail ]]; then
      echo "network unavailable"
      exit 1
    elif [[ "$repo" == switched || "$repo" == repo ]]; then
      echo "Updating 1111111..2222222"
    else
      echo "Already up to date."
    fi
    ;;
  *)
    echo "unexpected git invocation for $repo: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$TMP/bin/git"
export PATH="$TMP/bin:$PATH"

output=$("$ROOT/pull-all")

test_number=0

pass() {
  test_number=$((test_number + 1))
  printf 'ok %d - %s\n' "$test_number" "$1"
}

fail() {
  test_number=$((test_number + 1))
  printf 'not ok %d - %s\n' "$test_number" "$1" >&2
  shift
  printf '%s\n' "$@" >&2
  exit 1
}

assert_output_contains() {
  local expected="$1" description="$2"
  if [[ "$output" == *"$expected"* ]]; then
    pass "$description"
  else
    fail "$description" "expected output to contain: $expected" "actual output:" "$output"
  fi
}

assert_log_contains() {
  local expected="$1" description="$2"
  if grep -Fxq "$expected" "$MOCK_LOG"; then
    pass "$description"
  else
    fail "$description" "expected Git log entry: $expected" "actual Git log:" "$(cat "$MOCK_LOG")"
  fi
}

assert_output_contains "clean                     up to date [master]" \
  "reports an up-to-date default branch"
assert_output_contains "dirty                     skipped    [dirty working tree]" \
  "skips a dirty working tree"
assert_output_contains "local                     skipped    [on feature — no remote tracking branch]" \
  "skips a branch without remote tracking"
assert_output_contains "unpushed                  skipped    [on feature — 2 unpushed commits]" \
  "skips a branch with unpushed commits"
assert_output_contains "switched                  pulled     [master]" \
  "reports a successful branch switch and pull"
assert_output_contains "pull-fail                 error      [pull failed: network unavailable]" \
  "reports the first line of a pull failure"
assert_output_contains "group/nested/repo         pulled     [master]" \
  "discovers and pulls a nested repository"

assert_log_contains $'switched\tcheckout master' \
  "checks out the default branch before pulling"
assert_log_contains $'switched\tpull' \
  "pulls after switching branches"
assert_log_contains $'repo\tpull' \
  "pulls a recursively discovered repository"

if grep -Eq $'^(dirty|local|unpushed)\tpull$' "$MOCK_LOG"; then
  fail "never pulls repositories that were skipped" "actual Git log:" "$(cat "$MOCK_LOG")"
else
  pass "never pulls repositories that were skipped"
fi

printf '1..%d\n' "$test_number"
