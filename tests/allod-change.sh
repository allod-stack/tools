#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ALLOD="$ROOT/allod"
REAL_GIT=$(command -v git)
TMP=$(mktemp -d)
RUN_ID="t$$"
CAPTURE_OUTPUT=""
CAPTURE_STATUS=0
test_number=0
repo_number=0
declare -a WORKTREES=()

# Worktrees now land under $HOME/changes, and $HOME is the temp dir removed
# below, so no sweep outside $TMP is needed.
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

begin_worktree() {
  local desc="$1" repo="$2" path
  path=$(bash "$ALLOD" change begin -d "$desc" "$repo")
  WORKTREES+=("$path")
  printf '%s\n' "$path"
}

record_in_repo() {
  local repo="$1"
  shift
  (cd "$repo" && bash "$ALLOD" change record "$@")
}

submit_in_repo() {
  local repo="$1"
  shift
  (cd "$repo" && bash "$ALLOD" change submit "$@")
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

# Count the worktree directories begin would have created for a description.
# Named rather than counting all of $HOME/changes, because earlier tests leave
# their own worktrees there.
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
  bash "$ALLOD" change list "$repo" | awk -F'\t' -v p="$path" '$2 == p { print $4 }'
}

# The binding contract: list reports 'clean' if and only if cleanup on that path
# would succeed. Asserted by running cleanup rather than by restating its
# refusals, since list's precedence order deliberately differs from the order
# cleanup evaluates in and the error messages need not agree.
assert_list_matches_cleanup() {
  local repo="$1" path="$2" expected="$3" label="$4"

  assert_equal "$(list_state_for "$repo" "$path")" "$expected" "list reports $expected for $label"
  capture bash "$ALLOD" change cleanup "$path"
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
  cat > "$bin/forge" <<'EOF'
#!/usr/bin/env bash
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
  chmod +x "$bin/forge"
  printf '%s\n' "$bin"
}

# A git wrapper that forwards to the real git, with one of three behaviours:
#
#   push-log            record every push invocation (the default)
#   lose-worktree-race  another agent creates agent/<desc> between begin's
#                       branch pre-check and its 'worktree add', which then
#                       fails
#   block-handoff       'worktree add' registers, then the handoff file is
#                       pre-created read-only so begin's C3 write fails. The
#                       file is made unwritable, never its directory: an
#                       unwritable admin directory would also stop the
#                       rollback's prune, which reports the failure and still
#                       exits 0.
make_mock_git_path() {
  local name="$1" mode="${2:-push-log}"
  local bin="$TMP/mock-git-bin-$name"
  mkdir -p "$bin"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'set -euo pipefail\n'
    printf 'MOCK_GIT_MODE=%q\n' "$mode"
    cat <<'EOF'
worktree_add=false
repo=""
path=""
branch=""
argc=$#
i=1
while (( i <= argc )); do
  cur="${!i}"
  j=$((i + 1))
  k=$((i + 2))
  nxt=""
  if (( j <= argc )); then
    nxt="${!j}"
  fi
  case "$cur" in
    -C) repo="$nxt" ;;
    -b) branch="$nxt" ;;
    worktree)
      if [[ "$nxt" == "add" ]] && (( k <= argc )); then
        worktree_add=true
        path="${!k}"
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

if [[ "$worktree_add" == true && "$MOCK_GIT_MODE" == "lose-worktree-race" ]]; then
  "$REAL_GIT" -C "$repo" branch "$branch" HEAD
  printf "fatal: a branch named '%s' already exists\n" "$branch" >&2
  exit 128
fi

if [[ "$worktree_add" == true && "$MOCK_GIT_MODE" == "block-handoff" ]]; then
  "$REAL_GIT" "$@"
  git_dir=$("$REAL_GIT" -C "$path" rev-parse --path-format=absolute --git-dir)
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

# begin tests
#
# Everything that must leave $HOME/changes untouched is asserted first, while
# the directory is still guaranteed absent: the first successful 'begin -d'
# creates it for the rest of the run.

repo="$HOME/work/begin-open"
init_repo "$repo" master
capture bash "$ALLOD" change begin "$repo"
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
capture bash "$ALLOD" change begin "$outside"
assert_status 0 "begin without -d succeeds for an outside-HOME repo with no origin"
assert_equal "$CAPTURE_OUTPUT" "$outside" "outside-HOME repo is treated as unprotected"

repo="$HOME/work/begin-protected-no-desc"
init_repo "$repo" main
protect_repo "$repo" main
capture bash "$ALLOD" change begin "$repo"
assert_status 1 "begin requires -d on protected repo"
assert_contains "$CAPTURE_OUTPUT" "requires -d" "begin missing -d explains failure"

repo="$HOME/work/begin-no-origin"
init_repo_no_remote "$repo" master
capture bash "$ALLOD" change begin -d "${RUN_ID}-no-origin" "$repo"
assert_status 1 "begin -d refuses a repo with no origin remote"
assert_contains "$CAPTURE_OUTPUT" "no 'origin' remote" "begin -d names the missing remote"
assert_not_contains "$CAPTURE_OUTPUT" "could not check origin for branch" \
  "begin -d fails on the missing remote rather than on ls-remote"

repo="$HOME/work/begin-no-origin-head"
init_repo_no_origin_head "$repo" master
capture bash "$ALLOD" change begin -d "${RUN_ID}-no-head" "$repo"
assert_status 1 "begin -d refuses an unprotected repo with no origin/HEAD"
assert_contains "$CAPTURE_OUTPUT" "remote set-head" "begin -d names the origin/HEAD repair"

repo="$HOME/work/begin-missing-base"
init_repo "$repo" master
protect_repo "$repo" no-such-branch
desc="${RUN_ID}-missing-base"
capture bash "$ALLOD" change begin -d "$desc" "$repo"
assert_status 1 "begin -d refuses a base branch that is missing on origin"
[[ ! -e "$HOME/changes" ]] &&
  pass "begin -d creates no worktree directory when the base is missing" ||
  fail "begin -d creates no worktree directory when the base is missing" "exists: $HOME/changes"
assert_equal "$(git -C "$repo" branch --list "agent/$desc")" "" \
  "begin -d creates no branch when the base is missing"

repo="$HOME/work/begin-invalid-open"
init_repo "$repo" master
for invalid in "has space" "has/slash" "" ".foo" "foo..bar" "foo.lock"; do
  capture bash "$ALLOD" change begin -d "$invalid" "$repo"
  assert_status 1 "begin rejects invalid description '${invalid:-<empty>}' in an unprotected repo"
done

repo="$HOME/work/begin-invalid"
init_repo "$repo" master
protect_repo "$repo" master
for invalid in "has space" "has/slash" "" ".foo" "foo..bar" "foo.lock"; do
  capture bash "$ALLOD" change begin -d "$invalid" "$repo"
  assert_status 1 "begin rejects invalid description '${invalid:-<empty>}'"
done

# The flip: -d isolates whether or not the repo is protected.

repo="$HOME/work/begin-isolated"
init_repo "$repo" master
desc="${RUN_ID}-isolated"
path=$(begin_worktree "$desc" "$repo")
case "$path" in
  "$HOME/changes/work-begin-isolated-${desc}-"??????)
    pass "begin -d sites an unprotected repo's worktree under ~/changes" ;;
  *) fail "begin -d sites an unprotected repo's worktree under ~/changes" "actual path: $path" ;;
esac
[[ -d "$path" ]] || fail "begin -d creates a worktree for an unprotected repo" "missing worktree: $path"
pass "begin -d creates a worktree for an unprotected repo"
assert_equal "$(git -C "$path" branch --show-current)" "agent/$desc" \
  "begin -d creates the agent branch for an unprotected repo"
git -C "$path" merge-base --is-ancestor origin/master HEAD &&
  pass "begin -d starts an unprotected worktree from the default branch" ||
  fail "begin -d starts an unprotected worktree from the default branch"
assert_equal "$(git -C "$repo" branch --show-current)" "master" \
  "begin -d leaves the shared checkout on its default branch"
assert_equal "$(git -C "$repo" status --porcelain)" "" "begin -d leaves the shared checkout clean"

git_dir=$(git -C "$path" rev-parse --path-format=absolute --git-dir)
common_dir=$(git -C "$path" rev-parse --path-format=absolute --git-common-dir)
[[ "$git_dir" != "$common_dir" ]] &&
  pass "begin -d creates a linked worktree with its own git dir" ||
  fail "begin -d creates a linked worktree with its own git dir" "git-dir: $git_dir"
[[ -f "$git_dir/allod-change-branch" ]] &&
  pass "begin -d writes the branch handoff file" ||
  fail "begin -d writes the branch handoff file" "missing: $git_dir/allod-change-branch"
assert_equal "$(cat "$git_dir/allod-change-branch")" "agent/$desc" \
  "handoff file names the branch begin created"

repo="$HOME/work/begin-protected"
init_repo "$repo" main
protect_repo "$repo" main
desc="${RUN_ID}-begin"
path=$(begin_worktree "$desc" "$repo")
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
path=$(begin_worktree "$desc" "$repo")
capture bash "$ALLOD" change begin -d "$desc" "$repo"
assert_status 5 "begin rejects an existing local agent branch"
assert_contains "$CAPTURE_OUTPUT" "already exists locally" "begin local branch failure is actionable"

repo="$HOME/work/begin-twice-open"
init_repo "$repo" master
desc="${RUN_ID}-twice-open"
path=$(begin_worktree "$desc" "$repo")
capture bash "$ALLOD" change begin -d "$desc" "$repo"
assert_status 5 "begin rejects an existing local agent branch in an unprotected repo"
assert_contains "$CAPTURE_OUTPUT" "already exists locally" \
  "begin local branch failure is actionable for an unprotected repo"

repo="$HOME/work/begin-remote-exists"
init_repo "$repo" master
protect_repo "$repo" master
git -C "$repo" push -q origin HEAD:refs/heads/agent/"${RUN_ID}-remote"
capture bash "$ALLOD" change begin -d "${RUN_ID}-remote" "$repo"
assert_status 5 "begin rejects an existing remote agent branch"
assert_contains "$CAPTURE_OUTPUT" "already exists on origin" "begin remote branch failure is actionable"

repo="$HOME/work/begin-remote-exists-open"
init_repo "$repo" master
git -C "$repo" push -q origin HEAD:refs/heads/agent/"${RUN_ID}-remote-open"
capture bash "$ALLOD" change begin -d "${RUN_ID}-remote-open" "$repo"
assert_status 5 "begin rejects an existing remote agent branch in an unprotected repo"
assert_contains "$CAPTURE_OUTPUT" "already exists on origin" \
  "begin remote branch failure is actionable for an unprotected repo"

repo="$HOME/work/allod/tools"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-nested"
path=$(begin_worktree "$desc" "$repo")
case "$path" in
  "$HOME/changes/work-allod-tools-${desc}-"??????)
    pass "begin sanitizes nested repo slug for the worktree path" ;;
  *) fail "begin sanitizes nested repo slug for the worktree path" "actual path: $path" ;;
esac

# Rollback: a failed 'worktree add' or handoff write leaves nothing behind, and
# says so when it cannot prove otherwise.

export REAL_GIT

repo="$HOME/work/begin-lost-race"
init_repo "$repo" master
desc="${RUN_ID}-lost-race"
mock_git_path=$(make_mock_git_path lost-race lose-worktree-race)
capture_with_path "$mock_git_path" bash "$ALLOD" change begin -d "$desc" "$repo"
[[ "$CAPTURE_STATUS" -ne 0 ]] &&
  pass "begin fails when another agent wins the worktree race" ||
  fail "begin fails when another agent wins the worktree race" "$CAPTURE_OUTPUT"
[[ -n "$(git -C "$repo" branch --list "agent/$desc")" ]] &&
  pass "begin leaves a branch it cannot prove it created" ||
  fail "begin leaves a branch it cannot prove it created"
assert_equal "$(changes_entries_for "$desc")" "0" \
  "begin removes its worktree directory after a failed add"
assert_contains "$CAPTURE_OUTPUT" "may have been left behind" \
  "begin reports the branch it refused to delete"
assert_contains "$CAPTURE_OUTPUT" "branch --list 'agent/*'" \
  "begin points at the branch listing after a failed add"
assert_not_contains "$CAPTURE_OUTPUT" "allod change list" \
  "begin does not point at list, which cannot show a branch with no worktree"

repo="$HOME/work/begin-handoff-blocked"
init_repo "$repo" master
desc="${RUN_ID}-handoff-blocked"
mock_git_path=$(make_mock_git_path handoff-blocked block-handoff)
capture_with_path "$mock_git_path" bash "$ALLOD" change begin -d "$desc" "$repo"
[[ "$CAPTURE_STATUS" -ne 0 ]] &&
  pass "begin fails when the handoff write fails" ||
  fail "begin fails when the handoff write fails" "$CAPTURE_OUTPUT"
assert_equal "$(changes_entries_for "$desc")" "0" \
  "begin removes its worktree directory after a failed handoff write"
assert_equal "$(worktree_count "$repo")" "1" \
  "begin prunes the worktree admin entry after a failed handoff write"

# list tests

repo="$HOME/work/list-empty"
init_repo "$repo" master
capture bash "$ALLOD" change list "$repo"
assert_status 0 "list exits 0 for a repo with no worktrees"
assert_equal "$CAPTURE_OUTPUT" "" "list prints nothing for a repo with no worktrees"

repo="$HOME/work/list-rows"
init_repo "$repo" master
desc="${RUN_ID}-rows"
path=$(begin_worktree "$desc" "$repo")
capture bash "$ALLOD" change list "$repo"
assert_status 0 "list exits 0 with a worktree present"
assert_equal "$(printf '%s\n' "$CAPTURE_OUTPUT" | wc -l)" "1" "list prints one row per linked worktree"
assert_equal "$CAPTURE_OUTPUT" "$(printf 'list-rows\t%s\tagent/%s\tclean' "$path" "$desc")" \
  "list row carries repo, path, branch, and state"

other_repo="$HOME/work/list-other"
init_repo "$other_repo" master
other_desc="${RUN_ID}-other"
other_path=$(begin_worktree "$other_desc" "$other_repo")
capture bash "$ALLOD" change list
assert_status 0 "list exits 0 with no argument"
assert_contains "$CAPTURE_OUTPUT" "$path" "list with no argument walks the workspace"
assert_contains "$CAPTURE_OUTPUT" "$other_path" "list with no argument covers every repo"
capture bash "$ALLOD" change list "$other_repo"
assert_contains "$CAPTURE_OUTPUT" "$other_path" "list <repo-path> lists that repo's worktrees"
assert_not_contains "$CAPTURE_OUTPUT" "$path" "list <repo-path> scopes to that repo only"

# One case per state, each checked against what cleanup actually does.

repo="$HOME/work/list-states"
init_repo "$repo" master

desc="${RUN_ID}-state-clean"
path=$(begin_worktree "$desc" "$repo")
assert_list_matches_cleanup "$repo" "$path" clean "a fresh worktree"

desc="${RUN_ID}-state-dirty"
path=$(begin_worktree "$desc" "$repo")
printf 'dirty\n' > "$path/tracked.txt"
assert_list_matches_cleanup "$repo" "$path" dirty "a dirty worktree"

desc="${RUN_ID}-state-unpushed"
path=$(begin_worktree "$desc" "$repo")
printf 'unpushed\n' > "$path/tracked.txt"
git -C "$path" commit -qam "unpushed"
assert_list_matches_cleanup "$repo" "$path" unpushed "a worktree with unpushed commits"

desc="${RUN_ID}-state-detached"
path=$(begin_worktree "$desc" "$repo")
git -C "$path" checkout -q --detach
assert_contains "$(bash "$ALLOD" change list "$repo")" "(detached)" \
  "list names a detached worktree in the branch column"
assert_list_matches_cleanup "$repo" "$path" detached "a detached worktree"

desc="${RUN_ID}-state-locked"
path=$(begin_worktree "$desc" "$repo")
git -C "$repo" worktree lock "$path"
assert_list_matches_cleanup "$repo" "$path" locked "a locked worktree"
git -C "$repo" worktree unlock "$path"
assert_list_matches_cleanup "$repo" "$path" clean "the same worktree once unlocked"

# Precedence, one assertion per adjacent pair of the reporting order.

repo="$HOME/work/list-precedence"
init_repo "$repo" master

desc="${RUN_ID}-locked-dirty"
path=$(begin_worktree "$desc" "$repo")
printf 'dirty\n' > "$path/tracked.txt"
git -C "$repo" worktree lock "$path"
assert_equal "$(list_state_for "$repo" "$path")" "locked" "list reports locked ahead of dirty"

desc="${RUN_ID}-detached-dirty"
path=$(begin_worktree "$desc" "$repo")
git -C "$path" checkout -q --detach
printf 'dirty\n' > "$path/tracked.txt"
assert_equal "$(list_state_for "$repo" "$path")" "detached" "list reports detached ahead of dirty"

desc="${RUN_ID}-dirty-unpushed"
path=$(begin_worktree "$desc" "$repo")
printf 'committed\n' > "$path/tracked.txt"
git -C "$path" commit -qam "unpushed"
printf 'dirty\n' > "$path/tracked.txt"
assert_equal "$(list_state_for "$repo" "$path")" "dirty" "list reports dirty ahead of unpushed"

desc="${RUN_ID}-locked-gone"
path=$(begin_worktree "$desc" "$repo")
git -C "$repo" worktree lock "$path"
rm -rf "$path"
assert_equal "$(list_state_for "$repo" "$path")" "locked" \
  "list reports locked, not prunable, for a locked worktree whose directory is gone"

repo="$HOME/work/list-prunable"
init_repo "$repo" master
desc="${RUN_ID}-prunable"
path=$(begin_worktree "$desc" "$repo")
rm -rf "$path"
worktrees_before=$(worktree_count "$repo")
assert_equal "$(list_state_for "$repo" "$path")" "prunable" \
  "list reports prunable for a worktree whose directory is gone"
assert_equal "$(worktree_count "$repo")" "$worktrees_before" \
  "list does not prune the worktrees it reports"
capture bash "$ALLOD" change cleanup "$path"
[[ "$CAPTURE_STATUS" -ne 0 ]] &&
  pass "cleanup refuses a prunable worktree path" ||
  fail "cleanup refuses a prunable worktree path" "$CAPTURE_OUTPUT"

# record tests

repo="$HOME/work/record-open"
init_repo "$repo" master
git -C "$repo" checkout -q -b feature
printf 'changed\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "record nonprotected"
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
path=$(begin_worktree "$desc" "$repo")
printf 'changed\n' > "$path/tracked.txt"
capture record_in_repo "$path" -m "record protected worktree"
assert_status 0 "record commits and pushes protected worktree branch"
remote_has_branch "$repo" "agent/$desc" &&
  pass "record pushes protected worktree agent branch" ||
  fail "record pushes protected worktree agent branch"

repo="$HOME/work/record-protected-default"
init_repo "$repo" master
protect_repo "$repo" master
printf 'changed\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "blocked"
assert_status 2 "record refuses protected branch before staging"
git -C "$repo" diff --cached --quiet &&
  pass "record leaves index untouched when protected branch is refused" ||
  fail "record leaves index untouched when protected branch is refused"

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
capture record_in_repo "$repo" -m ""
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

repo="$HOME/work/record-add-u"
init_repo "$repo" master
git -C "$repo" checkout -q -b add-u
printf 'one\n' > "$repo/file1.txt"
printf 'two\n' > "$repo/file2.txt"
git -C "$repo" add file1.txt file2.txt
git -C "$repo" commit -qm "add tracked files"
printf 'one changed\n' > "$repo/file1.txt"
printf 'two changed\n' > "$repo/file2.txt"
printf 'new\n' > "$repo/untracked.txt"
capture record_in_repo "$repo" -m "tracked changes"
assert_status 0 "record without -f stages tracked modifications"
files=$(changed_files_in_head "$repo")
assert_contains "$files" "file1.txt" "record add -u includes first tracked file"
assert_contains "$files" "file2.txt" "record add -u includes second tracked file"
assert_not_contains "$files" "untracked.txt" "record add -u excludes untracked file"

repo="$HOME/work/record-retry"
init_repo "$repo" master
good_remote=$(git -C "$repo" remote get-url origin)
git -C "$repo" checkout -q -b retry
printf 'retry\n' > "$repo/tracked.txt"
git -C "$repo" remote set-url origin "$TMP/missing-remote.git"
capture record_in_repo "$repo" -m "retry push"
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
capture record_in_repo "$repo" -m "second unpushed"
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

repo="$HOME/work/record-message-file"
init_repo "$repo" master
git -C "$repo" checkout -q -b message-file
printf 'message from file\n' > "$TMP/message.txt"
printf 'changed\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -M "$TMP/message.txt"
assert_status 0 "record reads commit message from file"
assert_equal "$(git -C "$repo" log -1 --format=%s)" "message from file" \
  "record -M file uses file message"

repo="$HOME/work/record-message-stdin"
init_repo "$repo" master
git -C "$repo" checkout -q -b message-stdin
printf 'changed\n' > "$repo/tracked.txt"
set +e
CAPTURE_OUTPUT=$(cd "$repo" && printf 'message from stdin' | bash "$ALLOD" change record -M - 2>&1)
CAPTURE_STATUS=$?
set -e
assert_status 0 "record reads commit message from stdin"
assert_equal "$(git -C "$repo" log -1 --format=%s)" "message from stdin" \
  "record -M - uses stdin message"

repo="$HOME/work/record-no-amend"
init_repo "$repo" master
git -C "$repo" checkout -q -b no-amend
printf 'first\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "first record"
assert_status 0 "record first commit for no-amend test"
printf 'second\n' > "$repo/tracked.txt"
capture record_in_repo "$repo" -m "second record"
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
capture_with_path "$mock_git_path" bash -c 'cd "$1" && bash "$2" change record -m "no force"' _ "$repo" "$ALLOD"
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

# cleanup tests

repo="$HOME/work/cleanup-clean"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-cleanup-clean"
path=$(begin_worktree "$desc" "$repo")
push_log="$TMP/cleanup-push.log"
: > "$push_log"
export REAL_GIT GIT_PUSH_LOG="$push_log"
mock_git_path=$(make_mock_git_path cleanup)
capture_with_path "$mock_git_path" bash "$ALLOD" change cleanup "$path"
assert_status 0 "cleanup removes clean worktree"
[[ ! -d "$path" ]] &&
  pass "cleanup deletes worktree directory" ||
  fail "cleanup deletes worktree directory" "still exists: $path"
[[ -z "$(git -C "$repo" branch --list "agent/$desc")" ]] &&
  pass "cleanup deletes local agent branch" ||
  fail "cleanup deletes local agent branch"
assert_equal "$(cat "$push_log")" "" "cleanup does not push remote branch deletion"

repo="$HOME/work/cleanup-dirty"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-cleanup-dirty"
path=$(begin_worktree "$desc" "$repo")
printf 'dirty\n' > "$path/tracked.txt"
capture bash "$ALLOD" change cleanup "$path"
[[ "$CAPTURE_STATUS" -ne 0 ]] || fail "cleanup refuses dirty worktree" "$CAPTURE_OUTPUT"
assert_contains "$CAPTURE_OUTPUT" "dirty" "cleanup dirty failure is actionable"

repo="$HOME/work/cleanup-unpushed"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-cleanup-unpushed"
path=$(begin_worktree "$desc" "$repo")
printf 'unpushed\n' > "$path/tracked.txt"
git -C "$path" commit -qam "unpushed"
capture bash "$ALLOD" change cleanup "$path"
[[ "$CAPTURE_STATUS" -ne 0 ]] || fail "cleanup refuses unpushed worktree" "$CAPTURE_OUTPUT"
assert_contains "$CAPTURE_OUTPUT" "unpushed" "cleanup unpushed failure is actionable"

repo="$HOME/work/cleanup-regular"
init_repo "$repo" master
git -C "$repo" checkout -q -b agent/regular-cleanup
capture bash "$ALLOD" change cleanup "$repo"
[[ "$CAPTURE_STATUS" -ne 0 ]] || fail "cleanup refuses regular repo" "$CAPTURE_OUTPUT"
assert_contains "$CAPTURE_OUTPUT" "regular repository" "cleanup regular repo failure is actionable"
[[ -n "$(git -C "$repo" branch --list agent/regular-cleanup)" ]] &&
  pass "cleanup regular repo refusal leaves branch intact" ||
  fail "cleanup regular repo refusal leaves branch intact"

printf '\nTests run: %d\n' "$test_number"
printf 'All %d allod change tests passed.\n' "$test_number"
