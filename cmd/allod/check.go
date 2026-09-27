package main

// The 'check' namespace is the workspace's gate, in place of `nix flake check`:
// it runs a flake's machines, modules, passive outputs and checks one `nix`
// process at a time, reports every failing step in one run, and refuses a run
// that witnessed nothing. It reads a flake and builds its checks; it writes
// nothing. docs/allod-check.md is the long version.

import (
	"fmt"
	"os/exec"
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
exposes, one 'nix' process at a time, and reports all of them: a failing step
does not stop the run. The flake reference defaults to '.'.

A check keyed on this machine's Nix system is built; one keyed on another
system can only be evaluated, and a run that built no check at all is refused
rather than reported green.

'--override-input <input> <flake-ref>' takes two values, as 'nix' does, and
may be repeated. Every 'nix' invocation that reads the flake gets all of them,
so the whole gate runs against the overridden inputs, and the checkout's
flake.lock is left untouched.

An output that is neither run (nixosConfigurations, checks, nixosModules) nor
allowed is refused before any step runs. Allowed are 'lib' and 'vmFacts', plus
whatever 'passive-outputs' lists in allod-check.toml at the flake root:

  passive-outputs = ["profilesSource", "secretsSource"]
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
			if len(args) < 3 {
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

// checkStep is one step of the gate: one label in the transcript and one `nix`
// invocation, or two when the check's name cannot be spelled in an installable
// and its derivation has to be evaluated before it can be built.
type checkStep struct {
	label string
	argv  []string
	// buildDrv turns argv into an evaluation that prints a derivation path,
	// which the step then builds.
	buildDrv bool
	// covered marks a step that makes the run meaningful: a machine or a
	// check. A passive output or a module does not.
	covered bool
	// system is set on a check step, and built says whether it is built or
	// only evaluated.
	system string
	built  bool
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

	enumeration, ok := checkEnumerate(options)
	if !ok {
		die(1, "could not enumerate what to run in %s", options.flake)
	}

	if refused := checkRefusedOutputs(enumeration.Outputs, allowed); len(refused) > 0 {
		fmt.Fprintf(stderr, "allod: %s exposes flake output(s) allod check does not run:\n", options.flake)
		for _, name := range refused {
			fmt.Fprintf(stderr, "  - %s\n", name)
		}
		die(1, "an unknown output is usually a typo; to allow one deliberately, list it in %s = [...] in %s at the flake root",
			checkPassiveOutputsKey, checkConfigName)
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

// checkSteps is the whole of what the gate runs, in the order it runs it:
// passive outputs, modules, machines, checks.
func checkSteps(options checkOptions, enumeration checkEnumeration, allowed []string, system string) []checkStep {
	var steps []checkStep
	for _, name := range checkPassiveOutputs(enumeration.Outputs, allowed) {
		steps = append(steps, checkStep{
			label: "passive output " + name,
			argv:  checkPassiveArgs(options, name),
		})
	}
	for _, name := range enumeration.Modules {
		steps = append(steps, checkStep{
			label: "nixos module " + name,
			argv:  checkModuleArgs(options, name),
		})
	}
	for _, name := range enumeration.Machines {
		steps = append(steps, checkStep{
			label:   "machine " + name,
			argv:    checkMachineArgs(options, name),
			covered: true,
		})
	}
	for _, entry := range enumeration.Checks {
		if entry.System == system {
			argv, buildDrv := checkBuiltArgs(options, entry)
			steps = append(steps, checkStep{
				label:    fmt.Sprintf("check %s.%s (built)", entry.System, entry.Name),
				argv:     argv,
				buildDrv: buildDrv,
				covered:  true,
				system:   entry.System,
				built:    true,
			})
			continue
		}
		steps = append(steps, checkStep{
			label: fmt.Sprintf("check %s.%s (evaluated, not built: %s is not %s)",
				entry.System, entry.Name, entry.System, system),
			argv:    checkEvaluatedArgs(options, entry),
			covered: true,
			system:  entry.System,
		})
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
	if step.covered {
		runner.covered++
	}
	if step.system != "" {
		if step.built {
			runner.built++
		} else {
			runner.evaluated++
			runner.rememberSystem(step.system)
		}
	}
	if !runner.invoke(step) {
		fmt.Fprintf(stderr, "!!! FAILED: %s\n", step.label)
		runner.failed = append(runner.failed, step.label)
	}
}

func (runner *checkRunner) invoke(step checkStep) bool {
	if !step.buildDrv {
		return checkStream("nix", step.argv) == 0
	}
	drv, status := checkCapture("nix", step.argv)
	if status != 0 || drv == "" {
		return false
	}
	return checkStream("nix", checkBuildDrvArgs(drv)) == 0
}

// rememberSystem deduplicates by exact name rather than sorting lines: a system
// name containing a newline is one name, and a line-oriented pass would count
// it as two.
func (runner *checkRunner) rememberSystem(system string) {
	for _, seen := range runner.evaluatedSystem {
		if seen == system {
			return
		}
	}
	runner.evaluatedSystem = append(runner.evaluatedSystem, system)
}

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

	// Evaluation catches a failure written in a check's Nix expression, where
	// most of them live; it does not catch one written into a builder's shell.
	// An all-evaluated run therefore cannot tell a passing suite from a
	// failing one, and reporting it green is the false green this refusal
	// exists for.
	if runner.built == 0 && runner.evaluated > 0 {
		fmt.Fprintf(stderr, "\nWitnessed nothing: evaluated %d of %d checks and built none of them.\n",
			runner.evaluated, runner.evaluated)
		fmt.Fprint(stderr, "Evaluation alone is not this gate. A check that fails in its builder shell rather\n")
		fmt.Fprint(stderr, "than in its Nix expression evaluates here and fails only on a build, so an\n")
		fmt.Fprint(stderr, "all-evaluated run cannot tell a passing suite from a failing one.\n")
		fmt.Fprintf(stderr, "This host is %s; run the gate on %s, the system these checks are keyed on.\n",
			system, systems)
		exit(1)
	}

	if runner.evaluated > 0 {
		fmt.Fprintf(stdout, "\nAll %d steps passed (checks: %d built, %d evaluated only).\n",
			runner.steps, runner.built, runner.evaluated)
		fmt.Fprintf(stdout, "The %d evaluated-only check(s) are for %s and need a host of that system to be built.\n",
			runner.evaluated, systems)
		return
	}
	fmt.Fprintf(stdout, "\nAll %d steps passed (checks: %d built).\n", runner.steps, runner.built)
}
