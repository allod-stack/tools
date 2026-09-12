//go:build secret

package main

// 'allod secret rotate' replaces a credential's value on the host: the same
// landing 'create' and 'rekey' do, applied to the third case — a value that
// already has a ciphertext and whose recipients are not changing. It ports
// the group-rotation half of nexus's rotate-token (about 1450 lines of
// bash): group resolution, the three plaintext formats, dry run, and the
// printed deploy/verify/revocation steps. Out of the port: rotate-token's
// --group/--forgejo-token/--allow-single-secret selectors (a credential name
// is the only selector here, and it always names its whole registry group,
// the way '--group' rotates a shared token today), and refresh-local-auth
// (it installs root-owned files under sudo, a privilege this command does
// not hold; see printDeploySteps).
//
// The unit of rotation is the registry group: every credential the group
// lists is re-encrypted from the one value read on stdin, exactly as
// rotate-token's '--group' rotates a shared Forgejo token from one prompt.
// A structured format decrypts its old ciphertext first and carries the
// non-secret fields forward, the same way rotate-token's prepare_item does;
// everything stays in memory until every ciphertext is staged, then one
// nix flake check gates one commit and push, exactly like 'create' and
// 'rekey' land. No rotation_state change: every entry stays "active".
//
// A dry run reads and decrypts nothing: it runs every gate this command
// applies to the group (branch, clean tree, active state, ciphertext
// present, recipients, registry shape, uniform format) but not the
// repository's own nix flake check, which only a real landing runs.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const secretRotateDetail = `'rotate' replaces a value: the rotation registry group that lists <name>
among its credentials is the whole unit, so every credential in that group
is re-encrypted from the one value read on stdin, the way 'rotate-token
--group' rotates a shared Forgejo token today. Every credential in the
group must already be active with a ciphertext on disk. A structured
format (credential-store-url, rclone-remote-stanza) decrypts the old
ciphertext first and carries its non-secret fields forward; a group mixing
formats that cannot share one prompted value is refused. A structured
value may not contain whitespace, a control character, or (for
credential-store-url) '@', '/', ':', '?', '#', or '%' — git
percent-decodes a credential-store URL's password, so an unrefused '%'
would let the stored value differ from the one that was typed.

'--dry-run' runs every gate this command applies to the group — branch,
clean tree, active state, ciphertext present, recipients, registry shape,
unique group membership, uniform format — but not the repository's own
checks, which run only on a real landing; it prints the group, its
targets, the deploy and verification steps, and the revocation gate,
describing what a live run would do rather than instructing it, and reads
no value, decrypts nothing, and writes nothing. rotation_state does not
change. A group with a local_auth_refresh entry gets one more printed
step naming 'rotate-token refresh-local-auth --group <alias>' — an
instruction on a live run, phrased as what a live run would print on a
dry run — because that part of rotate-token stays a separate host
script: it installs root-owned files under sudo, a privilege this
command does not hold. The branch is re-verified immediately before
writing and again immediately before committing, refusing (with every
ciphertext already written restored) if the checkout moved in between.

'rotate' works on the secrets checkout, found through the repository
registry under ~/work unless <checkout> names a worktree, and on whatever
branch is checked out there. It refuses the default branch and a dirty
tree: the landing is a commit on a branch, and merging stays your act.

The identity is $AGE_IDENTITY, or ~/.ssh/host; its .pub must be among the
recipients. The plaintext is never written to disk, printed, or passed as
an argument.
`

// --- Argument parsing ---

// parseSecretRotateArgs reads 'rotate's shape: '<name> [<checkout>]
// [--dry-run]'. It is not parseSecretArgs (create and rekey take no
// options at all) because --dry-run exists only here.
func parseSecretRotateArgs(args []string) (name, checkout string, dryRun bool) {
	var positional []string
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			fmt.Fprint(stdout, secretCommandHelp("rotate"))
			exit(0)
		case arg == "--dry-run":
			dryRun = true
		case strings.HasPrefix(arg, "-"):
			secretCommandUsageError("rotate", "unknown option for secret rotate: %s", arg)
		default:
			positional = append(positional, arg)
		}
	}
	switch len(positional) {
	case 1:
		return positional[0], "", dryRun
	case 2:
		return positional[0], positional[1], dryRun
	case 0:
		secretCommandUsageError("rotate", "secret rotate requires a credential name")
	default:
		secretCommandUsageError("rotate", "secret rotate takes a credential name and at most one checkout path")
	}
	return "", "", false
}

// --- Group resolution and metadata ---

// selectRotationGroup finds the one registry group that lists name among its
// credentials, and returns the whole registry alongside it so the caller
// can check every other member of that group too (see
// verifyGroupMembersUnique) without a second evaluation. A credential
// belongs to exactly one group in a well-formed registry; this refuses both
// "no group" and "more than one" for the requested name, the way
// lookupSecret already refuses a credential with more than one agenix
// consumer, rather than guessing which the operator meant.
func selectRotationGroup(checkout, name string) (string, tokenGroup, map[string]tokenGroup) {
	groups, err := secretEvalRegistry(checkout)
	if err != nil {
		die(1, "could not evaluate lib.forgejoTokenGroups in %s: %s", checkout, err)
	}
	var aliases []string
	for alias, group := range groups {
		for _, credential := range group.Credentials {
			if credential.Credential == name {
				aliases = append(aliases, alias)
				break
			}
		}
	}
	sort.Strings(aliases)
	switch len(aliases) {
	case 0:
		die(1, "no rotation registry entry for '%s' in %s/forgejo-token-groups.json; rotate operates on the registry group a credential belongs to", name, checkout)
	case 1:
		return aliases[0], groups[aliases[0]], groups
	default:
		die(1, "credential '%s' is registered in more than one rotation registry group (%s) in %s/forgejo-token-groups.json; fix the registry so each credential belongs to one group", name, strings.Join(aliases, ", "), checkout)
	}
	return "", tokenGroup{}, nil
}

// verifyGroupMembersUnique checks not just the requested credential but
// every member of the selected group: a group is not a safe rotation unit
// if one of its other credentials also sits in a second registry group,
// since a run here would land that credential's new value without anyone
// having asked whether the second group's rotation also needed it.
func verifyGroupMembersUnique(group tokenGroup, groups map[string]tokenGroup) {
	for _, credential := range group.Credentials {
		var aliases []string
		for alias, candidate := range groups {
			for _, member := range candidate.Credentials {
				if member.Credential == credential.Credential {
					aliases = append(aliases, alias)
					break
				}
			}
		}
		sort.Strings(aliases)
		if len(aliases) > 1 {
			die(1, "credential '%s' is registered in more than one rotation registry group (%s); fix the registry so each credential belongs to one group", credential.Credential, strings.Join(aliases, ", "))
		}
	}
}

var (
	validRegistryService    = map[string]bool{"forgejo": true, "none": true}
	validRotationStrategy   = map[string]bool{"overlap": true, "in-place": true}
	validCredentialFormat   = map[string]bool{"raw-forgejo-token": true, "credential-store-url": true, "rclone-remote-stanza": true}
	validRegistryTargetKind = map[string]bool{"nixos-host": true, "dev-vm": true, "privacy-vm": true, "service-vm": true}
	validRegistryVerifyType = map[string]bool{"forge-token-verify": true, "git-ls-remote": true, "site-check": true}
)

// validateGroupMetadata checks the registry shape rotate-token's
// validate_group_metadata checks, so a group missing a field this command
// needs is refused with what is wrong rather than a blank line in the
// printed steps or a decrypt attempt against the wrong target.
func validateGroupMetadata(alias string, group tokenGroup) {
	fail := func(reason string) {
		die(1, "rotation registry group '%s' has unsupported metadata: %s", alias, reason)
	}
	if !validRegistryService[group.Service] {
		fail(fmt.Sprintf("service '%s' is neither 'forgejo' nor 'none'", group.Service))
	}
	if group.RegistryAlias == "" {
		fail("registry_alias is empty")
	}
	if group.Service == "forgejo" && (group.Account == "" || group.UITokenName == "") {
		fail("a forgejo group needs a non-empty account and ui_token_name")
	}
	if !validRotationStrategy[group.RotationStrategy] {
		fail(fmt.Sprintf("rotation_strategy '%s' is neither 'overlap' nor 'in-place'", group.RotationStrategy))
	}
	if len(group.Credentials) == 0 {
		fail("credentials is empty")
	}
	for _, credential := range group.Credentials {
		if credential.Credential == "" || credential.SecretPath == "" {
			fail("a credential entry is missing its credential name or secret_path")
		}
		if !validCredentialFormat[credential.Format] {
			fail(fmt.Sprintf("credential '%s' has unsupported format '%s'", credential.Credential, credential.Format))
		}
		if len(credential.Targets) == 0 {
			fail(fmt.Sprintf("credential '%s' has no targets", credential.Credential))
		}
		for _, target := range credential.Targets {
			if target.System == "" || target.DeployedPath == "" {
				fail(fmt.Sprintf("a target of credential '%s' is missing system or deployed_path", credential.Credential))
			}
			if !validRegistryTargetKind[target.Kind] {
				fail(fmt.Sprintf("a target of credential '%s' has unsupported kind '%s'", credential.Credential, target.Kind))
			}
			if !validRegistryVerifyType[target.Verify.Type] {
				fail(fmt.Sprintf("a target of credential '%s' has unsupported verify type '%s'", credential.Credential, target.Verify.Type))
			}
		}
	}
	for _, refresh := range group.LocalAuthRefresh {
		if refresh.Contract != "nixos-netrc-from-root-git-credentials" {
			fail(fmt.Sprintf("local_auth_refresh entry has unsupported contract '%s'", refresh.Contract))
		}
		if refresh.System == "" || refresh.LocalUsername == "" || refresh.SourceCredential == "" {
			fail("a local_auth_refresh entry is missing system, local_username, or source_credential")
		}
		matches := 0
		for _, credential := range group.Credentials {
			if credential.Credential != refresh.SourceCredential || credential.Format != "credential-store-url" {
				continue
			}
			for _, target := range credential.Targets {
				if target.System == refresh.System && target.DeployedPath == "/root/.git-credentials" {
					matches++
				}
			}
		}
		if matches != 1 {
			fail(fmt.Sprintf("local_auth_refresh source_credential '%s' does not name exactly one credential-store-url target at %s:/root/.git-credentials", refresh.SourceCredential, refresh.System))
		}
	}
}

// assertUniformPromptedFormat refuses a group whose formats cannot share one
// prompted value: rclone-remote-stanza treats the prompted value as a
// password to obscure, every other format treats it as a token to store or
// splice into a URL, and feeding one prompted value to both meanings would
// silently obscure a token or store a password verbatim. Ported from
// rotate-token's assert_uniform_prompted_format.
func assertUniformPromptedFormat(alias string, credentials []registryCredential) {
	hasStanza, hasOther := false, false
	for _, credential := range credentials {
		if credential.Format == "rclone-remote-stanza" {
			hasStanza = true
		} else {
			hasOther = true
		}
	}
	if hasStanza && hasOther {
		die(1, "rotation registry group '%s' mixes rclone-remote-stanza with a token format; one prompted value cannot rotate both a password and a token", alias)
	}
}

func groupCommitSubject(alias string, group tokenGroup) string {
	if group.Service == "forgejo" {
		return fmt.Sprintf("rotate %s Forgejo token", alias)
	}
	return fmt.Sprintf("rotate %s", alias)
}

// --- The value, per format ---

// credentialStoreURLPattern matches exactly what rotate-token's
// parse_credential_store_plaintext accepts: one https://user:token@host line.
var credentialStoreURLPattern = regexp.MustCompile(`^https://([^:\s][^:\s]*):([^@\s][^@\s]*)@([^/\s][^/\s]*)$`)

// parseCredentialStorePlaintext ports rotate-token's
// parse_credential_store_plaintext: the decrypted secret must be exactly one
// non-empty line shaped https://<user>:<token>@<host>. Only user and host
// are returned; the old token is discarded unread.
func parseCredentialStorePlaintext(plaintext []byte, path string) (user, host string, err error) {
	var first string
	nonEmpty := 0
	for _, line := range strings.Split(string(plaintext), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonEmpty++
		if nonEmpty == 1 {
			first = line
		}
	}
	if nonEmpty != 1 {
		return "", "", fmt.Errorf("credential-store secret '%s' must contain exactly one non-empty line", path)
	}
	match := credentialStoreURLPattern.FindStringSubmatch(first)
	if match == nil {
		return "", "", fmt.Errorf("credential-store secret '%s' is not a supported https://user:token@host URL", path)
	}
	return match[1], match[3], nil
}

// rcloneStanzaPattern matches the exact stanza remoteStanza (site_config.go,
// behind the 'site' tag) writes: one '[shared]' section with type, host,
// user, pass, and explicit_tls, in that order, nothing else. Ported from
// rotate-token's parse_rclone_remote_stanza. secret_rotate.go cannot import
// remoteStanza across build tags (a -tags secret build carries no 'site'
// code), so this file ports the small builder below instead of sharing it;
// both are one line, and 'shared' is site_config.go's siteRemoteName.
var rcloneStanzaPattern = regexp.MustCompile(`^\[shared\]\ntype = ftp\nhost = ([^[:cntrl:]]+)\nuser = ([^[:cntrl:]]+)\npass = [^[:cntrl:]]+\nexplicit_tls = true$`)

// parseRcloneRemoteStanza returns the host and user a decrypted stanza
// carries forward; the old obscured password is discarded unread. Trailing
// newlines are trimmed first, matching what rotate-token's
// existing=$(age -d ...) leaves after bash's command substitution strips
// them.
func parseRcloneRemoteStanza(plaintext []byte, path string) (host, user string, err error) {
	trimmed := strings.TrimRight(string(plaintext), "\n")
	match := rcloneStanzaPattern.FindStringSubmatch(trimmed)
	if match == nil {
		return "", "", fmt.Errorf("rclone-remote-stanza secret '%s' is not exactly one [shared] section with type, host, user, pass, and explicit_tls", path)
	}
	return match[1], match[2], nil
}

// buildRcloneRemoteStanza renders the stanza in the exact byte shape
// remoteStanza (site_config.go) writes, so the ciphertext rclone reads is
// unchanged by anything but pass. Ported rather than shared; see
// rcloneStanzaPattern's comment.
func buildRcloneRemoteStanza(host, user, obscured string) string {
	return fmt.Sprintf("[shared]\ntype = ftp\nhost = %s\nuser = %s\npass = %s\nexplicit_tls = true\n", host, user, obscured)
}

var base64URLToken = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// obscureRclonePassword runs 'rclone obscure -' with the password on stdin,
// never on argv: arguments are world-readable in /proc/<pid>/cmdline for as
// long as the process lives. The obscured form is reversible by anyone with
// rclone, so it is exactly as sensitive as the password and is never printed
// or logged, same as the password itself. This is a real subprocess, not a
// test seam, the way landCommit's git is: a test fakes 'rclone' on PATH.
// secretRotate checks 'rclone' is on PATH before it ever reads the value
// (see requireRcloneOnPath); the exec.ErrNotFound branch here is a second,
// harmless line of defense against a PATH that changed in between.
func obscureRclonePassword(password string) (string, error) {
	var out, errOut bytes.Buffer
	cmd := exec.Command("rclone", "obscure", "-")
	cmd.Stdin = strings.NewReader(password)
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("rclone not found on PATH; the rclone-remote-stanza format needs it to obscure the new password")
		}
		return "", fmt.Errorf("failed to obscure the new password: %s", strings.TrimSpace(errOut.String()))
	}
	obscured := strings.TrimRight(out.String(), "\r\n")
	if !base64URLToken.MatchString(obscured) {
		return "", errors.New("'rclone obscure -' printed something other than one base64url token")
	}
	return obscured, nil
}

// requireRcloneOnPath is checked before the value is ever read from stdin,
// not after: asking the operator to paste a password only to refuse it for
// a missing binary wastes the paste and, worse, leaves it sitting in shell
// history or a terminal scrollback for no reason.
func requireRcloneOnPath() {
	if _, err := exec.LookPath("rclone"); err != nil {
		die(1, "rclone not found on PATH; the rclone-remote-stanza format needs it to obscure the new password")
	}
}

// trimOnePromptedLine strips one trailing line ending, the way rotate-token's
// 'read -r' always does. The structured formats splice the prompted value
// into one line of a URL or feed it to 'rclone obscure -' as a password, so
// a trailing newline left over from an ordinary 'echo token | ...' pipe
// would otherwise land inside the credential. raw-forgejo-token keeps the
// verbatim value instead (see secretRotate), matching create's documented
// "trailing newline included" contract.
func trimOnePromptedLine(value []byte) []byte {
	text := strings.TrimSuffix(string(value), "\n")
	text = strings.TrimSuffix(text, "\r")
	return []byte(text)
}

// validateStructuredValue refuses a prompted value that could corrupt a
// structured format once trimOnePromptedLine has removed the one line
// ending an ordinary pipe leaves. rclone-remote-stanza treats the value as
// a password fed to 'rclone obscure -'; credential-store-url splices it
// directly into a URL. Either way, an embedded control character or any
// other whitespace (a second line, a tab, a stray space) does not belong in
// one token, and credential-store-url additionally cannot tolerate a
// character its own URL syntax reserves — without this check, a value with
// an embedded newline or an '@' would silently produce a malformed URL that
// still gets encrypted, checked, committed, and pushed. '%' is refused for
// the same reason even though it is not a URL delimiter itself: git
// percent-decodes a credential-store URL's password, so a value containing
// 'tok%40x' would be stored verbatim but later supplied to git as 'tok@x' —
// a different value than the one that was encrypted.
func validateStructuredValue(value []byte, format, path string) error {
	reserved := ""
	if format == "credential-store-url" {
		reserved = "@/:?#%"
	}
	for _, r := range string(value) {
		switch {
		case unicode.IsControl(r):
			return fmt.Errorf("the new value for %s contains a control character; %s needs one plain token with nothing else in it", path, format)
		case unicode.IsSpace(r):
			return fmt.Errorf("the new value for %s contains whitespace; %s needs one plain token with no embedded whitespace", path, format)
		case strings.ContainsRune(reserved, r):
			return fmt.Errorf("the new value for %s contains %q, which credential-store-url reserves for its URL syntax", path, string(r))
		}
	}
	return nil
}

// writeCiphertextAtomic writes data to a temporary file in the same
// directory as path and renames it into place, so a crash or a killed
// process leaves the file as either the old bytes or the new ones and never
// a truncated partial write. The temporary file is removed on any failure
// before the rename; a failure at or after the rename is vanishingly
// unlikely (both calls are in the same directory) and is reported as any
// other write failure, which already triggers the group's restore path.
func writeCiphertextAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rotate-"+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil {
		os.Remove(tmpPath)
		return writeErr
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return closeErr
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// --- The command ---

// rotationItem is everything rotate has verified and decrypted about one
// credential in the selected group before it prompts for the new value.
type rotationItem struct {
	credential string
	format     string
	target     secretTarget
	fullPath   string
	username   string // credential-store-url, rclone-remote-stanza
	host       string // credential-store-url, rclone-remote-stanza
	original   []byte
	ciphertext []byte
}

func secretRotate(args []string) {
	name, checkoutArg, dryRun := parseSecretRotateArgs(args)
	checkout := resolveSecretsCheckout(checkoutArg)
	// Same gate as create and rekey, run whether or not this is a dry run:
	// a dry run's whole purpose is to surface a refusal before the value is
	// ever asked for, so it does not get bash's dry-run exemption from the
	// clean-tree check.
	branch := requireLandingBranch(checkout)

	alias, group, groups := selectRotationGroup(checkout, name)
	validateGroupMetadata(alias, group)
	assertUniformPromptedFormat(alias, group.Credentials)
	verifyGroupMembersUnique(group, groups)

	items := make([]rotationItem, len(group.Credentials))
	for i, credential := range group.Credentials {
		target := lookupSecret(checkout, credential.Credential)
		target.branch = branch
		if target.state != "active" {
			die(1, "credential '%s' is '%s', not 'active'; rotate re-encrypts an existing ciphertext. For its first value run 'allod secret create %s'", credential.Credential, target.state, credential.Credential)
		}
		full := filepath.Join(checkout, target.path)
		original, err := os.ReadFile(full)
		if err != nil {
			die(1, "%s does not exist while '%s' is 'active'; the inventory check should have refused this branch", target.path, credential.Credential)
		}
		items[i] = rotationItem{
			credential: credential.Credential,
			format:     credential.Format,
			target:     target,
			fullPath:   full,
			original:   original,
		}
		// A dry run reads and decrypts nothing: the printed steps below
		// never use the decrypted user or host, only the registry's own
		// fields, so decrypting here would violate the "no prompt, decrypt,
		// or encrypt" promise the banner makes for no benefit.
		if dryRun {
			continue
		}
		switch credential.Format {
		case "credential-store-url":
			identity := ageIdentityPath()
			plaintext, err := secretDecrypt(identity, full)
			if err != nil {
				die(1, "could not decrypt %s with %s: %s", target.path, identity, err)
			}
			user, host, parseErr := parseCredentialStorePlaintext(plaintext, target.path)
			if parseErr != nil {
				die(1, "%s", parseErr)
			}
			items[i].username, items[i].host = user, host
		case "rclone-remote-stanza":
			identity := ageIdentityPath()
			plaintext, err := secretDecrypt(identity, full)
			if err != nil {
				die(1, "could not decrypt %s with %s: %s", target.path, identity, err)
			}
			host, user, parseErr := parseRcloneRemoteStanza(plaintext, target.path)
			if parseErr != nil {
				die(1, "%s", parseErr)
			}
			items[i].username, items[i].host = user, host
		}
	}

	commitSubject := groupCommitSubject(alias, group)

	if dryRun {
		fmt.Fprintln(stderr, "Dry run: no prompt, decrypt, or encrypt will run.")
		printGroupSummary(stderr, alias, group)
		printDeploySteps(stderr, checkout, alias, branch, group, commitSubject, deployStepsDryRun)
		printVerification(stderr, group)
		printRevocationGate(stderr, group)
		return
	}

	needsRclone := false
	for _, item := range items {
		if item.format == "rclone-remote-stanza" {
			needsRclone = true
			break
		}
	}
	if needsRclone {
		requireRcloneOnPath()
	}

	raw := readSecretValue(alias)
	value := trimOnePromptedLine(raw)
	for i := range items {
		item := &items[i]
		var plaintext []byte
		switch item.format {
		case "raw-forgejo-token":
			plaintext = raw
		case "credential-store-url":
			if err := validateStructuredValue(value, item.format, item.target.path); err != nil {
				die(1, "%s", err)
			}
			plaintext = []byte(fmt.Sprintf("https://%s:%s@%s", item.username, value, item.host))
		case "rclone-remote-stanza":
			if err := validateStructuredValue(value, item.format, item.target.path); err != nil {
				die(1, "%s", err)
			}
			obscured, err := obscureRclonePassword(string(value))
			if err != nil {
				die(1, "%s for %s", err, item.target.path)
			}
			plaintext = []byte(buildRcloneRemoteStanza(item.host, item.username, obscured))
		}
		item.ciphertext = encryptOrDie(item.target, plaintext)
	}
	raw, value = nil, nil

	// The branch was gated once above, before the (potentially long) wait
	// for the value on stdin or at the terminal. Re-check it now, right
	// before anything is written: another process could have switched the
	// checkout to a different branch — or dirtied it — while this one was
	// blocked reading input, and landing on whatever branch happens to be
	// checked out now would be the wrong commit on the wrong branch.
	if current := requireLandingBranch(checkout); current != branch {
		die(1, "%s moved from branch '%s' to '%s' while rotate was waiting for the value; refusing to land on a branch nobody asked for. Start over on '%s'", checkout, branch, current, branch)
	}

	// Nothing has been written until here. From here on, a failure restores
	// every ciphertext before reporting, and says so if it could not. The
	// restore writes through the same temp-file-and-rename helper the new
	// ciphertexts use, so a crash mid-restore leaves the file as either the
	// new bytes or the original ones, never a truncated write either way.
	restore := func() string {
		var restored []string
		problems := 0
		for _, item := range items {
			if err := writeCiphertextAtomic(item.fullPath, item.original, 0644); err != nil {
				fmt.Fprintf(stderr, "allod: could not restore %s: %s\n", item.target.path, err)
				problems++
				continue
			}
			restored = append(restored, item.target.path)
		}
		if problems > 0 {
			return "the tree could NOT be fully restored; inspect it before retrying"
		}
		return fmt.Sprintf("restored %s", strings.Join(restored, ", "))
	}

	var paths []string
	for _, item := range items {
		// Written to a temporary file in the same directory and renamed
		// into place, so a crash mid-write leaves the old ciphertext intact
		// rather than a truncated one: the file is always either the old
		// bytes or the new ones, never a partial write of either.
		if err := writeCiphertextAtomic(item.fullPath, item.ciphertext, 0644); err != nil {
			die(1, "could not write %s: %s; %s", item.target.path, err, restore())
		}
		paths = append(paths, item.target.path)
	}
	if status := secretFlakeCheck(checkout); status != 0 {
		die(status, "the repository's checks failed; %s", restore())
	}

	// landCommitOrReportPush, not landCommit: a push failure still leaves
	// this group's deploy, verify, refresh-local-auth, and revocation steps
	// worth printing (the commit is real; a rebuild and a manual push are
	// still the operator's next move), so the steps print unconditionally
	// below and the push failure is reported, and the command exits
	// non-zero on it, only after that. landCommitOrReportPush re-verifies
	// branch itself, immediately before 'git add', closing the window the
	// flake check above (which can run for a while) leaves open after the
	// re-check already done before the writes.
	commit, pushStatus, pushOutput := landCommitOrReportPush(checkout, branch, commitSubject, restore, paths...)
	for _, item := range items {
		fmt.Fprintf(stdout, "Wrote %s, encrypted to %d recipients from secrets.nix\n", item.target.path, len(item.target.recipients))
	}
	pushed := pushStatus == 0
	if pushed {
		fmt.Fprintf(stdout, "Committed %s on %s and pushed to origin; merging is yours\n", commit, branch)
	} else {
		fmt.Fprintf(stdout, "Committed %s on %s; the push failed (see below)\n", commit, branch)
	}

	printGroupSummary(stderr, alias, group)
	deploySteps := deployStepsLandedPushed
	if !pushed {
		deploySteps = deployStepsLandedNotPushed
	}
	printDeploySteps(stderr, checkout, alias, branch, group, commitSubject, deploySteps)
	printVerification(stderr, group)
	printRevocationGate(stderr, group)

	if !pushed {
		fmt.Fprintf(stderr, "allod: committed %s but the push failed:\n%s\n", commit, pushOutput)
		die(pushStatus, "push the branch yourself once the cause is fixed: git -C %s push origin HEAD", checkout)
	}
}

// --- Printed steps ---

// dashIfEmpty is jq's own idiom in rotate-token's printers ('.user // "-"'):
// a field the registry leaves out prints as '-' rather than an empty field.
func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// printGroupSummary ports rotate-token's print_group_summary: the group,
// its service, strategy, every affected secret and its format, and every
// target with its verification probe. The local-auth-refresh timing prose
// bash prints in this function is not ported: the printed deploy step is
// the whole of what 'rotate' says about refresh-local-auth (see
// printDeploySteps), by the overseer's decision that it needs "nothing else
// about it".
func printGroupSummary(w io.Writer, alias string, group tokenGroup) {
	if group.Service == "forgejo" {
		fmt.Fprintf(w, "Forgejo group: %s\n", alias)
		fmt.Fprintf(w, "Forgejo token: %s/%s\n", group.Account, group.UITokenName)
	} else {
		fmt.Fprintf(w, "Registry group: %s\n", alias)
		fmt.Fprintln(w, "Service: none (not a Forgejo token)")
	}
	fmt.Fprintf(w, "Provider rotation strategy: %s\n", group.RotationStrategy)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Affected secrets:")
	for _, credential := range group.Credentials {
		fmt.Fprintf(w, "  - %s (%s, %s)\n", credential.SecretPath, credential.Credential, credential.Format)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Affected targets:")
	for _, credential := range group.Credentials {
		for _, target := range credential.Targets {
			if target.Verify.Type == "git-ls-remote" {
				fmt.Fprintf(w, "  - %s (%s): %s, verify %s as %s\n",
					target.System, target.Kind, target.DeployedPath, target.Verify.RepoURL, dashIfEmpty(target.Verify.CredentialContext))
			} else {
				fmt.Fprintf(w, "  - %s (%s): %s, verify as %s\n",
					target.System, target.Kind, target.DeployedPath, dashIfEmpty(target.User))
			}
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "--- Provider behavior ---")
	if group.RotationStrategy == "in-place" {
		fmt.Fprintln(w, "Provider rotation invalidates the old token when the replacement token is generated.")
	} else {
		fmt.Fprintln(w, "The old token should remain usable until deployment and verification complete.")
	}
}

// rebuildTarget is one (system, kind) pair the rebuild step names.
type rebuildTarget struct{ system, kind string }

// uniqueRebuildTargets dedupes a group's targets by system+kind and sorts
// the result by "system:kind", matching jq's unique_by(f) in
// print_group_deploy_instructions: unique_by sorts by the key function, it
// does not merely stabilize input order.
func uniqueRebuildTargets(group tokenGroup) []rebuildTarget {
	seen := map[string]bool{}
	var targets []rebuildTarget
	for _, credential := range group.Credentials {
		for _, target := range credential.Targets {
			key := target.System + ":" + target.Kind
			if seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, rebuildTarget{target.System, target.Kind})
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].system+":"+targets[i].kind < targets[j].system+":"+targets[j].kind
	})
	return targets
}

func printRebuildCommand(w io.Writer, system, kind string) {
	switch kind {
	case "nixos-host":
		fmt.Fprintf(w, "   sudo nixos-rebuild switch --flake ~/work/allod/deploy#%s\n", system)
	case "dev-vm", "privacy-vm", "service-vm":
		fmt.Fprintf(w, "   rebuild-vm-from-host %s\n", system)
	default:
		die(1, "unsupported target kind '%s' for %s", kind, system)
	}
}

// deployStepsLandingState says what printDeploySteps's "land the commit"
// step should say: rotate has already committed and pushed by the time it
// prints its steps (or dry-run mode ran nothing at all), so this step never
// instructs the operator to run git themselves — that would contradict what
// the command just did, or claim it did something a dry run did not.
type deployStepsLandingState int

const (
	deployStepsDryRun deployStepsLandingState = iota
	deployStepsLandedPushed
	deployStepsLandedNotPushed
)

// printDeploySteps ports rotate-token's print_group_deploy_instructions.
// Unlike rotate-token, this command has already landed the commit by the
// time it prints (or, in a dry run, will land it exactly this way on a real
// run), so the "commit and push" step describes what happened instead of
// instructing the operator to run git themselves. When the group carries
// local_auth_refresh entries, step 1 names 'rotate-token refresh-local-auth
// --group <alias>' as the operator's next step; 'rotate' never runs it (see
// secret_rotate.go's file comment).
func printDeploySteps(w io.Writer, checkout, alias, branch string, group tokenGroup, commitSubject string, landing deployStepsLandingState) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "--- Deployment steps ---")
	step := 1
	if len(group.LocalAuthRefresh) > 0 {
		if landing == deployStepsDryRun {
			// No ciphertext has been rotated by a dry run, so this cannot
			// read as something to do now: it describes what the printed
			// steps will say once a live run actually lands the secret.
			fmt.Fprintf(w, "%d. After the real run lands the rotated secret, refresh declared local auth before any git push, flake-lock update, or rebuild fetch:\n   rotate-token refresh-local-auth --group %s\n\n", step, alias)
		} else {
			fmt.Fprintf(w, "%d. Refresh declared local auth from the rotated encrypted secret before any git push, flake-lock update, or rebuild fetch:\n   rotate-token refresh-local-auth --group %s\n\n", step, alias)
		}
		step++
	}

	switch landing {
	case deployStepsLandedPushed:
		fmt.Fprintf(w, "%d. %s is committed (%q) and pushed to origin; merge it into its default branch when you are ready.\n", step, branch, commitSubject)
	case deployStepsLandedNotPushed:
		fmt.Fprintf(w, "%d. %s is committed (%q) but the push failed; push it yourself (the error follows below), then merge it into its default branch.\n", step, branch, commitSubject)
	default: // deployStepsDryRun
		fmt.Fprintf(w, "%d. A live run commits", step)
		for _, credential := range group.Credentials {
			fmt.Fprintf(w, " %s", credential.SecretPath)
		}
		fmt.Fprintf(w, " in %s as %q and pushes %s to origin; merging stays your act.\n", checkout, commitSubject, branch)
	}
	step++

	fmt.Fprintf(w, "\n%d. Update the deploy flake lock:\n   cd ~/work/allod/deploy\n   nix flake update secrets\n   git add flake.lock\n   git commit -m \"update secrets input\"\n   git push\n\n", step)
	step++

	fmt.Fprintf(w, "%d. Rebuild affected target(s):\n", step)
	for _, target := range uniqueRebuildTargets(group) {
		printRebuildCommand(w, target.system, target.kind)
	}
}

// printVerification ports rotate-token's print_group_verification: one
// verification command per target, in the order the registry lists them.
func printVerification(w io.Writer, group tokenGroup) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "--- Verification ---")
	for _, credential := range group.Credentials {
		for _, target := range credential.Targets {
			switch target.Verify.Type {
			case "forge-token-verify":
				user := dashIfEmpty(target.User)
				fmt.Fprintf(w, "%s (user %s):\n", target.System, user)
				if target.Kind == "nixos-host" {
					fmt.Fprintf(w, "   sudo -u %s forge token verify < %s\n", user, target.DeployedPath)
				} else {
					fmt.Fprintf(w, "   ssh %s 'forge token verify < %s'\n", target.System, target.DeployedPath)
				}
			case "git-ls-remote":
				fmt.Fprintf(w, "%s (%s):\n", target.System, dashIfEmpty(target.Verify.CredentialContext))
				if target.Kind == "nixos-host" {
					fmt.Fprintf(w, "   sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote %s HEAD\n", target.Verify.RepoURL)
				} else {
					fmt.Fprintf(w, "   ssh %s 'sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote %s HEAD'\n", target.System, target.Verify.RepoURL)
				}
			case "site-check":
				// rclone itself rides on the 'allod' wrapper's PATH, not the
				// operator's, so a raw rclone command here would be "command
				// not found"; 'allod site check' is the command the
				// operator has.
				fmt.Fprintf(w, "%s (%s):\n", target.System, target.Kind)
				if target.Kind == "nixos-host" {
					fmt.Fprintln(w, "   allod site check")
				} else {
					fmt.Fprintf(w, "   ssh %s 'allod site check'\n", target.System)
				}
			default:
				die(1, "unsupported verification type '%s' for %s", target.Verify.Type, target.System)
			}
		}
	}
}

// printRevocationGate ports rotate-token's print_group_revocation and
// print_group_in_place_no_revocation: Forgejo account/UI-token wording only
// for a forgejo group, generic wording for a none group, and no revocation
// step at all when the provider rotates in place (the old value is already
// invalid once the replacement exists).
func printRevocationGate(w io.Writer, group tokenGroup) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "--- Revocation gate ---")
	if group.RotationStrategy == "in-place" {
		fmt.Fprintln(w, "No old-token revocation step is printed for this group. Provider rotation is")
		fmt.Fprintln(w, "in-place, so the old token is invalidated when the replacement is generated.")
		return
	}
	if group.Service != "forgejo" {
		fmt.Fprintln(w, "After every rebuild and verification command above succeeds, revoke the old")
		fmt.Fprintln(w, "value at the service that issued it.")
		return
	}
	fmt.Fprintln(w, "After every rebuild and verification command above succeeds, revoke the old")
	fmt.Fprintf(w, "Forgejo UI token '%s' while logged in as '%s'.\n", group.UITokenName, group.Account)
}
