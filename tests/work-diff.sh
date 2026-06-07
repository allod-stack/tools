#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

export HOME="$TMP/home"
WORK="$HOME/work"
mkdir -p "$WORK/group"

init_repo() {
  local dir="$1"
  git init -q -b master "$dir"
  git -C "$dir" config user.name Test
  git -C "$dir" config user.email test@example.invalid
  printf 'base\n' > "$dir/file.txt"
  git -C "$dir" add file.txt
  git -C "$dir" commit -qm initial
}

init_repo "$WORK/clean"
init_repo "$WORK/changed"
init_repo "$WORK/group/nested"

printf 'staged\n' > "$WORK/changed/staged.txt"
git -C "$WORK/changed" add staged.txt
printf 'base\nunstaged\n' > "$WORK/changed/file.txt"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

output=$(bash "$ROOT/work-diff")
[[ "$output" == *"clean  [master]"* ]] || fail "clean repo missing"
[[ "$output" == *"(clean)"* ]] || fail "clean status missing"
[[ "$output" == *"changed  [master]"* ]] || fail "changed repo missing"
[[ "$output" == *"A  staged.txt"* ]] || fail "porcelain staged status missing"
[[ "$output" == *" M file.txt"* ]] || fail "porcelain unstaged status missing"
[[ "$output" == *"--- staged ---"* ]] || fail "staged diff heading missing"
[[ "$output" == *"+staged"* ]] || fail "staged diff missing"
[[ "$output" == *"--- unstaged ---"* ]] || fail "unstaged diff heading missing"
[[ "$output" == *"+unstaged"* ]] || fail "unstaged diff missing"
[[ "$output" == *"group/nested  [master]"* ]] || fail "nested repo missing"

target=$(bash "$ROOT/work-diff" changed)
[[ "$target" == *"changed  [master]"* ]] || fail "targeted repo missing"
[[ "$target" != *"clean  [master]"* ]] || fail "targeted mode showed another repo"

help=$(bash "$ROOT/work-diff" --help)
[[ "$help" == *"Usage: work-diff"* ]] || fail "help output missing"

if output=$(bash "$ROOT/work-diff" --invalid 2>&1); then
  fail "unknown option succeeded"
fi
[[ "$output" == *"Unknown option: --invalid"* ]] || fail "unknown option message missing"

if output=$(bash "$ROOT/work-diff" missing 2>&1); then
  fail "missing repository succeeded"
fi
[[ "$output" == *"Error: 'missing' not found"* ]] || fail "missing repo message absent"

echo "work-diff tests passed"
