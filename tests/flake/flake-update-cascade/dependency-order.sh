#!/usr/bin/env bash
# One run brings a chain of repositories to a consistent set of locks: each
# repository is processed after the repositories it pins, and a pin of a
# workspace repository that is behind that repository's branch head moves
# whether or not its input was named, so a lock commit the run pushes upstream
# reaches every downstream lock in the same run (allod/tools#171).
#
# The chain is leaf, mid and top: leaf pins the external demo input, mid pins
# leaf, top pins mid. The mock git serves a per-repository tracking head that
# advances when the run pushes.
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

a="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
b="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
c="cccccccccccccccccccccccccccccccccccccccc"

pin_of() { jq -r ".nodes[\"$2\"].locked.rev" "$HOME/work/$1/flake.lock"; }
head_of() { cat "$MOCK_HEADS/$1"; }
commits_in() { grep -c $'^git\t'"$1"$'\tcommit' "$MOCK_LOG" || true; }

# --- Direct mode, one run: leaf, then mid, then top, each pinning what the
# run just pushed, each commit naming only what moved there ---
new_home chain
write_chain leaf mid top
export MOCK_HEAD="$b" MOCK_SCENARIO=direct
output=$(run_cascade demo) && status=0 || status=$?
assert_equal "$status" "0" "the chained run succeeds"
assert_log_order "commits and pushes leaf, then mid, then top" \
  $'git\tleaf\tpush' $'git\tmid\tpush' $'git\ttop\tpush'
assert_equal "$(pin_of leaf demo)" "$b" "leaf takes the named input's head"
assert_equal "$(pin_of mid leaf)" "$(head_of leaf)" "mid pins the leaf head the run pushed"
assert_equal "$(pin_of top mid)" "$(head_of mid)" "top pins the mid head the run pushed"
assert_log_contains $'git\tleaf\tcommit -m flake.lock: update demo' "leaf's commit names demo"
assert_log_contains $'git\tmid\tcommit -m flake.lock: update leaf' "mid's commit names only its leaf pin"
assert_log_contains $'git\ttop\tcommit -m flake.lock: update mid' "top's commit names only its mid pin"
assert_equal "$(grep -c $'\tcommit -m flake.lock: update demo' "$MOCK_LOG")" "1" \
  "no commit names an input that moved in another repository"
assert_contains "$output" "==> mid
  pulling...
  updating leaf...
  leaf: aaaaaaa → $(head_of leaf | cut -c1-7)" "reports mid's pin change against the pushed head"

# --- Directory order puts top first; the sort, not the walk, decides ---
new_home reversed
write_chain 3-leaf 2-mid 1-top
export MOCK_HEAD="$b" MOCK_SCENARIO=direct
output=$(run_cascade demo)
assert_log_order "processes the chain leaf first whatever the directory order" \
  $'git\t3-leaf\tpush' $'git\t2-mid\tpush' $'git\t1-top\tpush'
assert_equal "$(grep '^==> ' <<<"$output" | tr '\n' ' ')" "==> 3-leaf ==> 2-mid ==> 1-top " \
  "prints the repositories in dependency order"
assert_equal "$(pin_of 2-mid 3-leaf)" "$(head_of 3-leaf)" "mid still pins the pushed leaf head"
assert_equal "$(pin_of 1-top 2-mid)" "$(head_of 2-mid)" "top still pins the pushed mid head"

# --- PR mode: leaf gets a PR; mid and top wait on it, untouched, exit 0 ---
new_home pr
write_chain leaf mid top
export MOCK_HEAD="$b" MOCK_SCENARIO=pr
output=$(run_cascade demo --pr) && status=0 || status=$?
assert_equal "$status" "0" "waiting repositories do not fail the run"
assert_contains "$output" "PR #42 updated" "leaf gets its PR"
assert_contains "$output" "==> mid
  waiting on PR #42 (leaf)" "mid waits on leaf's PR by number"
assert_contains "$output" "==> top
  waiting on PR #42 (leaf)" "top waits on the same PR through mid"
assert_equal "$(( $(commits_in mid) + $(commits_in top) ))" "0" "nothing is committed in a waiting repository"
assert_equal "$(grep -Ec $'^git\t(mid|top)\tpull' "$MOCK_LOG" || true)" "0" "a waiting repository is not even pulled"
assert_contains "$output" "Waiting on unmerged PRs:
  mid: PR #42 (leaf)
  top: PR #42 (leaf)
Re-run the same command after they merge to continue the cascade." \
  "ends by naming the waiting repositories, their PRs, and the way on"

# --- Already-merged wave: mid's pin is behind leaf's head with no new leaf
# commit, and a run naming an unrelated input still moves mid, then top ---
new_home merged
write_chain leaf mid top
printf '%s\n' "$c" > "$MOCK_HEADS/leaf"
export MOCK_HEAD="$a" MOCK_SCENARIO=direct
output=$(run_cascade demo)
assert_contains "$output" "==> leaf
  pulling...
  already up to date" "leaf has nothing to do"
assert_equal "$(pin_of mid leaf)" "$c" "mid's stale pin moves to leaf's merged head"
assert_log_contains $'git\tmid\tcommit -m flake.lock: update leaf' "mid commits the propagated pin"
assert_equal "$(pin_of top mid)" "$(head_of mid)" "top follows mid's new commit"
assert_equal "$(commits_in leaf)" "0" "leaf is not committed to"

# --- Dry run: the first wave is shown, and the rest is named as not shown ---
new_home dry-run
write_chain leaf mid top
before=$(cat "$HOME"/work/*/flake.lock | sha256sum)
export MOCK_HEAD="$b" MOCK_SCENARIO=dry-run
output=$(run_cascade demo --dry-run) && status=0 || status=$?
assert_equal "$status" "0" "the dry run succeeds"
assert_contains "$output" "demo: aaaaaaa → bbbbbbb" "shows leaf's change"
assert_contains "$output" "==> mid
  pulling...
  leaf: leaf moves in this run; a dry run cannot show the revision it will pin
  no other changes (dry-run)" "says mid's pin of leaf cannot be shown"
assert_contains "$output" "  mid: mid moves in this run; a dry run cannot show the revision it will pin" \
  "carries the notice through to top"
assert_contains "$output" "Dry run: mid, top also take the commits above once they are pushed; a dry run cannot show the revisions they will pin." \
  "ends by naming the repositories a real run also moves"
assert_equal "$(cat "$HOME"/work/*/flake.lock | sha256sum)" "$before" "changes no lock"
assert_equal "$(grep -Ec $'^git\t(leaf|mid|top)\t(commit|push)' "$MOCK_LOG" || true)" "0" "commits and pushes nothing"

# --- A cycle is a preflight error naming both repositories ---
new_home cycle
write_declared_lock "$HOME/work/x" y '{"type":"git","url":"https://forge.anarch.diy/acme/y.git"}'
write_declared_lock "$HOME/work/y" x '{"type":"git","url":"https://forge.anarch.diy/acme/x.git"}'
export MOCK_HEAD="$b" MOCK_SCENARIO=direct
output=$(run_cascade demo 2>&1) && status=0 || status=$?
assert_equal "$status" "1" "a cycle fails preflight"
assert_contains "$output" "Pre-flight checks failed — no changes made." "a cycle stops the run before anything is touched"
assert_contains "$output" "  x: dependency cycle: x → y → x" "names the cycle from x"
assert_contains "$output" "  y: dependency cycle: y → x → y" "names the cycle from y"
assert_equal "$(grep -c '^nix' "$MOCK_LOG" || true)" "0" "does not invoke Nix on a cycle"
assert_equal "$(grep -Ec $'^git\t(x|y)\tpull' "$MOCK_LOG" || true)" "0" "does not pull on a cycle"

# --- A repository with only workspace pins, all current, is examined and
# left alone; one with neither a named input nor a workspace pin is skipped ---
new_home current
write_chain leaf mid top
write_direct_lock "$HOME/work/other"
printf '%s\n' '{"nodes":{"root":{"inputs":{"unrelated":"unrelated"}},"unrelated":{"original":{"type":"github","owner":"acme","repo":"unrelated","ref":"main"},"locked":{"rev":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}' > "$HOME/work/other/flake.lock"
export MOCK_HEAD="$a" MOCK_SCENARIO=current
output=$(run_cascade demo)
assert_contains "$output" "==> mid
  pulling...
  already up to date" "a current workspace pin is read and left alone"
assert_contains "$output" "==> other
  no directly pinned demo input found, skipping" "a repository pinning nothing in the workspace is skipped as before"
assert_equal "$(grep -c '^nix' "$MOCK_LOG" || true)" "0" "makes no nix call when nothing moved"

finish_tests "flake-update-cascade dependency-order"
