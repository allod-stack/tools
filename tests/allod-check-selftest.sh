#!/usr/bin/env bash
#
# Proves 'allod check' goes red, one throwaway flake per way it can, and that it
# is no more permissive than stock `nix flake check`. It drives a real `nix`,
# which the flake's sandboxed checks cannot host, so it is run by hand: after a
# Nix upgrade, and before a change to the command lands.
#
#     ALLOD_UNDER_TEST=$(nix build --no-link --print-out-paths .#allod)/bin/allod \
#       bash tests/allod-check-selftest.sh

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
export GIT_AUTHOR_NAME=selftest GIT_AUTHOR_EMAIL=selftest@example.invalid
export GIT_COMMITTER_NAME=selftest GIT_COMMITTER_EMAIL=selftest@example.invalid

# Writes a fixture flake, reading the `outputs` attribute set from stdin. The
# body stays a literal here-document so its Nix `${...}` interpolations reach Nix
# untouched, and the generated bindings go inside `outputs` because Nix requires
# flake.nix itself to be an attribute set literal.
write_fixture() {
  mkdir -p -- "${work}/$1"
  {
    printf '{\n  outputs = { self }:\n    let\n'
    printf '      thisSystem = "%s";\n      otherSystem = "%s";\n' "$this_system" "$other_system"
    cat <<'HELPERS'
      trivial = system: name: derivation {
        inherit name system; builder = "/bin/sh"; args = [ "-c" "echo ok > $out" ];
      };
      failingBuild = system: name: derivation {
        inherit name system; builder = "/bin/sh"; args = [ "-c" "exit 1" ];
      };
      okMachine = { config.system.build.toplevel = trivial thisSystem "green-machine-toplevel"; };
    in
HELPERS
    cat
  } > "${work}/$1/flake.nix"
}

# A git checkout whose set of checks is read out of an input, so an override is
# the only way another set of names reaches it. The data flakes sit outside it.
write_input_repo() {
  mkdir -p -- "$1"
  {
    printf '{\n  inputs.data.url = "path:%s/override/dataA";\n' "$work"
    printf '  outputs = { self, data }:\n    let\n      thisSystem = "%s";\n' "$this_system"
    cat <<'HELPERS'
      trivial = name: derivation {
        inherit name; system = thisSystem; builder = "/bin/sh"; args = [ "-c" "echo ok > $out" ];
      };
    in
    {
      checks.${thisSystem} = builtins.listToAttrs
        (map (name: { inherit name; value = trivial "override-check"; }) data.names);
    };
}
HELPERS
  } > "$1/flake.nix"
  git -C "$1" init -q -b master
  git -C "$1" add -A
  git -C "$1" commit -qm "fixture"
}

write_fixture green <<'EOF'
    { nixosConfigurations.green-machine = okMachine;
      checks = {
        ${thisSystem}.green-local = trivial thisSystem "green-local-check";
        ${otherSystem}.green-foreign = trivial otherSystem "green-foreign-check";
      }; };
}
EOF

write_fixture checks-only <<'EOF'
    { checks.${thisSystem}.green-local = trivial thisSystem "green-local-check"; };
}
EOF

write_fixture dotted-names <<'EOF'
    { nixosConfigurations."green.machine" = okMachine;
      checks.${thisSystem}."green.local" = trivial thisSystem "green-local-check"; };
}
EOF

write_fixture tabbed-names <<'EOF'
    let
      tabbedMachine = "bad\tnixosConfigurations.\"good\".config.system.build.toplevel";
      tabbedCheck = "bad\tchecks.\"${thisSystem}\".\"green-local\"";
    in
    { nixosConfigurations.good = okMachine;
      nixosConfigurations.${tabbedMachine}.config.system.build.toplevel =
        throw "tabbed machine throws on purpose";
      checks.${thisSystem} = {
        green-local = trivial thisSystem "green-local-check";
        ${tabbedCheck} = failingBuild thisSystem "tabbed-check";
      }; };
}
EOF

write_fixture passive-throw <<'EOF'
    { checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
      lib = throw "lib throws on purpose"; };
}
EOF

write_fixture modules <<'EOF'
    { nixosConfigurations.green-machine = okMachine;
      checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
      nixosModules = { green-module = { ... }: { }; red-module = throw "red-module throws on purpose"; }; };
}
EOF

# red-notdrv is what `nix flake check` itself rejects. red-drvpath forces to a
# perfectly good derivation attribute set and only throws on `drvPath`, so it is
# what pins the gate to forcing `drvPath` rather than something cheaper.
write_fixture red <<'EOF'
    { nixosConfigurations = {
        green-machine = okMachine;
        red-machine.config.system.build.toplevel = throw "red-machine toplevel throws on purpose";
        red-notdrv.config.system.build.toplevel =
          { name = "red-notdrv"; drvPath = "/nix/store/0000000000000000000000000000000-not.drv"; };
        red-drvpath.config.system.build.toplevel = {
          type = "derivation"; name = "red-drvpath";
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
      }; };
}
EOF

write_fixture nothing-to-run <<'EOF'
    { nixosConfigurations = {}; checks = {}; };
}
EOF

# shell-only-failure is the check the refusal exists for: its builder exits 1,
# so it fails when built, and nothing in its Nix expression is wrong, so it
# evaluates cleanly. Keyed on the other system it can only be evaluated here,
# and every step of this fixture passes.
write_fixture all-evaluated <<'EOF'
    { nixosConfigurations.green-machine = okMachine;
      checks.${otherSystem} = {
        green-foreign = trivial otherSystem "green-foreign-check";
        shell-only-failure = failingBuild otherSystem "shell-only-failure-check";
      }; };
}
EOF

write_fixture bad-system <<'EOF'
    { nixosConfigurations.green-machine = okMachine;
      checks = { ${thisSystem}.green-local = trivial thisSystem "green-local-check";
                 typo.green-typo = trivial thisSystem "green-typo-check"; }; };
}
EOF

write_fixture unhandled-output <<'EOF'
    { nixosConfigurations.green-machine = okMachine;
      checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
      packages.${thisSystem}.green-package = trivial thisSystem "green-package"; };
}
EOF

write_fixture unknown-output <<'EOF'
    { nixosConfigurations.green-machine = okMachine;
      checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
      chekcs.${thisSystem}.typo = trivial thisSystem "typo-check"; };
}
EOF

write_fixture configured-output <<'EOF'
    { checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
      profilesSource = { note = "a source-only output this repo exposes on purpose"; }; };
}
EOF
printf 'passive-outputs = ["profilesSource"]\n' > "${work}/configured-output/allod-check.toml"

write_fixture bad-config <<'EOF'
    { checks.${thisSystem}.green-local = trivial thisSystem "green-local-check"; };
}
EOF
printf 'passive-output = ["profilesSource"]\n' > "${work}/bad-config/allod-check.toml"

# The same file as configured-output's, in a git checkout that has not committed
# it: Nix does not see an untracked file, so neither does the gate.
write_fixture untracked-config <<'EOF'
    { checks.${thisSystem}.green-local = trivial thisSystem "green-local-check";
      profilesSource = { note = "a source-only output this repo exposes on purpose"; }; };
}
EOF
git -C "${work}/untracked-config" init -q -b master
git -C "${work}/untracked-config" add flake.nix
git -C "${work}/untracked-config" commit -qm "fixture"
printf 'passive-outputs = ["profilesSource"]\n' > "${work}/untracked-config/allod-check.toml"

mkdir -p "${work}/override/dataA" "${work}/override/dataB" "${work}/override/dataC"
printf '{ outputs = { self }: { names = [ "base-check" ]; }; }\n' > "${work}/override/dataA/flake.nix"
printf '{ outputs = { self }: { names = [ "branch-check" "branch-extra" ]; }; }\n' > "${work}/override/dataB/flake.nix"
printf '{ outputs = { self }: { names = [ "quo\\"te" ]; }; }\n' > "${work}/override/dataC/flake.nix"

override_repo="${work}/override/repo"
write_input_repo "$override_repo"
nix flake lock "$override_repo" > /dev/null 2>&1 \
  || { printf 'FAIL: could not lock the override fixture\n' >&2; exit 1; }
git -C "$override_repo" add -A
git -C "$override_repo" commit -qm "lock the fixture"

no_lock_repo="${work}/override/no-lock"
write_input_repo "$no_lock_repo"

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
# The gate announces every step it starts, so all eight names appear in the
# transcript either way; finding exactly the five failures under the summary
# heading is what shows the run continued past the first failure.
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
# such a line is what shows it stopped first.
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
says "bad-config: named the file it could not use" "allod-check.toml"
says "bad-config: named the key it does not know" "passive-output"
silent_about "bad-config: ran no step at all" "==> "

run_gate untracked-config "${work}/untracked-config"
exits_nonzero untracked-config
says "untracked-config: an uncommitted allod-check.toml allows nothing" "  - profilesSource"
silent_about "untracked-config: ran no step at all" "==> "

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

run_gate no-lock "$no_lock_repo"
exits_zero no-lock
says "no-lock: checked a flake with an input and no lock file" "All 1 steps passed (checks: 1 built)."
if [ -e "${no_lock_repo}/flake.lock" ]; then
  fail "no-lock: wrote a flake.lock into the checkout"
else
  pass "no-lock: wrote no flake.lock"
fi

printf '\n##### Part 2: the differential against nix flake check\n'

# Each probe holds a green local check beside whatever is being probed, so the
# only reason either tool can go red is the probe itself.
write_fixture diff/green <<'EOF'
    { nixosConfigurations.m = okMachine; checks.${thisSystem}.g = trivial thisSystem "g"; };
}
EOF
write_fixture diff/check-not-a-derivation <<'EOF'
    { checks.${thisSystem} = { g = trivial thisSystem "g"; probe = 42; }; };
}
EOF
write_fixture diff/check-is-a-list <<'EOF'
    { checks.${thisSystem} = { g = trivial thisSystem "g";
        probe = [ (trivial thisSystem "p1") (trivial thisSystem "p2") ]; }; };
}
EOF
write_fixture diff/check-is-a-set-of-derivations <<'EOF'
    { checks.${thisSystem} = { g = trivial thisSystem "g";
        probe = { one = trivial thisSystem "p1"; two = trivial thisSystem "p2"; }; }; };
}
EOF
write_fixture diff/check-drvpath-throws <<'EOF'
    { checks.${thisSystem} = { g = trivial thisSystem "g";
        probe = { type = "derivation"; name = "probe"; drvPath = throw "probe drvPath throws"; }; }; };
}
EOF
write_fixture diff/checks-system-not-attrs <<'EOF'
    { nixosConfigurations.m = okMachine; checks.${thisSystem} = 42; };
}
EOF
write_fixture diff/machine-toplevel-not-a-derivation <<'EOF'
    { checks.${thisSystem}.g = trivial thisSystem "g";
      nixosConfigurations.probe.config.system.build.toplevel =
        { name = "probe"; drvPath = "/nix/store/0000000000000000000000000000000-not.drv"; }; };
}
EOF
write_fixture diff/machine-without-config <<'EOF'
    { checks.${thisSystem}.g = trivial thisSystem "g"; nixosConfigurations.probe = { }; };
}
EOF
write_fixture diff/module-throws <<'EOF'
    { checks.${thisSystem}.g = trivial thisSystem "g";
      nixosModules.probe = throw "probe module throws"; };
}
EOF
write_fixture diff/module-is-an-integer <<'EOF'
    { checks.${thisSystem}.g = trivial thisSystem "g"; nixosModules.probe = 42; };
}
EOF
write_fixture diff/configurations-are-a-list <<'EOF'
    { checks.${thisSystem}.g = trivial thisSystem "g"; nixosConfigurations = [ okMachine ]; };
}
EOF
write_fixture diff/passive-output-throws <<'EOF'
    { checks.${thisSystem}.g = trivial thisSystem "g"; lib = throw "lib throws"; };
}
EOF
write_fixture diff/passive-output-is-an-integer <<'EOF'
    { checks.${thisSystem}.g = trivial thisSystem "g"; lib = 42; };
}
EOF

printf '\n%-36s %-22s %-14s %s\n' fixture 'nix flake check' 'allod check' verdict
printf '%-36s %-22s %-14s %s\n' ------- --------------- ----------- -------
for probe in green check-not-a-derivation check-is-a-list check-is-a-set-of-derivations \
  check-drvpath-throws checks-system-not-attrs machine-toplevel-not-a-derivation \
  machine-without-config module-throws module-is-an-integer configurations-are-a-list \
  passive-output-throws passive-output-is-an-integer; do
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
