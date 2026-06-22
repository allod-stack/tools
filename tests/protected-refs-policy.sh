#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
policy="$repo_root/git/protected-refs-policy"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
test_stdout="$tmp/stdout"
test_stderr="$tmp/stderr"

export HOME="$tmp/home"
mkdir -p "$HOME/.config/git" "$HOME/work/cdk/misc/git-hooks"
cp "$repo_root/git/protected-branches" "$HOME/.config/git/protected-branches"

git -C "$HOME/work/cdk" init --initial-branch=main >/dev/null
git -C "$HOME/work/cdk" config user.name "Test User"
git -C "$HOME/work/cdk" config user.email "test@example.invalid"
git -C "$HOME/work/cdk" commit --allow-empty -m initial >/dev/null

expect_fail() {
  if "$@" >"$test_stdout" 2>"$test_stderr"; then
    echo "Expected failure: $*" >&2
    exit 1
  fi
}

expect_pass() {
  if ! "$@" >"$test_stdout" 2>"$test_stderr"; then
    echo "Expected success: $*" >&2
    cat "$test_stderr" >&2
    exit 1
  fi
}

cd "$HOME/work/cdk"

expect_fail bash "$policy" pre-commit
expect_pass env ALLOW_PROTECTED_REF=1 bash "$policy" pre-commit

git checkout -b agent/test >/dev/null
cat > misc/git-hooks/pre-commit <<'HOOK'
#!/usr/bin/env bash
printf ran > "$HOME/cdk-pre-commit-ran"
HOOK
chmod +x misc/git-hooks/pre-commit

expect_pass bash "$policy" pre-commit
test "$(cat "$HOME/cdk-pre-commit-ran")" = "ran"

git checkout main >/dev/null
expect_fail bash "$policy" pre-rebase origin/main main
expect_pass env ALLOW_PROTECTED_REF=1 bash "$policy" pre-rebase origin/main main

expect_fail bash "$policy" pre-merge-commit
expect_pass env ALLOW_PROTECTED_REF=1 bash "$policy" pre-merge-commit

printf '%s %s %s %s\n' \
  refs/heads/agent/test 0000000000000000000000000000000000000001 refs/heads/main 0000000000000000000000000000000000000002 \
  | expect_fail bash "$policy" pre-push origin ssh://example.invalid/repo.git

printf '%s %s %s %s\n' \
  refs/heads/agent/test 0000000000000000000000000000000000000001 refs/heads/main 0000000000000000000000000000000000000002 \
  | expect_pass env ALLOW_PROTECTED_REF=1 bash "$policy" pre-push origin ssh://example.invalid/repo.git

mkdir -p "$HOME/work/hashpool"
git -C "$HOME/work/hashpool" init --initial-branch=main >/dev/null
git -C "$HOME/work/hashpool" config user.name "Test User"
git -C "$HOME/work/hashpool" config user.email "test@example.invalid"
git -C "$HOME/work/hashpool" commit --allow-empty -m initial >/dev/null
cd "$HOME/work/hashpool"

expect_pass bash "$policy" pre-commit

printf 'protected refs policy tests passed\n'
