#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ALLOD="${ALLOD_UNDER_TEST:-$(command -v allod)}"
REAL_GIT=$(command -v git)
TMP=$(mktemp -d)
RUN_ID="t$$"
CAPTURE_OUTPUT=""
CAPTURE_STATUS=0
test_number=0
repo_number=0
declare -a WORKTREES=()

# Clones and worktrees now land under $HOME/changes, and $HOME is the temp
# dir removed below, so no sweep outside $TMP is needed.
cleanup() {
  local path
  for path in "${WORKTREES[@]}"; do
    rm -rf "$path"
  done
  rm -rf "$TMP"
}
trap cleanup EXIT

export HOME="$TMP/home"
export XDG_CONFIG_HOME="$HOME/.config"
mkdir -p "$HOME/.config/git" "$HOME/work" "$TMP/remotes"
touch "$HOME/.config/git/protected-branches"
# A clone's own .git carries none of a fixture repo's per-repo identity, the
# same as a real worker's clone relies on $HOME/.gitconfig for one.
git config --global user.name "Test User"
git config --global user.email "test@example.invalid"

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

assert_equal() {
  local actual="$1" expected="$2" description="$3"
  if [[ "$actual" == "$expected" ]]; then
    pass "$description"
  else
    fail "$description" "expected: $expected" "actual: $actual"
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

assert_not_contains() {
  local actual="$1" unexpected="$2" description="$3"
  if [[ "$actual" != *"$unexpected"* ]]; then
    pass "$description"
  else
    fail "$description" "expected output not to contain: $unexpected" "actual output:" "$actual"
  fi
}

assert_status() {
  local expected="$1" description="$2"
  if [[ "$CAPTURE_STATUS" -eq "$expected" ]]; then
    pass "$description"
  else
    fail "$description" "expected status: $expected" "actual status: $CAPTURE_STATUS" \
      "output:" "$CAPTURE_OUTPUT"
  fi
}

capture() {
  set +e
  CAPTURE_OUTPUT=$("$@" 2>&1)
  CAPTURE_STATUS=$?
  set -e
}

capture_with_path() {
  local path="$1"
  shift
  set +e
  CAPTURE_OUTPUT=$(PATH="$path" "$@" 2>&1)
  CAPTURE_STATUS=$?
  set -e
}

init_repo() {
  local repo="$1" branch="${2:-master}"
  local remote
  repo_number=$((repo_number + 1))
  remote="$TMP/remotes/repo-${repo_number}.git"

  mkdir -p "$(dirname "$repo")"
  git init -q --bare -b "$branch" "$remote"
  git init -q -b "$branch" "$repo"
  git -C "$repo" config user.name "Test User"
  git -C "$repo" config user.email "test@example.invalid"
  printf 'base\n' > "$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm initial
  git -C "$repo" remote add origin "$remote"
  git -C "$repo" push -q -u origin "$branch"
  git -C "$repo" fetch -q origin
  git -C "$repo" remote set-head origin "$branch" >/dev/null 2>&1 || true
}

init_repo_no_origin_head() {
  local repo="$1" branch="${2:-master}"
  init_repo "$repo" "$branch"
  # Silent on success; a failure here must be legible rather than swallowed,
  # since the fixture it builds is the whole point of the test that uses it.
  git -C "$repo" remote set-head origin -d
}

# 'begin' leaves the submodule unpopulated; populating it is what 'git worktree remove' refuses,
# so callers exercise both states.
init_repo_with_submodule() {
  local repo="$1" branch="${2:-master}" sub="$TMP/submodule-source-$((repo_number + 1))"
  git init -q -b "$branch" "$sub"
  git -C "$sub" config user.name "Test User"
  git -C "$sub" config user.email "test@example.invalid"
  printf 'sub\n' > "$sub/sub.txt"
  git -C "$sub" add sub.txt
  git -C "$sub" commit -qm "submodule initial"

  init_repo "$repo" "$branch"
  git -C "$repo" -c protocol.file.allow=always submodule add -q "$sub" vendor
  git -C "$repo" commit -qm "add submodule"
  git -C "$repo" push -q origin "$branch"
}

populate_submodules() {
  git -C "$1" -c protocol.file.allow=always submodule update --init -q
}

init_repo_no_remote() {
  local repo="$1" branch="${2:-master}"
  mkdir -p "$(dirname "$repo")"
  git init -q -b "$branch" "$repo"
  git -C "$repo" config user.name "Test User"
  git -C "$repo" config user.email "test@example.invalid"
  printf 'base\n' > "$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm initial
}

protect_repo() {
  local repo="$1" branch="$2"
  printf '%s %s\n' "${repo#"$HOME"/}" "$branch" >> "$HOME/.config/git/protected-branches"
}

begin_clone() {
  local desc="$1" repo="$2" path
  path=$("$ALLOD" change begin -d "$desc" "$repo")
  WORKTREES+=("$path")
  printf '%s\n' "$path"
}

# 'list'/'record'/'cleanup' must still handle a linked worktree though 'begin' no longer makes
# one; mirrors 'begin -d': fetch, branch from origin/<start>, write the same handoff file.
make_legacy_worktree() {
  local desc="$1" repo="$2" start="$3" path="$HOME/changes/legacy-${desc}"
  git -C "$repo" fetch -q origin
  git -C "$repo" worktree add -q -b "agent/$desc" "$path" "origin/$start"
  git_dir=$(git -C "$path" rev-parse --path-format=absolute --git-dir)
  printf 'agent/%s\n' "$desc" > "$git_dir/allod-change-branch"
  WORKTREES+=("$path")
  printf '%s\n' "$path"
}

record_in_repo() {
  local repo="$1"
  shift
  (cd "$repo" && "$ALLOD" change record "$@")
}

submit_in_repo() {
  local repo="$1"
  shift
  (cd "$repo" && "$ALLOD" change submit "$@")
}

remote_has_branch() {
  local repo="$1" branch="$2"
  git -C "$repo" ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1
}

changed_files_in_head() {
  local repo="$1"
  git -C "$repo" diff-tree --no-commit-id --name-only -r HEAD | sort
}

worktree_count() {
  git -C "$1" worktree list | wc -l
}

# Named rather than counting all of $HOME/changes: earlier tests leave their own entries there.
changes_entries_for() {
  local desc="$1" count=0 entry
  shopt -s nullglob
  for entry in "$HOME/changes/"*"-${desc}-"*; do
    if [[ -e "$entry" ]]; then
      count=$((count + 1))
    fi
  done
  shopt -u nullglob
  printf '%s\n' "$count"
}

list_state_for() {
  local repo="$1" path="$2"
  "$ALLOD" change list "$repo" | awk -F'\t' -v p="$path" '$2 == p { print $4 }'
}

# Contract: list reports 'clean' iff cleanup would succeed. Runs cleanup for real, since list's
# own precedence order differs from cleanup's and the messages need not agree.
assert_list_matches_cleanup() {
  local repo="$1" path="$2" expected="$3" label="$4"

  assert_equal "$(list_state_for "$repo" "$path")" "$expected" "list reports $expected for $label"
  capture "$ALLOD" change cleanup "$path"
  if [[ "$expected" == "clean" ]]; then
    assert_status 0 "cleanup succeeds where list reported clean: $label"
  elif [[ "$CAPTURE_STATUS" -ne 0 ]]; then
    pass "cleanup refuses where list reported $expected: $label"
  else
    fail "cleanup refuses where list reported $expected: $label" "$CAPTURE_OUTPUT"
  fi
}

make_restricted_path_without_forge() {
  local bin="$TMP/no-forge-bin"
  local tool target
  mkdir -p "$bin"
  for tool in bash git dirname mktemp sed grep cat rm basename awk; do
    target=$(command -v "$tool")
    ln -sf "$target" "$bin/$tool"
  done
  printf '%s\n' "$bin"
}

# MOCK_FORGE_ALL_STATE and MOCK_FORGE_ALL_NUMBER answer '-s all' (cleanup and
# list's own query): empty MOCK_FORGE_ALL_STATE means no pull request for
# the branch at all. MOCK_FORGE_FAIL, set to anything, forces every call to
# fail, for the "forge did not answer" cases.
make_mock_forge() {
  local mode="$1"
  local bin="$TMP/forge-${mode}-bin"
  mkdir -p "$bin"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'EOF'
set -euo pipefail

printf '%s\n' "$*" >> "$MOCK_FORGE_LOG"

if [[ -n "${MOCK_FORGE_FAIL:-}" ]]; then
  echo "mock forge: forced failure" >&2
  exit 1
fi

if [[ "$1 $2" == "pr find-by-head" ]]; then
  shift 2
  state_all=false
  for arg in "$@"; do
    if [[ "$arg" == "all" ]]; then
      state_all=true
    fi
  done
  if $state_all; then
    if [[ -n "${MOCK_FORGE_ALL_STATE:-}" ]]; then
      printf '%s\t%s\n' "${MOCK_FORGE_ALL_NUMBER:-99}" "$MOCK_FORGE_ALL_STATE"
    fi
    exit 0
  fi
  if [[ "${MOCK_FORGE_MODE:-}" == "existing" ]]; then
    printf '42\n'
  fi
  exit 0
fi

if [[ "$1 $2" == "pr create" ]]; then
  body_file=""
  previous=""
  for arg in "$@"; do
    if [[ "$previous" == "-F" || "$previous" == "--body-file" ]]; then
      body_file="$arg"
      break
    fi
    previous="$arg"
  done
  [[ -n "$body_file" ]] || {
    echo "mock forge: missing body file" >&2
    exit 1
  }
  cat "$body_file" >> "$MOCK_FORGE_BODY_LOG"
  printf 'PR created: https://forge.example/acme/repo/pulls/1\n'
  exit 0
fi

echo "mock forge: unexpected args: $*" >&2
exit 1
EOF
  } > "$bin/forge"
  chmod +x "$bin/forge"
  printf '%s\n' "$bin"
}

# Modes: push-log (default) records every push; block-handoff makes the handoff file (never its
# directory, which would also break rollback's rm -rf) read-only so begin's write fails.
make_mock_git_path() {
  local name="$1" mode="${2:-push-log}"
  local bin="$TMP/mock-git-bin-$name"
  mkdir -p "$bin"
  {
    printf '#!%s\n' "$(command -v bash)"
    printf 'set -euo pipefail\n'
    printf 'MOCK_GIT_MODE=%q\n' "$mode"
    cat <<'EOF'
checkout_b=false
repo=""
argc=$#
i=1
while (( i <= argc )); do
  cur="${!i}"
  j=$((i + 1))
  nxt=""
  if (( j <= argc )); then
    nxt="${!j}"
  fi
  case "$cur" in
    -C) repo="$nxt" ;;
    checkout)
      if [[ "$nxt" == "-b" ]]; then
        checkout_b=true
      fi
      ;;
    push)
      if [[ "$MOCK_GIT_MODE" == "push-log" ]]; then
        printf '%s\n' "$*" >> "$GIT_PUSH_LOG"
      fi
      ;;
  esac
  i=$((i + 1))
done

if [[ "$checkout_b" == true && "$MOCK_GIT_MODE" == "block-handoff" ]]; then
  "$REAL_GIT" "$@"
  git_dir=$("$REAL_GIT" -C "$repo" rev-parse --path-format=absolute --git-dir)
  : > "$git_dir/allod-change-branch"
  chmod a-w "$git_dir/allod-change-branch"
  exit 0
fi

exec "$REAL_GIT" "$@"
EOF
  } > "$bin/git"
  chmod +x "$bin/git"
  printf '%s:%s\n' "$bin" "$PATH"
}

# Logs every invocation's argv, order included, before forwarding to the real git.
make_logging_git() {
  local log="$1"
  local bin="$TMP/logging-git-bin"
  mkdir -p "$bin"
  {
    printf '#!%s\n' "$(command -v bash)"
    printf 'REAL_GIT=%q\n' "$REAL_GIT"
    printf 'LOG=%q\n' "$log"
    cat <<'EOF'
printf '%s\n' "$*" >> "$LOG"
exec "$REAL_GIT" "$@"
EOF
  } > "$bin/git"
  chmod +x "$bin/git"
  printf '%s\n' "$bin"
}

# Plants a core.hooksPath directory in the clone's own .git/config, with one
# hook under it that touches marker: proof that no outside command ran any
# git command in the clone at all, which would otherwise be the one way the
# hook could fire (no sandboxed worker is running inside it either).
plant_never_run_hook() {
  local path="$1" marker="$2" dir="$TMP/never-run-hooks-$RANDOM"
  mkdir -p "$dir"
  printf '#!/bin/sh\ntouch "%s"\n' "$marker" > "$dir/post-checkout"
  chmod +x "$dir/post-checkout"
  git -C "$path" config core.hooksPath "$dir"
}

# begin tests
# Tests needing $HOME/changes absent run first: the first successful 'begin -d' creates it.

repo="$HOME/work/begin-open"
init_repo "$repo" master
capture "$ALLOD" change begin "$repo"
assert_status 0 "begin without -d succeeds for a non-protected repo"
assert_equal "$CAPTURE_OUTPUT" "$repo" "begin without -d prints the shared checkout path"
assert_equal "$(worktree_count "$repo")" "1" "begin without -d creates no worktree"
assert_equal "$(git -C "$repo" branch --list 'agent/*')" "" "begin without -d creates no agent branch"
[[ ! -e "$HOME/changes" ]] &&
  pass "begin without -d does not create the changes directory" ||
  fail "begin without -d does not create the changes directory" "exists: $HOME/changes"
printf 'in place\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "in-place commit" -f tracked.txt
assert_status 0 "record commits in place after begin without -d"
assert_equal "$(git -C "$repo" log -1 --format=%s)" "in-place commit" \
  "in-place record uses the given message"
assert_equal "$(git -C "$repo" rev-list --count origin/master..HEAD)" "0" \
  "in-place record pushes the commit to origin"

outside="$TMP/outside-repo"
init_repo_no_remote "$outside" master
capture "$ALLOD" change begin "$outside"
assert_status 0 "begin without -d succeeds for an outside-HOME repo with no origin"
assert_equal "$CAPTURE_OUTPUT" "$outside" "outside-HOME repo is treated as unprotected"

# resolveClone's workspace walk only ever looks under $WORK_DIR, so a clone
# issued from outside it would get a ledger entry no command could ever find.
capture "$ALLOD" change begin -d "${RUN_ID}-outside" "$outside"
assert_status 1 "begin -d refuses a repository outside the work directory"
assert_contains "$CAPTURE_OUTPUT" "work directory" "the outside-work-dir refusal is actionable"
assert_equal "$(changes_entries_for "${RUN_ID}-outside")" "0" \
  "the refused begin -d created no clone directory"
capture "$ALLOD" change begin --resume "${RUN_ID}-outside" "$outside"
assert_status 1 "begin --resume also refuses a repository outside the work directory"

repo="$HOME/work/begin-protected-no-desc"
init_repo "$repo" main
protect_repo "$repo" main
capture "$ALLOD" change begin "$repo"
assert_status 1 "begin requires -d on protected repo"
assert_contains "$CAPTURE_OUTPUT" "requires -d" "begin missing -d explains failure"

repo="$HOME/work/begin-no-origin"
init_repo_no_remote "$repo" master
capture "$ALLOD" change begin -d "${RUN_ID}-no-origin" "$repo"
assert_status 1 "begin -d refuses a repo with no origin remote"
assert_contains "$CAPTURE_OUTPUT" "no 'origin' remote" "begin -d names the missing remote"
assert_not_contains "$CAPTURE_OUTPUT" "could not check origin for branch" \
  "begin -d fails on the missing remote rather than on ls-remote"

repo="$HOME/work/begin-no-origin-head"
init_repo_no_origin_head "$repo" master
capture "$ALLOD" change begin -d "${RUN_ID}-no-head" "$repo"
assert_status 1 "begin -d refuses an unprotected repo with no origin/HEAD"
assert_contains "$CAPTURE_OUTPUT" "remote set-head" "begin -d names the origin/HEAD repair"

repo="$HOME/work/begin-missing-base"
init_repo "$repo" master
protect_repo "$repo" no-such-branch
desc="${RUN_ID}-missing-base"
capture "$ALLOD" change begin -d "$desc" "$repo"
assert_status 1 "begin -d refuses a base branch that is missing on origin"
[[ ! -e "$HOME/changes" ]] &&
  pass "begin -d creates no worktree directory when the base is missing" ||
  fail "begin -d creates no worktree directory when the base is missing" "exists: $HOME/changes"
assert_equal "$(git -C "$repo" branch --list "agent/$desc")" "" \
  "begin -d creates no branch when the base is missing"

repo="$HOME/work/begin-invalid-open"
init_repo "$repo" master
for invalid in "has space" "has/slash" "" ".foo" "foo..bar" "foo.lock"; do
  capture "$ALLOD" change begin -d "$invalid" "$repo"
  assert_status 1 "begin rejects invalid description '${invalid:-<empty>}' in an unprotected repo"
done

repo="$HOME/work/begin-invalid"
init_repo "$repo" master
protect_repo "$repo" master
for invalid in "has space" "has/slash" "" ".foo" "foo..bar" "foo.lock"; do
  capture "$ALLOD" change begin -d "$invalid" "$repo"
  assert_status 1 "begin rejects invalid description '${invalid:-<empty>}'"
done

# The flip: -d isolates whether or not the repo is protected.

repo="$HOME/work/begin-isolated"
init_repo "$repo" master
desc="${RUN_ID}-isolated"
path=$(begin_clone "$desc" "$repo")
case "$path" in
  "$HOME/changes/work-begin-isolated-${desc}-"??????)
    pass "begin -d sites an unprotected repo's clone under ~/changes" ;;
  *) fail "begin -d sites an unprotected repo's clone under ~/changes" "actual path: $path" ;;
esac
[[ -d "$path" ]] || fail "begin -d creates a clone for an unprotected repo" "missing clone: $path"
pass "begin -d creates a clone for an unprotected repo"
assert_equal "$(git -C "$path" branch --show-current)" "agent/$desc" \
  "begin -d creates the agent branch for an unprotected repo"
git -C "$path" merge-base --is-ancestor origin/master HEAD &&
  pass "begin -d starts an unprotected clone from the default branch" ||
  fail "begin -d starts an unprotected clone from the default branch"
assert_equal "$(git -C "$repo" branch --show-current)" "master" \
  "begin -d leaves the shared checkout on its default branch"
assert_equal "$(git -C "$repo" status --porcelain)" "" "begin -d leaves the shared checkout clean"

git_dir=$(git -C "$path" rev-parse --path-format=absolute --git-dir)
common_dir=$(git -C "$path" rev-parse --path-format=absolute --git-common-dir)
[[ "$git_dir" == "$common_dir" ]] &&
  pass "begin -d creates a standalone clone, not a linked worktree" ||
  fail "begin -d creates a standalone clone, not a linked worktree" "git-dir: $git_dir, common-dir: $common_dir"
[[ ! -e "$git_dir/objects/info/alternates" ]] &&
  pass "begin -d never makes an alternates clone" ||
  fail "begin -d never makes an alternates clone" "found: $git_dir/objects/info/alternates"
repo_git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
sample_obj=$(git -C "$repo" rev-parse HEAD)
sample_rel="${sample_obj:0:2}/${sample_obj:2}"
if [[ -f "$repo_git_dir/objects/$sample_rel" ]]; then
  [[ "$(stat -c '%i' "$git_dir/objects/$sample_rel")" != "$(stat -c '%i' "$repo_git_dir/objects/$sample_rel")" ]] &&
    pass "begin -d copies the parent's objects rather than hardlinking them" ||
    fail "begin -d copies the parent's objects rather than hardlinking them" \
      "clone object has the same inode as the parent's own object store"
fi
# A hard-linked object lets the worker chmod/overwrite through the parent's inode; find -links +1
# generalizes the single-object check over the whole object store.
linked=$(find "$git_dir/objects" -type f -links +1)
[[ -z "$linked" ]] &&
  pass "no file under the clone's .git/objects has a link count greater than 1" ||
  fail "no file under the clone's .git/objects has a link count greater than 1" "$linked"
[[ -f "$git_dir/allod-change-branch" ]] &&
  pass "begin -d writes the branch handoff file" ||
  fail "begin -d writes the branch handoff file" "missing: $git_dir/allod-change-branch"
assert_equal "$(cat "$git_dir/allod-change-branch")" "agent/$desc" \
  "handoff file names the branch begin created"

repo="$HOME/work/begin-protected"
init_repo "$repo" main
protect_repo "$repo" main
desc="${RUN_ID}-begin"
path=$(begin_clone "$desc" "$repo")
[[ -d "$path" ]] || fail "begin creates protected worktree" "missing worktree: $path"
pass "begin creates protected worktree"
assert_equal "$(git -C "$path" branch --show-current)" "agent/$desc" \
  "begin creates agent branch"
git -C "$path" merge-base --is-ancestor origin/main HEAD &&
  pass "begin starts from configured protected branch" ||
  fail "begin starts from configured protected branch"

repo="$HOME/work/begin-twice"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-twice"
path=$(begin_clone "$desc" "$repo")
capture "$ALLOD" change begin -d "$desc" "$repo"
assert_status 5 "begin rejects an existing local agent branch"
assert_contains "$CAPTURE_OUTPUT" "already exists locally" "begin local branch failure is actionable"

repo="$HOME/work/begin-twice-open"
init_repo "$repo" master
desc="${RUN_ID}-twice-open"
path=$(begin_clone "$desc" "$repo")
capture "$ALLOD" change begin -d "$desc" "$repo"
assert_status 5 "begin rejects an existing local agent branch in an unprotected repo"
assert_contains "$CAPTURE_OUTPUT" "already exists locally" \
  "begin local branch failure is actionable for an unprotected repo"

repo="$HOME/work/begin-remote-exists"
init_repo "$repo" master
protect_repo "$repo" master
git -C "$repo" push -q origin HEAD:refs/heads/agent/"${RUN_ID}-remote"
capture "$ALLOD" change begin -d "${RUN_ID}-remote" "$repo"
assert_status 5 "begin rejects an existing remote agent branch"
assert_contains "$CAPTURE_OUTPUT" "already exists on origin" "begin remote branch failure is actionable"

repo="$HOME/work/begin-remote-exists-open"
init_repo "$repo" master
git -C "$repo" push -q origin HEAD:refs/heads/agent/"${RUN_ID}-remote-open"
capture "$ALLOD" change begin -d "${RUN_ID}-remote-open" "$repo"
assert_status 5 "begin rejects an existing remote agent branch in an unprotected repo"
assert_contains "$CAPTURE_OUTPUT" "already exists on origin" \
  "begin remote branch failure is actionable for an unprotected repo"

# --resume: the remote agent branch is the start point, not a collision.

repo="$HOME/work/begin-resume"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-resume"
# Push the agent branch from a throwaway clone so the shared checkout never
# holds a local agent/* ref, matching a branch another session started.
seed="$TMP/seed-resume"
git clone -q "$(git -C "$repo" remote get-url origin)" "$seed"
git -C "$seed" config user.name "Test User"
git -C "$seed" config user.email "test@example.invalid"
git -C "$seed" checkout -q -b "agent/$desc"
printf 'resumed\n' > "$seed/tracked.txt"
git -C "$seed" commit -qam "first commit on the agent branch"
git -C "$seed" push -q origin "agent/$desc"
seed_sha=$(git -C "$seed" rev-parse HEAD)
capture "$ALLOD" change begin --resume "$desc" "$repo"
assert_status 0 "begin --resume picks up an existing remote agent branch"
path="$CAPTURE_OUTPUT"
WORKTREES+=("$path")
case "$path" in
  "$HOME/changes/work-begin-resume-${desc}-"??????)
    pass "begin --resume sites the worktree under ~/changes like -d" ;;
  *) fail "begin --resume sites the worktree under ~/changes like -d" "actual path: $path" ;;
esac
assert_equal "$(git -C "$path" branch --show-current)" "agent/$desc" \
  "begin --resume checks out the agent branch"
assert_equal "$(git -C "$path" rev-parse HEAD)" "$seed_sha" \
  "begin --resume starts from the remote agent branch, not the default branch"
assert_equal "$(git -C "$path" rev-parse --abbrev-ref '@{upstream}')" "origin/agent/$desc" \
  "begin --resume tracks the remote agent branch"
git_dir=$(git -C "$path" rev-parse --path-format=absolute --git-dir)
assert_equal "$(cat "$git_dir/allod-change-branch")" "agent/$desc" \
  "begin --resume writes the branch handoff file"
assert_equal "$(git -C "$repo" branch --show-current)" "master" \
  "begin --resume leaves the shared checkout on its default branch"
printf 'resumed twice\n' > "$path/tracked.txt"
export ALLOD_CHANGE_SANDBOX=1
record_in_repo "$path" -m "second commit on the resumed branch" >/dev/null
unset ALLOD_CHANGE_SANDBOX
assert_equal "$(git -C "$repo" ls-remote origin "refs/heads/agent/$desc" | cut -f1)" \
  "$(git -C "$path" rev-parse HEAD)" \
  "record pushes a resumed branch additively"

repo="$HOME/work/begin-resume-missing"
init_repo "$repo" master
capture "$ALLOD" change begin --resume "${RUN_ID}-resume-missing" "$repo"
assert_status 5 "begin --resume refuses a branch that is missing on origin"
assert_contains "$CAPTURE_OUTPUT" "does not exist on origin" "begin --resume names the missing remote branch"
assert_contains "$CAPTURE_OUTPUT" "use -d" "begin --resume points at -d for a new change"
assert_equal "$(changes_entries_for "${RUN_ID}-resume-missing")" "0" \
  "begin --resume creates no worktree when the branch is missing on origin"

repo="$HOME/work/begin-resume-local"
init_repo "$repo" master
desc="${RUN_ID}-resume-local"
path=$(begin_clone "$desc" "$repo")
git -C "$path" push -q origin "agent/$desc"
capture "$ALLOD" change begin --resume "$desc" "$repo"
assert_status 5 "begin --resume refuses a branch already checked out locally"
assert_contains "$CAPTURE_OUTPUT" "already exists locally" \
  "begin --resume names the local branch collision"
assert_equal "$(changes_entries_for "$desc")" "1" "begin --resume adds no second clone for a branch its ledger already lists"

repo="$HOME/work/begin-resume-both"
init_repo "$repo" master
capture "$ALLOD" change begin -d "${RUN_ID}-both" --resume "${RUN_ID}-both" "$repo"
assert_status 1 "begin refuses -d and --resume together"
assert_contains "$CAPTURE_OUTPUT" "not both" "begin names the -d/--resume conflict"
capture "$ALLOD" change begin --resume "${RUN_ID}-both" -d "${RUN_ID}-both" "$repo"
assert_status 1 "begin refuses --resume and -d together in either order"
capture "$ALLOD" change begin --resume
assert_status 1 "begin --resume requires a description"

repo="$HOME/work/allod/tools"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-nested"
path=$(begin_clone "$desc" "$repo")
case "$path" in
  "$HOME/changes/work-allod-tools-${desc}-"??????)
    pass "begin sanitizes nested repo slug for the worktree path" ;;
  *) fail "begin sanitizes nested repo slug for the worktree path" "actual path: $path" ;;
esac

# begin never touches the parent until its final ledger write, so any earlier failure is just
# rm -rf on the half-made clone.

export REAL_GIT

repo="$HOME/work/begin-handoff-blocked"
init_repo "$repo" master
desc="${RUN_ID}-handoff-blocked"
mock_git_path=$(make_mock_git_path handoff-blocked block-handoff)
capture_with_path "$mock_git_path" "$ALLOD" change begin -d "$desc" "$repo"
[[ "$CAPTURE_STATUS" -ne 0 ]] &&
  pass "begin fails when the handoff write fails" ||
  fail "begin fails when the handoff write fails" "$CAPTURE_OUTPUT"
assert_equal "$(changes_entries_for "$desc")" "0" \
  "begin removes its clone directory after a failed handoff write"
git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
[[ ! -e "$git_dir/allod-changes/$desc" ]] &&
  pass "begin rolls back its ledger claim after a failed handoff write" ||
  fail "begin rolls back its ledger claim after a failed handoff write" "$(cat "$git_dir/allod-changes/$desc")"

repo="$HOME/work/begin-ledger-blocked"
init_repo "$repo" master
desc="${RUN_ID}-ledger-blocked"
git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
: > "$git_dir/allod-changes"
capture "$ALLOD" change begin -d "$desc" "$repo"
[[ "$CAPTURE_STATUS" -ne 0 ]] &&
  pass "begin fails when the parent's ledger directory cannot be created" ||
  fail "begin fails when the parent's ledger directory cannot be created" "$CAPTURE_OUTPUT"
assert_equal "$(changes_entries_for "$desc")" "0" \
  "begin removes its clone directory after a failed ledger write"

# claimChangeLedgerEntry's O_EXCL create is the real race-free gate (go test covers the atomicity);
# pre-planting the entry here stands in for losing that race end to end.
repo="$HOME/work/begin-ledger-claim-race"
init_repo "$repo" master
desc="${RUN_ID}-claim-race"
git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
mkdir -p "$git_dir/allod-changes"
printf 'some/other/winning-clone-path\nagent/%s\n' "$desc" > "$git_dir/allod-changes/$desc"
capture "$ALLOD" change begin -d "$desc" "$repo"
assert_status 5 "begin -d refuses a description an existing ledger claim already holds"
assert_equal "$(changes_entries_for "$desc")" "0" \
  "the losing begin -d left no clone directory of its own"
assert_equal "$(cat "$git_dir/allod-changes/$desc")" "$(printf 'some/other/winning-clone-path\nagent/%s' "$desc")" \
  "the losing begin -d left the winner's ledger entry untouched"

# list tests

repo="$HOME/work/list-empty"
init_repo "$repo" master
capture "$ALLOD" change list "$repo"
assert_status 0 "list exits 0 for a repo with no clones or worktrees"
assert_equal "$CAPTURE_OUTPUT" "" "list prints nothing for a repo with no clones or worktrees"

repo="$HOME/work/list-rows"
init_repo "$repo" master
desc="${RUN_ID}-rows"
path=$(begin_clone "$desc" "$repo")
forge_log="$TMP/forge-list-rows.log"
export MOCK_FORGE_LOG="$forge_log" MOCK_FORGE_ALL_STATE="" MOCK_FORGE_ALL_NUMBER=""
forge_bin=$(make_mock_forge list-rows)
capture_with_path "$forge_bin:$PATH" "$ALLOD" change list "$repo"
assert_status 0 "list exits 0 with a clone present"
assert_equal "$(printf '%s' "$CAPTURE_OUTPUT" | grep -c .)" "1" "list prints one row per ledgered clone"
assert_equal "$CAPTURE_OUTPUT" "$(printf 'list-rows\t%s\tagent/%s\tunsubmitted' "$path" "$desc")" \
  "list row carries repo, path, branch, and state: a fresh clone with no pull request yet is unsubmitted, read from the forge, never from the clone's own disk"

# The ledger is the whole mechanism: a two-line file under the parent's own
# .git, named for the description, gone once 'cleanup' runs.
git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
ledger_file="$git_dir/allod-changes/$desc"
[[ -f "$ledger_file" ]] &&
  pass "begin -d writes a ledger entry in the parent" ||
  fail "begin -d writes a ledger entry in the parent" "missing: $ledger_file"
assert_equal "$(sed -n '1p' "$ledger_file")" "$path" "the ledger entry's first line is the clone's path"
assert_equal "$(sed -n '2p' "$ledger_file")" "agent/$desc" "the ledger entry's second line is the clone's branch"
capture_with_path "$forge_bin:$PATH" "$ALLOD" change cleanup "$path"
assert_status 1 "cleanup refuses a clone whose branch has no pull request yet"
assert_contains "$CAPTURE_OUTPUT" "no pull request" "the no-pull-request refusal is actionable"
assert_contains "$CAPTURE_OUTPUT" "allod change run" "the no-pull-request refusal names the worker's own command"
rm -rf "$path"
rm -f "$ledger_file"

other_repo="$HOME/work/list-other"
init_repo "$other_repo" master
other_desc="${RUN_ID}-other"
desc="${RUN_ID}-rows2"
path=$(begin_clone "$desc" "$repo")
other_path=$(begin_clone "$other_desc" "$other_repo")
capture "$ALLOD" change list
assert_status 0 "list exits 0 with no argument"
assert_contains "$CAPTURE_OUTPUT" "$path" "list with no argument walks the workspace"
assert_contains "$CAPTURE_OUTPUT" "$other_path" "list with no argument covers every repo"
capture "$ALLOD" change list "$other_repo"
assert_contains "$CAPTURE_OUTPUT" "$other_path" "list <repo-path> lists that repo's clones"
assert_not_contains "$CAPTURE_OUTPUT" "$path" "list <repo-path> scopes to that repo only"

# Clone states come from the forge: one case per state, each checked
# against what cleanup actually does, driven by the mock forge's '-s all'
# answer.

list_with_forge() {
  local repo="$1" forge_bin="$2"
  capture_with_path "$forge_bin:$PATH" "$ALLOD" change list "$repo"
}

cleanup_with_forge() {
  local path="$1" forge_bin="$2"
  capture_with_path "$forge_bin:$PATH" "$ALLOD" change cleanup "$path"
}

repo="$HOME/work/list-states"
init_repo "$repo" master

forge_log="$TMP/forge-list-states.log"
export MOCK_FORGE_LOG="$forge_log"
forge_bin=$(make_mock_forge list-states)

desc="${RUN_ID}-state-open"
path=$(begin_clone "$desc" "$repo")
MOCK_FORGE_ALL_NUMBER=7 MOCK_FORGE_ALL_STATE=open list_with_forge "$repo" "$forge_bin"
assert_contains "$CAPTURE_OUTPUT" $'\topen' "list reports open for a branch with an open pull request"
MOCK_FORGE_ALL_NUMBER=7 MOCK_FORGE_ALL_STATE=open cleanup_with_forge "$path" "$forge_bin"
assert_status 1 "cleanup refuses a clone whose pull request is still open"
assert_contains "$CAPTURE_OUTPUT" "still open" "the open-pull-request refusal is actionable"
[[ -d "$path" ]] &&
  pass "cleanup's open-pull-request refusal preserves the clone" ||
  fail "cleanup's open-pull-request refusal preserves the clone" "missing: $path"

desc="${RUN_ID}-state-merged"
path=$(begin_clone "$desc" "$repo")
plant_never_run_hook "$path" "$TMP/never-run-merged-${RUN_ID}"
# Model #297's landing shape before the forge answers: the branch is
# rebase-merged under a new hash and then deleted. The recorded PR head and
# merged state remain authoritative; cleanup must not inspect the clone.
printf 'rebase-merged\n' > "$path/tracked.txt"
git -C "$path" commit -qam "rebase-merged work"
git -C "$path" push -q origin "HEAD:refs/heads/agent/$desc"
source_sha=$(git -C "$path" rev-parse HEAD)
git -C "$repo" fetch -q origin "agent/$desc"
git -C "$repo" cherry-pick "$source_sha" >/dev/null
git -C "$repo" commit -q --amend -m 'rebase-merged work (forge)'
git -C "$repo" push -q origin master
git -C "$repo" push -q origin --delete "agent/$desc"
[[ "$(git -C "$path" rev-parse HEAD)" != "$(git -C "$repo" rev-parse HEAD)" ]] &&
  pass "the merged-PR fixture lands the patch under a new hash before deleting its branch" ||
  fail "the merged-PR fixture lands the patch under a new hash before deleting its branch"
git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
ledger_file="$git_dir/allod-changes/$desc"
MOCK_FORGE_ALL_NUMBER=8 MOCK_FORGE_ALL_STATE=merged list_with_forge "$repo" "$forge_bin"
assert_contains "$CAPTURE_OUTPUT" $'\tmerged' "list reports merged after a patch-equivalent landing deletes the pull request's branch"
MOCK_FORGE_ALL_NUMBER=8 MOCK_FORGE_ALL_STATE=merged cleanup_with_forge "$path" "$forge_bin"
assert_status 0 "cleanup removes the branch-deleted clone whose pull request is merged"
[[ ! -d "$path" ]] &&
  pass "cleanup deletes the clone directory once merged" ||
  fail "cleanup deletes the clone directory once merged" "still exists: $path"
[[ ! -e "$ledger_file" ]] &&
  pass "cleanup drops the ledger entry once merged" ||
  fail "cleanup drops the ledger entry once merged" "still exists: $ledger_file"
[[ ! -e "$TMP/never-run-merged-${RUN_ID}" ]] &&
  pass "cleanup never ran the clone's own core.hooksPath hook" ||
  fail "cleanup never ran the clone's own core.hooksPath hook" "marker present"

desc="${RUN_ID}-state-closed"
path=$(begin_clone "$desc" "$repo")
plant_never_run_hook "$path" "$TMP/never-run-closed-${RUN_ID}"
MOCK_FORGE_ALL_NUMBER=9 MOCK_FORGE_ALL_STATE=closed list_with_forge "$repo" "$forge_bin"
assert_contains "$CAPTURE_OUTPUT" $'\tclosed' "list reports closed for a branch with a closed, unmerged pull request"
MOCK_FORGE_ALL_NUMBER=9 MOCK_FORGE_ALL_STATE=closed cleanup_with_forge "$path" "$forge_bin"
assert_status 0 "cleanup removes a clone whose pull request is closed without merging"
[[ ! -d "$path" ]] &&
  pass "cleanup deletes the clone directory once closed" ||
  fail "cleanup deletes the clone directory once closed" "still exists: $path"
[[ ! -e "$TMP/never-run-closed-${RUN_ID}" ]] &&
  pass "cleanup never ran the closed clone's own core.hooksPath hook either" ||
  fail "cleanup never ran the closed clone's own core.hooksPath hook either" "marker present"

desc="${RUN_ID}-state-forge-fails"
path=$(begin_clone "$desc" "$repo")
MOCK_FORGE_FAIL=1 list_with_forge "$repo" "$forge_bin"
assert_contains "$CAPTURE_OUTPUT" $'\tunknown' "list reports unknown when the forge does not answer"
MOCK_FORGE_FAIL=1 cleanup_with_forge "$path" "$forge_bin"
assert_status 1 "cleanup refuses when the forge does not answer"
assert_contains "$CAPTURE_OUTPUT" "could not ask the forge" "the forge-failure refusal names it"
unset MOCK_FORGE_FAIL
rm -rf "$path"
rm -f "$git_dir/allod-changes/$desc"

# A deleted clone is 'missing', never 'prunable': no worktree-prune side channel exists, so cleanup
# drops the ledger entry itself; the ledger match must happen before resolveGitRepo would refuse.
# 'missing' is checked before the forge is ever asked anything, so no mock forge is needed here.
repo="$HOME/work/list-missing"
init_repo "$repo" master
desc="${RUN_ID}-missing"
path=$(begin_clone "$desc" "$repo")
rm -rf "$path"
assert_equal "$(list_state_for "$repo" "$path")" "missing" "list reports missing for a deleted clone"
git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
ledger_file="$git_dir/allod-changes/$desc"
capture "$ALLOD" change cleanup "$path"
assert_status 0 "cleanup on a missing clone's path drops its ledger entry instead of refusing"
assert_contains "$CAPTURE_OUTPUT" "is missing" "cleanup on a missing clone says so"
[[ ! -e "$ledger_file" ]] &&
  pass "cleanup drops the ledger entry of a missing clone" ||
  fail "cleanup drops the ledger entry of a missing clone" "still exists: $ledger_file"

# A ledgered path that exists as a regular file is not missing; list still
# asks the forge for the branch's pull-request state.
repo="$HOME/work/list-not-a-directory"
init_repo "$repo" master
desc="${RUN_ID}-list-not-a-directory"
list_file_git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
mkdir -p "$list_file_git_dir/allod-changes"
list_file_path="$HOME/changes/${RUN_ID}-list-not-a-directory-file"
touch "$list_file_path"
printf '%s\nagent/%s\n' "$list_file_path" "$desc" > "$list_file_git_dir/allod-changes/$desc"
MOCK_FORGE_ALL_NUMBER=10 MOCK_FORGE_ALL_STATE=open list_with_forge "$repo" "$forge_bin"
assert_contains "$CAPTURE_OUTPUT" "${list_file_path}"$'\t'"agent/$desc"$'\topen' \
  "list asks the forge about a ledgered path that exists as a regular file"
rm -f "$list_file_path" "$list_file_git_dir/allod-changes/$desc"

# This fixture has no origin at all, which 'begin' refuses outright, so it is built as a raw
# linked worktree instead; legacy worktree states are untouched.
repo="$HOME/work/list-state-unknown"
init_repo_no_remote "$repo" master
desc="${RUN_ID}-state-unknown"
path="$HOME/changes/list-state-unknown-${desc}"
git -C "$repo" worktree add -q -b "agent/$desc" "$path"
WORKTREES+=("$path")
printf 'local only\n' > "$path/tracked.txt"
git -C "$path" commit -qam "local-only commit"
assert_list_matches_cleanup "$repo" "$path" unknown "a worktree with no remote comparison base"
[[ -d "$path" ]] && pass "unknown cleanup refusal preserves the worktree" ||
  fail "unknown cleanup refusal preserves the worktree" "missing: $path"
[[ -n "$(git -C "$repo" branch --list "agent/$desc")" ]] &&
  pass "unknown cleanup refusal preserves the branch" ||
  fail "unknown cleanup refusal preserves the branch"
assert_contains "$CAPTURE_OUTPUT" "no origin remote" \
  "unknown cleanup refusal names why comparison failed"

# legacy linked worktree tests (drain window)
# 'begin' makes no more of these; 'list'/'record'/'cleanup' must still handle one made before.

repo="$HOME/work/legacy-list"
init_repo "$repo" master

desc="${RUN_ID}-legacy-clean"
path=$(make_legacy_worktree "$desc" "$repo" master)
assert_list_matches_cleanup "$repo" "$path" clean "a fresh legacy worktree"

# Legacy linked worktrees still use Git history. Once the source branch has
# disappeared, patch equivalence against the freshly fetched default branch
# recognizes a rebase-style landing instead of requiring the old hash.
desc="${RUN_ID}-legacy-rebase-landed"
path=$(make_legacy_worktree "$desc" "$repo" master)
printf 'legacy rebase landing\n' > "$path/tracked.txt"
git -C "$path" commit -qam "legacy rebase landing"
git -C "$path" push -q origin "HEAD:refs/heads/agent/$desc"
source_sha=$(git -C "$path" rev-parse HEAD)
git -C "$repo" fetch -q origin "agent/$desc"
git -C "$repo" cherry-pick "$source_sha" >/dev/null
git -C "$repo" commit -q --amend -m 'legacy rebase landing (forge)'
git -C "$repo" push -q origin master
git -C "$repo" push -q origin --delete "agent/$desc"
git -C "$repo" fetch -q --prune origin
assert_list_matches_cleanup "$repo" "$path" clean \
  "a legacy worktree whose deleted branch landed patch-equivalently"

desc="${RUN_ID}-legacy-locked"
path=$(make_legacy_worktree "$desc" "$repo" master)
git -C "$repo" worktree lock "$path"
assert_list_matches_cleanup "$repo" "$path" locked "a locked legacy worktree"
git -C "$repo" worktree unlock "$path"
assert_list_matches_cleanup "$repo" "$path" clean "the same legacy worktree once unlocked"

desc="${RUN_ID}-legacy-prunable"
path=$(make_legacy_worktree "$desc" "$repo" master)
rm -rf "$path"
worktrees_before=$(worktree_count "$repo")
assert_equal "$(list_state_for "$repo" "$path")" "prunable" \
  "list reports prunable for a legacy worktree whose directory is gone"
assert_equal "$(worktree_count "$repo")" "$worktrees_before" \
  "list does not prune the worktrees it reports"
capture "$ALLOD" change cleanup "$path"
[[ "$CAPTURE_STATUS" -ne 0 ]] &&
  pass "cleanup refuses a prunable legacy worktree path" ||
  fail "cleanup refuses a prunable legacy worktree path" "$CAPTURE_OUTPUT"

desc="${RUN_ID}-legacy-detached"
path=$(make_legacy_worktree "$desc" "$repo" master)
git -C "$path" checkout -q --detach
assert_contains "$("$ALLOD" change list "$repo")" "(detached)" \
  "list names a detached legacy worktree in the branch column"
assert_list_matches_cleanup "$repo" "$path" detached "a detached legacy worktree"

desc="${RUN_ID}-legacy-locked-dirty"
path=$(make_legacy_worktree "$desc" "$repo" master)
printf 'dirty\n' > "$path/tracked.txt"
git -C "$repo" worktree lock "$path"
assert_equal "$(list_state_for "$repo" "$path")" "locked" \
  "list reports locked ahead of dirty for a legacy worktree"

desc="${RUN_ID}-legacy-locked-gone"
path=$(make_legacy_worktree "$desc" "$repo" master)
git -C "$repo" worktree lock "$path"
rm -rf "$path"
assert_equal "$(list_state_for "$repo" "$path")" "locked" \
  "list reports locked, not prunable, for a locked legacy worktree whose directory is gone"

# record from inside a legacy worktree, including the protected-branch
# refusal, must keep working unchanged.

repo="$HOME/work/legacy-record"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-legacy-record"
path=$(make_legacy_worktree "$desc" "$repo" master)
printf 'changed\n' > "$path/tracked.txt"
capture record_in_repo "$path" -m "record from a legacy worktree"
assert_status 0 "record from inside a legacy worktree commits on agent/*"
remote_has_branch "$repo" "agent/$desc" &&
  pass "record from a legacy worktree pushes the agent branch" ||
  fail "record from a legacy worktree pushes the agent branch"

git -C "$repo" push -q origin master:release
git -C "$repo" fetch -q origin
protect_repo "$repo" release
release_path="$HOME/changes/legacy-release-${desc}"
git -C "$repo" worktree add -q -b release "$release_path" origin/release
WORKTREES+=("$release_path")
printf 'edit\n' > "$release_path/tracked.txt"
capture record_in_repo "$release_path" -m "must be refused"
assert_status 2 "record from a legacy worktree checked out on a protected branch still refuses it"

# cleanup of a legacy worktree, including deleting its branch in the parent
# (there is no branch to delete for a clone), must keep working unchanged.

repo="$HOME/work/legacy-cleanup"
init_repo "$repo" master

desc="${RUN_ID}-legacy-cleanup-dirty"
path=$(make_legacy_worktree "$desc" "$repo" master)
printf 'dirty\n' > "$path/tracked.txt"
capture "$ALLOD" change cleanup "$path"
[[ "$CAPTURE_STATUS" -ne 0 ]] || fail "cleanup refuses a dirty legacy worktree" "$CAPTURE_OUTPUT"
assert_contains "$CAPTURE_OUTPUT" "dirty" "cleanup dirty legacy worktree failure is actionable"

desc="${RUN_ID}-legacy-cleanup-unpushed"
path=$(make_legacy_worktree "$desc" "$repo" master)
printf 'unpushed\n' > "$path/tracked.txt"
git -C "$path" commit -qam "unpushed"
capture "$ALLOD" change cleanup "$path"
[[ "$CAPTURE_STATUS" -ne 0 ]] || fail "cleanup refuses an unpushed legacy worktree" "$CAPTURE_OUTPUT"
assert_contains "$CAPTURE_OUTPUT" "unpushed" "cleanup unpushed legacy worktree failure is actionable"

desc="${RUN_ID}-legacy-cleanup-clean"
path=$(make_legacy_worktree "$desc" "$repo" master)
capture "$ALLOD" change cleanup "$path"
assert_status 0 "cleanup removes a clean legacy worktree"
[[ ! -d "$path" ]] &&
  pass "cleanup deletes the legacy worktree directory" ||
  fail "cleanup deletes the legacy worktree directory" "still exists: $path"
[[ -z "$(git -C "$repo" branch --list "agent/$desc")" ]] &&
  pass "cleanup deletes the legacy worktree's branch in the parent" ||
  fail "cleanup deletes the legacy worktree's branch in the parent"

# record tests

repo="$HOME/work/record-open"
init_repo "$repo" master
git -C "$repo" checkout -q -b feature
printf 'changed\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "record nonprotected" -f tracked.txt
assert_status 0 "record commits and pushes non-protected branch"
remote_has_branch "$repo" feature &&
  pass "record creates remote branch for non-protected branch" ||
  fail "record creates remote branch for non-protected branch"
assert_equal "$(git -C "$repo" log -1 --format=%s)" "record nonprotected" \
  "record uses provided commit message"

# Outside the sandbox, a ledgered clone is the worker's own: record and
# submit refuse it outright, before any lock and before any git command
# runs against it.
repo="$HOME/work/record-outside-sandboxed-clone"
init_repo "$repo" master
desc="${RUN_ID}-outside-sandboxed-clone"
path=$(begin_clone "$desc" "$repo")
head_before=$(git -C "$path" rev-parse HEAD)
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a ledgered clone from outside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "allod change run" \
  "the outside record refusal names the worker's own sandboxed command"
assert_equal "$(git -C "$path" rev-parse HEAD)" "$head_before" "the refused record committed nothing"

capture submit_in_repo "$path" -t "must be refused" --dry-run
assert_status 11 "submit refuses a ledgered clone from outside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "allod change run" \
  "the outside submit refusal names the worker's own sandboxed command"

# The ledger match that decides this is pure path comparison, never a git
# command; the logging git proves none runs against the clone either way.
git_call_log="$TMP/git-call-log-${RUN_ID}"
: > "$git_call_log"
logging_git_bin=$(make_logging_git "$git_call_log")
saved_path_for_log="$PATH"
export PATH="$logging_git_bin:$PATH"
capture record_in_repo "$path" -m "must be refused"
export PATH="$saved_path_for_log"
assert_status 11 "record (logging git) still refuses the clone from outside the sandbox"
calls_against_clone=$(grep -F -- "$path" "$git_call_log" || true)
[[ -z "$calls_against_clone" ]] &&
  pass "no git command runs against the clone before the outside refusal" ||
  fail "no git command runs against the clone before the outside refusal" "$calls_against_clone"

# Inside the sandbox (ALLOD_CHANGE_SANDBOX=1, set only by 'run' in practice):
# record and submit treat the clone as an ordinary repository. A clone never
# sits at its repo's own registered path (it lives under ~/changes), so
# protection can only reach it through the origin-identity match, never the
# exact-path one: the parent sits at 'work/acme/sandboxed-<run>', which is
# both its own registered entry (so the parent itself is not misplaced) and
# the tail of its origin's own path (so the clone's origin-identity lookup
# finds the same entry), exactly the way a real forge URL's owner/repo tail
# would.
repo="$HOME/work/acme/sandboxed-${RUN_ID}"
sandboxed_remote="$TMP/remotes/acme/sandboxed-${RUN_ID}.git"
mkdir -p "$(dirname "$repo")" "$(dirname "$sandboxed_remote")"
git init -q --bare -b master "$sandboxed_remote"
git init -q -b master "$repo"
git -C "$repo" config user.name "Test User"
git -C "$repo" config user.email "test@example.invalid"
printf 'base\n' > "$repo/tracked.txt"
git -C "$repo" add tracked.txt
git -C "$repo" commit -qm initial
git -C "$repo" remote add origin "$sandboxed_remote"
git -C "$repo" push -q -u origin master
git -C "$repo" fetch -q origin
git -C "$repo" remote set-head origin master
printf 'work/acme/sandboxed-%s master\n' "$RUN_ID" >> "$HOME/.config/git/protected-branches"
desc="${RUN_ID}-sandboxed"
path=$(begin_clone "$desc" "$repo")
printf 'changed\n' > "$path/tracked.txt"
export ALLOD_CHANGE_SANDBOX=1
capture record_in_repo "$path" -m "record inside the sandbox"
unset ALLOD_CHANGE_SANDBOX
assert_status 0 "record inside the sandbox commits and pushes a protected repo's clone"
bare_sha=$(git -C "$repo" ls-remote origin "refs/heads/agent/$desc" | cut -f1)
remote=$(git -C "$repo" remote get-url origin)
# Read the pushed commit back from the bare origin, never from the clone: a
# clone's own refs are the worker's to rewrite.
assert_equal "$(git -C "$remote" log -1 --format=%s "$bare_sha" 2>/dev/null)" "record inside the sandbox" \
  "the bare origin, not the clone, shows the pushed commit's message"

# Moved checkout: pinned by the clone's own handoff file, exactly as a
# legacy worktree's is, now that the ledger is invisible inside.
git -C "$path" switch -q -c some/other-branch
printf 'changed again\n' > "$path/tracked.txt"
export ALLOD_CHANGE_SANDBOX=1
capture record_in_repo "$path" -m "must be refused"
unset ALLOD_CHANGE_SANDBOX
assert_status 3 "record inside the sandbox refuses a checkout moved off its own handoff branch"
assert_contains "$CAPTURE_OUTPUT" "agent/$desc" "the moved-checkout refusal names the handoff branch"
export ALLOD_CHANGE_SANDBOX=1
capture submit_in_repo "$path" -t "must be refused" --dry-run
unset ALLOD_CHANGE_SANDBOX
assert_status 3 "submit inside the sandbox refuses a checkout moved off its own handoff branch"
assert_contains "$CAPTURE_OUTPUT" "checkout was moved after begin" \
  "submit's moved-checkout refusal explains the handoff mismatch"
git -C "$path" checkout -q -- tracked.txt
git -C "$path" switch -q "agent/$desc"

# Protected branch: branch protection by origin still applies inside the
# sandbox. The handoff file is removed first, the same way a checkout that
# predates 'begin -d' carries none, so this isolates the protection check
# from the moved-checkout one just above (switching off agent/$desc at all
# is itself a moved checkout, and that check runs first).
git -C "$path" checkout -q master
git_dir=$(git -C "$path" rev-parse --path-format=absolute --git-dir)
rm -f "$git_dir/allod-change-branch"
printf 'edit\n' > "$path/tracked.txt"
export ALLOD_CHANGE_SANDBOX=1
capture record_in_repo "$path" -m "must be refused"
unset ALLOD_CHANGE_SANDBOX
assert_status 2 "record inside the sandbox still refuses a protected branch"
git -C "$path" checkout -q -- tracked.txt
git -C "$path" checkout -q "agent/$desc"

export ALLOD_CHANGE_SANDBOX=1
capture submit_in_repo "$path" -t "sandboxed submit" --dry-run
unset ALLOD_CHANGE_SANDBOX
assert_status 0 "submit --dry-run inside the sandbox resolves the base from the clone's own origin/HEAD"
assert_contains "$CAPTURE_OUTPUT" "Base: master" \
  "submit --dry-run names the clone's own default branch as the base"

repo="$HOME/work/record-protected-default"
init_repo "$repo" master
protect_repo "$repo" master
printf 'changed\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "blocked"
assert_status 2 "record refuses protected branch before staging"
git -C "$repo" diff --cached --quiet &&
  pass "record leaves index untouched when protected branch is refused" ||
  fail "record leaves index untouched when protected branch is refused"

repo="$HOME/work/record-untracked"
init_repo "$repo" master
git -C "$repo" checkout -q -b feature
printf 'new\n' > "$repo/new.txt"
head_before=$(git -C "$repo" rev-parse HEAD)
capture record_in_repo "$repo" -m "record with untracked"
assert_status 1 "record refuses to commit with an untracked file present"
assert_contains "$CAPTURE_OUTPUT" "new.txt" "record names the untracked file"
assert_contains "$CAPTURE_OUTPUT" "--files" "record points at --files"
assert_equal "$(git -C "$repo" rev-parse HEAD)" "$head_before" \
  "record commits nothing when untracked files block it"
remote_has_branch "$repo" feature &&
  fail "record pushes nothing when untracked files block it" ||
  pass "record pushes nothing when untracked files block it"

capture record_in_repo "$repo" -m "record names the untracked file" -f new.txt
assert_status 0 "record records the named untracked file"
assert_contains "$(git -C "$repo" show --name-only --format=%s HEAD)" "new.txt" \
  "record includes the named file in the commit"

mkdir -p "$repo/sub"
printf 'a\n' > "$repo/sub/a.txt"
printf 'b\n' > "$repo/sub/b.txt"
capture record_in_repo "$repo" -m "record names a directory" -f sub
assert_status 0 "record stages a named directory of untracked files"

printf 'junk\n' > "$repo/junk.tmp"
printf 'junk.tmp\n' > "$repo/.gitignore"
git -C "$repo" add .gitignore
git -C "$repo" commit -qm "ignore junk"
git -C "$repo" push -q origin feature
printf 'changed again\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "record ignores ignored files" -f tracked.txt
assert_status 0 "record ignores gitignored files"

repo="$HOME/work/record-sweep-refused"
init_repo "$repo" master
printf 'changed\n' > "$repo/tracked.txt"
head_before=$(git -C "$repo" rev-parse HEAD)
capture record_in_repo "$repo" -m "sweep refused"
assert_status 7 "record refuses to sweep an unnamed tracked change in a main checkout"
assert_contains "$CAPTURE_OUTPUT" "tracked.txt" "record names the swept path it refuses to stage"
assert_contains "$CAPTURE_OUTPUT" "--files" "record points at --files"
git -C "$repo" diff --cached --quiet &&
  pass "record stages nothing when the sweep is refused" ||
  fail "record stages nothing when the sweep is refused" "$(git -C "$repo" diff --cached --name-only)"
assert_equal "$(git -C "$repo" rev-parse HEAD)" "$head_before" \
  "record makes no commit when the sweep is refused"

repo="$HOME/work/record-sweep-named"
init_repo "$repo" master
printf 'changed\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "sweep named" --files tracked.txt
assert_status 0 "record commits a named file in a main checkout"
assert_equal "$(git -C "$repo" log -1 --format=%s)" "sweep named" \
  "record --files commit uses the given message"

repo="$HOME/work/record-sweep-clone"
init_repo "$repo" master
desc="${RUN_ID}-sweep-clone"
path=$(begin_clone "$desc" "$repo")
printf 'changed\n' > "$path/tracked.txt"
export ALLOD_CHANGE_SANDBOX=1
capture record_in_repo "$path" -m "clone sweep unaffected"
unset ALLOD_CHANGE_SANDBOX
assert_status 0 "record inside the sandbox still stages tracked modifications in a clone with no --files"
assert_equal "$(git -C "$path" log -1 --format=%s)" "clone sweep unaffected" \
  "record commits the clone's swept change"

repo="$HOME/work/record-detached"
init_repo "$repo" master
git -C "$repo" checkout -q --detach
capture record_in_repo "$repo" -m "detached"
assert_status 1 "record rejects detached HEAD"
assert_contains "$CAPTURE_OUTPUT" "detached" "record detached HEAD message is actionable"

repo="$HOME/work/record-empty"
init_repo "$repo" master
capture record_in_repo "$repo" -m "empty"
assert_status 4 "record exits 4 when nothing changed and nothing unpushed"

repo="$HOME/work/record-empty-message"
init_repo "$repo" master
git -C "$repo" checkout -q -b empty-message
printf 'changed\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "" -f tracked.txt
assert_status 1 "record rejects empty commit message"

repo="$HOME/work/record-files"
init_repo "$repo" master
git -C "$repo" checkout -q -b selected-files
printf 'one\n' > "$repo/file1.txt"
printf 'two\n' > "$repo/file2.txt"
printf 'three\n' > "$repo/file3.txt"
git -C "$repo" add file1.txt file2.txt file3.txt
git -C "$repo" commit -qm "add files"
printf 'one changed\n' > "$repo/file1.txt"
printf 'two changed\n' > "$repo/file2.txt"
printf 'three changed\n' > "$repo/file3.txt"
capture record_in_repo "$repo" -m "selected files" -f file1.txt -f file2.txt
assert_status 0 "record accepts repeated --files"
files=$(changed_files_in_head "$repo")
assert_contains "$files" "file1.txt" "record -f stages first selected file"
assert_contains "$files" "file2.txt" "record -f stages second selected file"
assert_not_contains "$files" "file3.txt" "record -f leaves unselected file out of commit"

# '--files' is plural: one flag takes as many paths as follow it, the repeated
# form still accumulates, and the two combine in one invocation.
repo="$HOME/work/record-files-variadic"
init_repo "$repo" master
git -C "$repo" checkout -q -b variadic-files
printf 'one\n' > "$repo/file1.txt"
printf 'two\n' > "$repo/file2.txt"
printf 'three\n' > "$repo/file3.txt"
printf 'four\n' > "$repo/file4.txt"
git -C "$repo" add file1.txt file2.txt file3.txt file4.txt
git -C "$repo" commit -qm "add files"
printf 'one changed\n' > "$repo/file1.txt"
printf 'two changed\n' > "$repo/file2.txt"
printf 'three changed\n' > "$repo/file3.txt"
printf 'four changed\n' > "$repo/file4.txt"
capture record_in_repo "$repo" -m "several paths" --files file1.txt file2.txt
assert_status 0 "record accepts several paths after one --files"
files=$(changed_files_in_head "$repo")
assert_contains "$files" "file1.txt" "record --files stages the first path of a list"
assert_contains "$files" "file2.txt" "record --files stages a later path of the same list"
assert_not_contains "$files" "file3.txt" "record --files leaves an unnamed file out of the commit"
printf 'one again\n' > "$repo/file1.txt"
printf 'three again\n' > "$repo/file3.txt"
capture record_in_repo "$repo" -m "mixed forms" -f file1.txt file3.txt --files file4.txt
assert_status 0 "record combines a -f list with a repeated --files"
files=$(changed_files_in_head "$repo")
assert_contains "$files" "file1.txt" "record -f stages the first path of a list"
assert_contains "$files" "file3.txt" "record -f stages a later path of the same list"
assert_contains "$files" "file4.txt" "record also stages the path from the repeated flag"

# A following option ends the path list, so flag order changes nothing.
repo="$HOME/work/record-files-order"
init_repo "$repo" master
git -C "$repo" checkout -q -b files-order
printf 'one\n' > "$repo/file1.txt"
printf 'two\n' > "$repo/file2.txt"
printf 'three\n' > "$repo/file3.txt"
git -C "$repo" add file1.txt file2.txt file3.txt
git -C "$repo" commit -qm "add files"
printf 'one changed\n' > "$repo/file1.txt"
printf 'two changed\n' > "$repo/file2.txt"
printf 'three changed\n' > "$repo/file3.txt"
capture record_in_repo "$repo" --files file1.txt file2.txt -m "files before message"
assert_status 0 "record accepts a message flag after a --files list"
assert_equal "$(git -C "$repo" log -1 --format=%s)" "files before message" \
  "record stops consuming paths at the next option"
files_first=$(changed_files_in_head "$repo")
assert_not_contains "$files_first" "file3.txt" "record leaves an unnamed file out when --files comes first"
printf 'one again\n' > "$repo/file1.txt"
printf 'two again\n' > "$repo/file2.txt"
capture record_in_repo "$repo" -m "message before files" --files file1.txt file2.txt
assert_status 0 "record accepts a --files list after the message flag"
assert_equal "$(git -C "$repo" log -1 --format=%s)" "message before files" \
  "record keeps the message when the path list comes last"
assert_equal "$(changed_files_in_head "$repo")" "$files_first" \
  "record stages the same files whichever order the flags come in"

# A path list that consumed nothing must fail loud rather than fall through to
# the 'git add -u' sweep.
repo="$HOME/work/record-files-empty"
init_repo "$repo" master
git -C "$repo" checkout -q -b empty-files
printf 'changed\n' > "$repo/tracked.txt"
head_before=$(git -C "$repo" rev-parse HEAD)
capture record_in_repo "$repo" -m "no paths" --files
assert_status 1 "record rejects a trailing --files with no path"
assert_contains "$CAPTURE_OUTPUT" "requires a value" "record names --files as missing its value"
capture record_in_repo "$repo" --files -m "no paths"
assert_status 1 "record rejects --files followed by another option"
assert_contains "$CAPTURE_OUTPUT" "requires a value" "record names --files as missing its value before an option"
capture record_in_repo "$repo" -m "no paths" -f
assert_status 1 "record rejects a trailing -f with no path"
assert_equal "$(git -C "$repo" rev-parse HEAD)" "$head_before" \
  "record commits nothing when an option is missing its value"

# A bare '--' ends option parsing and names nothing, so 'git add -u' stays in force — not an
# error, unlike an option missing its value.
repo="$HOME/work/record-bare-dashdash"
init_repo "$repo" master
desc="${RUN_ID}-bare-dashdash"
path=$(begin_clone "$desc" "$repo")
printf 'one\n' > "$path/file1.txt"
printf 'two\n' > "$path/file2.txt"
git -C "$path" add file1.txt file2.txt
git -C "$path" commit -qm "add tracked files"
printf 'one changed\n' > "$path/file1.txt"
printf 'two changed\n' > "$path/file2.txt"
export ALLOD_CHANGE_SANDBOX=1
capture record_in_repo "$path" -m "bare dashdash" --
unset ALLOD_CHANGE_SANDBOX
assert_status 0 "record inside the sandbox accepts a bare -- with no path"
files=$(changed_files_in_head "$path")
assert_contains "$files" "file1.txt" "bare -- falls back to add -u for the first file"
assert_contains "$files" "file2.txt" "bare -- falls back to add -u for the second file"

# '--' ends option parsing, so a path that begins with '-' is still reachable.
repo="$HOME/work/record-files-dash"
init_repo "$repo" master
git -C "$repo" checkout -q -b dash-files
printf 'one\n' > "$repo/file1.txt"
printf 'dash\n' > "$repo/-dash.txt"
git -C "$repo" add -- file1.txt -dash.txt
git -C "$repo" commit -qm "add files"
printf 'one changed\n' > "$repo/file1.txt"
printf 'dash changed\n' > "$repo/-dash.txt"
capture record_in_repo "$repo" -m "dash path" --files file1.txt -- -dash.txt
assert_status 0 "record stages a '-'-prefixed path after --"
files=$(changed_files_in_head "$repo")
assert_contains "$files" "file1.txt" "record keeps the --files list when -- follows it"
assert_contains "$files" "-dash.txt" "record stages the path named after --"

repo="$HOME/work/record-add-u"
init_repo "$repo" master
desc="${RUN_ID}-add-u"
path=$(begin_clone "$desc" "$repo")
printf 'one\n' > "$path/file1.txt"
printf 'two\n' > "$path/file2.txt"
git -C "$path" add file1.txt file2.txt
git -C "$path" commit -qm "add tracked files"
printf 'one changed\n' > "$path/file1.txt"
printf 'two changed\n' > "$path/file2.txt"
printf 'new\n' > "$path/untracked.txt"
export ALLOD_CHANGE_SANDBOX=1
capture record_in_repo "$path" -m "tracked changes"
unset ALLOD_CHANGE_SANDBOX
assert_status 1 "record inside the sandbox refuses untracked files when staging tracked modifications in a clone"
assert_contains "$CAPTURE_OUTPUT" "untracked.txt" "record names the untracked file it refuses to leave out"
git -C "$path" diff --cached --quiet &&
  pass "record stages nothing when untracked files block it" ||
  fail "record stages nothing when untracked files block it"

rm "$path/untracked.txt"
export ALLOD_CHANGE_SANDBOX=1
capture record_in_repo "$path" -m "tracked changes"
unset ALLOD_CHANGE_SANDBOX
assert_status 0 "record inside the sandbox without -f stages tracked modifications in a clone"
files=$(changed_files_in_head "$path")
assert_contains "$files" "file1.txt" "record add -u includes first tracked file"
assert_contains "$files" "file2.txt" "record add -u includes second tracked file"

repo="$HOME/work/record-retry"
init_repo "$repo" master
good_remote=$(git -C "$repo" remote get-url origin)
git -C "$repo" checkout -q -b retry
printf 'retry\n' > "$repo/tracked.txt"
git -C "$repo" remote set-url origin "$TMP/missing-remote.git"
capture record_in_repo "$repo" -m "retry push" -f tracked.txt
[[ "$CAPTURE_STATUS" -ne 0 ]] || fail "record first push can fail after commit" "$CAPTURE_OUTPUT"
git -C "$repo" remote set-url origin "$good_remote"
capture record_in_repo "$repo" -m "retry push ignored"
assert_status 0 "record retries push when local commit is unpushed"
remote_has_branch "$repo" retry &&
  pass "record retry creates remote branch" ||
  fail "record retry creates remote branch"

repo="$HOME/work/record-stacked"
init_repo "$repo" master
git -C "$repo" checkout -q -b stacked
printf 'first\n' > "$repo/tracked.txt"
git -C "$repo" commit -qam "first unpushed"
printf 'second\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "second unpushed" -f tracked.txt
assert_status 0 "record commits new changes on top of existing unpushed commits"
git -C "$repo" fetch -q origin stacked
assert_equal "$(git -C "$repo" rev-list --count origin/master..origin/stacked)" "2" \
  "record pushes existing and new unpushed commits"

repo="$HOME/work/record-no-upstream"
init_repo "$repo" master
git -C "$repo" checkout -q -b no-upstream
printf 'local\n' > "$repo/tracked.txt"
git -C "$repo" commit -qam "local commit"
capture record_in_repo "$repo" -m "ignored"
assert_status 0 "record push retry works on branch with no upstream"
remote_has_branch "$repo" no-upstream &&
  pass "record no-upstream retry pushes current branch" ||
  fail "record no-upstream retry pushes current branch"

repo="$HOME/work/record-unknown"
init_repo_no_remote "$repo" master
git -C "$repo" checkout -q -b local-only
printf 'local\n' > "$repo/tracked.txt"
git -C "$repo" commit -qam "local-only commit"
capture record_in_repo "$repo" -m "must not be used"
assert_status 4 "record distinguishes an unknown remote comparison from no unpushed commits"
assert_contains "$CAPTURE_OUTPUT" "cannot determine whether commits are unpushed" \
  "record reports the unknown comparison honestly"
assert_contains "$CAPTURE_OUTPUT" "no origin remote" \
  "record names why the comparison is unknown"

repo="$HOME/work/record-no-default-head"
init_repo_no_origin_head "$repo" master
git -C "$repo" checkout -q -b orphan
printf 'local\n' > "$repo/tracked.txt"
git -C "$repo" commit -qam "local commit"
capture record_in_repo "$repo" -m "must not be used"
assert_status 4 "record refuses rather than guess master when origin/HEAD is unresolved"
assert_contains "$CAPTURE_OUTPUT" "cannot determine whether commits are unpushed" \
  "record reports the unresolved default branch honestly"
assert_contains "$CAPTURE_OUTPUT" "default branch of origin cannot be resolved" \
  "record names why the comparison is unknown"

repo="$HOME/work/record-message-file"
init_repo "$repo" master
git -C "$repo" checkout -q -b message-file
printf 'message from file\n' > "$TMP/message.txt"
printf 'changed\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -M "$TMP/message.txt" -f tracked.txt
assert_status 0 "record reads commit message from file"
assert_equal "$(git -C "$repo" log -1 --format=%s)" "message from file" \
  "record -M file uses file message"

repo="$HOME/work/record-message-stdin"
init_repo "$repo" master
git -C "$repo" checkout -q -b message-stdin
printf 'changed\n' > "$repo/tracked.txt"
set +e
CAPTURE_OUTPUT=$(cd "$repo" && printf 'message from stdin' | "$ALLOD" change record -M - -f tracked.txt 2>&1)
CAPTURE_STATUS=$?
set -e
assert_status 0 "record reads commit message from stdin"
assert_equal "$(git -C "$repo" log -1 --format=%s)" "message from stdin" \
  "record -M - uses stdin message"

repo="$HOME/work/record-no-amend"
init_repo "$repo" master
git -C "$repo" checkout -q -b no-amend
printf 'first\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "first record" -f tracked.txt
assert_status 0 "record first commit for no-amend test"
printf 'second\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "second record" -f tracked.txt
assert_status 0 "record second commit for no-amend test"
assert_equal "$(git -C "$repo" rev-list --count origin/master..HEAD)" "2" \
  "record creates additive commits instead of amending"

repo="$HOME/work/record-no-force"
init_repo "$repo" master
git -C "$repo" checkout -q -b no-force
printf 'changed\n' > "$repo/tracked.txt"
push_log="$TMP/push.log"
: > "$push_log"
export REAL_GIT GIT_PUSH_LOG="$push_log"
mock_git_path=$(make_mock_git_path no-force)
capture_with_path "$mock_git_path" bash -c 'cd "$1" && "$2" change record -m "no force" -f tracked.txt' _ "$repo" "$ALLOD"
assert_status 0 "record succeeds through mock git wrapper"
push_args=$(cat "$push_log")
assert_contains "$push_args" "push -u origin HEAD" "record uses additive push command"
assert_not_contains "$push_args" "--force" "record never force-pushes"

# submit tests

repo="$HOME/work/submit-valid"
init_repo "$repo" master
git -C "$repo" checkout -q -b agent/submit-valid
forge_log="$TMP/forge-valid.log"
forge_body="$TMP/forge-valid.body"
export MOCK_FORGE_LOG="$forge_log" MOCK_FORGE_BODY_LOG="$forge_body" MOCK_FORGE_MODE=""
forge_bin=$(make_mock_forge valid)
body=$'Summary\n\n## Validation\nmanual test'
capture_with_path "$forge_bin:$PATH" submit_in_repo "$repo" -t "Submit valid" -b "$body"
assert_status 0 "submit creates PR with validation body"
forge_calls=$(cat "$forge_log")
assert_contains "$forge_calls" "pr find-by-head agent/submit-valid" "submit checks for existing PR"
assert_contains "$forge_calls" "pr create -t Submit valid -H agent/submit-valid -B master -F" \
  "submit calls forge pr create with expected args"
assert_contains "$(cat "$forge_body")" "## Validation" "submit passes body file to forge"

repo="$HOME/work/submit-depends"
init_repo "$repo" master
git -C "$repo" checkout -q -b agent/submit-depends
forge_log="$TMP/forge-depends.log"
forge_body="$TMP/forge-depends.body"
export MOCK_FORGE_LOG="$forge_log" MOCK_FORGE_BODY_LOG="$forge_body" MOCK_FORGE_MODE=""
forge_bin=$(make_mock_forge depends)
capture_with_path "$forge_bin:$PATH" submit_in_repo "$repo" -t "Depends" -b "$body" --depends-on "#12"
assert_status 0 "submit accepts depends-on"
assert_contains "$(cat "$forge_body")" "Depends on: #12" "submit appends depends-on line"

repo="$HOME/work/submit-dry-run"
init_repo "$repo" master
git -C "$repo" checkout -q -b agent/submit-dry-run
no_forge_path=$(make_restricted_path_without_forge)
capture_with_path "$no_forge_path" submit_in_repo "$repo" -t "Dry run" -b "$body" --base develop --depends-on "#9" --dry-run
assert_status 0 "submit dry-run succeeds without forge on PATH"
assert_contains "$CAPTURE_OUTPUT" "Branch: agent/submit-dry-run" "submit dry-run prints branch"
assert_contains "$CAPTURE_OUTPUT" "Base: develop" "submit dry-run prints base"
assert_contains "$CAPTURE_OUTPUT" "Title: Dry run" "submit dry-run prints title"
assert_contains "$CAPTURE_OUTPUT" "Depends on: #9" "submit dry-run prints assembled body"

repo="$HOME/work/submit-no-forge"
init_repo "$repo" master
git -C "$repo" checkout -q -b agent/submit-no-forge
capture_with_path "$no_forge_path" submit_in_repo "$repo" -t "No forge" -b "$body"
assert_status 1 "submit fails when forge is not on PATH"
assert_contains "$CAPTURE_OUTPUT" "forge" "submit missing forge message is actionable"

repo="$HOME/work/submit-existing"
init_repo "$repo" master
git -C "$repo" checkout -q -b agent/submit-existing
forge_log="$TMP/forge-existing.log"
forge_body="$TMP/forge-existing.body"
export MOCK_FORGE_LOG="$forge_log" MOCK_FORGE_BODY_LOG="$forge_body" MOCK_FORGE_MODE="existing"
forge_bin=$(make_mock_forge existing)
capture_with_path "$forge_bin:$PATH" submit_in_repo "$repo" -t "Existing" -b "$body"
assert_status 6 "submit rejects branch with existing PR"
assert_contains "$CAPTURE_OUTPUT" "forge pr edit" "submit directs user to forge pr edit"
assert_not_contains "$(cat "$forge_log")" "pr create" "submit does not create duplicate PR"

repo="$HOME/work/submit-detached"
init_repo "$repo" master
git -C "$repo" checkout -q --detach
capture submit_in_repo "$repo" -t "Detached" -b "$body" --dry-run
assert_status 1 "submit rejects detached HEAD"

repo="$HOME/work/submit-no-default-head"
init_repo_no_origin_head "$repo" master
git -C "$repo" checkout -q -b agent/submit-no-default-head
capture submit_in_repo "$repo" -t "No default" -b "$body"
assert_status 1 "submit refuses a PR base it cannot resolve rather than guessing master"
assert_contains "$CAPTURE_OUTPUT" "remote set-head" "submit names the origin/HEAD repair"

# A running worker's clone is hands-off for 'cleanup' and 'list' alike
# (exit 13); 'record' and 'submit' no longer touch the ledger lock at all,
# since they refuse any ledgered clone from outside before reaching it.
repo="$HOME/work/active-scope"
init_repo "$repo" master
desc="${RUN_ID}-active-scope"
path=$(begin_clone "$desc" "$repo")
# listRow must hold its lock through its own forge query, not just probe and release;
# one locked entry must not block or corrupt another's row.
other_desc="${RUN_ID}-active-scope-sibling"
other_path=$(begin_clone "$other_desc" "$repo")
forge_log="$TMP/forge-active-scope.log"
export MOCK_FORGE_LOG="$forge_log" MOCK_FORGE_ALL_STATE="" MOCK_FORGE_ALL_NUMBER=""
forge_bin=$(make_mock_forge active-scope)
# Closes the window between resolving the clone and the forge query that follows: outside
# commands refuse while the ledger file is locked, with no systemctl involved at all.
active_scope_ledger_file="$(git -C "$repo" rev-parse --path-format=absolute --git-dir)/allod-changes/$desc"
exec {active_scope_lock_fd}>>"$active_scope_ledger_file"
flock -x "$active_scope_lock_fd"
cleanup_with_forge "$path" "$forge_bin"
assert_status 13 "cleanup refuses while the clone's own ledger file is locked"
assert_contains "$CAPTURE_OUTPUT" "locked" "the lock refusal names what it found"
[[ -d "$path" ]] &&
  pass "cleanup's active-lock refusal preserves the clone" ||
  fail "cleanup's active-lock refusal preserves the clone" "missing: $path"
list_with_forge "$repo" "$forge_bin"
assert_contains "$CAPTURE_OUTPUT" "$path	agent/$desc	running" \
  "list reports running, never asks the forge at all, while the clone's own ledger file is locked"
assert_contains "$CAPTURE_OUTPUT" "$other_path	agent/$other_desc	unsubmitted" \
  "the sibling clone's own row is unaffected: one locked ledger entry does not block or corrupt another's, in the same 'list' call"
exec {active_scope_lock_fd}>&-
rm -rf "$path" "$other_path"
rm -f "$active_scope_ledger_file" "$(dirname "$active_scope_ledger_file")/$other_desc"

# cleanup tests

repo="$HOME/work/cleanup-stat-error"
init_repo "$repo" master
desc="${RUN_ID}-cleanup-stat-error"
stat_error_git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
mkdir -p "$stat_error_git_dir/allod-changes"
# A ledger entry naming a path with a non-directory in the middle: stat on
# it fails with ENOTDIR, not ENOENT, so this must not read as 'missing'.
bogus_parent="$HOME/changes/${RUN_ID}-not-a-directory"
touch "$bogus_parent"
bogus_path="$bogus_parent/subpath"
printf '%s\nagent/%s\n' "$bogus_path" "$desc" > "$stat_error_git_dir/allod-changes/$desc"
capture "$ALLOD" change cleanup "$bogus_path"
assert_status 1 "cleanup refuses rather than treat a non-ENOENT stat error as missing"
assert_contains "$CAPTURE_OUTPUT" "could not check clone" "the stat-error refusal names it"
[[ -f "$stat_error_git_dir/allod-changes/$desc" ]] &&
  pass "the stat-error refusal leaves the ledger entry in place" ||
  fail "the stat-error refusal leaves the ledger entry in place" "entry was dropped"
rm -f "$bogus_parent"
rm -f "$stat_error_git_dir/allod-changes/$desc"

# A path that exists and stats cleanly but is a plain file, not a directory, needs the dedicated
# IsDir check; it must never read as 'missing' either.
repo="$HOME/work/cleanup-not-a-directory"
init_repo "$repo" master
desc="${RUN_ID}-cleanup-not-a-directory"
not_dir_git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
mkdir -p "$not_dir_git_dir/allod-changes"
not_dir_path="$HOME/changes/${RUN_ID}-cleanup-not-a-directory-file"
touch "$not_dir_path"
printf '%s\nagent/%s\n' "$not_dir_path" "$desc" > "$not_dir_git_dir/allod-changes/$desc"
capture "$ALLOD" change cleanup "$not_dir_path"
assert_status 1 "cleanup refuses when the clone path exists but is not a directory"
assert_contains "$CAPTURE_OUTPUT" "not a directory" "the not-a-directory refusal names the reason"
[[ -f "$not_dir_path" ]] &&
  pass "the plain file at the clone's path is untouched" ||
  fail "the plain file at the clone's path is untouched" "missing: $not_dir_path"
[[ -f "$not_dir_git_dir/allod-changes/$desc" ]] &&
  pass "the not-a-directory refusal leaves the ledger entry in place" ||
  fail "the not-a-directory refusal leaves the ledger entry in place" "entry was dropped"
rm -f "$not_dir_path"
rm -f "$not_dir_git_dir/allod-changes/$desc"

repo="$HOME/work/cleanup-regular"
init_repo "$repo" master
git -C "$repo" checkout -q -b agent/regular-cleanup
capture "$ALLOD" change cleanup "$repo"
[[ "$CAPTURE_STATUS" -ne 0 ]] || fail "cleanup refuses regular repo" "$CAPTURE_OUTPUT"
assert_contains "$CAPTURE_OUTPUT" "regular repository" "cleanup regular repo failure is actionable"
[[ -n "$(git -C "$repo" branch --list agent/regular-cleanup)" ]] &&
  pass "cleanup regular repo refusal leaves branch intact" ||
  fail "cleanup regular repo refusal leaves branch intact"

misplace_repo() {
  local repo="$1" entry="$2" branch="${3:-master}"
  init_repo_no_remote "$repo" "$branch"
  git -C "$repo" remote add origin "ssh://git@forge.example:2222/acme/widget.git"
  printf '%s %s\n' "$entry" master >> "$HOME/.config/git/protected-branches"
}

assert_names_both_paths() {
  local output="$1" description="$2"
  assert_contains "$output" "'work/acme/widget'" "$description names the expected path"
  assert_contains "$output" "'work/acme-widget'" "$description names the actual path"
  assert_contains "$output" "'acme/widget'" "$description names the repository"
}

repo="$HOME/work/acme-widget"
misplace_repo "$repo" work/acme/widget

capture "$ALLOD" change begin "$repo"
assert_status 8 "begin without -d refuses a misplaced protected checkout"
assert_names_both_paths "$CAPTURE_OUTPUT" "the begin refusal"

capture "$ALLOD" change begin -d "${RUN_ID}-misplaced" "$repo"
assert_status 8 "begin -d refuses a misplaced protected checkout"
assert_names_both_paths "$CAPTURE_OUTPUT" "the begin -d refusal"
assert_equal "$(git -C "$repo" branch --list "agent/${RUN_ID}-misplaced")" "" \
  "the refused begin -d created no branch"

capture "$ALLOD" change begin --resume "${RUN_ID}-misplaced" "$repo"
assert_status 8 "begin --resume refuses a misplaced protected checkout"
assert_names_both_paths "$CAPTURE_OUTPUT" "the begin --resume refusal"

printf 'edit\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "must be refused" -f tracked.txt
assert_status 8 "record refuses a misplaced protected checkout"
assert_names_both_paths "$CAPTURE_OUTPUT" "the record refusal"
assert_equal "$(git -C "$repo" diff --cached --name-only)" "" \
  "the refused record staged nothing"
assert_equal "$(git -C "$repo" log -1 --format=%s)" "initial" \
  "the refused record committed nothing"

git -C "$repo" checkout -q -b "agent/${RUN_ID}-misplaced-branch"
capture record_in_repo "$repo" -m "must be refused" -f tracked.txt
assert_status 8 "record refuses a misplaced protected checkout on an agent branch"
git -C "$repo" checkout -q master
git -C "$repo" checkout -q -- tracked.txt

worktree="$TMP/misplaced-worktree"
git -C "$repo" worktree add -q -b "agent/${RUN_ID}-misplaced-wt" "$worktree"
WORKTREES+=("$worktree")
capture record_in_repo "$worktree" -m "must be refused" -f tracked.txt
assert_status 8 "record refuses from a worktree of a misplaced protected checkout"
assert_names_both_paths "$CAPTURE_OUTPUT" "the worktree record refusal"

repo="$HOME/work/two-entries"
init_repo "$repo" master
git -C "$repo" push -q origin master:release
git -C "$repo" fetch -q origin
protect_repo "$repo" master
protect_repo "$repo" release
desc="${RUN_ID}-two-entries"
path=$(begin_clone "$desc" "$repo")
git -C "$path" merge-base --is-ancestor origin/master HEAD &&
  pass "begin starts from the first listed branch when a repo has several" ||
  fail "begin starts from the first listed branch when a repo has several"
git -C "$repo" checkout -q -b release origin/release
printf 'edit\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "must be refused" -f tracked.txt
assert_status 2 "record refuses the second listed branch of a repo with several entries"
assert_contains "$CAPTURE_OUTPUT" "protected branch 'release'" \
  "the second-entry refusal names that branch"
git -C "$repo" checkout -q -- tracked.txt
git -C "$repo" checkout -q master

super="$HOME/work/super"
init_repo "$super" master
sub_source="$TMP/lib-source"
git init -q -b master "$sub_source"
git -C "$sub_source" config user.name "Test User"
git -C "$sub_source" config user.email "test@example.invalid"
printf 'lib\n' > "$sub_source/lib.txt"
git -C "$sub_source" add lib.txt
git -C "$sub_source" commit -qm "lib initial"
git -C "$super" -c protocol.file.allow=always submodule add -q "$sub_source" lib
git -C "$super" commit -qm "add submodule"
sub="$super/lib"
git -C "$sub" config user.name "Test User"
git -C "$sub" config user.email "test@example.invalid"
git -C "$sub" checkout -q -B master
protect_repo "$sub" master
printf 'edit\n' > "$sub/lib.txt"
capture record_in_repo "$sub" -m "must be refused" -f lib.txt
assert_status 2 "record refuses the protected branch of a populated submodule"
assert_contains "$CAPTURE_OUTPUT" "protected branch 'master'" \
  "the submodule refusal names the branch"

repo="$HOME/work/separate-widget"
mkdir -p "$repo"
git init -q --separate-git-dir="$TMP/separate-gitdir" -b master "$repo"
git -C "$repo" config user.name "Test User"
git -C "$repo" config user.email "test@example.invalid"
printf 'base\n' > "$repo/tracked.txt"
git -C "$repo" add tracked.txt
git -C "$repo" commit -qm initial
git -C "$repo" remote add origin "https://forge.example/acme/separate.git"
protect_repo "$repo" master
capture "$ALLOD" change begin "$repo"
assert_status 1 "begin requires -d in a --separate-git-dir protected repo"
assert_contains "$CAPTURE_OUTPUT" "requires -d" \
  "the separate-git-dir refusal explains the failure"
printf 'edit\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "must be refused" -f tracked.txt
assert_status 2 "record refuses the protected branch of a --separate-git-dir repo"

repo="$HOME/work/other/gadget"
init_repo_no_remote "$repo" master
git -C "$repo" remote add origin "https://forge.example/other/gadget.git"
capture "$ALLOD" change begin "$repo"
assert_status 0 "begin allows a repo whose origin matches no entry"
assert_equal "$CAPTURE_OUTPUT" "$repo" "an unlisted repo still prints its checkout path"

# resolveClone must survive a symlinked $HOME: git reports paths already resolved, but a ledger
# entry is written from the raw $HOME 'begin' saw, so plain-string comparison would never match.
record_at() {
  local path="$1" home="$2"
  shift 2
  (cd "$path" && HOME="$home" XDG_CONFIG_HOME="$home/.config" "$ALLOD" change record "$@")
}

alt_real="$TMP/alt-home-real"
alt_home="$TMP/alt-home-link"
mkdir -p "$alt_real/.config/git" "$alt_real/work"
touch "$alt_real/.config/git/protected-branches"
printf '[user]\n\tname = Test User\n\temail = test@example.invalid\n' > "$alt_real/.gitconfig"
ln -s "$alt_real" "$alt_home"

# The remote path ends in 'work/symlink-repo.git' so a resolveClone regression would reproduce
# this exact failure, not a weaker one that happens to pass.
alt_repo="$alt_home/work/symlink-repo"
alt_remote="$TMP/remotes/work/symlink-repo.git"
mkdir -p "$(dirname "$alt_repo")" "$(dirname "$alt_remote")"
git init -q --bare -b master "$alt_remote"
HOME="$alt_home" git init -q -b master "$alt_repo"
printf 'base\n' > "$alt_repo/tracked.txt"
HOME="$alt_home" git -C "$alt_repo" add tracked.txt
HOME="$alt_home" git -C "$alt_repo" commit -qm initial
HOME="$alt_home" git -C "$alt_repo" remote add origin "$alt_remote"
HOME="$alt_home" git -C "$alt_repo" push -q -u origin master
HOME="$alt_home" git -C "$alt_repo" fetch -q origin
HOME="$alt_home" git -C "$alt_repo" remote set-head origin master
printf 'work/symlink-repo master\n' >> "$alt_real/.config/git/protected-branches"

alt_desc="${RUN_ID}-symlink-home"
alt_path=$(HOME="$alt_home" XDG_CONFIG_HOME="$alt_home/.config" "$ALLOD" change begin -d "$alt_desc" "$alt_repo")
WORKTREES+=("$alt_path")
printf 'edit\n' > "$alt_path/tracked.txt"
capture record_at "$alt_path" "$alt_home" -m "symlink home"
assert_status 11 "record refuses the clone from outside the sandbox even when \$HOME is a symlink"
assert_contains "$CAPTURE_OUTPUT" "allod change run" \
  "resolveClone's physical-path matching still identifies the clone despite the symlinked \$HOME"

printf '\nTests run: %d\n' "$test_number"
printf 'All %d allod change tests passed.\n' "$test_number"
