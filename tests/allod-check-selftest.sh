#!/usr/bin/env bash
#
# Proves 'allod check' goes red, and that it is no more permissive than stock
# `nix flake check`. It needs a real `nix`, which the flake's own sandboxed
# checks cannot host, so it is run by hand: after a Nix upgrade, and before a
# change to the command lands. docs/allod-check.md says the same.
#
#     ALLOD_UNDER_TEST=$(nix build --no-link --print-out-paths .#allod)/bin/allod \
#       bash tests/allod-check-selftest.sh
#
# Part 1, the gate can fail. Sixteen throwaway flakes, one per way it can:
#
#   green              a machine and two checks that all pass, one keyed on another system; must exit 0, run every step, and report one check built and one evaluated only.
#   checks-only        checks but no nixosConfigurations, a flake `nix flake check` also passes; must exit 0.
#   dotted-names       valid attribute names that cannot be spliced into a CLI attr path; must exit 0.
#   tabbed-names       valid attribute names containing tabs that look like injected record fields; must fail for their own reasons, not be reinterpreted as another step.
#   passive-throw      an allowed passive output that throws; must exit non-zero, since native `nix flake check` forces it too.
#   modules            an exported nixosModules entry that throws, beside one that does not; must exit non-zero naming only the thrower, proving the module step forces the value rather than announcing it.
#   red                five simultaneous failures — a machine whose toplevel throws, one whose toplevel is not a derivation, one whose drvPath throws, a check whose builder exits 1, and a foreign-system check whose drvPath throws; must exit non-zero naming all five and none of the three that pass, since a non-zero exit alone would not show the gate kept going past the first failure.
#   nothing-to-run     empty nixosConfigurations and empty checks; must exit non-zero rather than report a green run of no steps.
#   all-evaluated      a machine and two checks keyed only on another system, one failing in its builder shell and evaluating cleanly, so every step passes and this is the run that would otherwise be a false green; must exit non-zero, saying it built nothing and naming the system to run on.
#   bad-system         a `checks.typo` entry; must exit non-zero naming `typo`.
#   unhandled-output   a `packages` output, which `nix flake check` validates and the gate has no step for; must exit non-zero naming `packages`, before running any step.
#   unknown-output     a misspelled top-level output, which native `nix flake check` only warns on; must exit non-zero naming it, before running any step.
#   configured-output  a passive output the tool does not allow by itself, allowed by allod-check.toml; must exit 0 and run a step for it.
#   bad-config         an allod-check.toml with a key the tool does not know; must exit non-zero naming the file, before running any step.
#   override-checks    a flake whose set of checks comes from an input, run with and without --override-input; the overridden run must show the overridden checks and no other, and the checkout's flake.lock must be byte-identical afterwards.
#   override-unsafe    the same flake overridden with an input whose check name holds a double quote, which cannot be spelled in an installable at all; must exit 0 having built it.
#
# Part 2, the differential. Stock `nix flake check` and the gate are run over
# the same small fixtures, and a fixture Nix rejects and the gate accepts is a
# failure: that is the gate claiming coverage Nix has and it does not. The
# reverse is fine and is printed as such. `--no-build` is passed to stock,
# because what is being compared is how deeply each one evaluates.
#
# Every fixture in both parts is a bare `derivation` call, needing no nixpkgs,
# no network and no flake input other than a sibling fixture.

set -euo pipefail

allod="${ALLOD_UNDER_TEST:-$(command -v allod)}"

failures=0
pass() { printf 'ok:   %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; failures=$((failures + 1)); }

work=""
# Reached through `trap cleanup EXIT`, which shellcheck cannot see.
# shellcheck disable=SC2329
cleanup() { [ -z "$work" ] || rm -rf -- "$work"; }
trap cleanup EXIT

[ -x "$allod" ] || { printf 'FAIL: %s is missing or not executable\n' "$allod" >&2; exit 1; }

this_system="$(nix eval --raw --impure --expr builtins.currentSystem)" \
  || { printf "FAIL: could not determine this machine's Nix system\n" >&2; exit 1; }
[ -n "$this_system" ] || { printf "FAIL: this machine's Nix system came back empty\n" >&2; exit 1; }

# A second real system name, so the fixtures exercise the branch that evaluates
# a foreign check without building it. Derived so the two can never collide.
other_system=aarch64-linux
if [ "$this_system" = "$other_system" ]; then
  other_system=x86_64-linux
fi

work="$(mktemp -d)"

# Writes a fixture flake, reading the `outputs` attribute set from stdin. Only
# the system names and the shared helpers are generated; the body stays a
# literal here-document so its Nix `${...}` interpolations reach Nix untouched.
# Nix requires flake.nix to be an attribute set literal, so the generated
# bindings go inside `outputs` rather than in a `let` around the whole file.
write_fixture() {
  local name="$1"
  mkdir -p -- "${work}/${name}"
  {
    printf '{\n  outputs = { self }:\n    let\n'
    printf '      thisSystem = "%s";\n      otherSystem = "%s";\n' "$this_system" "$other_system"
    cat <<'HELPERS'
      trivial = system: name: derivation {
        inherit name system;
        builder = "/bin/sh";
        args = [ "-c" "echo ok > $out" ];
      };
      failingBuild = system: name: derivation {
        inherit name system;
        builder = "/bin/sh";
        args = [ "-c" "exit 1" ];
      };
      okMachine = { config.system.build.toplevel = trivial thisSystem "green-machine-toplevel"; };
    in
HELPERS
    cat
  } > "${work}/${name}/flake.nix"
}

write_fixture green <<'EOF'
    {
      nixosConfigurations.green-machine = okMachine;
      checks = {
        ${thisSystem}.green-local = trivial thisSystem "green-local-check";
        ${otherSystem}.green-foreign = trivial otherSystem "green-foreign-check";
      };
    };
}
EOF

write_fixture checks-only <<'EOF'
    {
      checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
    };
}
EOF

write_fixture dotted-names <<'EOF'
    {
      nixosConfigurations."green.machine" = okMachine;
      checks.${thisSystem}."green.local" = trivial thisSystem "green-local-check";
    };
}
EOF

write_fixture tabbed-names <<'EOF'
    let
      tabbedMachine = "bad\tnixosConfigurations.\"good\".config.system.build.toplevel";
      tabbedCheck = "bad\tchecks.\"${thisSystem}\".\"green-local\"";
    in
    {
      nixosConfigurations.good = okMachine;
      nixosConfigurations.${tabbedMachine}.config.system.build.toplevel =
        throw "tabbed machine throws on purpose";
      checks.${thisSystem} = {
        green-local = trivial thisSystem "green-local-check";
        ${tabbedCheck} = failingBuild thisSystem "tabbed-check";
      };
    };
}
EOF

write_fixture passive-throw <<'EOF'
    {
      checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
      lib = throw "lib throws on purpose";
    };
}
EOF

write_fixture modules <<'EOF'
    {
      nixosConfigurations.green-machine = okMachine;
      checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
      nixosModules = {
        green-module = { ... }: { };
        red-module = throw "red-module throws on purpose";
      };
    };
}
EOF

# red-notdrv is what `nix flake check` itself rejects, with "attribute
# 'config.system.build.toplevel' is not a derivation". red-drvpath forces to a
# perfectly good derivation attribute set and only throws on `drvPath`, so it is
# what pins the gate to forcing `drvPath` rather than something cheaper.
write_fixture red <<'EOF'
    {
      nixosConfigurations = {
        green-machine = okMachine;
        red-machine.config.system.build.toplevel = throw "red-machine toplevel throws on purpose";
        red-notdrv.config.system.build.toplevel = {
          name = "red-notdrv";
          drvPath = "/nix/store/0000000000000000000000000000000-not-a-derivation.drv";
        };
        red-drvpath.config.system.build.toplevel = {
          type = "derivation";
          name = "red-drvpath";
          drvPath = throw "red-drvpath drvPath throws on purpose";
        };
      };
      checks = {
        ${thisSystem} = {
          green-local = trivial thisSystem "green-local-check";
          red-local = failingBuild thisSystem "red-local-check";
        };
        ${otherSystem} = {
          green-foreign = trivial otherSystem "green-foreign-check";
          red-foreign = throw "red-foreign drvPath throws on purpose";
        };
      };
    };
}
EOF

write_fixture nothing-to-run <<'EOF'
    {
      nixosConfigurations = {};
      checks = {};
    };
}
EOF

# shell-only-failure is the check the refusal exists for: its builder exits 1,
# so it fails when built, and nothing in its Nix expression is wrong, so it
# evaluates cleanly. Keyed on the other system it can only be evaluated here,
# and every step of this fixture passes.
write_fixture all-evaluated <<'EOF'
    {
      nixosConfigurations.green-machine = okMachine;
      checks.${otherSystem} = {
        green-foreign = trivial otherSystem "green-foreign-check";
        shell-only-failure = failingBuild otherSystem "shell-only-failure-check";
      };
    };
}
EOF

write_fixture bad-system <<'EOF'
    {
      nixosConfigurations.green-machine = okMachine;
      checks = {
        ${thisSystem}.green-local = trivial thisSystem "green-local-check";
        typo.green-typo = trivial thisSystem "green-typo-check";
      };
    };
}
EOF

write_fixture unhandled-output <<'EOF'
    {
      nixosConfigurations.green-machine = okMachine;
      checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
      packages.${thisSystem}.green-package = trivial thisSystem "green-package";
    };
}
EOF

write_fixture unknown-output <<'EOF'
    {
      nixosConfigurations.green-machine = okMachine;
      checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
      chekcs.${thisSystem}.typo = trivial thisSystem "typo-check";
    };
}
EOF

write_fixture configured-output <<'EOF'
    {
      checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
      profilesSource = { note = "a source-only output this repo exposes on purpose"; };
    };
}
EOF
printf 'passive-outputs = ["profilesSource"]\n' > "${work}/configured-output/allod-check.toml"

write_fixture bad-config <<'EOF'
    {
      checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
    };
}
EOF
printf 'passive-output = ["profilesSource"]\n' > "${work}/bad-config/allod-check.toml"

# The override fixtures: one flake whose set of checks is read out of an input,
# and three data flakes to point that input at. The data flakes sit outside the
# repository, so overriding the input is the only way their names reach it, and
# the repository is a real git checkout with a committed lock file, which is
# what makes "the lock file is untouched" a meaningful assertion.
mkdir -p "${work}/override/dataA" "${work}/override/dataB" "${work}/override/dataC"
printf '{ outputs = { self }: { names = [ "base-check" ]; }; }\n' \
  > "${work}/override/dataA/flake.nix"
printf '{ outputs = { self }: { names = [ "branch-check" "branch-extra" ]; }; }\n' \
  > "${work}/override/dataB/flake.nix"
printf '{ outputs = { self }: { names = [ "quo\\"te" ]; }; }\n' \
  > "${work}/override/dataC/flake.nix"

override_repo="${work}/override/repo"
mkdir -p "$override_repo"
{
  printf '{\n  inputs.data.url = "path:%s/override/dataA";\n' "$work"
  printf '  outputs = { self, data }:\n    let\n      thisSystem = "%s";\n' "$this_system"
  cat <<'HELPERS'
      trivial = name: derivation {
        inherit name;
        system = thisSystem;
        builder = "/bin/sh";
        args = [ "-c" "echo ok > $out" ];
      };
    in
    {
      checks.${thisSystem} = builtins.listToAttrs
        (map (name: { inherit name; value = trivial "override-check"; }) data.names);
    };
}
HELPERS
} > "${override_repo}/flake.nix"

git -C "$override_repo" init -q -b master
git -C "$override_repo" add -A
git -C "$override_repo" -c user.email=selftest@example.invalid -c user.name=selftest \
  commit -qm "override fixture"
nix flake lock "$override_repo" > /dev/null 2>&1 \
  || { printf 'FAIL: could not lock the override fixture\n' >&2; exit 1; }
git -C "$override_repo" add -A
git -C "$override_repo" -c user.email=selftest@example.invalid -c user.name=selftest \
  commit -qm "lock the override fixture"

# Runs the gate, leaving its transcript in $output and its exit status in
# $status. Both are printed, so a self-test failure carries its own evidence.
status=0
output=""
run_gate() {
  local label="$1"
  shift
  printf '\n=== %s\n' "$label"
  status=0
  output="$("$allod" check "$@" 2>&1)" || status=$?
  printf '%s\n=== %s exit status: %d\n' "$output" "$label" "$status"
}

run_fixture() { run_gate "$1" "path:${work}/$1"; }

# Asserts on the transcript of the run just made.
says() {
  if grep -qF -- "$2" <<< "$output"; then pass "$1"; else fail "$1"; fi
}
silent_about() {
  if grep -qF -- "$2" <<< "$output"; then fail "$1"; else pass "$1"; fi
}
exits_zero() {
  if [ "$status" -eq 0 ]; then pass "$1: exited 0"; else fail "$1: exited ${status}, expected 0"; fi
}
exits_nonzero() {
  if [ "$status" -ne 0 ]; then
    pass "$1: exited ${status}, non-zero as required"
  else
    fail "$1: exited 0, expected non-zero"
  fi
}

printf '\n##### Part 1: the gate can fail\n'

run_fixture green
exits_zero green
# Without "announced each step it started" and "ran all three steps" here,
# renaming the step banner or skipping steps entirely would leave every failure
# assertion in the other fixtures still passing.
says "green: announced each step it started" "==> "
says "green: ran all three steps, one check built and one evaluated only" \
  "All 3 steps passed (checks: 1 built, 1 evaluated only)."
says "green: named the system the evaluated-only check needs" \
  "The 1 evaluated-only check(s) are for ${other_system} and need a host of that system to be built."

run_fixture checks-only
exits_zero checks-only
says "checks-only: ran the check despite there being no machines" "All 1 steps passed (checks: 1 built)."

run_fixture dotted-names
exits_zero dotted-names
says "dotted-names: ran the dotted machine name" "machine green.machine"
says "dotted-names: ran the dotted check name" "check ${this_system}.green.local"
says "dotted-names: ran both steps" "All 2 steps passed (checks: 1 built)."

run_fixture tabbed-names
exits_nonzero tabbed-names
says "tabbed-names: printed both failures" "Failed steps (2 of 4)"
says "tabbed-names: named the tabbed machine" $'bad\tnixosConfigurations."good".config.system.build.toplevel'
says "tabbed-names: named the tabbed check" "$(printf 'bad\tchecks."%s"."green-local"' "$this_system")"
says "tabbed-names: still ran the real green machine" "machine good"
says "tabbed-names: still ran the real green check" "check ${this_system}.green-local"

run_fixture passive-throw
exits_nonzero passive-throw
says "passive-throw: ran the passive output step" "passive output lib"
says "passive-throw: failed on the passive output" "lib throws on purpose"
says "passive-throw: still ran the ordinary check" "check ${this_system}.green-local"

run_fixture modules
exits_nonzero modules
says "modules: ran the module that evaluates" "nixos module green-module"
says "modules: failed on the module that throws" "red-module throws on purpose"
says "modules: reported exactly the one module failure" "Failed steps (1 of 4)"
says "modules: still ran the machine and the check" "machine green-machine"

run_fixture red
exits_nonzero red
# Only the summary is searched from here. The gate announces every step it
# starts, so all eight names appear in the transcript either way; finding
# exactly the five failures under the summary heading is what shows the run
# continued past the first failure and recorded them all.
output="$(sed -n '/^Failed steps/,$p' <<< "$output")"
says "red: printed a failure summary" "Failed steps (5 of 8)"
says "red: named the machine whose toplevel throws" "red-machine"
says "red: named the machine whose toplevel is not a derivation" "red-notdrv"
says "red: named the machine whose drvPath throws" "red-drvpath"
says "red: named the check whose builder failed" "red-local"
says "red: named the foreign-system check that throws" "red-foreign"
silent_about "red: did not blame green-machine" "green-machine"
silent_about "red: did not blame green-local" "green-local"
silent_about "red: did not blame green-foreign" "green-foreign"

run_fixture nothing-to-run
exits_nonzero nothing-to-run
says "nothing-to-run: refused to call a run of no steps green" "found no machine and no check to run"
silent_about "nothing-to-run: did not report success" "steps passed"

run_fixture all-evaluated
exits_nonzero all-evaluated
# Both checks must have run and passed as evaluations; the refusal, not a failed
# step, is what has to make this exit non-zero.
says "all-evaluated: evaluated the check that fails only when built" \
  "check ${other_system}.shell-only-failure (evaluated, not built: ${other_system} is not ${this_system})"
silent_about "all-evaluated: no step failed" "!!! FAILED"
silent_about "all-evaluated: printed no failure summary" "Failed steps"
says "all-evaluated: refused a run that built nothing" \
  "Witnessed nothing: evaluated 2 of 2 checks and built none of them."
says "all-evaluated: named the system to run on" \
  "This host is ${this_system}; run the gate on ${other_system}, the system these checks are keyed on."
silent_about "all-evaluated: did not report success" "steps passed"

run_fixture bad-system
exits_nonzero bad-system
says "bad-system: stopped at the enumeration" "could not enumerate what to run in"
says "bad-system: named typo as an invalid system" 'checks.typo: "typo" is not a valid system type'

run_fixture unhandled-output
exits_nonzero unhandled-output
says "unhandled-output: refused an output it does not run" "flake output(s) allod check does not run"
says "unhandled-output: named packages" "  - packages"
# The gate announces every step it starts with a line beginning `==> `, and this
# fixture's machine and check would both otherwise have run, so the absence of
# such a line is what shows it stopped first. The `green` fixture's assertion
# that the banner appears is what keeps this from passing merely because the
# banner was renamed.
silent_about "unhandled-output: ran no step at all" "==> "

run_fixture unknown-output
exits_nonzero unknown-output
says "unknown-output: refused an unknown output" "flake output(s) allod check does not run"
says "unknown-output: named chekcs" "  - chekcs"
says "unknown-output: said how to allow one deliberately" "allod-check.toml"
silent_about "unknown-output: ran no step at all" "==> "

run_fixture configured-output
exits_zero configured-output
says "configured-output: ran the output allod-check.toml allows" "passive output profilesSource"
says "configured-output: ran both steps" "All 2 steps passed (checks: 1 built)."

run_fixture bad-config
exits_nonzero bad-config
says "bad-config: named the file it could not read" "allod-check.toml"
says "bad-config: named the key it does not know" "unknown key 'passive-output'"
silent_about "bad-config: ran no step at all" "==> "

run_gate override-none "$override_repo"
exits_zero override-none
says "override-none: ran the check the locked input names" "check ${this_system}.base-check (built)"
says "override-none: ran exactly that one check" "All 1 steps passed (checks: 1 built)."
cp "${override_repo}/flake.lock" "${work}/override/lock-before"

run_gate override-checks "--override-input" "data" "path:${work}/override/dataB" "$override_repo"
exits_zero override-checks
says "override-checks: reported the override" "Override: data = path:${work}/override/dataB"
says "override-checks: ran the first overridden check" "check ${this_system}.branch-check (built)"
says "override-checks: ran the second overridden check" "check ${this_system}.branch-extra (built)"
says "override-checks: ran exactly the overridden set" "All 2 steps passed (checks: 2 built)."
silent_about "override-checks: did not run the locked input's check" "base-check"
if cmp -s "${override_repo}/flake.lock" "${work}/override/lock-before"; then
  pass "override-checks: left flake.lock byte-identical"
else
  fail "override-checks: rewrote the checkout's flake.lock"
fi

run_gate override-unsafe "--override-input" "data" "path:${work}/override/dataC" "$override_repo"
exits_zero override-unsafe
says "override-unsafe: built a check whose name cannot be spelled in an installable" \
  "$(printf 'check %s.quo"te (built)' "$this_system")"
says "override-unsafe: ran exactly that one check" "All 1 steps passed (checks: 1 built)."

printf '\n##### Part 2: the differential against nix flake check\n'

# Writes a differential fixture, reading the `outputs` attribute set from stdin.
# Each one holds a green local check beside whatever is being probed, so the
# only reason either tool can go red is the probe itself.
write_probe() {
  local name="$1"
  mkdir -p -- "${work}/diff/${name}"
  {
    printf '{\n  outputs = { self }:\n    let\n      thisSystem = "%s";\n' "$this_system"
    cat <<'HELPERS'
      trivial = name: derivation {
        inherit name;
        system = thisSystem;
        builder = "/bin/sh";
        args = [ "-c" "echo ok > $out" ];
      };
    in
HELPERS
    cat
  } > "${work}/diff/${name}/flake.nix"
}

write_probe green <<'EOF'
    {
      nixosConfigurations.green-machine.config.system.build.toplevel = trivial "green-toplevel";
      checks.${thisSystem}.green-local = trivial "green-local-check";
    };
}
EOF

write_probe check-not-a-derivation <<'EOF'
    {
      checks.${thisSystem} = {
        green-local = trivial "green-local-check";
        probe = 42;
      };
    };
}
EOF

write_probe check-drvpath-throws <<'EOF'
    {
      checks.${thisSystem} = {
        green-local = trivial "green-local-check";
        probe = { type = "derivation"; name = "probe"; drvPath = throw "probe drvPath throws on purpose"; };
      };
    };
}
EOF

write_probe checks-system-not-attrs <<'EOF'
    {
      nixosConfigurations.green-machine.config.system.build.toplevel = trivial "green-toplevel";
      checks.${thisSystem} = 42;
    };
}
EOF

write_probe machine-toplevel-not-a-derivation <<'EOF'
    {
      checks.${thisSystem}.green-local = trivial "green-local-check";
      nixosConfigurations.probe.config.system.build.toplevel = {
        name = "probe";
        drvPath = "/nix/store/0000000000000000000000000000000-not-a-derivation.drv";
      };
    };
}
EOF

write_probe machine-without-config <<'EOF'
    {
      checks.${thisSystem}.green-local = trivial "green-local-check";
      nixosConfigurations.probe = { };
    };
}
EOF

write_probe module-throws <<'EOF'
    {
      checks.${thisSystem}.green-local = trivial "green-local-check";
      nixosModules.probe = throw "probe module throws on purpose";
    };
}
EOF

write_probe module-is-an-integer <<'EOF'
    {
      checks.${thisSystem}.green-local = trivial "green-local-check";
      nixosModules.probe = 42;
    };
}
EOF

write_probe configurations-are-a-list <<'EOF'
    {
      checks.${thisSystem}.green-local = trivial "green-local-check";
      nixosConfigurations = [ { config.system.build.toplevel = trivial "probe-toplevel"; } ];
    };
}
EOF

write_probe passive-output-throws <<'EOF'
    {
      checks.${thisSystem}.green-local = trivial "green-local-check";
      lib = throw "lib throws on purpose";
    };
}
EOF

write_probe passive-output-is-an-integer <<'EOF'
    {
      checks.${thisSystem}.green-local = trivial "green-local-check";
      lib = 42;
    };
}
EOF

printf '\n%-36s %-22s %-14s %s\n' fixture 'nix flake check' 'allod check' verdict
printf '%-36s %-22s %-14s %s\n' ------- --------------- ----------- -------
for probe in green check-not-a-derivation check-drvpath-throws checks-system-not-attrs \
  machine-toplevel-not-a-derivation machine-without-config module-throws \
  module-is-an-integer configurations-are-a-list passive-output-throws \
  passive-output-is-an-integer; do
  nix_verdict=accepts
  nix flake check --no-build "path:${work}/diff/${probe}" > /dev/null 2>&1 || nix_verdict=rejects
  gate_verdict=accepts
  "$allod" check "path:${work}/diff/${probe}" > /dev/null 2>&1 || gate_verdict=rejects

  verdict=agree
  if [ "$nix_verdict" = rejects ] && [ "$gate_verdict" = accepts ]; then
    verdict='GATE IS WEAKER'
    fail "differential ${probe}: nix flake check rejects it and the gate accepts it"
  elif [ "$nix_verdict" = accepts ] && [ "$gate_verdict" = rejects ]; then
    verdict='gate is stricter (fine)'
  fi
  printf '%-36s %-22s %-14s %s\n' "$probe" "$nix_verdict" "$gate_verdict" "$verdict"
done

if [ "$failures" -eq 0 ]; then
  printf '\nallod-check-selftest.sh: PASS\n'
  exit 0
fi
printf '\nallod-check-selftest.sh: FAIL (%d assertion(s) failed)\n' "$failures" >&2
exit 1
