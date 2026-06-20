#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

export HOME="$TMP/home"
export MOCK_LOG="$TMP/git.log"
mkdir -p "$HOME/work/.git" "$HOME/work/group/nested" "$TMP/bin"

for repo in clean dirty local unpushed switched pull-fail group/nested/repo; do
  mkdir -p "$HOME/work/$repo/.git"
  : > "$HOME/work/$repo/.git/HEAD"
done

cat > "$TMP/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[[ "$1" == "-C" ]] || { echo "expected git -C" >&2; exit 1; }
repo_dir="$2"
repo=$(basename "$2")
shift 2
printf '%s\t%s\n' "$repo" "$*" >> "$MOCK_LOG"
command="$*"

case "$command" in
  "rev-parse --show-toplevel")
    [[ -f "$repo_dir/.git/HEAD" ]] || exit 1
    printf '%s\n' "$repo_dir"
    ;;
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
    elif [[ "$repo" == local ]]; then
      echo "There is no tracking information for the current branch."
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

assert_log_not_contains() {
  local pattern="$1" description="$2"
  if grep -Eq "$pattern" "$MOCK_LOG"; then
    fail "$description" "actual Git log:" "$(cat "$MOCK_LOG")"
  else
    pass "$description"
  fi
}

# --- Default mode (no flags): pull current branch, no switching ---

output=$("$ROOT/pull-all")

assert_output_contains "clean                     up to date [master]" \
  "default: reports an up-to-date default branch"
assert_output_contains "dirty                     skipped    [dirty working tree]" \
  "default: skips a dirty working tree"
assert_output_contains "local                     error      [pull failed: There is no tracking information" \
  "default: reports pull error for branch without tracking"
assert_output_contains "unpushed                  up to date [feature]" \
  "default: pulls non-default branch in place"
assert_output_contains "switched                  pulled     [feature]" \
  "default: pulls non-default branch without switching"
assert_output_contains "pull-fail                 error      [pull failed: network unavailable]" \
  "default: reports the first line of a pull failure"
assert_output_contains "group/nested/repo         pulled     [master]" \
  "default: discovers and pulls a nested repository"

assert_log_not_contains $'^(local|unpushed|switched)\tcheckout' \
  "default: never checks out non-default branches"
assert_log_not_contains $'^dirty\tpull$' \
  "default: never pulls dirty repos"

# --- --switch mode: switch clean branches ---

: > "$MOCK_LOG"
output=$("$ROOT/pull-all" --switch)

assert_output_contains "clean                     up to date [master]" \
  "--switch: reports an up-to-date default branch"
assert_output_contains "dirty                     skipped    [dirty working tree]" \
  "--switch: skips a dirty working tree"
assert_output_contains "local                     skipped    [on feature — no remote tracking branch]" \
  "--switch: skips a branch without remote tracking"
assert_output_contains "unpushed                  skipped    [on feature — 2 unpushed commits]" \
  "--switch: skips a branch with unpushed commits"
assert_output_contains "switched                  pulled     [master]" \
  "--switch: reports a successful branch switch and pull"
assert_output_contains "pull-fail                 error      [pull failed: network unavailable]" \
  "--switch: reports the first line of a pull failure"
assert_output_contains "group/nested/repo         pulled     [master]" \
  "--switch: discovers and pulls a nested repository"

assert_log_contains $'switched\tcheckout master' \
  "--switch: checks out the default branch before pulling"
assert_log_contains $'switched\tpull' \
  "--switch: pulls after switching branches"
assert_log_contains $'repo\tpull' \
  "--switch: pulls a recursively discovered repository"

assert_log_not_contains $'^(dirty|local|unpushed)\tpull$' \
  "--switch: never pulls repositories that were skipped"

# --- --help ---

help_output=$("$ROOT/pull-all" --help)

if [[ "$help_output" == *"--switch"* ]]; then
  pass "--help mentions --switch flag"
else
  fail "--help mentions --switch flag" "actual output:" "$help_output"
fi

printf '\nTests run: %d\n' "$test_number"
printf '✅ All %d pull-all tests passed.\n' "$test_number"
