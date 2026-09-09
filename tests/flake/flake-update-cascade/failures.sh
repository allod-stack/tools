#!/usr/bin/env bash
# Every failure that can interrupt one repository's update, in each mode that
# has it, must be reported, leave the checkout restored, remove the temporary
# lock copy, and let the run go on to a status of 1. A failure the tool does
# not guard ends the run with the failing command's status instead.
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

# run_status captures output and exit status without ending the suite.
run_status() {
  RUN_OUTPUT=$(run_cascade "$@" 2>&1) && RUN_STATUS=0 || RUN_STATUS=$?
}

assert_no_temp_locks() {
  local description="$1"
  if [[ -e /tmp/app.flake.lock.old || -e /tmp/app.flake.lock.new ]]; then
    fail "$description" "temporary lock copy left behind: $(ls /tmp/app.flake.lock.* 2>/dev/null)"
  else
    pass "$description"
  fi
}

assert_log_count() {
  local pattern="$1" expected="$2" description="$3"
  assert_equal "$(grep -Ec "$pattern" "$MOCK_LOG" || true)" "$expected" "$description"
}

# --- Nix update failure, all three modes ---
for mode in "" --dry-run --pr; do
  new_home "update-fail$mode"
  write_direct_lock "$HOME/work/app"
  before=$(sha256sum "$HOME/work/app/flake.lock")
  export MOCK_SCENARIO=direct MOCK_FAIL_NIX="flake update"
  run_status demo $mode
  assert_equal "$RUN_STATUS" "1" "nix update failure ends with status 1 (${mode:-direct})"
  assert_contains "$RUN_OUTPUT" "nix flake update failed, skipping" \
    "reports the failed update (${mode:-direct})"
  assert_log_count $'^git\tapp\t(add|commit|push|checkout -B)' 0 \
    "never stages, commits, branches or pushes after a failed update (${mode:-direct})"
  assert_no_temp_locks "removes the temporary lock copy after a failed update (${mode:-direct})"
  if [[ "$mode" == --dry-run ]]; then
    assert_equal "$(sha256sum "$HOME/work/app/flake.lock")" "$before" \
      "leaves the lock file untouched after a failed dry-run update"
  else
    assert_log_contains $'git\tapp\tcheckout -- flake.lock' \
      "restores the lock file after a failed update (${mode:-direct})"
  fi
done
unset MOCK_FAIL_NIX

# --- Evaluation failure, all three modes ---
for mode in "" --dry-run --pr; do
  new_home "eval-fail$mode"
  write_direct_lock "$HOME/work/app"
  export MOCK_SCENARIO=direct MOCK_FAIL_NIX="flake metadata"
  run_status demo $mode
  assert_equal "$RUN_STATUS" "1" "evaluation failure ends with status 1 (${mode:-direct})"
  if [[ "$mode" == --dry-run ]]; then
    assert_contains "$RUN_OUTPUT" "broken evaluation with updated lock, skipping" \
      "reports the broken evaluation (dry-run)"
  else
    assert_contains "$RUN_OUTPUT" "broken evaluation after update — reverting flake.lock" \
      "reports the broken evaluation and the revert (${mode:-direct})"
    assert_log_contains $'git\tapp\tcheckout -- flake.lock' \
      "reverts the lock file after a broken evaluation (${mode:-direct})"
  fi
  assert_log_count $'^git\tapp\t(add|commit|push|checkout -B)' 0 \
    "never stages, commits, branches or pushes after a broken evaluation (${mode:-direct})"
  assert_no_temp_locks "removes the temporary lock copy after a broken evaluation (${mode:-direct})"
done
unset MOCK_FAIL_NIX

# --- Direct mode: commit blocked, push rejected ---
new_home commit-fail
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=direct MOCK_FAIL_GIT="commit -m flake.lock: update demo"
run_status demo
assert_equal "$RUN_STATUS" "1" "a blocked commit ends with status 1"
assert_contains "$RUN_OUTPUT" "commit failed (hook blocked?) — reverting" \
  "reports the blocked commit"
assert_log_contains $'git\tapp\trestore --staged flake.lock' "unstages the lock file after a blocked commit"
assert_log_contains $'git\tapp\tcheckout -- flake.lock' "restores the lock file after a blocked commit"
assert_log_count $'^git\tapp\tpush' 0 "never pushes after a blocked commit"

new_home push-fail-direct
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=direct MOCK_FAIL_GIT="push"
run_status demo
assert_equal "$RUN_STATUS" "1" "a rejected direct push ends with status 1"
assert_contains "$RUN_OUTPUT" "push failed" "reports the rejected push (direct)"
assert_contains "$RUN_OUTPUT" "demo: aaaaaaa → bbbbbbb" "reports the revision change before the push (direct)"
assert_log_contains $'git\tapp\tcommit -m flake.lock: update demo' "commits before the rejected push (direct)"
assert_no_temp_locks "removes the temporary lock copy before pushing (direct)"

# --- PR mode: push rejected, stale tracking ref, first-time PR creation ---
new_home push-fail-pr
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=pr MOCK_FAIL_GIT="push --force-with-lease origin agent/flake-update-demo"
run_status demo --pr
assert_equal "$RUN_STATUS" "1" "a rejected PR push ends with status 1"
assert_contains "$RUN_OUTPUT" "push failed" "reports the rejected push (PR)"
assert_log_contains $'git\tapp\tcheckout master' "returns to the default branch after a rejected push"
assert_log_contains $'git\tapp\tcheckout -- flake.lock' "restores the lock file after a rejected push"
assert_log_count $'^forge\t' 0 "never consults forge after a rejected push"
unset MOCK_FAIL_GIT

new_home stale-ref
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=pr MOCK_FAIL_GIT="fetch origin agent/flake-update-demo"
run_status demo --pr
assert_equal "$RUN_STATUS" "0" "a missing remote branch does not fail the run"
assert_log_contains $'git\tapp\tupdate-ref -d refs/remotes/origin/agent/flake-update-demo' \
  "drops the stale tracking ref when the remote branch is gone"
assert_log_contains $'git\tapp\tpush --force-with-lease origin agent/flake-update-demo' \
  "still pushes with lease protection after dropping the stale ref"
unset MOCK_FAIL_GIT

new_home new-pr
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=pr MOCK_FORGE_NO_PR=1 MOCK_FORGE_CREATE_URL="https://forge.example/acme/app/pulls/7"
run_status demo --pr
assert_equal "$RUN_STATUS" "0" "creating a PR succeeds"
assert_log_contains $'forge\t-R acme/app pr create --title flake.lock: update demo --head agent/flake-update-demo --base master --body Automated flake.lock update for inputs `demo`.' \
  "creates the PR with the combined-update title and body"
assert_contains "$RUN_OUTPUT" "  https://forge.example/acme/app/pulls/7" "prints the new PR URL"
assert_log_contains $'git\tapp\tcheckout master' "returns to the default branch after creating the PR"

new_home create-fail
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=pr MOCK_FORGE_NO_PR=1 MOCK_FORGE_CREATE_FAIL=1
run_status demo --pr
assert_equal "$RUN_STATUS" "1" "a failed PR creation ends with status 1"
assert_contains "$RUN_OUTPUT" "PR creation failed" "reports the failed PR creation"
assert_log_contains $'git\tapp\tcheckout master' "returns to the default branch after a failed PR creation"
assert_log_contains $'git\tapp\tcheckout -- flake.lock' "restores the lock file after a failed PR creation"
unset MOCK_FORGE_CREATE_FAIL

new_home new-pr-no-url
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=pr MOCK_FORGE_NO_PR=1 MOCK_FORGE_CREATE_URL=""
run_status demo --pr
assert_contains "$RUN_OUTPUT" "PR created (could not fetch URL)" \
  "reports a created PR whose URL forge did not print"
unset MOCK_FORGE_CREATE_URL MOCK_FORGE_NO_PR

# --- Pull failure skips the repository before any update ---
new_home pull-fail
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=direct MOCK_FAIL_GIT="pull"
run_status demo
assert_equal "$RUN_STATUS" "1" "a failed pull ends with status 1"
assert_contains "$RUN_OUTPUT" "pull failed, skipping" "reports the failed pull"
assert_log_count $'^nix\t' 0 "never invokes Nix after a failed pull"
unset MOCK_FAIL_GIT

# --- A failure the tool does not guard ends the run with that status ---
new_home restore-fail
write_direct_lock "$HOME/work/app"
export MOCK_SCENARIO=direct MOCK_FAIL_NIX="flake update" MOCK_FAIL_GIT="checkout -- flake.lock"
run_status demo
assert_equal "$RUN_STATUS" "1" "a failed restore ends the run with the restore's status"
assert_contains "$RUN_OUTPUT" "nix flake update failed, skipping" \
  "reports the update failure before the restore fails"
unset MOCK_FAIL_NIX MOCK_FAIL_GIT

# --- One failure does not stop the next repository ---
new_home continue
write_direct_lock "$HOME/work/app"
write_direct_lock "$HOME/work/other"
export MOCK_SCENARIO=direct MOCK_FAIL_GIT="push"
run_status demo
assert_equal "$RUN_STATUS" "1" "one failed repository fails the run"
assert_equal "$(grep -c '^==> ' <<<"$RUN_OUTPUT")" "2" "both repositories are processed"
assert_log_contains $'git\tother\tcommit -m flake.lock: update demo' \
  "the second repository is still updated after the first fails"
unset MOCK_FAIL_GIT

finish_tests "flake-update-cascade failures"
