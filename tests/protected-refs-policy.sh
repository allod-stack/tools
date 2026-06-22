#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
policy="$repo_root/protected-refs-policy"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
test_stdout="$tmp/stdout"
test_stderr="$tmp/stderr"

export HOME="$tmp/home"
mkdir -p "$HOME/.config/git" "$HOME/work/cdk/misc/git-hooks"
cat > "$HOME/.config/git/protected-branches" <<'FIXTURE'
work/cdk main
FIXTURE

git -C "$HOME/work/cdk" init --initial-branch=main >/dev/null 2>&1
git -C "$HOME/work/cdk" config user.name "Test User"
git -C "$HOME/work/cdk" config user.email "test@example.invalid"
git -C "$HOME/work/cdk" commit --allow-empty -m initial >/dev/null 2>&1

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

forge_url="ssh://git@forge.anarch.diy:2222/vnprc/repo.git"
zero="0000000000000000000000000000000000000000"

# --- Protected branch: pre-commit ---

cd "$HOME/work/cdk"

assert_blocks "pre-commit: blocks commit on protected branch" \
  bash "$policy" pre-commit

assert_allows "pre-commit: override allows commit on protected branch" \
  env ALLOW_PROTECTED_REF=1 bash "$policy" pre-commit

git checkout -b agent/test >/dev/null 2>&1
cat > misc/git-hooks/pre-commit <<'HOOK'
#!/usr/bin/env bash
printf ran > "$HOME/cdk-pre-commit-ran"
HOOK
chmod +x misc/git-hooks/pre-commit

assert_allows "pre-commit: allows commit on non-protected branch" \
  bash "$policy" pre-commit

if [ "$(cat "$HOME/cdk-pre-commit-ran")" = "ran" ]; then
  pass "pre-commit: runs repo-specific cdk hook"
else
  fail "pre-commit: runs repo-specific cdk hook" "hook marker file missing or wrong"
fi

# --- Protected branch: pre-rebase ---

git checkout main >/dev/null 2>&1

assert_blocks "pre-rebase: blocks rebase on protected branch" \
  bash "$policy" pre-rebase origin/main main

assert_allows "pre-rebase: override allows rebase on protected branch" \
  env ALLOW_PROTECTED_REF=1 bash "$policy" pre-rebase origin/main main

# --- Protected branch: pre-merge-commit ---

assert_blocks "pre-merge-commit: blocks merge into protected branch" \
  bash "$policy" pre-merge-commit

assert_allows "pre-merge-commit: override allows merge into protected branch" \
  env ALLOW_PROTECTED_REF=1 bash "$policy" pre-merge-commit

# --- Pre-push: external remote blocking ---

printf '%s %s %s %s\n' \
  refs/heads/agent/test "${zero}1" refs/heads/main "${zero}2" \
  | assert_blocks "pre-push: blocks push to unauthorized remote" \
    bash "$policy" pre-push origin ssh://example.invalid/repo.git

printf '%s %s %s %s\n' \
  refs/heads/agent/test "${zero}1" refs/heads/main "${zero}2" \
  | assert_allows "pre-push: override allows push to unauthorized remote" \
    env ALLOW_PROTECTED_REF=1 bash "$policy" pre-push origin ssh://example.invalid/repo.git

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
  "refs/heads/agent/feature" "$divergent_sha" "refs/heads/agent/feature" "$feature_sha" \
  | assert_allows "pre-push: override allows force-push to agent/* branch" \
    env ALLOW_PROTECTED_REF=1 bash "$policy" pre-push origin "$forge_url"

printf '%s %s %s %s\n' \
  "refs/heads/my-branch" "$divergent_sha" "refs/heads/my-branch" "$feature_sha" \
  | assert_allows "pre-push: allows force-push to non-agent branch" \
    bash "$policy" pre-push origin "$forge_url"

printf '%s %s %s %s\n' \
  "refs/heads/agent/new" "$divergent_sha" "refs/heads/agent/new" "$zero" \
  | assert_allows "pre-push: allows new branch push to agent/* (no remote history)" \
    bash "$policy" pre-push origin "$forge_url"

# --- Non-protected repo ---

mkdir -p "$HOME/work/hashpool"
git -C "$HOME/work/hashpool" init --initial-branch=main >/dev/null 2>&1
git -C "$HOME/work/hashpool" config user.name "Test User"
git -C "$HOME/work/hashpool" config user.email "test@example.invalid"
git -C "$HOME/work/hashpool" commit --allow-empty -m initial >/dev/null 2>&1
cd "$HOME/work/hashpool"

assert_allows "pre-commit: allows commit in non-protected repo" \
  bash "$policy" pre-commit

total=$(<"$counter_file")
printf '\nTests run: %d\n' "$total"
printf '✅ All %d protected-refs-policy tests passed.\n' "$total"
