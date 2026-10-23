#!/usr/bin/env bash
# 'allod change run' against a real systemd user manager and Nix daemon, not wired into 'nix
# flake check'. Run from the devShell (nix develop -c bash ...), which builds a test-only binary.
set -euo pipefail

ALLOD="${ALLOD_UNDER_TEST:-$(command -v allod || true)}"
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORK_DIR="${WORK_DIR:-$HOME/work}"
FIXTURE="$WORK_DIR/allod-change-run-fixture-$$"
CLONE=""
CLONE2=""
FAILURES=0
STEPS=0
OUTPUT=""
STATUS=0

cleanup() {
  systemctl --user reset-failed 'allod-run-*' >/dev/null 2>&1 || true
  if [ -n "$CLONE" ] && [ -d "$CLONE" ]; then
    git -C "$CLONE" checkout -q -- . >/dev/null 2>&1 || true
    git -C "$CLONE" clean -qfd >/dev/null 2>&1 || true
    "$ALLOD" change cleanup "$CLONE" >/dev/null 2>&1 || rm -rf "$CLONE"
  fi
  if [ -n "$CLONE2" ] && [ -d "$CLONE2" ]; then
    git -C "$CLONE2" checkout -q -- . >/dev/null 2>&1 || true
    git -C "$CLONE2" clean -qfd >/dev/null 2>&1 || true
    "$ALLOD" change cleanup "$CLONE2" >/dev/null 2>&1 || rm -rf "$CLONE2"
  fi
  rm -rf "$FIXTURE"
}
trap cleanup EXIT

for tool in systemctl systemd-run journalctl bwrap nix-build git; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf 'this script needs %s on PATH, and it is not there\n' "$tool" >&2
    exit 2
  fi
done
if [ ! -x "$ALLOD" ]; then
  printf 'set ALLOD_UNDER_TEST to the allod binary to test\n' >&2
  exit 2
fi

# --- Fixture: a repo under WORK_DIR with a local bare origin (no network, no protected-refs-policy
# remote rail), cloned to an agent/* branch. ---

mkdir -p "$FIXTURE"
git init -q -b master "$FIXTURE/repo"
git -C "$FIXTURE/repo" config user.name "By Hand"
git -C "$FIXTURE/repo" config user.email "byhand@example.invalid"
printf 'base\n' > "$FIXTURE/repo/tracked.txt"
git -C "$FIXTURE/repo" add tracked.txt
git -C "$FIXTURE/repo" commit -qm initial
git clone -q --bare "$FIXTURE/repo" "$FIXTURE/remote.git"
git -C "$FIXTURE/repo" remote add origin "$FIXTURE/remote.git"
git -C "$FIXTURE/repo" fetch -q origin
git -C "$FIXTURE/repo" remote set-head origin master
# A real repo's own 'git gc' writes '.git/objects/info/commit-graph', copied by 'git clone' into
# every clone made from it; no command reads or minds it.
git -C "$FIXTURE/repo" commit-graph write --reachable

CLONE=$("$ALLOD" change begin -d "by-hand-probe-$$" "$FIXTURE/repo")
CLONE2=$("$ALLOD" change begin -d "by-hand-probe2-$$" "$FIXTURE/repo")

# --- Harness (same shape as tests/site-preview-by-hand.sh) ---

step() {
  local name="$1"
  shift
  set +e
  ( set -e; "$@" )
  status=$?
  set -e
  STEPS=$((STEPS + 1))
  if [ "$status" -eq 0 ]; then
    printf 'PASS %s\n' "$name"
  else
    printf 'FAIL %s\n' "$name" >&2
    FAILURES=$((FAILURES + 1))
  fi
}

fail_step() {
  printf '     %s\n' "$@" >&2
  exit 1
}

run_sandboxed() {
  set +e
  OUTPUT=$("$ALLOD" change run "$@" 2>&1)
  STATUS=$?
  set -e
}

# This VM's own nix trusted-users list is read for real here, before anything mocks it.
real_trusted_user_is_refused() {
  run_sandboxed "$CLONE" -- true
  if [ "$STATUS" -ne 12 ]; then
    fail_step "expected exit 12 (trusted Nix user), got $STATUS" "output:" "$OUTPUT"
  fi
  printf '     %s\n' "$OUTPUT"
}

# Every later step needs the sandbox to run; a 'nix' reporting only 'root' stands in once the
# real refusal above is proven. Set up only after that check, since PATH is process-global.
stand_in_for_the_trusted_user_check() {
  local mock_nix_bin="$FIXTURE/mock-nix-bin"
  mkdir -p "$mock_nix_bin"
  cat > "$mock_nix_bin/nix" <<'EOF'
#!/usr/bin/env bash
if [ "$1 $2 $3" = "config show trusted-users" ]; then
  echo root
  exit 0
fi
if [ "$1 $2 $3" = "config show netrc-file" ]; then
  echo ""
  exit 0
fi
echo "mock nix: unexpected args: $*" >&2
exit 1
EOF
  chmod +x "$mock_nix_bin/nix"
  go build -C "$ROOT" -ldflags '-X main.nixExecutablePath=nix' \
    -o "$FIXTURE/allod-mock-nix" ./cmd/allod
  ALLOD="$FIXTURE/allod-mock-nix"
  export PATH="$mock_nix_bin:$PATH"
}

current_unit() {
  systemctl --user list-units --no-legend --plain 'allod-run-*' 2>/dev/null | awk '{print $1}'
}

# --- Steps ---

the_scope_carries_the_cap() {
  "$ALLOD" change run --memory 256M "$CLONE" -- sleep 4 &
  local bg=$!
  sleep 1
  local unit
  unit=$(current_unit)
  if [ -z "$unit" ]; then
    kill "$bg" >/dev/null 2>&1 || true
    fail_step "no allod-run-* scope was listed while the sandboxed sleep ran"
  fi
  printf '     unit: %s\n' "$unit"
  systemctl --user show "$unit" -p MemoryMax
  local cap
  cap=$(systemctl --user show "$unit" -p MemoryMax --value)
  if [ "$cap" != "268435456" ]; then
    fail_step "expected MemoryMax=268435456 (256M), got $cap"
  fi
  wait "$bg"
}

a_two_clone_run_names_both_and_is_writable_in_both() {
  # '+' is not a legal systemd unit-name character, so a multi-clone scope name used to fail at
  # 'systemd-run --unit' itself; the fake 'systemctl' elsewhere hid this entirely.
  "$ALLOD" change run "$CLONE" "$CLONE2" -- bash -c '
    printf "clone1\n" > "$1/by-hand-multi-1.txt" &&
    printf "clone2\n" > "$2/by-hand-multi-2.txt" &&
    sleep 2
  ' bash "$CLONE" "$CLONE2" &
  local bg=$!
  sleep 1
  local unit
  unit=$(current_unit)
  if [ -z "$unit" ]; then
    kill "$bg" >/dev/null 2>&1 || true
    fail_step "no allod-run-* scope was listed while the two-clone sandbox ran"
  fi
  printf '     unit: %s\n' "$unit"
  set +e
  wait "$bg"
  local status=$?
  set -e
  if [ "$status" -ne 0 ]; then
    fail_step "the two-clone run itself failed with status $status"
  fi
  if [ ! -f "$CLONE/by-hand-multi-1.txt" ]; then
    fail_step "the first named clone was not writable inside the two-clone sandbox"
  fi
  if [ ! -f "$CLONE2/by-hand-multi-2.txt" ]; then
    fail_step "the second named clone was not writable inside the two-clone sandbox"
  fi
  rm -f "$CLONE/by-hand-multi-1.txt" "$CLONE2/by-hand-multi-2.txt"
}

a_clone_of_a_repo_with_its_own_commit_graph_is_accepted() {
  if [ ! -e "$CLONE/.git/objects/info/commit-graph" ]; then
    fail_step "the fixture repo's own commit-graph was not copied into the clone by 'git clone'"
  fi
  run_sandboxed "$CLONE" -- true
  if [ "$STATUS" -ne 0 ]; then
    fail_step "run refused a clone whose parent had its own legitimate commit-graph" "$OUTPUT"
  fi
}

# --cap-drop ALL must leave nothing effective over a real kernel; the mocked systemd-run
# elsewhere only checks the invocation asked for it.
the_sandbox_drops_every_capability() {
  run_sandboxed "$CLONE" -- grep '^CapEff:' /proc/self/status
  if [ "$STATUS" -ne 0 ]; then
    fail_step "could not read CapEff inside the sandbox" "$OUTPUT"
  fi
  printf '     %s\n' "$OUTPUT"
  local capeff
  capeff=$(printf '%s\n' "$OUTPUT" | awk '{print $2}')
  if [ "$capeff" != "0000000000000000" ]; then
    fail_step "expected CapEff all zeros, got $capeff"
  fi
}

a_run_commits_inside_and_leaves_the_parent_alone() {
  local refs_before packed_before refs_after packed_after git_dir
  git_dir=$(git -C "$FIXTURE/repo" rev-parse --path-format=absolute --git-dir)
  refs_before=$(find "$git_dir/refs" -type f -exec sha256sum {} \; | sort)
  [ -f "$git_dir/packed-refs" ] && packed_before=$(sha256sum "$git_dir/packed-refs") || packed_before=""

  run_sandboxed "$CLONE" -- bash -c '
    cd "$1" &&
    printf "sandboxed\n" > by-hand-tracked.txt &&
    git add by-hand-tracked.txt &&
    git commit -q -m "sandboxed by-hand commit"
  ' bash "$CLONE"
  if [ "$STATUS" -ne 0 ]; then
    fail_step "git commit inside the sandbox failed" "$OUTPUT"
  fi
  if [ "$(git -C "$CLONE" log -1 --format=%s)" != "sandboxed by-hand commit" ]; then
    fail_step "the commit did not land on the clone's own branch"
  fi

  refs_after=$(find "$git_dir/refs" -type f -exec sha256sum {} \; | sort)
  [ -f "$git_dir/packed-refs" ] && packed_after=$(sha256sum "$git_dir/packed-refs") || packed_after=""
  if [ "$refs_after" != "$refs_before" ] || [ "$packed_after" != "$packed_before" ]; then
    fail_step "the parent's refs changed after a sandboxed commit"
  fi
  git -C "$CLONE" reset -q --hard HEAD^
}

exceeding_the_cap_kills_only_the_scope() {
  local probe="$CLONE/by-hand-oom-probe.sh"
  cat > "$probe" <<'EOF'
#!/usr/bin/env bash
x=$(head -c 200000000 /dev/zero | tr '\0' 'a')
echo "done with length ${#x}"
EOF
  chmod +x "$probe"
  sleep 300 &
  local survivor_pid=$!
  run_sandboxed --memory 32M "$CLONE" -- bash "$probe"
  rm -f "$probe"
  if [ "$STATUS" -eq 0 ]; then
    kill "$survivor_pid" >/dev/null 2>&1 || true
    fail_step "the oversized allocation did not get killed" "$OUTPUT"
  fi
  printf '     sandboxed command exit status: %s\n' "$STATUS"
  if ! kill -0 "$survivor_pid" 2>/dev/null; then
    fail_step "a process started outside the scope before the run did not survive the OOM kill: it escaped the scope"
  fi
  kill "$survivor_pid" >/dev/null 2>&1 || true
  wait "$survivor_pid" 2>/dev/null || true
  local unit result
  unit=$(systemctl --user list-units --all --no-legend --plain 'allod-run-*' 2>/dev/null | awk '{print $1}')
  result=$(systemctl --user show "$unit" -p Result --value 2>/dev/null || true)
  printf '     unit: %s result: %s\n' "$unit" "$result"
  if [ "$result" != "oom-kill" ]; then
    fail_step "expected the scope's Result to be oom-kill, got: $result"
  fi
  journalctl --user -u "$unit" --no-pager 2>&1 | tail -5
  systemctl --user reset-failed "$unit" >/dev/null 2>&1 || true
}

# Read-only, and skipped rather than faked when absent: this script runs against the real
# $HOME, never a fixture one, so it must not write to the VM's own forge credential.
the_real_forge_token_is_readable_inside() {
  if [ ! -r "$HOME/.config/git/forgejo-token" ]; then
    printf '     no readable ~/.config/git/forgejo-token on this VM; nothing to probe\n'
    return 0
  fi
  run_sandboxed "$CLONE" -- cat "$HOME/.config/git/forgejo-token"
  if [ "$STATUS" -ne 0 ]; then
    fail_step "the VM's own forgejo-token was not readable inside the sandbox" "$OUTPUT"
  fi
  if [ "$OUTPUT" != "$(cat "$HOME/.config/git/forgejo-token")" ]; then
    fail_step "the token read inside the sandbox did not match the real file"
  fi
}

# Read-only, and skipped when there is no readable token to probe with: a real forge binary on
# PATH must see the bound token as its own, not just a readable file.
the_real_forge_token_authenticates_inside() {
  if [ ! -r "$HOME/.config/git/forgejo-token" ]; then
    printf '     no readable ~/.config/git/forgejo-token on this VM; nothing to probe\n'
    return 0
  fi
  run_sandboxed "$CLONE" -- forge auth status
  if [ "$STATUS" -ne 0 ]; then
    fail_step "'forge auth status' failed inside the sandbox" "$OUTPUT"
  fi
  printf '     %s\n' "$OUTPUT"
}

# Read-only too, and skipped when there is no ~/.ssh/config to read: every identity file it
# names must be present inside, owned by the invoking user, not root or nobody.
every_ssh_identity_file_is_present_and_owned() {
  if [ ! -r "$HOME/.ssh/config" ]; then
    printf '     no readable ~/.ssh/config on this VM; nothing to probe\n'
    return 0
  fi
  local uid identity keyword value found=0
  uid=$(id -u)
  while read -r keyword value; do
    case "$keyword" in
      [Ii][Dd][Ee][Nn][Tt][Ii][Tt][Yy][Ff][Ii][Ll][Ee]) ;;
      *) continue ;;
    esac
    value="${value%\"}"
    value="${value#\"}"
    value="${value//%d/$HOME}"
    case "$value" in
      "~") value="$HOME" ;;
      "~/"*) value="$HOME/${value#\~/}" ;;
    esac
    [ -f "$value" ] || continue
    found=1
    run_sandboxed "$CLONE" -- stat -c '%u' "$value"
    if [ "$STATUS" -ne 0 ]; then
      fail_step "identity file '$value' was not present inside the sandbox" "$OUTPUT"
    fi
    if [ "$OUTPUT" != "$uid" ]; then
      fail_step "identity file '$value' inside the sandbox is owned by uid $OUTPUT, not the invoking user ($uid)"
    fi
  done < "$HOME/.ssh/config"
  if [ "$found" -eq 0 ]; then
    printf '     ~/.ssh/config names no identity file that resolves to a regular file; nothing to probe\n'
  fi
}

the_real_ssh_config_parses_inside() {
  if ! command -v ssh >/dev/null 2>&1; then
    printf '     no ssh binary on this VM; nothing to probe\n'
    return 0
  fi
  if [ ! -r "$HOME/.ssh/config" ]; then
    printf '     no readable ~/.ssh/config on this VM; nothing to probe\n'
    return 0
  fi
  local host
  host=$(awk '
    tolower($1) == "host" {
      for (i = 2; i <= NF; i++) {
        if ($i != "*" && substr($i, 1, 1) != "!") { print $i; exit }
      }
    }
  ' "$HOME/.ssh/config")
  if [ -z "$host" ]; then
    host=ssh-config-probe.invalid
  fi
  run_sandboxed "$CLONE" -- ssh -G "$host"
  if [ "$STATUS" -ne 0 ]; then
    fail_step "ssh -G '$host' rejected the config chain inside the sandbox" "$OUTPUT"
  fi
  printf '     ssh -G parsed the config for %s\n' "$host"
}

a_nix_build_succeeds_through_the_daemon_socket() {
  # 'derivation' needs no channel/flake registry lookup, unlike '<nixpkgs>', so this build stays
  # independent of NIX_PATH being cleared.
  run_sandboxed "$CLONE" -- nix-build --no-out-link -E \
    'derivation { name = "allod-change-run-by-hand-probe"; system = "x86_64-linux"; builder = "/bin/sh"; args = [ "-c" "echo ok > $out" ]; }'
  if [ "$STATUS" -ne 0 ]; then
    fail_step "nix-build through the daemon socket failed" "$OUTPUT"
  fi
  printf '     %s\n' "$OUTPUT"
}

step 'a real Nix trusted user is refused for real' real_trusted_user_is_refused
# Not run through 'step': that runs its argument in a subshell, and the
# PATH change this makes must reach every step after it, not just itself.
stand_in_for_the_trusted_user_check
step 'the scope is named and carries the memory cap' the_scope_carries_the_cap
step 'a two-clone run names both clones and both are writable inside' a_two_clone_run_names_both_and_is_writable_in_both
step 'a clone of a repo with its own commit-graph is accepted' a_clone_of_a_repo_with_its_own_commit_graph_is_accepted
step 'the sandbox drops every capability over a real kernel' the_sandbox_drops_every_capability
step 'a sandboxed commit lands on the clone, the parent is untouched' a_run_commits_inside_and_leaves_the_parent_alone
step 'exceeding the memory cap kills only the scope' exceeding_the_cap_kills_only_the_scope
step 'the real forgejo-token, if any, is readable inside the sandbox' the_real_forge_token_is_readable_inside
step 'the real forgejo-token, if any, authenticates forge inside the sandbox' the_real_forge_token_authenticates_inside
step 'every identity file the real ~/.ssh/config names, if any, is present and owned inside' every_ssh_identity_file_is_present_and_owned
step 'ssh parses the real config include chain inside the sandbox' the_real_ssh_config_parses_inside
step 'nix-build succeeds inside the sandbox through the daemon socket' a_nix_build_succeeds_through_the_daemon_socket

if [ "$FAILURES" -ne 0 ]; then
  printf '%d of %d steps failed\n' "$FAILURES" "$STEPS" >&2
  exit 1
fi
printf 'all %d steps passed\n' "$STEPS"
