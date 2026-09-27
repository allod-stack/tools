#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
policy="$repo_root/git-hooks/protected-refs-policy"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
test_stdout="$tmp/stdout"
test_stderr="$tmp/stderr"

export HOME="$tmp/home"
mkdir -p "$HOME/.config/git" "$HOME/work/test-repo"
cat > "$HOME/.config/git/protected-branches" <<'FIXTURE'
work/test-repo main
FIXTURE

git -C "$HOME/work/test-repo" init --initial-branch=main >/dev/null 2>&1
git -C "$HOME/work/test-repo" config user.name "Test User"
git -C "$HOME/work/test-repo" config user.email "test@example.invalid"
git -C "$HOME/work/test-repo" commit --allow-empty -m initial >/dev/null 2>&1

counter_file="$tmp/test_count"
printf '0' > "$counter_file"

pass() {
  local n=$(( $(<"$counter_file") + 1 ))
  printf '%d' "$n" > "$counter_file"
  printf '✅ %d - %s\n' "$n" "$1"
}

fail() {
  local n=$(( $(<"$counter_file") + 1 ))
  printf '%d' "$n" > "$counter_file"
  printf '❌ %d - %s\n' "$n" "$1" >&2
  shift
  printf '%s\n' "$@" >&2
  exit 1
}

assert_blocks() {
  local description="$1"
  shift
  if "$@" >"$test_stdout" 2>"$test_stderr"; then
    fail "$description" "expected policy to block, but it allowed"
  else
    pass "$description"
  fi
}

assert_allows() {
  local description="$1"
  shift
  if ! "$@" >"$test_stdout" 2>"$test_stderr"; then
    fail "$description" "expected policy to allow, but it blocked:" "$(cat "$test_stderr")"
  else
    pass "$description"
  fi
}

assert_same_commit() {
  local description="$1" actual="$2" expected="$3"
  if [ "$actual" = "$expected" ]; then
    pass "$description"
  else
    fail "$description" "expected HEAD: $expected" "actual HEAD: $actual"
  fi
}

forge_url="ssh://git@forge.anarch.diy:2222/vnprc/repo.git"
zero="0000000000000000000000000000000000000000"
# Resolved, not '#!/usr/bin/env bash': the fixture hooks below are written at run
# time, so patchShebangs cannot reach them and a sandbox has no /usr/bin/env.
hook_shebang="#!$(command -v bash)"

# --- Protected branch: pre-commit ---

cd "$HOME/work/test-repo"

assert_blocks "pre-commit: blocks commit on protected branch" \
  bash "$policy" pre-commit

git checkout -b agent/test >/dev/null 2>&1

assert_allows "pre-commit: allows commit on non-protected branch" \
  bash "$policy" pre-commit

# --- Protected branch: pre-rebase ---

git checkout main >/dev/null 2>&1

assert_blocks "pre-rebase: blocks rebase on protected branch" \
  bash "$policy" pre-rebase origin/main main

# --- Protected branch: pre-merge-commit ---

assert_blocks "pre-merge-commit: blocks merge into protected branch" \
  bash "$policy" pre-merge-commit

# --- Pre-push: external remote blocking ---

printf '%s %s %s %s\n' \
  refs/heads/agent/test "${zero}1" refs/heads/main "${zero}2" \
  | assert_blocks "pre-push: blocks push to unauthorized remote" \
    bash "$policy" pre-push origin ssh://example.invalid/repo.git

# --- Pre-push: AGit ref blocking ---

printf '%s %s %s %s\n' \
  refs/heads/agent/test "${zero}1" refs/for/master/some-topic "$zero" \
  | assert_blocks "pre-push: blocks AGit refs/for/* push" \
    bash "$policy" pre-push origin "$forge_url"

grep -q "AGit is not an accepted intake path" "$test_stderr" \
  && pass "pre-push: AGit block explains the sanctioned intake paths" \
  || fail "pre-push: AGit block explains the sanctioned intake paths" \
    "expected AGit intake-path message in stderr, got: $(cat "$test_stderr")"

printf '%s %s %s %s\n' \
  refs/heads/agent/foo "${zero}1" refs/heads/agent/foo "$zero" \
  | assert_allows "pre-push: allows ordinary agent/foo push" \
    bash "$policy" pre-push origin "$forge_url"

# --- Pre-push: force-push blocking ---

git checkout -b agent/feature >/dev/null 2>&1
git commit --allow-empty -m "feature 1" >/dev/null 2>&1
feature_sha="$(git rev-parse HEAD)"
base_sha="$(git rev-parse HEAD~1)"

git checkout -b divergent HEAD~1 >/dev/null 2>&1
git commit --allow-empty -m "divergent" >/dev/null 2>&1
divergent_sha="$(git rev-parse HEAD)"

printf '%s %s %s %s\n' \
  "refs/heads/agent/feature" "$feature_sha" "refs/heads/agent/feature" "$base_sha" \
  | assert_allows "pre-push: allows fast-forward push to agent/* branch" \
    bash "$policy" pre-push origin "$forge_url"

printf '%s %s %s %s\n' \
  "refs/heads/agent/feature" "$divergent_sha" "refs/heads/agent/feature" "$feature_sha" \
  | assert_blocks "pre-push: blocks force-push to agent/* branch" \
    bash "$policy" pre-push origin "$forge_url"

printf '%s %s %s %s\n' \
  "refs/heads/my-branch" "$divergent_sha" "refs/heads/my-branch" "$feature_sha" \
  | assert_allows "pre-push: allows force-push to non-agent branch" \
    bash "$policy" pre-push origin "$forge_url"

printf '%s %s %s %s\n' \
  "refs/heads/agent/new" "$divergent_sha" "refs/heads/agent/new" "$zero" \
  | assert_allows "pre-push: allows new branch push to agent/* (no remote history)" \
    bash "$policy" pre-push origin "$forge_url"

# --- Non-protected repo ---

mkdir -p "$HOME/work/other-repo"
git -C "$HOME/work/other-repo" init --initial-branch=main >/dev/null 2>&1
git -C "$HOME/work/other-repo" config user.name "Test User"
git -C "$HOME/work/other-repo" config user.email "test@example.invalid"
git -C "$HOME/work/other-repo" commit --allow-empty -m initial >/dev/null 2>&1
cd "$HOME/work/other-repo"

assert_allows "pre-commit: allows commit in non-protected repo" \
  bash "$policy" pre-commit

# --- Tracked hook dispatch: .hookspath ---

hookspath_repo="$HOME/work/hookspath-repo"
mkdir -p "$hookspath_repo"
git -C "$hookspath_repo" init --initial-branch=dev >/dev/null 2>&1
git -C "$hookspath_repo" config user.name "Test User"
git -C "$hookspath_repo" config user.email "test@example.invalid"
git -C "$hookspath_repo" commit --allow-empty -m initial >/dev/null 2>&1

mkdir -p "$hookspath_repo/misc/git-hooks"
printf '%s\nprintf "hookspath-ran\n"\n' "$hook_shebang" > "$hookspath_repo/misc/git-hooks/pre-commit"
chmod +x "$hookspath_repo/misc/git-hooks/pre-commit"
printf 'misc/git-hooks\n' > "$hookspath_repo/.hookspath"

cd "$hookspath_repo"

assert_allows "tracked hook: runs hook found via .hookspath" \
  bash "$policy" pre-commit

grep -q "hookspath-ran" "$test_stdout" \
  && pass "tracked hook: .hookspath hook produced expected output" \
  || fail "tracked hook: .hookspath hook produced expected output" \
    "expected 'hookspath-ran' in stdout, got: $(cat "$test_stdout")"

# --- Tracked hook dispatch: .hooks/ fallback ---

fallback_repo="$HOME/work/fallback-repo"
mkdir -p "$fallback_repo"
git -C "$fallback_repo" init --initial-branch=dev >/dev/null 2>&1
git -C "$fallback_repo" config user.name "Test User"
git -C "$fallback_repo" config user.email "test@example.invalid"
git -C "$fallback_repo" commit --allow-empty -m initial >/dev/null 2>&1

mkdir -p "$fallback_repo/.hooks"
printf '%s\nprintf "fallback-ran\n"\n' "$hook_shebang" > "$fallback_repo/.hooks/pre-commit"
chmod +x "$fallback_repo/.hooks/pre-commit"

cd "$fallback_repo"

assert_allows "tracked hook: runs hook found via .hooks/ fallback" \
  bash "$policy" pre-commit

grep -q "fallback-ran" "$test_stdout" \
  && pass "tracked hook: .hooks/ fallback hook produced expected output" \
  || fail "tracked hook: .hooks/ fallback hook produced expected output" \
    "expected 'fallback-ran' in stdout, got: $(cat "$test_stdout")"

# --- Tracked hook dispatch: no hooks present ---

nohook_repo="$HOME/work/nohook-repo"
mkdir -p "$nohook_repo"
git -C "$nohook_repo" init --initial-branch=dev >/dev/null 2>&1
git -C "$nohook_repo" config user.name "Test User"
git -C "$nohook_repo" config user.email "test@example.invalid"
git -C "$nohook_repo" commit --allow-empty -m initial >/dev/null 2>&1
cd "$nohook_repo"

assert_allows "tracked hook: succeeds silently when no .hookspath or .hooks/" \
  bash "$policy" pre-commit

# --- Repository identity: the cases shared with the Go rails ---
#
# tests/fixtures/protection-cases.tsv is the one table both implementations of
# the rule answer to; its header states the columns. Each row gets its own $HOME
# because the branch list is part of the case.

cases_file="$repo_root/tests/fixtures/protection-cases.tsv"
if [ ! -r "$cases_file" ]; then
  fail "shared case table is readable" "missing or unreadable: $cases_file"
fi

write_branch_list() {
  local target="$1" entries="$2" entry
  : > "$target"
  if [ "$entries" = "-" ]; then
    return 0
  fi
  local IFS=';'
  for entry in $entries; do
    printf '%s %s\n' "${entry%%=*}" "${entry#*=}" >> "$target"
  done
}

init_case_repo() {
  local dir="$1" branch="$2"
  mkdir -p "$dir"
  git -C "$dir" init -q --initial-branch="$branch" >/dev/null
  git -C "$dir" config user.name "Test User"
  git -C "$dir" config user.email "test@example.invalid"
  git -C "$dir" commit -q --allow-empty -m initial >/dev/null
}

set_case_origin() {
  local dir="$1" origin="$2"
  if git -C "$dir" remote get-url origin >/dev/null 2>&1; then
    git -C "$dir" remote remove origin
  fi
  if [ "$origin" != "-" ]; then
    git -C "$dir" remote add origin "$origin"
  fi
}

# Prints the $HOME the case runs under: git reports physical paths, so a
# symlinked or slash-suffixed $HOME is a way for a correctly placed checkout to
# read as misplaced.
make_case_home() {
  local root="$1" home_kind="$2"
  case "$home_kind" in
    plain) mkdir -p "$root/home"; printf '%s\n' "$root/home" ;;
    trailing-slash) mkdir -p "$root/home"; printf '%s/\n' "$root/home" ;;
    symlink)
      mkdir -p "$root/real-home"
      ln -s "$root/real-home" "$root/home"
      printf '%s\n' "$root/home"
      ;;
    *) printf 'unknown home kind: %s\n' "$home_kind" >&2; return 1 ;;
  esac
}

# Builds one case and prints the directory the hook is to run in.
init_case_checkout() {
  local home="$1" origin="$2" checkout="$3" layout="$4" branch="$5" name="$6"
  local repo="${home%/}/$checkout" run_dir super source_repo git_dir
  case "$layout" in
    plain|worktree)
      init_case_repo "$repo" bootstrap
      ;;
    submodule)
      super="$(dirname "$repo")"
      source_repo="${home%/}/submodule-sources/$name"
      init_case_repo "$source_repo" bootstrap
      init_case_repo "$super" bootstrap
      git -C "$super" -c protocol.file.allow=always \
        submodule add -q "$source_repo" "$(basename "$repo")" >/dev/null
      git -C "$super" commit -q -m "add submodule" >/dev/null
      git -C "$repo" config user.name "Test User"
      git -C "$repo" config user.email "test@example.invalid"
      ;;
    separate-git-dir)
      git_dir="${home%/}/gitdirs/$name"
      mkdir -p "$(dirname "$git_dir")" "$repo"
      git -C "$repo" init -q --separate-git-dir="$git_dir" \
        --initial-branch=bootstrap >/dev/null
      git -C "$repo" config user.name "Test User"
      git -C "$repo" config user.email "test@example.invalid"
      git -C "$repo" commit -q --allow-empty -m initial >/dev/null
      ;;
    *)
      printf 'unknown layout: %s\n' "$layout" >&2
      return 1
      ;;
  esac
  set_case_origin "$repo" "$origin"
  # One branch cannot be checked out twice, so a worktree case keeps the branch
  # under test out of the repository it belongs to.
  if [ "$layout" = "worktree" ]; then
    run_dir="${home%/}/worktrees/$name"
    mkdir -p "${home%/}/worktrees"
    git -C "$repo" worktree add -q -b "$branch" "$run_dir" >/dev/null
  else
    run_dir="$repo"
    git -C "$repo" checkout -q -b "$branch" >/dev/null
  fi
  printf '%s\n' "$run_dir"
}

case_count=0
while IFS=$'\t' read -r -u 3 name entries origin checkout layout home_kind branch verdict expected; do
  case "$name" in ''|\#*) continue ;; esac
  case_count=$((case_count + 1))
  case_home="$(make_case_home "$tmp/cases/$name" "$home_kind")"
  mkdir -p "${case_home%/}/.config/git"
  write_branch_list "${case_home%/}/.config/git/protected-branches" "$entries"
  export HOME="$case_home"
  run_dir="$(init_case_checkout "$case_home" "$origin" "$checkout" "$layout" "$branch" "$name")"

  set +e
  ( cd "$run_dir" && bash "$policy" pre-commit ) >"$test_stdout" 2>"$test_stderr" </dev/null
  case_status=$?
  set -e

  case "$verdict" in
    unprotected)
      if [ "$case_status" -ne 0 ]; then
        fail "case $name: pre-commit allowed" "hook blocked it:" "$(cat "$test_stderr")"
      fi
      if [ -s "$test_stderr" ]; then
        fail "case $name: pre-commit silent" "stderr:" "$(cat "$test_stderr")"
      fi
      pass "case $name: pre-commit allowed, silently"
      ;;
    protected)
      if [ "$case_status" -eq 0 ]; then
        fail "case $name: pre-commit blocked" "hook allowed the commit"
      fi
      if ! grep -q "not permitted on protected branch '$branch'" "$test_stderr"; then
        fail "case $name: block names the branch" "stderr:" "$(cat "$test_stderr")"
      fi
      if grep -q "Move the checkout" "$test_stderr"; then
        fail "case $name: block reports no misplacement" "stderr:" "$(cat "$test_stderr")"
      fi
      pass "case $name: pre-commit blocked on the protected branch"
      ;;
    mismatch)
      if [ "$case_status" -eq 0 ]; then
        fail "case $name: pre-commit blocked" "hook allowed the commit"
      fi
      if ! grep -q "not permitted on protected branch '$branch'" "$test_stderr"; then
        fail "case $name: block names the branch" "stderr:" "$(cat "$test_stderr")"
      fi
      if ! grep -q "expects the checkout at '$expected'" "$test_stderr"; then
        fail "case $name: block names the expected path" "stderr:" "$(cat "$test_stderr")"
      fi
      if ! grep -q "it is at '$checkout'" "$test_stderr"; then
        fail "case $name: block names the actual path" "stderr:" "$(cat "$test_stderr")"
      fi
      pass "case $name: pre-commit blocked, naming both paths"
      ;;
    *)
      fail "case $name: verdict is a known word" "unknown verdict: $verdict"
      ;;
  esac
done 3< "$cases_file"

table_rows="$(awk '!/^#/ && NF > 0' "$cases_file" | wc -l)"
if [ "$case_count" -eq 0 ]; then
  fail "shared case table was consumed" "no rows ran"
fi
if [ "$case_count" -ne "$table_rows" ]; then
  fail "shared case table was consumed whole" "ran $case_count of $table_rows rows"
fi
pass "shared case table: every one of its $case_count rows ran"

assert_names_both_paths() {
  local description="$1" expected="$2" actual="$3"
  if ! grep -q "expects the checkout at '$expected'" "$test_stderr"; then
    fail "$description" "expected path missing from stderr:" "$(cat "$test_stderr")"
  fi
  if ! grep -q "it is at '$actual'" "$test_stderr"; then
    fail "$description" "actual path missing from stderr:" "$(cat "$test_stderr")"
  fi
  pass "$description"
}

# A fixture hooks directory, so the cases below witness the hook the way git
# invokes it and not only the script called by hand.
hooks_fixture="$tmp/hooks-fixture"
mkdir -p "$hooks_fixture"
printf '%s\nexec bash %q pre-commit "$@"\n' "$hook_shebang" "$policy" > "$hooks_fixture/pre-commit"
chmod +x "$hooks_fixture/pre-commit"

# --- Near miss: a protected repo checked out where no entry names ---

export HOME="$tmp/near-miss-home"
mkdir -p "$HOME/.config/git"
printf 'work/acme/widget master\n' > "$HOME/.config/git/protected-branches"
printf 'forge.example\n' > "$HOME/.config/git/allowed-external-remotes"

near_origin="ssh://git@forge.example:2222/acme/widget.git"
near_repo="$HOME/work/acme-widget"
mkdir -p "$near_repo"
git -C "$near_repo" init -q --initial-branch=master
git -C "$near_repo" config user.name "Test User"
git -C "$near_repo" config user.email "test@example.invalid"
git -C "$near_repo" commit -q --allow-empty -m initial
git -C "$near_repo" remote add origin "$near_origin"
near_sha="$(git -C "$near_repo" rev-parse HEAD)"

cd "$near_repo"

assert_blocks "near miss: blocks commit on the protected branch" \
  bash "$policy" pre-commit
assert_names_both_paths "near miss: commit block names both paths" \
  work/acme/widget work/acme-widget

assert_blocks "near miss: blocks rebase of the protected branch" \
  bash "$policy" pre-rebase origin/master master
assert_names_both_paths "near miss: rebase block names both paths" \
  work/acme/widget work/acme-widget

assert_blocks "near miss: blocks merge into the protected branch" \
  bash "$policy" pre-merge-commit
assert_names_both_paths "near miss: merge block names both paths" \
  work/acme/widget work/acme-widget

printf '%s %s %s %s\n' \
  refs/heads/master "$near_sha" refs/heads/agent/x "$zero" \
  | assert_blocks "near miss: blocks push from the protected branch" \
    bash "$policy" pre-push origin "$near_origin"
assert_names_both_paths "near miss: push-from block names both paths" \
  work/acme/widget work/acme-widget

printf '%s %s %s %s\n' \
  refs/heads/agent/x "$near_sha" refs/heads/master "$zero" \
  | assert_blocks "near miss: blocks push to the protected branch" \
    bash "$policy" pre-push origin "$near_origin"
assert_names_both_paths "near miss: push-to block names both paths" \
  work/acme/widget work/acme-widget

printf '%s %s %s %s\n' \
  refs/heads/agent/x "$near_sha" refs/heads/agent/x "$zero" \
  | assert_allows "near miss: allows an agent branch push" \
    bash "$policy" pre-push origin "$near_origin"

# Driven through core.hooksPath, as git would.
git -C "$near_repo" config core.hooksPath "$hooks_fixture"
printf 'edit\n' > "$near_repo/file.txt"
git -C "$near_repo" add file.txt

assert_blocks "near miss: git commit is refused through core.hooksPath" \
  git -C "$near_repo" commit -m "must be refused"
assert_names_both_paths "near miss: the refused commit names both paths" \
  work/acme/widget work/acme-widget
assert_same_commit "near miss: the refused commit left HEAD where it was" \
  "$(git -C "$near_repo" rev-parse HEAD)" "$near_sha"

# The same fixture hooks directory over a repository nothing lists, so the
# refusal above cannot be core.hooksPath breaking every commit.
unlisted_repo="$HOME/work/other/gadget"
mkdir -p "$unlisted_repo"
git -C "$unlisted_repo" init -q --initial-branch=master
git -C "$unlisted_repo" config user.name "Test User"
git -C "$unlisted_repo" config user.email "test@example.invalid"
git -C "$unlisted_repo" remote add origin "https://forge.example/other/gadget.git"
git -C "$unlisted_repo" config core.hooksPath "$hooks_fixture"
printf 'edit\n' > "$unlisted_repo/file.txt"
git -C "$unlisted_repo" add file.txt

assert_allows "unlisted repo: git commit succeeds through the same core.hooksPath" \
  git -C "$unlisted_repo" commit -m "allowed"

# --- Linked worktree of a canonically placed protected repo ---

export HOME="$tmp/worktree-home"
mkdir -p "$HOME/.config/git"
printf 'work/acme/widget master\n' > "$HOME/.config/git/protected-branches"
printf 'work/acme/widget agent/signed\n' > "$HOME/.config/git/signing-required-branches"
printf 'forge.example\n' > "$HOME/.config/git/allowed-external-remotes"

wt_main="$HOME/work/acme/widget"
mkdir -p "$wt_main"
git -C "$wt_main" init -q --initial-branch=bootstrap
git -C "$wt_main" config user.name "Test User"
git -C "$wt_main" config user.email "test@example.invalid"
git -C "$wt_main" commit -q --allow-empty -m initial
git -C "$wt_main" remote add origin "$near_origin"
wt_protected="$HOME/changes/widget-master"
wt_agent="$HOME/changes/widget-agent"
git -C "$wt_main" worktree add -q -b master "$wt_protected"
git -C "$wt_main" worktree add -q -b agent/x "$wt_agent"
wt_sha="$(git -C "$wt_agent" rev-parse HEAD)"

cd "$wt_protected"

assert_blocks "worktree: blocks commit on the protected branch of the repo it belongs to" \
  bash "$policy" pre-commit

cd "$wt_agent"

assert_allows "worktree: allows commit on an agent branch" \
  bash "$policy" pre-commit

printf '%s %s %s %s\n' \
  refs/heads/agent/signed "$wt_sha" refs/heads/agent/signed "$zero" \
  | assert_blocks "worktree: enforces the signing requirement of the repo it belongs to" \
    bash "$policy" pre-push origin "$near_origin"
if grep -q "must be GPG-signed" "$test_stderr"; then
  pass "worktree: the signing block names the requirement"
else
  fail "worktree: the signing block names the requirement" "stderr:" "$(cat "$test_stderr")"
fi

git -C "$wt_main" config core.hooksPath "$hooks_fixture"
printf 'edit\n' > "$wt_protected/file.txt"
git -C "$wt_protected" add file.txt

assert_blocks "worktree: git commit on the protected branch is refused through core.hooksPath" \
  git -C "$wt_protected" commit -m "must be refused"

printf 'edit\n' > "$wt_agent/file.txt"
git -C "$wt_agent" add file.txt

assert_allows "worktree: git commit on an agent branch succeeds through core.hooksPath" \
  git -C "$wt_agent" commit -m "allowed"

# --- Repo-local hook dispatch from a linked worktree ---
#
# .git is a file in a worktree, so this only works through the common git dir.

printf '%s\nprintf "repo-local-ran\n"\n' "$hook_shebang" > "$wt_main/.git/hooks/pre-commit"
chmod +x "$wt_main/.git/hooks/pre-commit"

cd "$wt_agent"

assert_allows "repo-local hook: dispatcher runs it from a linked worktree" \
  bash "$policy" pre-commit
if grep -q "repo-local-ran" "$test_stdout"; then
  pass "repo-local hook: its output reached the dispatcher's stdout"
else
  fail "repo-local hook: its output reached the dispatcher's stdout" \
    "expected 'repo-local-ran' in stdout, got:" "$(cat "$test_stdout")"
fi

cd "$wt_main"

assert_allows "repo-local hook: dispatcher still runs it from the main checkout" \
  bash "$policy" pre-commit
if grep -q "repo-local-ran" "$test_stdout"; then
  pass "repo-local hook: main-checkout output reached the dispatcher's stdout"
else
  fail "repo-local hook: main-checkout output reached the dispatcher's stdout" \
    "expected 'repo-local-ran' in stdout, got:" "$(cat "$test_stdout")"
fi

total=$(<"$counter_file")
printf '\nTests run: %d\n' "$total"
printf '✅ All %d protected-refs-policy tests passed.\n' "$total"
