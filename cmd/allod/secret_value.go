//go:build secret

package main

// The declared shape of a credential's value and of a target's
// verification, shared by 'create', 'rotate', and 'migrate'.
//
// A credential's non-secret text is a template around one '{secret}'
// placeholder; a credential with no 'value' stores the secret verbatim.
// The only vocabulary left is the encoder list, and that list lives in the
// secrets checkout (lib.credentialEncodings), not here: this file holds an
// implementation per exported name, and refuses an exported name it has no
// implementation for rather than silently storing an unencoded secret.
//
// The secret is used verbatim. Nothing here trims a trailing newline,
// rejects whitespace, or otherwise inspects the candidate value: the
// template's bytes and the secret's bytes both reach the ciphertext exactly
// as given, and a consumer handed a value it cannot use fails loudly at its
// own boundary rather than having this command guess which byte was meant.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// credentialSecretPlaceholder is the one literal a template substitutes at.
const credentialSecretPlaceholder = "{secret}"

// validateCredentialValue holds a declared value to the contract the
// secrets flake's credential-registry check enforces, so this command
// refuses the same registry the check would rather than rendering
// something the check has not seen.
func validateCredentialValue(value *credentialValue, encodings []string) error {
	if value == nil {
		return nil
	}
	if strings.Count(value.Template, credentialSecretPlaceholder) != 1 {
		return fmt.Errorf("value.template must contain exactly one %s placeholder", credentialSecretPlaceholder)
	}
	if value.Encode != "" && !stringInList(value.Encode, encodings) {
		return fmt.Errorf("value.encode %q is not exported by lib.credentialEncodings", value.Encode)
	}
	return nil
}

// credentialEncoding is the group-wide encoding one credential asks for, or
// the empty string for none. One prompted value feeds a whole rotation
// group, so the group must agree on it.
func credentialEncoding(credential registryCredential) string {
	if credential.Value == nil {
		return ""
	}
	return credential.Value.Encode
}

func nixEvalCredentialEncodings(checkout string) ([]string, error) {
	data, err := nixEvalJSON(checkout, "lib.credentialEncodings")
	if err != nil {
		return nil, err
	}
	var encodings []string
	if err := json.Unmarshal(data, &encodings); err != nil {
		return nil, fmt.Errorf("lib.credentialEncodings is not a list of strings: %w", err)
	}
	return encodings, nil
}

// renderCredentialValue produces the plaintext one credential stores: the
// secret verbatim when no value is declared, otherwise the template with
// the (optionally encoded) secret substituted at its sole placeholder.
// Every other byte of the template survives, including whether it ends in a
// newline.
func renderCredentialValue(value *credentialValue, secret []byte, encodings []string) ([]byte, error) {
	if err := validateCredentialValue(value, encodings); err != nil {
		return nil, err
	}
	if value == nil {
		return append([]byte(nil), secret...), nil
	}
	rendered := secret
	if value.Encode != "" {
		encoded, err := encodeCredentialSecret(value.Encode, secret)
		if err != nil {
			return nil, err
		}
		rendered = encoded
	}
	return []byte(strings.Replace(value.Template, credentialSecretPlaceholder, string(rendered), 1)), nil
}

// encodeCredentialSecret runs the one encoder this build implements. An
// encoder the checkout exports but this build does not implement is a
// refusal, never a silent pass-through: storing a raw password where an
// obscured one belongs would be a credential this deployment cannot use and
// a plaintext password in a file that was supposed to hold none.
func encodeCredentialSecret(name string, secret []byte) ([]byte, error) {
	switch name {
	case "rclone-obscure":
		return rcloneObscure(secret)
	default:
		return nil, fmt.Errorf("encoder %q is exported by lib.credentialEncodings but this allod build has no implementation for it", name)
	}
}

// rcloneObscuredForm is the one shape 'rclone obscure -' prints. Anything
// else — empty, multi-line, or carrying a control character — could inject
// extra configuration lines into the rendered template, so it is refused
// rather than substituted.
var rcloneObscuredForm = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// rcloneObscure writes the candidate to stdin. In particular, it never puts
// a secret in argv, where other local users could read it through procfs
// for as long as the process lives. The obscured form is reversible by
// anyone with rclone, so it is exactly as sensitive as the password itself
// and is never printed or logged either.
//
// The encoder's own output is dropped rather than reported. A process
// handed a secret on stdin can put that secret into anything it writes — a
// debug build, a usage message that echoes its input, an 'rclone' put on
// PATH by someone else — and this error reaches the operator through die(),
// which prints it. The exit status is what a diagnosis needs from a process
// that was handed a secret; reading a program's own message back is a thing
// to do with a value that is not one.
func rcloneObscure(secret []byte) ([]byte, error) {
	var out bytes.Buffer
	cmd := exec.Command("rclone", "obscure", "-")
	cmd.Stdin = bytes.NewReader(secret)
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("'rclone obscure -' failed (%s); its own output is withheld, since an encoder handed a secret can echo it", encoderExitStatus(err))
	}
	obscured := bytes.TrimRight(out.Bytes(), "\r\n")
	if !rcloneObscuredForm.Match(obscured) {
		return nil, fmt.Errorf("'rclone obscure -' printed something other than one base64url token")
	}
	return obscured, nil
}

// requireRcloneOnPath is checked before the value is ever read from stdin,
// not after: asking the operator to paste a password only to refuse it for
// a missing binary wastes the paste and, worse, leaves it sitting in shell
// history or a terminal scrollback for no reason.
func requireRcloneOnPath() {
	if _, err := exec.LookPath("rclone"); err != nil {
		die(1, "rclone not found on PATH; the rclone-obscure encoding needs it to obscure the new password")
	}
}

// --- Verification ---

// verifyCommand reads a target's declared verification: one non-empty
// command line, printed verbatim. A legacy structured object decodes as
// something other than a string and is named as such, so the refusal points
// at 'migrate' rather than at a JSON type.
func verifyCommand(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("has no verify command")
	}
	var command string
	if err := json.Unmarshal(raw, &command); err != nil {
		return "", fmt.Errorf("has structured legacy verify data, not one command string")
	}
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("has an empty verify command")
	}
	if strings.ContainsAny(command, "\r\n") {
		return "", fmt.Errorf("has a verify command spanning more than one line")
	}
	return command, nil
}

// --- The registry ---

// registryCredentialFor finds the one registry entry for a credential and
// the alias of the group holding it. It counts entries, not groups: two
// entries with the same name inside one group are two entries a command
// would have to choose between, and 'migrate' rewriting only the first
// would leave the second behind in the legacy shape, committed and
// unnoticed. Zero, two in one group, and one in each of two groups are all
// refused, because rotate and migrate each act on exactly one entry.
func registryCredentialFor(groups map[string]tokenGroup, name string) (registryCredential, string, error) {
	found, aliases := registryCredentialEntries(groups, name)
	distinct := distinctSorted(aliases)
	switch {
	case len(aliases) == 1:
		return found, aliases[0], nil
	case len(aliases) == 0:
		return registryCredential{}, "", fmt.Errorf("no rotation registry entry for '%s' in forgejo-token-groups.json", name)
	case len(distinct) > 1:
		return registryCredential{}, "", fmt.Errorf("credential '%s' is registered in more than one rotation registry group (%s); fix the registry so each credential belongs to one group", name, strings.Join(distinct, ", "))
	default:
		return registryCredential{}, "", fmt.Errorf("credential '%s' is listed %d times in rotation registry group '%s'; a command acts on one entry, so a second entry of the same name would be left behind. Fix the registry so each credential is listed once", name, len(aliases), distinct[0])
	}
}

// registryCredentialEntries returns the last entry named name and the alias
// of the group holding each entry — one alias per entry, so two entries in
// one group appear twice and the caller can count them.
func registryCredentialEntries(groups map[string]tokenGroup, name string) (registryCredential, []string) {
	var found registryCredential
	var aliases []string
	for alias, group := range groups {
		for _, credential := range group.Credentials {
			if credential.Credential == name {
				aliases = append(aliases, alias)
				found = credential
			}
		}
	}
	sort.Strings(aliases)
	return found, aliases
}

// distinctSorted is the sorted set of values, used to tell "two entries in
// one group" from "one entry in each of two groups" when both are refused.
func distinctSorted(values []string) []string {
	var distinct []string
	for _, value := range values {
		if !stringInList(value, distinct) {
			distinct = append(distinct, value)
		}
	}
	sort.Strings(distinct)
	return distinct
}

// isLegacyCredential reports the shape a credential still carries. The
// secrets flake's own check treats 'format' as the discriminator and
// refuses a credential carrying both, so this command reads the same one
// field rather than inventing a second rule.
func isLegacyCredential(credential registryCredential) bool { return credential.Format != "" }

// legacyCredentialContainers are the two legacy formats whose plaintext is
// a container with non-secret text around the secret. Those are the two
// 'migrate' can take apart; every other legacy format stores the secret on
// its own, so there is nothing to recover and nothing to decrypt.
var legacyCredentialContainers = []string{"credential-store-url", "rclone-remote-stanza"}

// legacyRefusalReason says how one legacy credential reaches the new shape,
// which depends on which legacy format it carries. Sending an operator to
// 'migrate' for a plain entry would be a wasted hop: migrate refuses it and
// names a registry edit, so the command that refused says so directly.
// action names what the caller was about to do, e.g. "rotating it".
func legacyRefusalReason(credential registryCredential, action string) string {
	if stringInList(credential.Format, legacyCredentialContainers) {
		return fmt.Sprintf("run 'allod secret migrate %s' before %s", credential.Credential, action)
	}
	return "it is a plain legacy value that 'rotate-token' still rotates, and migrate has no container to take apart: it becomes new-shape by a registry edit that drops its 'format' and turns each target's 'verify' object into its command string, which needs no decryption"
}

// credentialShapeError refuses the one shape the coexistence rule forbids:
// a credential is wholly legacy or wholly new, never both. The secrets
// flake's credential-registry check fails closed on the mix too, so a
// registry that reaches this command carrying both has not been checked,
// and guessing which half is authoritative is exactly the guess that would
// encrypt a value into the wrong text.
func credentialShapeError(credential registryCredential) error {
	if credential.Format == "" || credential.Value == nil {
		return nil
	}
	return fmt.Errorf("credential '%s' carries both a legacy 'format' and a declared 'value'; a credential is one shape or the other, so fix the registry entry before landing anything for it", credential.Credential)
}

// encoderExitStatus describes how an encoder process ended without quoting
// anything the process itself produced.
func encoderExitStatus(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Sprintf("exit status %d", exitErr.ExitCode())
	}
	return "it could not be run"
}
