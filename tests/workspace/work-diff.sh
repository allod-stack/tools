#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

export HOME="$TMP/home"
WORK="$HOME/work"
mkdir -p "$WORK/.git" "$WORK/group" "$WORK/.cache"

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
init_repo "$WORK/untracked"
init_repo "$WORK/.profile"
init_repo "$WORK/.cache/ignored"

printf 'staged\n' > "$WORK/changed/staged.txt"
git -C "$WORK/changed" add staged.txt
printf 'base\nunstaged\n' > "$WORK/changed/file.txt"

printf 'loose\n' > "$WORK/untracked/loose.txt"

# Linked worktrees, sited outside the workspace the way agent worktrees are.
WT="$TMP/worktrees"
mkdir -p "$WT"

git -C "$WORK/changed" worktree add -q -b agent/demo "$WT/demo"
printf 'wt-staged\n' > "$WT/demo/wt-staged.txt"
git -C "$WT/demo" add wt-staged.txt
printf 'base\nfrom-worktree\n' > "$WT/demo/file.txt"

git -C "$WORK/clean" worktree add -q --detach "$WT/detached"
printf 'base\ndetached-edit\n' > "$WT/detached/file.txt"

git -C "$WORK/group/nested" worktree add -q -b agent/gone "$WT/gone"
rm -rf "$WT/gone"

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

if output=$(bash "$ROOT/workspace/work-diff"); then
  pass "renders every repo without aborting on an untracked-only working tree"
else
  fail "renders every repo without aborting on an untracked-only working tree" \
    "work-diff exited non-zero; output:" "$output"
fi

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
assert_contains "$output" ".profile  [master]" "discovers a dot-named repository"
assert_not_contains "$output" ".cache/ignored" "does not recurse through a dot-named non-repo directory"
assert_contains "$output" "untracked  [master]" "discovers a repository with only untracked changes"
assert_contains "$output" "?? loose.txt" "shows untracked-only porcelain status"

# --- Linked worktrees -------------------------------------------------------

assert_contains "$output" "↳ worktree $WT/demo  [agent/demo]" \
  "shows a linked worktree attributed to its branch"
assert_contains "$output" "A  wt-staged.txt" "shows staged status from inside a worktree"
assert_contains "$output" "+from-worktree" "renders uncommitted worktree changes"
assert_contains "$output" "↳ worktree $WT/detached  [detached]" \
  "labels a worktree with a detached HEAD"
assert_contains "$output" "+detached-edit" "renders changes in a detached worktree"
assert_not_contains "$output" "$WT/gone" \
  "skips a worktree whose directory no longer exists"

before_worktree="${output%%↳ worktree*}"
assert_contains "$before_worktree" "changed  [master]" \
  "nests a worktree under the repo that owns it"
assert_not_contains "$before_worktree" "clean  [master]" \
  "does not defer worktree output past the next repository"

worktrees=$(bash -c 'source "$1"; workspace_collect_worktrees "$2"' _ \
  "$ROOT/lib/workspace.sh" "$WORK/changed")
assert_contains "$worktrees" "$WT/demo"$'\t'"agent/demo" \
  "workspace_collect_worktrees reports path and branch"
assert_not_contains "$worktrees" "$WORK/changed"$'\t' \
  "workspace_collect_worktrees skips the main checkout"

# C5: the mutating consumers (pull-all, flake-status, flake-update-cascade) read
# workspace_collect_repos, whose output set stays exactly the registry checkouts
# even for repos that have linked worktrees.
collected=$(bash -c 'source "$1"; workspace_collect_repos "$2"' _ \
  "$ROOT/lib/workspace.sh" "$WORK")
expected=$'changed\nclean\ngroup/nested\nuntracked\n.profile'
if [[ "$collected" == "$expected" ]]; then
  pass "workspace_collect_repos returns exactly the registry checkouts"
else
  fail "workspace_collect_repos returns exactly the registry checkouts" \
    "expected:" "$expected" "actual:" "$collected"
fi

embedded="$TMP/work-diff-embedded"
{
  cat "$ROOT/lib/workspace.sh"
  cat "$ROOT/workspace/work-diff"
} > "$embedded"
embedded_output=$(bash "$embedded")
assert_contains "$embedded_output" "group/nested  [master]" \
  "works when the shared library is embedded by Nix packaging"
assert_contains "$embedded_output" ".profile  [master]" \
  "embedded library discovers a dot-named repository"
assert_contains "$embedded_output" "↳ worktree $WT/demo  [agent/demo]" \
  "embedded library renders linked worktrees"

target=$(bash "$ROOT/workspace/work-diff" changed)
assert_contains "$target" "changed  [master]" "shows the requested repository in targeted mode"
assert_not_contains "$target" "clean  [master]" "excludes other repositories in targeted mode"
assert_contains "$target" "↳ worktree $WT/demo  [agent/demo]" \
  "shows the requested repository's worktrees in targeted mode"
assert_not_contains "$target" "$WT/detached" \
  "excludes other repositories' worktrees in targeted mode"

help=$(bash "$ROOT/workspace/work-diff" --help)
assert_contains "$help" "Usage: work-diff" "prints usage for --help"

if output=$(bash "$ROOT/workspace/work-diff" --invalid 2>&1); then
  fail "rejects an unknown option" "command unexpectedly succeeded" "$output"
fi
assert_contains "$output" "Unknown option: --invalid" "explains an unknown-option failure"

if output=$(bash "$ROOT/workspace/work-diff" missing 2>&1); then
  fail "rejects a missing repository" "command unexpectedly succeeded" "$output"
fi
assert_contains "$output" "Error: 'missing' not found" "explains a missing-repository failure"

printf '\nTests run: %d\n' "$test_number"
printf '✅ All %d work-diff tests passed.\n' "$test_number"
