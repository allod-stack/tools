#!/usr/bin/env bash
# Regression test for allod/tools#143: a repo whose flake declares foreign
# nixConfig (extra-substituters) must ride through the cascade without
# prompting. With stderr on a TTY nix asks "do you want to allow configuration
# setting 'extra-substituters' ..." and blocks reading stdin; the cascade must
# decline and complete instead of wedging.
#
# Unlike the sibling suites this test runs the real nix against a fixture
# flake, under a pty (util-linux `script`) so the prompt machinery is live.
# Arm 1 proves the fixture still provokes the prompt on this nix — when a
# future nix stops prompting, arm 1 fails and this whole test can be deleted.
# Arm 2 is the regression pin: the cascade completes, declines the foreign
# config, and leaves the repository untouched.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

fail() {
  echo "FAIL: $1" >&2
  shift
  for extra in "$@"; do
    echo "$extra" >&2
  done
  exit 1
}

for tool in nix script git; do
  command -v "$tool" >/dev/null || fail "required tool missing: $tool"
done
REAL_GIT=$(command -v git)

export HOME="$TMP/home"
export MOCK_LOG="$TMP/commands.log"
REPO="$HOME/work/app"
mkdir -p "$HOME/work/.git" "$REPO" "$HOME/.config/git" "$TMP/bin" "$TMP/dep"
: > "$HOME/.config/git/active-pr-branches"
: > "$HOME/.config/git/protected-branches"
: > "$MOCK_LOG"

# --- Fixture: a dependency flake plus a real git repo that consumes it and
# declares an untrusted substituter in its nixConfig. Real git only for setup;
# nix's git fetcher needs a valid repository with the flake files tracked.
cat > "$TMP/dep/flake.nix" <<'EOF'
{
  outputs = { self }: { };
}
EOF

cat > "$REPO/flake.nix" <<EOF
{
  nixConfig.extra-substituters = [ "https://cache.example.invalid" ];
  inputs.dep.url = "path:$TMP/dep";
  outputs = { self, dep }: { };
}
EOF

git_fixture() {
  "$REAL_GIT" -C "$REPO" -c init.defaultBranch=master \
    -c user.name=fixture -c user.email=fixture@example.invalid "$@"
}
git_fixture init -q
git_fixture add flake.nix
git_fixture commit -q -m "fixture flake"
nix flake lock "$REPO" </dev/null 2>"$TMP/setup.log" \
  || fail "fixture lock generation failed" "$(cat "$TMP/setup.log")"
git_fixture add flake.lock
git_fixture commit -q -m "fixture lock"
echo change > "$TMP/dep/extra"

# --- Mock git for the cascade's own invocations (repo discovery, pre-flight,
# pull). Setup above already used the real git; nix fetches via libgit2 and
# never consults PATH.
cat > "$TMP/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[[ "$1" == "-C" ]] || { echo "unexpected git invocation: $*" >&2; exit 1; }
dir="$2"
shift 2
printf 'git\t%s\n' "$*" >> "$MOCK_LOG"

case "$*" in
  "rev-parse --show-toplevel")
    [[ -f "$dir/.git/HEAD" ]] || exit 1
    printf '%s\n' "$dir"
    ;;
  "symbolic-ref refs/remotes/origin/HEAD")
    printf 'refs/remotes/origin/master\n'
    ;;
  "branch --show-current")
    printf 'master\n'
    ;;
  "diff --quiet"|"diff --cached --quiet"|"pull")
    exit 0
    ;;
  "rev-parse @{u}")
    printf 'origin/master\n'
    ;;
  "rev-list HEAD...@{u} --count")
    printf '0\n'
    ;;
  *)
    echo "unexpected git invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$TMP/bin/git"
export PATH="$TMP/bin:$PATH"

# A fifo opened read-write keeps the pty's stdin open without ever sending a
# byte, so a prompting nix genuinely blocks instead of reading EOF.
mkfifo "$TMP/held-stdin"
exec 3<>"$TMP/held-stdin"

# --- Arm 1: the fixture must provoke the prompt on this nix. A raw update
# (stderr on the pty, stdin held open, no /dev/null) has to wedge until the
# bound kills it. If this arm fails, this nix no longer prompts and the test
# has lost its witness — delete it rather than patching around this check.
arm1_cmd="nix flake update dep --flake $(printf '%q' "$REPO")"
arm1_cmd+=" --output-lock-file $(printf '%q' "$TMP/sabotage.lock")"
set +e
timeout 15 script -qec "$arm1_cmd" /dev/null <&3 >"$TMP/arm1.out" 2>&1
arm1_rc=$?
set -e
[[ "$arm1_rc" == 124 ]] \
  || fail "expected the raw update to wedge at the prompt (exit 124), got $arm1_rc" \
    "$(cat "$TMP/arm1.out")"
grep -Fq "do you want to allow configuration setting" "$TMP/arm1.out" \
  || fail "raw update wedged without showing the nixConfig prompt" \
    "$(cat "$TMP/arm1.out")"

# --- Arm 2: the cascade over the same repo, same pty conditions, must
# complete promptly, decline the foreign config, and leave the repo unchanged.
arm2_cmd="bash $(printf '%q' "$ROOT/flake/flake-update-cascade") dep --dry-run"
before=$(sha256sum "$REPO/flake.lock")
set +e
timeout 60 script -qec "$arm2_cmd" /dev/null <&3 >"$TMP/arm2.out" 2>&1
arm2_rc=$?
set -e
exec 3<&-
after=$(sha256sum "$REPO/flake.lock")

[[ "$arm2_rc" == 0 ]] \
  || fail "cascade did not complete (exit $arm2_rc; 124 means it wedged at the prompt)" \
    "$(cat "$TMP/arm2.out")"
grep -Fq "==> app" "$TMP/arm2.out" \
  || fail "cascade did not process the fixture repo" "$(cat "$TMP/arm2.out")"
grep -Fq "already up to date" "$TMP/arm2.out" \
  || fail "cascade did not finish the dry-run update" "$(cat "$TMP/arm2.out")"
grep -Fq "ignoring untrusted flake configuration setting" "$TMP/arm2.out" \
  || fail "cascade did not decline the foreign nixConfig" "$(cat "$TMP/arm2.out")"
[[ "$before" == "$after" ]] || fail "dry-run changed flake.lock"

echo "flake-update-cascade nixConfig non-interactivity tests passed"
