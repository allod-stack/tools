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

make_mock_forge() {
  local mode="$1"
  local bin="$TMP/forge-${mode}-bin"
  mkdir -p "$bin"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'EOF'
set -euo pipefail

printf '%s\n' "$*" >> "$MOCK_FORGE_LOG"

if [[ "$1 $2" == "pr find-by-head" ]]; then
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
    status)
      if [[ "$MOCK_GIT_MODE" == "fail-status" ]]; then
        echo "mock git: status forced to fail" >&2
        exit 1
      fi
      ;;
    ls-files)
      if [[ "$MOCK_GIT_MODE" == "fail-ls-files" ]]; then
        echo "mock git: ls-files forced to fail" >&2
        exit 1
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
record_in_repo "$path" -m "second commit on the resumed branch" >/dev/null
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
capture "$ALLOD" change list "$repo"
assert_status 0 "list exits 0 with a clone present"
assert_equal "$(printf '%s' "$CAPTURE_OUTPUT" | grep -c .)" "1" "list prints one row per ledgered clone"
assert_equal "$CAPTURE_OUTPUT" "$(printf 'list-rows\t%s\tagent/%s\tclean' "$path" "$desc")" \
  "list row carries repo, path, branch, and state: a fresh clone with no commits of its own is clean, even though its branch was never pushed under its own name, because HEAD is still reachable from origin's default branch"

# The ledger is the whole mechanism: a two-line file under the parent's own
# .git, named for the description, gone once 'cleanup' runs.
git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
ledger_file="$git_dir/allod-changes/$desc"
[[ -f "$ledger_file" ]] &&
  pass "begin -d writes a ledger entry in the parent" ||
  fail "begin -d writes a ledger entry in the parent" "missing: $ledger_file"
assert_equal "$(sed -n '1p' "$ledger_file")" "$path" "the ledger entry's first line is the clone's path"
assert_equal "$(sed -n '2p' "$ledger_file")" "agent/$desc" "the ledger entry's second line is the clone's branch"
# unpushedCommitsForClone falls back to origin's default branch head when the ledger branch is
# absent remotely; this clone has no commit beyond that, so no push is needed first.
capture record_in_repo "$path" -m "nothing to record"
assert_status 4 "record on a fresh, uncommitted clone has nothing to commit and nothing unpushed to push either"
capture "$ALLOD" change cleanup "$path"
assert_status 0 "cleanup removes a clean clone, with no push needed first"
[[ ! -e "$ledger_file" ]] &&
  pass "cleanup drops the parent's ledger entry" ||
  fail "cleanup drops the parent's ledger entry" "still exists: $ledger_file"
[[ ! -d "$path" ]] &&
  pass "cleanup removes the clone directory" ||
  fail "cleanup removes the clone directory" "still exists: $path"

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

# One case per state, each checked against what cleanup actually does.

repo="$HOME/work/list-states"
init_repo "$repo" master

desc="${RUN_ID}-state-clean"
path=$(begin_clone "$desc" "$repo")
# Pushed once then committed again: exercises the 'present' ls-remote path, not the default-branch
# fallback a never-pushed clone takes.
printf 'already landed\n' > "$path/tracked.txt"
git -C "$path" commit -qam "already landed"
git -C "$path" push -q origin HEAD
assert_list_matches_cleanup "$repo" "$path" clean "a clone with a commit, genuinely pushed under its own branch name"

desc="${RUN_ID}-state-dirty"
path=$(begin_clone "$desc" "$repo")
printf 'dirty\n' > "$path/tracked.txt"
assert_list_matches_cleanup "$repo" "$path" dirty "a dirty clone"

desc="${RUN_ID}-state-unpushed"
path=$(begin_clone "$desc" "$repo")
printf 'unpushed\n' > "$path/tracked.txt"
git -C "$path" commit -qam "unpushed"
# Exercises the default-branch fallback (branch never pushed): a real local commit neither remote
# head is an ancestor of still refuses.
assert_list_matches_cleanup "$repo" "$path" unpushed "a clone with one local commit on an otherwise never-pushed branch"

# git cherry compares non-merge patches. A merge commit absent from the base
# must still block deletion even when both of its parents are already there.
repo="$HOME/work/list-state-merge"
init_repo "$repo" master
printf 'second base\n' > "$repo/tracked.txt"
git -C "$repo" commit -qam "second base"
git -C "$repo" push -q origin master
desc="${RUN_ID}-state-merge"
path=$(begin_clone "$desc" "$repo")
base_commit=$(git -C "$path" rev-parse HEAD)
base_parent=$(git -C "$path" rev-parse HEAD^)
base_tree=$(git -C "$path" rev-parse 'HEAD^{tree}')
merge_commit=$(printf 'local merge\n' | git -C "$path" commit-tree "$base_tree" -p "$base_commit" -p "$base_parent")
git -C "$path" reset -q --hard "$merge_commit"
assert_list_matches_cleanup "$repo" "$path" unpushed "a clone with an unpushed merge commit"

desc="${RUN_ID}-state-detached"
path=$(begin_clone "$desc" "$repo")
git -C "$path" checkout -q --detach
assert_list_matches_cleanup "$repo" "$path" detached "a clone with a detached HEAD"

# A deleted clone is 'missing', never 'prunable': no worktree-prune side channel exists, so cleanup
# drops the ledger entry itself; the ledger match must happen before resolveGitRepo would refuse.
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

# A deliberately stale local remote-tracking ref must not block a clean clone: unpushedCommitsForClone
# asks the real remote via ls-remote, never the local ref.
repo="$HOME/work/cleanup-fetches-first"
init_repo "$repo" master
desc="${RUN_ID}-cleanup-fetches-first"
path=$(begin_clone "$desc" "$repo")
printf 'relayed\n' > "$path/tracked.txt"
git -C "$path" commit -qam "relayed elsewhere"
git -C "$path" push -q origin "HEAD:refs/heads/agent/$desc"
git -C "$path" branch -q --set-upstream-to="origin/agent/$desc"
stale=$(git -C "$path" rev-parse HEAD^)
git -C "$path" update-ref "refs/remotes/origin/agent/$desc" "$stale"
capture "$ALLOD" change cleanup "$path"
assert_status 0 \
  "cleanup asks the real remote directly, so a stale local tracking ref does not block a clean clone"
[[ ! -d "$path" ]] &&
  pass "cleanup removes the clone once the real remote shows it clean" ||
  fail "cleanup removes the clone once the real remote shows it clean" "still exists: $path"

# A rebase-merged, forge-deleted branch (allod/tools#297): the merge lands under a
# new hash the clone never fetched; cleanup must fetch and judge by patch, not hash.
repo="$HOME/work/cleanup-rebase-merge"
init_repo "$repo" master
desc="${RUN_ID}-cleanup-rebase-merge"
path=$(begin_clone "$desc" "$repo")
printf 'rebase-merged\n' > "$path/tracked.txt"
git -C "$path" commit -qam "rebase-merged work"
git -C "$path" push -q origin "HEAD:refs/heads/agent/$desc"
# The forge side: apply the patch onto master under a new hash, delete the branch.
git -C "$repo" fetch -q origin "agent/$desc"
git -C "$repo" checkout -q -B "forge-rebase-${RUN_ID}" master
git -C "$repo" cherry-pick FETCH_HEAD
git -C "$repo" commit -q --amend -m 'rebase-merged work (forge)'
git -C "$repo" push -q origin "HEAD:refs/heads/master"
git -C "$repo" push -q origin --delete "agent/$desc"
git -C "$repo" checkout -q master
git -C "$repo" branch -qD "forge-rebase-${RUN_ID}"
[[ "$(git -C "$path" rev-parse HEAD)" != "$(git -C "$repo" rev-parse origin/master)" ]] &&
  pass "the fixture rebase-merge really produced a hash the clone has never seen" ||
  fail "the fixture rebase-merge really produced a hash the clone has never seen" 'identical hashes: the fixture proves nothing'
capture "$ALLOD" change list "$repo"
assert_contains "$CAPTURE_OUTPUT" 'unknown' \
  'list without a fetch reports the never-fetched rewrite as unknown, not clean'
capture "$ALLOD" change cleanup "$path"
assert_status 0 \
  "cleanup fetches, so a rebase-merged branch deleted on origin cleans up instead of dying on a missing object"
[[ ! -d "$path" ]] &&
  pass "cleanup removes the rebase-merged clone" ||
  fail "cleanup removes the rebase-merged clone" "still exists: $path"

# A forged local remote-tracking ref claiming an unpushed branch already landed must not fool
# cleanup either: only the real remote is trusted.
repo="$HOME/work/unpushed-local-ref-lie"
init_repo "$repo" master
desc="${RUN_ID}-unpushed-local-ref-lie"
path=$(begin_clone "$desc" "$repo")
printf 'real work\n' > "$path/tracked.txt"
git -C "$path" commit -qam "real work, never pushed"
git -C "$path" update-ref "refs/remotes/origin/agent/$desc" "$(git -C "$path" rev-parse HEAD)"
capture "$ALLOD" change cleanup "$path"
assert_status 1 "cleanup refuses a clone whose only evidence of being pushed is its own local remote-tracking ref"
assert_contains "$CAPTURE_OUTPUT" "unpushed" "the refusal says the branch is unpushed, not clean"
[[ -d "$path" ]] &&
  pass "the local-ref lie does not make cleanup discard real, unpushed work" ||
  fail "the local-ref lie does not make cleanup discard real, unpushed work" "missing: $path"
git -C "$path" push -q origin HEAD
capture "$ALLOD" change cleanup "$path"
assert_status 0 "cleanup succeeds once the branch is genuinely pushed"

# ls-remote genuinely unreachable (not just 'branch absent', exit 2) must read 'unknown', never
# pushed or clean; breaking origin identically in parent and clone keeps the origin check happy.
repo="$HOME/work/list-state-clone-unknown"
init_repo "$repo" master
desc="${RUN_ID}-state-clone-unknown"
path=$(begin_clone "$desc" "$repo")
broken_origin="$TMP/does-not-exist-${RUN_ID}.git"
git -C "$repo" remote set-url origin "$broken_origin"
git -C "$path" remote set-url origin "$broken_origin"
assert_list_matches_cleanup "$repo" "$path" unknown "a clone whose remote cannot even be reached"

# This fixture has no origin at all, which 'begin' now refuses outright, so it is built as a
# raw linked worktree instead.
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

# The config gate refuses a populated submodule (exit 11) for record/submit/cleanup/run, but
# 'list' reports that row 'unsafe' and keeps listing the rest.
sub_repo="$HOME/work/list-submodule"
init_repo_with_submodule "$sub_repo" master

desc="${RUN_ID}-state-sub-unpopulated"
path=$(begin_clone "$desc" "$sub_repo")
assert_equal "$(git -C "$path" status --porcelain)" "" "an unpopulated submodule leaves a clean status"
git -C "$path" push -q origin HEAD
assert_list_matches_cleanup "$sub_repo" "$path" clean "a clone whose submodule is unpopulated"

desc="${RUN_ID}-state-sub-populated"
path=$(begin_clone "$desc" "$sub_repo")
populate_submodules "$path"
assert_equal "$(git -C "$path" status --porcelain)" "" \
  "a populated submodule at its recorded commit leaves a clean status"
assert_equal "$(list_state_for "$sub_repo" "$path")" "unsafe" \
  "list reports unsafe, not a crash, for a clone with a populated submodule"
capture "$ALLOD" change cleanup "$path"
assert_status 11 "cleanup refuses a clone with a populated submodule, sandboxed clones do not support them"
[[ -d "$path" ]] &&
  pass "cleanup's submodule refusal preserves the clone" ||
  fail "cleanup's submodule refusal preserves the clone" "missing: $path"
# allod itself can never clean this one up with cleanup (it too refuses),
# so later tests against $sub_repo's ledger need it gone by hand.
sub_git_dir=$(git -C "$sub_repo" rev-parse --path-format=absolute --git-dir)
rm -rf "$path"
rm -f "$sub_git_dir/allod-changes/$desc"

desc="${RUN_ID}-state-sub-populated-alongside"
other_path=$(begin_clone "$desc" "$sub_repo")
populate_submodules "$other_path"
other_path2_desc="${RUN_ID}-state-sub-populated-sibling"
other_path2=$(begin_clone "$other_path2_desc" "$sub_repo")
git -C "$other_path2" push -q origin HEAD
capture "$ALLOD" change list "$sub_repo"
assert_status 0 "list does not die when one of several clones is unsafe"
assert_contains "$CAPTURE_OUTPUT" "$other_path	agent/$desc	unsafe" \
  "the unsafe clone's own row says so"
assert_contains "$CAPTURE_OUTPUT" "$other_path2	agent/$other_path2_desc	clean" \
  "a safe sibling clone still gets its own row"
rm -rf "$other_path"
rm -f "$sub_git_dir/allod-changes/$desc"
"$ALLOD" change cleanup "$other_path2" >/dev/null

# Precedence, one assertion per adjacent pair of the reporting order.

repo="$HOME/work/list-precedence"
init_repo "$repo" master

desc="${RUN_ID}-detached-dirty"
path=$(begin_clone "$desc" "$repo")
git -C "$path" checkout -q --detach
printf 'dirty\n' > "$path/tracked.txt"
assert_equal "$(list_state_for "$repo" "$path")" "detached" "list reports detached ahead of dirty"

desc="${RUN_ID}-submodule-dirty"
path=$(begin_clone "$desc" "$sub_repo")
populate_submodules "$path"
printf 'dirty\n' > "$path/tracked.txt"
assert_equal "$(list_state_for "$sub_repo" "$path")" "unsafe" \
  "list reports unsafe ahead of reporting dirty"
rm -rf "$path"
rm -f "$sub_git_dir/allod-changes/$desc"

desc="${RUN_ID}-dirty-unpushed"
path=$(begin_clone "$desc" "$repo")
printf 'committed\n' > "$path/tracked.txt"
git -C "$path" commit -qam "unpushed"
printf 'dirty\n' > "$path/tracked.txt"
assert_equal "$(list_state_for "$repo" "$path")" "dirty" "list reports dirty ahead of unpushed"

# legacy linked worktree tests (drain window)
# 'begin' makes no more of these; 'list'/'record'/'cleanup' must still handle one made before.

repo="$HOME/work/legacy-list"
init_repo "$repo" master

desc="${RUN_ID}-legacy-clean"
path=$(make_legacy_worktree "$desc" "$repo" master)
assert_list_matches_cleanup "$repo" "$path" clean "a fresh legacy worktree"

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

repo="$HOME/work/record-protected"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-record-protected"
path=$(begin_clone "$desc" "$repo")
printf 'changed\n' > "$path/tracked.txt"
capture record_in_repo "$path" -m "record protected clone"
assert_status 0 "record commits and pushes a protected repo's clone"
remote_has_branch "$repo" "agent/$desc" &&
  pass "record pushes a protected repo's clone's agent branch" ||
  fail "record pushes a protected repo's clone's agent branch"

# Switching the clone to the parent's protected branch is refused by the ledger check (exit 3)
# alone, even with the clone-owned handoff file removed entirely.
git -C "$path" checkout -q master
git_dir=$(git -C "$path" rev-parse --path-format=absolute --git-dir)
rm -f "$git_dir/allod-change-branch"
printf 'edit\n' > "$path/tracked.txt"
capture record_in_repo "$path" -m "must be refused"
assert_status 3 "record from a clone switched off agent/* is refused by the ledger check, handoff file or not"
assert_contains "$CAPTURE_OUTPUT" "agent/$desc" "the ledger refusal names the ledger's branch"
git -C "$path" checkout -q -- tracked.txt
git -C "$path" checkout -q "agent/$desc"

repo="$HOME/work/record-clone-origin-changed"
init_repo "$repo" master
desc="${RUN_ID}-origin-changed"
path=$(begin_clone "$desc" "$repo")
head_before=$(git -C "$path" rev-parse HEAD)
git -C "$path" remote set-url origin "https://forge.example/someone/else.git"
printf 'changed\n' > "$path/tracked.txt"
capture record_in_repo "$path" -m "must be refused"
assert_status 9 "record refuses a clone whose origin no longer matches its parent's"
assert_contains "$CAPTURE_OUTPUT" "https://forge.example/someone/else.git" \
  "the origin-mismatch refusal names the clone's origin"
assert_equal "$(git -C "$path" diff --cached --name-only)" "" \
  "the refused record staged nothing"
assert_equal "$(git -C "$path" rev-parse HEAD)" "$head_before" \
  "the refused record committed nothing"

repo="$HOME/work/record-rewritten-handoff"
init_repo "$repo" master
desc="${RUN_ID}-rewritten-handoff"
path=$(begin_clone "$desc" "$repo")
head_before=$(git -C "$path" rev-parse HEAD)
# record must refuse by the ledger's branch even when the clone-owned handoff file is rewritten
# to lie about it.
git -C "$path" checkout -q -b scratch
git_dir=$(git -C "$path" rev-parse --path-format=absolute --git-dir)
printf 'scratch\n' > "$git_dir/allod-change-branch"
printf 'changed\n' > "$path/tracked.txt"
capture record_in_repo "$path" -m "must be refused"
assert_status 3 "record refuses a clone on a branch its rewritten handoff file agrees with but its ledger does not"
assert_contains "$CAPTURE_OUTPUT" "agent/$desc" "the refusal names the ledger's branch, not the handoff file's"
assert_equal "$(git -C "$path" rev-parse HEAD)" "$head_before" \
  "the refused record committed nothing"

repo="$HOME/work/record-clone-pushurl-changed"
init_repo "$repo" master
desc="${RUN_ID}-pushurl-changed"
path=$(begin_clone "$desc" "$repo")
head_before=$(git -C "$path" rev-parse HEAD)
# 'git remote get-url origin' reports only the fetch URL; a push-URL-only redirect must still
# be caught.
git -C "$path" remote set-url --push origin "$TMP/attacker-pushurl.git"
printf 'changed\n' > "$path/tracked.txt"
capture record_in_repo "$path" -m "must be refused"
assert_status 9 "record refuses a clone whose push URL no longer matches its parent's"
assert_contains "$CAPTURE_OUTPUT" "$TMP/attacker-pushurl.git" \
  "the push-url mismatch refusal names the clone's push URL"
assert_equal "$(git -C "$path" rev-parse HEAD)" "$head_before" \
  "the refused record committed nothing"

repo="$HOME/work/record-clone-second-pushurl"
init_repo "$repo" master
desc="${RUN_ID}-second-pushurl"
path=$(begin_clone "$desc" "$repo")
head_before=$(git -C "$path" rev-parse HEAD)
# 'get-url --push' without '--all' shows only the first configured push URL;
# 'git push' pushes to every one, so a second, added URL must not go unseen.
git -C "$path" remote set-url --add --push origin "$TMP/attacker-second-pushurl.git"
printf 'changed\n' > "$path/tracked.txt"
capture record_in_repo "$path" -m "must be refused"
assert_status 9 "record refuses a clone with a second push URL the parent never had"
assert_contains "$CAPTURE_OUTPUT" "$TMP/attacker-second-pushurl.git" \
  "the second-push-url refusal names it"
assert_equal "$(git -C "$path" rev-parse HEAD)" "$head_before" \
  "the refused record committed nothing"

repo="$HOME/work/submit-clone-pushurl-changed"
init_repo "$repo" master
desc="${RUN_ID}-submit-pushurl-changed"
path=$(begin_clone "$desc" "$repo")
git -C "$path" remote set-url --push origin "$TMP/attacker-pushurl-2.git"
capture submit_in_repo "$path" -t "must be refused" --dry-run
assert_status 9 "submit refuses a clone whose push URL no longer matches its parent's"

repo="$HOME/work/record-protected-default"
init_repo "$repo" master
protect_repo "$repo" master
printf 'changed\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "blocked"
assert_status 2 "record refuses protected branch before staging"
git -C "$repo" diff --cached --quiet &&
  pass "record leaves index untouched when protected branch is refused" ||
  fail "record leaves index untouched when protected branch is refused"

repo="$HOME/work/record-moved-checkout"
init_repo "$repo" master
desc="${RUN_ID}-moved-checkout"
path=$(begin_clone "$desc" "$repo")
git -C "$path" switch -q -c some/other-branch
printf 'changed\n' > "$path/tracked.txt"
status_before=$(git -C "$path" status --porcelain)
head_before=$(git -C "$path" rev-parse HEAD)
capture record_in_repo "$path" -m "moved checkout"
assert_status 3 "record refuses a checkout moved off the branch begin handed out"
assert_contains "$CAPTURE_OUTPUT" "agent/$desc" "record names the branch begin handed out"
assert_contains "$CAPTURE_OUTPUT" "some/other-branch" "record names the branch actually checked out"
assert_equal "$(git -C "$path" status --porcelain)" "$status_before" \
  "record leaves a moved checkout's working tree unchanged"
assert_equal "$(git -C "$path" rev-parse HEAD)" "$head_before" \
  "record makes no commit in a moved checkout"

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
capture record_in_repo "$path" -m "clone sweep unaffected"
assert_status 0 "record still stages tracked modifications in a clone with no --files"
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
capture record_in_repo "$path" -m "bare dashdash" --
assert_status 0 "record accepts a bare -- with no path"
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
capture record_in_repo "$path" -m "tracked changes"
assert_status 1 "record refuses untracked files when staging tracked modifications in a clone"
assert_contains "$CAPTURE_OUTPUT" "untracked.txt" "record names the untracked file it refuses to leave out"
git -C "$path" diff --cached --quiet &&
  pass "record stages nothing when untracked files block it" ||
  fail "record stages nothing when untracked files block it"

rm "$path/untracked.txt"
capture record_in_repo "$path" -m "tracked changes"
assert_status 0 "record without -f stages tracked modifications in a clone"
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

# A worker's own clone config can run a command the moment anything outside the sandbox next
# touches it; record/submit/list/cleanup must all refuse first.

repo="$HOME/work/config-gate-credential"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-credential"
path=$(begin_clone "$desc" "$repo")
pwned="$TMP/pwned-${RUN_ID}"
git -C "$path" config credential.helper "!touch $pwned"

capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a clone whose config could run a command"
assert_contains "$CAPTURE_OUTPUT" "credential.helper" "the config refusal names the unsafe key"
[[ ! -e "$pwned" ]] &&
  pass "record's config refusal never ran the planted command" ||
  fail "record's config refusal never ran the planted command" "found: $pwned"

# resolveGitRepo used to run 'git rev-parse --show-toplevel' against the target before the ledger
# matched; the logging git proves that call no longer happens before the gate's refusal.
git_call_log="$TMP/git-call-log-${RUN_ID}"
: > "$git_call_log"
logging_git_bin=$(make_logging_git "$git_call_log")
saved_path_for_log="$PATH"
export PATH="$logging_git_bin:$PATH"
capture record_in_repo "$path" -m "must be refused"
export PATH="$saved_path_for_log"
assert_status 11 "record (logging git) still refuses the clone whose config could run a command"
# Other repos' own ledger-index walk legitimately runs 'show-toplevel' on their own directories;
# what must never appear is that call naming this clone.
toplevel_against_clone=$(grep -F -- "show-toplevel" "$git_call_log" | grep -F -- "$path" || true)
[[ -z "$toplevel_against_clone" ]] &&
  pass "no 'git rev-parse --show-toplevel' runs against the clone before the config gate's refusal" ||
  fail "no 'git rev-parse --show-toplevel' runs against the clone before the config gate's refusal" \
    "$toplevel_against_clone"

capture submit_in_repo "$path" -t "must be refused" --dry-run
assert_status 11 "submit refuses a clone whose config could run a command"
[[ ! -e "$pwned" ]] &&
  pass "submit's config refusal never ran the planted command" ||
  fail "submit's config refusal never ran the planted command" "found: $pwned"

assert_equal "$(list_state_for "$repo" "$path")" "unsafe" \
  "list reports unsafe, not a crash, for a clone whose config could run a command"
[[ ! -e "$pwned" ]] &&
  pass "list's own check never ran the planted command" ||
  fail "list's own check never ran the planted command" "found: $pwned"

capture "$ALLOD" change cleanup "$path"
assert_status 11 "cleanup refuses a clone whose config could run a command"
[[ ! -e "$pwned" ]] &&
  pass "cleanup's config refusal never ran the planted command" ||
  fail "cleanup's config refusal never ran the planted command" "found: $pwned"
[[ -d "$path" ]] &&
  pass "cleanup's config refusal preserves the clone" ||
  fail "cleanup's config refusal preserves the clone" "missing: $path"
git -C "$path" config --unset credential.helper
git -C "$path" push -q origin HEAD
capture "$ALLOD" change cleanup "$path"
assert_status 0 "cleanup succeeds once the unsafe config key is gone"

repo="$HOME/work/config-gate-hook"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-hook"
path=$(begin_clone "$desc" "$repo")
pwned_hook="$TMP/pwned-hook-${RUN_ID}"
hook_dir=$(git -C "$path" rev-parse --path-format=absolute --git-dir)/hooks
mkdir -p "$hook_dir"
printf '#!/bin/sh\ntouch "%s"\n' "$pwned_hook" > "$hook_dir/pre-push"
chmod +x "$hook_dir/pre-push"
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a clone with an added executable hook"
assert_contains "$CAPTURE_OUTPUT" "pre-push" "the hook refusal names the hook"
[[ ! -e "$pwned_hook" ]] &&
  pass "the planted hook never ran" ||
  fail "the planted hook never ran" "found: $pwned_hook"

repo="$HOME/work/config-gate-allowlisted"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-allowlisted"
path=$(begin_clone "$desc" "$repo")
# Only the ledger branch's own remote/merge, pinned to the one value begin
# itself sets, are allowed; re-asserting that exact value is harmless.
git -C "$path" config "branch.agent/$desc.remote" origin
git -C "$path" config "branch.agent/$desc.merge" "refs/heads/agent/$desc"
printf 'edit\n' > "$path/tracked.txt"
capture record_in_repo "$path" -m "allowlisted config keys still work"
assert_status 0 "record succeeds when the clone's config carries only allowlisted keys"

repo="$HOME/work/config-gate-rebase-refused"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-rebase-refused"
path=$(begin_clone "$desc" "$repo")
git -C "$path" config "branch.agent/$desc.rebase" true
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses branch.<ledger branch>.rebase, not on the allowlist"

repo="$HOME/work/config-gate-other-branch-refused"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-other-branch-refused"
path=$(begin_clone "$desc" "$repo")
git -C "$path" config "branch.someother.remote" origin
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a branch.* key for a branch that is not the ledger's own"

repo="$HOME/work/config-gate-wrong-remote-refused"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-wrong-remote-refused"
path=$(begin_clone "$desc" "$repo")
git -C "$path" config "branch.agent/$desc.remote" .
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses branch.<ledger branch>.remote set to anything but origin"

# extensions.refstorage/.objectformat pass the allowlist since git writes them itself; faking a
# reftable/sha256 repo via 'git config' alone would make git misbehave unrelated to this gate.

repo="$HOME/work/config-gate-extensions-worktreeconfig-refused"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-extensions-worktreeconfig-refused"
path=$(begin_clone "$desc" "$repo")
git -C "$path" config extensions.worktreeconfig true
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses extensions.worktreeconfig"

repo="$HOME/work/config-gate-extensions-other-refused"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-extensions-other-refused"
path=$(begin_clone "$desc" "$repo")
git -C "$path" config extensions.somethingelse true
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses an extensions.* key that is not on the allowlist"

# remote.origin.fetch and core.bare are pinned to the one value an ordinary clone has, never
# accepted with any value, or a worker could redirect fetch or go bare.
repo="$HOME/work/config-gate-fetch-refspec"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-fetch-refspec"
path=$(begin_clone "$desc" "$repo")
git -C "$path" config remote.origin.fetch "+refs/heads/agent/*:refs/remotes/origin/agent/*"
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a remote.origin.fetch refspec other than the one an ordinary clone has"

repo="$HOME/work/config-gate-core-bare"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-core-bare"
path=$(begin_clone "$desc" "$repo")
git -C "$path" config core.bare true
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses core.bare set to true"

# A '.git/config' 'git config --file --list' itself cannot even read must
# never be treated as an empty, safe one: refuse, not pass.
repo="$HOME/work/config-gate-unreadable-config"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-unreadable-config"
path=$(begin_clone "$desc" "$repo")
printf '[section\nkey = value\n' > "$path/.git/config"
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a clone whose .git/config git itself cannot parse"
assert_contains "$CAPTURE_OUTPUT" "could not read" "the refusal names the parse failure"

repo="$HOME/work/config-gate-gitdir-swap"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-gitdir-swap"
path=$(begin_clone "$desc" "$repo")
# A worker can replace '.git' with a gitfile pointing at a directory it controls; this gate never
# reads it unless '.git' is an ordinary directory.
mv "$path/.git" "$path/.gitdir"
printf 'gitdir: %s/.gitdir\n' "$path" > "$path/.git"
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a clone whose .git is a gitfile redirect"
capture submit_in_repo "$path" -t "must be refused" --dry-run
assert_status 11 "submit refuses a clone whose .git is a gitfile redirect"
assert_equal "$(list_state_for "$repo" "$path")" "unsafe" \
  "list reports unsafe, not a crash, for a clone whose .git is a gitfile redirect"
capture "$ALLOD" change cleanup "$path"
assert_status 11 "cleanup refuses a clone whose .git is a gitfile redirect"
# allod can never act on this clone again either, so it is reclaimed by hand.
gitdir_swap_main=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
rm -rf "$path"
rm -f "$gitdir_swap_main/allod-changes/$desc"

# A linked worktree's own 'commondir' file redirects git to load config/refs/hooks from another
# directory this gate never reads, so it refuses outright before looking.
repo="$HOME/work/config-gate-commondir"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-commondir"
path=$(begin_clone "$desc" "$repo")
commondir_sibling="$TMP/commondir-sibling-${RUN_ID}"
commondir_pwned="$TMP/commondir-pwned-${RUN_ID}"
mkdir -p "$commondir_sibling"
printf '[credential]\n\thelper = "!touch %s"\n' "$commondir_pwned" > "$commondir_sibling/config"
printf '%s\n' "$commondir_sibling" > "$path/.git/commondir"
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a clone whose .git/commondir redirects to another directory"
assert_contains "$CAPTURE_OUTPUT" "commondir" "the refusal names the commondir file"
capture "$ALLOD" change cleanup "$path"
assert_status 11 "cleanup refuses a clone whose .git/commondir redirects to another directory"
[[ ! -e "$commondir_pwned" ]] &&
  pass "the commondir refusal never read, let alone ran, the sibling's config" ||
  fail "the commondir refusal never read, let alone ran, the sibling's config" "found: $commondir_pwned"
commondir_main=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
rm -rf "$path"
rm -f "$commondir_main/allod-changes/$desc"

# The same for a bare 'gitdir' file (the other half of a linked worktree's
# '.git' admin directory, pointing the other way).
repo="$HOME/work/config-gate-gitdir-file"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-gitdir-file"
path=$(begin_clone "$desc" "$repo")
printf 'gitdir: %s/elsewhere\n' "$TMP" > "$path/.git/gitdir"
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a clone whose .git/gitdir names another directory"
assert_contains "$CAPTURE_OUTPUT" "gitdir" "the refusal names the gitdir file"
gitdir_file_main=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
rm -rf "$path"
rm -f "$gitdir_file_main/allod-changes/$desc"

# The allowlist read of '.git/config' must run before hasPopulatedSubmodule's own 'git ls-files',
# a real git invocation that loads the clone's full config.
repo="$HOME/work/config-gate-order-submodule-then-config"
init_repo "$repo" master
desc="${RUN_ID}-config-gate-order-submodule-then-config"
path=$(begin_clone "$desc" "$repo")
populate_submodules "$path"
pwned_order="$TMP/pwned-order-${RUN_ID}"
git -C "$path" config credential.helper "!touch $pwned_order"
order_log="$TMP/git-call-log-order-${RUN_ID}"
: > "$order_log"
order_logging_git_bin=$(make_logging_git "$order_log")
saved_path_for_order="$PATH"
export PATH="$order_logging_git_bin:$PATH"
capture record_in_repo "$path" -m "must be refused"
export PATH="$saved_path_for_order"
assert_status 11 "record refuses a clone with both a populated submodule and a hostile config"
assert_contains "$CAPTURE_OUTPUT" "credential.helper" \
  "the hostile config is named, not the submodule: the config check runs first"
ls_files_against_clone=$(grep -F -- "ls-files" "$order_log" | grep -F -- "$path" || true)
[[ -z "$ls_files_against_clone" ]] &&
  pass "hasPopulatedSubmodule's own 'git ls-files' never runs against a clone whose config already failed the gate" ||
  fail "hasPopulatedSubmodule's own 'git ls-files' never runs against a clone whose config already failed the gate" \
    "$ls_files_against_clone"
[[ ! -e "$pwned_order" ]] &&
  pass "the planted credential.helper command never ran" ||
  fail "the planted credential.helper command never ran" "found: $pwned_order"
order_main=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
rm -rf "$path"
rm -f "$order_main/allod-changes/$desc"

# git-hooks/protected-refs-policy runs a tracked hook by executable-on-disk status, not git
# tracking, straight from the clone's working tree; the gate refuses unless it matches the parent.
repo="$HOME/work/tracked-hooks-added"
init_repo "$repo" master
desc="${RUN_ID}-tracked-hooks-added"
path=$(begin_clone "$desc" "$repo")
mkdir -p "$path/.hooks"
tracked_hook_pwned="$TMP/tracked-hook-pwned-${RUN_ID}"
printf '#!/bin/sh\ntouch %s\n' "$tracked_hook_pwned" > "$path/.hooks/pre-commit"
chmod +x "$path/.hooks/pre-commit"
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a clone with an added tracked hook (.hooks/pre-commit) the parent's origin does not have"
assert_contains "$CAPTURE_OUTPUT" "tracked hooks" "the refusal names the tracked hooks directory"
capture submit_in_repo "$path" -t "must be refused" --dry-run
assert_status 11 "submit refuses the same clone"
assert_equal "$(list_state_for "$repo" "$path")" "unsafe" \
  "list reports unsafe, not a crash, for a clone with an added tracked hook"
[[ ! -e "$tracked_hook_pwned" ]] &&
  pass "the planted tracked hook never ran" ||
  fail "the planted tracked hook never ran" "found: $tracked_hook_pwned"
tracked_hooks_added_main=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
rm -rf "$path"
rm -f "$tracked_hooks_added_main/allod-changes/$desc"

# A repo whose origin already tracks '.hooks/pre-commit': an unmodified
# clone passes, a modified one is refused.
repo="$HOME/work/tracked-hooks-base"
init_repo "$repo" master
mkdir -p "$repo/.hooks"
printf '#!/bin/sh\necho ok\n' > "$repo/.hooks/pre-commit"
chmod +x "$repo/.hooks/pre-commit"
# The dispatcher only runs a tracked hook that is executable on disk, so this one must stay inert
# unless a clone's copy's mode diverges.
printf '#!/bin/sh\necho ok\n' > "$repo/.hooks/pre-push"
chmod -x "$repo/.hooks/pre-push"
git -C "$repo" add .hooks/pre-commit .hooks/pre-push
git -C "$repo" commit -qm "add a tracked pre-commit hook"
git -C "$repo" push -q origin master
git -C "$repo" fetch -q origin

desc="${RUN_ID}-tracked-hooks-identical"
path=$(begin_clone "$desc" "$repo")
printf 'more\n' >> "$path/tracked.txt"
capture record_in_repo "$path" -m "an identical tracked hook does not block a legitimate commit"
assert_status 0 "record succeeds when the clone's tracked hook is byte-identical to the parent's origin"
tracked_hooks_identical_main=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
"$ALLOD" change cleanup "$path" >/dev/null 2>&1 || rm -rf "$path"
rm -f "$tracked_hooks_identical_main/allod-changes/$desc"

desc="${RUN_ID}-tracked-hooks-modified"
path=$(begin_clone "$desc" "$repo")
printf '#!/bin/sh\ntouch %s/tracked-hook-pwned-modified-%s\n' "$TMP" "$RUN_ID" > "$path/.hooks/pre-commit"
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a clone whose existing tracked hook was modified from the parent's origin"
tracked_hooks_modified_main=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
rm -rf "$path"
rm -f "$tracked_hooks_modified_main/allod-changes/$desc"

# Byte-identical content is not enough: a chmod +x with no git command at all still makes the
# dispatcher run it.
desc="${RUN_ID}-tracked-hooks-chmod"
path=$(begin_clone "$desc" "$repo")
[[ ! -x "$path/.hooks/pre-push" ]] &&
  pass "the clone's own copy of .hooks/pre-push starts non-executable, matching the parent's origin" ||
  fail "the clone's own copy of .hooks/pre-push starts non-executable, matching the parent's origin"
chmod +x "$path/.hooks/pre-push"
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a clone whose tracked hook was chmod +x'd, even with unchanged content"
assert_contains "$CAPTURE_OUTPUT" "executable bit" "the refusal names the mode mismatch"
tracked_hooks_chmod_main=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
rm -rf "$path"
rm -f "$tracked_hooks_chmod_main/allod-changes/$desc"

# A '.hookspath' the parent's own working tree does not have: refused
# outright, regardless of what it names.
desc="${RUN_ID}-tracked-hookspath-added"
path=$(begin_clone "$desc" "$repo")
printf '.hooks\n' > "$path/.hookspath"
capture record_in_repo "$path" -m "must be refused"
assert_status 11 "record refuses a clone that adds a .hookspath the parent does not have"
assert_contains "$CAPTURE_OUTPUT" "hookspath" "the refusal names the hookspath mismatch"
tracked_hookspath_added_main=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
rm -rf "$path"
rm -f "$tracked_hookspath_added_main/allod-changes/$desc"

repo="$HOME/work/active-scope"
init_repo "$repo" master
desc="${RUN_ID}-active-scope"
path=$(begin_clone "$desc" "$repo")
# listRow must hold its lock through every one of its own git calls, not just probe and release;
# one locked entry must not block or corrupt another's row.
other_desc="${RUN_ID}-active-scope-sibling"
other_path=$(begin_clone "$other_desc" "$repo")
printf 'edit\n' > "$path/tracked.txt"
# Closes the window between the gate reading the clone and the git command that follows: outside
# commands refuse while the ledger file is locked, with no systemctl involved at all.
active_scope_ledger_file="$(git -C "$repo" rev-parse --path-format=absolute --git-dir)/allod-changes/$desc"
exec {active_scope_lock_fd}>>"$active_scope_ledger_file"
flock -x "$active_scope_lock_fd"
capture record_in_repo "$path" -m "must be refused"
assert_status 13 "record refuses while the clone's own ledger file is locked"
assert_contains "$CAPTURE_OUTPUT" "locked" "the lock refusal names what it found"
capture submit_in_repo "$path" -t "must be refused" --dry-run
assert_status 13 "submit refuses while the clone's own ledger file is locked"
capture "$ALLOD" change cleanup "$path"
assert_status 13 "cleanup refuses while the clone's own ledger file is locked"
capture "$ALLOD" change list "$path"
assert_contains "$CAPTURE_OUTPUT" $'\trunning' "explicit clone list honors the ledger lock"
[[ -d "$path" ]] &&
  pass "cleanup's active-lock refusal preserves the clone" ||
  fail "cleanup's active-lock refusal preserves the clone" "missing: $path"
assert_equal "$(list_state_for "$repo" "$path")" "running" \
  "list reports running, never runs git at all, while the clone's own ledger file is locked"
assert_equal "$(list_state_for "$repo" "$other_path")" "clean" \
  "the sibling clone's own row is unaffected: one locked ledger entry does not block or corrupt another's, in the same 'list' call"
exec {active_scope_lock_fd}>&-
capture record_in_repo "$path" -m "record once the lock is released"
assert_status 0 "record succeeds once the clone's ledger file is no longer locked"

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

# A fake git failing only 'status' isolates cleanup's own dirty-check failure from
# hasPopulatedSubmodule's, which a wholesale-broken git would trip first instead.
repo="$HOME/work/cleanup-status-fails"
init_repo "$repo" master
desc="${RUN_ID}-cleanup-status-fails"
path=$(begin_clone "$desc" "$repo")
export REAL_GIT
mock_git_path=$(make_mock_git_path cleanup-status-fails fail-status)
capture_with_path "$mock_git_path" "$ALLOD" change cleanup "$path"
assert_status 1 "cleanup refuses rather than treat a failed 'git status' as clean"
assert_contains "$CAPTURE_OUTPUT" "could not check whether clone" "the status-failure refusal names it"
[[ -d "$path" ]] &&
  pass "the status-failure refusal preserves the clone" ||
  fail "the status-failure refusal preserves the clone" "missing: $path"
rm -rf "$path"
status_fails_main=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
rm -f "$status_fails_main/allod-changes/$desc"

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

repo="$HOME/work/cleanup-clean"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-cleanup-clean"
path=$(begin_clone "$desc" "$repo")
git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
ledger_file="$git_dir/allod-changes/$desc"
push_log="$TMP/cleanup-push.log"
: > "$push_log"
# Pushed once with the real git before the mock takes over, since unpushedCommitsForClone now
# asks the real remote directly rather than falling back to origin's default branch.
git -C "$path" push -q origin HEAD
export REAL_GIT GIT_PUSH_LOG="$push_log"
mock_git_path=$(make_mock_git_path cleanup)
capture_with_path "$mock_git_path" "$ALLOD" change cleanup "$path"
assert_status 0 "cleanup removes a clean clone"
[[ ! -d "$path" ]] &&
  pass "cleanup deletes the clone directory" ||
  fail "cleanup deletes the clone directory" "still exists: $path"
[[ ! -e "$ledger_file" ]] &&
  pass "cleanup drops the parent's ledger entry" ||
  fail "cleanup drops the parent's ledger entry" "still exists: $ledger_file"
assert_equal "$(cat "$push_log")" "" "cleanup does not push anything"

repo="$HOME/work/cleanup-pushurl-changed"
init_repo "$repo" master
desc="${RUN_ID}-cleanup-pushurl-changed"
path=$(begin_clone "$desc" "$repo")
# A worker can repoint origin's push URL at a bare repo inside its own
# clone and push there; cleanup must not then delete the only copy.
git -C "$path" remote set-url --push origin "$TMP/attacker-cleanup-pushurl.git"
capture "$ALLOD" change cleanup "$path"
assert_status 9 "cleanup refuses a clone whose push URL no longer matches its parent's"
[[ -d "$path" ]] &&
  pass "cleanup over a push-url mismatch preserves the clone" ||
  fail "cleanup over a push-url mismatch preserves the clone" "missing: $path"
# 'list' must never call this clone 'clean': that promises cleanup would
# succeed, and cleanup refuses it outright (exit 9, just above).
assert_equal "$(list_state_for "$repo" "$path")" "unsafe" \
  "list reports unsafe, not clean, for a clone whose push URL no longer matches its parent's"

repo="$HOME/work/cleanup-branch-changed"
init_repo "$repo" master
desc="${RUN_ID}-cleanup-branch-changed"
path=$(begin_clone "$desc" "$repo")
printf 'would be discarded\n' > "$path/tracked.txt"
git -C "$path" commit -qam "unpushed on the ledgered branch"
# cleanup must judge and ever discard only the ledgered branch, never whatever is checked out,
# even after a worker switches to a different, clean, already-pushed one.
git -C "$path" checkout -q -b "agent/${desc}-elsewhere" --no-track origin/master
assert_equal "$(list_state_for "$repo" "$path")" "moved" \
  "list reports moved, not clean, for a clone checked out on a branch its ledger does not list"
capture "$ALLOD" change cleanup "$path"
assert_status 3 "cleanup refuses a clone checked out on a branch its ledger does not list"
[[ -d "$path" ]] &&
  pass "cleanup over a branch mismatch preserves the clone" ||
  fail "cleanup over a branch mismatch preserves the clone" "missing: $path"
git -C "$path" checkout -q "agent/$desc"
assert_equal "$(git -C "$path" log -1 --format=%s)" "unpushed on the ledgered branch" \
  "the ledgered branch's unpushed commit survives the refused cleanup"

repo="$HOME/work/cleanup-dirty"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-cleanup-dirty"
path=$(begin_clone "$desc" "$repo")
printf 'dirty\n' > "$path/tracked.txt"
capture "$ALLOD" change cleanup "$path"
[[ "$CAPTURE_STATUS" -ne 0 ]] || fail "cleanup refuses a dirty clone" "$CAPTURE_OUTPUT"
assert_contains "$CAPTURE_OUTPUT" "dirty" "cleanup dirty failure is actionable"

repo="$HOME/work/cleanup-unpushed"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-cleanup-unpushed"
path=$(begin_clone "$desc" "$repo")
printf 'unpushed\n' > "$path/tracked.txt"
git -C "$path" commit -qam "unpushed"
capture "$ALLOD" change cleanup "$path"
[[ "$CAPTURE_STATUS" -ne 0 ]] || fail "cleanup refuses unpushed worktree" "$CAPTURE_OUTPUT"
assert_contains "$CAPTURE_OUTPUT" "unpushed" "cleanup unpushed failure is actionable"

# GIT_NO_REPLACE_OBJECTS defeats a replace ref that would make an unrelated remote head look
# already landed; cloneConfigUnsafeReason's own refs/replace check now refuses even earlier.
repo="$HOME/work/cleanup-replace-ref"
init_repo "$repo" master
desc="${RUN_ID}-cleanup-replace-ref"
path=$(begin_clone "$desc" "$repo")
printf 'real unpushed work\n' > "$path/tracked.txt"
git -C "$path" commit -qam "real unpushed work"
head_sha=$(git -C "$path" rev-parse HEAD)
# An orphan commit under the ledger branch's own name is genuinely not an ancestor of HEAD, so
# this reads unpushed without the replace ref.
orphan_sha=$(git -C "$path" commit-tree "$(git -C "$path" rev-parse HEAD^{tree})" -m "orphan remote head")
git -C "$path" push -q origin "$orphan_sha:refs/heads/agent/$desc"
# The replace ref substitutes a fake commit descending from HEAD, so walking the replaced
# object's parents (not its own top-level identity) would wrongly find HEAD reachable.
fake_sha=$(git -C "$path" commit-tree "$(git -C "$path" rev-parse HEAD^{tree})" -p "$head_sha" -m "fake descendant of HEAD")
git -C "$path" replace "$orphan_sha" "$fake_sha"
capture "$ALLOD" change cleanup "$path"
assert_status 11 "cleanup refuses outright the moment a replace ref exists, via the config gate, before judging the branch pushed or not at all"
assert_contains "$CAPTURE_OUTPUT" "refs/replace" "the config-gate refusal names refs/replace"
[[ -d "$path" ]] &&
  pass "the replace-ref attempt does not make cleanup discard real, unpushed work" ||
  fail "the replace-ref attempt does not make cleanup discard real, unpushed work" "missing: $path"
git -C "$path" replace -d "$orphan_sha"

# With the replace ref gone, this still correctly reports unpushed, now on its own merits rather
# than the config gate's earlier refusal.
capture "$ALLOD" change cleanup "$path"
assert_status 1 "cleanup now reaches the real unpushed check once the replace ref is gone"
assert_contains "$CAPTURE_OUTPUT" "unpushed" "cleanup reports the branch as unpushed on its own merits"
# The earlier orphan push diverged history, so a plain push is a non-fast-forward; force is fine
# on this disposable fixture.
git -C "$path" push -qf origin HEAD
capture "$ALLOD" change cleanup "$path"
assert_status 0 "cleanup succeeds once genuinely pushed"

# A graft line for HEAD's own commit, substituting its real parent for the remote head, makes
# HEAD falsely look descended from a commit it never was.
repo="$HOME/work/cleanup-grafts"
init_repo "$repo" master
desc="${RUN_ID}-cleanup-grafts"
path=$(begin_clone "$desc" "$repo")
printf 'real unpushed work\n' > "$path/tracked.txt"
git -C "$path" commit -qam "real unpushed work"
head_sha=$(git -C "$path" rev-parse HEAD)
orphan_sha=$(git -C "$path" commit-tree "$(git -C "$path" rev-parse HEAD^{tree})" -m "orphan remote head")
git -C "$path" push -q origin "$orphan_sha:refs/heads/agent/$desc"
mkdir -p "$path/.git/info"
printf '%s %s\n' "$head_sha" "$orphan_sha" > "$path/.git/info/grafts"
capture "$ALLOD" change cleanup "$path"
assert_status 11 "cleanup refuses outright the moment '.git/info/grafts' exists, via the config gate, before judging the branch pushed or not at all"
assert_contains "$CAPTURE_OUTPUT" "grafts" "the config-gate refusal names grafts"
[[ -d "$path" ]] &&
  pass "the graft does not make cleanup discard real, unpushed work" ||
  fail "the graft does not make cleanup discard real, unpushed work" "missing: $path"
rm -f "$path/.git/info/grafts"
capture "$ALLOD" change cleanup "$path"
assert_status 1 "cleanup now reaches the real unpushed check once the graft is gone"
assert_contains "$CAPTURE_OUTPUT" "unpushed" "cleanup reports the branch as unpushed on its own merits"
git -C "$path" push -qf origin HEAD
capture "$ALLOD" change cleanup "$path"
assert_status 0 "cleanup succeeds once genuinely pushed"

# '.git/shallow' is refused the same way without needing a genuinely shallow clone: a plain
# planted file exercises the same refusal.
repo="$HOME/work/cleanup-shallow"
init_repo "$repo" master
desc="${RUN_ID}-cleanup-shallow"
path=$(begin_clone "$desc" "$repo")
printf 'shallow-marker\n' > "$path/.git/shallow"
capture "$ALLOD" change cleanup "$path"
assert_status 11 "cleanup refuses outright the moment '.git/shallow' exists"
assert_contains "$CAPTURE_OUTPUT" "shallow" "the config-gate refusal names shallow"
rm -f "$path/.git/shallow"
git -C "$path" push -q origin HEAD
capture "$ALLOD" change cleanup "$path"
assert_status 0 "cleanup succeeds once '.git/shallow' is gone and the branch is pushed"

# A relayed commit lands on the default branch under a new hash, same patch: the
# clone reads as landed, by the same judgment as the rebase-merge case above.
repo="$HOME/work/cleanup-relayed"
init_repo "$repo" master
desc="${RUN_ID}-cleanup-relayed"
path=$(begin_clone "$desc" "$repo")
printf 'relayed\n' > "$path/tracked.txt"
git -C "$path" commit -qam "relayed change"
source_sha=$(git -C "$path" rev-parse HEAD)
patch_file="$TMP/relayed.patch"
git -C "$path" format-patch -1 --stdout > "$patch_file"
GIT_COMMITTER_DATE='2030-01-01T00:00:00Z' git -C "$repo" am -q "$patch_file"
relayed_sha=$(git -C "$repo" rev-parse HEAD)
[[ "$source_sha" != "$relayed_sha" ]] && pass "relay fixture rewrites the commit ID" ||
  fail "relay fixture rewrites the commit ID" "both commits: $source_sha"
git -C "$repo" push -q origin master
# A clone's refs/remotes/origin/* is its own, unshared with the parent, so it only learns of a
# relayed push once it fetches for itself.
git -C "$path" fetch -q origin
assert_list_matches_cleanup "$repo" "$path" clean "a patch-equivalent relayed clone reads as landed, not unpushed"

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
capture record_at "$alt_path" "$alt_home" -m "symlink home" --files tracked.txt
assert_status 0 "record from a clone succeeds when \$HOME is a symlink"
assert_not_contains "$CAPTURE_OUTPUT" "misplaced" \
  "a clone under a symlinked \$HOME is not read as a misplaced checkout of itself"

repo="$HOME/work/host-git-execution"
init_repo "$repo" master
desc="${RUN_ID}-host-git-execution"
path=$(begin_clone "$desc" "$repo")
host_hook_marker="$TMP/host-hook-ran"
mkdir -p "$path/host-hooks"
printf '#!/bin/sh\ntouch "%s"\n' "$host_hook_marker" > "$path/host-hooks/pre-commit"
chmod +x "$path/host-hooks/pre-commit"
git config --global core.hooksPath "$path/host-hooks"
printf 'changed\n' > "$path/tracked.txt"
capture record_in_repo "$path" -m "safe host Git" --files tracked.txt host-hooks/pre-commit
assert_status 0 "record succeeds despite a global hook pointing into worker files"
[[ ! -e "$host_hook_marker" ]] && pass "host Git never executes a worker-selected global hook" ||
  fail "host Git never executes a worker-selected global hook"
git config --global --unset core.hooksPath

filter_marker="$TMP/host-filter-ran"
mkdir -p "$path/scripts"
printf '#!/bin/sh\ntouch "%s"\ncat\n' "$filter_marker" > "$path/scripts/filter"
chmod +x "$path/scripts/filter"
git config --global filter.audit.clean ./scripts/filter
printf 'tracked.txt filter=audit\n' > "$path/.gitattributes"
printf 'filtered\n' > "$path/tracked.txt"
git -C "$path" add tracked.txt
[[ -e "$filter_marker" ]] && pass "sabotage: worker attributes can invoke a global filter outside the safe Git path" ||
  fail "sabotage: worker attributes can invoke a global filter outside the safe Git path"
rm -f "$filter_marker"
capture record_in_repo "$path" -m "safe attributes" --files tracked.txt .gitattributes scripts/filter
assert_status 0 "record succeeds with attributes naming a global filter"
[[ ! -e "$filter_marker" ]] && pass "host Git never executes the worker-selected global filter" ||
  fail "host Git never executes the worker-selected global filter"
git config --global --unset filter.audit.clean

logs_path="$path/.git/logs/HEAD"
mv "$logs_path" "$logs_path.saved"
ln -s "$TMP/reflog-target" "$logs_path"
capture "$ALLOD" change list "$path"
assert_contains "$CAPTURE_OUTPUT" $'\tunsafe' "explicit clone list refuses a reflog symlink"
capture record_in_repo "$path" -m "must refuse"
assert_status 11 "record refuses a reflog symlink before Git can append through it"
rm "$logs_path"
mv "$logs_path.saved" "$logs_path"

touch "$path/.git/objects/info/commit-graph"
capture "$ALLOD" change cleanup "$path"
assert_status 11 "cleanup refuses a worker-controlled commit graph"
rm "$path/.git/objects/info/commit-graph"

# The old gate walked only '.git/logs'; git writes many files under .git without a lockfile
# (FETCH_HEAD, packed-refs, etc.), so the walk now covers the whole tree.
outside_target="$TMP/outside-fetch-head-target"
printf 'untouched\n' > "$outside_target"
fetch_head_path="$path/.git/FETCH_HEAD"
rm -f "$fetch_head_path"
ln -s "$outside_target" "$fetch_head_path"
capture record_in_repo "$path" -m "must refuse"
assert_status 11 "record refuses a symlink at .git/FETCH_HEAD before git can write through it"
assert_contains "$CAPTURE_OUTPUT" "FETCH_HEAD" "the refusal names the symlinked file"
assert_equal "$(cat "$outside_target")" "untouched" \
  "the file outside the clone that FETCH_HEAD pointed to is untouched"
rm "$fetch_head_path"

# hasPopulatedSubmodule's own 'git ls-files --stage' failing must refuse,
# never silently read as 'no submodule' and let the gate pass.
repo="$HOME/work/submodule-check-fails"
init_repo "$repo" master
desc="${RUN_ID}-submodule-check-fails"
path=$(begin_clone "$desc" "$repo")
export REAL_GIT
mock_git_path=$(make_mock_git_path submodule-check-fails fail-ls-files)
capture_with_path "$mock_git_path" "$ALLOD" change cleanup "$path"
assert_status 11 "cleanup refuses rather than treat a failed 'git ls-files --stage' as 'no submodule'"
assert_contains "$CAPTURE_OUTPUT" "could not be checked for a populated submodule" \
  "the refusal names the submodule check failure"

# 'list' must never say 'clean' for a clone whose own 'git status --porcelain' it could not
# actually read.
repo="$HOME/work/list-status-fails"
init_repo "$repo" master
desc="${RUN_ID}-list-status-fails"
path=$(begin_clone "$desc" "$repo")
git -C "$path" push -qf origin HEAD
mock_git_path=$(make_mock_git_path list-status-fails fail-status)
capture_with_path "$mock_git_path" "$ALLOD" change list "$repo"
assert_contains "$CAPTURE_OUTPUT" $'\tunknown' \
  "list reports unknown, never clean, when its own 'git status --porcelain' fails"

# A failing 'git status --porcelain --ignored' must refuse, never silently skip the
# untracked/ignored check it was there to run.
repo="$HOME/work/tracked-hooks-status-fails"
init_repo "$repo" master
mkdir -p "$repo/.hooks"
printf '#!/bin/sh\necho ok\n' > "$repo/.hooks/pre-commit"
chmod +x "$repo/.hooks/pre-commit"
git -C "$repo" add .hooks/pre-commit
git -C "$repo" commit -qm "add a tracked pre-commit hook"
git -C "$repo" push -q origin master
git -C "$repo" fetch -q origin
desc="${RUN_ID}-tracked-hooks-status-fails"
path=$(begin_clone "$desc" "$repo")
mock_git_path=$(make_mock_git_path tracked-hooks-status-fails fail-status)
capture_with_path "$mock_git_path" "$ALLOD" change cleanup "$path"
assert_status 11 "cleanup refuses rather than skip the tracked-hooks untracked/ignored check when 'git status' fails"
assert_contains "$CAPTURE_OUTPUT" "could not be read" "the refusal names the tracked-hooks status failure"

# A clone's own refs/remotes/origin/HEAD is worker-writable; submit must resolve the PR base
# against the parent's real default branch instead.
repo="$HOME/work/submit-base-from-parent"
init_repo "$repo" master
git -C "$repo" checkout -qb other-default master
git -C "$repo" push -q origin other-default
git -C "$repo" checkout -q master
desc="${RUN_ID}-submit-base-from-parent"
path=$(begin_clone "$desc" "$repo")
git -C "$path" remote set-head origin other-default
capture submit_in_repo "$path" -t "base from parent" -b "body" --dry-run
assert_status 0 "submit dry-run succeeds for a clone whose own origin/HEAD was moved"
assert_contains "$CAPTURE_OUTPUT" "Base: master" \
  "submit resolves the PR base against the parent's real default branch, not the clone's own tampered origin/HEAD"

# A real repo's own 'git gc' writes '.git/objects/info/commit-graph', which 'git clone' copies
# into the clone; 'begin' clears it so the gate's own check does not refuse a brand-new clone.
repo="$HOME/work/commit-graph-at-begin"
init_repo "$repo" master
git -C "$repo" commit-graph write --reachable
desc="${RUN_ID}-commit-graph-at-begin"
path=$(begin_clone "$desc" "$repo")
[[ ! -e "$path/.git/objects/info/commit-graph" ]] &&
  pass "begin deletes a commit-graph copied in from a parent that had one of its own" ||
  fail "begin deletes a commit-graph copied in from a parent that had one of its own" \
    "still present: $path/.git/objects/info/commit-graph"
[[ ! -e "$path/.git/objects/info/commit-graphs" ]] &&
  pass "begin deletes a commit-graphs/ chain copied in the same way" ||
  fail "begin deletes a commit-graphs/ chain copied in the same way" \
    "still present: $path/.git/objects/info/commit-graphs"
capture "$ALLOD" change cleanup "$path"
assert_status 0 "cleanup accepts a clone whose parent had its own legitimate commit-graph before begin ran"

printf '\nTests run: %d\n' "$test_number"
printf 'All %d allod change tests passed.\n' "$test_number"
