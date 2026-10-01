package main

// docs/allod-check.md is the long version of this command.

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

func init() {
	registerNamespace(namespace{
		name:    "check",
		summary: "Run a flake's machines and checks, one nix process at a time",
		main:    checkMain,
	})
}

const checkUsageText = `Usage:
  allod check [--override-input <input> <flake-ref>]... [<flake-ref>]

Runs every machine, nixos module, allowed passive output and check the flake
exposes, one 'nix' process at a time, reporting all of them: a failing step does
not stop the run. The reference defaults to '.'. A check keyed on another system
is only evaluated, and a run that evaluated checks and built none is refused.

'--override-input' takes two values, as 'nix' does, and may be repeated; every
'nix' invocation that reads the flake gets all of them. The checkout is never
written to, lock file included.

An output other than nixosConfigurations, checks and nixosModules is refused
unless allowed: the flake output schema's standard names (packages,
homeModules, ...) are passive, as are 'lib' and 'vmFacts' always, and whatever
passive-outputs lists in allod-check.toml at the flake root, which is forced
and nothing more.
`

type checkOverride struct {
	input string
	ref   string
}

type checkOptions struct {
	flake     string
	overrides []checkOverride
}

func checkUsageError(format string, args ...any) {
	fmt.Fprintf(stderr, "allod: "+format+"\n", args...)
	fmt.Fprint(stderr, checkUsageText)
	exit(1)
}

func parseCheckArgs(args []string) checkOptions {
	options := checkOptions{flake: "."}
	var positional []string
	for len(args) > 0 {
		switch args[0] {
		case "--override-input":
			if len(args) < 3 || strings.HasPrefix(args[1], "-") || strings.HasPrefix(args[2], "-") {
				checkUsageError("--override-input requires an input name and a flake reference")
			}
			options.overrides = append(options.overrides, checkOverride{input: args[1], ref: args[2]})
			args = args[3:]
		case "-h", "--help":
			fmt.Fprint(stdout, checkUsageText)
			exit(0)
		case "--":
			positional = append(positional, args[1:]...)
			args = nil
		default:
			if strings.HasPrefix(args[0], "-") {
				checkUsageError("unknown option for allod check: %s", args[0])
			}
			positional = append(positional, args[0])
			args = args[1:]
		}
	}
	if len(positional) > 1 {
		checkUsageError("allod check takes at most one flake reference, got %d: %s",
			len(positional), strings.Join(positional, " "))
	}
	if len(positional) == 1 {
		options.flake = positional[0]
	}
	return options
}

type checkStepKind int

const (
	checkForcedStep checkStepKind = iota
	checkMachineStep
	checkBuiltStep
	checkEvaluatedStep
)

type checkStep struct {
	kind   checkStepKind
	label  string
	argv   []string
	system string
}

func checkMain(args []string) {
	options := parseCheckArgs(args)
	if _, err := exec.LookPath("nix"); err != nil {
		die(1, "'nix' not found on PATH")
	}

	system, status := checkCapture("nix", checkSystemArgs())
	if status != 0 || system == "" {
		die(1, "could not determine this machine's Nix system")
	}

	allowed := checkAllowedPassive(options)
	enumeration := checkEnumerate(options)

	if refused := checkRefusedOutputs(enumeration.Outputs, allowed); len(refused) > 0 {
		fmt.Fprintf(stderr, "allod: %s exposes flake output(s) allod check does not run:\n", options.flake)
		typoSuspected := false
		for _, name := range refused {
			if near, ok := checkNearestKnownName(name); ok {
				typoSuspected = true
				fmt.Fprintf(stderr, "  - %s (close to %s; probably a typo)\n", name, near)
			} else {
				fmt.Fprintf(stderr, "  - %s (not a standard output; to allow it, list it in %s = [...] in %s at the flake root)\n",
					name, checkPassiveOutputsKey, checkConfigName)
			}
		}
		if typoSuspected {
			die(1, "a name close to a standard flake output is usually a typo")
		}
		exit(1)
	}

	fmt.Fprintf(stdout, "Gate for %s on %s\n", options.flake, system)
	for _, override := range options.overrides {
		fmt.Fprintf(stdout, "Override: %s = %s\n", override.input, override.ref)
	}

	runner := &checkRunner{}
	for _, step := range checkSteps(options, enumeration, allowed, system) {
		runner.run(step)
	}
	runner.report(options.flake, system)
}

func checkSteps(options checkOptions, enumeration checkEnumeration, allowed []string, system string) []checkStep {
	var steps []checkStep
	add := func(kind checkStepKind, label, expr, stepSystem string) {
		steps = append(steps, checkStep{kind: kind, label: label, argv: options.rootArgs(expr), system: stepSystem})
	}
	for _, name := range checkPassiveOutputs(enumeration.Outputs, allowed) {
		add(checkForcedStep, "passive output "+name,
			checkForceExpr("builtins.getAttr "+nixStringLiteral(name)+" o"), "")
	}
	for _, name := range enumeration.Modules {
		add(checkForcedStep, "nixos module "+name,
			checkForceExpr("builtins.getAttr "+nixStringLiteral(name)+" o.nixosModules"), "")
	}
	for _, name := range enumeration.Machines {
		add(checkMachineStep, "machine "+name, checkDrvPathExpr("config.system.build.toplevel",
			"(builtins.getAttr "+nixStringLiteral(name)+" o.nixosConfigurations).config.system.build.toplevel"), "")
	}
	for _, entry := range enumeration.Checks {
		expr := checkDrvPathExpr("the check", "builtins.getAttr "+nixStringLiteral(entry.Name)+
			" (builtins.getAttr "+nixStringLiteral(entry.System)+" o.checks)")
		if entry.System == system {
			add(checkBuiltStep, fmt.Sprintf("check %s.%s (built)", entry.System, entry.Name), expr, entry.System)
			continue
		}
		add(checkEvaluatedStep, fmt.Sprintf("check %s.%s (evaluated, not built: %s is not %s)",
			entry.System, entry.Name, entry.System, system), expr, entry.System)
	}
	return steps
}

type checkRunner struct {
	steps           int
	covered         int
	built           int
	evaluated       int
	evaluatedSystem []string
	failed          []string
}

func (runner *checkRunner) run(step checkStep) {
	fmt.Fprintf(stdout, "\n==> %s\n", step.label)
	runner.steps++
	switch step.kind {
	case checkMachineStep:
		runner.covered++
	case checkBuiltStep:
		runner.covered++
		runner.built++
	case checkEvaluatedStep:
		runner.covered++
		runner.evaluated++
		runner.rememberSystem(step.system)
	}
	if !runner.invoke(step) {
		fmt.Fprintf(stderr, "!!! FAILED: %s\n", step.label)
		runner.failed = append(runner.failed, step.label)
	}
}

func (runner *checkRunner) invoke(step checkStep) bool {
	if step.kind != checkBuiltStep {
		return checkStream("nix", step.argv) == 0
	}
	drv, status := checkCapture("nix", step.argv)
	if status != 0 || drv == "" {
		return false
	}
	return checkStream("nix", checkBuildDrvArgs(drv)) == 0
}

// rememberSystem deduplicates by exact name, not by sorting lines: a system name
// containing a newline is one name, and a line-oriented pass counts it as two.
func (runner *checkRunner) rememberSystem(system string) {
	if !slices.Contains(runner.evaluatedSystem, system) {
		runner.evaluatedSystem = append(runner.evaluatedSystem, system)
	}
}

const checkWitnessedNothing = `Evaluation alone is not this gate. A check that fails in its builder shell rather
than in its Nix expression evaluates here and fails only on a build, so an
all-evaluated run cannot tell a passing suite from a failing one.
`

func (runner *checkRunner) report(flake, system string) {
	if runner.covered == 0 {
		die(1, "found no machine and no check to run in %s", flake)
	}

	if len(runner.failed) > 0 {
		fmt.Fprintf(stderr, "\nFailed steps (%d of %d):\n", len(runner.failed), runner.steps)
		for _, label := range runner.failed {
			fmt.Fprintf(stderr, "  - %s\n", label)
		}
		exit(1)
	}

	systems := strings.Join(runner.evaluatedSystem, ",")
	if runner.built == 0 && runner.evaluated > 0 {
		fmt.Fprintf(stderr, "\nWitnessed nothing: evaluated %d of %d checks and built none of them.\n",
			runner.evaluated, runner.evaluated)
		fmt.Fprint(stderr, checkWitnessedNothing)
		fmt.Fprintf(stderr, "This host is %s; run the gate on %s, the system these checks are keyed on.\n",
			system, systems)
		exit(1)
	}

	if runner.evaluated == 0 {
		fmt.Fprintf(stdout, "\nAll %d steps passed (checks: %d built).\n", runner.steps, runner.built)
		return
	}
	fmt.Fprintf(stdout, "\nAll %d steps passed (checks: %d built, %d evaluated only).\n",
		runner.steps, runner.built, runner.evaluated)
	fmt.Fprintf(stdout, "The %d evaluated-only check(s) are for %s and need a host of that system to be built.\n",
		runner.evaluated, systems)
}
