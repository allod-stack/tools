#!/usr/bin/env bash
# A repository whose origin is outside the forge is never touched, in any mode.
#
# The cascade walks every repository under the workspace root, and a
# third-party checkout living there has an origin the operator holds no push
# credential for and no intention of publishing to. Without this gate the tool
# committed a lock update to such a checkout's default branch and ran git push
# against github.com, which stopped at a username prompt one credential short
# of an unreviewed push to someone else's repository. The rule is the one the
# protected-refs-policy hook already enforces on push: the forge is always
# allowed, anything else needs an entry in allowed-external-remotes.
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

external_notice="origin https://github.com/acme/external-app.git is not the forge and not listed in ~/.config/git/allowed-external-remotes, skipping"

for mode in "" --dry-run --pr; do
  new_home "external${mode}"
  write_direct_lock "$HOME/work/external-app"
  before=$(sha256sum "$HOME/work/external-app/flake.lock")
  export MOCK_SCENARIO=pr
  output=$(bash "$ROOT/flake/flake-update-cascade" demo $mode)
  assert_contains "$output" "$external_notice" \
    "names the external origin and skips it (${mode:-direct})"
  assert_equal "$(grep -Ec $'^nix\t.*external-app' "$MOCK_LOG" || true)" "0" \
    "never invokes Nix on the external repository (${mode:-direct})"
  assert_equal "$(grep -Ec $'^git\texternal-app\t(pull|add|commit|checkout|push)' "$MOCK_LOG" || true)" "0" \
    "never pulls, commits, checks out or pushes the external repository (${mode:-direct})"
  assert_equal "$(sha256sum "$HOME/work/external-app/flake.lock")" "$before" \
    "leaves the external repository's lock untouched (${mode:-direct})"
done

# The gate can open: an allowlist entry admits an external origin, and the
# forge itself never needs one. Both proceed to an update in dry-run mode.
new_home allowlist
write_direct_lock "$HOME/work/allowed-app"
write_direct_lock "$HOME/work/app"
printf '%s\n' "github.com/acme/allowed-app" > "$HOME/.config/git/allowed-external-remotes"
export MOCK_SCENARIO=dry-run
output=$(bash "$ROOT/flake/flake-update-cascade" demo --dry-run)
assert_log_contains $'nix\tflake update demo --flake '"$HOME/work/allowed-app" \
  "an allowlisted external origin is updated"
assert_log_contains $'nix\tflake update demo --flake '"$HOME/work/app" \
  "a forge origin is updated without an allowlist entry"

# No origin at all: nothing to push to, so nothing is touched either.
new_home no-origin
write_direct_lock "$HOME/work/no-origin-app"
export MOCK_SCENARIO=dry-run
output=$(bash "$ROOT/flake/flake-update-cascade" demo --dry-run)
assert_contains "$output" "no origin remote, skipping" \
  "skips a repository with no origin remote"
assert_equal "$(grep -Ec $'^nix\t.*no-origin-app' "$MOCK_LOG" || true)" "0" \
  "never invokes Nix on a repository with no origin"

finish_tests "flake-update-cascade external-remote"
