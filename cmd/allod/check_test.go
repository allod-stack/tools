package main

// Tests for 'allod check'. The seams they swap (checkStream, checkCapture) sit
// at the exec boundary, so every argv these tests pin is the one production
// code built, not one a test reimplemented. tests/allod-check-selftest.sh is
// the other half: it runs the real nix, which the sandbox has no room for.
//
// The seams are package-level mutable state, so no test here calls t.Parallel.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Harness ---

type checkStub struct {
	system    string
	outputs   string
	metadata  string
	drv       string
	failWhen  func(args []string) bool
	captured  [][]string
	streamed  [][]string
	enumFails bool
}

func (stub *checkStub) capture(args []string) (string, int) {
	stub.captured = append(stub.captured, args)
	switch {
	case args[len(args)-1] == "builtins.currentSystem":
		return stub.system, 0
	case args[0] == "flake":
		return stub.metadata, 0
	case len(args) > 1 && args[1] == "--json":
		if stub.enumFails {
			return "", 1
		}
		return stub.outputs, 0
	}
	if stub.failWhen != nil && stub.failWhen(args) {
		return "", 1
	}
	return stub.drv, 0
}

func useCheckStub(t *testing.T, stub *checkStub) *checkStub {
	t.Helper()
	if stub.system == "" {
		stub.system = "x86_64-linux"
	}
	if stub.drv == "" {
		stub.drv = "/nix/store/0000000000000000000000000000000-fixture.drv"
	}
	previousCapture, previousStream := checkCapture, checkStream
	checkCapture = func(name string, args []string) (string, int) {
		if name != "nix" {
			t.Errorf("captured command = %q, want nix", name)
		}
		return stub.capture(args)
	}
	checkStream = func(name string, args []string) int {
		if name != "nix" {
			t.Errorf("streamed command = %q, want nix", name)
		}
		stub.streamed = append(stub.streamed, args)
		if stub.failWhen != nil && stub.failWhen(args) {
			return 1
		}
		return 0
	}
	t.Cleanup(func() { checkCapture, checkStream = previousCapture, previousStream })
	stubTools(t, "nix")
	t.Chdir(t.TempDir())
	return stub
}

// --- Argument parsing ---

func TestCheckArgsDefaults(t *testing.T) {
	options := parseCheckArgs(nil)
	if options.flake != "." {
		t.Errorf("flake = %q, want .", options.flake)
	}
	if len(options.overrides) != 0 {
		t.Errorf("overrides = %v, want none", options.overrides)
	}
}

func TestCheckArgsPositionalFlake(t *testing.T) {
	options := parseCheckArgs([]string{"path:/tmp/fixture"})
	if options.flake != "path:/tmp/fixture" {
		t.Errorf("flake = %q, want path:/tmp/fixture", options.flake)
	}
}

func TestCheckArgsOverridesRepeatAndKeepOrder(t *testing.T) {
	options := parseCheckArgs([]string{
		"--override-input", "secrets", "path:/fixture/secrets",
		"--override-input", "profiles", "path:/fixture/profiles",
		"/repo",
	})
	if options.flake != "/repo" {
		t.Errorf("flake = %q, want /repo", options.flake)
	}
	want := []checkOverride{
		{input: "secrets", ref: "path:/fixture/secrets"},
		{input: "profiles", ref: "path:/fixture/profiles"},
	}
	if len(options.overrides) != len(want) {
		t.Fatalf("overrides = %v, want %v", options.overrides, want)
	}
	for index := range want {
		if options.overrides[index] != want[index] {
			t.Errorf("override %d = %v, want %v", index, options.overrides[index], want[index])
		}
	}
}

func TestCheckArgsOverrideAfterFlake(t *testing.T) {
	options := parseCheckArgs([]string{"/repo", "--override-input", "data", "path:/fixture"})
	if options.flake != "/repo" || len(options.overrides) != 1 {
		t.Errorf("parsed = %+v, want /repo with one override", options)
	}
}

func TestCheckArgsDoubleDashEndsOptions(t *testing.T) {
	options := parseCheckArgs([]string{"--", "-weird-ref"})
	if options.flake != "-weird-ref" {
		t.Errorf("flake = %q, want -weird-ref", options.flake)
	}
}

func TestCheckArgsErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing override values", []string{"--override-input", "data"}, "--override-input requires an input name and a flake reference"},
		{"no override values", []string{"--override-input"}, "--override-input requires an input name and a flake reference"},
		{"unknown option", []string{"--all-systems"}, "unknown option for allod check: --all-systems"},
		{"two flake refs", []string{"/one", "/two"}, "takes at most one flake reference"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			useCheckStub(t, &checkStub{})
			_, errText, code := runAllod(t, append([]string{"check"}, testCase.args...)...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(errText, testCase.want) {
				t.Errorf("stderr does not contain %q\ngot: %q", testCase.want, errText)
			}
			if !strings.Contains(errText, "Usage:\n  allod check") {
				t.Errorf("stderr does not repeat the usage\ngot: %q", errText)
			}
		})
	}
}

func TestCheckHelpPrintsUsage(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		stub := useCheckStub(t, &checkStub{})
		out, errText, code := runAllod(t, "check", flag)
		if code != 0 {
			t.Errorf("%s: exit code = %d, want 0", flag, code)
		}
		if errText != "" {
			t.Errorf("%s: stderr = %q, want empty", flag, errText)
		}
		if !strings.Contains(out, "--override-input") {
			t.Errorf("%s: usage does not document --override-input\ngot: %q", flag, out)
		}
		if len(stub.captured)+len(stub.streamed) != 0 {
			t.Errorf("%s: ran nix %d times, want 0", flag, len(stub.captured)+len(stub.streamed))
		}
	}
}

func TestCheckIsRegistered(t *testing.T) {
	entry, ok := lookupNamespace("check")
	if !ok {
		t.Fatal("the check namespace is not registered")
	}
	if entry.summary == "" {
		t.Error("the check namespace has no summary for the usage text")
	}
}

// --- The argv of every kind of step ---

func TestCheckSystemArgv(t *testing.T) {
	want := []string{"eval", "--raw", "--impure", "--expr", "builtins.currentSystem"}
	if got := checkSystemArgs(); !equalArgs(got, want) {
		t.Errorf("checkSystemArgs = %v, want %v", got, want)
	}
}

func TestCheckStepArgvWithoutOverrides(t *testing.T) {
	options := checkOptions{flake: "."}
	entry := checkEntry{System: "x86_64-linux", Name: "green-local"}

	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"enumeration", checkEnumerateArgs(options),
			[]string{"eval", "--json", ".#.", "--apply", checkEnumerationExpr}},
		{"metadata", checkMetadataArgs(options),
			[]string{"flake", "metadata", "--json", "."}},
		{"passive output", checkPassiveArgs(options, "vmFacts"),
			[]string{"eval", "--raw", `.#"vmFacts"`, "--apply", checkForceExpr}},
		{"module", checkModuleArgs(options, "green-module"),
			[]string{"eval", "--raw", `.#nixosModules."green-module"`, "--apply", checkForceExpr}},
		{"machine", checkMachineArgs(options, "green.machine"),
			[]string{"eval", "--raw", `.#nixosConfigurations."green.machine".config.system.build.toplevel`,
				"--apply", checkMachineExpr}},
		{"evaluated check", checkEvaluatedArgs(options, entry),
			[]string{"eval", "--raw", `.#checks."x86_64-linux"."green-local".drvPath`}},
		{"build a derivation path", checkBuildDrvArgs("/nix/store/aaa-fixture.drv"),
			[]string{"build", "--no-link", "/nix/store/aaa-fixture.drv^*"}},
	}
	for _, testCase := range cases {
		if !equalArgs(testCase.got, testCase.want) {
			t.Errorf("%s argv = %v, want %v", testCase.name, testCase.got, testCase.want)
		}
	}

	argv, buildDrv := checkBuiltArgs(options, entry)
	if buildDrv {
		t.Error("a check with a spellable name should be built in one nix process")
	}
	want := []string{"build", "--no-link", `.#checks."x86_64-linux"."green-local"`}
	if !equalArgs(argv, want) {
		t.Errorf("built check argv = %v, want %v", argv, want)
	}
}

// TestCheckStepArgvWithOverrides pins the one property the whole override
// feature rests on: every invocation that reads the flake carries every
// override, and none of them may write the checkout's lock file.
func TestCheckStepArgvWithOverrides(t *testing.T) {
	options := checkOptions{flake: "/repo", overrides: []checkOverride{
		{input: "secrets", ref: "path:/fixture/secrets"},
		{input: "profiles", ref: "path:/fixture/profiles"},
	}}
	entry := checkEntry{System: "aarch64-linux", Name: "green-foreign"}
	builtArgv, _ := checkBuiltArgs(options, entry)
	unsafeArgv, buildDrv := checkBuiltArgs(options, checkEntry{System: "x86_64-linux", Name: "bad\tname"})
	if !buildDrv {
		t.Error("a check whose name cannot be spelled must be evaluated before it is built")
	}

	tail := []string{
		"--override-input", "secrets", "path:/fixture/secrets",
		"--override-input", "profiles", "path:/fixture/profiles",
		"--no-write-lock-file",
	}
	for name, argv := range map[string][]string{
		"enumeration":     checkEnumerateArgs(options),
		"metadata":        checkMetadataArgs(options),
		"passive output":  checkPassiveArgs(options, "vmFacts"),
		"module":          checkModuleArgs(options, "green-module"),
		"machine":         checkMachineArgs(options, "green-machine"),
		"built check":     builtArgv,
		"unsafe check":    unsafeArgv,
		"evaluated check": checkEvaluatedArgs(options, entry),
	} {
		if len(argv) < len(tail) || !equalArgs(argv[len(argv)-len(tail):], tail) {
			t.Errorf("%s argv = %v, want it to end with %v", name, argv, tail)
		}
	}

	if got := checkBuildDrvArgs("/nix/store/aaa-fixture.drv"); len(got) != 3 {
		t.Errorf("building a derivation path takes no flake flags, got %v", got)
	}
}

// TestCheckUnsafeStepArgvEmbedsTheName covers the route for a name that cannot
// be spelled in an installable: the whole output set plus the name as a Nix
// string literal.
func TestCheckUnsafeStepArgvEmbedsTheName(t *testing.T) {
	options := checkOptions{flake: "."}
	cases := []struct {
		name  string
		argv  []string
		wants []string
	}{
		{"passive output", checkPassiveArgs(options, "bad\"name"),
			[]string{`.#.`, `builtins.getAttr "bad\"name" o`}},
		{"module", checkModuleArgs(options, "bad\nname"),
			[]string{`.#.`, `builtins.getAttr "bad\nname" o.nixosModules`}},
		{"machine", checkMachineArgs(options, "bad\tname"),
			[]string{`.#.`, `builtins.getAttr "bad\tname" o.nixosConfigurations`, "is not a derivation"}},
		{"check", checkDrvPathArgs(options, checkEntry{System: "x86_64-linux", Name: "bad${name}"}),
			[]string{`.#.`, `builtins.getAttr "bad\${name}"`, `builtins.getAttr "x86_64-linux" o.checks`}},
	}
	for _, testCase := range cases {
		joined := strings.Join(testCase.argv, " ")
		for _, want := range testCase.wants {
			if !strings.Contains(joined, want) {
				t.Errorf("%s argv does not contain %q\ngot: %s", testCase.name, want, joined)
			}
		}
	}
}

// TestCheckInstallableSafe pins the measured set: on Nix 2.34.8 the flake
// reference and the fragment are parsed as one URL, and these are the ASCII
// characters that breaks it whatever the quoting.
func TestCheckInstallableSafe(t *testing.T) {
	for _, name := range []string{"green-local", "green.local", "sp ace", "hash#mark", "dollar$sign", "a:b,c=d", "x86_64-linux"} {
		if !checkInstallableSafe(name) {
			t.Errorf("checkInstallableSafe(%q) = false, want true", name)
		}
	}
	for _, unsafe := range []string{"\x01", "\t", "\n", "\r", "\x1f", "\x7f", `"`, "%", "<", ">", "?", "[", `\`, "]", "^", "`", "{", "|", "}", "é"} {
		if checkInstallableSafe("a" + unsafe + "b") {
			t.Errorf("checkInstallableSafe(%q) = true, want false", "a"+unsafe+"b")
		}
	}
	if checkInstallableSafe("") {
		t.Error("checkInstallableSafe(\"\") = true, want false")
	}
}

func TestNixStringLiteral(t *testing.T) {
	cases := map[string]string{
		"plain":          `"plain"`,
		"quo\"te":        `"quo\"te"`,
		"back\\slash":    `"back\\slash"`,
		"tab\there":      `"tab\there"`,
		"nl\nhere":       `"nl\nhere"`,
		"cr\rhere":       `"cr\rhere"`,
		"anti${plain}":   `"anti\${plain}"`,
		"dollar$alone":   `"dollar$alone"`,
		"trailing$":      `"trailing$"`,
		"both\\${x}\"y":  `"both\\\${x}\"y"`,
		"bad\tchecks.\"": `"bad\tchecks.\""`,
	}
	for input, want := range cases {
		if got := nixStringLiteral(input); got != want {
			t.Errorf("nixStringLiteral(%q) = %s, want %s", input, got, want)
		}
	}
}

// --- The accounting ---

const checkGreenOutputs = `{
  "outputs": ["checks", "nixosConfigurations"],
  "machines": ["green-machine"],
  "modules": [],
  "checks": [
    {"system": "x86_64-linux", "name": "green-local"},
    {"system": "aarch64-linux", "name": "green-foreign"}
  ]
}`

func TestCheckGreenRun(t *testing.T) {
	stub := useCheckStub(t, &checkStub{outputs: checkGreenOutputs})
	out, errText, code := runAllod(t, "check")
	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	for _, want := range []string{
		"Gate for . on x86_64-linux\n",
		"==> machine green-machine\n",
		"==> check x86_64-linux.green-local (built)\n",
		"==> check aarch64-linux.green-foreign (evaluated, not built: aarch64-linux is not x86_64-linux)\n",
		"All 3 steps passed (checks: 1 built, 1 evaluated only).\n",
		"The 1 evaluated-only check(s) are for aarch64-linux and need a host of that system to be built.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not contain %q\ngot: %s", want, out)
		}
	}
	if errText != "" {
		t.Errorf("stderr = %q, want empty", errText)
	}
	if got := len(stub.streamed); got != 3 {
		t.Errorf("ran %d nix steps, want 3: %v", got, stub.streamed)
	}
}

func TestCheckReportsEveryFailure(t *testing.T) {
	stub := useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["checks", "nixosConfigurations"],
		           "machines": ["red-machine", "green-machine"],
		           "modules": [],
		           "checks": [{"system": "x86_64-linux", "name": "red-local"},
		                      {"system": "x86_64-linux", "name": "green-local"}]}`,
		failWhen: func(args []string) bool { return strings.Contains(strings.Join(args, " "), "red") },
	})
	out, errText, code := runAllod(t, "check")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{
		"!!! FAILED: machine red-machine\n",
		"!!! FAILED: check x86_64-linux.red-local (built)\n",
		"\nFailed steps (2 of 4):\n",
		"  - machine red-machine\n",
		"  - check x86_64-linux.red-local (built)\n",
	} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr does not contain %q\ngot: %s", want, errText)
		}
	}
	if strings.Contains(errText, "green") {
		t.Errorf("stderr blames a green step\ngot: %s", errText)
	}
	if strings.Contains(out, "steps passed") {
		t.Errorf("stdout reports success\ngot: %s", out)
	}
	if got := len(stub.streamed); got != 4 {
		t.Errorf("ran %d nix steps, want all 4 despite the failures: %v", got, stub.streamed)
	}
}

func TestCheckNothingToRun(t *testing.T) {
	stub := useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["checks", "nixosConfigurations"], "machines": [], "modules": [], "checks": []}`,
	})
	out, errText, code := runAllod(t, "check")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "found no machine and no check to run in ."; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %s", want, errText)
	}
	if strings.Contains(out, "steps passed") {
		t.Errorf("stdout reports success\ngot: %s", out)
	}
	if len(stub.streamed) != 0 {
		t.Errorf("ran %d nix steps, want 0: %v", len(stub.streamed), stub.streamed)
	}
}

// TestCheckModulesAloneAreNotAGate covers the counter's reason for existing: a
// flake whose only steps are modules and passive outputs has been read, not
// gated.
func TestCheckModulesAloneAreNotAGate(t *testing.T) {
	stub := useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["nixosModules", "lib"], "machines": [], "modules": ["green-module"], "checks": []}`,
	})
	_, errText, code := runAllod(t, "check")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "found no machine and no check to run"; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %s", want, errText)
	}
	if got := len(stub.streamed); got != 2 {
		t.Errorf("ran %d nix steps, want the passive output and the module: %v", got, stub.streamed)
	}
}

func TestCheckWitnessedNothing(t *testing.T) {
	useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["checks", "nixosConfigurations"],
		           "machines": ["green-machine"],
		           "modules": [],
		           "checks": [{"system": "aarch64-linux", "name": "green-foreign"},
		                      {"system": "aarch64-linux", "name": "shell-only-failure"}]}`,
	})
	out, errText, code := runAllod(t, "check")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{
		"Witnessed nothing: evaluated 2 of 2 checks and built none of them.\n",
		"This host is x86_64-linux; run the gate on aarch64-linux, the system these checks are keyed on.\n",
	} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr does not contain %q\ngot: %s", want, errText)
		}
	}
	if strings.Contains(errText, "!!! FAILED") {
		t.Errorf("a step failed, so this is not the refusal under test\ngot: %s", errText)
	}
	if strings.Contains(out, "steps passed") {
		t.Errorf("stdout reports success\ngot: %s", out)
	}
}

// TestCheckWitnessedNothingNamesEachSystemOnce pins the deduplication of the
// evaluated systems by exact name.
func TestCheckWitnessedNothingNamesEachSystemOnce(t *testing.T) {
	useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["checks"], "machines": [], "modules": [],
		           "checks": [{"system": "aarch64-linux", "name": "one"},
		                      {"system": "aarch64-linux", "name": "two"},
		                      {"system": "riscv64-linux", "name": "three"}]}`,
	})
	_, errText, _ := runAllod(t, "check")
	if want := "run the gate on aarch64-linux,riscv64-linux,"; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %s", want, errText)
	}
}

func TestCheckBuiltAndEvaluatedTogether(t *testing.T) {
	useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["checks"], "machines": [], "modules": [],
		           "checks": [{"system": "x86_64-linux", "name": "local"},
		                      {"system": "aarch64-linux", "name": "foreign"}]}`,
	})
	out, _, code := runAllod(t, "check")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if want := "All 2 steps passed (checks: 1 built, 1 evaluated only).\n"; !strings.Contains(out, want) {
		t.Errorf("stdout does not contain %q\ngot: %s", want, out)
	}
}

// TestCheckUnsafeCheckBuildsWhatItEvaluated covers the two-process route: the
// derivation path the evaluation printed is what the build is handed.
func TestCheckUnsafeCheckBuildsWhatItEvaluated(t *testing.T) {
	stub := useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["checks"], "machines": [], "modules": [],
		           "checks": [{"system": "x86_64-linux", "name": "bad\tname"}]}`,
		drv: "/nix/store/1111111111111111111111111111111-tabbed.drv",
	})
	out, errText, code := runAllod(t, "check")
	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if want := "==> check x86_64-linux.bad\tname (built)\n"; !strings.Contains(out, want) {
		t.Errorf("stdout does not contain %q\ngot: %q", want, out)
	}
	if len(stub.streamed) != 1 {
		t.Fatalf("streamed %v, want one build", stub.streamed)
	}
	want := []string{"build", "--no-link", "/nix/store/1111111111111111111111111111111-tabbed.drv^*"}
	if !equalArgs(stub.streamed[0], want) {
		t.Errorf("build argv = %v, want %v", stub.streamed[0], want)
	}
}

func TestCheckUnsafeCheckFailsWithoutBuilding(t *testing.T) {
	stub := useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["checks"], "machines": [], "modules": [],
		           "checks": [{"system": "x86_64-linux", "name": "bad\tname"}]}`,
		failWhen: func(args []string) bool { return strings.Contains(strings.Join(args, " "), "getAttr") },
	})
	_, errText, code := runAllod(t, "check")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "!!! FAILED: check x86_64-linux.bad\tname (built)"; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
	}
	if len(stub.streamed) != 0 {
		t.Errorf("built %v after the evaluation failed, want nothing", stub.streamed)
	}
}

func TestCheckEnumerationFailureStopsTheRun(t *testing.T) {
	stub := useCheckStub(t, &checkStub{enumFails: true})
	_, errText, code := runAllod(t, "check")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "could not enumerate what to run in ."; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %s", want, errText)
	}
	if len(stub.streamed) != 0 {
		t.Errorf("ran %d steps after a failed enumeration, want 0", len(stub.streamed))
	}
}

func TestCheckUnexpectedEnumerationJSON(t *testing.T) {
	useCheckStub(t, &checkStub{outputs: `{"outputs": 42}`})
	_, errText, code := runAllod(t, "check")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "unexpected enumeration JSON from ."; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %s", want, errText)
	}
}

// --- Allowed and refused outputs ---

func TestCheckRefusesOutputsItDoesNotRun(t *testing.T) {
	stub := useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["checks", "chekcs", "packages", "vmFacts"],
		           "machines": [], "modules": [],
		           "checks": [{"system": "x86_64-linux", "name": "green-local"}]}`,
	})
	out, errText, code := runAllod(t, "check")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{
		"exposes flake output(s) allod check does not run:\n",
		"  - chekcs\n",
		"  - packages\n",
		"allod-check.toml",
	} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr does not contain %q\ngot: %s", want, errText)
		}
	}
	if strings.Contains(errText, "vmFacts") {
		t.Errorf("stderr refuses an allowed passive output\ngot: %s", errText)
	}
	if strings.Contains(out, "==> ") {
		t.Errorf("a step ran before the refusal\ngot: %s", out)
	}
	if len(stub.streamed) != 0 {
		t.Errorf("ran %d steps before the refusal, want 0", len(stub.streamed))
	}
}

func TestCheckForcesAllowedPassiveOutputs(t *testing.T) {
	useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["vmFacts", "lib", "checks"], "machines": [], "modules": [],
		           "checks": [{"system": "x86_64-linux", "name": "green-local"}]}`,
	})
	out, _, code := runAllod(t, "check")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	for _, want := range []string{"==> passive output lib\n", "==> passive output vmFacts\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not contain %q\ngot: %s", want, out)
		}
	}
	if want := "All 3 steps passed (checks: 1 built).\n"; !strings.Contains(out, want) {
		t.Errorf("stdout does not contain %q\ngot: %s", want, out)
	}
}

func TestCheckConfigAllowsAnOutput(t *testing.T) {
	useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["checks", "profilesSource"], "machines": [], "modules": [],
		           "checks": [{"system": "x86_64-linux", "name": "green-local"}]}`,
	})
	writeCheckConfig(t, "passive-outputs = [\"profilesSource\"]\n")
	out, errText, code := runAllod(t, "check")
	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if want := "==> passive output profilesSource\n"; !strings.Contains(out, want) {
		t.Errorf("stdout does not contain %q\ngot: %s", want, out)
	}
}

func TestCheckConfigDoesNotInventOutputs(t *testing.T) {
	useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["checks"], "machines": [], "modules": [],
		           "checks": [{"system": "x86_64-linux", "name": "green-local"}]}`,
	})
	writeCheckConfig(t, "passive-outputs = [\"profilesSource\", \"secretsSource\"]\n")
	out, _, code := runAllod(t, "check")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if strings.Contains(out, "passive output") {
		t.Errorf("an allowed output the flake does not have became a step\ngot: %s", out)
	}
}

func TestCheckConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"unknown key", "passive-output = [\"a\"]\n", "unknown key 'passive-output'"},
		{"table", "[repo]\npassive-outputs = [\"a\"]\n", "[table] header is not accepted"},
		{"not a list", "passive-outputs = \"profilesSource\"\n", "must be a list of quoted strings"},
		{"unterminated list", "passive-outputs = [\"a\"\n", "never closed with ']'"},
		{"unterminated string", "passive-outputs = [\"a]\n", "string is never closed"},
		{"bare word", "passive-outputs = [profilesSource]\n", "expected a quoted output name"},
		{"twice", "passive-outputs = [\"a\"]\npassive-outputs = [\"b\"]\n", "set more than once"},
		{"empty name", "passive-outputs = [\"\"]\n", "cannot be empty"},
		{"a handled output", "passive-outputs = [\"checks\"]\n", "which allod check runs itself"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stub := useCheckStub(t, &checkStub{outputs: checkGreenOutputs})
			path := writeCheckConfig(t, testCase.text)
			_, errText, code := runAllod(t, "check")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(errText, testCase.want) {
				t.Errorf("stderr does not contain %q\ngot: %s", testCase.want, errText)
			}
			if !strings.Contains(errText, path) {
				t.Errorf("stderr does not name %s\ngot: %s", path, errText)
			}
			if len(stub.streamed) != 0 {
				t.Errorf("ran %d steps despite a bad %s, want 0", len(stub.streamed), checkConfigName)
			}
		})
	}
}

func TestParseCheckConfigAccepts(t *testing.T) {
	cases := map[string][]string{
		"":                                     nil,
		"# just a comment\n":                   nil,
		"passive-outputs = []\n":               nil,
		"passive-outputs = [\"a\"]":            {"a"},
		"passive-outputs = ['a', \"b\"] # x\n": {"a", "b"},
		"passive-outputs = [\n  \"a\",\n  # a comment\n  \"b\",\n]\n": {"a", "b"},
		"passive-outputs=[\"a\",\"a\"]\n":                             {"a"},
	}
	for text, want := range cases {
		got, err := parseCheckConfig(text)
		if err != nil {
			t.Errorf("parseCheckConfig(%q) errored: %s", text, err)
			continue
		}
		if len(got) != len(want) {
			t.Errorf("parseCheckConfig(%q) = %v, want %v", text, got, want)
			continue
		}
		for index := range want {
			if got[index] != want[index] {
				t.Errorf("parseCheckConfig(%q) = %v, want %v", text, got, want)
			}
		}
	}
}

// TestCheckConfigComesFromTheFlakeSource covers a reference that is not a
// directory: the file is read from the source path nix reports, so it comes
// from the revision being checked.
func TestCheckConfigComesFromTheFlakeSource(t *testing.T) {
	source := t.TempDir()
	stub := useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["checks", "profilesSource"], "machines": [], "modules": [],
		           "checks": [{"system": "x86_64-linux", "name": "green-local"}]}`,
	})
	stub.metadata = `{"path": "` + source + `"}`
	if err := os.WriteFile(filepath.Join(source, checkConfigName), []byte("passive-outputs = [\"profilesSource\"]\n"), 0644); err != nil {
		t.Fatalf("could not write %s: %v", checkConfigName, err)
	}
	out, errText, code := runAllod(t, "check", "path:/somewhere/else")
	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if want := "==> passive output profilesSource\n"; !strings.Contains(out, want) {
		t.Errorf("stdout does not contain %q\ngot: %s", want, out)
	}
}

func writeCheckConfig(t *testing.T, text string) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not read the current directory: %v", err)
	}
	path := filepath.Join(directory, checkConfigName)
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatalf("could not write %s: %v", path, err)
	}
	return checkConfigName
}
