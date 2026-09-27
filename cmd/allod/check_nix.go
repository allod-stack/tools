package main

// Every way 'allod check' reaches a flake. One rule decides the shape of a
// step: a name that can be spelled in an installable is reached as
// '<ref>#<attr path>', which keeps Nix's evaluation cache useful, and any other
// name is reached through the flake's whole output set, '<ref>#.', with the
// name embedded as a Nix string literal. `builtins.getFlake` is not used at
// all: it ignores --override-input, and '<ref>#.' honours it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// checkStream runs one step's `nix` invocation with the transcript going
// straight to the terminal. Its stdin is closed, so a step cannot eat the
// caller's input.
var checkStream = func(name string, args []string) int {
	return runCommand("", nil, stdout, stderr, name, args...)
}

// checkCapture runs a `nix` invocation whose stdout the gate reads, leaving its
// stderr on the terminal so a failure explains itself.
var checkCapture = func(name string, args []string) (string, int) {
	var out bytes.Buffer
	command := exec.Command(name, args...)
	command.Stdout = &out
	command.Stderr = stderr
	status := commandExitCode(command.Run())
	return strings.TrimRight(out.String(), "\n"), status
}

// checkEnumerationExpr forces attribute names only, never a machine or a check,
// so the whole enumeration is one cheap evaluation. Rejecting a checks
// attribute with no '-' in its name restates Nix's own checkSystemName, so a
// typo such as checks.typo cannot be enumerated as a system and quietly pass.
const checkEnumerationExpr = `outputs:
let
  checks = outputs.checks or {};
  entriesFor = system:
    if builtins.length (builtins.split "-" system) < 2
    then throw "checks.${system}: \"${system}\" is not a valid system type; a system name must contain a \"-\""
    else map (name: { inherit system name; }) (builtins.attrNames (builtins.getAttr system checks));
in
{
  outputs = builtins.attrNames outputs;
  machines = builtins.attrNames (outputs.nixosConfigurations or {});
  modules = builtins.attrNames (outputs.nixosModules or {});
  checks = builtins.concatMap entriesFor (builtins.attrNames checks);
}`

const checkForceExpr = `value: builtins.seq value "ok"`

// checkMachineExpr does what `nix flake check` does with a machine — force
// config.system.build.toplevel and check that it is a derivation — and then
// forces its drvPath, instantiating it.
const checkMachineExpr = `top: if (top.type or null) != "derivation"
     then throw "config.system.build.toplevel is not a derivation"
     else top.drvPath`

type checkEntry struct {
	System string `json:"system"`
	Name   string `json:"name"`
}

type checkEnumeration struct {
	Outputs  []string     `json:"outputs"`
	Machines []string     `json:"machines"`
	Modules  []string     `json:"modules"`
	Checks   []checkEntry `json:"checks"`
}

// flakeFlags is what every `nix` invocation that reads the flake gets.
// --no-write-lock-file keeps an override from rewriting the checkout's
// flake.lock.
func (options checkOptions) flakeFlags() []string {
	var flags []string
	for _, override := range options.overrides {
		flags = append(flags, "--override-input", override.input, override.ref)
	}
	if len(options.overrides) > 0 {
		flags = append(flags, "--no-write-lock-file")
	}
	return flags
}

func (options checkOptions) evalArgs(args ...string) []string {
	return append(append([]string{"eval"}, args...), options.flakeFlags()...)
}

func checkSystemArgs() []string {
	return []string{"eval", "--raw", "--impure", "--expr", "builtins.currentSystem"}
}

func checkMetadataArgs(options checkOptions) []string {
	return append([]string{"flake", "metadata", "--json", options.flake}, options.flakeFlags()...)
}

func checkEnumerateArgs(options checkOptions) []string {
	return append([]string{"eval", "--json", options.flake + "#.", "--apply", checkEnumerationExpr},
		options.flakeFlags()...)
}

func checkEnumerate(options checkOptions) (checkEnumeration, bool) {
	out, status := checkCapture("nix", checkEnumerateArgs(options))
	if status != 0 {
		return checkEnumeration{}, false
	}
	var enumeration checkEnumeration
	if err := json.Unmarshal([]byte(out), &enumeration); err != nil {
		die(1, "unexpected enumeration JSON from %s: %s", options.flake, err)
	}
	return enumeration, true
}

func checkPassiveArgs(options checkOptions, name string) []string {
	if checkInstallableSafe(name) {
		return options.evalArgs("--raw", options.flake+"#"+checkAttrPath(name), "--apply", checkForceExpr)
	}
	return options.evalArgs("--raw", options.flake+"#.", "--apply",
		fmt.Sprintf(`o: builtins.seq (builtins.getAttr %s o) "ok"`, nixStringLiteral(name)))
}

func checkModuleArgs(options checkOptions, name string) []string {
	if checkInstallableSafe(name) {
		return options.evalArgs("--raw", options.flake+"#nixosModules."+checkAttrPath(name), "--apply", checkForceExpr)
	}
	return options.evalArgs("--raw", options.flake+"#.", "--apply",
		fmt.Sprintf(`o: builtins.seq (builtins.getAttr %s o.nixosModules) "ok"`, nixStringLiteral(name)))
}

func checkMachineArgs(options checkOptions, name string) []string {
	if checkInstallableSafe(name) {
		return options.evalArgs("--raw",
			options.flake+"#nixosConfigurations."+checkAttrPath(name)+".config.system.build.toplevel",
			"--apply", checkMachineExpr)
	}
	return options.evalArgs("--raw", options.flake+"#.", "--apply",
		fmt.Sprintf("o: (%s) ((builtins.getAttr %s o.nixosConfigurations).config.system.build.toplevel)",
			checkMachineExpr, nixStringLiteral(name)))
}

// checkBuiltArgs returns the invocation for a check this host can build, and
// whether that invocation only evaluates the derivation path, leaving the build
// to a second `nix` process.
func checkBuiltArgs(options checkOptions, entry checkEntry) ([]string, bool) {
	if checkInstallableSafe(entry.System) && checkInstallableSafe(entry.Name) {
		argv := append([]string{"build", "--no-link",
			options.flake + "#checks." + checkAttrPath(entry.System, entry.Name)}, options.flakeFlags()...)
		return argv, false
	}
	return checkDrvPathArgs(options, entry), true
}

func checkEvaluatedArgs(options checkOptions, entry checkEntry) []string {
	if checkInstallableSafe(entry.System) && checkInstallableSafe(entry.Name) {
		return options.evalArgs("--raw", options.flake+"#checks."+checkAttrPath(entry.System, entry.Name)+".drvPath")
	}
	return checkDrvPathArgs(options, entry)
}

func checkDrvPathArgs(options checkOptions, entry checkEntry) []string {
	return options.evalArgs("--raw", options.flake+"#.", "--apply",
		fmt.Sprintf("o: (builtins.getAttr %s (builtins.getAttr %s o.checks)).drvPath",
			nixStringLiteral(entry.Name), nixStringLiteral(entry.System)))
}

// checkBuildDrvArgs builds every output of an already instantiated derivation,
// which reads no flake and so takes no overrides.
func checkBuildDrvArgs(drv string) []string {
	return []string{"build", "--no-link", drv + "^*"}
}

// checkAttrPath spells one attribute path for an installable, every segment
// quoted, so a name like "green.local" is not reinterpreted as a nested path.
// Nix's installable parser has no escapes inside those quotes, which is why
// checkInstallableSafe must hold for every segment.
func checkAttrPath(segments ...string) string {
	quoted := make([]string, len(segments))
	for index, segment := range segments {
		quoted[index] = `"` + segment + `"`
	}
	return strings.Join(quoted, ".")
}

// checkInstallableUnsafe is every ASCII character measured to break
// '<ref>#<attr path>' on Nix 2.34.8: the flake reference and the fragment are
// parsed as one URL, so a character the URL grammar rejects makes the whole
// installable unparseable however the name is quoted. Bytes outside printable
// ASCII are unsafe for the same reason.
const checkInstallableUnsafe = "\"%<>?[\\]^`{|}"

func checkInstallableSafe(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if character < 0x20 || character > 0x7e {
			return false
		}
		if strings.IndexByte(checkInstallableUnsafe, character) >= 0 {
			return false
		}
	}
	return true
}

// nixStringLiteral spells a name as a Nix string literal, so a name holding a
// quote, a backslash or an antiquotation cannot end the literal or be evaluated
// as Nix.
func nixStringLiteral(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for index := 0; index < len(value); index++ {
		switch character := value[index]; character {
		case '\\':
			out.WriteString(`\\`)
		case '"':
			out.WriteString(`\"`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		case '$':
			if index+1 < len(value) && value[index+1] == '{' {
				out.WriteString(`\${`)
				index++
				continue
			}
			out.WriteByte('$')
		default:
			out.WriteByte(character)
		}
	}
	out.WriteByte('"')
	return out.String()
}
