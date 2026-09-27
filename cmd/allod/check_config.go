package main

// Which top-level flake outputs 'allod check' will tolerate without running
// them, and where that list comes from: two names built in, plus whatever
// allod-check.toml at the flake root adds. Everything else is refused before
// any step runs.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	checkConfigName        = "allod-check.toml"
	checkPassiveOutputsKey = "passive-outputs"
)

// checkHandledOutputs are the outputs the gate runs a step for. checkBuiltinPassive
// are the outputs it forces without running anything of its own, the way
// `nix flake check` does, and which every repository may expose.
var (
	checkHandledOutputs   = []string{"nixosConfigurations", "checks", "nixosModules"}
	checkBuiltinPassive   = []string{"lib", "vmFacts"}
	errCheckConfigNoTable = errors.New("a [table] header is not accepted; the only key is " + checkPassiveOutputsKey)
)

// checkAllowedPassive is the built-in passive names plus the flake's own.
func checkAllowedPassive(options checkOptions) []string {
	allowed := append([]string{}, checkBuiltinPassive...)
	for _, name := range checkConfigPassiveOutputs(options) {
		if !checkContains(allowed, name) {
			allowed = append(allowed, name)
		}
	}
	return allowed
}

func checkConfigPassiveOutputs(options checkOptions) []string {
	path := checkConfigPath(options)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		die(1, "could not read %s: %s", path, err)
	}
	names, err := parseCheckConfig(string(data))
	if err != nil {
		die(1, "invalid %s: %s", path, err)
	}
	for _, name := range names {
		if checkContains(checkHandledOutputs, name) {
			die(1, "invalid %s: %s lists %s, which allod check runs itself", path, checkPassiveOutputsKey, name)
		}
	}
	return names
}

// checkConfigPath locates allod-check.toml. A directory reference is read where
// it is; any other reference is asked of Nix, so the file comes from the same
// revision the gate is about to check.
func checkConfigPath(options checkOptions) string {
	if info, err := os.Stat(options.flake); err == nil && info.IsDir() {
		return filepath.Join(options.flake, checkConfigName)
	}
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
	return filepath.Join(metadata.Path, checkConfigName)
}

// checkRefusedOutputs is every top-level output the gate neither runs nor is
// allowed to leave alone.
func checkRefusedOutputs(outputs, allowed []string) []string {
	var refused []string
	for _, name := range outputs {
		if checkContains(checkHandledOutputs, name) || checkContains(allowed, name) {
			continue
		}
		refused = append(refused, name)
	}
	return refused
}

// checkPassiveOutputs is the allowed names the flake actually exposes, in the
// order they are allowed in rather than the flake's own, so the transcript does
// not move when a repository gains an output.
func checkPassiveOutputs(outputs, allowed []string) []string {
	var present []string
	for _, name := range allowed {
		if checkContains(outputs, name) {
			present = append(present, name)
		}
	}
	return present
}

func checkContains(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

// parseCheckConfig reads allod-check.toml, whose only key is passive-outputs, a
// list of quoted strings. Anything else in the file is an error rather than
// something skipped: the file exists to widen what the gate tolerates, so a key
// it does not understand is either a typo or a newer tool's setting, and both
// are worth stopping for.
func parseCheckConfig(text string) ([]string, error) {
	tokens, err := tokenizeCheckConfig(text)
	if err != nil {
		return nil, err
	}
	var names []string
	index, seen := 0, false
	for index < len(tokens) {
		if tokens[index].kind == '[' {
			return nil, errCheckConfigNoTable
		}
		if tokens[index].kind != 'k' {
			return nil, fmt.Errorf("expected a key, found %s", tokens[index].describe())
		}
		key := tokens[index].text
		index++
		if key != checkPassiveOutputsKey {
			return nil, fmt.Errorf("unknown key '%s'; the only key is %s", key, checkPassiveOutputsKey)
		}
		if seen {
			return nil, fmt.Errorf("%s is set more than once", key)
		}
		seen = true
		if index >= len(tokens) || tokens[index].kind != '=' {
			return nil, fmt.Errorf("%s must be followed by '='", key)
		}
		index++
		if index >= len(tokens) || tokens[index].kind != '[' {
			return nil, fmt.Errorf("%s must be a list of quoted strings, as in: %s = [\"profilesSource\"]",
				key, checkPassiveOutputsKey)
		}
		index++
		for {
			if index >= len(tokens) {
				return nil, fmt.Errorf("%s: the list is never closed with ']'", key)
			}
			if tokens[index].kind == ']' {
				index++
				break
			}
			if tokens[index].kind != 's' {
				return nil, fmt.Errorf("%s: expected a quoted output name, found %s", key, tokens[index].describe())
			}
			if tokens[index].text == "" {
				return nil, fmt.Errorf("%s: an output name cannot be empty", key)
			}
			if !checkContains(names, tokens[index].text) {
				names = append(names, tokens[index].text)
			}
			index++
			if index < len(tokens) && tokens[index].kind == ',' {
				index++
			}
		}
	}
	return names, nil
}

// checkConfigToken is one token of allod-check.toml: 'k' a bare key, 's' a
// string value, or the punctuation character itself.
type checkConfigToken struct {
	kind byte
	text string
}

func (token checkConfigToken) describe() string {
	if token.kind == 's' {
		return "the string \"" + token.text + "\""
	}
	return "'" + token.text + "'"
}

func tokenizeCheckConfig(text string) ([]checkConfigToken, error) {
	var tokens []checkConfigToken
	for index := 0; index < len(text); {
		character := text[index]
		switch {
		case character == ' ' || character == '\t' || character == '\r' || character == '\n':
			index++
		case character == '#':
			for index < len(text) && text[index] != '\n' {
				index++
			}
		case character == '=' || character == '[' || character == ']' || character == ',':
			tokens = append(tokens, checkConfigToken{kind: character, text: string(character)})
			index++
		case character == '"' || character == '\'':
			value, next, err := scanCheckConfigString(text, index)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, checkConfigToken{kind: 's', text: value})
			index = next
		case isCheckConfigKeyByte(character):
			start := index
			for index < len(text) && isCheckConfigKeyByte(text[index]) {
				index++
			}
			tokens = append(tokens, checkConfigToken{kind: 'k', text: text[start:index]})
		default:
			return nil, fmt.Errorf("unexpected character '%c'", character)
		}
	}
	return tokens, nil
}

func isCheckConfigKeyByte(character byte) bool {
	return character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' ||
		character == '-' || character == '_' || character == '.'
}

// scanCheckConfigString reads one TOML string: a basic string in double quotes
// with the common backslash escapes, or a literal string in single quotes with
// none. Multi-line strings are not accepted; an output name has no use for one.
func scanCheckConfigString(text string, start int) (string, int, error) {
	quote := text[start]
	var out strings.Builder
	for index := start + 1; index < len(text); index++ {
		character := text[index]
		if character == '\n' {
			break
		}
		if quote == '"' && character == '\\' {
			index++
			if index >= len(text) {
				break
			}
			switch text[index] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'r':
				out.WriteByte('\r')
			case '"':
				out.WriteByte('"')
			case '\\':
				out.WriteByte('\\')
			default:
				return "", 0, fmt.Errorf("unknown escape '\\%c' in a string", text[index])
			}
			continue
		}
		if character == quote {
			return out.String(), index + 1, nil
		}
		out.WriteByte(character)
	}
	return "", 0, errors.New("a string is never closed")
}
