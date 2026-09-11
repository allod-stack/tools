package main

// 'allod secret declare' writes a credential's non-secret half — the three
// entries an agent currently has to hand-edit across credentials.nix,
// secrets.nix, and forgejo-token-groups.json — so the PR that declares a new
// credential is generated rather than transcribed from the secrets
// template's README by hand. It writes no ciphertext, reads no identity,
// and never commits: the diff it leaves is reviewed like any other, and the
// inventory check validates it exactly the way it validates a hand edit.
//
// This file carries no build tag, unlike secret.go's 'create' and 'rekey':
// it runs on every machine that can check out the secrets repository,
// because declaring what a credential needs is an authoring act, not a
// custody one. Its own init() registers the 'secret' namespace itself — see
// secret_common.go for why exactly one file may do that — and secret.go's
// init() extends the same table with 'create' and 'rekey' from behind its
// tag.
//
// declare is a text tool, not a nix one: it finds each insertion point by
// pattern rather than by evaluating the flake, so it needs no nix on PATH
// and can run before the branch it edits would even evaluate (a 'pending'
// entry with no ciphertext, which is exactly the shape credential-inventory
// accepts). Every insertion is one contiguous block; every other byte of
// every file is untouched.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// --- Registration ---

func init() {
	registerNamespace(namespace{
		name:    "secret",
		summary: "Write or land a credential; declare works everywhere, create and rekey need the secret tag",
		main:    secretMain,
	})
	secretCommands = append([]secretCommand{{
		name:    "declare",
		summary: "Write a credential's non-secret half across the three files it touches",
		usage:   []string{declareUsageLine},
		detail:  secretDeclareDetail,
		run:     secretDeclare,
	}}, secretCommands...)
}

const declareUsageLine = `allod secret declare <name> [<checkout>] --kind <kind> --owner <owner> --to <machine>[,<machine>...] --format <format> --deployed-path <path> --verify <probe> [--service none|forgejo] [--account <account>] [--ui-token-name <name>] [--strategy overlap|in-place]`

var declareValidKinds = []string{"user", "machine-host", "forge-git", "agent", "service"}
var declareValidFormats = []string{"raw-forgejo-token", "credential-store-url", "rclone-remote-stanza"}
var declareValidVerifyTypes = []string{"forge-token-verify", "git-ls-remote", "site-check"}
var declareValidStrategies = []string{"overlap", "in-place"}
var declareValidServices = []string{"none", "forgejo"}

const secretDeclareDetail = `'declare' writes three insertions against the secrets checkout: the
credentials.nix entry in rotation_state "pending" (the agent-pr-token
layout), the secrets.nix recipient line ('[ hostKey ] ++ vmKeys "<vm>"' per
target machine), and a forgejo-token-groups.json rotation registry group
whose 'service' is "none" or "forgejo". Every insertion is one contiguous
block; nothing else in any file changes. All three are written together or
none are: it checks all three files for a name collision before touching
any of them, and re-reads each file it wrote to confirm it still parses,
restoring on the first failure.

<name> must match ^[a-z0-9][a-z0-9-]*$ — it becomes a nix attribute name, a
secrets.nix key, and a filename. --kind is the credential's own kind and
must be one of: user, machine-host, forge-git, agent, service (the enum
credential-inventory enforces). --owner is a free-form identifier. --to
names one or more target machines, comma-separated or by repeating the
flag; 'nexus' is the host and gets target kind "nixos-host", every other
name is looked up in the inventory's VM registry to derive "dev-vm" or
"privacy-vm" — a name absent there is refused. --format is one of:
raw-forgejo-token, credential-store-url, rclone-remote-stanza. --deployed-path
and --verify (one of: forge-token-verify, git-ls-remote, site-check) apply
to every target. --service defaults to "none"; "forgejo" requires --account
and --ui-token-name, and "none" refuses them. --strategy defaults to
"overlap".

declare refuses a name already declared anywhere: a credentials.nix entry,
a secrets.nix line for secrets/<name>.age, or a registry group keyed <name>
or naming <name> among its credentials, listing every match found. It
writes text, not ciphertext, reads no identity, and never commits — review
the diff it leaves, then run 'allod secret create <name>' to land the
secret half.

It resolves the checkout the way 'create' does: the repository registry's
checkout under ~/work, or an explicit <checkout> path. On success it prints
the three paths it changed, one per line, and nothing else.
`

// --- Argument parsing ---

type declareArgs struct {
	name         string
	checkout     string
	kind         string
	owner        string
	to           []string
	format       string
	deployedPath string
	verify       string
	service      string
	account      string
	uiTokenName  string
	strategy     string
}

var declareNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func joinList(values []string) string { return strings.Join(values, ", ") }

func parseDeclareArgs(args []string) declareArgs {
	parsed := declareArgs{service: "none", strategy: "overlap"}
	var positional []string
	haveService, haveStrategy := false, false

	for len(args) > 0 {
		arg := args[0]
		switch arg {
		case "-h", "--help":
			fmt.Fprint(stdout, secretCommandHelp("declare"))
			exit(0)
		case "--kind", "--owner", "--format", "--deployed-path", "--verify", "--service", "--account", "--ui-token-name", "--strategy":
			if len(args) < 2 {
				secretCommandUsageError("declare", "%s requires a value", arg)
			}
			value := args[1]
			switch arg {
			case "--kind":
				parsed.kind = value
			case "--owner":
				parsed.owner = value
			case "--format":
				parsed.format = value
			case "--deployed-path":
				parsed.deployedPath = value
			case "--verify":
				parsed.verify = value
			case "--service":
				parsed.service, haveService = value, true
			case "--account":
				parsed.account = value
			case "--ui-token-name":
				parsed.uiTokenName = value
			case "--strategy":
				parsed.strategy, haveStrategy = value, true
			}
			args = args[2:]
		case "--to":
			if len(args) < 2 {
				secretCommandUsageError("declare", "--to requires a value")
			}
			for _, machine := range strings.Split(args[1], ",") {
				if machine == "" {
					secretCommandUsageError("declare", "--to names an empty machine in %q", args[1])
				}
				parsed.to = append(parsed.to, machine)
			}
			args = args[2:]
		default:
			if strings.HasPrefix(arg, "-") {
				secretCommandUsageError("declare", "unknown option for secret declare: %s", arg)
			}
			positional = append(positional, arg)
			args = args[1:]
		}
	}

	switch len(positional) {
	case 1:
		parsed.name = positional[0]
	case 2:
		parsed.name, parsed.checkout = positional[0], positional[1]
	case 0:
		secretCommandUsageError("declare", "secret declare requires a credential name")
	default:
		secretCommandUsageError("declare", "secret declare takes a credential name and at most one checkout path")
	}

	if !declareNamePattern.MatchString(parsed.name) {
		secretCommandUsageError("declare", "invalid credential name %q; expected to match ^[a-z0-9][a-z0-9-]*$", parsed.name)
	}
	if parsed.kind == "" {
		secretCommandUsageError("declare", "--kind is required")
	}
	if !stringInList(parsed.kind, declareValidKinds) {
		secretCommandUsageError("declare", "invalid --kind %q; expected one of: %s", parsed.kind, joinList(declareValidKinds))
	}
	if parsed.owner == "" {
		secretCommandUsageError("declare", "--owner is required")
	}
	if len(parsed.to) == 0 {
		secretCommandUsageError("declare", "--to is required")
	}
	if parsed.format == "" {
		secretCommandUsageError("declare", "--format is required")
	}
	if !stringInList(parsed.format, declareValidFormats) {
		secretCommandUsageError("declare", "invalid --format %q; expected one of: %s", parsed.format, joinList(declareValidFormats))
	}
	if parsed.deployedPath == "" {
		secretCommandUsageError("declare", "--deployed-path is required")
	}
	if parsed.verify == "" {
		secretCommandUsageError("declare", "--verify is required")
	}
	if !stringInList(parsed.verify, declareValidVerifyTypes) {
		secretCommandUsageError("declare", "invalid --verify %q; expected one of: %s", parsed.verify, joinList(declareValidVerifyTypes))
	}
	if haveService && !stringInList(parsed.service, declareValidServices) {
		secretCommandUsageError("declare", "invalid --service %q; expected one of: %s", parsed.service, joinList(declareValidServices))
	}
	if haveStrategy && !stringInList(parsed.strategy, declareValidStrategies) {
		secretCommandUsageError("declare", "invalid --strategy %q; expected one of: %s", parsed.strategy, joinList(declareValidStrategies))
	}
	switch parsed.service {
	case "forgejo":
		if parsed.account == "" || parsed.uiTokenName == "" {
			secretCommandUsageError("declare", "--service forgejo requires --account and --ui-token-name")
		}
	case "none":
		if parsed.account != "" || parsed.uiTokenName != "" {
			secretCommandUsageError("declare", "--service none refuses --account and --ui-token-name")
		}
	}
	return parsed
}

func stringInList(value string, list []string) bool {
	for _, candidate := range list {
		if candidate == value {
			return true
		}
	}
	return false
}

// --- Target machine kind ---

// declareVMSpec is the subset of one vm-specs.json entry declare reads.
// vm-specs.json (generated from the inventory flake's 'machines' attrset by
// mkVmSpecsJson) does not carry a 'type' field for guest machines — that
// field is filtered out on the way to JSON, kept only inside the flake's
// own validation. 'repos' is the one field left that distinguishes a dev VM
// from a privacy VM in the committed public template: a dev VM carries the
// repository checkouts an agent works in, a privacy VM carries none. This
// is a real limitation, not the inventory 'type' field the brief that wrote
// this command expected to read; see the worker report for allod/tools#196.
type declareVMSpec struct {
	Repos []string `json:"repos"`
}

func declareInventoryCheckout() string {
	if value := os.Getenv("INVENTORY"); value != "" {
		return strings.TrimRight(value, "/")
	}
	return filepath.Join(workDir(), "allod", "inventory")
}

func declareVMSpecsPath() string {
	return filepath.Join(declareInventoryCheckout(), "scripts", "vm-specs.json")
}

// declareLoadVMSpecs is a test seam: production reads vm-specs.json from
// disk, tests replace it with a fixture map so no inventory checkout is
// needed to exercise the derivation.
var declareLoadVMSpecs = func() map[string]declareVMSpec {
	path := declareVMSpecsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		die(1, "could not read the inventory VM registry %s: %s", path, err)
	}
	var specs map[string]declareVMSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		die(1, "%s is not valid JSON: %s", path, err)
	}
	return specs
}

// declareTargetKind derives one machine's registry 'kind' rather than
// taking it as a flag, so nothing on the command line can misdeclare a
// machine's own type. 'nexus' is the one host and is never looked up.
func declareTargetKind(machine string, specs map[string]declareVMSpec) string {
	if machine == "nexus" {
		return "nixos-host"
	}
	entry, ok := specs[machine]
	if !ok {
		die(1, "machine '%s' named in --to is not in %s", machine, declareVMSpecsPath())
	}
	if len(entry.Repos) > 0 {
		return "dev-vm"
	}
	return "privacy-vm"
}

// --- The registry group shape ---

type declareVerify struct {
	Type string `json:"type"`
}

type declareTarget struct {
	System       string        `json:"system"`
	Kind         string        `json:"kind"`
	DeployedPath string        `json:"deployed_path"`
	Verify       declareVerify `json:"verify"`
}

type declareCredentialEntry struct {
	Credential string          `json:"credential"`
	SecretPath string          `json:"secret_path"`
	Format     string          `json:"format"`
	Targets    []declareTarget `json:"targets"`
}

type declareGroup struct {
	Service          string                   `json:"service"`
	Account          string                   `json:"account,omitempty"`
	UITokenName      string                   `json:"ui_token_name,omitempty"`
	RegistryAlias    string                   `json:"registry_alias"`
	RotationStrategy string                   `json:"rotation_strategy"`
	Credentials      []declareCredentialEntry `json:"credentials"`
}

// registryGroup for reading the existing file back during collision checks.
// It carries only what collision detection needs; every other field the
// real validator requires is ignored here the way secret.go's decoders
// ignore fields they do not consult.
type registryGroup struct {
	Credentials []struct {
		Credential string `json:"credential"`
		SecretPath string `json:"secret_path"`
	} `json:"credentials"`
}

func buildDeclareGroup(parsed declareArgs, targets []declareTarget) declareGroup {
	group := declareGroup{
		Service:          parsed.service,
		Account:          parsed.account,
		UITokenName:      parsed.uiTokenName,
		RegistryAlias:    parsed.name,
		RotationStrategy: parsed.strategy,
		Credentials: []declareCredentialEntry{{
			Credential: parsed.name,
			SecretPath: "secrets/" + parsed.name + ".age",
			Format:     parsed.format,
			Targets:    targets,
		}},
	}
	return group
}

// --- Collision checks ---

// declareCredentialsHasEntry reports whether credentials.nix already
// declares a literal '<name> = { ... }' or '"<name>" = { ... }' attribute,
// the same shape flipPendingToActive (secret.go) looks for.
func declareCredentialsHasEntry(text, name string) bool {
	pattern := regexp.MustCompile(`(?m)^[ \t]*"?` + regexp.QuoteMeta(name) + `"?[ \t]*=[ \t]*\{`)
	return pattern.MatchString(text)
}

// declareSecretsHasLine reports whether secrets.nix already declares a
// publicKeys line for the given repository-relative path.
func declareSecretsHasLine(text, path string) bool {
	pattern := regexp.MustCompile(`(?m)^[ \t]*"` + regexp.QuoteMeta(path) + `"\.publicKeys[ \t]*=`)
	return pattern.MatchString(text)
}

// declareCollisions checks all three files for the name before anything is
// written, and returns every match found, file and finding together, so a
// refusal names all of them at once rather than the first one hit.
func declareCollisions(credentialsText, secretsText string, registry map[string]registryGroup, name, secretPath string) []string {
	var findings []string
	if declareCredentialsHasEntry(credentialsText, name) {
		findings = append(findings, fmt.Sprintf("credentials.nix: an entry named '%s' already exists", name))
	}
	if declareSecretsHasLine(secretsText, secretPath) {
		findings = append(findings, fmt.Sprintf("secrets.nix: a publicKeys line for '%s' already exists", secretPath))
	}
	var aliases []string
	for alias := range registry {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		group := registry[alias]
		if alias == name {
			findings = append(findings, fmt.Sprintf("forgejo-token-groups.json: a group keyed '%s' already exists", alias))
		}
		for _, credential := range group.Credentials {
			if credential.Credential == name {
				findings = append(findings, fmt.Sprintf("forgejo-token-groups.json: group '%s' already names credential '%s'", alias, name))
			}
			if credential.SecretPath == secretPath {
				findings = append(findings, fmt.Sprintf("forgejo-token-groups.json: group '%s' already uses secret path '%s'", alias, secretPath))
			}
		}
	}
	return findings
}

// --- Text insertion ---

// declareCredentialsTemplate is exactly the agent-pr-token entry's layout in
// the secrets template's credentials.nix: 2-space indent for the attribute,
// 4-space indent for its fields, '=' aligned under 'rotation_state', the
// longest field name.
const declareCredentialsTemplate = "  %[1]s = {\n" +
	"    name           = \"%[1]s\";\n" +
	"    kind           = \"%[2]s\";\n" +
	"    owner          = \"%[3]s\";\n" +
	"    public_key     = null;\n" +
	"    consumers      = [\n" +
	"      { type = \"agenix\"; repo = \"secrets\"; secret = \"secrets/%[1]s.age\"; }\n" +
	"    ];\n" +
	"    rotation_state = \"pending\";\n" +
	"  };\n"

// declareInsertCredentialsEntry inserts the new entry immediately before the
// attrset's closing '}' — the last byte of the file in the secrets
// template's layout — preceded by a blank line, matching how the existing
// entries there are separated. It touches nothing else in the file.
func declareInsertCredentialsEntry(text string, parsed declareArgs) (string, error) {
	trimmed := strings.TrimRight(text, "\n")
	if !strings.HasSuffix(trimmed, "}") {
		return "", fmt.Errorf("does not end in a closing '}'")
	}
	body := trimmed[:len(trimmed)-1]
	entry := fmt.Sprintf(declareCredentialsTemplate, parsed.name, parsed.kind, parsed.owner)
	return body + "\n" + entry + "}\n", nil
}

// declareSecretsRecipients renders the '[ hostKey ] ++ vmKeys "<vm>" ...'
// expression for one or more target machines, in --to order. 'nexus' is the
// host itself: hostKey already covers it, so it contributes no vmKeys call
// — the same shape the template's host-only lines use
// ('"secrets/vm-host-keys/nexus-ssh.age".publicKeys = [ hostKey ];').
func declareSecretsRecipients(to []string) string {
	var b strings.Builder
	b.WriteString("[ hostKey ]")
	for _, machine := range to {
		if machine == "nexus" {
			continue
		}
		fmt.Fprintf(&b, " ++ vmKeys %q", machine)
	}
	return b.String()
}

// declareSecretsLinePattern matches one top-level 'secrets/<path>.age'
// publicKeys line, the shape every existing entry in the template uses.
var declareSecretsLinePattern = regexp.MustCompile(`(?m)^  "secrets/[A-Za-z0-9._/-]+\.age"\.publicKeys[ \t]*=.*;[ \t]*$`)

// declareInsertSecretsLine inserts the new recipient line immediately after
// the last existing line of the same shape, touching nothing else. Unlike
// credentials.nix entries, secrets.nix lines are not blank-line separated in
// the template, so none is added here.
func declareInsertSecretsLine(text string, parsed declareArgs) (string, error) {
	matches := declareSecretsLinePattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return "", fmt.Errorf("no existing publicKeys line to insert after")
	}
	last := matches[len(matches)-1]
	line := fmt.Sprintf(`  "secrets/%s.age".publicKeys = %s;`, parsed.name, declareSecretsRecipients(parsed.to))
	return text[:last[1]] + "\n" + line + text[last[1]:], nil
}

// declareInsertRegistryGroup inserts the new group as a new top-level key,
// indented to match the file (two spaces), immediately before the file's
// closing '}', adding a comma after the previous last group when the
// registry is not empty. The group value is marshaled at the same
// indentation depth the template's own entries use, so its shape may differ
// cosmetically from a hand-formatted one (the template's existing
// single-line 'targets' objects, for instance) without affecting validity.
func declareInsertRegistryGroup(text, name string, group declareGroup) (string, error) {
	trimmed := strings.TrimRight(text, "\n")
	if !strings.HasSuffix(trimmed, "}") {
		return "", fmt.Errorf("does not end in a closing '}'")
	}
	body := trimmed[:len(trimmed)-1]
	index := len(body) - 1
	for index >= 0 && isJSONSpace(body[index]) {
		index--
	}
	empty := index < 0 || body[index] == '{'
	prefix := body[:index+1]

	groupJSON, err := json.MarshalIndent(group, "  ", "  ")
	if err != nil {
		return "", fmt.Errorf("could not encode the registry group: %w", err)
	}

	var out strings.Builder
	out.WriteString(prefix)
	if !empty {
		out.WriteString(",")
	}
	out.WriteString("\n  ")
	keyBytes, err := json.Marshal(name)
	if err != nil {
		return "", fmt.Errorf("could not encode the registry key: %w", err)
	}
	out.Write(keyBytes)
	out.WriteString(": ")
	out.Write(groupJSON)
	out.WriteString("\n}\n")
	return out.String(), nil
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// --- Structural validation ---

// declareNixBalanced is the "cheap structural check" the design calls for
// instead of shelling out to nix: brace depth never goes negative and ends
// at zero, with braces inside double-quoted strings and '#' comments
// ignored so a value or a comment containing '{' or '}' cannot miscount it.
// It cannot catch every way a nix file could be broken, only the one this
// command could plausibly cause: a misplaced insertion.
func declareNixBalanced(text string) bool {
	depth := 0
	for index := 0; index < len(text); index++ {
		switch text[index] {
		case '#':
			for index < len(text) && text[index] != '\n' {
				index++
			}
		case '"':
			index++
			for index < len(text) && text[index] != '"' {
				if text[index] == '\\' {
					index++
				}
				index++
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// --- The command ---

func readDeclareFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		die(1, "could not read %s: %s", path, err)
	}
	return string(data)
}

func secretDeclare(args []string) {
	parsed := parseDeclareArgs(args)
	checkout := resolveSecretsCheckout(parsed.checkout)

	credentialsPath := filepath.Join(checkout, "credentials.nix")
	secretsPath := filepath.Join(checkout, "secrets.nix")
	registryPath := filepath.Join(checkout, "forgejo-token-groups.json")

	credentialsText := readDeclareFile(credentialsPath)
	secretsText := readDeclareFile(secretsPath)
	registryText := readDeclareFile(registryPath)

	var registry map[string]registryGroup
	if err := json.Unmarshal([]byte(registryText), &registry); err != nil {
		die(1, "%s is not valid JSON: %s", registryPath, err)
	}

	secretPath := "secrets/" + parsed.name + ".age"
	if findings := declareCollisions(credentialsText, secretsText, registry, parsed.name, secretPath); len(findings) > 0 {
		for _, finding := range findings {
			fmt.Fprintf(stderr, "allod: %s\n", finding)
		}
		die(1, "credential '%s' already exists; declare never edits an existing declaration", parsed.name)
	}

	// The VM registry is loaded only if some target is not 'nexus': a
	// declaration that targets the host alone needs no inventory checkout
	// at all.
	var specs map[string]declareVMSpec
	needsVMRegistry := false
	for _, machine := range parsed.to {
		if machine != "nexus" {
			needsVMRegistry = true
			break
		}
	}
	if needsVMRegistry {
		specs = declareLoadVMSpecs()
	}
	targets := make([]declareTarget, 0, len(parsed.to))
	for _, machine := range parsed.to {
		targets = append(targets, declareTarget{
			System:       machine,
			Kind:         declareTargetKind(machine, specs),
			DeployedPath: parsed.deployedPath,
			Verify:       declareVerify{Type: parsed.verify},
		})
	}

	newCredentialsText, err := declareInsertCredentialsEntry(credentialsText, parsed)
	if err != nil {
		die(1, "credentials.nix: %s", err)
	}
	if !declareNixBalanced(newCredentialsText) {
		die(1, "credentials.nix: the edit would leave unbalanced braces; refusing to write")
	}

	newSecretsText, err := declareInsertSecretsLine(secretsText, parsed)
	if err != nil {
		die(1, "secrets.nix: %s", err)
	}
	if !declareNixBalanced(newSecretsText) {
		die(1, "secrets.nix: the edit would leave unbalanced braces; refusing to write")
	}

	group := buildDeclareGroup(parsed, targets)
	newRegistryText, err := declareInsertRegistryGroup(registryText, parsed.name, group)
	if err != nil {
		die(1, "forgejo-token-groups.json: %s", err)
	}
	if !json.Valid([]byte(newRegistryText)) {
		die(1, "forgejo-token-groups.json: the edit would leave invalid JSON; refusing to write")
	}

	// Nothing is written until here. From here on a failed write restores
	// every file already written, in order, before reporting.
	writes := []struct {
		path    string
		content []byte
	}{
		{credentialsPath, []byte(newCredentialsText)},
		{secretsPath, []byte(newSecretsText)},
		{registryPath, []byte(newRegistryText)},
	}
	originals := map[string][]byte{
		credentialsPath: []byte(credentialsText),
		secretsPath:     []byte(secretsText),
		registryPath:    []byte(registryText),
	}
	var done []string
	for _, write := range writes {
		if err := os.WriteFile(write.path, write.content, 0644); err != nil {
			problems := 0
			for _, path := range done {
				if restoreErr := os.WriteFile(path, originals[path], 0644); restoreErr != nil {
					fmt.Fprintf(stderr, "allod: could not restore %s: %s\n", path, restoreErr)
					problems++
				}
			}
			if problems > 0 {
				die(1, "could not write %s: %s; the tree could NOT be fully restored, inspect it before retrying", write.path, err)
			}
			die(1, "could not write %s: %s; restored %s", write.path, err, joinList(done))
		}
		done = append(done, write.path)
	}

	for _, path := range []string{credentialsPath, secretsPath, registryPath} {
		rel, err := filepath.Rel(checkout, path)
		if err != nil {
			rel = path
		}
		fmt.Fprintln(stdout, rel)
	}
}
