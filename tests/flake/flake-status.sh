#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

export HOME="$TMP/home"
mkdir -p "$HOME/work/.git" "$HOME/work/group" "$TMP/bin"

write_lock() {
  local dir="$1" rev="$2" include_demo="$3"
  mkdir -p "$dir/.git"
  : > "$dir/.git/HEAD"
  if [[ "$include_demo" == true ]]; then
    cat > "$dir/flake.lock" <<EOF
{
  "nodes": {
    "root": {"inputs": {"demo": "demo-node", "followed": ["demo", "nixpkgs"]}},
    "demo-node": {
      "locked": {"rev": "$rev", "lastModified": 0},
      "original": {"type": "github", "owner": "acme", "repo": "demo"}
    }
  }
}
EOF
  else
    cat > "$dir/flake.lock" <<'EOF'
{"nodes":{"root":{"inputs":{"followed":["other","nixpkgs"]}}}}
EOF
  fi
}

REV_A=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
REV_B=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
write_lock "$HOME/work/alpha" "$REV_A" true
write_lock "$HOME/work/beta" "$REV_B" true
write_lock "$HOME/work/group/delta" "$REV_A" true
write_lock "$HOME/work/gamma" "$REV_A" false
mkdir -p "$HOME/work/no-lock/.git"
: > "$HOME/work/no-lock/.git/HEAD"

# epsilon holds the pins --upstream must compare to the branch each lock
# names, not to the remote's default branch: stable sits at the head of its
# release branch while the default branch has moved on; lagging is behind its
# release branch; pinned names its own revision; tagged is an annotated tag
# whose peeled commit is what the lock holds.
REV_STABLE=dddddddddddddddddddddddddddddddddddddddd
REV_LAGGING=eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
REV_PINNED=0123456789012345678901234567890123456789
REV_TAG_PEELED=1111111111111111111111111111111111111111
mkdir -p "$HOME/work/epsilon/.git"
: > "$HOME/work/epsilon/.git/HEAD"
cat > "$HOME/work/epsilon/flake.lock" <<EOF
{
  "nodes": {
    "root": {"inputs": {"stable": "stable-node", "lagging": "lagging-node",
                        "pinned": "pinned-node", "tagged": "tagged-node"}},
    "stable-node": {
      "locked": {"rev": "$REV_STABLE", "lastModified": 0},
      "original": {"type": "github", "owner": "acme", "repo": "stable", "ref": "release-1.0"}
    },
    "lagging-node": {
      "locked": {"rev": "$REV_LAGGING", "lastModified": 0},
      "original": {"type": "git", "url": "https://example.org/acme/lagging.git", "ref": "release-1.0"}
    },
    "pinned-node": {
      "locked": {"rev": "$REV_PINNED", "lastModified": 0},
      "original": {"type": "github", "owner": "acme", "repo": "pinned", "rev": "$REV_PINNED"}
    },
    "tagged-node": {
      "locked": {"rev": "$REV_TAG_PEELED", "lastModified": 0},
      "original": {"type": "github", "owner": "acme", "repo": "tagged", "ref": "v1"}
    }
  }
}
EOF

export LS_REMOTE_LOG="$TMP/ls-remote.log"
: > "$LS_REMOTE_LOG"

# The interpreter is named outright: the check sandbox has no /usr/bin/env.
{
printf '#!%s\n' "$(command -v bash)"
cat <<'EOF'
set -euo pipefail

# ls-remote answers per remote and per ref, the way a real remote does: the
# default branch of acme/demo and acme/stable is ccccccc; release-1.0 of
# acme/stable is at the lock's revision, release-1.0 of acme/lagging is ahead
# of it; acme/tagged carries only an annotated tag v1. Every call is logged so
# the suite can see which refs were asked for.
if [[ "$1" == "ls-remote" ]]; then
  shift
  [[ "$1" == "--exit-code" ]] && shift
  url="$1"; shift
  printf '%s %s\n' "$url" "$*" >> "$LS_REMOTE_LOG"
  found=0
  for spec in "$@"; do
    case "$url $spec" in
      "https://github.com/acme/demo HEAD"|"https://github.com/acme/stable HEAD")
        printf 'cccccccccccccccccccccccccccccccccccccccc\tHEAD\n'; found=1 ;;
      "https://github.com/acme/stable refs/heads/release-1.0")
        printf 'dddddddddddddddddddddddddddddddddddddddd\trefs/heads/release-1.0\n'; found=1 ;;
      "https://example.org/acme/lagging.git refs/heads/release-1.0")
        printf 'ffffffffffffffffffffffffffffffffffffffff\trefs/heads/release-1.0\n'; found=1 ;;
      "https://github.com/acme/tagged refs/tags/v1")
        printf '2222222222222222222222222222222222222222\trefs/tags/v1\n'
        printf '1111111111111111111111111111111111111111\trefs/tags/v1^{}\n'; found=1 ;;
    esac
  done
  (( found )) && exit 0
  exit 2
fi

[[ "$1" == "-C" ]] || { echo "unexpected git invocation: $*" >&2; exit 1; }
repo_dir="$2"
repo=$(basename "$2")
shift 2
command="$*"

case "$command" in
  "rev-parse --show-toplevel")
    [[ -f "$repo_dir/.git/HEAD" ]] || exit 1
    printf '%s\n' "$repo_dir"
    ;;
  "symbolic-ref refs/remotes/origin/HEAD")
    printf 'refs/remotes/origin/master\n'
    ;;
  "branch --show-current")
    [[ "$repo" == beta ]] && printf 'feature\n' || printf 'master\n'
    ;;
  "diff --cached --quiet")
    exit 0
    ;;
  "diff --quiet")
    [[ "$repo" == beta ]] && exit 1 || exit 0
    ;;
  "rev-list HEAD...@{u} --count")
    [[ "$repo" == beta ]] && printf '2\n' || printf '0\n'
    ;;
  *)
    echo "unexpected git invocation for $repo: $command" >&2
    exit 1
    ;;
esac
EOF
} > "$TMP/bin/git"
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

assert_contains() {
  local actual="$1" expected="$2" description="$3"
  if [[ "$actual" == *"$expected"* ]]; then
    pass "$description"
  else
    fail "$description" "expected output to contain: $expected" "actual output:" "$actual"
  fi
}

assert_not_contains() {
  local actual="$1" unexpected="$2" description="$3"
  if [[ "$actual" != *"$unexpected"* ]]; then
    pass "$description"
  else
    fail "$description" "expected output not to contain: $unexpected" "actual output:" "$actual"
  fi
}

output=$(bash "$ROOT/flake/flake-status" demo)
assert_contains "$output" "demo — INCONSISTENT (1 repo differs)" \
  "reports inconsistent revisions"
assert_contains "$output" "alpha                 aaaaaaa  1970-01-01" \
  "shows the majority revision"
assert_contains "$output" "beta                  bbbbbbb  1970-01-01" \
  "shows a differing revision"
assert_contains "$output" "[on branch feature, not master]" \
  "warns about a non-default branch"
assert_contains "$output" "[dirty: unstaged changes]" \
  "warns about unstaged changes"
assert_contains "$output" "[2 unpushed commits]" \
  "warns about unpushed commits"
assert_contains "$output" "← stale" \
  "marks the differing revision as stale"
assert_contains "$output" "gamma                 (not an input)" \
  "reports repositories without the named input"
assert_not_contains "$output" "no-lock" \
  "omits repositories without a lock file"

upstream=$(bash "$ROOT/flake/flake-status" demo --upstream)
assert_contains "$upstream" "upstream: ccccccc (local pins are behind)" \
  "reports when local pins are behind upstream"

if output=$(bash "$ROOT/flake/flake-status" demo --check-upstream 2>&1); then
  fail "rejects the removed --check-upstream alias" \
    "command unexpectedly succeeded" "$output"
fi
assert_contains "$output" "unknown option: --check-upstream" \
  "rejects the removed --check-upstream alias"

all=$(bash "$ROOT/flake/flake-status")
assert_contains "$all" "==> alpha" \
  "shows repository headings in all-input mode"
assert_contains "$all" "demo                  aaaaaaa  1970-01-01" \
  "shows direct pins in all-input mode"
assert_not_contains "$all" "followed" \
  "omits follows inputs from all-input mode"

all_upstream=$(bash "$ROOT/flake/flake-status" --upstream)
assert_contains "$all_upstream" "→ ccccccc" \
  "shows upstream arrow inline in all-input mode"
assert_not_contains "$all_upstream" "upstream:" \
  "does not show separate upstream rows in all-input mode"
assert_contains "$all_upstream" "flake-update-cascade demo" \
  "suggests flake-update-cascade for outdated inputs"
assert_contains "$all_upstream" "Outdated (external):" \
  "labels github inputs as external"

# A pin is compared to the branch its lock names (allod/tools#170).
row() { grep -E "^  $2 " <<< "$1" || true; }
stable_row=$(row "$all_upstream" stable)
assert_contains "$stable_row" "stable                ddddddd  1970-01-01" \
  "shows a release-branch pin at its head"
assert_not_contains "$stable_row" "→" \
  "puts no arrow on a pin at the head of the branch its lock names"
assert_contains "$(row "$all_upstream" lagging)" "→ fffffff" \
  "puts an arrow on a pin behind the branch its lock names"
assert_not_contains "$(row "$all_upstream" pinned)" "→" \
  "puts no arrow on a pin that names its own revision"
assert_not_contains "$(row "$all_upstream" tagged)" "→" \
  "compares a tag pin to the tag's peeled commit"
external=$(grep -A1 'Outdated (external):' <<< "$all_upstream" | tail -1)
assert_contains "$external" "flake-update-cascade demo lagging" \
  "lists only the pins behind their own branch, sorted"
assert_not_contains "$external" "stable" \
  "omits a pin at the head of its release branch from the outdated list"
assert_contains "$(cat "$LS_REMOTE_LOG")" \
  "https://github.com/acme/stable refs/heads/release-1.0 refs/tags/release-1.0" \
  "asks the remote for the locked ref as a branch, then as a tag"
assert_not_contains "$(cat "$LS_REMOTE_LOG")" "https://github.com/acme/stable HEAD" \
  "does not ask for the default branch when the lock names a ref"
assert_not_contains "$(cat "$LS_REMOTE_LOG")" "acme/pinned" \
  "does not ask the remote about a pin that names its own revision"

named_stable=$(bash "$ROOT/flake/flake-status" stable --upstream)
assert_contains "$named_stable" "upstream: up to date at ddddddd" \
  "named-input mode compares to the branch the lock names"

help=$(bash "$ROOT/flake/flake-status" --help)
assert_contains "$help" "Usage: flake-status" "prints usage for --help"

if output=$(bash "$ROOT/flake/flake-status" --invalid 2>&1); then
  fail "rejects an unknown option" "command unexpectedly succeeded" "$output"
fi
assert_contains "$output" "unknown option: --invalid" \
  "explains an unknown-option failure"

if output=$(bash "$ROOT/flake/flake-status" one two 2>&1); then
  fail "rejects an extra positional argument" "command unexpectedly succeeded" "$output"
fi
assert_contains "$output" "unexpected argument: two" \
  "explains an extra-argument failure"

printf '\nTests run: %d\n' "$test_number"
printf '✅ All %d flake-status tests passed.\n' "$test_number"
