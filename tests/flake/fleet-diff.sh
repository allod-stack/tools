#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

export HOME="$TMP/home"
mkdir -p "$HOME" "$TMP/bin"

# A composition root whose lock has a transitive input (archetypes/vm), a direct
# input (inventory), and a root-level follows (nixpkgs). Only the graph shape
# matters — nix never reads it, the tool's override check does.
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
        "nixpkgs": ["archetypes", "nixpkgs"]
      }
    },
    "archetypes": {
      "inputs": {"vm": "vm", "nixpkgs": ["archetypes", "vm", "nixpkgs"]},
      "locked": {"rev": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
    },
    "vm": {
      "inputs": {"nixpkgs": "nixpkgs"},
      "locked": {"rev": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
    },
    "nixpkgs": {"locked": {"rev": "cccccccccccccccccccccccccccccccccccccccc"}},
    "inventory": {"locked": {"rev": "dddddddddddddddddddddddddddddddddddddddd"}}
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

cat > "$TMP/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == "-C" ]] || { echo "unexpected git invocation: $*" >&2; exit 1; }
shift 2
printf 'git\t%s\n' "$*" >> "$MOCK_LOG"

case "$*" in
  "status --porcelain -- flake.nix flake.lock")
    [[ "${MOCK_DIRTY:-}" == true ]] && printf ' M flake.lock\n'
    exit 0
    ;;
  *)
    echo "unexpected git invocation: $*" >&2
    exit 1
    ;;
esac
EOF

chmod +x "$TMP/bin/nix" "$TMP/bin/git"
export PATH="$TMP/bin:$PATH"

export MOCK_MACHINES="allod-canary allod-dev allod-work"
export MOCK_CHANGED=""
export MOCK_FAIL=""
export MOCK_DIRTY=false

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
assert_contains "$OUTPUT" "direct inputs: archetypes inventory nixpkgs" \
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
assert_contains "$OUTPUT" "--override needs <input>=<url>" \
  "explains the override format"

run "$DEPLOY" --override 'archetypes/vm='
assert_equal "$STATUS" "1" "an override with an empty url is a usage error"
assert_contains "$OUTPUT" "--override has an empty url" \
  "explains an empty override url"

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
