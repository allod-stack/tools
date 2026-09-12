//go:build secret

package main

// 'allod secret migrate' is the one-shot bridge from a legacy container
// credential to the declared value shape, run once per credential by the
// operator at the machine that holds the age identity. It is the only place
// in this program that still knows what 'credential-store-url' and
// 'rclone-remote-stanza' mean, and the only one that decrypts in order to
// write registry text: the whole point of the new shape is that the
// non-secret half is declared rather than recovered, and this command is
// how a credential that predates the declaration acquires one.
//
// Everything it can check without the plaintext, it checks first: the
// branch and the clean tree, the registry lookup, that the credential is
// still legacy, that no local_auth_refresh entry names it, that its format
// is one of the two containers, and that every one of its targets'
// structured verify objects converts to a command. Only then is the
// ciphertext decrypted, into memory and nowhere else, and the template
// built from it must pass two proofs before anything is written:
// substituting the extracted secret back into the template must reproduce
// the decrypted bytes exactly, and the extracted bytes must appear nowhere
// in the template outside the placeholder. Either proof failing is a
// refusal with no write.
//
// The ciphertext is never rewritten, so nothing a machine deploys changes;
// the commit is a registry edit and nothing else. Delete this file when
// legacy acceptance is removed from the secrets flake's check.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const secretMigrateDetail = `'migrate' rewrites one legacy container credential's rotation registry
entry into the declared value shape: 'format' becomes a 'value.template'
around one '{secret}' placeholder, and each target's structured 'verify'
object becomes the one command string rotate-token used to print for it.
The ciphertext is not touched, so no deployed file changes; the commit
holds forgejo-token-groups.json and nothing else.

It decrypts <name> once, in memory, only to recover the non-secret text
around the stored secret, and only after every check that does not need the
plaintext has passed. The decrypted bytes must match the exact byte shape
rotate-token writes for that format; substituting the extracted secret into
the proposed template must reproduce those bytes exactly; and the extracted
bytes must occur nowhere else in the template. The plaintext is never
written to disk, printed, or passed as an argument.

'migrate' refuses a credential that already has a 'value', one that is in
no registry group or in two, one named by a group's local_auth_refresh
(that consumer still needs the legacy shape — allod/nexus#52), and any
legacy format other than the two containers. A plain legacy entry
('raw-forgejo-token' or 'raw') needs no decryption at all: migrate it by
editing the registry directly, dropping 'format' and converting each
target's 'verify' object to its command string.

'migrate' works on the secrets checkout, found through the repository
registry under ~/work unless <checkout> names a worktree, and on whatever
branch is checked out there. It refuses the default branch and a dirty
tree: the landing is a commit on a branch, and merging stays your act.
`

// The exact byte shapes rotate-token writes for the two containers.
//
// Go's '$' matches only at the end of the text, never before a final
// newline, so a trailing newline has to be spelled out where one is
// accepted. rotate-token's credential-store branch encrypts
// 'printf %s' output with no trailing newline, while 'allod secret create'
// stores whatever was piped in, newline included; both are accepted and
// whichever was read round-trips byte for byte. Its rclone branch appends
// the newline remoteStanza (site_config.go) writes, so that one is
// required. The character classes are rotate-token's own
// (parse_credential_store_plaintext and parse_rclone_remote_stanza), not a
// stricter grammar invented here.
var (
	legacyCredentialStorePattern = regexp.MustCompile(`^https://([^:[:space:]][^:[:space:]]*):([^@[:space:]][^@[:space:]]*)@([^/[:space:]][^/[:space:]]*)\n?$`)
	legacyRcloneStanzaPattern    = regexp.MustCompile(`^\[shared\]\ntype = ftp\nhost = ([^[:cntrl:]]+)\nuser = ([^[:cntrl:]]+)\npass = ([^[:cntrl:]]+)\nexplicit_tls = true\n$`)
)

// migrateTarget and migrateCredential are the registry shapes this command
// rewrites. They are decoded with unknown fields refused: a field migrate
// does not model is a field it would silently drop when it re-renders the
// credential, and dropping registry data during a migration is exactly the
// failure this whole command exists to avoid.
type migrateTarget struct {
	System       string          `json:"system"`
	Kind         string          `json:"kind"`
	User         string          `json:"user,omitempty"`
	DeployedPath string          `json:"deployed_path"`
	Verify       json.RawMessage `json:"verify"`
}

type migrateCredential struct {
	Credential string           `json:"credential"`
	SecretPath string           `json:"secret_path"`
	Format     string           `json:"format,omitempty"`
	Value      *credentialValue `json:"value,omitempty"`
	Targets    []migrateTarget  `json:"targets"`
}

// legacyVerify is the structured object a legacy target carries.
type legacyVerify struct {
	Type              string `json:"type"`
	RepoURL           string `json:"repo_url,omitempty"`
	CredentialContext string `json:"credential_context,omitempty"`
}

func secretMigrate(args []string) {
	name, checkoutArg := parseSecretArgs("migrate", args)
	checkout := resolveSecretsCheckout(checkoutArg)
	branch := requireLandingBranch(checkout)

	groups, err := secretEvalRegistry(checkout)
	if err != nil {
		die(1, "could not evaluate lib.forgejoTokenGroups in %s: %s", checkout, err)
	}
	credential, alias, err := registryCredentialFor(groups, name)
	if err != nil {
		die(1, "%s", err)
	}
	if err := credentialShapeError(credential); err != nil {
		die(1, "%s", err)
	}
	if !isLegacyCredential(credential) {
		die(1, "credential '%s' already carries a declared value; migrate translates a legacy 'format' entry and there is nothing to translate", name)
	}
	for _, group := range groups {
		for _, refresh := range group.LocalAuthRefresh {
			if refresh.SourceCredential == name {
				die(1, "credential '%s' is the source of a local_auth_refresh entry, and that consumer still requires the legacy format and structured verify metadata; migrate it after allod/nexus#52 gives refresh-local-auth a new-shape implementation or retires it", name)
			}
		}
	}
	switch credential.Format {
	case "credential-store-url", "rclone-remote-stanza":
	default:
		die(1, "credential '%s' has legacy format '%s', which is not one of the two containers migrate translates (credential-store-url, rclone-remote-stanza). A plain legacy entry needs no decryption: edit forgejo-token-groups.json to drop its 'format' and turn each target's 'verify' object into its command string", name, credential.Format)
	}

	// Everything that can be settled without the plaintext is settled here,
	// before the ciphertext is opened: a refusal after a decrypt is a
	// decrypt nobody needed.
	registryPath := filepath.Join(checkout, "forgejo-token-groups.json")
	original, err := os.ReadFile(registryPath)
	if err != nil {
		die(1, "could not read %s: %s", registryPath, err)
	}
	start, end, err := findRegistryCredentialSpan(original, alias, name)
	if err != nil {
		die(1, "%s: %s", registryPath, err)
	}
	entry, err := decodeMigrateCredential(original[start:end])
	if err != nil {
		die(1, "the registry entry for '%s' in group '%s' %s", name, alias, err)
	}
	commands := make([]string, len(entry.Targets))
	for index, target := range entry.Targets {
		command, err := legacyVerifyCommand(target)
		if err != nil {
			die(1, "credential '%s' target '%s': %s", name, target.System, err)
		}
		commands[index] = command
	}

	target := lookupSecret(checkout, name)
	identity := ageIdentityPath()
	raw, err := secretDecrypt(identity, filepath.Join(checkout, target.path))
	if err != nil {
		die(1, "could not decrypt %s with %s: %s", target.path, identity, err)
	}
	value, extracted, err := legacyValueTemplate(credential.Format, raw, target.path)
	if err != nil {
		die(1, "%s", err)
	}
	// Byte reasoning throughout: no trim and no line normalization may turn
	// a non-canonical legacy plaintext into a declaration that renders
	// something else.
	rendered := []byte(strings.Replace(value.Template, credentialSecretPlaceholder, string(extracted), 1))
	if !bytes.Equal(rendered, raw) {
		die(1, "%s does not round-trip byte for byte through the template migrate would write for it; nothing was changed", target.path)
	}
	if bytes.Contains([]byte(strings.Replace(value.Template, credentialSecretPlaceholder, "", 1)), extracted) {
		die(1, "the secret stored in %s also occurs in the non-secret text around it, so a template would leak it; nothing was changed", target.path)
	}
	rendered, raw = nil, nil

	entry.Format = ""
	entry.Value = value
	for index := range entry.Targets {
		// Command-specific metadata belongs in the verify string now, not
		// in a registry field beside it, so the legacy 'user' is cleared
		// once it has been baked into the command above.
		entry.Targets[index].User = ""
		encoded, err := json.Marshal(commands[index])
		if err != nil {
			die(1, "could not encode the verify command for target '%s': %s", entry.Targets[index].System, err)
		}
		entry.Targets[index].Verify = encoded
	}
	replacement, err := renderRegistryCredential(entry, registryEntryIndent(original, start))
	if err != nil {
		die(1, "could not encode the migrated registry entry for '%s': %s", name, err)
	}
	updated := make([]byte, 0, len(original)-(end-start)+len(replacement))
	updated = append(updated, original[:start]...)
	updated = append(updated, replacement...)
	updated = append(updated, original[end:]...)
	if !json.Valid(updated) {
		die(1, "the migrated %s would not be valid JSON; nothing was changed", registryPath)
	}

	restore := func() string {
		if err := declareAtomicWrite(registryPath, original); err != nil {
			fmt.Fprintf(stderr, "allod: could not restore %s: %s\n", registryPath, err)
			return "forgejo-token-groups.json could NOT be restored; inspect it before retrying"
		}
		return "restored forgejo-token-groups.json"
	}
	if err := declareAtomicWrite(registryPath, updated); err != nil {
		die(1, "could not write %s: %s", registryPath, err)
	}
	if status := secretFlakeCheck(checkout); status != 0 {
		die(status, "the repository's checks failed; %s", restore())
	}
	commit := landCommit(checkout, branch, fmt.Sprintf("Migrate %s to a credential value template", name), restore, "forgejo-token-groups.json")
	fmt.Fprintf(stdout, "forgejo-token-groups.json: %s in group %s now declares a value template; %s is unchanged\n", name, alias, target.path)
	fmt.Fprintf(stdout, "Committed %s on %s and pushed to origin; merging is yours\n", commit, branch)
}

// legacyValueTemplate parses one legacy container's canonical bytes and
// returns the template that reproduces them and the secret field it
// extracted. Only the two containers are handled; the caller has already
// refused every other format.
func legacyValueTemplate(format string, raw []byte, path string) (*credentialValue, []byte, error) {
	switch format {
	case "credential-store-url":
		match := legacyCredentialStorePattern.FindSubmatch(raw)
		if match == nil {
			return nil, nil, fmt.Errorf("%s is not one canonical 'https://<user>:<token>@<host>' credential-store line; migrate translates only the exact bytes rotate-token writes", path)
		}
		trailing := ""
		if bytes.HasSuffix(raw, []byte("\n")) {
			// Whichever byte shape was stored is the one that must come
			// back out, so the newline travels in the template rather than
			// being normalised away.
			trailing = "\n"
		}
		template := "https://" + string(match[1]) + ":" + credentialSecretPlaceholder + "@" + string(match[3]) + trailing
		return &credentialValue{Template: template}, match[2], nil
	case "rclone-remote-stanza":
		match := legacyRcloneStanzaPattern.FindSubmatch(raw)
		if match == nil {
			return nil, nil, fmt.Errorf("%s is not one canonical [shared] rclone section with type, host, user, pass, and explicit_tls in that order and a trailing newline; migrate translates only the exact bytes rotate-token writes", path)
		}
		template := "[shared]\ntype = ftp\nhost = " + string(match[1]) + "\nuser = " + string(match[2]) +
			"\npass = " + credentialSecretPlaceholder + "\nexplicit_tls = true\n"
		// The ciphertext is unchanged by this migration, and the value it
		// currently stores is the obscured pass, not the password. The
		// round-trip proof above is therefore plain substitution of those
		// already-obscured bytes; the encoder named here is not applied
		// now. It describes what the next 'allod secret rotate' does: that
		// run takes a raw password and obscures it before substitution.
		return &credentialValue{Template: template, Encode: "rclone-obscure"}, match[3], nil
	default:
		return nil, nil, fmt.Errorf("migrate has no translation for legacy format '%s'", format)
	}
}

// legacyVerifyCommand renders the inner command rotate-token's
// print_group_verification prints for one legacy probe type. The consumer
// adds 'ssh <system>' around it for a remote target at print time (see
// printVerification), so this is the command as it runs on the target
// machine and nothing more.
func legacyVerifyCommand(target migrateTarget) (string, error) {
	var verify legacyVerify
	if err := json.Unmarshal(target.Verify, &verify); err != nil {
		return "", fmt.Errorf("its 'verify' is not a structured legacy probe object, so there is nothing to translate")
	}
	switch verify.Type {
	case "git-ls-remote":
		if verify.RepoURL == "" {
			return "", fmt.Errorf("its git-ls-remote verify carries no repo_url, so no command can be built from it")
		}
		return "sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote " + verify.RepoURL + " HEAD", nil
	case "forge-token-verify":
		if target.DeployedPath == "" {
			return "", fmt.Errorf("its forge-token-verify target carries no deployed_path, so no command can be built from it")
		}
		if target.Kind != "nixos-host" {
			return "forge token verify < " + target.DeployedPath, nil
		}
		if target.User == "" {
			return "", fmt.Errorf("its forge-token-verify target on a nixos-host carries no user, so no 'sudo -u <user>' command can be built from it")
		}
		return "sudo -u " + target.User + " forge token verify < " + target.DeployedPath, nil
	case "site-check":
		return "allod site check", nil
	case "tailscale-status":
		return "tailscale status --peers=false", nil
	default:
		return "", fmt.Errorf("its verify type '%s' is not one migrate can translate (git-ls-remote, forge-token-verify, site-check, tailscale-status); write the command into the registry by hand", verify.Type)
	}
}

// --- The registry edit ---

// decodeMigrateCredential reads one credential object out of the registry
// text and refuses any field migrate does not model, since re-rendering the
// object would drop it.
func decodeMigrateCredential(text []byte) (migrateCredential, error) {
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.DisallowUnknownFields()
	var entry migrateCredential
	if err := decoder.Decode(&entry); err != nil {
		return migrateCredential{}, fmt.Errorf("carries something migrate cannot rewrite without losing it: %s", err)
	}
	return entry, nil
}

// findRegistryCredentialSpan locates the byte range of one credential
// object inside the registry file, so migrate can replace exactly those
// bytes and leave every other byte of the file — including the formatting
// and every field of every other group — untouched. It walks the document
// with the JSON decoder rather than matching text, so a name that also
// appears inside some unrelated string cannot be mistaken for the entry.
func findRegistryCredentialSpan(text []byte, alias, name string) (int, int, error) {
	groupStart, groupText, err := findJSONMember(text, alias)
	if err != nil {
		return 0, 0, fmt.Errorf("group '%s': %s", alias, err)
	}
	credentialsStart, credentialsText, err := findJSONMember(groupText, "credentials")
	if err != nil {
		return 0, 0, fmt.Errorf("group '%s': credentials: %s", alias, err)
	}
	elementStart, elementText, err := findJSONCredential(credentialsText, name)
	if err != nil {
		return 0, 0, fmt.Errorf("group '%s': %s", alias, err)
	}
	start := groupStart + credentialsStart + elementStart
	return start, start + len(elementText), nil
}

// findJSONMember returns the offset of one object member's value within
// object, and the value's exact input bytes.
func findJSONMember(object []byte, key string) (int, []byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(object))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return 0, nil, fmt.Errorf("expected a JSON object")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return 0, nil, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return 0, nil, err
		}
		if token == key {
			return int(decoder.InputOffset()) - len(value), value, nil
		}
	}
	return 0, nil, fmt.Errorf("no '%s' member", key)
}

// findJSONCredential returns the offset and exact input bytes of the array
// element whose "credential" is name.
func findJSONCredential(array []byte, name string) (int, []byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(array))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return 0, nil, fmt.Errorf("credentials is not a JSON array")
	}
	for decoder.More() {
		var element json.RawMessage
		if err := decoder.Decode(&element); err != nil {
			return 0, nil, err
		}
		var named struct {
			Credential string `json:"credential"`
		}
		if err := json.Unmarshal(element, &named); err != nil {
			return 0, nil, err
		}
		if named.Credential == name {
			return int(decoder.InputOffset()) - len(element), element, nil
		}
	}
	return 0, nil, fmt.Errorf("no credential named '%s'", name)
}

// registryEntryIndent is the whitespace the credential object's own line
// starts with, so the replacement lands at the column the rest of the file
// uses.
func registryEntryIndent(text []byte, start int) string {
	lineStart := bytes.LastIndexByte(text[:start], '\n') + 1
	prefix := text[lineStart:start]
	if len(bytes.TrimLeft(prefix, " \t")) != 0 {
		return ""
	}
	return string(prefix)
}

// renderRegistryCredential renders the migrated entry at the given
// indentation. HTML escaping is off: a verify command routinely contains
// '<' (a redirection), and '<' in the registry would be correct JSON
// and unreadable text.
func renderRegistryCredential(entry migrateCredential, indent string) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent(indent, "  ")
	if err := encoder.Encode(entry); err != nil {
		return nil, err
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}
