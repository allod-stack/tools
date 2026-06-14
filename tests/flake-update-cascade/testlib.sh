#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

MOCK_BIN="$TMP/bin"
mkdir -p "$MOCK_BIN"
export MOCK_LOG="$TMP/commands.log"

cat > "$MOCK_BIN/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[[ "$1" == "-C" ]] || { echo "unexpected git invocation: $*" >&2; exit 1; }
dir="$2"
repo=$(basename "$dir")
shift 2
command="$*"
printf 'git\t%s\t%s\n' "$repo" "$command" >> "$MOCK_LOG"

case "$command" in
  "rev-parse --show-toplevel")
    [[ -f "$dir/.git/HEAD" ]] || exit 1
    printf '%s\n' "$dir"
    ;;
  "symbolic-ref refs/remotes/origin/HEAD")
    printf 'refs/remotes/origin/master\n'
    ;;
  "branch --show-current")
    [[ "${MOCK_SCENARIO:-}" == wrong-branch ]] && printf 'feature\n' || printf 'master\n'
    ;;
  "diff --quiet")
    [[ "${MOCK_SCENARIO:-}" == dirty ]] && exit 1 || exit 0
    ;;
  "diff --cached --quiet")
    exit 0
    ;;
  "rev-parse @{u}")
    printf 'origin/master\n'
    ;;
  "rev-list HEAD...@{u} --count")
    [[ "${MOCK_SCENARIO:-}" == unpushed ]] && printf '1\n' || printf '0\n'
    ;;
  "pull")
    exit 0
    ;;
  "diff --quiet -- flake.lock")
    [[ "${MOCK_SCENARIO:-}" == pr ]] && exit 1 || exit 0
    ;;
  "remote get-url origin")
    printf 'ssh://git@forge.example:2222/acme/%s.git\n' "$repo"
    ;;
  "add flake.lock"|"checkout -B agent/flake-update-demo"|"commit -m flake.lock: update demo"|"fetch origin agent/flake-update-demo"|"push --force-with-lease origin agent/flake-update-demo"|"checkout master"|"checkout -- flake.lock")
    exit 0
    ;;
  *)
    echo "unexpected git invocation for $repo: $command" >&2
    exit 1
    ;;
esac
EOF

cat > "$MOCK_BIN/nix" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'nix\t%s\n' "$*" >> "$MOCK_LOG"

case "$1 $2" in
  "flake update")
    flake=""
    output=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --flake) flake="$2"; shift 2 ;;
        --output-lock-file) output="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    target="${output:-$flake/flake.lock}"
    jq '.nodes.demo.locked.rev = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"' \
      "$flake/flake.lock" > "$target.tmp"
    mv "$target.tmp" "$target"
    ;;
  "flake metadata")
    exit 0
    ;;
  *)
    echo "unexpected nix invocation: $*" >&2
    exit 1
    ;;
esac
EOF

cat > "$MOCK_BIN/forge" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'forge\t%s\n' "$*" >> "$MOCK_LOG"
case "$*" in
  *"pr find-by-head agent/flake-update-demo") printf '42\n' ;;
  *) echo "unexpected forge invocation: $*" >&2; exit 1 ;;
esac
EOF

chmod +x "$MOCK_BIN/git" "$MOCK_BIN/nix" "$MOCK_BIN/forge"
export PATH="$MOCK_BIN:$PATH"
test_number=0

write_direct_lock() {
  local dir="$1"
  mkdir -p "$dir/.git"
  : > "$dir/.git/HEAD"
  cat > "$dir/flake.lock" <<'EOF'
{
  "nodes": {
    "root": {"inputs": {"demo": "demo"}},
    "demo": {"locked": {"rev": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
  }
}
EOF
}

write_follow_lock() {
  local dir="$1"
  mkdir -p "$dir/.git"
  : > "$dir/.git/HEAD"
  printf '%s\n' '{"nodes":{"root":{"inputs":{"demo":["base","demo"]}}}}' > "$dir/flake.lock"
}

new_home() {
  export HOME="$TMP/home-$1"
  rm -rf "$HOME"
  mkdir -p "$HOME/work/.git" "$HOME/.config/git"
  : > "$HOME/.config/git/active-pr-branches"
  : > "$HOME/.config/git/protected-branches"
  : > "$MOCK_LOG"
}

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

run_fail() {
  local expected="$1" description="$2"
  shift 2
  local output
  if output=$(bash "$ROOT/flake-update-cascade" "$@" 2>&1); then
    fail "$description" "command unexpectedly succeeded: $*" "$output"
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

assert_log_contains() {
  local expected="$1" description="$2"
  if grep -Fq "$expected" "$MOCK_LOG"; then
    pass "$description"
  else
    fail "$description" "expected command log entry: $expected" \
      "actual command log:" "$(cat "$MOCK_LOG")"
  fi
}

finish_tests() {
  local suite="$1"
  printf '\nTests run: %d\n' "$test_number"
  printf '✅ All %d %s tests passed.\n' "$test_number" "$suite"
}
