#!/usr/bin/env bash
# 'allod change run''s sandbox mount set, against a mock 'systemd-run' execing real bwrap. Run
# from the devShell (nix develop -c bash ...), which builds a test-only binary; see docs/allod-change-run.md.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ALLOD="${ALLOD_UNDER_TEST:-$(command -v allod)}"
TMP=$(mktemp -d)
PRODUCTION_ALLOD="$ALLOD"
# The mock-only binary preserves the production query logic while letting
# each test supply a different Nix answer through PATH.
go build -C "$ROOT" -ldflags '-X main.nixExecutablePath=nix' -o "$TMP/allod-mock-nix" ./cmd/allod
ALLOD="$TMP/allod-mock-nix"
RUN_ID="r$$"
CAPTURE_OUTPUT=""
CAPTURE_STATUS=0
test_number=0
repo_number=0
declare -a CLONES=()

cleanup() {
  local path
  for path in "${CLONES[@]}"; do
    rm -rf "$path"
  done
  rm -rf "$TMP"
}
trap cleanup EXIT

export HOME="$TMP/home"
export XDG_CONFIG_HOME="$HOME/.config"
export INVENTORY="$TMP/inventory"
mkdir -p "$HOME/.config/git" "$HOME/work" "$TMP/remotes" "$INVENTORY/scripts"
touch "$HOME/.config/git/protected-branches"
touch "$HOME/.config/git/protected-refs-policy"
git config --global user.name "Test User"
git config --global user.email "test@example.invalid"

# The one credential 'run' must never let a sandboxed command see, as a symlink resolving
# outside both the config tree and the Nix store: the agenix shape a real VM uses.
forgejo_token_target="$TMP/agenix-forgejo-token"
printf 'super-secret-token\n' > "$forgejo_token_target"
ln -s "$forgejo_token_target" "$HOME/.config/git/forgejo-token"
# A second credential under an arbitrary name ('forgejo-verifier-token') that 'forge' honors via
# FORGE_TOKEN_FILE; only the allowlist of what may be bound keeps it out, regardless of content.
printf 'second-secret-verifier-token\n' > "$HOME/.config/git/forgejo-verifier-token"
# A bind keyed on the basename alone would still bind a hard link to it.
ln "$HOME/.config/git/forgejo-token" "$HOME/.config/git/forgejo-token-alias"
# An exclusion checked only at the top level would still bind a copy one
# directory down.
mkdir -p "$HOME/.config/git/hooks"
ln -s "$(readlink -f "$(command -v bash)")" "$HOME/.config/git/hooks/pre-commit"
ln "$HOME/.config/git/forgejo-token" "$HOME/.config/git/hooks/forgejo-token-in-hooks"
# A byte copy, not a hard link, so os.SameFile never matches it, but still the real secret under
# a name of the worker's choosing.
cp "$HOME/.config/git/forgejo-token" "$HOME/.config/git/hooks/forgejo-token-copy"
# A symlink resolving to the token, not just a hard link.
ln -s "../forgejo-token" "$HOME/.config/git/hooks/evil"
# A symlink from 'hooks/' (walked) to the second credential (not on the allowlist, never walked
# on its own): the target's own name must itself be allowlisted, not just resolve inside the tree.
ln -s "../forgejo-verifier-token" "$HOME/.config/git/hooks/evil-to-second-credential"
# A symlink pointing entirely outside ~/.config/git: binding it would
# expose whatever it resolves to, wherever that is.
ln -s "$HOME/.ssh" "$HOME/.config/git/sshlink"
printf '#!/bin/sh\necho ordinary hook\n' > "$HOME/.config/git/hooks/ordinary-hook"
chmod +x "$HOME/.config/git/hooks/ordinary-hook"
printf 'third-secret\n' > "$HOME/.config/git/hooks/second-credential"
ln "$HOME/.config/git/forgejo-verifier-token" "$HOME/.config/git/hooks/second-credential-alias"
# A symlink into the Nix store, the home-manager pattern every dotfile here uses on a real VM,
# named 'forge-hosts' (an allowlisted name) so the store carve-out still binds it.
store_target=$(command -v bash)
ln -s "$store_target" "$HOME/.config/git/forge-hosts"
# A symlink to a directory, resolving inside the tree itself, would bind the whole tree a second
# time under its own name, with no per-file exclusion applied to that second copy.
ln -s "." "$HOME/.config/git/alias"
# The VM's own forge SSH identity: 'run' binds these by name, read-only,
# never ~/.ssh as a whole.
mkdir -p "$HOME/.ssh"
# /etc/ssh/ssh_config is root-owned on the real host. The sandbox's user namespace maps that
# owner to nobody, so this Include also exercises the private recursive copy rather than only
# the private top-level ~/.ssh/config copy.
printf 'Include /etc/ssh/ssh_config\nHost *\n  IdentityFile ~/.ssh/id_ed25519\n' > "$HOME/.ssh/config"
printf 'forge.example ssh-ed25519 AAAAexamplehostkey\n' > "$HOME/.ssh/known_hosts"
printf 'fake-private-key\n' > "$HOME/.ssh/id_ed25519"
printf 'fake-public-key\n' > "$HOME/.ssh/id_ed25519.pub"
# Not named by ~/.ssh/config at all: every named file being bound must not
# make this one reachable too.
printf 'fake-other-key\n' > "$HOME/.ssh/id_other"

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

assert_status() {
  local expected="$1" description="$2"
  if [[ "$CAPTURE_STATUS" -eq "$expected" ]]; then
    pass "$description"
  else
    fail "$description" "expected status: $expected" "actual status: $CAPTURE_STATUS" \
      "output:" "$CAPTURE_OUTPUT"
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
  local repo="$1" branch="${2:-master}" remote
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

begin_clone() {
  local desc="$1" repo="$2" path
  path=$("$ALLOD" change begin -d "$desc" "$repo")
  CLONES+=("$path")
  printf '%s\n' "$path"
}

# A git wrapper that blocks one path outright, standing in for what the real environment refuses
# by never mounting the credential at all.
make_restricted_path_without_bwrap() {
  local bin="$TMP/no-bwrap-bin"
  local tool target
  mkdir -p "$bin"
  for tool in bash git dirname mktemp sed grep cat rm basename awk env; do
    target=$(command -v "$tool")
    ln -sf "$target" "$bin/$tool"
  done
  # requireUntrustedNixUser and the netrc preflight both fail closed when nix cannot be asked,
  # so this restricted PATH still needs a working nix answering both queries harmlessly.
  ln -sf "$(make_mock_nix_netrc root "")/nix" "$bin/nix"
  printf '%s\n' "$bin"
}

# Strips the scope wrapper and execs the real bwrap command handed to it, so the mount set is
# exercised for real; the scope/memory cap (needing a live session bus) go to the by-hand script.
make_mock_systemd_run() {
  local bin="$TMP/mock-systemd-run-bin"
  mkdir -p "$bin"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'EOF'
set -euo pipefail
printf '%s\n' "$*" >> "$MOCK_SYSTEMD_RUN_LOG"
args=("$@")
for i in "${!args[@]}"; do
  if [[ "${args[$i]}" == "--unit" ]]; then
    unit="${args[$((i + 1))]}"
    # The real systemd-run refuses a unit name outside systemd's legal charset; the fake one used
    # to accept anything, which is how '+' in a multi-clone scope name went unnoticed.
    if [[ ! "$unit" =~ ^[A-Za-z0-9:_.-]+$ ]]; then
      printf 'mock systemd-run: illegal unit name: %s\n' "$unit" >&2
      exit 1
    fi
  fi
  if [[ "${args[$i]}" == "--" ]]; then
    exec "${args[@]:$((i + 1))}"
  fi
done
echo "mock systemd-run: no -- found" >&2
exit 1
EOF
  } > "$bin/systemd-run"
  chmod +x "$bin/systemd-run"
  printf '%s\n' "$bin"
}

# A fake 'nix config show trusted-users' naming trusted, so the suite can run 'change run'
# without the real nix daemon's own list deciding whether these tests can even start.
make_mock_nix() {
  local trusted="$1"
  local bin="$TMP/mock-nix-bin-$trusted"
  mkdir -p "$bin"
  {
    printf '#!%s\n' "$(command -v bash)"
    printf 'TRUSTED=%q\n' "$trusted"
    cat <<'EOF'
if [[ "$1 $2 $3" == "config show trusted-users" ]]; then
  printf '%s\n' "$TRUSTED"
  exit 0
fi
echo "mock nix: unexpected args: $*" >&2
exit 1
EOF
  } > "$bin/nix"
  chmod +x "$bin/nix"
  printf '%s\n' "$bin"
}

# Also logs NIX_CONFIG and NIX_USER_CONF_FILES exactly as received, so a test can check
# requireUntrustedNixUser's own environment scrubbing without a real nix.
make_mock_nix_env_probe() {
  local trusted="$1" log="$2"
  local bin="$TMP/mock-nix-env-probe-bin-$test_number"
  mkdir -p "$bin"
  {
    printf '#!%s\n' "$(command -v bash)"
    printf 'TRUSTED=%q\n' "$trusted"
    printf 'PROBE_LOG=%q\n' "$log"
    cat <<'EOF'
if [[ "$1 $2 $3" == "config show trusted-users" ]]; then
  printf 'NIX_CONFIG=%s\n' "${NIX_CONFIG-<unset>}" >> "$PROBE_LOG"
  printf 'NIX_USER_CONF_FILES=%s\n' "${NIX_USER_CONF_FILES-<unset>}" >> "$PROBE_LOG"
  printf 'NIX_CONF_DIR=%s\n' "${NIX_CONF_DIR-<unset>}" >> "$PROBE_LOG"
  printf '%s\n' "$TRUSTED"
  exit 0
fi
echo "mock nix: unexpected args: $*" >&2
exit 1
EOF
  } > "$bin/nix"
  chmod +x "$bin/nix"
  printf '%s\n' "$bin"
}

# A fake 'nix' answering both trusted-users and netrc-file, so the netrc refusal can be
# exercised without the real daemon's own setting deciding whether the test starts.
make_mock_nix_netrc() {
  local trusted="$1"
  local netrc="$2"
  local bin="$TMP/mock-nix-netrc-bin-$test_number"
  mkdir -p "$bin"
  {
    printf '#!%s\n' "$(command -v bash)"
    printf 'TRUSTED=%q\n' "$trusted"
    printf 'NETRC=%q\n' "$netrc"
    cat <<'EOF'
if [[ "$1 $2 $3" == "config show trusted-users" ]]; then
  printf '%s\n' "$TRUSTED"
  exit 0
fi
if [[ "$1 $2 $3" == "config show netrc-file" ]]; then
  printf '%s\n' "$NETRC"
  exit 0
fi
echo "mock nix: unexpected args: $*" >&2
exit 1
EOF
  } > "$bin/nix"
  chmod +x "$bin/nix"
  printf '%s\n' "$bin"
}

# 'cleanup' asks the forge for the pull request on the branch it is about to remove; this
# answers every branch merged, the same shape tests/allod-change.sh's make_mock_forge uses.
make_mock_forge() {
  local bin="$TMP/mock-forge-bin"
  mkdir -p "$bin"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'EOF'
set -euo pipefail
printf '%s\n' "$*" >> "$MOCK_FORGE_LOG"
if [[ "$1 $2" == "pr find-by-head" ]]; then
  shift 2
  state_all=false
  for arg in "$@"; do
    [[ "$arg" == "all" ]] && state_all=true
  done
  if $state_all; then
    printf '1\tmerged\n'
  fi
  exit 0
fi
echo "mock forge: unexpected args: $*" >&2
exit 1
EOF
  } > "$bin/forge"
  chmod +x "$bin/forge"
  printf '%s\n' "$bin"
}

MOCK_BIN=$(make_mock_systemd_run)
export MOCK_SYSTEMD_RUN_LOG="$TMP/systemd-run.log"
touch "$MOCK_SYSTEMD_RUN_LOG"
NIX_UNTRUSTED_BIN=$(make_mock_nix_netrc root "")
FORGE_BIN=$(make_mock_forge)
export MOCK_FORGE_LOG="$TMP/forge.log"
touch "$MOCK_FORGE_LOG"
export PATH="$MOCK_BIN:$NIX_UNTRUSTED_BIN:$FORGE_BIN:$PATH"
run_sandboxed() {
  capture "$ALLOD" change run "$@"
}

# Logs every invocation's argv, order included, before forwarding to the real git.
make_logging_git() {
  local log="$1"
  local bin="$TMP/logging-git-bin"
  mkdir -p "$bin"
  {
    printf '#!%s\n' "$(command -v bash)"
    printf 'REAL_GIT=%q\n' "$(command -v git)"
    printf 'LOG=%q\n' "$log"
    cat <<'EOF'
printf '%s\n' "$*" >> "$LOG"
exec "$REAL_GIT" "$@"
EOF
  } > "$bin/git"
  chmod +x "$bin/git"
  printf '%s\n' "$bin"
}

probe_script='
for p in "$@"; do
  if [ -e "$p" ] || [ -L "$p" ]; then printf "PRESENT %s\n" "$p"; else printf "ABSENT %s\n" "$p"; fi
done
'

# --- Fixtures shared by the mount-set probes ---

repo="$HOME/work/sandboxed-repo"
init_repo "$repo" master
desc="${RUN_ID}-sandboxed"
clone=$(begin_clone "$desc" "$repo")

other_repo="$HOME/work/sibling-repo"
init_repo "$other_repo" master
sibling_desc="${RUN_ID}-sibling"
sibling_clone=$(begin_clone "$sibling_desc" "$other_repo")

printf '{\n  "repositories": {\n    "fixture/memory-repo": { "checkout": "memory-repo", "memory": true }\n  }\n}\n' \
  > "$INVENTORY/scripts/repositories.json"
mkdir -p "$HOME/work/memory-repo"
printf 'memory fixture\n' > "$HOME/work/memory-repo/note.md"
git -C "$HOME/work/memory-repo" init -q -b master
git -C "$HOME/work/memory-repo" add note.md
git -C "$HOME/work/memory-repo" commit -qm "memory fixture"
# A memory checkout's own ledger: must never be visible inside the sandbox.
mkdir -p "$HOME/work/memory-repo/.git/allod-changes"
printf 'fake-ledger-entry\n' > "$HOME/work/memory-repo/.git/allod-changes/fake-ledger"

mkdir -p "$HOME/.claude/projects"
printf 'session\n' > "$HOME/.claude/marker"
printf '{"hooks":"fake-overseer-hook-config"}\n' > "$HOME/.claude/settings.json"
printf '{"fake":"onboarding-state"}\n' > "$HOME/.claude.json"

# --- The sabotage half: every fixture the probe must find absent inside the
# sandbox genuinely exists outside it, so an absence inside is not vacuous. ---

[[ -e "$HOME/.config/git/forgejo-token" ]] && pass "sabotage: forgejo-token exists outside the sandbox" ||
  fail "sabotage: forgejo-token exists outside the sandbox"
[[ -e "$HOME/.config/git/forgejo-verifier-token" ]] && pass "sabotage: the second credential file exists outside the sandbox" ||
  fail "sabotage: the second credential file exists outside the sandbox"
[[ -e "$HOME/.config/git/forgejo-token-alias" ]] && pass "sabotage: the forgejo-token hard link exists outside the sandbox" ||
  fail "sabotage: the forgejo-token hard link exists outside the sandbox"
[[ -e "$HOME/.config/git/hooks/evil" ]] && pass "sabotage: hooks/evil resolves to something outside the sandbox" ||
  fail "sabotage: hooks/evil resolves to something outside the sandbox"
[[ -e "$HOME/.config/git/hooks/evil-to-second-credential" ]] && pass "sabotage: hooks/evil-to-second-credential resolves to the second credential outside the sandbox" ||
  fail "sabotage: hooks/evil-to-second-credential resolves to the second credential outside the sandbox"
[[ -e "$HOME/.config/git/sshlink/id_ed25519" ]] && pass "sabotage: sshlink resolves to ~/.ssh outside the sandbox" ||
  fail "sabotage: sshlink resolves to ~/.ssh outside the sandbox"
[[ -e "$HOME/.config/git/hooks/forgejo-token-in-hooks" ]] && pass "sabotage: the forgejo-token hard link under hooks/ exists outside the sandbox" ||
  fail "sabotage: the forgejo-token hard link under hooks/ exists outside the sandbox"
[[ -e "$HOME/.config/git/hooks/forgejo-token-copy" ]] && pass "sabotage: the forgejo-token byte copy under hooks/ exists outside the sandbox" ||
  fail "sabotage: the forgejo-token byte copy under hooks/ exists outside the sandbox"
[[ -e "$HOME/.config/git/hooks/second-credential" ]] && pass "sabotage: a distinct credential exists under hooks" ||
  fail "sabotage: a distinct credential exists under hooks"
[[ -e "$HOME/.config/git/alias/forgejo-token" ]] && pass "sabotage: alias/forgejo-token resolves to the real token outside the sandbox" ||
  fail "sabotage: alias/forgejo-token resolves to the real token outside the sandbox"
[[ -e "$HOME/.ssh/id_other" ]] && pass "sabotage: the unnamed identity file exists outside the sandbox" ||
  fail "sabotage: the unnamed identity file exists outside the sandbox"
[[ -e "$sibling_clone" ]] && pass "sabotage: the sibling clone exists outside the sandbox" ||
  fail "sabotage: the sibling clone exists outside the sandbox"
[[ -e "$repo" ]] && pass "sabotage: the shared checkout exists outside the sandbox" ||
  fail "sabotage: the shared checkout exists outside the sandbox"

# --- Mount set: absences ---

run_sandboxed "$clone" -- bash -c "$probe_script" bash \
  "$HOME/.config/git/forgejo-token-alias" \
  "$HOME/.config/git/forgejo-verifier-token" \
  "$HOME/.config/git/hooks/forgejo-token-in-hooks" "$HOME/.config/git/hooks/forgejo-token-copy" \
  "$HOME/.config/git/hooks/second-credential" "$HOME/.config/git/hooks/second-credential-alias" \
  "$HOME/.config/git/hooks/evil" "$HOME/.config/git/hooks/evil-to-second-credential" \
  "$HOME/.config/git/sshlink" "$HOME/.config/git/sshlink/id_ed25519" \
  "$HOME/.config/git/hooks/ordinary-hook" "$HOME/.config/git/forge-hosts" \
  "$HOME/.config/git/alias" "$HOME/.config/git/alias/forgejo-token" \
  "$HOME/.ssh/id_other" "$sibling_clone" "$repo"
assert_status 0 "the probe run itself succeeds"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/forgejo-verifier-token" \
  "a second credential under an arbitrary name is absent inside the sandbox too, the allowlist excludes anything not explicitly named"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/forgejo-token-alias" \
  "a hard link to forgejo-token is absent inside the sandbox too"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/hooks/forgejo-token-in-hooks" \
  "a hard link to forgejo-token one directory down is absent inside the sandbox too"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/hooks/forgejo-token-copy" \
  "a byte copy of forgejo-token (a new inode, not caught by os.SameFile) is absent inside the sandbox too"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/hooks/second-credential" \
  "a distinct credential under hooks is absent inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/hooks/second-credential-alias" \
  "a hard link to the distinct credential is absent inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/hooks/evil" \
  "a symlink resolving to forgejo-token (hooks/evil -> ../forgejo-token) is absent inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/hooks/evil-to-second-credential" \
  "a symlink from inside hooks/ to a second, non-allowlisted credential elsewhere under ~/.config/git is absent too: resolving inside the tree is not enough, the target's own name must itself be allowlisted"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/sshlink" \
  "a top-level symlink pointing outside ~/.config/git (sshlink -> ~/.ssh) is absent inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/sshlink/id_ed25519" \
  "nothing reachable through that symlink is bound either"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/hooks/ordinary-hook" \
  "an unmanaged hook file is absent inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $HOME/.config/git/forge-hosts" \
  "a symlink resolving into the Nix store (the home-manager pattern) is present inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/alias" \
  "a symlink to a directory (alias -> .) is absent inside the sandbox, never bound as if it were a single file"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.config/git/alias/forgejo-token" \
  "nothing reachable through that directory symlink is bound either, forgejo-token included"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/.ssh/id_other" \
  "an identity file ~/.ssh/config does not name is absent inside the sandbox, even though every file it does name is bound"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $sibling_clone" "a sibling clone is absent inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $repo" "the shared checkout is absent inside the sandbox"

run_sandboxed "$clone" -- bash -c "$probe_script" bash \
  "$HOME/work/memory-repo/.git/allod-changes/fake-ledger" "$HOME/work/memory-repo/.git/HEAD"
assert_status 0 "the memory-ledger probe run succeeds"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/work/memory-repo/.git/allod-changes/fake-ledger" \
  "a memory checkout's own ledger is absent inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "ABSENT $HOME/work/memory-repo/.git/HEAD" \
  "a memory checkout's .git is an empty tmpfs inside the sandbox, not the real one"

# --- Mount set: presences ---

run_sandboxed "$clone" -- bash -c "$probe_script" bash \
  "$clone" "$HOME/.gitconfig" "$HOME/work/memory-repo/note.md" "$HOME/.claude/marker" "$HOME/.claude.json" \
  "$HOME/.config/git/forgejo-token" \
  "$HOME/.ssh/config" "$HOME/.ssh/known_hosts" "$HOME/.ssh/id_ed25519" "$HOME/.ssh/id_ed25519.pub"
assert_status 0 "the presence probe run succeeds"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $clone" "the named clone is present inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $HOME/.gitconfig" "~/.gitconfig is present inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $HOME/work/memory-repo/note.md" \
  "a registry repository flagged memory:true is present inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $HOME/.claude/marker" "~/.claude is present inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $HOME/.claude.json" "~/.claude.json is present inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $HOME/.config/git/forgejo-token" \
  "forgejo-token is present inside the sandbox: the worker's own forge credential"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $HOME/.ssh/config" "~/.ssh/config is present inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $HOME/.ssh/known_hosts" "~/.ssh/known_hosts is present inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $HOME/.ssh/id_ed25519" \
  "the identity file ~/.ssh/config names is present inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $HOME/.ssh/id_ed25519.pub" \
  "its .pub sibling is bound alongside it"

run_sandboxed "$clone" -- ssh -G forge.example
assert_status 0 "ssh accepts the private user and system config include chains inside the user namespace"

run_sandboxed "$clone" -- cat "$HOME/.config/git/forgejo-token"
assert_status 0 \
  "forgejo-token is readable inside the sandbox even though it is a symlink resolving outside ~/.config/git and the Nix store"
assert_equal "$CAPTURE_OUTPUT" "$(cat "$forgejo_token_target")" \
  "its content equals the symlink's target outside"

# --- ~/.ssh/config as a symlink into the Nix store: ssh refuses a config owned by anyone but
# the invoking user, so a private, invoking-user-owned copy is what must be bound. ---

ssh_config_regular="$TMP/ssh-config-regular.saved"
mv "$HOME/.ssh/config" "$ssh_config_regular"
ln -s "$store_target" "$HOME/.ssh/config"
run_sandboxed "$clone" -- stat -c '%u %a' "$HOME/.ssh/config"
assert_status 0 "the ~/.ssh/config stat run succeeds"
assert_equal "$CAPTURE_OUTPUT" "$(id -u) 600" \
  "~/.ssh/config inside the sandbox is owned by the invoking user with mode 600, not the Nix store's root ownership"
run_sandboxed "$clone" -- sha256sum "$HOME/.ssh/config"
assert_status 0 "the ~/.ssh/config checksum run succeeds"
assert_contains "$CAPTURE_OUTPUT" "$(sha256sum "$store_target" | cut -d' ' -f1)" \
  "its content equals the symlink's target outside"
rm -f "$HOME/.ssh/config"
mv "$ssh_config_regular" "$HOME/.ssh/config"

run_sandboxed "$clone" -- bash -c 'touch "$1/should-fail" 2>/dev/null; echo "write-status:$?"' bash "$HOME/work/memory-repo"
assert_status 0 "the write-attempt run succeeds"
assert_contains "$CAPTURE_OUTPUT" "write-status:1" "a registry repository flagged memory:true is read-only inside the sandbox"

run_sandboxed "$clone" -- bash -c 'printf "tampered\n" > "$1" 2>/dev/null; echo "write-status:$?"' bash "$HOME/.claude/settings.json"
assert_status 0 "the ~/.claude/settings.json write-attempt run succeeds"
assert_contains "$CAPTURE_OUTPUT" "write-status:1" \
  "~/.claude is bound read-only as a whole: a worker cannot edit the overseer's own hook config"

run_sandboxed "$clone" -- bash -c 'touch "$1/sandbox-write" && echo wrote' bash "$HOME/.claude/projects"
assert_status 0 "the ~/.claude/projects write run succeeds"
assert_contains "$CAPTURE_OUTPUT" "wrote" "~/.claude/projects is read-write inside the sandbox"

# A hard link in a writable runner subdirectory shares its inode with the read-only file it
# names, so writing through the mount would write that file too. Refused before 'run' binds.
ln "$HOME/.claude/settings.json" "$HOME/.claude/projects/alias"
run_sandboxed "$clone" -- true
assert_status 14 "run refuses when a writable runner subdirectory holds a hard link to another file"
assert_contains "$CAPTURE_OUTPUT" "hard link" "the refusal names the hard link"
rm -f "$HOME/.claude/projects/alias"

# The hard-link walk covers the whole runner directory, not only writable subdirectories: a
# hard link directly under ~/.claude still aliases whatever it names.
ln "$forgejo_token_target" "$HOME/.claude/token-alias"
run_sandboxed "$clone" -- true
assert_status 14 "run refuses when a hard link sits directly under the runner's own base directory"
assert_contains "$CAPTURE_OUTPUT" "hard link" "the refusal names the hard link"
rm -f "$HOME/.claude/token-alias"

# A symlink resolving back into the runner's own base directory is the
# same risk as the hard link above, a second line of defense.
ln -s "../settings.json" "$HOME/.claude/projects/settings-alias"
run_sandboxed "$clone" -- true
assert_status 14 "run refuses when a writable runner subdirectory holds a symlink resolving back into the runner's own base"
assert_contains "$CAPTURE_OUTPUT" "resolving back inside" "the refusal names why"
rm -f "$HOME/.claude/projects/settings-alias"

# A symlink resolving entirely outside the runner's base is not refused: Claude Code itself
# plants exactly this shape and it must keep working.
memory_symlink_target="$HOME/work/some-project-memory"
mkdir -p "$memory_symlink_target"
ln -s "$memory_symlink_target" "$HOME/.claude/projects/memory-elsewhere"
run_sandboxed "$clone" -- true
assert_status 0 "run proceeds when a writable runner subdirectory holds a symlink resolving entirely outside the runner's own base"
rm -f "$HOME/.claude/projects/memory-elsewhere"

claude_json_before=$(cat "$HOME/.claude.json")
run_sandboxed "$clone" -- bash -c 'printf "rewritten\n" > "$1" && echo wrote' bash "$HOME/.claude.json"
assert_status 0 "the ~/.claude.json write run succeeds"
assert_contains "$CAPTURE_OUTPUT" "wrote" "~/.claude.json is read-write inside the sandbox"
assert_equal "$(cat "$HOME/.claude.json")" "$claude_json_before" \
  "the real ~/.claude.json is unchanged after the run: the sandbox wrote a private copy"

# /etc is bound whole; root-only files under it are protected by their own mode bits, not by
# anything this command does.
if [[ -e /etc/ssh/ssh_host_ed25519_key ]]; then
  run_sandboxed "$clone" -- bash -c 'cat "$1" >/dev/null 2>&1; echo "read-status:$?"' bash /etc/ssh/ssh_host_ed25519_key
  assert_status 0 "the ssh host key read-attempt run succeeds"
  assert_contains "$CAPTURE_OUTPUT" "read-status:1" \
    "/etc/ssh/ssh_host_ed25519_key stays unreadable inside the sandbox, protected by its own mode bits"
else
  pass "/etc/ssh/ssh_host_ed25519_key is not present on this VM; the mode-bits probe has nothing to check here"
fi

# --- Environment: cleared except an allowlist ---

export SECRET_PROBE=leak
# ALLOD_CHANGE_SANDBOX is never on the pass-through allowlist, so planting a
# different value for it outside must not reach the sandbox either: 'run'
# always sets it to '1' itself, regardless of what the launching shell held.
export ALLOD_CHANGE_SANDBOX=not-set-by-run
run_sandboxed "$clone" -- bash -c '
  [ -z "${SECRET_PROBE:-}" ] && echo SECRET_ABSENT || echo "SECRET_PRESENT:$SECRET_PROBE"
  [ -n "${PATH:-}" ] && echo PATH_PRESENT || echo PATH_ABSENT
  [ "${HOME:-}" = "'"$HOME"'" ] && echo HOME_PRESENT || echo "HOME_WRONG:${HOME:-}"
  echo "SANDBOX_MARKER:${ALLOD_CHANGE_SANDBOX:-<unset>}"
'
assert_status 0 "the environment probe run succeeds"
assert_contains "$CAPTURE_OUTPUT" "SECRET_ABSENT" \
  "a variable outside the allowlist (planted in the launching shell) does not reach the sandbox"
assert_contains "$CAPTURE_OUTPUT" "PATH_PRESENT" "PATH is present inside the sandbox"
assert_contains "$CAPTURE_OUTPUT" "HOME_PRESENT" "HOME is present inside the sandbox, unchanged"
assert_contains "$CAPTURE_OUTPUT" "SANDBOX_MARKER:1" \
  "ALLOD_CHANGE_SANDBOX is 1 inside the sandbox, not the different value the launching shell held: only 'run' itself sets it"
unset SECRET_PROBE ALLOD_CHANGE_SANDBOX

# --- git commit inside the clone; the parent is untouched ---

git_dir=$(git -C "$repo" rev-parse --path-format=absolute --git-dir)
refs_before=$(find "$git_dir/refs" -type f -exec sha256sum {} \; | sort)
packed_before=""
[[ -f "$git_dir/packed-refs" ]] && packed_before=$(sha256sum "$git_dir/packed-refs")

run_sandboxed "$clone" -- bash -c '
  cd "$1" &&
  printf "sandboxed\n" > tracked.txt &&
  git add tracked.txt &&
  git commit -q -m "sandboxed commit" &&
  git rev-parse HEAD
' bash "$clone"
assert_status 0 "git commit succeeds inside the sandboxed clone"

commit_subject=$(git -C "$clone" log -1 --format=%s)
assert_equal "$commit_subject" "sandboxed commit" "the commit landed on the clone's own branch"

refs_after=$(find "$git_dir/refs" -type f -exec sha256sum {} \; | sort)
packed_after=""
[[ -f "$git_dir/packed-refs" ]] && packed_after=$(sha256sum "$git_dir/packed-refs")
assert_equal "$refs_after" "$refs_before" "the parent's refs/ are byte-identical after a sandboxed commit"
assert_equal "$packed_after" "$packed_before" "the parent's packed-refs is byte-identical after a sandboxed commit"

# --- exit status propagation ---

run_sandboxed "$clone" -- bash -c 'exit 37'
assert_status 37 "the sandboxed command's own exit status propagates"

run_sandboxed "$clone" -- true
assert_status 0 "a successful sandboxed command exits 0"

# --- the scope wrapper: unit name and memory cap, as seen by the mock ---

: > "$MOCK_SYSTEMD_RUN_LOG"
run_sandboxed "$clone" -- true
assert_status 0 "a run with the default memory cap succeeds"
assert_contains "$(cat "$MOCK_SYSTEMD_RUN_LOG")" "MemoryMax=2G" "the default memory cap is 2G"
assert_contains "$(cat "$MOCK_SYSTEMD_RUN_LOG")" "--unit allod-run-agent-${desc}-" \
  "the scope's unit name carries the clone's branch"
assert_contains "$(cat "$MOCK_SYSTEMD_RUN_LOG")" "--new-session" \
  "the sandbox starts a new terminal session, so TIOCSTI cannot inject into the invoking terminal"
# Named unshares hard-fail where --unshare-all would silently continue without a namespace; the
# by-hand script checks CapEff against the real kernel, this checks only the invocation.
assert_contains "$(cat "$MOCK_SYSTEMD_RUN_LOG")" "--unshare-user" "the invocation asks for an explicit user-namespace unshare"
assert_contains "$(cat "$MOCK_SYSTEMD_RUN_LOG")" "--unshare-pid" "the invocation asks for an explicit pid-namespace unshare"
assert_contains "$(cat "$MOCK_SYSTEMD_RUN_LOG")" "--cap-drop ALL" "the invocation drops every capability"
case "$(cat "$MOCK_SYSTEMD_RUN_LOG")" in
  *--unshare-all*) fail "'--unshare-all' is gone; a failed namespace must be a hard failure, not a silent skip" ;;
  *) pass "'--unshare-all' is gone; a failed namespace must be a hard failure, not a silent skip" ;;
esac

: > "$MOCK_SYSTEMD_RUN_LOG"
run_sandboxed "$clone" --memory 123M -- true
assert_status 0 "a run with an explicit memory cap succeeds"
assert_contains "$(cat "$MOCK_SYSTEMD_RUN_LOG")" "MemoryMax=123M" "--memory overrides the default cap"

# --- more than one clone: the cross-repo case ---

run_sandboxed "$clone" "$sibling_clone" -- bash -c "$probe_script" bash "$clone" "$sibling_clone"
assert_status 0 "a run naming two clones succeeds"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $clone" "the first clone is present when two are named"
assert_contains "$CAPTURE_OUTPUT" "PRESENT $sibling_clone" "the second clone is present when two are named"

run_sandboxed "$clone" "$sibling_clone" -- pwd
assert_status 0 "a run naming two clones reports a working directory"
assert_equal "$CAPTURE_OUTPUT" "$clone" "the sandbox starts in the first named clone"

# A multi-clone run locks every clone it spans, not only the first; the scope's unit name joins
# every clone's branch with ':', since '+' is not a legal systemd unit-name character.
multi_a_repo="$HOME/work/multi-scope-a"
init_repo "$multi_a_repo" master
multi_a_desc="${RUN_ID}-multi-scope-a"
multi_a_clone=$(begin_clone "$multi_a_desc" "$multi_a_repo")

multi_b_repo="$HOME/work/multi-scope-b"
init_repo "$multi_b_repo" master
multi_b_desc="${RUN_ID}-multi-scope-b"
multi_b_clone=$(begin_clone "$multi_b_desc" "$multi_b_repo")

: > "$MOCK_SYSTEMD_RUN_LOG"
run_sandboxed "$multi_a_clone" "$multi_b_clone" -- true
assert_status 0 "a two-clone run succeeds"
assert_contains "$(cat "$MOCK_SYSTEMD_RUN_LOG")" \
  "--unit allod-run-agent-${multi_a_desc}:agent-${multi_b_desc}-" \
  "the scope's unit name still carries every clone's branch, joined by ':', in ledger order, and the mock's own charset check accepts it"

"$ALLOD" change run "$multi_a_clone" "$multi_b_clone" -- sleep 2 &
multi_run_pid=$!
sleep 0.5
capture "$ALLOD" change cleanup "$multi_b_clone"
assert_status 13 "cleanup on the second clone refuses while a multi-clone run still holds its lock"
capture "$ALLOD" change cleanup "$multi_a_clone"
assert_status 13 "cleanup on the first clone refuses too, not only the one named first"
wait "$multi_run_pid"
git -C "$multi_a_clone" push -q origin HEAD
git -C "$multi_b_clone" push -q origin HEAD
capture "$ALLOD" change cleanup "$multi_a_clone"
assert_status 0 "cleanup on the first clone succeeds once the run has exited"
capture "$ALLOD" change cleanup "$multi_b_clone"
assert_status 0 "cleanup on the second clone succeeds once the run has exited"

# The other direction: 'run' itself refuses to start while an outside
# command holds the exclusive lock, rather than wait for it or barge in.
locked_repo="$HOME/work/run-refuses-locked"
init_repo "$locked_repo" master
locked_desc="${RUN_ID}-run-refuses-locked"
locked_clone=$(begin_clone "$locked_desc" "$locked_repo")
locked_ledger_file="$(git -C "$locked_repo" rev-parse --path-format=absolute --git-dir)/allod-changes/$locked_desc"
exec {run_refuses_locked_fd}>>"$locked_ledger_file"
flock -x "$run_refuses_locked_fd"
capture "$ALLOD" change run "$locked_clone" -- true
assert_status 13 "run refuses to start while an outside command holds the clone's ledger file locked"
exec {run_refuses_locked_fd}>&-

# A second 'run' on the same clone takes the exclusive pre-flight lock too, conflicting with the
# first's shared lock the same way an outside command's would — refusing, non-blocking.
run_vs_run_repo="$HOME/work/run-vs-run"
init_repo "$run_vs_run_repo" master
run_vs_run_desc="${RUN_ID}-run-vs-run"
run_vs_run_clone=$(begin_clone "$run_vs_run_desc" "$run_vs_run_repo")
"$ALLOD" change run "$run_vs_run_clone" -- sleep 2 &
run_vs_run_pid=$!
sleep 0.5
capture "$ALLOD" change run "$run_vs_run_clone" -- true
assert_status 13 "a second 'run' on the same clone refuses at pre-flight rather than block on or run alongside the first"
wait "$run_vs_run_pid"
git -C "$run_vs_run_clone" push -q origin HEAD
capture "$ALLOD" change cleanup "$run_vs_run_clone"
assert_status 0 "cleanup succeeds once the one run that actually started has exited"

# bwrap closes inherited fds it does not recognize; --sync-fd keeps one through its setup, so
# the first clone's lock outlives allod. bwrap honours only one --sync-fd: a real limitation.
lock_lifetime_repo="$HOME/work/lock-lifetime"
init_repo "$lock_lifetime_repo" master
lock_lifetime_desc="${RUN_ID}-lock-lifetime"
lock_lifetime_clone=$(begin_clone "$lock_lifetime_desc" "$lock_lifetime_repo")
: > "$MOCK_SYSTEMD_RUN_LOG"
run_sandboxed "$lock_lifetime_clone" -- true
assert_status 0 "the lock-lifetime fixture's own run succeeds"
assert_contains "$(cat "$MOCK_SYSTEMD_RUN_LOG")" "--sync-fd 3" \
  "bwrap is told to keep the clone's own ledger-lock fd open and carry it into the sandboxed process, not just into systemd-run"

# bwrap honours only one --sync-fd (a second is a stderr warning, not an error): a multi-clone
# run must still pass exactly one, or that warning text leaks into the command's own output.
lock_lifetime_multi_repo="$HOME/work/lock-lifetime-multi"
init_repo "$lock_lifetime_multi_repo" master
lock_lifetime_multi_desc="${RUN_ID}-lock-lifetime-multi"
lock_lifetime_multi_clone=$(begin_clone "$lock_lifetime_multi_desc" "$lock_lifetime_multi_repo")
: > "$MOCK_SYSTEMD_RUN_LOG"
run_sandboxed "$lock_lifetime_clone" "$lock_lifetime_multi_clone" -- true
assert_status 0 "the two-clone lock-lifetime fixture's own run succeeds"
sync_fd_count=$(grep -o -- '--sync-fd' "$MOCK_SYSTEMD_RUN_LOG" | wc -l)
assert_equal "$sync_fd_count" "1" \
  "a two-clone run passes exactly one --sync-fd, never one per clone, since bwrap itself only honors the last"
assert_contains "$(cat "$MOCK_SYSTEMD_RUN_LOG")" "--sync-fd 3" \
  "the one --sync-fd names the first clone's own lock fd (3)"

# The private .claude.json copy must be discarded on every exit path past its creation, even a
# refusal deep inside buildBwrapArgs that unwinds through this command's own discard.
claude_json_leak_repo="$HOME/work/claude-json-leak"
init_repo "$claude_json_leak_repo" master
claude_json_leak_desc="${RUN_ID}-claude-json-leak"
claude_json_leak_clone=$(begin_clone "$claude_json_leak_desc" "$claude_json_leak_repo")
printf '{"fake":"account state"}\n' > "$HOME/.claude.json"
unreadable_dir="$HOME/.config/git/active-pr-branches/unreadable"
mkdir -p "$unreadable_dir"
chmod 000 "$unreadable_dir"
capture env TMPDIR="$TMP" "$ALLOD" change run "$claude_json_leak_clone" -- true
chmod 755 "$unreadable_dir"
rm -rf "$unreadable_dir"
assert_status 14 "run refuses past the point the private .claude.json copy exists"
assert_equal "$(find "$TMP" -maxdepth 1 -name 'allod-claude-json-*' | wc -l)" "0" \
  "no private .claude.json copy is left behind in TMPDIR after the refusal"
assert_equal "$(find "$TMP" -maxdepth 1 -name 'allod-ssh-config-*' | wc -l)" "0" \
  "no private ssh config copies are left behind in TMPDIR after the refusal"

# --- resolving from the working directory with no path given ---

# Not run in a subshell: capture's own CAPTURE_OUTPUT/CAPTURE_STATUS and pass/fail's test_number
# are plain variables a subshell's writes would never carry back out.
cwd_before_no_path_run=$(pwd)
cd "$clone"
capture "$ALLOD" change run -- pwd
cd "$cwd_before_no_path_run"
assert_status 0 "a run with no path resolves the clone from the working directory"
assert_equal "$CAPTURE_OUTPUT" "$clone" "the resolved clone is the one run inside"

# --- refusals that never reach bwrap or systemd-run ---

capture "$ALLOD" change run "$repo" -- true
assert_status 10 "run refuses a path that is not a ledgered clone"
assert_contains "$CAPTURE_OUTPUT" "not a clone" "the non-clone refusal is actionable"

# A non-repository path refuses at exit 10 without any git command running; the old
# resolveGitRepo fallback would have refused differently (exit 1) only after running git.
not_a_repo_path="$HOME/work/not-a-repo-at-all"
mkdir -p "$not_a_repo_path"
not_repo_log="$TMP/git-call-log-not-a-repo-${RUN_ID}"
: > "$not_repo_log"
not_repo_logging_git_bin=$(make_logging_git "$not_repo_log")
saved_path_not_repo="$PATH"
export PATH="$not_repo_logging_git_bin:$PATH"
capture "$ALLOD" change run "$not_a_repo_path" -- true
export PATH="$saved_path_not_repo"
assert_status 10 "run refuses a path that is not a repository at all, with the same exit code"
assert_contains "$CAPTURE_OUTPUT" "not a clone" "the refusal names it the same way as any other non-clone path"
git_call_against_not_repo=$(grep -F -- "$not_a_repo_path" "$not_repo_log" || true)
[[ -z "$git_call_against_not_repo" ]] &&
  pass "no git command ever runs against a path that matches no ledger entry" ||
  fail "no git command ever runs against a path that matches no ledger entry" "$git_call_against_not_repo"

git -C "$clone" checkout -q -b scratch

# The branch check reads .git/HEAD directly, so this refusal runs no git
# command against the clone either, same as the non-repository path above.
wrong_branch_log="$TMP/git-call-log-wrong-branch-${RUN_ID}"
: > "$wrong_branch_log"
wrong_branch_logging_git_bin=$(make_logging_git "$wrong_branch_log")
saved_path_wrong_branch="$PATH"
export PATH="$wrong_branch_logging_git_bin:$PATH"
capture "$ALLOD" change run "$clone" -- true
export PATH="$saved_path_wrong_branch"
assert_status 10 "run refuses a ledgered clone that is not on its ledgered agent/* branch"
git_call_against_wrong_branch=$(grep -F -- "$clone" "$wrong_branch_log" || true)
[[ -z "$git_call_against_wrong_branch" ]] &&
  pass "no git command runs against the clone to read the branch it is checked out on" ||
  fail "no git command runs against the clone to read the branch it is checked out on" "$git_call_against_wrong_branch"

git -C "$clone" checkout -q "agent/$desc"

capture "$ALLOD" change run "$clone" true
assert_status 1 "run refuses a missing '--'"

capture "$ALLOD" change run "$clone" --
assert_status 1 "run refuses a command-less '--'"

no_bwrap_path=$(make_restricted_path_without_bwrap)
capture_with_path "$no_bwrap_path" "$ALLOD" change run "$clone" -- true
assert_status 1 "run refuses when bwrap is not on PATH"
assert_contains "$CAPTURE_OUTPUT" "bwrap" "the missing-bwrap refusal names it"

# The daemon accepts privileged build options (post-build-hook) from a trusted uid and runs them
# as root, so binding its socket for a trusted user would hand a worker root on the VM.
real_user=$(id -un)
trusted_nix_bin=$(make_mock_nix "$real_user")
capture env PATH="$trusted_nix_bin:$PATH" "$ALLOD" change run "$clone" -- true
assert_status 12 "run refuses when the invoking user is a Nix trusted-user"
assert_contains "$CAPTURE_OUTPUT" "trusted-user" "the trusted-user refusal is actionable"
assert_contains "$CAPTURE_OUTPUT" "allod/vm#12" "the trusted-user refusal points at the issue that removes the trust"

root_only_nix_bin=$(make_mock_nix_netrc root "")
capture env PATH="$root_only_nix_bin:$PATH" "$ALLOD" change run "$clone" -- true
assert_status 0 "run proceeds when trusted-users names only root, not the invoking user"

# A bare '*' in trusted-users means every user is trusted; no per-name or per-group check above
# matches it literally, so it needs its own check.
wildcard_nix_bin=$(make_mock_nix '*')
capture env PATH="$wildcard_nix_bin:$PATH" "$ALLOD" change run "$clone" -- true
assert_status 12 "run refuses when trusted-users names '*', trusting every user"
assert_contains "$CAPTURE_OUTPUT" "trusted-user" "the wildcard refusal is actionable"

empty_nix_bin=$(make_mock_nix '')
capture env PATH="$empty_nix_bin:$PATH" "$ALLOD" change run "$clone" -- true
assert_status 1 "run refuses an empty successful trusted-users answer"

fake_nix_marker="$TMP/fake-nix-selected"
fake_nix_bin="$TMP/fake-nix-path"
mkdir -p "$fake_nix_bin"
printf '#!/bin/sh\ntouch "%s"\necho root\n' "$fake_nix_marker" > "$fake_nix_bin/nix"
chmod +x "$fake_nix_bin/nix"
capture env PATH="$fake_nix_bin:$PATH" "$PRODUCTION_ALLOD" change run "$clone" -- true
[[ ! -e "$fake_nix_marker" ]] && pass "production run ignores a fake nix earlier on PATH" ||
  fail "production run ignores a fake nix earlier on PATH"

# A 'nix' that cannot even be asked must refuse naming the error, never pass as 'nothing to
# compare against' — the old behaviour let a genuinely trusted user's run proceed unnoticed.
failing_nix_bin="$TMP/failing-nix-bin"
mkdir -p "$failing_nix_bin"
{
  printf '#!%s\n' "$(command -v bash)"
  printf 'echo "mock nix: config show failed" >&2\nexit 1\n'
} > "$failing_nix_bin/nix"
chmod +x "$failing_nix_bin/nix"
capture env PATH="$failing_nix_bin:$PATH" "$ALLOD" change run "$clone" -- true
assert_status 1 "run refuses when nix itself cannot answer the trusted-users query"
assert_contains "$CAPTURE_OUTPUT" "trusted-users" "the refusal names which query failed"

# Once trusted-users can be asked (and answers 'root', not this user), the
# netrc preflight's own query must fail closed the same way.
failing_netrc_nix_bin="$TMP/failing-netrc-nix-bin"
mkdir -p "$failing_netrc_nix_bin"
{
  printf '#!%s\n' "$(command -v bash)"
  cat <<'EOF'
if [[ "$1 $2 $3" == "config show trusted-users" ]]; then
  echo "root"
  exit 0
fi
echo "mock nix: config show netrc-file failed" >&2
exit 1
EOF
} > "$failing_netrc_nix_bin/nix"
chmod +x "$failing_netrc_nix_bin/nix"
capture env PATH="$failing_netrc_nix_bin:$PATH" "$ALLOD" change run "$clone" -- true
assert_status 1 "run refuses when nix itself cannot answer the netrc-file query"
assert_contains "$CAPTURE_OUTPUT" "netrc-file" "the refusal names the netrc query failed"

# NIX_CONFIG, NIX_USER_CONF_FILES, and NIX_CONF_DIR can each redirect nix's own answer away
# from the root-only daemon config, hiding a trusted user's membership from this check.
nix_env_probe_log="$TMP/nix-env-probe.log"
: > "$nix_env_probe_log"
env_probe_nix_bin=$(make_mock_nix_env_probe "$real_user" "$nix_env_probe_log")
capture env NIX_CONFIG="trusted-users = nobody" NIX_USER_CONF_FILES="$TMP/attacker-nix.conf" \
  NIX_CONF_DIR="$TMP/attacker-nix-conf-dir" \
  PATH="$env_probe_nix_bin:$PATH" "$ALLOD" change run "$clone" -- true
assert_status 12 "run refuses even when the invoker sets NIX_CONFIG, NIX_USER_CONF_FILES, and NIX_CONF_DIR"
grep -Fxq "NIX_CONFIG=<unset>" "$nix_env_probe_log" &&
  pass "NIX_CONFIG is removed from the environment before asking nix" ||
  fail "NIX_CONFIG is removed from the environment before asking nix" "$(cat "$nix_env_probe_log")"
grep -Fxq "NIX_USER_CONF_FILES=" "$nix_env_probe_log" &&
  pass "NIX_USER_CONF_FILES is forced to an explicit empty value before asking nix" ||
  fail "NIX_USER_CONF_FILES is forced to an explicit empty value before asking nix" "$(cat "$nix_env_probe_log")"
grep -Fxq "NIX_CONF_DIR=<unset>" "$nix_env_probe_log" &&
  pass "NIX_CONF_DIR is removed from the environment before asking nix, falling back to its own compiled-in default" ||
  fail "NIX_CONF_DIR is removed from the environment before asking nix, falling back to its own compiled-in default" "$(cat "$nix_env_probe_log")"

# The invoking user's own name sits in the stderr noise, not the stdout answer: merged, it would
# wrongly appear as a trusted-users entry and refuse; kept separate, only 'root' is parsed.
noisy_nix_bin="$TMP/noisy-nix-bin"
mkdir -p "$noisy_nix_bin"
{
  printf '#!%s\n' "$(command -v bash)"
  printf 'REAL_USER=%q\n' "$real_user"
  cat <<'EOF'
if [[ "$1 $2 $3" == "config show trusted-users" ]]; then
  printf 'warning: an unrelated notice mentions %s in passing\n' "$REAL_USER" >&2
  echo "root"
  exit 0
fi
if [[ "$1 $2 $3" == "config show netrc-file" ]]; then
  echo ""
  exit 0
fi
echo "mock nix: unexpected args: $*" >&2
exit 1
EOF
} > "$noisy_nix_bin/nix"
chmod +x "$noisy_nix_bin/nix"
capture env PATH="$noisy_nix_bin:$PATH" "$ALLOD" change run "$clone" -- true
assert_status 0 "a stderr warning naming the invoking user, alongside a valid trusted-users answer of 'root' on stdout, does not get parsed as part of the answer"

# /etc is bound whole; a readable netrc-file there would be exposed inside the sandbox like any
# other file under /etc.
netrc_path="$TMP/fake-netrc"
printf 'machine example.invalid login x password y\n' > "$netrc_path"
chmod 600 "$netrc_path"
netrc_nix_bin=$(make_mock_nix_netrc root "$netrc_path")
capture env PATH="$netrc_nix_bin:$PATH" "$ALLOD" change run "$clone" -- true
assert_status 15 "run refuses when nix's own netrc-file is readable"
assert_contains "$CAPTURE_OUTPUT" "netrc" "the netrc refusal names it"
rm -f "$netrc_path"

# When nix names no netrc-file (or names one that does not exist), run is
# not stopped by this check at all.
no_netrc_nix_bin=$(make_mock_nix_netrc root "")
capture env PATH="$no_netrc_nix_bin:$PATH" "$ALLOD" change run "$clone" -- true
assert_status 0 "run proceeds when nix names no netrc-file"

# A symlink at a fixed $HOME-relative path would bind whatever it resolves to as if it were the
# real thing, so 'run' refuses rather than follow it.
pi_target="$TMP/elsewhere-pi"
mkdir -p "$pi_target"
ln -s "$pi_target" "$HOME/.pi"
capture "$ALLOD" change run "$clone" -- true
assert_status 14 "run refuses when ~/.pi is a symlink rather than follow it"
assert_contains "$CAPTURE_OUTPUT" ".pi" "the symlink refusal names the path"
rm -f "$HOME/.pi"

# ~/.config/git itself as a symlink is the same risk as ~/.pi above: bindGitConfigTree's own walk
# cannot tell that apart from a real directory, so this is caught before that walk starts.
config_git_real="$HOME/.config/git"
config_git_elsewhere="$TMP/elsewhere-config-git"
mv "$config_git_real" "$config_git_elsewhere"
ln -s "$config_git_elsewhere" "$config_git_real"
capture "$ALLOD" change run "$clone" -- true
assert_status 14 "run refuses when ~/.config/git is a symlink rather than follow it"
assert_contains "$CAPTURE_OUTPUT" ".config/git" "the symlink refusal names the path"
rm -f "$config_git_real"
mv "$config_git_elsewhere" "$config_git_real"

mkdir -p "$HOME/.pi/agent"
ln -s "$repo" "$HOME/.pi/agent/sessions"
capture "$ALLOD" change run "$clone" -- true
assert_status 14 "run refuses a writable runner source symlink into a shared checkout"
rm "$HOME/.pi/agent/sessions"

mkdir -p "$HOME/.claude/projects/unreadable"
ln "$forgejo_token_target" "$HOME/.claude/projects/unreadable/alias"
chmod 000 "$HOME/.claude/projects/unreadable"
capture "$ALLOD" change run "$clone" -- true
assert_status 14 "run refuses an unreadable runner directory that could hide a credential alias"
chmod 700 "$HOME/.claude/projects/unreadable"
rm -rf "$HOME/.claude/projects/unreadable"

mv "$HOME/.config/git/protected-refs-policy" "$TMP/protected-refs-policy.saved"
capture "$ALLOD" change run "$clone" -- true
assert_status 14 "run refuses a missing global Git protection rail"
mv "$TMP/protected-refs-policy.saved" "$HOME/.config/git/protected-refs-policy"

mv "$HOME/.config/git/hooks/pre-commit" "$TMP/pre-commit.saved"
capture "$ALLOD" change run "$clone" -- true
assert_status 14 "run refuses a missing managed Git hook"
mv "$TMP/pre-commit.saved" "$HOME/.config/git/hooks/pre-commit"

chmod 0111 "$HOME/.config/git/hooks"
capture "$ALLOD" change run "$clone" -- true
assert_status 14 "run refuses an unreadable Git hooks directory"
chmod 0755 "$HOME/.config/git/hooks"

printf '\nTests run: %d\n' "$test_number"
printf 'All %d allod change run tests passed.\n' "$test_number"
