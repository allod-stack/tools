#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

export HOME="$TMP/home"
WORK="$HOME/work"
mkdir -p "$WORK/.git" "$WORK/group"

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

output=$(bash "$ROOT/work-diff")
assert_not_contains "$output" "$WORK  [" \
  "ignores an invalid .git marker at the workspace root"
assert_contains "$output" "clean  [master]" "discovers a top-level clean repository"
assert_contains "$output" "(clean)" "reports a clean working tree"
assert_contains "$output" "changed  [master]" "discovers a changed repository"
assert_contains "$output" "A  staged.txt" "shows staged porcelain status"
assert_contains "$output" " M file.txt" "shows unstaged porcelain status"
assert_contains "$output" "--- staged ---" "labels the staged diff section"
assert_contains "$output" "+staged" "renders staged diff content"
assert_contains "$output" "--- unstaged ---" "labels the unstaged diff section"
assert_contains "$output" "+unstaged" "renders unstaged diff content"
assert_contains "$output" "group/nested  [master]" "recursively discovers a nested repository"

embedded="$TMP/work-diff-embedded"
{
  cat "$ROOT/lib/workspace.sh"
  cat "$ROOT/work-diff"
} > "$embedded"
embedded_output=$(bash "$embedded")
assert_contains "$embedded_output" "group/nested  [master]" \
  "works when the shared library is embedded by Nix packaging"

target=$(bash "$ROOT/work-diff" changed)
assert_contains "$target" "changed  [master]" "shows the requested repository in targeted mode"
assert_not_contains "$target" "clean  [master]" "excludes other repositories in targeted mode"

help=$(bash "$ROOT/work-diff" --help)
assert_contains "$help" "Usage: work-diff" "prints usage for --help"

if output=$(bash "$ROOT/work-diff" --invalid 2>&1); then
  fail "rejects an unknown option" "command unexpectedly succeeded" "$output"
fi
assert_contains "$output" "Unknown option: --invalid" "explains an unknown-option failure"

if output=$(bash "$ROOT/work-diff" missing 2>&1); then
  fail "rejects a missing repository" "command unexpectedly succeeded" "$output"
fi
assert_contains "$output" "Error: 'missing' not found" "explains a missing-repository failure"

printf '\nTests run: %d\n' "$test_number"
printf '✅ All %d work-diff tests passed.\n' "$test_number"
