#!/usr/bin/env bash
# Branch heads are read over the git protocol and compared to the lock before
# nix is asked anything, so a run that finds nothing moved makes no nix call,
# and a moved input is pinned to the head that was read rather than resolved
# again by nix. GitHub's REST API allows sixty unauthenticated requests an hour
# per address, `nix flake update` spends one per named GitHub input per repo
# just to learn that nothing moved, and every machine behind one address draws
# on the same sixty. A ref advertisement costs nothing against it.
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

a="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
b="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
github_lookup=$'git\tls-remote\t--exit-code https://github.com/acme/demo refs/heads/main refs/tags/main'

nix_calls() { grep -c '^nix' "$MOCK_LOG" || true; }
nix_updates() { grep -c $'^nix\tflake update' "$MOCK_LOG" || true; }
head_reads() { grep -c $'^git\tls-remote' "$MOCK_LOG" || true; }

# --- Nothing moved: one read for a branch two repositories pin, no nix ---
new_home current
write_github_lock "$HOME/work/app"
write_github_lock "$HOME/work/app2"
export MOCK_HEAD="$a" MOCK_SCENARIO=current
output=$(run_cascade demo)
assert_equal "$(grep -c 'already up to date' <<<"$output")" "2" \
  "reports both repositories as up to date"
assert_log_contains "$github_lookup" "reads the branch head with git ls-remote"
assert_equal "$(head_reads)" "1" "reads a branch shared by two repositories once"
assert_equal "$(nix_calls)" "0" "makes no nix call when nothing moved"
assert_equal "$(grep -Ec $'^git\tapp2?\t(add|commit|push)' "$MOCK_LOG" || true)" "0" \
  "stages, commits and pushes nothing when nothing moved"

# --- A moved GitHub input is pinned to the head just read, in every mode ---
new_home moved
write_github_lock "$HOME/work/app"
export MOCK_HEAD="$b" MOCK_SCENARIO=direct
output=$(run_cascade demo)
assert_contains "$output" "updating demo..." "names the moved input"
assert_contains "$output" "demo: aaaaaaa → bbbbbbb" "reports the revision change"
assert_log_contains $'nix\tflake lock '"$HOME/work/app"' --override-input demo github:acme/demo/'"$b" \
  "pins the moved input with nix flake lock"
assert_equal "$(nix_updates)" "0" "does not ask nix to resolve the branch again"
assert_equal "$(jq -r .nodes.demo.locked.rev "$HOME/work/app/flake.lock")" "$b" \
  "writes the pinned revision into the lock"
assert_contains "$output" "committed and pushed" "commits and pushes the moved lock"

new_home moved-dry-run
write_github_lock "$HOME/work/app"
before=$(sha256sum "$HOME/work/app/flake.lock")
export MOCK_HEAD="$b" MOCK_SCENARIO=dry-run
output=$(run_cascade demo --dry-run)
assert_contains "$output" "demo: aaaaaaa → bbbbbbb" "dry-run reports the revision change"
assert_contains "$output" "dry-run: no changes made" "dry-run says it changed nothing"
assert_log_contains $'nix\tflake lock '"$HOME/work/app"' --override-input demo github:acme/demo/'"$b"' --output-lock-file /tmp/app.flake.lock.new' \
  "dry-run pins into the temporary lock"
assert_equal "$(sha256sum "$HOME/work/app/flake.lock")" "$before" "dry-run leaves the lock untouched"

new_home moved-pr
write_github_lock "$HOME/work/app"
export MOCK_HEAD="$b" MOCK_SCENARIO=pr
output=$(run_cascade demo --pr)
assert_log_contains $'nix\tflake lock '"$HOME/work/app"' --override-input demo github:acme/demo/'"$b" \
  "PR mode pins the moved input with nix flake lock"
assert_log_contains $'git\tapp\tcheckout -B agent/flake-update-demo' "PR mode still branches"
assert_contains "$output" "PR #42 updated" "PR mode still reaches the forge"

# --- A git branch on a forge pins with a git+ reference ---
new_home moved-git
write_git_lock "$HOME/work/app"
export MOCK_HEAD="$b" MOCK_SCENARIO=dry-run
output=$(run_cascade demo --dry-run)
assert_log_contains $'git\tls-remote\t--exit-code ssh://git@forge.example:2222/acme/demo.git refs/heads/master refs/tags/master' \
  "reads a git input's branch at its own URL"
assert_log_contains $'nix\tflake lock '"$HOME/work/app"' --override-input demo git+ssh://git@forge.example:2222/acme/demo.git?rev='"$b" \
  "pins a git input with a git+ reference carrying the revision"

# --- A head the tool cannot read is left to nix, and says so ---
new_home unreachable
write_github_lock "$HOME/work/app"
export MOCK_HEAD=unreachable MOCK_SCENARIO=dry-run
output=$(run_cascade demo --dry-run)
assert_contains "$output" "demo: could not resolve main at https://github.com/acme/demo; asking nix instead" \
  "says when a head could not be read"
assert_log_contains $'nix\tflake update demo --flake '"$HOME/work/app"' --output-lock-file /tmp/app.flake.lock.new' \
  "falls back to nix flake update for an unresolved input"
assert_equal "$(head_reads)" "1" "does not retry a failed read"

# --- An input pinned to a revision in flake.nix is neither read nor updated ---
new_home pinned
write_pinned_lock "$HOME/work/app"
export MOCK_HEAD="$b" MOCK_SCENARIO=dry-run
output=$(run_cascade demo --dry-run)
assert_contains "$output" "already up to date" "treats a revision-pinned input as nothing to move"
assert_equal "$(( $(nix_calls) + $(head_reads) ))" "0" "neither reads nor updates a pinned input"

# --- One resolvable and one opaque input share one temporary lock ---
new_home mixed
write_declared_lock "$HOME/work/app" \
  demo '{"type":"github","owner":"acme","repo":"demo","ref":"main"}' \
  other '{"type":"indirect","id":"other"}'
export MOCK_HEAD="$b" MOCK_SCENARIO=dry-run
output=$(run_cascade demo other --dry-run)
assert_contains "$output" "updating demo other (dry-run)..." "names both inputs"
assert_log_contains $'nix\tflake lock '"$HOME/work/app"' --override-input demo github:acme/demo/'"$b"' --output-lock-file /tmp/app.flake.lock.new' \
  "pins the resolvable input into the temporary lock"
assert_log_contains $'nix\tflake update other --flake '"$HOME/work/app"' --reference-lock-file /tmp/app.flake.lock.new --output-lock-file /tmp/app.flake.lock.new' \
  "updates the opaque input from and into the same temporary lock"
assert_contains "$output" "demo: aaaaaaa → bbbbbbb" "reports the pinned change"
assert_contains "$output" "other: aaaaaaa → bbbbbbb" "reports the nix-resolved change"

# --- A failed nix flake lock is handled like a failed update ---
new_home lock-fail
write_github_lock "$HOME/work/app"
export MOCK_HEAD="$b" MOCK_SCENARIO=direct MOCK_FAIL_NIX="flake lock"
output=$(run_cascade demo 2>&1) && status=0 || status=$?
unset MOCK_FAIL_NIX
assert_equal "$status" "1" "a failed nix flake lock ends with status 1"
assert_contains "$output" "nix flake lock failed, skipping" "reports the failed lock"
assert_log_contains $'git\tapp\tcheckout -- flake.lock' "restores the lock file after a failed lock"
assert_equal "$(grep -Ec $'^git\tapp\t(add|commit|push)' "$MOCK_LOG" || true)" "0" \
  "never stages, commits or pushes after a failed lock"
if [[ -e /tmp/app.flake.lock.old ]]; then
  fail "removes the temporary lock copy after a failed lock" "left behind: /tmp/app.flake.lock.old"
else
  pass "removes the temporary lock copy after a failed lock"
fi

finish_tests "flake-update-cascade resolve-heads"
