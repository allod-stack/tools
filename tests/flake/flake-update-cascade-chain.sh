#!/usr/bin/env bash
# The mutating path over real git and real nix: three repositories with bare
# origins in a disposable directory, chained leaf → mid → top, and one direct
# run naming only leaf's external input that carries the bump through all
# three, each repository pinning the commit the run just pushed upstream
# (allod/tools#171).
#
# Like the nixConfig suite this drives the real nix, so it runs by hand rather
# than under nix flake check:
#
#   CASCADE_UNDER_TEST=... bash tests/flake/flake-update-cascade-chain.sh
#
# Nix's git fetcher reads the bare origins over file://, so no network is
# needed; the fixture's own HOME carries the allowlist entry the origins need
# and no protected branches.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
# shellcheck source=cascade-under-test.sh
source "$ROOT/tests/flake/cascade-under-test.sh"

for tool in nix git jq; do
  command -v "$tool" >/dev/null || { echo "required tool missing: $tool" >&2; exit 1; }
done

FIX="$TMP/fixture"
mkdir -p "$FIX/src" "$FIX/origin" "$FIX/work/acme" "$FIX/home/.config/git"

# A git identity of the fixture's own, and none of the real home's config or
# hooks.
cat > "$FIX/gitconfig" <<CFG
[user]
	name = fixture
	email = fixture@example.invalid
[init]
	defaultBranch = master
CFG
export GIT_CONFIG_GLOBAL="$FIX/gitconfig" GIT_CONFIG_NOSYSTEM=1
unset XDG_CONFIG_HOME

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

check() {
  # check <description> <actual> <expected>
  [[ "$2" == "$3" ]] || fail "$1: got '$2', expected '$3'"
  echo "ok    $1"
}

# flake_with <dir> [<input> <url>]... writes a flake declaring the inputs.
flake_with() {
  local dir="$1"
  shift
  {
    echo "{"
    while [[ $# -gt 0 ]]; do
      echo "  inputs.$1.url = \"$2\";"
      shift 2
    done
    echo "  outputs = { self, ... }: { };"
    echo "}"
  } > "$dir/flake.nix"
}

# publish <name> locks, commits and pushes src/<name> to a fresh bare origin.
publish() {
  local name="$1" src="$FIX/src/$1"
  git -C "$src" init -q
  git -C "$src" add flake.nix
  nix flake lock "$src" </dev/null 2>"$TMP/lock.log" || fail "locking $name" "$(cat "$TMP/lock.log")"
  [[ -f "$src/flake.lock" ]] && git -C "$src" add flake.lock
  git -C "$src" commit -q -m "fixture $name"
  git init -q --bare "$FIX/origin/$name.git"
  git -C "$src" remote add origin "$FIX/origin/$name.git"
  git -C "$src" push -q origin master
  git -C "$FIX/origin/$name.git" symbolic-ref HEAD refs/heads/master
}

mkdir -p "$FIX/src/demo" "$FIX/src/leaf" "$FIX/src/mid" "$FIX/src/top"
flake_with "$FIX/src/demo"
publish demo
flake_with "$FIX/src/leaf" demo "git+file://$FIX/origin/demo.git"
publish leaf
flake_with "$FIX/src/mid" leaf "git+file://$FIX/origin/leaf.git"
publish mid
flake_with "$FIX/src/top" mid "git+file://$FIX/origin/mid.git"
publish top

for name in leaf mid top; do
  git clone -q "$FIX/origin/$name.git" "$FIX/work/acme/$name"
done

# demo moves: the only change the command line names.
echo change > "$FIX/src/demo/extra"
git -C "$FIX/src/demo" add extra
git -C "$FIX/src/demo" commit -q -m "demo moves"
git -C "$FIX/src/demo" push -q origin master
demo_head=$(git -C "$FIX/origin/demo.git" rev-parse HEAD)

# The cascade's policy files: the origins are outside the forge, so they need
# an allowlist entry; nothing is protected.
printf '%s\n' "$FIX/origin/" > "$FIX/home/.config/git/allowed-external-remotes"
: > "$FIX/home/.config/git/active-pr-branches"
: > "$FIX/home/.config/git/protected-branches"

status=0
output=$(HOME="$FIX/home" WORK_DIR="$FIX/work" run_cascade demo 2>&1) || status=$?
[[ "$status" == 0 ]] || fail "the run exited $status" "$output"

# The order: each repository's push precedes the next one's pull.
headers=$(grep -o '^==> acme/[a-z]*' <<<"$output" | tr '\n' ' ')
check "processes leaf, mid, top in dependency order" "$headers" "==> acme/leaf ==> acme/mid ==> acme/top "
check "leaf pins demo's new head" "$(jq -r .nodes.demo.locked.rev "$FIX/work/acme/leaf/flake.lock")" "$demo_head"
check "mid pins the leaf commit the run pushed" \
  "$(jq -r .nodes.leaf.locked.rev "$FIX/work/acme/mid/flake.lock")" "$(git -C "$FIX/origin/leaf.git" rev-parse HEAD)"
check "top pins the mid commit the run pushed" \
  "$(jq -r .nodes.mid.locked.rev "$FIX/work/acme/top/flake.lock")" "$(git -C "$FIX/origin/mid.git" rev-parse HEAD)"
for name in leaf mid top; do
  check "$name's origin gained exactly one commit" "$(git -C "$FIX/origin/$name.git" rev-list --count HEAD)" "2"
  check "$name's checkout is clean" "$(git -C "$FIX/work/acme/$name" status --porcelain)" ""
done
check "leaf's commit names the input that moved there" \
  "$(git -C "$FIX/origin/leaf.git" log -1 --format=%s)" "flake.lock: update demo"
check "mid's commit names its leaf pin and the demo it carries" \
  "$(git -C "$FIX/origin/mid.git" log -1 --format=%s)" "flake.lock: update leaf, leaf/demo"
check "top's commit names its mid pin and the demo it carries" \
  "$(git -C "$FIX/origin/top.git" log -1 --format=%s)" "flake.lock: update mid, mid/leaf/demo"

echo "flake-update-cascade chain tests passed"
