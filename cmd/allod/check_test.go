package main

// The seams these tests swap are package-level mutable state, so no test here
// calls t.Parallel. What nix itself does with an expression is
// tests/allod-check-selftest.sh's job.

import (
	"slices"
	"strings"
	"testing"
)

// captureDie runs something that must stop the run, and returns what it printed.
func captureDie(t *testing.T, run func()) (text string) {
	t.Helper()
	var out strings.Builder
	restore := swapStreams(&out, &out)
	defer func() {
		restore()
		text = out.String()
		if recovered := recover(); recovered == nil {
			t.Error("the run was not stopped")
		}
	}()
	run()
	return ""
}

type checkStub struct {
	outputs  string
	failWhen string
	captured [][]string
	streamed [][]string
}

func useCheckStub(t *testing.T, stub *checkStub) *checkStub {
	t.Helper()
	previousCapture, previousStream := checkCapture, checkStream
	checkCapture = func(name string, args []string) (string, int) {
		stub.captured = append(stub.captured, args)
		joined := strings.Join(args, " ")
		switch {
		case slices.Contains(args, "builtins.currentSystem"):
			return "x86_64-linux", 0
		case args[0] == "flake":
			// A source path nothing can read, so no allod-check.toml is found.
			return `{"path": "/nix/store/0000-source"}`, 0
		case args[1] == "--json":
			return stub.outputs, 0
		}
		if stub.failWhen != "" && strings.Contains(joined, stub.failWhen) {
			return "", 1
		}
		return "/nix/store/0000-fixture.drv", 0
	}
	checkStream = func(name string, args []string) int {
		stub.streamed = append(stub.streamed, args)
		if stub.failWhen != "" && strings.Contains(strings.Join(args, " "), stub.failWhen) {
			return 1
		}
		return 0
	}
	t.Cleanup(func() { checkCapture, checkStream = previousCapture, previousStream })
	stubTools(t, "nix")
	t.Chdir(t.TempDir())
	return stub
}

func TestCheckArgs(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		flake     string
		overrides []checkOverride
	}{
		{"default reference", nil, ".", nil},
		{"positional reference", []string{"path:/fixture"}, "path:/fixture", nil},
		{"-- ends option parsing", []string{"--", "-weird"}, "-weird", nil},
		{"overrides repeat and keep order", []string{
			"--override-input", "secrets", "path:/s",
			"--override-input", "profiles", "path:/p", "/repo",
		}, "/repo", []checkOverride{{"secrets", "path:/s"}, {"profiles", "path:/p"}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			options := parseCheckArgs(testCase.args)
			if options.flake != testCase.flake {
				t.Errorf("flake = %q, want %q", options.flake, testCase.flake)
			}
			if len(options.overrides) != len(testCase.overrides) {
				t.Fatalf("overrides = %v, want %v", options.overrides, testCase.overrides)
			}
			for index := range testCase.overrides {
				if options.overrides[index] != testCase.overrides[index] {
					t.Errorf("override %d = %v, want %v", index, options.overrides[index], testCase.overrides[index])
				}
			}
		})
	}
}

func TestCheckArgsErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing override reference", []string{"--override-input", "data"}, "--override-input requires"},
		{"an option as the override reference", []string{"--override-input", "data", "--help", "."}, "--override-input requires"},
		{"an option as the input name", []string{"--override-input", "--help", "path:/d"}, "--override-input requires"},
		{"unknown option", []string{"--all-systems"}, "unknown option for allod check: --all-systems"},
		{"two references", []string{"/one", "/two"}, "takes at most one flake reference"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stub := useCheckStub(t, &checkStub{})
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
			if len(stub.captured)+len(stub.streamed) != 0 {
				t.Error("a usage error ran nix")
			}
		})
	}
}

func TestCheckHelpPrintsUsage(t *testing.T) {
	stub := useCheckStub(t, &checkStub{})
	out, errText, code := runAllod(t, "check", "--help")
	if code != 0 || errText != "" || !strings.Contains(out, "--override-input") {
		t.Errorf("--help: exit %d, stderr %q, stdout %q", code, errText, out)
	}
	if len(stub.captured)+len(stub.streamed) != 0 {
		t.Error("--help ran nix")
	}
}

// TestCheckStepArgv pins the expression and argv of every kind of step, each
// carrying every override and --no-write-lock-file whether or not an override
// was given. The expressions are written out rather than built from production.
func TestCheckStepArgv(t *testing.T) {
	enumeration := checkEnumeration{
		Outputs:  []string{"lib", "nixosModules", "nixosConfigurations", "checks"},
		Machines: []string{`green"machine`},
		Modules:  []string{"green-module"},
		Checks: []checkEntry{
			{System: "x86_64-linux", Name: "green-local"},
			{System: "aarch64-linux", Name: "green-foreign"},
		},
	}
	wantExpr := []string{
		`o: builtins.seq (builtins.getAttr "lib" o) "ok"`,
		`o: builtins.seq (builtins.getAttr "green-module" o.nixosModules) "ok"`,
		`o: (d: if (d.type or null) != "derivation" then throw "config.system.build.toplevel is not a derivation" else d.drvPath) ((builtins.getAttr "green\"machine" o.nixosConfigurations).config.system.build.toplevel)`,
		`o: (d: if (d.type or null) != "derivation" then throw "the check is not a derivation" else d.drvPath) (builtins.getAttr "green-local" (builtins.getAttr "x86_64-linux" o.checks))`,
		`o: (d: if (d.type or null) != "derivation" then throw "the check is not a derivation" else d.drvPath) (builtins.getAttr "green-foreign" (builtins.getAttr "aarch64-linux" o.checks))`,
	}

	for _, options := range []checkOptions{
		{flake: "."},
		{flake: ".", overrides: []checkOverride{{"secrets", "path:/s"}, {"profiles", "path:/p"}}},
	} {
		tail := []string{"--no-write-lock-file"}
		for _, override := range options.overrides {
			tail = append(tail, "--override-input", override.input, override.ref)
		}
		steps := checkSteps(options, enumeration, []string{"lib", "vmFacts"}, "x86_64-linux")
		if len(steps) != len(wantExpr) {
			t.Fatalf("built %d steps, want %d", len(steps), len(wantExpr))
		}
		for index, step := range steps {
			want := append([]string{"eval", "--raw", ".#.", "--apply", wantExpr[index]}, tail...)
			if !equalArgs(step.argv, want) {
				t.Errorf("step %q argv =\n%v\nwant\n%v", step.label, step.argv, want)
			}
		}
		for name, argv := range map[string][]string{
			"enumeration": checkEnumerateArgs(options),
			"metadata":    checkMetadataArgs(options),
		} {
			if !equalArgs(argv[len(argv)-len(tail):], tail) {
				t.Errorf("%s argv = %v, want it to end with %v", name, argv, tail)
			}
		}
	}

	if got, want := checkSystemArgs(), []string{"eval", "--raw", "--impure", "--expr", "builtins.currentSystem"}; !equalArgs(got, want) {
		t.Errorf("checkSystemArgs = %v, want %v", got, want)
	}
	if got, want := checkBuildDrvArgs("/nix/store/aaa.drv"), []string{"build", "--no-link", "/nix/store/aaa.drv^*"}; !equalArgs(got, want) {
		t.Errorf("checkBuildDrvArgs = %v, want %v", got, want)
	}
	if got, want := checkConfigArgs("a = 1\n"), []string{"eval", "--json", "--expr", `builtins.fromTOML "a = 1\n"`}; !equalArgs(got, want) {
		t.Errorf("checkConfigArgs = %v, want %v", got, want)
	}
}

func TestNixStringLiteral(t *testing.T) {
	cases := map[string]string{
		"":                  `""`,
		"plain":             `"plain"`,
		`quo"te`:            `"quo\"te"`,
		`back\slash`:        `"back\\slash"`,
		"tab\there":         `"tab\there"`,
		"nl\nhere":          `"nl\nhere"`,
		"cr\rhere":          `"cr\rhere"`,
		"anti${plain}":      `"anti\${plain}"`,
		"dollar$alone":      `"dollar$alone"`,
		`esc\${x}`:          `"esc\\\${x}"`,
		"bad\tchecks.\"x\"": `"bad\tchecks.\"x\""`,
	}
	for input, want := range cases {
		if got := nixStringLiteral(input); got != want {
			t.Errorf("nixStringLiteral(%q) = %s, want %s", input, got, want)
		}
	}
}

func TestCheckCountsBuiltApartFromEvaluated(t *testing.T) {
	stub := useCheckStub(t, &checkStub{outputs: `{"outputs": ["checks", "nixosConfigurations"],
	  "machines": ["green-machine"], "modules": [],
	  "checks": [{"system": "x86_64-linux", "name": "green-local"},
	             {"system": "aarch64-linux", "name": "green-foreign"}]}`})
	out, errText, code := runAllod(t, "check")
	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	for _, want := range []string{
		"All 3 steps passed (checks: 1 built, 1 evaluated only).\n",
		"The 1 evaluated-only check(s) are for aarch64-linux and need a host of that system to be built.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not contain %q\ngot: %s", want, out)
		}
	}
	if len(stub.streamed) != 3 {
		t.Errorf("streamed %v, want the machine, the foreign evaluation and one build", stub.streamed)
	}
}

// TestCheckFailedEvaluationIsNotBuilt is the one thing about a failing step only
// a stub can show: the build of a check whose evaluation failed never runs.
func TestCheckFailedEvaluationIsNotBuilt(t *testing.T) {
	stub := useCheckStub(t, &checkStub{
		outputs: `{"outputs": ["checks"], "machines": [], "modules": [],
		           "checks": [{"system": "x86_64-linux", "name": "red-local"},
		                      {"system": "x86_64-linux", "name": "green-local"}]}`,
		failWhen: "red",
	})
	_, errText, code := runAllod(t, "check")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "\nFailed steps (1 of 2):\n  - check x86_64-linux.red-local (built)\n"; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %s", want, errText)
	}
	if len(stub.streamed) != 1 {
		t.Errorf("streamed %v, want only green-local's build", stub.streamed)
	}
}

func TestCheckRefusals(t *testing.T) {
	cases := []struct {
		name    string
		outputs string
		want    []string
	}{
		{"nothing to run",
			`{"outputs": ["checks"], "machines": [], "modules": [], "checks": []}`,
			[]string{"found no machine and no check to run in ."}},
		{"modules and passive outputs are not a gate",
			`{"outputs": ["lib", "nixosModules"], "machines": [], "modules": ["green-module"], "checks": []}`,
			[]string{"found no machine and no check to run in ."}},
		{"witnessed nothing, each system named once",
			`{"outputs": ["checks"], "machines": [], "modules": [],
			  "checks": [{"system": "aarch64-linux", "name": "one"},
			             {"system": "aarch64-linux", "name": "two"},
			             {"system": "riscv64-linux", "name": "three"}]}`,
			[]string{"Witnessed nothing: evaluated 3 of 3 checks and built none of them.",
				"run the gate on aarch64-linux,riscv64-linux,"}},
		{"an unlisted output, and not the allowed one beside it",
			`{"outputs": ["checks", "chekcs", "packages", "vmFacts"], "machines": [], "modules": [],
			  "checks": [{"system": "x86_64-linux", "name": "green-local"}]}`,
			[]string{"exposes flake output(s) allod check does not run:\n  - chekcs\n  - packages\n"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			useCheckStub(t, &checkStub{outputs: testCase.outputs})
			out, errText, code := runAllod(t, "check")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			for _, want := range testCase.want {
				if !strings.Contains(errText, want) {
					t.Errorf("stderr does not contain %q\ngot: %s", want, errText)
				}
			}
			if strings.Contains(out, "steps passed") {
				t.Errorf("stdout reports success\ngot: %s", out)
			}
			if strings.Contains(errText, "!!! FAILED") || strings.Contains(errText, "vmFacts") {
				t.Errorf("stderr blames a step or an allowed output\ngot: %s", errText)
			}
		})
	}
}

func TestCheckConfigNamesDeduplicates(t *testing.T) {
	names := checkConfigNames(checkOptions{flake: "."}, `{"passive-outputs": ["profilesSource", "profilesSource"]}`)
	if len(names) != 1 || names[0] != "profilesSource" {
		t.Errorf("names = %v, want one profilesSource", names)
	}
}

func TestCheckConfigRefusals(t *testing.T) {
	cases := map[string]string{
		`{"passive-output": ["a"]}`:       "passive-output",
		`{"passive-outputs": 42}`:         "passive-outputs",
		`{"passive-outputs": ["checks"]}`: "which allod check runs itself",
		`{"passive-outputs": [""]}`:       "which allod check runs itself",
	}
	for decoded, want := range cases {
		t.Run(decoded, func(t *testing.T) {
			errText := captureDie(t, func() { checkConfigNames(checkOptions{flake: "."}, decoded) })
			if !strings.Contains(errText, want) {
				t.Errorf("stderr does not contain %q\ngot: %s", want, errText)
			}
			if !strings.Contains(errText, checkConfigName) {
				t.Errorf("stderr does not name %s\ngot: %s", checkConfigName, errText)
			}
		})
	}
}
