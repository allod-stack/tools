#!/usr/bin/env bash
# Hermetic tests for verify-session. A fake forge, git, allod and pi stand on
# PATH; no process reaches the real forge, a real repository, or an agent.
# The refusal paths are the priority, because a seat check that passes
# silently is the failure that matters.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
UNDER_TEST="$ROOT/verify-session/verify-session"
TMP=$(mktemp -d)
FAKE_DIR="$TMP/bin"

cleanup() {
  rm -rf "$TMP"
}
trap cleanup EXIT

export HOME="$TMP/home"
mkdir -p "$HOME" "$FAKE_DIR"
export FAKE_DIR
PATH="$FAKE_DIR:$PATH"
export PATH

VERIFIER_TOKEN="$TMP/verifier-token"
AMBIENT_TOKEN="$TMP/ambient-token"
printf 'verifier-token\n' > "$VERIFIER_TOKEN"
printf 'ambient-token\n' > "$AMBIENT_TOKEN"
chmod 600 "$VERIFIER_TOKEN" "$AMBIENT_TOKEN"

# --- fakes ---

# Runtime-written fakes are beyond patchShebangs' reach, and a Nix build
# sandbox has no /usr/bin/env, so resolve the interpreter now.
write_fake() {
  local path="$1"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat
  } > "$path"
  chmod +x "$path"
}

write_fake "$FAKE_DIR/forge" <<'SH'
set -euo pipefail
printf 'forge\t%s\n' "$*" >> "$FAKE_DIR/forge.log"
if [[ -n "${FAKE_FORGE_AUTH_FAIL:-}" && "${1:-} ${2:-}" == "auth status" ]]; then
  printf 'forge: authentication failed\n' >&2
  exit 1
fi
case "${1:-} ${2:-}" in
  "auth status")
    printf 'Authenticated as %s (%s)\n' "${FAKE_FORGE_ACCOUNT:-verifier}" "${FORGE_TOKEN_FILE:-}"
    ;;
  "pr view")
    printf '{"author":{"login":"%s"},"headRefName":"%s"}\n' \
      "${FAKE_PR_AUTHOR:-author-agent}" "${FAKE_HEAD_REF:-agent/pr-branch}"
    ;;
  "pr snapshot")
    printf '{"schema_version":1,"pull_request":{"number":7},"base":{"repository":{"clone_url":"%s"},"ref":"master","sha":"base-sha"},"head":{"repository":{"clone_url":"%s"},"ref":"%s","sha":"%s"}}\n' \
      "${FAKE_CLONE_URL:-https://forge.example/acme/widget.git}" \
      "${FAKE_CLONE_URL:-https://forge.example/acme/widget.git}" \
      "${FAKE_HEAD_REF:-agent/pr-branch}" \
      "${FAKE_HEAD_SHA:-0123456789abcdef0123456789abcdef01234567}"
    ;;
  *)
    printf 'fake forge: unhandled: %s\n' "$*" >&2
    exit 1
    ;;
esac
SH

write_fake "$FAKE_DIR/allod" <<'SH'
set -euo pipefail
printf 'allod\t%s\n' "$*" >> "$FAKE_DIR/allod.log"
if [[ "${1:-} ${2:-}" == "change list" ]]; then
  if [[ -n "${FAKE_ALLOD_LIST_FAIL:-}" ]]; then
    printf 'allod: forge unreachable\n' >&2
    exit 1
  fi
  if [[ -n "${FAKE_CHANGE_LIST:-}" ]]; then
    printf '%s\n' "$FAKE_CHANGE_LIST"
  fi
  exit 0
fi
printf 'fake allod: unhandled: %s\n' "$*" >&2
exit 1
SH

write_fake "$FAKE_DIR/git" <<'SH'
set -euo pipefail
printf 'git\t%s\n' "$*" >> "$FAKE_DIR/git.log"
if [[ "${1:-}" == "clone" ]]; then
  last=""
  for last in "$@"; do :; done
  mkdir -p "$last"
fi
exit 0
SH

write_fake "$FAKE_DIR/pi" <<'SH'
set -euo pipefail
printf 'pi\t%s\n' "$*" >> "$FAKE_DIR/pi.log"
printf '%s\n' "${FORGE_TOKEN_FILE:-}" > "$FAKE_DIR/pi.token-file"
printf '%s\n' "$PWD" > "$FAKE_DIR/pi.pwd"
prompt=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -p|--print)
      prompt="${2:-}"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
printf '%s' "$prompt" > "$FAKE_DIR/pi.prompt"
if [[ -n "${FAKE_PI_FAIL:-}" ]]; then
  printf 'pi failed\n' >&2
  exit 1
fi
printf 'verifier ran\n'
printf 'PROOF_COMMENT=%s\n' "${FAKE_PROOF_URL:-https://forge.example/acme/widget/pulls/305#issuecomment-1}"
SH

# --- harness ---

CAPTURE_OUTPUT=""
CAPTURE_STATUS=0
test_number=0

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

assert_contains() {
  local actual="$1" expected="$2" description="$3"
  if [[ "$actual" == *"$expected"* ]]; then
    pass "$description"
  else
    fail "$description" "expected output to contain: $expected" "actual output:" "$actual"
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

assert_file_exists() {
  local file="$1" description="$2"
  if [[ -f "$file" ]]; then
    pass "$description"
  else
    fail "$description" "missing file: $file"
  fi
}

assert_file_absent() {
  local file="$1" description="$2"
  if [[ ! -e "$file" ]]; then
    pass "$description"
  else
    fail "$description" "unexpected path: $file"
  fi
}

capture() {
  set +e
  CAPTURE_OUTPUT=$("$@" 2>&1)
  CAPTURE_STATUS=$?
  set -e
}

reset_fakes() {
  rm -f "$FAKE_DIR/forge.log" "$FAKE_DIR/allod.log" "$FAKE_DIR/git.log" \
    "$FAKE_DIR/pi.log" "$FAKE_DIR/pi.prompt" "$FAKE_DIR/pi.token-file" \
    "$FAKE_DIR/pi.pwd"
  unset FAKE_FORGE_ACCOUNT FAKE_PR_AUTHOR FAKE_HEAD_REF FAKE_HEAD_SHA \
    FAKE_CLONE_URL FAKE_CHANGE_LIST FAKE_ALLOD_LIST_FAIL FAKE_FORGE_AUTH_FAIL \
    FAKE_PI_FAIL FAKE_PROOF_URL
  unset FORGE_TOKEN_FILE
}

# --- cases ---

reset_fakes
capture "$UNDER_TEST" --help
assert_status 0 "help exits zero"
assert_contains "$CAPTURE_OUTPUT" "Usage:" "help prints usage"
assert_contains "$CAPTURE_OUTPUT" "--token-file" "help names the token flag"
assert_contains "$CAPTURE_OUTPUT" "owner/repo#number" "help names the pull request form"

reset_fakes
capture "$UNDER_TEST"
assert_status 1 "a missing pull request is refused"
assert_contains "$CAPTURE_OUTPUT" "expected one pull request" "the refusal names the missing argument"

reset_fakes
capture "$UNDER_TEST" acme/widget#7
assert_status 1 "a missing token is refused"
assert_contains "$CAPTURE_OUTPUT" "no verifier token" "the refusal names the missing token"

reset_fakes
export FAKE_FORGE_ACCOUNT=author-agent
export FAKE_PR_AUTHOR=author-agent
capture "$UNDER_TEST" --token-file "$VERIFIER_TOKEN" acme/widget#7
assert_status 2 "the author's credential is refused"
assert_contains "$CAPTURE_OUTPUT" "author" "the refusal names the author seat"
assert_file_absent "$FAKE_DIR/pi.log" "no verifier runs on a refused seat"
assert_file_absent "$FAKE_DIR/git.log" "no scratch clone is cut on a refused seat"

reset_fakes
export FAKE_FORGE_ACCOUNT=verifier
export FAKE_PR_AUTHOR=author-agent
export FAKE_HEAD_REF=agent/pr-branch
export FAKE_CHANGE_LIST=$'acme/widget\t/home/x/changes/widget-agent-pr-branch-abc\tagent/pr-branch\topen'
capture "$UNDER_TEST" --token-file "$VERIFIER_TOKEN" acme/widget#7
assert_status 2 "a live clone of the branch is refused"
assert_contains "$CAPTURE_OUTPUT" "holds a clone" "the refusal names the live clone"
assert_file_absent "$FAKE_DIR/pi.log" "no verifier runs while a clone holds the branch"

# Every seat check must fail closed.
reset_fakes
export FAKE_PR_AUTHOR=author-agent
export FAKE_ALLOD_LIST_FAIL=1
capture "$UNDER_TEST" --token-file "$VERIFIER_TOKEN" acme/widget#7
assert_status 3 "an unreadable clone list is refused"
assert_contains "$CAPTURE_OUTPUT" "seat check failed: allod change list" "the refusal names the failed list"
assert_file_absent "$FAKE_DIR/pi.log" "no verifier runs when the clone list cannot be read"

reset_fakes
export FAKE_FORGE_AUTH_FAIL=1
capture "$UNDER_TEST" --token-file "$VERIFIER_TOKEN" acme/widget#7
assert_status 3 "a failed auth check is refused"
assert_contains "$CAPTURE_OUTPUT" "seat check failed: forge auth status" "the refusal names the failed auth check"
assert_file_absent "$FAKE_DIR/pi.log" "no verifier runs when the seat cannot be confirmed"

reset_fakes
export FAKE_FORGE_ACCOUNT=verifier
export FAKE_PR_AUTHOR=author-agent
export FAKE_HEAD_REF=agent/pr-branch
export FAKE_HEAD_SHA=0123456789abcdef0123456789abcdef01234567
export FAKE_CLONE_URL=https://forge.example/acme/widget.git
export FORGE_TOKEN_FILE="$AMBIENT_TOKEN"
capture "$UNDER_TEST" --token-file "$VERIFIER_TOKEN" acme/widget#7
assert_status 0 "the verifier seat runs"
assert_contains "$CAPTURE_OUTPUT" "verifier ran" "the verifier's output is the script's output"
assert_contains "$CAPTURE_OUTPUT" "proof-comment: https://forge.example/acme/widget/pulls/305#issuecomment-1" \
  "the proof comment link is reported"
assert_file_exists "$FAKE_DIR/pi.log" "the verifier process is invoked"
assert_file_exists "$FAKE_DIR/git.log" "a scratch clone is cut"

git_log=$(cat "$FAKE_DIR/git.log")
assert_contains "$git_log" "refs/pull/7/head" "the pull request ref is fetched"
assert_contains "$git_log" "0123456789abcdef0123456789abcdef01234567" "the head is checked out by hash"
assert_contains "$git_log" "clone" "the forge remote is cloned"

pi_log=$(cat "$FAKE_DIR/pi.log")
assert_contains "$pi_log" "--no-session" "the verifier runs headless without a session"
assert_contains "$pi_log" "-p" "the verifier runs in print mode"

assert_equal "$(cat "$FAKE_DIR/pi.token-file")" "$VERIFIER_TOKEN" \
  "the argument token file overrides the ambient one for the verifier"

clone_target=$(awk -F'\t' '$2 ~ /^clone/ { n = split($2, a, " "); print a[n] }' "$FAKE_DIR/git.log")
assert_equal "$(cat "$FAKE_DIR/pi.pwd")" "$clone_target" \
  "the verifier runs inside the scratch clone, not the caller's directory"

prompt=$(cat "$FAKE_DIR/pi.prompt")
assert_contains "$prompt" "verify-merge" "the brief names the skill"
assert_contains "$prompt" "acme/widget#7" "the brief names the pull request"
assert_contains "$prompt" "ledger" "the brief names the ledger lookup"
assert_contains "$prompt" "Validation" "the brief names the no-map validation fallback"
assert_contains "$prompt" "score" "the brief names the score-first rule"
assert_contains "$prompt" "PROOF_COMMENT" "the brief names the proof-comment contract"

reset_fakes
export FAKE_FORGE_ACCOUNT=verifier
export FAKE_PR_AUTHOR=author-agent
capture "$UNDER_TEST" --token-file "$VERIFIER_TOKEN" --timeout --help acme/widget#7
assert_status 6 "an option-shaped timeout is refused, not run"
assert_contains "$CAPTURE_OUTPUT" "verifier exited" "the refusal reports the failed timeout"
assert_file_absent "$FAKE_DIR/pi.log" "no verifier runs with an invalid timeout"

reset_fakes
export FAKE_FORGE_ACCOUNT=verifier
export FAKE_PR_AUTHOR=author-agent
export FAKE_PI_FAIL=1
capture "$UNDER_TEST" --token-file "$VERIFIER_TOKEN" acme/widget#7
assert_status 6 "a failed verifier is reported"
assert_contains "$CAPTURE_OUTPUT" "verifier exited" "the refusal names the failed verifier"

printf '\nTests run: %d\n' "$test_number"
printf 'All %d verify-session tests passed.\n' "$test_number"
