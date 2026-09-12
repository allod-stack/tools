//go:build secret

package main

// The secret namespace lands an encrypted credential in the secrets
// repository, at the one machine that holds the age identity.
//
// It verifies and encrypts; it does not author. A credential's non-secret
// half — the credentials.nix entry in rotation_state "pending", the
// secrets.nix recipient line, and the rotation registry entry — is written
// by an agent's PR and reviewed there, and the inventory check has already
// passed on it before this command is ever run. 'create' then reads the
// plaintext, encrypts it to exactly the recipients secrets.nix declares,
// writes the ciphertext, flips the state to "active", runs the repository's
// checks, and commits and pushes the branch. There are no flags for kind,
// owner, format, or recipients: the reviewed diff is the only authoring path,
// so there is one shape to get right, and nothing on a command line can aim
// the ciphertext at a machine the registry does not list.
//
// The plaintext never touches a filesystem. It arrives on stdin (or one
// hidden-echo line from the terminal), goes to the age process over a pipe,
// and the ciphertext comes back the same way and is written whole; a failure
// anywhere leaves no partial file. 'rekey' pipes 'age -d' into memory and back
// into 'age -e' the same way, always rewriting the file, so the
// recipient-only change that agenix silently skips cannot recur here.
//
// This file compiles only with -tags secret, which only the host toolchain
// sets. On every other machine 'allod secret' is an unknown namespace: the
// capability is absent, not refused, and no prose has to say an agent may
// not do this. secret_absent_test.go pins that half; secret_test.go the other.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const secretCreateDetail = `'create' requires the branch to already carry, for <name>: a credentials.nix
entry in rotation_state "pending" with exactly one agenix consumer in this
repository, a secrets.nix line for that consumer's path whose recipients
include this host's identity, a rotation registry entry naming both, and no
ciphertext yet. It refuses anything else and says what it found. The value
is read from stdin verbatim (trailing newline included, so multi-line
formats round-trip), or as one hidden line when stdin is a terminal; an
empty or whitespace-only value is refused. It then writes the ciphertext,
sets the state to "active", runs 'nix flake check', restores both files if
that fails, and otherwise commits and pushes the branch.

'create' works on the secrets checkout, found through the repository
registry under ~/work unless <checkout> names a worktree, and on whatever
branch is checked out there. It refuses the default branch and a dirty
tree: the landing is a commit on a branch, and merging stays your act.

The identity is $AGE_IDENTITY, or ~/.ssh/host; its .pub must be among the
recipients. The plaintext is never written to disk, printed, or passed as
an argument.
`

const secretRekeyDetail = `'rekey' decrypts <name>'s existing ciphertext with the host identity and
re-encrypts it to the recipients secrets.nix declares now, always rewriting
the file, then checks, commits, and pushes the same way as 'create'. Use it
after a recipient line changes. For a new value of an existing credential
run 'allod secret rotate <name>'.

'rekey' works on the secrets checkout, found through the repository
registry under ~/work unless <checkout> names a worktree, and on whatever
branch is checked out there. It refuses the default branch and a dirty
tree: the landing is a commit on a branch, and merging stays your act.

The identity is $AGE_IDENTITY, or ~/.ssh/host; its .pub must be among the
recipients. The plaintext is never written to disk, printed, or passed as
an argument.
`

// init extends the 'secret' namespace secret_declare.go's init() already
// registered, adding the three commands this build's tag opts it into. It
// appends to secretCommands rather than replacing it, and never calls
// registerNamespace: that would panic on the duplicate word, and
// secret_declare.go's init() is the only one allowed to call it.
func init() {
	secretCommands = append(secretCommands,
		secretCommand{
			name:    "create",
			summary: "Encrypt a pending credential's value and flip it to active",
			usage:   []string{"allod secret create <name> [<checkout>]"},
			detail:  secretCreateDetail,
			run:     secretCreate,
		},
		secretCommand{
			name:    "rekey",
			summary: "Re-encrypt an existing credential to the recipients secrets.nix declares",
			usage:   []string{"allod secret rekey <name> [<checkout>]"},
			detail:  secretRekeyDetail,
			run:     secretRekey,
		},
		secretCommand{
			name:    "rotate",
			summary: "Replace a credential's value, and every value its rotation group shares it with",
			usage:   []string{"allod secret rotate <name> [<checkout>] [--dry-run]"},
			detail:  secretRotateDetail,
			run:     secretRotate,
		},
	)
}

// Test seams. Every effect this namespace has outside the process and git
// goes through one of these, so the tests can drive the whole command
// against a real git fixture without nix, age, or a terminal.
var (
	secretEvalCredentials = nixEvalCredentials
	secretEvalRegistry    = nixEvalTokenGroups
	secretEvalRecipients  = nixEvalRecipients
	secretEncrypt         = ageEncrypt
	secretDecrypt         = ageDecrypt
	secretFlakeCheck      = nixFlakeCheck
	secretStdinIsTerminal = func() bool { return isTerminal(os.Stdin) }
	secretAskOnTerminal   = askSecretOnTerminal
)

// parseSecretArgs reads the shared '<name> [<checkout>]' shape 'create' and
// 'rekey' both take. Every option is unknown: the commands take none by
// design.
func parseSecretArgs(command string, args []string) (name, checkout string) {
	var positional []string
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			fmt.Fprint(stdout, secretCommandHelp(command))
			exit(0)
		case strings.HasPrefix(arg, "-"):
			secretCommandUsageError(command, "unknown option for secret %s: %s", command, arg)
		default:
			positional = append(positional, arg)
		}
	}
	switch len(positional) {
	case 1:
		return positional[0], ""
	case 2:
		return positional[0], positional[1]
	case 0:
		secretCommandUsageError(command, "secret %s requires a credential name", command)
	default:
		secretCommandUsageError(command, "secret %s takes a credential name and at most one checkout path", command)
	}
	return "", ""
}

// --- The repository ---

// The inventory and registry shapes this command reads, as nix eval --json
// renders them. Fields the command does not consult are left out; the
// decoder ignores them.
type credentialConsumer struct {
	Type   string `json:"type"`
	Repo   string `json:"repo"`
	Secret string `json:"secret"`
}

type credentialEntry struct {
	Name          string               `json:"name"`
	RotationState string               `json:"rotation_state"`
	Consumers     []credentialConsumer `json:"consumers"`
}

type registryCredential struct {
	Credential string `json:"credential"`
	SecretPath string `json:"secret_path"`

	// The remaining fields serve 'rotate' only; 'create' and 'rekey' read
	// nothing past SecretPath.
	Format  string           `json:"format"`
	Targets []registryTarget `json:"targets"`
}

// registryTarget and registryVerify serve 'rotate' only, in secret_rotate.go.
type registryTarget struct {
	System       string         `json:"system"`
	Kind         string         `json:"kind"`
	User         string         `json:"user"`
	DeployedPath string         `json:"deployed_path"`
	Verify       registryVerify `json:"verify"`
}

type registryVerify struct {
	Type              string `json:"type"`
	RepoURL           string `json:"repo_url"`
	CredentialContext string `json:"credential_context"`
}

// localAuthRefreshEntry mirrors one entry of a group's local_auth_refresh
// array. 'rotate' reads it only to decide whether to print the operator's
// next step; installing the bundle stays rotate-token's job (see
// secret_rotate.go).
type localAuthRefreshEntry struct {
	Contract         string `json:"contract"`
	System           string `json:"system"`
	LocalUsername    string `json:"local_username"`
	SourceCredential string `json:"source_credential"`
}

type tokenGroup struct {
	Credentials []registryCredential `json:"credentials"`

	// The remaining fields serve 'rotate' only.
	RegistryAlias    string                  `json:"registry_alias"`
	Service          string                  `json:"service"`
	Account          string                  `json:"account"`
	UITokenName      string                  `json:"ui_token_name"`
	RotationStrategy string                  `json:"rotation_strategy"`
	LocalAuthRefresh []localAuthRefreshEntry `json:"local_auth_refresh"`
}

// secretTarget is everything 'create' and 'rekey' verified about one
// credential before touching anything.
type secretTarget struct {
	name       string
	path       string // repository-relative, e.g. secrets/<name>.age
	state      string
	recipients []string
	branch     string
}

// requireLandingBranch is the precondition every landing shares: a branch
// that is not the default one, and a tree with nothing in it that the commit
// would not own. A leftover ciphertext from a failed attempt is 'dirty' here
// too, and is named as such rather than silently swept into the landing.
func requireLandingBranch(checkout string) string {
	branch := currentBranch(checkout)
	// defaultRemoteBranch guesses 'master' when origin/HEAD is unset, and a
	// guess is not good enough here: a checkout whose default is 'main' would
	// pass the comparison and the landing would be pushed straight to it.
	if !gitQuiet(checkout, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD") {
		die(1, "could not resolve the default branch of %s; run: git -C %s remote set-head origin -a", checkout, checkout)
	}
	if branch == defaultRemoteBranch(checkout) {
		die(1, "%s is on its default branch '%s'; check out the branch that declares the credential (an agent's agent/<description> branch) and rerun. Merging stays a separate act.", checkout, branch)
	}
	status, ok := gitOutput(checkout, "status", "--porcelain")
	if !ok {
		die(1, "git status failed in %s; refusing to guess whether the tree is clean", checkout)
	}
	if status != "" {
		fmt.Fprintf(stderr, "allod: %s has uncommitted or untracked changes:\n%s\n", checkout, status)
		die(1, "commit or remove them first; the landing must be the only change in its commit")
	}
	return branch
}

func ageIdentityPath() string {
	if value := os.Getenv("AGE_IDENTITY"); value != "" {
		return value
	}
	return filepath.Join(homeDir(), ".ssh", "host")
}

// keyMaterial reduces an SSH public key line to its type and base64, so a
// comment difference does not make the same key look like another.
func keyMaterial(key string) string {
	fields := strings.Fields(key)
	if len(fields) < 2 {
		return strings.TrimSpace(key)
	}
	return fields[0] + " " + fields[1]
}

func hostPublicKeyMaterial(identity string) string {
	data, err := os.ReadFile(identity + ".pub")
	if err != nil {
		die(1, "cannot read the host identity's public key %s.pub; set AGE_IDENTITY to the identity whose .pub secrets.nix lists", identity)
	}
	material := keyMaterial(string(data))
	if material == "" {
		die(1, "%s.pub is empty", identity)
	}
	return material
}

// validSecretPath accepts the repository-relative paths secrets.nix keys:
// under secrets/, plain characters, ending in .age. The path is interpolated
// into a nix expression and joined onto the checkout, so this is a safety
// check before it is a politeness one.
var validSecretPath = regexp.MustCompile(`^secrets/[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*\.age$`)

// lookupSecret verifies the non-secret half of a credential and returns what
// it found. Every refusal names the file that disagrees and what it holds.
func lookupSecret(checkout, name string) secretTarget {
	credentials, err := secretEvalCredentials(checkout)
	if err != nil {
		die(1, "could not evaluate lib.credentials in %s: %s", checkout, err)
	}
	entry, ok := credentials[name]
	if !ok {
		die(1, "no credential named '%s' in %s/credentials.nix", name, checkout)
	}
	var paths []string
	for _, consumer := range entry.Consumers {
		if consumer.Type == "agenix" && consumer.Repo == "secrets" {
			paths = append(paths, consumer.Secret)
		}
	}
	if len(paths) != 1 {
		die(1, "credential '%s' has %d agenix consumers in this repository in credentials.nix; this command lands exactly one ciphertext per credential", name, len(paths))
	}
	path := paths[0]
	if !validSecretPath.MatchString(path) || strings.Contains(path, "/../") {
		die(1, "credential '%s' names an unusable secret path '%s' in credentials.nix; expected secrets/<name>.age", name, path)
	}

	recipients, err := secretEvalRecipients(checkout, path)
	if err != nil {
		die(1, "secrets.nix in %s does not declare '%s' for '%s', or does not evaluate: %s", checkout, path, name, err)
	}
	if len(recipients) == 0 {
		die(1, "secrets.nix in %s lists no recipients for '%s'", checkout, path)
	}
	identity := ageIdentityPath()
	host := hostPublicKeyMaterial(identity)
	found := false
	for _, recipient := range recipients {
		if keyMaterial(recipient) == host {
			found = true
			break
		}
	}
	if !found {
		die(1, "the recipients secrets.nix lists for '%s' do not include this host's identity (%s.pub); a ciphertext this host cannot decrypt could never be rekeyed or rotated here", path, identity)
	}

	groups, err := secretEvalRegistry(checkout)
	if err != nil {
		die(1, "could not evaluate lib.forgejoTokenGroups in %s: %s", checkout, err)
	}
	registered := false
	for alias, group := range groups {
		for _, credential := range group.Credentials {
			if credential.Credential != name {
				continue
			}
			if credential.SecretPath != path {
				die(1, "the rotation registry entry for '%s' (group '%s') names '%s' but its credentials.nix consumer is '%s'; make them agree", name, alias, credential.SecretPath, path)
			}
			registered = true
		}
	}
	if !registered {
		die(1, "no rotation registry entry for '%s' in %s/forgejo-token-groups.json; a credential is registered for rotation in the same landing that creates it", name, checkout)
	}

	return secretTarget{name: name, path: path, state: entry.RotationState, recipients: recipients}
}

// --- Input ---

// readSecretValue takes the plaintext exactly as given on a pipe, or as one
// hidden line from the terminal. It never echoes, logs, or stores it.
func readSecretValue(name string) []byte {
	var value []byte
	if secretStdinIsTerminal() {
		line, err := secretAskOnTerminal(fmt.Sprintf("Value for %s (not echoed): ", name))
		if err != nil {
			die(1, "could not read the value: %s", err)
		}
		value = []byte(line)
	} else {
		data, err := io.ReadAll(stdin)
		if err != nil {
			die(1, "could not read the value from stdin: %s", err)
		}
		value = data
	}
	if len(bytes.TrimSpace(value)) == 0 {
		die(1, "the value is empty; refusing to encrypt nothing. Pipe the credential in, or run from a terminal to be asked for it")
	}
	return value
}

// --- The edit ---

var rotationStatePending = regexp.MustCompile(`(rotation_state\s*=\s*)"pending"(\s*;)`)

// flipPendingToActive rewrites the one literal 'rotation_state = "pending"'
// inside the '<name> = { ... }' entry of credentials.nix and touches nothing
// else. It works on the text rather than regenerating the file so the diff
// the reviewer sees is one line. An entry that is generated rather than
// written out — a mkActiveEntry call, say — has no literal to flip, and the
// command says so instead of guessing.
func flipPendingToActive(text, name string) (string, error) {
	header := regexp.MustCompile(`(?m)^[ \t]*"?` + regexp.QuoteMeta(name) + `"?[ \t]*=[ \t]*\{`)
	location := header.FindStringIndex(text)
	if location == nil {
		return "", fmt.Errorf("no literal '%s = { ... }' entry in credentials.nix; the command flips only entries written out there", name)
	}
	start := location[1] - 1 // the '{'
	end := matchingBrace(text, start)
	if end < 0 {
		return "", fmt.Errorf("the '%s = {' entry in credentials.nix has no closing brace", name)
	}
	// Comment lines are skipped, so a note quoting the field next to the
	// real one neither counts as a second assignment nor gets rewritten.
	lines := strings.Split(text[start:end+1], "\n")
	found := 0
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		matches := len(rotationStatePending.FindAllStringIndex(line, -1))
		if matches == 0 {
			continue
		}
		found += matches
		lines[index] = rotationStatePending.ReplaceAllString(line, `${1}"active"${2}`)
	}
	if found != 1 {
		return "", fmt.Errorf("expected exactly one literal 'rotation_state = \"pending\"' inside the '%s' entry of credentials.nix, found %d", name, found)
	}
	return text[:start] + strings.Join(lines, "\n") + text[end+1:], nil
}

// matchingBrace returns the index of the '}' that closes the '{' at start,
// or -1. Braces inside double-quoted strings and '#' comments do not count,
// so a value such as description = "}" cannot end the entry early. Nix's
// indented ” strings are not recognised; no inventory entry uses one.
func matchingBrace(text string, start int) int {
	depth := 0
	for index := start; index < len(text); index++ {
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
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

// --- Commands ---

func secretCreate(args []string) {
	name, checkoutArg := parseSecretArgs("create", args)
	checkout := resolveSecretsCheckout(checkoutArg)
	branch := requireLandingBranch(checkout)
	target := lookupSecret(checkout, name)
	target.branch = branch
	if target.state != "pending" {
		die(1, "credential '%s' is '%s', not 'pending'; create lands only a pending entry. For a recipient change run 'allod secret rekey %s'; for a new value use rotate-token", name, target.state, name)
	}
	file := filepath.Join(checkout, target.path)
	if _, err := os.Lstat(file); err == nil {
		die(1, "%s already exists while '%s' is pending; a pending entry has no ciphertext. If it is a leftover from a failed attempt, remove it and rerun", target.path, name)
	}

	credentialsPath := filepath.Join(checkout, "credentials.nix")
	original, err := os.ReadFile(credentialsPath)
	if err != nil {
		die(1, "could not read %s", credentialsPath)
	}
	flipped, err := flipPendingToActive(string(original), name)
	if err != nil {
		die(1, "%s", err)
	}

	value := readSecretValue(name)
	ciphertext := encryptOrDie(target, value)
	value = nil

	// Nothing has been written until here. From here on, a failure restores
	// both files before reporting, and says so if it could not.
	restore := func() string {
		problems := 0
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "allod: could not remove %s: %s\n", file, err)
			problems++
		}
		if err := os.WriteFile(credentialsPath, original, 0644); err != nil {
			fmt.Fprintf(stderr, "allod: could not restore %s: %s\n", credentialsPath, err)
			problems++
		}
		if problems > 0 {
			return "the tree could NOT be fully restored; inspect it before retrying"
		}
		return fmt.Sprintf("restored credentials.nix and removed %s", target.path)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		die(1, "could not create %s", filepath.Dir(file))
	}
	if err := os.WriteFile(file, ciphertext, 0644); err != nil {
		die(1, "could not write %s: %s; %s", file, err, restore())
	}
	if err := os.WriteFile(credentialsPath, []byte(flipped), 0644); err != nil {
		die(1, "could not write %s: %s; %s", credentialsPath, err, restore())
	}
	// The textual flip is verified the way every consumer will read it.
	credentials, err := secretEvalCredentials(checkout)
	if err != nil || credentials[name].RotationState != "active" {
		die(1, "credentials.nix does not evaluate '%s' as active after the flip; %s", name, restore())
	}
	if status := secretFlakeCheck(checkout); status != 0 {
		die(status, "the repository's checks failed; %s", restore())
	}

	commit := landCommit(checkout, branch, fmt.Sprintf("Land %s: write its ciphertext and set rotation_state active", name), restore, target.path, "credentials.nix")
	fmt.Fprintf(stdout, "Wrote %s, encrypted to %d recipients from secrets.nix\n", target.path, len(target.recipients))
	fmt.Fprintf(stdout, "credentials.nix: %s pending -> active\n", name)
	fmt.Fprintf(stdout, "Committed %s on %s and pushed to origin; merging is yours\n", commit, branch)
}

func secretRekey(args []string) {
	name, checkoutArg := parseSecretArgs("rekey", args)
	checkout := resolveSecretsCheckout(checkoutArg)
	branch := requireLandingBranch(checkout)
	target := lookupSecret(checkout, name)
	target.branch = branch
	if target.state == "pending" {
		die(1, "credential '%s' is pending, so there is no ciphertext to rekey; run 'allod secret create %s'", name, name)
	}
	file := filepath.Join(checkout, target.path)
	original, err := os.ReadFile(file)
	if err != nil {
		die(1, "%s does not exist while '%s' is '%s'; the inventory check should have refused this branch", target.path, name, target.state)
	}

	identity := ageIdentityPath()
	value, err := secretDecrypt(identity, file)
	if err != nil {
		die(1, "could not decrypt %s with %s: %s", target.path, identity, err)
	}
	if len(bytes.TrimSpace(value)) == 0 {
		die(1, "%s decrypts to an empty value; refusing to re-encrypt nothing", target.path)
	}
	ciphertext := encryptOrDie(target, value)
	value = nil

	restore := func() string {
		if err := os.WriteFile(file, original, 0644); err != nil {
			fmt.Fprintf(stderr, "allod: could not restore %s: %s\n", file, err)
			return "the previous ciphertext could NOT be restored; inspect the tree before retrying"
		}
		return fmt.Sprintf("restored %s", target.path)
	}
	if err := os.WriteFile(file, ciphertext, 0644); err != nil {
		die(1, "could not write %s: %s; %s", file, err, restore())
	}
	if status := secretFlakeCheck(checkout); status != 0 {
		die(status, "the repository's checks failed; %s", restore())
	}

	commit := landCommit(checkout, branch, fmt.Sprintf("Rekey %s to the recipients secrets.nix declares", name), restore, target.path)
	fmt.Fprintf(stdout, "Rewrote %s, encrypted to %d recipients from secrets.nix\n", target.path, len(target.recipients))
	fmt.Fprintf(stdout, "Committed %s on %s and pushed to origin; merging is yours\n", commit, branch)
}

func encryptOrDie(target secretTarget, value []byte) []byte {
	ciphertext, err := secretEncrypt(target.recipients, value)
	if err != nil {
		die(1, "age failed to encrypt %s: %s", target.path, err)
	}
	if !bytes.HasPrefix(ciphertext, []byte("age-encryption.org/v1\n")) {
		die(1, "age produced something that is not an age file for %s; nothing written", target.path)
	}
	return ciphertext
}

// landCommit stages exactly the named files, commits, and pushes the current
// branch. A failed add or commit — a pre-commit hook, a missing author
// identity — unstages and restores the files, so the tree is as clean as
// every other refusal leaves it. A push failure leaves the commit in place
// and says so: the landing happened, the publication did not.
func landCommit(checkout, branch, message string, restore func() string, files ...string) string {
	commit, pushStatus, pushOutput := landCommitOrReportPush(checkout, branch, message, restore, files...)
	if pushStatus != 0 {
		fmt.Fprintf(stderr, "allod: committed %s but the push failed:\n%s\n", commit, pushOutput)
		die(pushStatus, "push the branch yourself once the cause is fixed: git -C %s push origin HEAD", checkout)
	}
	return commit
}

// currentBranchName reports HEAD's branch with 'git rev-parse --abbrev-ref
// HEAD', which prints the literal 'HEAD' for a detached checkout rather
// than the empty string 'git branch --show-current' (currentBranch, used by
// the initial gate) would. Either way a detached checkout will not equal
// the gated branch name, but the literal word makes landCommitOrReportPush's
// refusal read correctly instead of naming an empty branch.
func currentBranchName(checkout string) string {
	current, _ := gitOutput(checkout, "rev-parse", "--abbrev-ref", "HEAD")
	return current
}

// landCommitOrReportPush stages, commits, and attempts to push exactly like
// landCommit; unlike landCommit, a push failure is reported to the caller
// instead of being fatal. landCommit itself wraps this with the die() above,
// so create and rekey see no change in behavior; 'rotate' calls this
// directly because it has its deploy/verify/revocation steps still worth
// printing even when the push failed (see secret_rotate.go).
//
// branch is re-verified here, immediately before 'git add', because the
// caller's own gate ran before whatever checks it runs on the written
// files — a nix flake check evaluates the whole repository and can take a
// while — and a checkout that moved to a different branch during that wait
// must not have its landing committed there. Nothing runs between this
// check and the 'git add' below, so no further window is left for the
// branch to move again.
func landCommitOrReportPush(checkout, branch, message string, restore func() string, files ...string) (commit string, pushStatus int, pushOutput string) {
	if current := currentBranchName(checkout); current != branch {
		die(1, "%s moved from branch '%s' to '%s' while the checks ran; refusing to land on a branch nobody asked for; %s", checkout, branch, current, restore())
	}
	unstageAndRestore := func() string {
		captureCommand(checkout, nil, true, "git", append([]string{"reset", "-q", "--"}, files...)...)
		return restore()
	}
	args := append([]string{"add", "--"}, files...)
	if output, status := captureCommand(checkout, nil, true, "git", args...); status != 0 {
		die(status, "git add failed; %s:\n%s", unstageAndRestore(), output)
	}
	if output, status := captureCommand(checkout, nil, true, "git", "commit", "-q", "-m", message); status != 0 {
		die(status, "git commit failed; %s:\n%s", unstageAndRestore(), output)
	}
	commit, _ = gitOutput(checkout, "rev-parse", "--short", "HEAD")
	if output, status := captureCommand(checkout, nil, true, "git", "push", "origin", "HEAD"); status != 0 {
		return commit, status, output
	}
	return commit, 0, ""
}

// --- The world ---

// nixEvalJSON lives in secret_common.go, untagged, because 'declare' needs
// it too (to read a machine's type from the inventory flake) and only one
// copy may exist in the package.

func nixEvalCredentials(checkout string) (map[string]credentialEntry, error) {
	data, err := nixEvalJSON(checkout, "lib.credentials")
	if err != nil {
		return nil, err
	}
	var credentials map[string]credentialEntry
	if err := json.Unmarshal(data, &credentials); err != nil {
		return nil, fmt.Errorf("lib.credentials is not the expected shape: %w", err)
	}
	return credentials, nil
}

func nixEvalTokenGroups(checkout string) (map[string]tokenGroup, error) {
	data, err := nixEvalJSON(checkout, "lib.forgejoTokenGroups")
	if err != nil {
		return nil, err
	}
	var groups map[string]tokenGroup
	if err := json.Unmarshal(data, &groups); err != nil {
		return nil, fmt.Errorf("lib.forgejoTokenGroups is not the expected shape: %w", err)
	}
	return groups, nil
}

// nixEvalRecipients reads one path's recipients out of secrets.nix itself,
// the file agenix reads, rather than out of any list this command could be
// handed. The path has already passed validSecretPath, so it is safe to
// place inside a nix string literal.
func nixEvalRecipients(checkout, path string) ([]string, error) {
	expression := fmt.Sprintf(`(import ./secrets.nix)."%s".publicKeys`, path)
	var out, errOut bytes.Buffer
	cmd := exec.Command("nix", "eval", "--json", "--impure", "--expr", expression)
	cmd.Dir = checkout
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(errOut.String()))
	}
	var recipients []string
	if err := json.Unmarshal(out.Bytes(), &recipients); err != nil {
		return nil, fmt.Errorf("publicKeys is not a list of strings: %w", err)
	}
	return recipients, nil
}

// ageEncrypt runs age with the plaintext on stdin and the ciphertext on
// stdout. Recipients are public keys and may travel on argv; the value never
// does.
func ageEncrypt(recipients []string, plaintext []byte) ([]byte, error) {
	args := []string{"-e"}
	for _, recipient := range recipients {
		args = append(args, "-r", recipient)
	}
	var out, errOut bytes.Buffer
	cmd := exec.Command("age", args...)
	cmd.Stdin = bytes.NewReader(plaintext)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(errOut.String()))
	}
	return out.Bytes(), nil
}

func ageDecrypt(identity, file string) ([]byte, error) {
	var out, errOut bytes.Buffer
	cmd := exec.Command("age", "-d", "-i", identity, file)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(errOut.String()))
	}
	return out.Bytes(), nil
}

// nixFlakeCheck runs the repository's own checks with their output on
// stderr, so the operator watches the same thing a reviewer would.
func nixFlakeCheck(checkout string) int {
	return runCommand(checkout, nil, stderr, stderr, "nix", "flake", "check", "path:.")
}
