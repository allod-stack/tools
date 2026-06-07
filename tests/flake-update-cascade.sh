#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
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

write_direct_lock() {
  local dir="$1"
  mkdir -p "$dir/.git"
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
  printf '%s\n' '{"nodes":{"root":{"inputs":{"demo":["base","demo"]}}}}' > "$dir/flake.lock"
}

new_home() {
  export HOME="$TMP/home-$1"
  rm -rf "$HOME"
  mkdir -p "$HOME/work" "$HOME/.config/git"
  : > "$HOME/.config/git/active-pr-branches"
  : > "$HOME/.config/git/protected-branches"
  : > "$MOCK_LOG"
}

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

run_fail() {
  local expected="$1"
  shift
  local output
  if output=$(bash "$ROOT/flake-update-cascade" "$@" 2>&1); then
    fail "command unexpectedly succeeded: $*"
  fi
  [[ "$output" == *"$expected"* ]] || fail "failure missing '$expected': $output"
}

new_home validation
run_fail "missing required argument" 
run_fail "unknown option: --bad" demo --bad
run_fail "invalid input name" "bad/input"

new_home preflight
write_direct_lock "$HOME/work/dirty-repo"
export MOCK_SCENARIO=dirty
run_fail "Pre-flight checks failed — no changes made." demo
[[ "$(grep -c '^nix' "$MOCK_LOG" || true)" == 0 ]] || fail "preflight failure invoked nix"

new_home skips
mkdir -p "$HOME/work/no-lock/.git"
mkdir -p "$HOME/work/no-input/.git"
printf '%s\n' '{"nodes":{"root":{"inputs":{}}}}' > "$HOME/work/no-input/flake.lock"
write_follow_lock "$HOME/work/follows"
write_direct_lock "$HOME/work/protected"
printf '%s\n' "work/protected master" > "$HOME/.config/git/protected-branches"
export MOCK_SCENARIO=skips
output=$(bash "$ROOT/flake-update-cascade" demo)
[[ "$output" == *"no flake.lock, skipping"* ]] || fail "no-lock skip missing"
[[ "$output" == *"demo not an input, skipping"* ]] || fail "no-input skip missing"
[[ "$output" == *"demo is a follows, not directly pinned, skipping"* ]] || fail "follows skip missing"
[[ "$output" == *"protected branch (master)"* ]] || fail "protected skip missing"
[[ "$(grep -c '^nix' "$MOCK_LOG" || true)" == 0 ]] || fail "skip-only run invoked nix"

new_home dry-run
write_direct_lock "$HOME/work/app"
before=$(sha256sum "$HOME/work/app/flake.lock")
export MOCK_SCENARIO=dry-run
output=$(bash "$ROOT/flake-update-cascade" demo --dry-run)
[[ "$output" == *"aaaaaaa → bbbbbbb  (dry-run: no changes made)"* ]] || fail "dry-run revision output missing"
after=$(sha256sum "$HOME/work/app/flake.lock")
[[ "$before" == "$after" ]] || fail "dry-run changed flake.lock"
grep -Fq $'nix\tflake update demo --flake '"$HOME/work/app"' --output-lock-file /tmp/app.flake.lock.new' "$MOCK_LOG"

new_home pr
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=pr
output=$(bash "$ROOT/flake-update-cascade" demo --pr)
[[ "$output" == *"aaaaaaa → bbbbbbb"* ]] || fail "PR update revision output missing"
[[ "$output" == *"PR #42 updated"* ]] || fail "existing PR output missing"
grep -Fq $'git\tapp\tcheckout -B agent/flake-update-demo' "$MOCK_LOG"
grep -Fq $'git\tapp\tcommit -m flake.lock: update demo' "$MOCK_LOG"
grep -Fq $'git\tapp\tpush --force-with-lease origin agent/flake-update-demo' "$MOCK_LOG"
grep -Fq $'forge\t-R acme/app pr find-by-head agent/flake-update-demo' "$MOCK_LOG"
grep -Fq $'git\tapp\tcheckout master' "$MOCK_LOG"

echo "flake-update-cascade tests passed"
