#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
# shellcheck source=../cascade-under-test.sh
source "$ROOT/tests/flake/cascade-under-test.sh"

MOCK_BIN="$TMP/bin"
mkdir -p "$MOCK_BIN"
export MOCK_LOG="$TMP/commands.log"

{
  printf '#!%s\n' "$(command -v bash)"
  cat <<'EOF'
set -euo pipefail

# Branch heads. A repository with a file under MOCK_HEADS, named as the last
# component of its URL, has that file's content as the head of every ref —
# the per-repository tracking head a push advances. Otherwise MOCK_HEAD is the
# revision every asked-for ref resolves to, or `unreachable` for a remote that
# cannot be read. The answer is given for the first ref requested, as a branch
# that exists is.
if [[ "$1" == "ls-remote" ]]; then
  printf 'git\tls-remote\t%s\n' "${*:2}" >> "$MOCK_LOG"
  head_file="${MOCK_HEADS:-}/$(basename "$3" .git)"
  if [[ -n "${MOCK_HEADS:-}" && -f "$head_file" ]]; then
    printf '%s\t%s\n' "$(cat "$head_file")" "$4"
    exit 0
  fi
  case "${MOCK_HEAD:-}" in
    unreachable) exit 128 ;;
    "") echo "unexpected ls-remote with no MOCK_HEAD: $*" >&2; exit 1 ;;
  esac
  printf '%s\t%s\n' "$MOCK_HEAD" "$4"
  exit 0
fi

[[ "$1" == "-C" ]] || { echo "unexpected git invocation: $*" >&2; exit 1; }
dir="$2"
repo=$(basename "$dir")
shift 2
command="$*"
printf 'git\t%s\t%s\n' "$repo" "$command" >> "$MOCK_LOG"

# MOCK_FAIL_GIT names commands that fail, one per line, exactly as logged.
case $'\n'"${MOCK_FAIL_GIT:-}"$'\n' in *$'\n'"$command"$'\n'*) exit 1 ;; esac

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
    # A pull that brings in a lock commit: the file under MOCK_PULLED_LOCKS
    # named after the repository replaces its flake.lock.
    if [[ -n "${MOCK_PULLED_LOCKS:-}" && -f "$MOCK_PULLED_LOCKS/$repo" ]]; then
      cp "$MOCK_PULLED_LOCKS/$repo" "$dir/flake.lock"
    fi
    exit 0
    ;;
  "push")
    # A push to the default branch advances the repository's tracking head
    # to a revision derived from the lock it pushed, so a downstream pin can
    # be checked against exactly what was pushed.
    if [[ -n "${MOCK_HEADS:-}" ]]; then
      sha256sum "$dir/flake.lock" | cut -c1-40 > "$MOCK_HEADS/$repo"
    fi
    exit 0
    ;;
  "diff --quiet -- flake.lock")
    case "${MOCK_SCENARIO:-}" in pr|direct) exit 1 ;; *) exit 0 ;; esac
    ;;
  "remote get-url origin")
    case "$repo" in
      external-*|allowed-*) printf 'https://github.com/acme/%s.git\n' "$repo" ;;
      no-origin-*) echo "error: No such remote 'origin'" >&2; exit 2 ;;
      *) printf 'ssh://git@forge.anarch.diy:2222/acme/%s.git\n' "$repo" ;;
    esac
    ;;
  "add flake.lock"|"checkout -B agent/flake-update-demo"|"commit -m flake.lock: update "*|"fetch origin agent/flake-update-demo"|"update-ref -d refs/remotes/origin/agent/flake-update-demo"|"push --force-with-lease origin agent/flake-update-demo"|"checkout master"|"checkout -- flake.lock"|"restore --staged flake.lock"|"reset HEAD flake.lock")
    exit 0
    ;;
  *)
    echo "unexpected git invocation for $repo: $command" >&2
    exit 1
    ;;
esac
EOF
} > "$MOCK_BIN/git"

{
  printf '#!%s\n' "$(command -v bash)"
  cat <<'EOF'
set -euo pipefail
printf 'nix\t%s\n' "$*" >> "$MOCK_LOG"

# MOCK_FAIL_NIX names the subcommand that fails: "flake lock", "flake update"
# or "flake metadata".
[[ "$1 $2" == "${MOCK_FAIL_NIX:-}" ]] && exit 1

# Both lock-writing commands read the reference lock (the flake's own unless
# --reference-lock-file names another) and write the output lock (the flake's
# own unless --output-lock-file names another). A fixture path's node is its
# last component. An update moves every named path to the b revision; a lock
# pins each overridden path to the revision its flake reference ends in.
case "$1 $2" in
  "flake update")
    shift 2
    flake="" output="" reference="" filter="."
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --flake) flake="$2"; shift 2 ;;
        --output-lock-file) output="$2"; shift 2 ;;
        --reference-lock-file) reference="$2"; shift 2 ;;
        *) filter+=" | .nodes[\"${1##*/}\"].locked.rev = \"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\""; shift ;;
      esac
    done
    target="${output:-$flake/flake.lock}"
    jq "$filter" "${reference:-$flake/flake.lock}" > "$target.tmp"
    mv "$target.tmp" "$target"
    ;;
  "flake lock")
    shift 2
    flake="" output="" filter="."
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --override-input) filter+=" | .nodes[\"${2##*/}\"].locked.rev = \"${3##*[/=]}\""; shift 3 ;;
        --output-lock-file) output="$2"; shift 2 ;;
        *) flake="$1"; shift ;;
      esac
    done
    target="${output:-$flake/flake.lock}"
    jq "$filter" "$flake/flake.lock" > "$target.tmp"
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
} > "$MOCK_BIN/nix"

{
  printf '#!%s\n' "$(command -v bash)"
  cat <<'EOF'
set -euo pipefail
printf 'forge\t%s\n' "$*" >> "$MOCK_LOG"
case "$*" in
  *"pr find-by-head agent/flake-update-demo")
    [[ -n "${MOCK_FORGE_NO_PR:-}" ]] || printf '42\n' ;;
  *"pr create --title flake.lock: update demo --head agent/flake-update-demo --base master --body "*)
    [[ "${MOCK_FORGE_CREATE_URL:-}" == "" ]] || printf '%s\n' "$MOCK_FORGE_CREATE_URL" ;;
  *) echo "unexpected forge invocation: $*" >&2; exit 1 ;;
esac
EOF
} > "$MOCK_BIN/forge"

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

# Locks whose nodes carry the flake.nix declaration, as a lock Nix wrote does.
# write_declared_lock <dir> <node-name> <original-json> [<node-name> <original-json>]...
# writes root inputs named after each node, all locked at the a revision.
write_declared_lock() {
  local dir="$1"
  shift
  mkdir -p "$dir/.git"
  : > "$dir/.git/HEAD"
  local inputs="{}" nodes="{}"
  while [[ $# -gt 0 ]]; do
    inputs=$(jq -c --arg n "$1" '. + {($n): $n}' <<<"$inputs")
    nodes=$(jq -c --arg n "$1" --argjson o "$2" \
      '. + {($n): {original: $o, locked: {rev: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}' <<<"$nodes")
    shift 2
  done
  jq -n --argjson inputs "$inputs" --argjson nodes "$nodes" \
    '{nodes: ({root: {inputs: $inputs}} + $nodes)}' > "$dir/flake.lock"
}

# A GitHub branch, the shape nixpkgs and home-manager inputs take.
write_github_lock() {
  write_declared_lock "$1" demo '{"type":"github","owner":"acme","repo":"demo","ref":"main"}'
}

# A GitHub reference that names its revision in flake.nix.
write_pinned_lock() {
  write_declared_lock "$1" demo '{"type":"github","owner":"acme","repo":"demo","rev":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'
}

# A git branch on a forge, the shape framework inputs take.
write_git_lock() {
  write_declared_lock "$1" demo '{"type":"git","url":"ssh://git@forge.example:2222/acme/demo.git","ref":"master"}'
}

write_follow_lock() {
  local dir="$1"
  mkdir -p "$dir/.git"
  : > "$dir/.git/HEAD"
  printf '%s\n' '{"nodes":{"root":{"inputs":{"demo":["base","demo"]}}}}' > "$dir/flake.lock"
}

# A chain of three repositories: <leaf> pins the external demo input, <mid>
# pins <leaf>, and <top> pins <mid>, each pin a branch at the forge whose URL
# names the repository the mock gives that origin. Every repository's tracking
# head starts at the a revision the locks hold, so nothing is behind until a
# push or the suite moves a head. The repositories are named by directory so a
# suite can choose which order the directory walk finds them in.
write_chain() {
  local leaf="$1" mid="$2" top="$3"
  export MOCK_HEADS="$HOME/heads"
  mkdir -p "$MOCK_HEADS"
  write_github_lock "$HOME/work/$leaf"
  write_declared_lock "$HOME/work/$mid" \
    "$leaf" "{\"type\":\"git\",\"url\":\"https://forge.anarch.diy/acme/$leaf.git\"}"
  write_declared_lock "$HOME/work/$top" \
    "$mid" "{\"type\":\"git\",\"url\":\"https://forge.anarch.diy/acme/$mid.git\"}"
  local repo
  for repo in "$leaf" "$mid" "$top"; do
    printf '%s\n' aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa > "$MOCK_HEADS/$repo"
  done
}

new_home() {
  export HOME="$TMP/home-$1"
  rm -rf "$HOME"
  mkdir -p "$HOME/work/.git" "$HOME/.config/git"
  : > "$HOME/.config/git/active-pr-branches"
  : > "$HOME/.config/git/protected-branches"
  : > "$MOCK_LOG"
  unset MOCK_HEADS MOCK_PULLED_LOCKS
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
  if output=$(run_cascade "$@" 2>&1); then
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

# assert_log_order <description> <entry>... passes when every entry is in the
# command log and each appears after the one before it.
assert_log_order() {
  local description="$1"
  shift
  local previous=0 line entry
  for entry in "$@"; do
    line=$(grep -Fn -m1 "$entry" "$MOCK_LOG" | cut -d: -f1)
    if [[ -z "$line" ]]; then
      fail "$description" "missing command log entry: $entry" \
        "actual command log:" "$(cat "$MOCK_LOG")"
    elif (( line <= previous )); then
      fail "$description" "out of order at: $entry" \
        "actual command log:" "$(cat "$MOCK_LOG")"
    fi
    previous=$line
  done
  pass "$description"
}

finish_tests() {
  local suite="$1"
  printf '\nTests run: %d\n' "$test_number"
  printf '✅ All %d %s tests passed.\n' "$test_number" "$suite"
}
