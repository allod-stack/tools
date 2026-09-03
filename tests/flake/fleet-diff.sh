#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

export HOME="$TMP/home"
mkdir -p "$HOME" "$TMP/bin"

# A composition root whose lock has a transitive input (archetypes/vm), a direct
# input (inventory), a root-level follows (nixpkgs), and one input whose source
# is not a git remote (pinned). Only the graph shape and each node's recorded
# source matter — nix never reads this, the tool's override resolution does.
DEPLOY="$TMP/deploy"
mkdir -p "$DEPLOY"
: > "$DEPLOY/flake.nix"
cat > "$DEPLOY/flake.lock" <<'EOF'
{
  "nodes": {
    "root": {
      "inputs": {
        "archetypes": "archetypes",
        "inventory": "inventory",
        "nixpkgs": ["archetypes", "nixpkgs"],
        "pinned": "pinned"
      }
    },
    "archetypes": {
      "inputs": {"vm": "vm", "nixpkgs": ["archetypes", "vm", "nixpkgs"]},
      "locked": {
        "type": "git",
        "url": "https://forge.example/allod/archetypes.git",
        "rev": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
      }
    },
    "vm": {
      "inputs": {"nixpkgs": "nixpkgs"},
      "locked": {
        "type": "git",
        "url": "https://forge.example/allod/vm.git",
        "rev": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
      }
    },
    "nixpkgs": {
      "locked": {
        "type": "github",
        "owner": "NixOS",
        "repo": "nixpkgs",
        "rev": "cccccccccccccccccccccccccccccccccccccccc"
      }
    },
    "inventory": {
      "locked": {
        "type": "git",
        "url": "https://forge.example/allod/inventory.git",
        "rev": "dddddddddddddddddddddddddddddddddddddddd"
      }
    },
    "pinned": {
      "locked": {
        "type": "path",
        "path": "/nix/store/eeee-source",
        "narHash": "sha256-eeee"
      }
    }
  }
}
EOF

NOT_A_FLAKE="$TMP/not-a-flake"
mkdir -p "$NOT_A_FLAKE"

NO_LOCK="$TMP/no-lock"
mkdir -p "$NO_LOCK"
: > "$NO_LOCK/flake.nix"

export MOCK_LOG="$TMP/commands.log"
: > "$MOCK_LOG"

# --- Stubs ---
#
# nix returns controlled drvPaths per machine, so no real evaluation happens.
#   MOCK_MACHINES  space-separated fleet
#   MOCK_CHANGED   space-separated machines whose candidate drvPath differs
#   MOCK_FAIL      machine whose evaluation fails
cat > "$TMP/bin/nix" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'nix\t%s\n' "$*" >> "$MOCK_LOG"

[[ "$1" == "eval" ]] || { echo "unexpected nix invocation: $*" >&2; exit 1; }
attr="$2"

overridden=false
for arg in "$@"; do
  [[ "$arg" == "--override-input" ]] && overridden=true
done

# Fleet enumeration.
if [[ "$attr" == *"#nixosConfigurations" ]]; then
  printf '%s' "${MOCK_MACHINES:-}" \
    | tr ' ' '\n' \
    | jq -R . \
    | jq -s -c .
  exit 0
fi

# Per-machine toplevel drvPath.
if [[ "$attr" == *"#nixosConfigurations."*".config.system.build.toplevel.drvPath" ]]; then
  machine="${attr#*#nixosConfigurations.}"
  machine="${machine%%.config.*}"
  if [[ " ${MOCK_FAIL:-} " == *" $machine "* ]]; then
    echo "error: simulated evaluation failure for $machine" >&2
    exit 1
  fi
  if [[ "$overridden" == true && " ${MOCK_CHANGED:-} " == *" $machine "* ]]; then
    printf '/nix/store/candidate-%s.drv' "$machine"
  else
    printf '/nix/store/baseline-%s.drv' "$machine"
  fi
  exit 0
fi

echo "unexpected nix eval attribute: $attr" >&2
exit 1
EOF

# git answers two questions: whether the checkout's lock is dirty, and what refs
# a remote has.
#   MOCK_DIRTY          working tree has an uncommitted flake.lock
#   MOCK_LSREMOTE       ls-remote listing, tab-separated sha and ref per line
#   MOCK_LSREMOTE_FAIL  the remote cannot be reached
cat > "$TMP/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'git\t%s\n' "$*" >> "$MOCK_LOG"

case "$1" in
  -C)
    shift 2
    case "$*" in
      "status --porcelain -- flake.nix flake.lock")
        [[ "${MOCK_DIRTY:-}" == true ]] && printf ' M flake.lock\n'
        exit 0
        ;;
    esac
    ;;
  ls-remote)
    if [[ "${MOCK_LSREMOTE_FAIL:-}" == true ]]; then
      echo "fatal: could not read from remote repository" >&2
      exit 128
    fi
    printf '%s' "${MOCK_LSREMOTE:-}"
    exit 0
    ;;
esac

echo "unexpected git invocation: $*" >&2
exit 1
EOF

chmod +x "$TMP/bin/nix" "$TMP/bin/git"
export PATH="$TMP/bin:$PATH"

export MOCK_MACHINES="allod-canary allod-dev allod-work"
export MOCK_CHANGED=""
export MOCK_FAIL=""
export MOCK_DIRTY=false
export MOCK_LSREMOTE_FAIL=false

# One listing serves every remote the fixture names. It carries the shapes that
# matter: a plain branch tip, a commit that is the tip of both a branch and a
# tag, an annotated tag whose commit only appears on the peeled entry, and two
# commits sharing a seven-character prefix.
MOCK_LSREMOTE=$(printf '%s\n' \
  $'0123456789abcdef0123456789abcdef01234567\tHEAD' \
  $'0123456789abcdef0123456789abcdef01234567\trefs/heads/master' \
  $'89abcdef0123456789abcdef0123456789abcdef\trefs/heads/agent/split' \
  $'7777777777777777777777777777777777777777\trefs/tags/release' \
  $'7777777777777777777777777777777777777777\trefs/heads/agent/tagged' \
  $'1111111111111111111111111111111111111111\trefs/tags/v1' \
  $'fedcba9876543210fedcba9876543210fedcba98\trefs/tags/v1^{}' \
  $'abc1234000000000000000000000000000000000\trefs/heads/collide-one' \
  $'abc1234fffffffffffffffffffffffffffffffff\trefs/heads/collide-two')
export MOCK_LSREMOTE

OVERRIDE='archetypes/vm=git+https://forge.example/allod/vm.git?ref=refs/heads/agent/split&rev=0123456789abcdef0123456789abcdef01234567'

test_number=0

pass() {
  test_number=$((test_number + 1))
  printf '✅ %d - %s\n' "$test_number" "$1"
}

fail() {
  test_number=$((test_number + 1))
  printf '❌ %d - %s\n' "$test_number" "$1" >&2
  shift
  printf '%s\n' "$@" >&2
  exit 1
}

assert_contains() {
  local actual="$1" expected="$2" description="$3"
  if [[ "$actual" == *"$expected"* ]]; then
    pass "$description"
  else
    fail "$description" "expected output to contain: $expected" "actual output:" "$actual"
  fi
}

assert_not_contains() {
  local actual="$1" unexpected="$2" description="$3"
  if [[ "$actual" != *"$unexpected"* ]]; then
    pass "$description"
  else
    fail "$description" "expected output not to contain: $unexpected" "actual output:" "$actual"
  fi
}

assert_equal() {
  local actual="$1" expected="$2" description="$3"
  if [[ "$actual" == "$expected" ]]; then
    pass "$description"
  else
    fail "$description" "expected: $expected" "actual: $actual"
  fi
}

# Run fleet-diff, capturing combined output in OUTPUT and status in STATUS.
OUTPUT=""
STATUS=0
run() {
  : > "$MOCK_LOG"
  STATUS=0
  OUTPUT=$(bash "$ROOT/flake/fleet-diff" "$@" 2>&1) || STATUS=$?
}

# --- No machine changes ---

export MOCK_CHANGED=""
run "$DEPLOY" --override "$OVERRIDE"
assert_equal "$STATUS" "0" "report-only run over an inert change exits 0"
assert_contains "$OUTPUT" "fleet-diff: 3 machines in $DEPLOY" \
  "names the checkout and the fleet size"
assert_contains "$OUTPUT" "allod-dev              unchanged" \
  "reports an unaffected machine as unchanged"
assert_contains "$OUTPUT" "0 of 3 machines change." \
  "summarises an inert change"
assert_not_contains "$OUTPUT" "CHANGES" \
  "prints no CHANGES row when nothing changes"
assert_contains "$OUTPUT" "No expectation declared — report only" \
  "says nothing was asserted when no expectation was given"
assert_contains "$OUTPUT" "Scope: the fleet this checkout composes" \
  "carries the fleet-scope caveat into its own output"

# Per-machine sequential evaluation: two evals per machine plus one enumeration,
# and never a whole-flake check.
eval_count=$(grep -c $'^nix\teval' "$MOCK_LOG")
assert_equal "$eval_count" "7" "evaluates each machine twice, plus one enumeration"
assert_equal "$(grep -c 'flake check' "$MOCK_LOG" || true)" "0" \
  "never runs nix flake check over the whole flake"
assert_contains "$(cat "$MOCK_LOG")" \
  "--override-input archetypes/vm git+https://forge.example/allod/vm.git?ref=refs/heads/agent/split&rev=0123456789abcdef0123456789abcdef01234567" \
  "passes the override URL to nix as a single quoted argument"
assert_equal "$(grep -c 'ls-remote' "$MOCK_LOG" || true)" "0" \
  "a whole flake URL is passed through without consulting the remote"

# --- One machine changes ---

export MOCK_CHANGED="allod-work"
run "$DEPLOY" --override "$OVERRIDE"
assert_equal "$STATUS" "0" "report-only run exits 0 even when a machine changes"
assert_contains "$OUTPUT" "allod-work             CHANGES" \
  "reports an affected machine as CHANGES"
assert_contains "$OUTPUT" "allod-dev              unchanged" \
  "still reports the unaffected machines"
assert_contains "$OUTPUT" "1 of 3 machines change." \
  "counts the changed machines"

# --- Expectation matches ---

export MOCK_CHANGED=""
run "$DEPLOY" --override "$OVERRIDE" --expect-none
assert_equal "$STATUS" "0" "--expect-none passes when no machine changes"
assert_contains "$OUTPUT" "Expectation matches." \
  "confirms a matched land-inert expectation"
assert_contains "$OUTPUT" "expectation:  no machine changes" \
  "echoes the declared expectation"

export MOCK_CHANGED="allod-dev allod-work"
run "$DEPLOY" --override "$OVERRIDE" --expect allod-dev,allod-work
assert_equal "$STATUS" "0" "--expect passes when the computed set matches exactly"
assert_contains "$OUTPUT" "Expectation matches." \
  "confirms a matched activation expectation"

export MOCK_CHANGED="allod-dev allod-work"
run "$DEPLOY" --override "$OVERRIDE" --expect allod-dev --expect allod-work
assert_equal "$STATUS" "0" "repeating --expect accumulates the same set"

export MOCK_CHANGED="allod-dev"
run "$DEPLOY" --override "$OVERRIDE" --expect=allod-dev
assert_equal "$STATUS" "0" "--expect=<value> is accepted"

# --- Sabotage 1: the expectation misses a machine that changed ---

export MOCK_CHANGED="allod-work"
run "$DEPLOY" --override "$OVERRIDE" --expect-none
assert_equal "$STATUS" "2" "--expect-none fails when a machine changes"
assert_contains "$OUTPUT" "Expectation mismatch." \
  "names the failure"
assert_contains "$OUTPUT" "changed, not expected:     allod-work" \
  "names the unexpected machine"
assert_not_contains "$OUTPUT" "expected, did not change" \
  "does not report a missing machine when none was expected"

export MOCK_CHANGED="allod-dev allod-work"
run "$DEPLOY" --override "$OVERRIDE" --expect allod-dev
assert_equal "$STATUS" "2" "--expect fails when an extra machine changes"
assert_contains "$OUTPUT" "changed, not expected:     allod-work" \
  "names only the machine outside the expectation"

# --- Sabotage 2: the expectation names a machine that did not change ---

export MOCK_CHANGED=""
run "$DEPLOY" --override "$OVERRIDE" --expect allod-dev
assert_equal "$STATUS" "2" "--expect fails when the expected machine does not change"
assert_contains "$OUTPUT" "expected, did not change:  allod-dev" \
  "names the missing machine"
assert_not_contains "$OUTPUT" "changed, not expected" \
  "does not report an unexpected machine when nothing changed"

# --- Both directions at once, reported as two sets ---

export MOCK_CHANGED="allod-work"
run "$DEPLOY" --override "$OVERRIDE" --expect allod-dev
assert_equal "$STATUS" "2" "fails when the sets differ in both directions"
assert_contains "$OUTPUT" "changed, not expected:     allod-work" \
  "names the unexpected machine separately"
assert_contains "$OUTPUT" "expected, did not change:  allod-dev" \
  "names the missing machine separately"

# --- Evaluation failure is its own exit code ---

export MOCK_CHANGED=""
export MOCK_FAIL="allod-dev"
run "$DEPLOY" --override "$OVERRIDE" --expect-none
assert_equal "$STATUS" "3" "an evaluation failure exits 3, not 1 or 2"
assert_contains "$OUTPUT" "baseline evaluation failed for allod-dev" \
  "names the machine that could not be evaluated"
export MOCK_FAIL=""

# --- Working-tree warning ---

export MOCK_DIRTY=true
run "$DEPLOY" --override "$OVERRIDE" --expect-none
assert_equal "$STATUS" "0" "a dirty flake.lock warns without failing"
assert_contains "$OUTPUT" "the baseline is the working tree, not the committed lock" \
  "warns that an uncommitted lock moves the baseline"
export MOCK_DIRTY=false

# --- Short-form overrides: a revision, and the lock says where it lives ---
#
# The long form makes a human retype a URL the lock already records. Only the
# revision is theirs to know.

run "$DEPLOY" --override archetypes/vm=89abcde --expect-none
assert_equal "$STATUS" "0" "an abbreviated revision is accepted in place of a URL"
assert_contains "$(cat "$MOCK_LOG")" \
  "ls-remote --quiet https://forge.example/allod/vm.git" \
  "asks the repository the lock records for that input, not one the caller typed"
assert_contains "$(cat "$MOCK_LOG")" \
  "--override-input archetypes/vm git+https://forge.example/allod/vm.git?rev=89abcdef0123456789abcdef0123456789abcdef" \
  "expands the abbreviation into the whole revision it pins"
assert_contains "$OUTPUT" \
  "override:     archetypes/vm=89abcde → git+https://forge.example/allod/vm.git?rev=89abcdef0123456789abcdef0123456789abcdef  (refs/heads/agent/split)" \
  "shows what the abbreviation resolved to and where it was found, so the pinned commit is on the receipt"

run "$DEPLOY" --override archetypes/vm=89ABCDE --expect-none
assert_equal "$STATUS" "0" "an uppercase revision is accepted"
assert_contains "$(cat "$MOCK_LOG")" \
  "rev=89abcdef0123456789abcdef0123456789abcdef" \
  "matches a revision case-insensitively"

run "$DEPLOY" --override archetypes/vm=fedcba9 --expect-none
assert_equal "$STATUS" "0" "a commit reached only through an annotated tag resolves"
assert_contains "$(cat "$MOCK_LOG")" \
  "?rev=fedcba9876543210fedcba9876543210fedcba98" \
  "reads the peeled tag entry, which is where the commit hash lives"
assert_contains "$OUTPUT" "(refs/tags/v1)" \
  "names the tag rather than the tag object"

run "$DEPLOY" --override archetypes/vm=7777777 --expect-none
assert_equal "$STATUS" "0" "a commit carried by both a branch and a tag resolves"
assert_contains "$OUTPUT" "(refs/tags/release, refs/heads/agent/tagged)" \
  "names every ref carrying the commit rather than picking one"

run "$DEPLOY" --override nixpkgs=89abcde --expect-none
assert_equal "$STATUS" "0" "a github-locked input resolves by revision too"
assert_contains "$(cat "$MOCK_LOG")" \
  "ls-remote --quiet https://github.com/NixOS/nixpkgs.git" \
  "derives the github remote from the lock's owner and repo"
assert_contains "$(cat "$MOCK_LOG")" \
  "--override-input nixpkgs github:NixOS/nixpkgs/89abcdef0123456789abcdef0123456789abcdef" \
  "writes a github input back in its own flakeref form"

# A whole revision is already the thing nix wants. Asking the remote to confirm
# it would refuse a commit that is nobody's ref tip and would have worked.
run "$DEPLOY" --override archetypes/vm=89abcdef0123456789abcdef0123456789abcdef --expect-none
assert_equal "$STATUS" "0" "a whole 40-character revision is accepted in the same place"
assert_equal "$(grep -c 'ls-remote' "$MOCK_LOG" || true)" "0" \
  "asks the remote nothing about a revision that needs no expanding"
assert_contains "$(cat "$MOCK_LOG")" \
  "--override-input archetypes/vm git+https://forge.example/allod/vm.git?rev=89abcdef0123456789abcdef0123456789abcdef" \
  "pins the whole revision it was given"

run "$DEPLOY" --override archetypes/vm=deadbeefdeadbeefdeadbeefdeadbeefdeadbeef --expect-none
assert_equal "$STATUS" "0" "a whole revision that is no ref's tip is still accepted"
assert_contains "$(cat "$MOCK_LOG")" \
  "--override-input archetypes/vm git+https://forge.example/allod/vm.git?rev=deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" \
  "pins a commit deeper in history than any branch tip"

run "$DEPLOY" --override archetypes/vm=0123456 --expect-none
assert_equal "$STATUS" "0" "the default branch tip resolves"
assert_contains "$OUTPUT" "(refs/heads/master)" \
  "names the branch rather than the symbolic HEAD that duplicates it"
assert_not_contains "$OUTPUT" "HEAD" \
  "never reports HEAD as a ref carrying a commit"

run "$DEPLOY" --override archetypes/vm=89abcdef0123456789abcdef0123456789abcdefff --expect-none
assert_equal "$STATUS" "1" "a hex string longer than a revision is a usage error"
assert_contains "$OUTPUT" "a revision is at most 40 characters, got 42" \
  "says the revision is too long rather than too short"

run "$DEPLOY" --override archetypes/vm=89abcde --override archetypes/vm=fedcba9 --expect-none
assert_equal "$STATUS" "1" "the same input overridden twice is a usage error"
assert_contains "$OUTPUT" "names archetypes/vm twice" \
  "refuses rather than printing a receipt for an override nix would discard"

# Sabotage 4: an abbreviation that names two commits must not silently pin one.
run "$DEPLOY" --override archetypes/vm=abc1234 --expect-none
assert_equal "$STATUS" "1" "an ambiguous abbreviation fails instead of pinning a guess"
assert_contains "$OUTPUT" "abc1234 names 2 commits" \
  "says how many commits the abbreviation matched"
assert_contains "$OUTPUT" "abc1234000000000000000000000000000000000" \
  "names one colliding commit"
assert_contains "$OUTPUT" "abc1234fffffffffffffffffffffffffffffffff" \
  "names the other colliding commit"
assert_contains "$OUTPUT" "give more characters" \
  "says what to do about a collision"
assert_not_contains "$OUTPUT" "machines change." \
  "evaluates nothing once an abbreviation is known ambiguous"

run "$DEPLOY" --override archetypes/vm=9999999 --expect-none
assert_equal "$STATUS" "1" "an abbreviation on no ref of the remote is refused"
assert_contains "$OUTPUT" "no ref of https://forge.example/allod/vm.git carries a commit starting with 9999999" \
  "names the remote it asked and the revision it could not find"
assert_contains "$OUTPUT" "whole 40-character revision" \
  "points at the escape hatch for a commit that is no ref's tip"
assert_not_contains "$OUTPUT" "machines change." \
  "evaluates nothing once an abbreviation is known unresolvable"

run "$DEPLOY" --override archetypes/vm=89abc --expect-none
assert_equal "$STATUS" "1" "a revision shorter than the minimum is a usage error"
assert_contains "$OUTPUT" "a revision needs at least 7 characters, got 5" \
  "says how many characters a revision needs, and how many it got"
assert_equal "$(grep -c 'ls-remote' "$MOCK_LOG" || true)" "0" \
  "does not consult the remote for a revision it has already rejected"

run "$DEPLOY" --override pinned=89abcde --expect-none
assert_equal "$STATUS" "1" "an input with no git remote cannot take a revision"
assert_contains "$OUTPUT" "cannot name a repository for pinned" \
  "names the input whose source it cannot resolve"
assert_contains "$OUTPUT" "'path' source" \
  "names the source type recorded in the lock"
assert_contains "$OUTPUT" "give the whole flake URL" \
  "points at the escape hatch"

export MOCK_LSREMOTE_FAIL=true
run "$DEPLOY" --override archetypes/vm=89abcde --expect-none
assert_equal "$STATUS" "1" "an unreachable remote is a precondition error, not an evaluation one"
assert_contains "$OUTPUT" "could not list the refs of https://forge.example/allod/vm.git" \
  "names the remote it could not reach"
assert_contains "$OUTPUT" "could not read from remote repository" \
  "passes git's own diagnostic through"
export MOCK_LSREMOTE_FAIL=false

# A bad override path is refused before any revision is resolved, so a typo
# costs no network round trip.
run "$DEPLOY" --override archetypse/vm=89abcde --expect-none
assert_equal "$STATUS" "1" "a mistyped path with a revision value still fails on the path"
assert_equal "$(grep -c 'ls-remote' "$MOCK_LOG" || true)" "0" \
  "checks every override path before consulting any remote"

run "$DEPLOY" --override archetypes/vm=89abcde --override inventory=fedcba9 --expect-none
assert_equal "$STATUS" "0" "revisions and their remotes are resolved per override"
assert_contains "$(cat "$MOCK_LOG")" \
  "ls-remote --quiet https://forge.example/allod/inventory.git" \
  "asks each override's own remote"
assert_contains "$(cat "$MOCK_LOG")" \
  "--override-input inventory git+https://forge.example/allod/inventory.git?rev=fedcba9876543210fedcba9876543210fedcba98" \
  "resolves the second override independently of the first"

# --- Argument handling ---

run "$DEPLOY" --override "$OVERRIDE" --override 'inventory=git+https://forge.example/allod/inventory.git?rev=dddd'
assert_equal "$STATUS" "0" "--override is repeatable"
assert_contains "$(cat "$MOCK_LOG")" \
  "--override-input inventory git+https://forge.example/allod/inventory.git?rev=dddd" \
  "passes every override through to nix"

run "$DEPLOY" --override 'nixpkgs=git+https://forge.example/x.git?rev=cccc'
assert_equal "$STATUS" "0" "an override reached through a follows resolves"

run --override "$OVERRIDE" -- "$DEPLOY"
assert_equal "$STATUS" "0" "-- ends option parsing"

run --help
assert_equal "$STATUS" "0" "--help exits 0"
assert_contains "$OUTPUT" "Usage: fleet-diff" "prints usage for --help"

run "$DEPLOY" --invalid --override "$OVERRIDE"
assert_equal "$STATUS" "1" "an unknown option is a usage error"
assert_contains "$OUTPUT" "unknown option: --invalid" \
  "explains an unknown-option failure"

run "$DEPLOY"
assert_equal "$STATUS" "1" "a run with no override is a usage error"
assert_contains "$OUTPUT" "missing required option: --override" \
  "explains the missing override"

run "$DEPLOY" one --override "$OVERRIDE"
assert_equal "$STATUS" "1" "a second positional argument is a usage error"
assert_contains "$OUTPUT" "unexpected argument: one" \
  "explains an extra-argument failure"

run "$NOT_A_FLAKE" --override "$OVERRIDE"
assert_equal "$STATUS" "1" "a checkout without flake.nix is a usage error"
assert_contains "$OUTPUT" "no flake.nix in $NOT_A_FLAKE" \
  "explains that the checkout is not a composition root"

run "$NO_LOCK" --override "$OVERRIDE"
assert_equal "$STATUS" "1" "a checkout without flake.lock is a usage error"
assert_contains "$OUTPUT" "the baseline must be a committed lock" \
  "refuses a checkout with no committed lock to compare against"

# Sabotage 3: nix answers a mistyped --override-input with a warning and exit 0,
# evaluating the baseline — so without this check every machine would report
# unchanged and --expect-none would pass while proving nothing.
run "$DEPLOY" --override 'archetypse/vm=git+https://forge.example/allod/vm.git?rev=bbbb' --expect-none
assert_equal "$STATUS" "1" "a mistyped override path fails instead of passing --expect-none"
assert_contains "$OUTPUT" "--override names inputs absent from" \
  "explains that the override matched no input"
assert_contains "$OUTPUT" "archetypse/vm" \
  "names the override path it could not resolve"
assert_contains "$OUTPUT" "direct inputs: archetypes inventory nixpkgs pinned" \
  "lists the direct inputs so a path mistake is obvious"
assert_not_contains "$OUTPUT" "machines change." \
  "does not evaluate anything once an override is known bad"

run "$DEPLOY" --override 'vm=git+https://forge.example/allod/vm.git?rev=bbbb' --expect-none
assert_equal "$STATUS" "1" "a transitive input named without its path is rejected"
assert_contains "$OUTPUT" "transitive inputs override by path" \
  "points at the path form for a transitive input"

run "$TMP/absent" --override "$OVERRIDE"
assert_equal "$STATUS" "1" "a missing directory is a usage error"
assert_contains "$OUTPUT" "not a directory: $TMP/absent" \
  "explains a missing checkout"

run "$DEPLOY" --override 'archetypes/vm'
assert_equal "$STATUS" "1" "an override without = is a usage error"
assert_contains "$OUTPUT" "--override needs <input>=<rev>" \
  "explains the override format"

run "$DEPLOY" --override 'archetypes/vm='
assert_equal "$STATUS" "1" "an override with an empty value is a usage error"
assert_contains "$OUTPUT" "--override has an empty value" \
  "explains an empty override value"

run "$DEPLOY" --override 'arche types=git+https://forge.example/x.git'
assert_equal "$STATUS" "1" "an override with an invalid input name is a usage error"
assert_contains "$OUTPUT" "invalid input name in --override" \
  "explains an invalid input name"

run "$DEPLOY" --override --expect-none
assert_equal "$STATUS" "1" "--override followed by an option consumed nothing"
assert_contains "$OUTPUT" "--override requires a value" \
  "explains an override that consumed nothing"

run "$DEPLOY" --override "$OVERRIDE" --expect --expect-none
assert_equal "$STATUS" "1" "--expect followed by an option consumed nothing"
assert_contains "$OUTPUT" "--expect requires a value" \
  "explains an expect list that consumed nothing"

run "$DEPLOY" --override "$OVERRIDE" --expect ''
assert_equal "$STATUS" "1" "an empty --expect list is an error, not a default"
assert_contains "$OUTPUT" "--expect consumed no machine name" \
  "explains an empty expect list"

run "$DEPLOY" --override "$OVERRIDE" --expect 'allod-dev,,allod-work'
assert_equal "$STATUS" "1" "an empty element in an --expect list is an error"
assert_contains "$OUTPUT" "--expect consumed an empty machine name" \
  "explains an empty element in an expect list"

run "$DEPLOY" --override "$OVERRIDE" --expect allod-dev --expect-none
assert_equal "$STATUS" "1" "--expect and --expect-none together is a usage error"
assert_contains "$OUTPUT" "mutually exclusive" \
  "explains the contradictory expectation flags"

run "$DEPLOY" --override "$OVERRIDE" --expect allod-dv
assert_equal "$STATUS" "1" "a machine not in the fleet is a usage error, not a mismatch"
assert_contains "$OUTPUT" "--expect names machines not in this fleet: allod-dv" \
  "names the machine it could not find"
assert_contains "$OUTPUT" "fleet: allod-canary allod-dev allod-work" \
  "lists the fleet so a typo is obvious"

# A fleet with no machines is an evaluation error, never a vacuous pass.
export MOCK_MACHINES=""
run "$DEPLOY" --override "$OVERRIDE" --expect-none
assert_equal "$STATUS" "3" "an empty fleet fails loudly instead of matching --expect-none"
assert_contains "$OUTPUT" "composes no nixosConfigurations" \
  "explains an empty fleet"
export MOCK_MACHINES="allod-canary allod-dev allod-work"

printf '\nTests run: %d\n' "$test_number"
printf '✅ All %d fleet-diff tests passed.\n' "$test_number"
