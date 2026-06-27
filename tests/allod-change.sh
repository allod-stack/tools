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

cleanup() {
  local path
  for path in "${WORKTREES[@]}"; do
    rm -rf "$path"
  done
  find /tmp -maxdepth 1 -type d -name "allod-change-*-${RUN_ID}-*" -exec rm -rf {} +
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

make_mock_git_path() {
  local bin="$TMP/mock-git-bin-$1"
  mkdir -p "$bin"
  cat > "$bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
for arg in "$@"; do
  if [[ "$arg" == "push" ]]; then
    printf '%s\n' "$*" >> "$GIT_PUSH_LOG"
    break
  fi
done
exec "$REAL_GIT" "$@"
EOF
  chmod +x "$bin/git"
  printf '%s:%s\n' "$bin" "$PATH"
}

# begin tests

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

capture bash "$ALLOD" change begin "$repo"
[[ "$CAPTURE_STATUS" -ne 0 ]] || fail "begin requires -d on protected repo" "$CAPTURE_OUTPUT"
assert_contains "$CAPTURE_OUTPUT" "requires -d" "begin missing -d explains failure"

repo="$HOME/work/begin-open"
init_repo "$repo" master
capture bash "$ALLOD" change begin "$repo"
assert_status 0 "begin succeeds for non-protected repo"
assert_equal "$CAPTURE_OUTPUT" "$repo" "begin prints repo path for non-protected repo"

outside="$TMP/outside-repo"
init_repo_no_remote "$outside" master
capture bash "$ALLOD" change begin "$outside"
assert_status 0 "begin succeeds for outside-HOME repo without origin"
assert_equal "$CAPTURE_OUTPUT" "$outside" "outside-HOME repo is treated as unprotected"

repo="$HOME/work/begin-twice"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-twice"
path=$(begin_worktree "$desc" "$repo")
capture bash "$ALLOD" change begin -d "$desc" "$repo"
assert_status 5 "begin rejects an existing local agent branch"
assert_contains "$CAPTURE_OUTPUT" "already exists locally" "begin local branch failure is actionable"

repo="$HOME/work/begin-remote-exists"
init_repo "$repo" master
protect_repo "$repo" master
git -C "$repo" push -q origin HEAD:refs/heads/agent/"${RUN_ID}-remote"
capture bash "$ALLOD" change begin -d "${RUN_ID}-remote" "$repo"
assert_status 5 "begin rejects an existing remote agent branch"
assert_contains "$CAPTURE_OUTPUT" "already exists on origin" "begin remote branch failure is actionable"

repo="$HOME/work/allod/tools"
init_repo "$repo" master
protect_repo "$repo" master
desc="${RUN_ID}-nested"
path=$(begin_worktree "$desc" "$repo")
case "$path" in
  /tmp/allod-change-work-allod-tools-"$desc"-*) pass "begin sanitizes nested repo slug for tmp path" ;;
  *) fail "begin sanitizes nested repo slug for tmp path" "actual path: $path" ;;
esac

repo="$HOME/work/begin-invalid"
init_repo "$repo" master
protect_repo "$repo" master
for invalid in "has space" "has/slash" "" ".foo" "foo..bar" "foo.lock"; do
  capture bash "$ALLOD" change begin -d "$invalid" "$repo"
  assert_status 1 "begin rejects invalid description '${invalid:-<empty>}'"
done

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

repo="$HOME/work/submit-missing-validation"
init_repo "$repo" master
git -C "$repo" checkout -q -b agent/submit-missing-validation
forge_log="$TMP/forge-missing-validation.log"
forge_body="$TMP/forge-missing-validation.body"
export MOCK_FORGE_LOG="$forge_log" MOCK_FORGE_BODY_LOG="$forge_body" MOCK_FORGE_MODE=""
forge_bin=$(make_mock_forge missing-validation)
capture_with_path "$forge_bin:$PATH" submit_in_repo "$repo" -t "Missing validation" -b "No validation"
assert_status 3 "submit rejects body without validation section"

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

capture_with_path "$no_forge_path" submit_in_repo "$repo" -t "Dry invalid" -b "No validation" --dry-run
assert_status 3 "submit dry-run still validates body"

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

repo="$HOME/work/submit-docs-only"
init_repo "$repo" master
git -C "$repo" checkout -q -b agent/submit-docs-only
forge_log="$TMP/forge-docs-only.log"
forge_body="$TMP/forge-docs-only.body"
export MOCK_FORGE_LOG="$forge_log" MOCK_FORGE_BODY_LOG="$forge_body" MOCK_FORGE_MODE=""
forge_bin=$(make_mock_forge docs-only)
capture_with_path "$forge_bin:$PATH" submit_in_repo "$repo" -t "Docs only" -b "Update README" --docs-only
assert_status 0 "submit --docs-only skips validation requirement"
assert_contains "$(cat "$forge_body")" "Update README" "submit --docs-only passes body to forge"

repo="$HOME/work/submit-docs-only-dry"
init_repo "$repo" master
git -C "$repo" checkout -q -b agent/submit-docs-only-dry
capture_with_path "$no_forge_path" submit_in_repo "$repo" -t "Docs dry" -b "Update README" --docs-only --dry-run
assert_status 0 "submit --docs-only dry-run skips validation"
assert_contains "$CAPTURE_OUTPUT" "Title: Docs dry" "submit --docs-only dry-run prints title"

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
