//go:build secret

package main

// 'allod secret rotate' replaces a credential's value on the host: the same
// landing 'create' and 'rekey' do, applied to the third case — a value that
// already has a ciphertext and whose recipients are not changing. It ports
// the group-rotation half of nexus's rotate-token (about 1450 lines of
// bash): group resolution, dry run, and the printed deploy, verify, and
// revocation steps. Out of the port: rotate-token's
// --group/--forgejo-token/--allow-single-secret selectors (a credential name
// is the only selector here, and it always names its whole registry group,
// the way '--group' rotates a shared token today), and refresh-local-auth,
// now its own host script of that name (it installs root-owned files under
// sudo, a privilege this command does not hold; see printDeploySteps).
//
// What this does not port is rotate-token's per-format switch. A
// credential's non-secret text is declared in the registry as a template
// around one '{secret}' placeholder, so rotation renders that template
// around the new secret and never decrypts the old one: the parsers that
// lifted a user and a host out of the old ciphertext are gone, and with
// them the only reason this command ever had to read an existing value.
// 'allod secret migrate' does that lift exactly once per legacy container,
// and a legacy entry is refused here by name.
//
// The unit of rotation is the registry group: every credential the group
// lists is re-encrypted from the one value read on stdin, exactly as
// rotate-token's '--group' rotates a shared Forgejo token from one prompt.
// Everything stays in memory until every ciphertext is staged, then one
// nix flake check gates one commit and push, exactly like 'create' and
// 'rekey' land. No rotation_state change: every entry stays "active".
//
// A dry run reads and decrypts nothing: it runs every gate this command
// applies to the group (branch, clean tree, active state, ciphertext
// present, recipients, registry shape, declared value, declared
// verification, and — for a group with a local_auth_refresh entry —
// 'refresh-local-auth' resolving on PATH) but not the repository's own nix
// flake check, which only a real landing runs.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const secretRotateDetail = `'rotate' replaces a value: the rotation registry group that lists <name>
among its credentials is the whole unit, so every credential in that group
is re-encrypted from the one value read on stdin, the way 'rotate-token
--group' rotates a shared Forgejo token today. Every credential in the
group must already be active with a ciphertext on disk.

Each credential's plaintext is its declared value.template with the new
secret substituted at the sole '{secret}' placeholder, or the secret
verbatim when the credential declares no value; 'value.encode' names an
encoding applied to the secret alone before substitution. Nothing else in
the template changes, and no existing ciphertext is decrypted. The value is
read from stdin verbatim, so a trailing newline is part of the secret — use
printf '%s' "$value" | allod secret rotate <name> when it should not be. A
credential still carrying a legacy 'format' is refused, naming 'allod
secret migrate <name>'; so is a group that has one among its other members.

'--dry-run' runs every gate this command applies to the group — branch,
clean tree, active state, ciphertext present, recipients, registry shape,
unique group membership, declared value and verification — but not the
repository's own checks, which run only on a real landing; it prints the
group, its targets, the deploy and verification steps, and the revocation
gate, describing what a live run would do rather than instructing it, and
reads no value, decrypts nothing, and writes nothing. rotation_state does
not change. A group with a local_auth_refresh entry gets one more printed
step naming 'refresh-local-auth --group <alias>' — an instruction on a live
run, phrased as what a live run would print on a dry run — because that
stays a separate host script: it installs root-owned files under sudo, a
privilege this command does not hold. Such a group also requires
'refresh-local-auth' to resolve on PATH, checked before the value is read
and on a dry run too, so a host whose nexus pin predates allod/nexus#52 is
refused before a live run lands a rotation the operator could not finish.
The branch is re-verified immediately before writing and again
immediately before committing, refusing (with every ciphertext already
written restored) if the checkout moved in between.

'rotate' works on the secrets checkout, found through the repository
registry under ~/work unless <checkout> names a worktree, and on whatever
branch is checked out there. It refuses the default branch and a dirty
tree: the landing is a commit on a branch, and merging stays your act.

The identity is $AGE_IDENTITY, or ~/.ssh/host; its .pub must be among the
recipients. The plaintext is never written to disk, printed, or passed as
an argument.
`

// secretHostName is the seam printVerification compares a target's system
// against: a target on this machine prints its command bare, every other
// one prints it behind 'ssh'.
var secretHostName = os.Hostname

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
// verifyGroupMembersUnique) without a second evaluation. A credential has
// exactly one entry in exactly one group in a well-formed registry; this
// counts entries rather than groups, so two entries of the same name inside
// one group are refused as well as one entry in each of two groups. Either
// way there is no single answer to which entry the operator meant, and
// lookupSecret already refuses a credential with more than one agenix
// consumer for the same reason.
func selectRotationGroup(checkout, name string) (string, tokenGroup, map[string]tokenGroup) {
	groups, err := secretEvalRegistry(checkout)
	if err != nil {
		die(1, "could not evaluate lib.forgejoTokenGroups in %s: %s", checkout, err)
	}
	_, aliases := registryCredentialEntries(groups, name)
	distinct := distinctSorted(aliases)
	switch {
	case len(aliases) == 0:
		die(1, "no rotation registry entry for '%s' in %s/forgejo-token-groups.json; rotate operates on the registry group a credential belongs to", name, checkout)
	case len(aliases) == 1:
		return aliases[0], groups[aliases[0]], groups
	case len(distinct) > 1:
		die(1, "credential '%s' is registered in more than one rotation registry group (%s) in %s/forgejo-token-groups.json; fix the registry so each credential belongs to one group", name, strings.Join(distinct, ", "), checkout)
	default:
		die(1, "credential '%s' is listed %d times in rotation registry group '%s' in %s/forgejo-token-groups.json; rotate acts on one entry per credential, so fix the registry so each credential is listed once", name, len(aliases), distinct[0], checkout)
	}
	return "", tokenGroup{}, nil
}

// verifyGroupMembersUnique checks not just the requested credential but
// every member of the selected group: a group is not a safe rotation unit
// if one of its other credentials also sits in a second registry group,
// since a run here would land that credential's new value without anyone
// having asked whether the second group's rotation also needed it. A member
// listed twice inside this one group is refused too — two entries for one
// credential means two declared values for one ciphertext, and rendering
// the new secret through whichever came first is exactly the guess this
// command does not make.
func verifyGroupMembersUnique(group tokenGroup, groups map[string]tokenGroup) {
	for _, credential := range group.Credentials {
		_, aliases := registryCredentialEntries(groups, credential.Credential)
		distinct := distinctSorted(aliases)
		switch {
		case len(aliases) < 2:
		case len(distinct) > 1:
			die(1, "credential '%s' is registered in more than one rotation registry group (%s); fix the registry so each credential belongs to one group", credential.Credential, strings.Join(distinct, ", "))
		default:
			die(1, "credential '%s' is listed %d times in rotation registry group '%s'; rotate acts on one entry per credential, so fix the registry so each credential is listed once", credential.Credential, len(aliases), distinct[0])
		}
	}
}

var (
	validRegistryService    = map[string]bool{"forgejo": true, "none": true}
	validRotationStrategy   = map[string]bool{"overlap": true, "in-place": true}
	validRegistryTargetKind = map[string]bool{"nixos-host": true, "dev-vm": true, "privacy-vm": true, "service-vm": true}
)

// credentialStoreURLTemplate is the one template shape the local auth
// refresh contract can consume: a single netrc-consumable
// 'https://<user>:{secret}@<host>' line. It mirrors the archetypes check's
// own predicate character for character rather than applying a separate URL
// grammar, so a source credential this command accepts is one that check
// accepts too.
var credentialStoreURLTemplate = regexp.MustCompile(`^https://[^:[:space:]][^:[:space:]]*:\{secret\}@[^/[:space:]][^/[:space:]]*$`)

// isCredentialStoreURLSource reports whether a credential can be a
// local_auth_refresh source: legacy credential-store-url, or a new-shape
// template that renders exactly one credential-store line. Blank lines are
// dropped first, matching the runtime netrc parser.
func isCredentialStoreURLSource(credential registryCredential) bool {
	if credential.Format == "credential-store-url" {
		return true
	}
	if credential.Value == nil || credential.Value.Encode != "" {
		return false
	}
	template := credential.Value.Template
	if strings.Count(template, credentialSecretPlaceholder) != 1 {
		return false
	}
	var nonEmpty []string
	for _, line := range strings.Split(template, "\n") {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, line)
		}
	}
	return len(nonEmpty) == 1 && credentialStoreURLTemplate.MatchString(nonEmpty[0])
}

// validateGroupMetadata checks the registry shape rotate-token's
// validate_group_metadata checks, minus the two vocabularies this contract
// retired: a credential's format and a target's verify type. A group
// missing a field this command needs is refused with what is wrong rather
// than a blank line in the printed steps.
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
			if credential.Credential != refresh.SourceCredential || !isCredentialStoreURLSource(credential) {
				continue
			}
			for _, target := range credential.Targets {
				if target.System == refresh.System && target.DeployedPath == "/root/.git-credentials" {
					matches++
				}
			}
		}
		if matches != 1 {
			fail(fmt.Sprintf("local_auth_refresh source_credential '%s' does not name exactly one credential-store URL target at %s:/root/.git-credentials", refresh.SourceCredential, refresh.System))
		}
	}
}

// assertUniformGroupEncoding refuses a group whose credentials disagree on
// 'value.encode'. One prompted value feeds the whole group, and an encoding
// changes what that one value means — a password to obscure rather than a
// token to store — so a mixed group would silently obscure a token or store
// a password unobscured. The secrets flake's own credential-registry check
// refuses the same shape; this is that rule applied before any write.
func assertUniformGroupEncoding(alias string, credentials []registryCredential) {
	var encodings []string
	for _, credential := range credentials {
		encoding := credentialEncoding(credential)
		if !stringInList(encoding, encodings) {
			encodings = append(encodings, encoding)
		}
	}
	if len(encodings) > 1 {
		named := make([]string, 0, len(encodings))
		for _, encoding := range encodings {
			if encoding == "" {
				named = append(named, "none")
				continue
			}
			named = append(named, encoding)
		}
		sort.Strings(named)
		die(1, "rotation registry group '%s' mixes value encodings (%s); one prompted value cannot rotate credentials that encode it differently", alias, strings.Join(named, ", "))
	}
}

func groupCommitSubject(alias string, group tokenGroup) string {
	if group.Service == "forgejo" {
		return fmt.Sprintf("rotate %s Forgejo token", alias)
	}
	return fmt.Sprintf("rotate %s", alias)
}

// --- The command ---

// rotationItem is everything rotate has verified about one credential in
// the selected group before it prompts for the new value. Nothing here is
// decrypted: the non-secret half is declared, not recovered.
type rotationItem struct {
	credential string
	value      *credentialValue
	target     secretTarget
	fullPath   string
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
	for _, credential := range group.Credentials {
		if err := credentialShapeError(credential); err != nil {
			die(1, "%s", err)
		}
		if !isLegacyCredential(credential) {
			continue
		}
		if credential.Credential == name {
			die(1, "credential '%s' uses legacy format '%s'; %s", name, credential.Format, legacyRefusalReason(credential, "rotating it"))
		}
		die(1, "rotation registry group '%s' also lists legacy credential '%s' (format '%s'); %s", alias, credential.Credential, credential.Format, legacyRefusalReason(credential, "rotating this group"))
	}
	validateGroupMetadata(alias, group)
	assertUniformGroupEncoding(alias, group.Credentials)
	verifyGroupMembersUnique(group, groups)
	// Checked before the value is ever read, and on a dry run too: the
	// printed deploy step below tells the operator to run
	// 'refresh-local-auth' next, and a live run has already landed the
	// rotated secret by the time that step would fail, leaving the group
	// rotated with no way to finish the job.
	if len(group.LocalAuthRefresh) > 0 {
		if _, err := exec.LookPath("refresh-local-auth"); err != nil {
			die(1, "rotation registry group '%s' needs a local auth refresh after rotation, and 'refresh-local-auth' was not found on PATH; this host's nexus pin predates allod/nexus#52", alias)
		}
	}

	encodings, err := secretEvalEncodings(checkout)
	if err != nil {
		die(1, "could not evaluate lib.credentialEncodings in %s: %s", checkout, err)
	}

	items := make([]rotationItem, len(group.Credentials))
	for i, credential := range group.Credentials {
		if err := validateCredentialValue(credential.Value, encodings); err != nil {
			die(1, "credential '%s' has an unusable declared value: %s", credential.Credential, err)
		}
		for _, target := range credential.Targets {
			if _, err := verifyCommand(target.Verify); err != nil {
				die(1, "credential '%s' target '%s' %s; run 'allod secret migrate %s' to convert it", credential.Credential, target.System, err, credential.Credential)
			}
		}
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
			value:      credential.Value,
			target:     target,
			fullPath:   full,
			original:   original,
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

	// Checked before the value is ever read, so a missing encoder costs the
	// operator a refusal rather than a wasted paste (see requireRcloneOnPath).
	for _, credential := range group.Credentials {
		if credentialEncoding(credential) == "rclone-obscure" {
			requireRcloneOnPath()
			break
		}
	}

	secret := readSecretValue(alias)
	for i := range items {
		item := &items[i]
		plaintext, err := renderCredentialValue(item.value, secret, encodings)
		if err != nil {
			die(1, "could not render the new value for %s: %s", item.target.path, err)
		}
		item.ciphertext = encryptOrDie(item.target, plaintext)
	}
	// The candidate is dropped as soon as every ciphertext exists: it was
	// never on disk, in argv, or on either stream, and it is not kept alive
	// past the last use either.
	secret = nil

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
			if err := atomicWrite(item.fullPath, item.original); err != nil {
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
		if err := atomicWrite(item.fullPath, item.ciphertext); err != nil {
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

// credentialValueShape is what printGroupSummary says about a credential's
// value where rotate-token's summary printed its format: 'plain' for a
// credential that stores the secret verbatim, 'template' for one with
// declared surrounding text, and the encoding's name alongside when one
// applies.
func credentialValueShape(credential registryCredential) string {
	if credential.Value == nil {
		return "plain"
	}
	if credential.Value.Encode == "" {
		return "template"
	}
	return "template, encode " + credential.Value.Encode
}

// printGroupSummary ports rotate-token's print_group_summary: the group,
// its service, strategy, every affected secret and its value shape, and
// every target with the command that verifies it. The local-auth-refresh
// timing prose bash prints in this function is not ported: the printed
// deploy step is the whole of what 'rotate' says about refresh-local-auth
// (see printDeploySteps).
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
		fmt.Fprintf(w, "  - %s (%s, %s)\n", credential.SecretPath, credential.Credential, credentialValueShape(credential))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Affected targets:")
	for _, credential := range group.Credentials {
		for _, target := range credential.Targets {
			command, err := verifyCommand(target.Verify)
			if err != nil {
				die(1, "credential '%s' target '%s' %s; run 'allod secret migrate %s' to convert it", credential.Credential, target.System, err, credential.Credential)
			}
			fmt.Fprintf(w, "  - %s (%s): %s, verify: %s\n", target.System, target.Kind, target.DeployedPath, command)
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
// local_auth_refresh entries, step 1 names 'refresh-local-auth --group
// <alias>' as the operator's next step; 'rotate' never runs it (see
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
			fmt.Fprintf(w, "%d. After the real run lands the rotated secret, refresh declared local auth before any git push, flake-lock update, or rebuild fetch:\n   refresh-local-auth --group %s\n\n", step, alias)
		} else {
			fmt.Fprintf(w, "%d. Refresh declared local auth from the rotated encrypted secret before any git push, flake-lock update, or rebuild fetch:\n   refresh-local-auth --group %s\n\n", step, alias)
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

// posixShellQuote wraps a value in single quotes for a POSIX shell, ending
// and restarting the quoted run around every embedded quote. It is
// deliberately implemented here rather than shelling out: the verification
// text is printed for a human to run, and it must stay one command whatever
// the registry holds.
func posixShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// printVerification ports rotate-token's print_group_verification: one
// verification command per target, in the order the registry lists them.
// The command itself is the target's declared 'verify' string, printed
// verbatim on the machine it names and behind 'ssh' everywhere else —
// rotate-token's four probe types are now four such strings in the
// registry, and adding a fifth needs no code here.
func printVerification(w io.Writer, group tokenGroup) {
	host, err := secretHostName()
	if err != nil {
		die(1, "could not read this machine's host name to tell a local target from a remote one: %s", err)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "--- Verification ---")
	for _, credential := range group.Credentials {
		for _, target := range credential.Targets {
			command, err := verifyCommand(target.Verify)
			if err != nil {
				die(1, "credential '%s' target '%s' %s; run 'allod secret migrate %s' to convert it", credential.Credential, target.System, err, credential.Credential)
			}
			fmt.Fprintf(w, "%s (%s):\n", target.System, target.Kind)
			if target.System == host {
				fmt.Fprintf(w, "   %s\n", command)
				continue
			}
			fmt.Fprintf(w, "   ssh %s %s\n", posixShellQuote(target.System), posixShellQuote(command))
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
