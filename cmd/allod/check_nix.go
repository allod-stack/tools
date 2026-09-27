package main

// Every step reaches the flake the same way, through the root of its outputs
// with each name embedded as an escaped Nix string literal: a name that cannot
// be spelled in an installable is then no special case, and there is one route
// to get wrong instead of two. `builtins.getFlake` is not used at all, because
// it ignores --override-input while '<ref>#.' honours it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const (
	checkConfigName        = "allod-check.toml"
	checkPassiveOutputsKey = "passive-outputs"
)

var (
	checkHandledOutputs = []string{"nixosConfigurations", "checks", "nixosModules"}
	checkBuiltinPassive = []string{"lib", "vmFacts"}
)

var checkStream = func(name string, args []string) int {
	return runCommand("", nil, stdout, stderr, name, args...)
}

var checkCapture = func(name string, args []string) (string, int) {
	var out bytes.Buffer
	command := exec.Command(name, args...)
	command.Stdout = &out
	command.Stderr = stderr
	status := commandExitCode(command.Run())
	return strings.TrimRight(out.String(), "\n"), status
}

// checkEnumerationExpr forces attribute names only, never a machine or a check,
// so the whole enumeration is one cheap evaluation. Rejecting a checks attribute
// with no '-' in its name restates Nix's own checkSystemName, so a typo such as
// checks.typo cannot be enumerated as a system and quietly pass.
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

func (options checkOptions) flakeFlags() []string {
	flags := []string{"--no-write-lock-file"}
	for _, override := range options.overrides {
		flags = append(flags, "--override-input", override.input, override.ref)
	}
	return flags
}

func (options checkOptions) rootArgs(expr string) []string {
	return append([]string{"eval", "--raw", options.flake + "#.", "--apply", expr}, options.flakeFlags()...)
}

func checkSystemArgs() []string {
	return []string{"eval", "--raw", "--impure", "--expr", "builtins.currentSystem"}
}

func checkMetadataArgs(options checkOptions) []string {
	return append([]string{"flake", "metadata", "--json", options.flake}, options.flakeFlags()...)
}

func checkConfigArgs(text string) []string {
	return []string{"eval", "--json", "--expr", "builtins.fromTOML " + nixStringLiteral(text)}
}

func checkEnumerateArgs(options checkOptions) []string {
	return append([]string{"eval", "--json", options.flake + "#.", "--apply", checkEnumerationExpr},
		options.flakeFlags()...)
}

func checkForceExpr(value string) string {
	return fmt.Sprintf(`o: builtins.seq (%s) "ok"`, value)
}

// checkDrvPathExpr requires the value to be a derivation before instantiating
// it, which is what `nix flake check` requires of a check and of a machine's
// toplevel. Nothing downstream re-checks it: `nix build` takes an attribute set
// or a list of derivations too.
func checkDrvPathExpr(subject, value string) string {
	return fmt.Sprintf(`o: (d: if (d.type or null) != "derivation" then throw "%s is not a derivation" else d.drvPath) (%s)`,
		subject, value)
}

func checkBuildDrvArgs(drv string) []string {
	return []string{"build", "--no-link", drv + "^*"}
}

func checkEnumerate(options checkOptions) checkEnumeration {
	out, status := checkCapture("nix", checkEnumerateArgs(options))
	if status != 0 {
		die(1, "could not enumerate what to run in %s", options.flake)
	}
	var enumeration checkEnumeration
	if err := json.Unmarshal([]byte(out), &enumeration); err != nil {
		die(1, "unexpected enumeration JSON from %s: %s", options.flake, err)
	}
	return enumeration
}

func checkAllowedPassive(options checkOptions) []string {
	allowed := append([]string{}, checkBuiltinPassive...)
	for _, name := range checkConfigPassiveOutputs(options) {
		if !slices.Contains(allowed, name) {
			allowed = append(allowed, name)
		}
	}
	return allowed
}

// checkConfigPassiveOutputs reads the file from the source path Nix reports, not
// from the working tree, so an untracked allod-check.toml has exactly as much
// effect on the run as an untracked flake.nix: none. Nix decodes it, since the
// command already requires Nix and Nix already has a TOML reader.
func checkConfigPassiveOutputs(options checkOptions) []string {
	out, status := checkCapture("nix", checkMetadataArgs(options))
	if status != 0 {
		die(1, "could not read flake metadata for %s", options.flake)
	}
	var metadata struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &metadata); err != nil || metadata.Path == "" {
		die(1, "could not read the source path of %s from nix flake metadata", options.flake)
	}
	text, err := os.ReadFile(filepath.Join(metadata.Path, checkConfigName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		die(1, "could not read the %s of %s: %s", checkConfigName, options.flake, err)
	}
	decoded, status := checkCapture("nix", checkConfigArgs(string(text)))
	if status != 0 {
		die(1, "invalid %s in %s: nix could not read it as TOML", checkConfigName, options.flake)
	}
	return checkConfigNames(options, decoded)
}

func checkConfigNames(options checkOptions, decoded string) []string {
	var config struct {
		PassiveOutputs []string `json:"passive-outputs"`
	}
	reader := json.NewDecoder(strings.NewReader(decoded))
	reader.DisallowUnknownFields()
	if err := reader.Decode(&config); err != nil {
		// The decoder reads what Nix made of the TOML, so its wording is about
		// JSON; only the unknown-key case is worth passing through.
		reason := checkPassiveOutputsKey + " must be a list of strings"
		if unknown := strings.TrimPrefix(err.Error(), "json: "); strings.HasPrefix(unknown, "unknown field") {
			reason = unknown
		}
		die(1, "invalid %s in %s: %s; the only key is %s = [\"anOutput\"]",
			checkConfigName, options.flake, reason, checkPassiveOutputsKey)
	}
	var allowed []string
	for _, name := range config.PassiveOutputs {
		if name == "" || slices.Contains(checkHandledOutputs, name) {
			die(1, "invalid %s in %s: %s lists '%s', which allod check runs itself or cannot name",
				checkConfigName, options.flake, checkPassiveOutputsKey, name)
		}
		if !slices.Contains(allowed, name) {
			allowed = append(allowed, name)
		}
	}
	return allowed
}

func checkRefusedOutputs(outputs, allowed []string) []string {
	var refused []string
	for _, name := range outputs {
		if !slices.Contains(checkHandledOutputs, name) && !slices.Contains(allowed, name) {
			refused = append(refused, name)
		}
	}
	return refused
}

// checkPassiveOutputs is the allowed names the flake exposes, in the order they
// are allowed in rather than the flake's own, so the transcript does not move
// when a repository gains an output.
func checkPassiveOutputs(outputs, allowed []string) []string {
	var present []string
	for _, name := range allowed {
		if slices.Contains(outputs, name) {
			present = append(present, name)
		}
	}
	return present
}

// nixEscapes is what keeps a name or a file's text from ending the Nix string
// literal it is spelled into, or being evaluated as Nix. A Replacer never
// rescans what it wrote, so the order of these pairs is not load-bearing.
var nixEscapes = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "${", `\${`)

func nixStringLiteral(value string) string { return `"` + nixEscapes.Replace(value) + `"` }
