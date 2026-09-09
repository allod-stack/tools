#!/usr/bin/env bash
# The paths a repository is updated on come from the lock as it stands after
# the tool's own pull, so a checkout whose pull changed its input graph is
# judged on its current inputs rather than on the stale lock preflight
# classified (allod/tools#175). Before, a nested path from the old lock was
# handed to nix, which warned that it matched nothing and did nothing, and the
# repository was reported already up to date with its current inputs never
# examined.
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

a="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
b="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

# The lock preflight sees reaches demo only through vm; the pull replaces it
# with one that pins demo directly.
write_stale_lock() {
  mkdir -p "$1/.git"
  : > "$1/.git/HEAD"
  cat > "$1/flake.lock" <<LOCK
{
  "nodes": {
    "root": {"inputs": {"vm": "vm"}},
    "vm": {"inputs": {"demo": "demo"}, "original": {"type": "github", "owner": "acme", "repo": "vm", "ref": "main"}, "locked": {"rev": "$a"}},
    "demo": {"original": {"type": "github", "owner": "acme", "repo": "demo", "ref": "main"}, "locked": {"rev": "$a"}}
  }
}
LOCK
}

new_home graph-changed
write_stale_lock "$HOME/work/app"
export MOCK_PULLED_LOCKS="$HOME/pulled"
mkdir -p "$MOCK_PULLED_LOCKS"
jq -n --arg a "$a" '{nodes: {root: {inputs: {demo: "demo"}}, demo: {original: {type: "github", owner: "acme", repo: "demo", ref: "main"}, locked: {rev: $a}}}}' \
  > "$MOCK_PULLED_LOCKS/app"
export MOCK_HEAD="$b" MOCK_SCENARIO=dry-run
output=$(run_cascade demo --dry-run) && status=0 || status=$?
assert_equal "$status" "0" "the run succeeds"
assert_log_contains $'nix\tflake lock '"$HOME/work/app"' --override-input demo github:acme/demo/'"$b" \
  "asks nix about the path the pulled lock has"
assert_equal "$(grep -c 'vm/demo' "$MOCK_LOG" || true)" "0" "never asks nix about the path the old lock had"
assert_contains "$output" "demo: aaaaaaa → bbbbbbb" "reports the change on the current input"

# The pull removes the input altogether, as allod/nexus dropping vm did: the
# repository is skipped after the pull, with nothing asked of nix and no
# false 'already up to date'.
new_home input-gone
write_stale_lock "$HOME/work/app"
export MOCK_PULLED_LOCKS="$HOME/pulled"
mkdir -p "$MOCK_PULLED_LOCKS"
printf '%s\n' '{"nodes":{"root":{"inputs":{}}}}' > "$MOCK_PULLED_LOCKS/app"
export MOCK_HEAD="$b" MOCK_SCENARIO=dry-run
output=$(run_cascade demo --dry-run) && status=0 || status=$?
assert_equal "$status" "0" "the run succeeds"
assert_contains "$output" "==> app
  pulling...
  no directly pinned demo input found, skipping" "skips a repository whose pull removed the input"
assert_equal "$(grep -c 'already up to date' <<<"$output")" "0" "does not call an unexamined repository up to date"
assert_equal "$(( $(grep -c '^nix' "$MOCK_LOG" || true) + $(grep -c $'^git\tls-remote' "$MOCK_LOG" || true) ))" "0" \
  "neither reads a head nor asks nix for a path the current lock lacks"

finish_tests "flake-update-cascade post-pull-lock"
