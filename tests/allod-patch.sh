#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ALLOD="$ROOT/allod"
REAL_GIT=$(command -v git)
REAL_SSH=$(command -v ssh)
TMP=$(mktemp -d)
RUN_ID="t$$"
CAPTURE_OUTPUT=""
CAPTURE_STATUS=0
test_number=0
repo_number=0

cleanup() {
  find /tmp -maxdepth 1 -type d -name "allod-patch.*" -user "$(id -un)" -exec rm -rf {} + 2>/dev/null || true
  find /tmp -maxdepth 1 -type d -name "allod-patch-staging.*" -user "$(id -un)" -exec rm -rf {} + 2>/dev/null || true
  find /tmp -maxdepth 1 -type d -name "allod-patch-receive.*" -user "$(id -un)" -exec rm -rf {} + 2>/dev/null || true
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

clone_repo() {
  local source="$1" dest="$2"
  local remote
  remote=$(git -C "$source" remote get-url origin)
  mkdir -p "$(dirname "$dest")"
  git clone -q "$remote" "$dest"
  git -C "$dest" config user.name "Test User"
  git -C "$dest" config user.email "test@example.invalid"
}

add_commit() {
  local repo="$1" msg="$2" content="${3:-}"
  if [[ -n "$content" ]]; then
    printf '%s\n' "$content" > "$repo/tracked.txt"
  else
    printf '%s\n' "$msg" >> "$repo/tracked.txt"
  fi
  git -C "$repo" add -A
  git -C "$repo" commit -qm "$msg"
}

# --- SSH mock ---

MOCK_SSH_DIR="$TMP/mock-ssh"
MOCK_SSH_LOG="$TMP/ssh-commands.log"
MOCK_SSH_CALL_FILE="$TMP/ssh-call-count"
export MOCK_SSH_LOG MOCK_SSH_CALL_FILE

make_mock_ssh_path() {
  local bin="$MOCK_SSH_DIR/bin"
  mkdir -p "$bin"
  cat > "$bin/ssh" <<'MOCK_SSH_EOF'
#!/usr/bin/env bash

# Parse SSH args: ssh [options] [--] host command...
host=""
remote_cmd=""
past_opts=false

for arg in "$@"; do
  if [[ "$past_opts" == false ]]; then
    case "$arg" in
      --) past_opts=true; continue ;;
      -*) continue ;;
      *)
        host="$arg"
        past_opts=true
        continue
        ;;
    esac
  fi
  if [[ -z "$host" ]]; then
    host="$arg"
  else
    if [[ -n "$remote_cmd" ]]; then
      remote_cmd="$remote_cmd $arg"
    else
      remote_cmd="$arg"
    fi
  fi
done

# Log the remote command
{
  printf 'CALL\n'
  printf '%s\n' "$remote_cmd"
  printf 'END\n'
} >> "$MOCK_SSH_LOG"

# Track call count for phase-specific failures
count=$(cat "$MOCK_SSH_CALL_FILE" 2>/dev/null || printf '0')
count=$((count + 1))
printf '%s' "$count" > "$MOCK_SSH_CALL_FILE"

# Check for failure modes
case "${MOCK_SSH_FAIL:-}" in
  connect)
    printf 'ssh: connect to host %s port 22: Connection refused\n' "$host" >&2
    exit 255
    ;;
  generate)
    if [[ "$count" -eq 1 ]]; then
      printf 'allod: remote generation failed (mock)\n' >&2
      exit 1
    fi
    ;;
  transfer)
    if [[ "$count" -eq 2 ]]; then
      printf 'allod: transfer failed (mock)\n' >&2
      exit 1
    fi
    ;;
  cleanup)
    if [[ "$count" -eq 3 ]]; then
      printf 'allod: cleanup failed (mock)\n' >&2
      exit 1
    fi
    ;;
  truncated_tar)
    if [[ "$count" -eq 2 ]]; then
      printf 'partial garbage'
      exit 0
    fi
    ;;
  bad_tmpdir_*)
    # On the generate call, return a crafted tmpdir path instead of running the script
    if [[ "$count" -eq 1 ]]; then
      # Extract the crafted value after "bad_tmpdir_"
      printf '%s' "${MOCK_SSH_FAIL#bad_tmpdir_}"
      exit 0
    fi
    ;;
esac

# Execute the remote command locally with stdin/stdout preserved
bash -c "$remote_cmd"
MOCK_SSH_EOF
  chmod +x "$bin/ssh"

  # Build PATH with mock ssh first, plus all needed tools
  local tool target
  for tool in bash git dirname mktemp sed grep cat rm basename awk jq sha256sum base64 tar find wc printf cut sort head tail touch chmod mkdir rmdir mv cp ln readlink id tr; do
    target=$(command -v "$tool" 2>/dev/null) || continue
    [[ ! -e "$bin/$tool" ]] || continue
    ln -sf "$target" "$bin/$tool"
  done
  printf '%s\n' "$bin"
}

reset_mock_ssh() {
  : > "$MOCK_SSH_LOG"
  printf '0' > "$MOCK_SSH_CALL_FILE"
  export MOCK_SSH_FAIL=""
}

get_ssh_call_count() {
  cat "$MOCK_SSH_CALL_FILE" 2>/dev/null || printf '0'
}

get_ssh_commands() {
  cat "$MOCK_SSH_LOG" 2>/dev/null || true
}

# Build mock SSH PATH once
MOCK_BIN=$(make_mock_ssh_path)
MOCK_PATH="$MOCK_BIN:$PATH"

# --- Mock git for push tracking ---

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
  # Include mock SSH bin for combined path
  printf '%s:%s:%s\n' "$bin" "$MOCK_BIN" "$PATH"
}

# ================================================================
# CLI routing tests
# ================================================================

capture bash "$ALLOD" --help
assert_status 0 "allod --help exits 0"
assert_contains "$CAPTURE_OUTPUT" "change" "allod --help lists change"
assert_contains "$CAPTURE_OUTPUT" "patch" "allod --help lists patch"

capture bash "$ALLOD" patch --help
assert_status 0 "allod patch --help exits 0"
assert_contains "$CAPTURE_OUTPUT" "fetch" "patch help lists fetch"
assert_contains "$CAPTURE_OUTPUT" "apply" "patch help lists apply"
assert_contains "$CAPTURE_OUTPUT" "receive" "patch help lists receive"

capture bash "$ALLOD" patch
assert_status 1 "allod patch with no subcommand exits 1"
assert_contains "$CAPTURE_OUTPUT" "Usage" "patch no-subcommand shows usage"

capture bash "$ALLOD" patch nope
assert_status 1 "allod patch nope exits 1"
assert_contains "$CAPTURE_OUTPUT" "unknown patch command" "unknown patch subcommand message"

# ================================================================
# fetch tests
# ================================================================

# --- Happy path: single commit ---
source_repo="$TMP/repos/fetch-happy-source"
init_repo "$source_repo" master
add_commit "$source_repo" "first change" "changed content"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 0 "fetch happy path exits 0"
assert_contains "$CAPTURE_OUTPUT" "fetched 1 patch" "fetch reports patch count"
assert_contains "$CAPTURE_OUTPUT" "artifact dir:" "fetch reports artifact dir"

# Extract artifact dir from output
artifact_path=$(printf '%s' "$CAPTURE_OUTPUT" | grep 'artifact dir:' | sed 's/.*artifact dir: //')
[[ -d "$artifact_path" ]] && pass "fetch creates artifact dir" || fail "fetch creates artifact dir" "missing: $artifact_path"
[[ -f "$artifact_path/manifest.json" ]] && pass "fetch creates manifest.json" || fail "fetch creates manifest.json"

manifest=$(cat "$artifact_path/manifest.json")
m_patch_count=$(printf '%s' "$manifest" | jq -r '.patch_count')
assert_equal "$m_patch_count" "1" "fetch manifest has correct patch count"
m_patches_len=$(printf '%s' "$manifest" | jq -r '.patches | length')
assert_equal "$m_patches_len" "1" "fetch manifest patches array length matches"
m_filename=$(printf '%s' "$manifest" | jq -r '.patches[0].filename')
[[ "$m_filename" == *.patch ]] && pass "fetch manifest patch filename ends in .patch" || fail "fetch manifest patch filename" "$m_filename"
[[ -f "$artifact_path/$m_filename" ]] && pass "fetch patch file exists" || fail "fetch patch file exists"

# Verify checksum
m_sha=$(printf '%s' "$manifest" | jq -r '.patches[0].sha256')
actual_sha=$(sha256sum -b -- "$artifact_path/$m_filename" | awk '{print $1}')
assert_equal "$actual_sha" "$m_sha" "fetch manifest sha256 matches patch file"

# Verify remote cleanup happened (3 SSH calls: generate, transfer, cleanup)
assert_equal "$(get_ssh_call_count)" "3" "fetch makes exactly 3 SSH calls"
rm -rf "$artifact_path"

# --- Multiple commits ---
source_repo="$TMP/repos/fetch-multi-source"
init_repo "$source_repo" master
add_commit "$source_repo" "change one"
add_commit "$source_repo" "change two"
add_commit "$source_repo" "change three"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 0 "fetch multiple commits exits 0"
assert_contains "$CAPTURE_OUTPUT" "fetched 3 patch" "fetch reports 3 patches"
artifact_path=$(printf '%s' "$CAPTURE_OUTPUT" | grep 'artifact dir:' | sed 's/.*artifact dir: //')
m_count=$(jq -r '.patch_count' "$artifact_path/manifest.json")
assert_equal "$m_count" "3" "fetch produces 3 patches for 3 commits"
rm -rf "$artifact_path"

# --- Input validation ---
reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch ":$source_repo"
assert_status 1 "fetch empty host exits 1"
assert_equal "$(get_ssh_call_count)" "0" "fetch empty host makes no SSH calls"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:"
assert_status 1 "fetch empty source repo exits 1"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:relative/path"
assert_status 1 "fetch relative source repo exits 1"
assert_contains "$CAPTURE_OUTPUT" "absolute path" "fetch relative repo message"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:/repo/with
newline"
assert_status 1 "fetch newline source repo exits 1"
assert_equal "$(get_ssh_call_count)" "0" "fetch newline source repo makes no SSH calls"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo" --base "refs/with
newline"
assert_status 1 "fetch newline base exits 1"
assert_equal "$(get_ssh_call_count)" "0" "fetch newline base makes no SSH calls"

# --- Dirty source worktree ---
source_repo="$TMP/repos/fetch-dirty-tracked"
init_repo "$source_repo" master
add_commit "$source_repo" "ahead of base"
printf 'modified\n' > "$source_repo/tracked.txt"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 10 "fetch dirty tracked exits 10"
assert_contains "$CAPTURE_OUTPUT" "dirty" "fetch dirty tracked message"

git -C "$source_repo" checkout -- tracked.txt

source_repo="$TMP/repos/fetch-dirty-staged"
init_repo "$source_repo" master
add_commit "$source_repo" "ahead of base"
printf 'staged\n' > "$source_repo/new-staged.txt"
git -C "$source_repo" add new-staged.txt

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 10 "fetch dirty staged exits 10"

git -C "$source_repo" reset -q HEAD new-staged.txt
rm "$source_repo/new-staged.txt"

source_repo="$TMP/repos/fetch-dirty-untracked"
init_repo "$source_repo" master
add_commit "$source_repo" "ahead of base"
printf 'untracked\n' > "$source_repo/untracked-file.txt"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 10 "fetch dirty untracked exits 10"

rm "$source_repo/untracked-file.txt"

# --- No commits to export (HEAD == base) ---
source_repo="$TMP/repos/fetch-no-commits"
init_repo "$source_repo" master

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 11 "fetch no commits exits 11"
assert_contains "$CAPTURE_OUTPUT" "not ahead" "fetch no commits message"

# --- Base not ancestor ---
source_repo="$TMP/repos/fetch-not-ancestor"
init_repo "$source_repo" master
add_commit "$source_repo" "on master"
git -C "$source_repo" checkout -q -b other
add_commit "$source_repo" "on other"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo" --base master
assert_status 0 "fetch from branch with master as base works"
artifact_path=$(printf '%s' "$CAPTURE_OUTPUT" | grep 'artifact dir:' | sed 's/.*artifact dir: //')
rm -rf "$artifact_path"

# Create a diverged scenario
source_repo="$TMP/repos/fetch-diverged"
init_repo "$source_repo" master
git -C "$source_repo" checkout -q -b feature
add_commit "$source_repo" "feature commit"
git -C "$source_repo" checkout -q master
add_commit "$source_repo" "master diverges"
git -C "$source_repo" push -q origin master
git -C "$source_repo" checkout -q feature

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo" --base origin/master
assert_status 11 "fetch diverged base exits 11"
assert_contains "$CAPTURE_OUTPUT" "not an ancestor" "fetch diverged message"

# --- Merge commit in range ---
source_repo="$TMP/repos/fetch-merge"
init_repo "$source_repo" master
git -C "$source_repo" checkout -q -b feature
printf 'feature\n' > "$source_repo/feature.txt"
git -C "$source_repo" add feature.txt
git -C "$source_repo" commit -qm "feature work"
git -C "$source_repo" checkout -q master
printf 'master\n' > "$source_repo/master.txt"
git -C "$source_repo" add master.txt
git -C "$source_repo" commit -qm "master work"
git -C "$source_repo" merge -q --no-ff feature -m "merge feature"
git -C "$source_repo" push -q origin master

reset_mock_ssh
base_before_merge=$(git -C "$source_repo" rev-parse HEAD~2)
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo" --base "$base_before_merge"
assert_status 11 "fetch merge commits exits 11"
assert_contains "$CAPTURE_OUTPUT" "non-linear" "fetch merge commits message"

# --- All empty commits ---
# git format-patch produces files for --allow-empty commits (patch with no diff),
# so the zero-patch exit path is not triggered. Verify they export successfully.
source_repo="$TMP/repos/fetch-empty-commits"
init_repo "$source_repo" master
git -C "$source_repo" commit -q --allow-empty -m "empty commit"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 0 "fetch empty commits exits 0 (format-patch produces a patch file)"
artifact_path=$(printf '%s' "$CAPTURE_OUTPUT" | grep 'artifact dir:' | sed 's/.*artifact dir: //')
rm -rf "$artifact_path"

# --- SSH connection failure ---
reset_mock_ssh
export MOCK_SSH_FAIL="connect"
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 1 "fetch SSH connection failure exits 1"
assert_contains "$CAPTURE_OUTPUT" "Connection refused" "fetch SSH failure message"
export MOCK_SSH_FAIL=""

# --- Remote cleanup success verified above (3 calls) ---

# --- Remote cleanup failure after successful local promotion ---
source_repo="$TMP/repos/fetch-cleanup-fail"
init_repo "$source_repo" master
add_commit "$source_repo" "content for cleanup fail test"

reset_mock_ssh
export MOCK_SSH_FAIL="cleanup"
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 1 "fetch cleanup failure exits 1"
assert_contains "$CAPTURE_OUTPUT" "remote cleanup failed" "fetch cleanup failure message"
assert_contains "$CAPTURE_OUTPUT" "local artifact:" "fetch cleanup failure shows local path"
assert_contains "$CAPTURE_OUTPUT" "manual cleanup" "fetch cleanup failure mentions manual cleanup"

# Verify local artifact was preserved
local_artifact=$(printf '%s' "$CAPTURE_OUTPUT" | grep 'local artifact:' | sed 's/.*local artifact: //')
[[ -d "$local_artifact" ]] && pass "fetch cleanup failure preserves local artifact" || \
  fail "fetch cleanup failure preserves local artifact" "missing: $local_artifact"
rm -rf "$local_artifact"
export MOCK_SSH_FAIL=""

# --- --output tests ---
source_repo="$TMP/repos/fetch-output-source"
init_repo "$source_repo" master
add_commit "$source_repo" "output test content"

output_dir="$TMP/custom-output/patches"
mkdir -p "$TMP/custom-output"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo" --output "$output_dir"
assert_status 0 "fetch --output exits 0"
[[ -d "$output_dir" ]] && pass "fetch --output creates specified dir" || fail "fetch --output creates dir" "missing: $output_dir"
[[ -f "$output_dir/manifest.json" ]] && pass "fetch --output has manifest" || fail "fetch --output has manifest"
rm -rf "$output_dir"

# --output existing path
mkdir -p "$output_dir"
reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo" --output "$output_dir"
assert_status 1 "fetch --output existing path exits 1"
assert_contains "$CAPTURE_OUTPUT" "already exists" "fetch --output existing path message"
assert_equal "$(get_ssh_call_count)" "0" "fetch --output existing path makes no SSH calls"
rmdir "$output_dir"

# --output dangling symlink
ln -s "$TMP/nonexistent-target" "$output_dir"
reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo" --output "$output_dir"
assert_status 1 "fetch --output dangling symlink exits 1"
assert_equal "$(get_ssh_call_count)" "0" "fetch --output dangling symlink makes no SSH calls"
rm "$output_dir"

# --output missing parent
reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo" --output "$TMP/nonexistent-parent/patches"
assert_status 1 "fetch --output missing parent exits 1"
assert_equal "$(get_ssh_call_count)" "0" "fetch --output missing parent makes no SSH calls"

# --- Local extraction failure (truncated tar) ---
source_repo="$TMP/repos/fetch-truncated"
init_repo "$source_repo" master
add_commit "$source_repo" "truncated tar content"

reset_mock_ssh
export MOCK_SSH_FAIL="truncated_tar"
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 1 "fetch truncated tar exits 1"
assert_contains "$CAPTURE_OUTPUT" "manual cleanup" "fetch truncated tar prints remote tmpdir"
# Verify no local artifact left
output_line=$(printf '%s' "$CAPTURE_OUTPUT" | grep 'artifact dir:' || true)
[[ -z "$output_line" ]] && pass "fetch truncated tar leaves no artifact dir" || \
  fail "fetch truncated tar leaves no artifact dir" "$output_line"
export MOCK_SSH_FAIL=""

# --- --base custom ref ---
source_repo="$TMP/repos/fetch-custom-base"
init_repo "$source_repo" master
add_commit "$source_repo" "before tag"
git -C "$source_repo" tag v1.0
add_commit "$source_repo" "after tag one"
add_commit "$source_repo" "after tag two"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo" --base v1.0
assert_status 0 "fetch --base custom ref exits 0"
assert_contains "$CAPTURE_OUTPUT" "fetched 2 patch" "fetch --base produces correct patch count"
artifact_path=$(printf '%s' "$CAPTURE_OUTPUT" | grep 'artifact dir:' | sed 's/.*artifact dir: //')
rm -rf "$artifact_path"

# --- Remote command static-ness ---
# Verify that the SSH commands don't contain raw repo paths or base64 values
source_repo_a="$TMP/repos/fetch-static-a"
init_repo "$source_repo_a" master
add_commit "$source_repo_a" "static test a"

source_repo_b="$TMP/repos/fetch-static-b"
init_repo "$source_repo_b" master
add_commit "$source_repo_b" "static test b"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo_a" --base origin/master
log_a=$(get_ssh_commands)
artifact_a=$(printf '%s' "$CAPTURE_OUTPUT" | grep 'artifact dir:' | sed 's/.*artifact dir: //')
rm -rf "$artifact_a"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo_b" --base HEAD~1
log_b=$(get_ssh_commands)
artifact_b=$(printf '%s' "$CAPTURE_OUTPUT" | grep 'artifact dir:' | sed 's/.*artifact dir: //')
rm -rf "$artifact_b"

# Extract generate commands (first CALL block from each log)
gen_a=$(printf '%s' "$log_a" | awk '/^CALL$/{n++} n==1 && !/^CALL$/ && !/^END$/{print}')
gen_b=$(printf '%s' "$log_b" | awk '/^CALL$/{n++} n==1 && !/^CALL$/ && !/^END$/{print}')
assert_equal "$gen_a" "$gen_b" "generate script is identical across different repos/bases"

# Verify raw values don't appear in commands
b64_repo_a=$(printf '%s' "$source_repo_a" | base64 -w 0)
assert_not_contains "$log_a" "$source_repo_a" "SSH commands don't contain raw source repo path"
assert_not_contains "$log_a" "$b64_repo_a" "SSH commands don't contain base64 source repo"

# --- Remote command injection guard ---
# Source repo with shell metacharacters
source_repo_special="$TMP/repos/fetch special;repo\$(echo injected)"
init_repo "$source_repo_special" master
add_commit "$source_repo_special" "injection test"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo_special"
assert_status 0 "fetch with shell metacharacters in repo path succeeds"
artifact_path=$(printf '%s' "$CAPTURE_OUTPUT" | grep 'artifact dir:' | sed 's/.*artifact dir: //')
[[ -f "$artifact_path/manifest.json" ]] && pass "fetch with metacharacters produces valid manifest" || \
  fail "fetch with metacharacters produces valid manifest"
rm -rf "$artifact_path"

# Invalid --base with shell metacharacters should fail safely
source_repo_meta="$TMP/repos/fetch-meta-base"
init_repo "$source_repo_meta" master
add_commit "$source_repo_meta" "meta base test"

sentinel_file="$TMP/injection-sentinel"
reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo_meta" \
  --base "\$(touch $sentinel_file)"
# Should fail (invalid ref) but not create sentinel
[[ "$CAPTURE_STATUS" -ne 0 ]] && pass "fetch with injected base exits non-zero" || \
  fail "fetch with injected base exits non-zero"
[[ ! -e "$sentinel_file" ]] && pass "fetch injection attempt does not create sentinel" || \
  fail "fetch injection attempt does not create sentinel" "sentinel exists at $sentinel_file"

# Long repo path forcing base64 past 76 bytes
long_component=$(printf '%0.sx' {1..80})
source_repo_long="$TMP/repos/$long_component"
init_repo "$source_repo_long" master
add_commit "$source_repo_long" "long path test"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo_long"
assert_status 0 "fetch with long repo path succeeds"
artifact_path=$(printf '%s' "$CAPTURE_OUTPUT" | grep 'artifact dir:' | sed 's/.*artifact dir: //')
rm -rf "$artifact_path"

# ================================================================
# apply tests
# ================================================================

# Helper: create a valid artifact dir from a source repo for apply tests
make_artifact() {
  local source_repo="$1" artifact_dir="$2"
  mkdir -p "$(dirname "$artifact_dir")"
  reset_mock_ssh
  set +e
  local out
  out=$(PATH="$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo" --output "$artifact_dir" 2>&1)
  local st=$?
  set -e
  if [[ "$st" -ne 0 ]]; then
    printf 'make_artifact failed: %s\n' "$out" >&2
    return 1
  fi
}

# --- Happy path ---
source_repo="$TMP/repos/apply-happy-source"
init_repo "$source_repo" master
add_commit "$source_repo" "apply test change" "applied content"

dest_repo="$TMP/repos/apply-happy-dest"
clone_repo "$source_repo" "$dest_repo"

artifact="$TMP/artifacts/apply-happy"
make_artifact "$source_repo" "$artifact"

pre_head=$(git -C "$dest_repo" rev-parse HEAD)
capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo"
assert_status 0 "apply happy path exits 0"
assert_contains "$CAPTURE_OUTPUT" "applied 1 patch" "apply reports patch count"
post_head=$(git -C "$dest_repo" rev-parse HEAD)
[[ "$post_head" != "$pre_head" ]] && pass "apply advances HEAD" || fail "apply advances HEAD"
assert_contains "$CAPTURE_OUTPUT" "git push" "apply without --push shows push reminder"

# Verify content was applied
applied_content=$(cat "$dest_repo/tracked.txt")
assert_equal "$applied_content" "applied content" "apply produces correct file content"

# --- Artifact path resolution (run from different cwd) ---
source_repo="$TMP/repos/apply-cwd-source"
init_repo "$source_repo" master
add_commit "$source_repo" "cwd test"

dest_repo="$TMP/repos/apply-cwd-dest"
clone_repo "$source_repo" "$dest_repo"

artifact="$TMP/artifacts/apply-cwd"
make_artifact "$source_repo" "$artifact"

capture bash -c "cd /tmp && bash '$ALLOD' patch apply '$artifact' --repo '$dest_repo'"
assert_status 0 "apply from different cwd exits 0"

# --- Destination repo resolution failure ---
source_repo="$TMP/repos/apply-missing-dest-source"
init_repo "$source_repo" master
add_commit "$source_repo" "missing destination test"

artifact="$TMP/artifacts/apply-missing-dest"
make_artifact "$source_repo" "$artifact"

missing_dest="$TMP/repos/apply-missing-dest-repo"
capture bash "$ALLOD" patch apply "$artifact" --repo "$missing_dest"
assert_status 1 "apply missing destination repo exits 1"
assert_contains "$CAPTURE_OUTPUT" "destination repository path is not a directory" \
  "apply missing destination names destination"
assert_contains "$CAPTURE_OUTPUT" "while resolving: <destination-repo>" \
  "apply missing destination shows argument context"
assert_contains "$CAPTURE_OUTPUT" "to fix:" "apply missing destination shows fix hint"
assert_not_contains "$CAPTURE_OUTPUT" "repo identity mismatch" \
  "apply missing destination is not reported as identity mismatch"
rm -rf "$artifact"

# --- Checksum mismatch ---
source_repo="$TMP/repos/apply-checksum-source"
init_repo "$source_repo" master
add_commit "$source_repo" "checksum test"

dest_repo="$TMP/repos/apply-checksum-dest"
clone_repo "$source_repo" "$dest_repo"

artifact="$TMP/artifacts/apply-checksum"
make_artifact "$source_repo" "$artifact"

# Tamper with the patch file
patch_file=$(jq -r '.patches[0].filename' "$artifact/manifest.json")
printf 'tampered\n' >> "$artifact/$patch_file"

capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo"
assert_status 12 "apply checksum mismatch exits 12"
assert_contains "$CAPTURE_OUTPUT" "checksum mismatch" "apply checksum mismatch message"

# --- Manifest validation: malformed JSON ---
bad_artifact="$TMP/artifacts/apply-bad-json"
mkdir -p "$bad_artifact"
printf 'not json' > "$bad_artifact/manifest.json"
capture bash "$ALLOD" patch apply "$bad_artifact" --repo "$dest_repo"
assert_status 12 "apply malformed JSON exits 12"

# --- Manifest validation: symlinked manifest ---
bad_artifact="$TMP/artifacts/apply-symlink-manifest"
mkdir -p "$bad_artifact"
ln -s /dev/null "$bad_artifact/manifest.json"
capture bash "$ALLOD" patch apply "$bad_artifact" --repo "$dest_repo"
assert_status 12 "apply symlinked manifest exits 12"
rm -rf "$bad_artifact"

# --- Manifest validation: missing required field ---
bad_artifact="$TMP/artifacts/apply-missing-field"
mkdir -p "$bad_artifact"
printf '{"repo_remote":"x","base_commit":"a"}' > "$bad_artifact/manifest.json"
capture bash "$ALLOD" patch apply "$bad_artifact" --repo "$dest_repo"
assert_status 12 "apply missing required field exits 12"
assert_contains "$CAPTURE_OUTPUT" "missing required field" "apply missing field message"
rm -rf "$bad_artifact"

# --- Manifest validation: non-full hex commit ---
bad_artifact="$TMP/artifacts/apply-bad-commit"
mkdir -p "$bad_artifact"
cat > "$bad_artifact/manifest.json" <<'BADJSON'
{"repo_remote":"x","base_commit":"abcdef","head_commit":"123456","patch_count":0,"patches":[]}
BADJSON
capture bash "$ALLOD" patch apply "$bad_artifact" --repo "$dest_repo"
assert_status 12 "apply non-full hex commit exits 12"
assert_contains "$CAPTURE_OUTPUT" "not a full lowercase hex commit ID" "apply bad commit message"
rm -rf "$bad_artifact"

# --- Manifest validation: refname commit (not hex) ---
bad_artifact="$TMP/artifacts/apply-refname-commit"
mkdir -p "$bad_artifact"
cat > "$bad_artifact/manifest.json" <<'BADJSON'
{"repo_remote":"x","base_commit":"refs/heads/master","head_commit":"refs/heads/master","patch_count":0,"patches":[]}
BADJSON
capture bash "$ALLOD" patch apply "$bad_artifact" --repo "$dest_repo"
assert_status 12 "apply refname commit exits 12"
rm -rf "$bad_artifact"

# --- Manifest validation: patch count mismatch ---
bad_artifact="$TMP/artifacts/apply-count-mismatch"
mkdir -p "$bad_artifact"
base_sha=$(git -C "$dest_repo" rev-parse HEAD)
cat > "$bad_artifact/manifest.json" <<BADJSON
{"repo_remote":"x","base_commit":"${base_sha}","head_commit":"${base_sha}","patch_count":5,"patches":[]}
BADJSON
capture bash "$ALLOD" patch apply "$bad_artifact" --repo "$dest_repo"
assert_status 12 "apply patch count mismatch exits 12"
assert_contains "$CAPTURE_OUTPUT" "does not match" "apply count mismatch message"
rm -rf "$bad_artifact"

# --- Manifest validation: duplicate filenames ---
bad_artifact="$TMP/artifacts/apply-dup-names"
mkdir -p "$bad_artifact"
printf 'patch content\n' > "$bad_artifact/0001-a.patch"
dup_sha=$(sha256sum -b -- "$bad_artifact/0001-a.patch" | awk '{print $1}')
cat > "$bad_artifact/manifest.json" <<BADJSON
{"repo_remote":"x","base_commit":"${base_sha}","head_commit":"${base_sha}","patch_count":2,"patches":[{"filename":"0001-a.patch","sha256":"${dup_sha}"},{"filename":"0001-a.patch","sha256":"${dup_sha}"}]}
BADJSON
capture bash "$ALLOD" patch apply "$bad_artifact" --repo "$dest_repo"
assert_status 12 "apply duplicate filenames exits 12"
assert_contains "$CAPTURE_OUTPUT" "duplicate" "apply duplicate message"
rm -rf "$bad_artifact"

# --- Manifest validation: path traversal filename ---
bad_artifact="$TMP/artifacts/apply-traversal"
mkdir -p "$bad_artifact"
cat > "$bad_artifact/manifest.json" <<BADJSON
{"repo_remote":"x","base_commit":"${base_sha}","head_commit":"${base_sha}","patch_count":1,"patches":[{"filename":"../etc/passwd.patch","sha256":"aaaa"}]}
BADJSON
capture bash "$ALLOD" patch apply "$bad_artifact" --repo "$dest_repo"
assert_status 12 "apply path traversal filename exits 12"
assert_contains "$CAPTURE_OUTPUT" "invalid" "apply traversal message"
rm -rf "$bad_artifact"

# --- Manifest validation: non-hex digest ---
bad_artifact="$TMP/artifacts/apply-bad-digest"
mkdir -p "$bad_artifact"
printf 'x\n' > "$bad_artifact/0001-a.patch"
cat > "$bad_artifact/manifest.json" <<BADJSON
{"repo_remote":"x","base_commit":"${base_sha}","head_commit":"${base_sha}","patch_count":1,"patches":[{"filename":"0001-a.patch","sha256":"not-a-hex-digest-but-it-is-64-chars-long-xxxxxxxxxxxxxxxxxx!!"}]}
BADJSON
capture bash "$ALLOD" patch apply "$bad_artifact" --repo "$dest_repo"
assert_status 12 "apply non-hex digest exits 12"
assert_contains "$CAPTURE_OUTPUT" "hex digest" "apply bad digest message"
rm -rf "$bad_artifact"

# --- Manifest validation: unlisted .patch file ---
source_repo="$TMP/repos/apply-unlisted-source"
init_repo "$source_repo" master
add_commit "$source_repo" "unlisted test"

dest_repo_unlisted="$TMP/repos/apply-unlisted-dest"
clone_repo "$source_repo" "$dest_repo_unlisted"

artifact="$TMP/artifacts/apply-unlisted"
make_artifact "$source_repo" "$artifact"
printf 'extra\n' > "$artifact/9999-extra.patch"

capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_unlisted"
assert_status 12 "apply unlisted .patch file exits 12"
assert_contains "$CAPTURE_OUTPUT" "unlisted" "apply unlisted message"
rm -rf "$artifact"

# --- Manifest validation: missing listed patch file ---
source_repo="$TMP/repos/apply-missing-patch-source"
init_repo "$source_repo" master
add_commit "$source_repo" "missing patch test"

dest_repo_missing="$TMP/repos/apply-missing-patch-dest"
clone_repo "$source_repo" "$dest_repo_missing"

artifact="$TMP/artifacts/apply-missing-patch"
make_artifact "$source_repo" "$artifact"
patch_file=$(jq -r '.patches[0].filename' "$artifact/manifest.json")
rm "$artifact/$patch_file"

capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_missing"
assert_status 12 "apply missing listed patch exits 12"
assert_contains "$CAPTURE_OUTPUT" "missing" "apply missing patch message"
rm -rf "$artifact"

# --- Manifest validation: symlink patch file ---
source_repo="$TMP/repos/apply-symlink-patch-source"
init_repo "$source_repo" master
add_commit "$source_repo" "symlink patch test"

dest_repo_sl="$TMP/repos/apply-symlink-patch-dest"
clone_repo "$source_repo" "$dest_repo_sl"

artifact="$TMP/artifacts/apply-symlink-patch"
make_artifact "$source_repo" "$artifact"
patch_file=$(jq -r '.patches[0].filename' "$artifact/manifest.json")
real_patch="$artifact/${patch_file}.real"
mv "$artifact/$patch_file" "$real_patch"
ln -s "$real_patch" "$artifact/$patch_file"

capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_sl"
assert_status 12 "apply symlink patch file exits 12"
assert_contains "$CAPTURE_OUTPUT" "symlink" "apply symlink patch message"
rm -rf "$artifact"

# --- Repo identity mismatch ---
source_repo="$TMP/repos/apply-mismatch-source"
init_repo "$source_repo" master
add_commit "$source_repo" "mismatch test"

dest_repo_mm="$TMP/repos/apply-mismatch-dest"
init_repo "$dest_repo_mm" master

artifact="$TMP/artifacts/apply-mismatch"
make_artifact "$source_repo" "$artifact"

capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_mm"
assert_status 13 "apply repo identity mismatch exits 13"
assert_contains "$CAPTURE_OUTPUT" "mismatch" "apply mismatch message"
assert_contains "$CAPTURE_OUTPUT" "manifest remote:" "apply mismatch shows manifest URL"
assert_contains "$CAPTURE_OUTPUT" "destination remote:" "apply mismatch shows dest URL"
rm -rf "$artifact"

# --- Base commit missing ---
source_repo="$TMP/repos/apply-base-missing-source"
init_repo "$source_repo" master

dest_repo_bm="$TMP/repos/apply-base-missing-dest"
clone_repo "$source_repo" "$dest_repo_bm"

# Add commits AFTER cloning dest, so dest doesn't have them
add_commit "$source_repo" "base missing prep"
git -C "$source_repo" push -q origin master
add_commit "$source_repo" "base missing change"

artifact="$TMP/artifacts/apply-base-missing"
make_artifact "$source_repo" "$artifact"

capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_bm"
assert_status 14 "apply base commit missing exits 14"
assert_contains "$CAPTURE_OUTPUT" "git fetch" "apply base missing hints git fetch"
rm -rf "$artifact"

# --- Base commit not ancestor (different branch) ---
source_repo="$TMP/repos/apply-not-ancestor-source"
init_repo "$source_repo" master
add_commit "$source_repo" "not-ancestor change"

dest_repo_na="$TMP/repos/apply-not-ancestor-dest"
clone_repo "$source_repo" "$dest_repo_na"

# Fetch so dest has the object, then checkout a branch that doesn't contain it
git -C "$dest_repo_na" fetch -q origin
git -C "$dest_repo_na" checkout -q --orphan orphan
git -C "$dest_repo_na" commit -q --allow-empty -m "orphan root"

artifact="$TMP/artifacts/apply-not-ancestor"
make_artifact "$source_repo" "$artifact"

capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_na"
assert_status 14 "apply base not ancestor exits 14"
assert_contains "$CAPTURE_OUTPUT" "not an ancestor" "apply not ancestor message"
rm -rf "$artifact"

# --- Dirty destination worktree ---
source_repo="$TMP/repos/apply-dirty-dest-source"
init_repo "$source_repo" master
add_commit "$source_repo" "dirty dest test"

dest_repo_dirty="$TMP/repos/apply-dirty-dest"
clone_repo "$source_repo" "$dest_repo_dirty"
printf 'dirty\n' > "$dest_repo_dirty/untracked.txt"

artifact="$TMP/artifacts/apply-dirty-dest"
make_artifact "$source_repo" "$artifact"

capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_dirty"
assert_status 16 "apply dirty destination exits 16"
rm "$dest_repo_dirty/untracked.txt"
rm -rf "$artifact"

# --- git am conflict ---
source_repo="$TMP/repos/apply-conflict-source"
init_repo "$source_repo" master
add_commit "$source_repo" "conflict change" "source version"

dest_repo_conflict="$TMP/repos/apply-conflict-dest"
clone_repo "$source_repo" "$dest_repo_conflict"
# Create conflicting change in destination
printf 'dest version\n' > "$dest_repo_conflict/tracked.txt"
git -C "$dest_repo_conflict" commit -qam "conflicting dest change"

artifact="$TMP/artifacts/apply-conflict"
make_artifact "$source_repo" "$artifact"

pre_head=$(git -C "$dest_repo_conflict" rev-parse HEAD)
capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_conflict"
assert_status 15 "apply conflict exits 15"
post_head=$(git -C "$dest_repo_conflict" rev-parse HEAD)
assert_equal "$post_head" "$pre_head" "apply conflict leaves HEAD unchanged"

# Verify clean state after abort
status_after=$(git -C "$dest_repo_conflict" status --porcelain)
[[ -z "$status_after" ]] && pass "apply conflict leaves worktree clean" || \
  fail "apply conflict leaves worktree clean" "$status_after"

ra_dir=$(git -C "$dest_repo_conflict" rev-parse --git-path rebase-apply)
rm_dir=$(git -C "$dest_repo_conflict" rev-parse --git-path rebase-merge)
[[ ! -d "$ra_dir" && ! -d "$rm_dir" ]] && pass "apply conflict removes rebase state" || \
  fail "apply conflict removes rebase state"
rm -rf "$artifact"

# --- Destination already ahead of base ---
source_repo="$TMP/repos/apply-ahead-source"
init_repo "$source_repo" master
add_commit "$source_repo" "ahead base change"

dest_repo_ahead="$TMP/repos/apply-ahead-dest"
clone_repo "$source_repo" "$dest_repo_ahead"
# Add a pre-existing non-conflicting commit in destination
printf 'dest-only\n' > "$dest_repo_ahead/dest-only.txt"
git -C "$dest_repo_ahead" add dest-only.txt
git -C "$dest_repo_ahead" commit -qm "pre-existing dest commit"

artifact="$TMP/artifacts/apply-ahead"
make_artifact "$source_repo" "$artifact"

capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_ahead"
assert_status 0 "apply with destination ahead of base exits 0"
rm -rf "$artifact"

# --- --push via mock git ---
source_repo="$TMP/repos/apply-push-source"
init_repo "$source_repo" master
add_commit "$source_repo" "push test"

dest_repo_push="$TMP/repos/apply-push-dest"
clone_repo "$source_repo" "$dest_repo_push"

artifact="$TMP/artifacts/apply-push"
make_artifact "$source_repo" "$artifact"

push_log="$TMP/push-apply.log"
: > "$push_log"
export REAL_GIT GIT_PUSH_LOG="$push_log"
mock_git_path=$(make_mock_git_path apply-push)

capture_with_path "$mock_git_path" bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_push" --push
assert_status 0 "apply --push exits 0"
push_calls=$(cat "$push_log")
assert_contains "$push_calls" "push" "apply --push calls git push"
rm -rf "$artifact"

# --- Without --push: no git push ---
source_repo="$TMP/repos/apply-nopush-source"
init_repo "$source_repo" master
add_commit "$source_repo" "no push test"

dest_repo_nopush="$TMP/repos/apply-nopush-dest"
clone_repo "$source_repo" "$dest_repo_nopush"

artifact="$TMP/artifacts/apply-nopush"
make_artifact "$source_repo" "$artifact"

push_log="$TMP/push-nopush.log"
: > "$push_log"
export GIT_PUSH_LOG="$push_log"
mock_git_path=$(make_mock_git_path apply-nopush)

capture_with_path "$mock_git_path" bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_nopush"
assert_status 0 "apply without --push exits 0"
push_calls=$(cat "$push_log")
[[ -z "$push_calls" ]] && pass "apply without --push does not call git push" || \
  fail "apply without --push does not call git push" "$push_calls"
rm -rf "$artifact"

# --- Multiple patches ---
source_repo="$TMP/repos/apply-multi-source"
init_repo "$source_repo" master
add_commit "$source_repo" "multi change one" "one"
add_commit "$source_repo" "multi change two" "two"
add_commit "$source_repo" "multi change three" "three"

dest_repo_multi="$TMP/repos/apply-multi-dest"
clone_repo "$source_repo" "$dest_repo_multi"

artifact="$TMP/artifacts/apply-multi"
make_artifact "$source_repo" "$artifact"

pre_head=$(git -C "$dest_repo_multi" rev-parse HEAD)
capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_multi"
assert_status 0 "apply multiple patches exits 0"
assert_contains "$CAPTURE_OUTPUT" "applied 3 patch" "apply reports 3 patches"
commit_count=$(git -C "$dest_repo_multi" rev-list --count "$pre_head..HEAD")
assert_equal "$commit_count" "3" "apply creates 3 commits"
final_content=$(cat "$dest_repo_multi/tracked.txt")
assert_equal "$final_content" "three" "apply multiple patches produces correct final content"
rm -rf "$artifact"

# --- Post-apply whitespace failure ---
source_repo="$TMP/repos/apply-whitespace-source"
init_repo "$source_repo" master
# Create a commit with trailing whitespace
printf 'trailing whitespace   \n' > "$source_repo/tracked.txt"
git -C "$source_repo" add tracked.txt
git -C "$source_repo" commit -qm "whitespace commit"

dest_repo_ws="$TMP/repos/apply-whitespace-dest"
clone_repo "$source_repo" "$dest_repo_ws"

artifact="$TMP/artifacts/apply-whitespace"
make_artifact "$source_repo" "$artifact"

pre_head=$(git -C "$dest_repo_ws" rev-parse HEAD)
capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_ws"
assert_status 1 "apply whitespace failure exits 1"
assert_contains "$CAPTURE_OUTPUT" "whitespace" "apply whitespace message"
assert_contains "$CAPTURE_OUTPUT" "pre-apply HEAD" "apply whitespace shows pre-apply HEAD"
# Commits should still be applied
post_head=$(git -C "$dest_repo_ws" rev-parse HEAD)
[[ "$post_head" != "$pre_head" ]] && pass "apply whitespace failure keeps applied commits" || \
  fail "apply whitespace failure keeps applied commits"
rm -rf "$artifact"

# --- --push failure ---
source_repo="$TMP/repos/apply-push-fail-source"
init_repo "$source_repo" master
add_commit "$source_repo" "push fail test"

dest_repo_pf="$TMP/repos/apply-push-fail-dest"
clone_repo "$source_repo" "$dest_repo_pf"
# Break the actual bare remote so push fails without changing the URL
remote_url=$(git -C "$dest_repo_pf" remote get-url origin)
rm -rf "$remote_url"

artifact="$TMP/artifacts/apply-push-fail"
make_artifact "$source_repo" "$artifact"

capture bash "$ALLOD" patch apply "$artifact" --repo "$dest_repo_pf" --push
assert_status 1 "apply --push failure exits 1"
assert_contains "$CAPTURE_OUTPUT" "push failed" "apply push failure message"
assert_contains "$CAPTURE_OUTPUT" "pre-apply HEAD" "apply push failure shows pre-apply HEAD"
# Commits should still be in place
post_head=$(git -C "$dest_repo_pf" rev-parse HEAD)
pre_head=$(git -C "$dest_repo_pf" rev-parse HEAD~1)
[[ "$post_head" != "$pre_head" ]] && pass "apply push failure keeps applied commits" || \
  fail "apply push failure keeps applied commits"
rm -rf "$artifact"

# ================================================================
# receive tests
# ================================================================

# --- Happy path ---
source_repo="$TMP/repos/receive-happy-source"
init_repo "$source_repo" master
add_commit "$source_repo" "receive test change" "received content"

dest_repo_recv="$TMP/repos/receive-happy-dest"
clone_repo "$source_repo" "$dest_repo_recv"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch receive "testhost:$source_repo" "$dest_repo_recv"
assert_status 0 "receive happy path exits 0"
assert_contains "$CAPTURE_OUTPUT" "artifact dir:" "receive prints artifact dir"

recv_artifact=$(printf '%s' "$CAPTURE_OUTPUT" | grep 'artifact dir:' | tail -1 | sed 's/.*artifact dir: //')
[[ -d "$recv_artifact" ]] && pass "receive artifact dir exists" || \
  fail "receive artifact dir exists" "missing: $recv_artifact"

recv_content=$(cat "$dest_repo_recv/tracked.txt")
assert_equal "$recv_content" "received content" "receive applies patches to destination"

# --- Destination repo preflight failure ---
source_repo="$TMP/repos/receive-missing-dest-source"
init_repo "$source_repo" master
add_commit "$source_repo" "receive missing destination test"

cwd_repo="$TMP/repos/receive-missing-dest-cwd"
init_repo "$cwd_repo" master

missing_dest="$TMP/repos/receive-missing-dest-repo"
reset_mock_ssh
capture_with_path "$MOCK_PATH" bash -c \
  "cd '$cwd_repo' && bash '$ALLOD' patch receive 'testhost:$source_repo' '$missing_dest'"
assert_status 1 "receive missing destination repo exits 1"
assert_contains "$CAPTURE_OUTPUT" "destination repository path is not a directory" \
  "receive missing destination names destination"
assert_contains "$CAPTURE_OUTPUT" "while resolving: <destination-repo>" \
  "receive missing destination shows argument context"
assert_contains "$CAPTURE_OUTPUT" "to fix:" "receive missing destination shows fix hint"
assert_not_contains "$CAPTURE_OUTPUT" "repo identity mismatch" \
  "receive missing destination is not reported as identity mismatch"
assert_not_contains "$CAPTURE_OUTPUT" "artifact dir:" \
  "receive missing destination does not fetch an artifact"
assert_equal "$(get_ssh_call_count)" "0" "receive missing destination makes no SSH calls"

# --- Fetch failure propagation ---
source_repo="$TMP/repos/receive-fetch-fail"
init_repo "$source_repo" master

dest_repo_ff="$TMP/repos/receive-fetch-fail-dest"
clone_repo "$source_repo" "$dest_repo_ff"

# Clean up any leftover receive dirs from prior tests
find /tmp -maxdepth 1 -type d -name "allod-patch-receive.*" -exec rm -rf {} + 2>/dev/null || true

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch receive "testhost:$source_repo" "$dest_repo_ff"
assert_status 11 "receive propagates fetch failure exit code (11 for no commits)"

# Verify receive-owned parent is cleaned up when no artifact was promoted
recv_parents=$(find /tmp -maxdepth 1 -type d -name "allod-patch-receive.*" 2>/dev/null | wc -l)
assert_equal "$recv_parents" "0" "receive cleans up parent dir on fetch failure"

# --- Fetch cleanup failure after promotion ---
source_repo="$TMP/repos/receive-cleanup-fail"
init_repo "$source_repo" master
add_commit "$source_repo" "cleanup fail receive test"

dest_repo_cf="$TMP/repos/receive-cleanup-fail-dest"
clone_repo "$source_repo" "$dest_repo_cf"

reset_mock_ssh
export MOCK_SSH_FAIL="cleanup"
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch receive "testhost:$source_repo" "$dest_repo_cf"
assert_status 1 "receive propagates cleanup failure"
assert_contains "$CAPTURE_OUTPUT" "artifact dir preserved" "receive cleanup failure preserves artifact"
# Apply should not have run
dest_head=$(git -C "$dest_repo_cf" rev-parse HEAD)
initial_head=$(git -C "$dest_repo_cf" rev-parse origin/master)
assert_equal "$dest_head" "$initial_head" "receive does not run apply after fetch cleanup failure"
export MOCK_SSH_FAIL=""

# --- Apply failure propagation ---
source_repo="$TMP/repos/receive-apply-fail-source"
init_repo "$source_repo" master
add_commit "$source_repo" "apply fail receive test"

# Destination with different origin (triggers mismatch)
dest_repo_af="$TMP/repos/receive-apply-fail-dest"
init_repo "$dest_repo_af" master

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch receive "testhost:$source_repo" "$dest_repo_af"
assert_status 13 "receive propagates apply failure exit code (13 for mismatch)"
assert_contains "$CAPTURE_OUTPUT" "artifact dir:" "receive prints artifact dir on apply failure"

# --- --push passthrough ---
source_repo="$TMP/repos/receive-push-source"
init_repo "$source_repo" master
add_commit "$source_repo" "push passthrough"

dest_repo_rp="$TMP/repos/receive-push-dest"
clone_repo "$source_repo" "$dest_repo_rp"

push_log="$TMP/push-receive.log"
: > "$push_log"
export REAL_GIT GIT_PUSH_LOG="$push_log"
mock_git_path=$(make_mock_git_path receive-push)
combined_path="${mock_git_path%%:*}:$MOCK_PATH"

reset_mock_ssh
capture_with_path "$combined_path" bash "$ALLOD" patch receive "testhost:$source_repo" "$dest_repo_rp" --push
assert_status 0 "receive --push exits 0"
push_calls=$(cat "$push_log")
assert_contains "$push_calls" "push" "receive --push triggers git push"

# ================================================================
# Additional validation tests (review findings)
# ================================================================

# --- Manifest validation: control-character filename ---
bad_artifact="$TMP/artifacts/apply-ctrl-char"
mkdir -p "$bad_artifact"
base_sha=$(git -C "$dest_repo" rev-parse HEAD)
cat > "$bad_artifact/manifest.json" <<BADJSON
{"repo_remote":"x","base_commit":"${base_sha}","head_commit":"${base_sha}","patch_count":1,"patches":[{"filename":"0001-foo\u0007bar.patch","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}
BADJSON
capture bash "$ALLOD" patch apply "$bad_artifact" --repo "$dest_repo"
assert_status 12 "apply control-character filename exits 12"
assert_contains "$CAPTURE_OUTPUT" "invalid" "apply control-character filename message"
rm -rf "$bad_artifact"

# --- Remote tmpdir validation: empty response ---
source_repo="$TMP/repos/fetch-tmpdir-empty"
init_repo "$source_repo" master
add_commit "$source_repo" "tmpdir empty test"

reset_mock_ssh
export MOCK_SSH_FAIL="bad_tmpdir_"
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 1 "fetch empty tmpdir exits 1"
assert_contains "$CAPTURE_OUTPUT" "no output" "fetch empty tmpdir message"
export MOCK_SSH_FAIL=""

# --- Remote tmpdir validation: relative path ---
reset_mock_ssh
export MOCK_SSH_FAIL="bad_tmpdir_tmp/allod-patch.abcdefghij"
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 1 "fetch relative tmpdir exits 1"
assert_contains "$CAPTURE_OUTPUT" "invalid remote temp dir" "fetch relative tmpdir message"
export MOCK_SSH_FAIL=""

# --- Remote tmpdir validation: wrong prefix ---
reset_mock_ssh
export MOCK_SSH_FAIL="bad_tmpdir_/tmp/evil-dir.abcdefghij"
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 1 "fetch wrong-prefix tmpdir exits 1"
assert_contains "$CAPTURE_OUTPUT" "invalid remote temp dir" "fetch wrong-prefix tmpdir message"
export MOCK_SSH_FAIL=""

# --- Remote tmpdir validation: path traversal ---
reset_mock_ssh
export MOCK_SSH_FAIL="bad_tmpdir_/tmp/allod-patch.abcdefghij/../../../etc"
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo"
assert_status 1 "fetch traversal tmpdir exits 1"
export MOCK_SSH_FAIL=""

# --- --output with unwritable parent ---
unwritable_parent="$TMP/unwritable-parent"
mkdir -p "$unwritable_parent"
chmod 000 "$unwritable_parent"

reset_mock_ssh
capture_with_path "$MOCK_PATH" bash "$ALLOD" patch fetch "testhost:$source_repo" --output "$unwritable_parent/patches"
assert_status 1 "fetch --output unwritable parent exits 1"
assert_equal "$(get_ssh_call_count)" "0" "fetch --output unwritable parent makes no SSH calls"
chmod 755 "$unwritable_parent"
rm -rf "$unwritable_parent"

# ================================================================
# Summary
# ================================================================

printf '\nTests run: %d\n' "$test_number"
printf 'All %d allod patch tests passed.\n' "$test_number"
