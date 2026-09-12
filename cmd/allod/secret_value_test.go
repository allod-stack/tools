//go:build secret

package main

// Tests for the declared value contract itself: the renderer 'create' and
// 'rotate' share, the encoder's stdin discipline, and the one-line verify
// command. The rendering table here is the same one the command-level tests
// in secret_test.go and secret_rotate_test.go drive end to end, so a change
// that made one path render differently from the other would fail there
// rather than being invisible.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRcloneTemplate is the stanza shape site_config.go's remoteStanza
// writes, with the password declared rather than stored.
const fixtureRcloneTemplate = "[shared]\ntype = ftp\nhost = ftp.example.test\nuser = fixture-user\npass = {secret}\nexplicit_tls = true\n"

// credentialRenderCase is one declared value, one candidate secret, and the
// exact plaintext both 'create' and 'rotate' must hand to age for it.
type credentialRenderCase struct {
	name        string
	value       *credentialValue
	secret      string
	want        string
	needsRclone bool
}

// credentialRenderCases covers the three value shapes the contract has —
// absent, a one-line template, and a multi-line template with an encoding —
// each with and without a trailing newline on the candidate value, because
// the value is used verbatim and that newline is part of it.
func credentialRenderCases() []credentialRenderCase {
	return []credentialRenderCase{
		{
			name:   "plain value",
			secret: "tok-fixture",
			want:   "tok-fixture",
		},
		{
			name:   "plain value with a trailing newline",
			secret: "tok-fixture\n",
			want:   "tok-fixture\n",
		},
		{
			name:   "url template",
			value:  &credentialValue{Template: "https://fixture-user:{secret}@example.test"},
			secret: "tok-fixture",
			want:   "https://fixture-user:tok-fixture@example.test",
		},
		{
			// The secret is used verbatim, so a trailing newline lands
			// inside the rendered URL rather than being trimmed away. The
			// help text says to strip it with printf when that is not what
			// was meant; nothing here guesses.
			name:   "url template with a trailing newline on the value",
			value:  &credentialValue{Template: "https://fixture-user:{secret}@example.test"},
			secret: "tok-fixture\n",
			want:   "https://fixture-user:tok-fixture\n@example.test",
		},
		{
			name:        "encoded multiline template",
			value:       &credentialValue{Template: fixtureRcloneTemplate, Encode: "rclone-obscure"},
			secret:      "pw-fixture",
			want:        "[shared]\ntype = ftp\nhost = ftp.example.test\nuser = fixture-user\npass = OBS-pw-fixture\nexplicit_tls = true\n",
			needsRclone: true,
		},
		{
			// The encoder, not the template, is what sees the trailing
			// newline here: its output is one token either way, and that
			// token is what reaches the template.
			name:        "encoded multiline template with a trailing newline on the value",
			value:       &credentialValue{Template: fixtureRcloneTemplate, Encode: "rclone-obscure"},
			secret:      "pw-fixture\n",
			want:        "[shared]\ntype = ftp\nhost = ftp.example.test\nuser = fixture-user\npass = OBS-pw-fixture\nexplicit_tls = true\n",
			needsRclone: true,
		},
	}
}

func TestRenderCredentialValue(t *testing.T) {
	for _, tc := range credentialRenderCases() {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsRclone {
				installFakeRclone(t)
			}
			got, err := renderCredentialValue(tc.value, []byte(tc.secret), []string{"rclone-obscure"})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("rendered %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateCredentialValueRefusals(t *testing.T) {
	cases := []struct {
		name  string
		value *credentialValue
		want  string
	}{
		{"no placeholder", &credentialValue{Template: "https://fixture-user:token@example.test"}, "exactly one {secret} placeholder"},
		{"two placeholders", &credentialValue{Template: "{secret}{secret}"}, "exactly one {secret} placeholder"},
		{"unknown encoder", &credentialValue{Template: "{secret}", Encode: "rot13"}, `value.encode "rot13" is not exported`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCredentialValue(tc.value, []string{"rclone-obscure"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	if err := validateCredentialValue(nil, nil); err != nil {
		t.Errorf("an absent value was refused: %v", err)
	}
}

// TestRenderCredentialValueRefusesAnUnimplementedExportedEncoder pins the
// fail-closed half of the encoder contract: the checkout owns the list, but
// a name this build cannot perform is a refusal, never a silent
// pass-through that would store a raw password where an encoded one belongs.
func TestRenderCredentialValueRefusesAnUnimplementedExportedEncoder(t *testing.T) {
	_, err := renderCredentialValue(&credentialValue{Template: "{secret}", Encode: "future-encoder"}, []byte("x"), []string{"future-encoder"})
	if err == nil || !strings.Contains(err.Error(), "no implementation for it") {
		t.Errorf("err = %v, want it to refuse an encoder this build cannot perform", err)
	}
}

// TestRcloneObscureSendsTheSecretOnStdinNotArgv pins the claim
// security-practices.md makes about every candidate secret: it crosses on
// stdin, and never on a command line other local users can read out of
// /proc. The fake records both, byte for byte, including the trailing
// newline the caller did not strip.
func TestRcloneObscureSendsTheSecretOnStdinNotArgv(t *testing.T) {
	dir := t.TempDir()
	stdinPath := filepath.Join(dir, "stdin")
	argvPath := filepath.Join(dir, "argv")
	script := "#!/bin/sh\ncat > \"$RCLONE_FIXTURE_STDIN\"\nprintf '%s\\n' \"$@\" > \"$RCLONE_FIXTURE_ARGV\"\nprintf 'OBSCURED'\n"
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "rclone"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RCLONE_FIXTURE_STDIN", stdinPath)
	t.Setenv("RCLONE_FIXTURE_ARGV", argvPath)

	got, err := rcloneObscure([]byte("pw-fixture\n"))
	if err != nil {
		t.Fatalf("rcloneObscure: %v", err)
	}
	if string(got) != "OBSCURED" {
		t.Errorf("obscured = %q", got)
	}
	sawStdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(sawStdin) != "pw-fixture\n" {
		t.Errorf("rclone read %q on stdin, want the candidate bytes verbatim", sawStdin)
	}
	sawArgv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "obscure\n-\n"; string(sawArgv) != want {
		t.Errorf("rclone argv = %q, want %q with no secret in it", sawArgv, want)
	}
}

// TestRcloneObscureRefusesOutputThatIsNotOneToken covers the injection this
// guard exists for: anything but one base64url token could add configuration
// lines to the rendered stanza.
func TestRcloneObscureRefusesOutputThatIsNotOneToken(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\ncat > /dev/null\nprintf 'OBS\\nexplicit_tls = false\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "rclone"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := rcloneObscure([]byte("pw-fixture")); err == nil || !strings.Contains(err.Error(), "one base64url token") {
		t.Errorf("err = %v, want a refusal naming the expected shape", err)
	}
}

func TestVerifyCommand(t *testing.T) {
	cases := []struct {
		name, raw, want, wantErr string
	}{
		{name: "one line", raw: `"allod site check"`, want: "allod site check"},
		{name: "missing", raw: "", wantErr: "has no verify command"},
		{name: "json null", raw: "null", wantErr: "has an empty verify command"},
		{name: "legacy object", raw: `{"type":"site-check"}`, wantErr: "structured legacy verify data"},
		{name: "empty string", raw: `""`, wantErr: "has an empty verify command"},
		{name: "whitespace only", raw: `"   "`, wantErr: "has an empty verify command"},
		{name: "two lines", raw: `"allod site check\nrm -rf /"`, wantErr: "more than one line"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := verifyCommand(json.RawMessage(tc.raw))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPOSIXShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"fixture-host", "'fixture-host'"},
		{"host'one", `'host'\''one'`},
		{"allod site check --note 'it'", `'allod site check --note '\''it'\'''`},
	}
	for _, tc := range cases {
		if got := posixShellQuote(tc.in); got != tc.want {
			t.Errorf("posixShellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsCredentialStoreURLSource(t *testing.T) {
	accepted := []registryCredential{
		{Format: "credential-store-url"},
		{Value: &credentialValue{Template: "https://fixture-user:{secret}@example.test"}},
		{Value: &credentialValue{Template: "\nhttps://fixture-user:{secret}@example.test\n"}},
	}
	for index, credential := range accepted {
		if !isCredentialStoreURLSource(credential) {
			t.Errorf("case %d: a credential-store source was refused: %+v", index, credential)
		}
	}
	refused := []registryCredential{
		{},
		{Value: &credentialValue{Template: "https://fixture-user:{secret}@example.test", Encode: "rclone-obscure"}},
		{Value: &credentialValue{Template: "https://fixture-user:{secret}@example.test/path"}},
		{Value: &credentialValue{Template: "https://fixture:user:{secret}@example.test"}},
		{Value: &credentialValue{Template: "https://fixture-user:{secret}@example.test\nsecond line"}},
		{Value: &credentialValue{Template: "http://fixture-user:{secret}@example.test"}},
		{Value: &credentialValue{Template: "https://:{secret}@example.test"}},
		{Value: &credentialValue{Template: "https://fixture-user:{secret}@"}},
	}
	for index, credential := range refused {
		if isCredentialStoreURLSource(credential) {
			t.Errorf("case %d: a template that is not one credential-store line was accepted: %+v", index, credential)
		}
	}
}

func TestRegistryCredentialForRefusesZeroAndTwoGroups(t *testing.T) {
	groups := map[string]tokenGroup{
		"one.rotate": {Credentials: []registryCredential{{Credential: "shared-cred"}}},
		"two.rotate": {Credentials: []registryCredential{{Credential: "shared-cred"}}},
	}
	if _, _, err := registryCredentialFor(groups, "shared-cred"); err == nil ||
		!strings.Contains(err.Error(), "one.rotate, two.rotate") {
		t.Errorf("err = %v, want it to name both groups", err)
	}
	if _, _, err := registryCredentialFor(groups, "absent-cred"); err == nil ||
		!strings.Contains(err.Error(), "no rotation registry entry for 'absent-cred'") {
		t.Errorf("err = %v, want a no-entry refusal", err)
	}
	single := map[string]tokenGroup{"one.rotate": {Credentials: []registryCredential{{Credential: "only-cred"}}}}
	credential, alias, err := registryCredentialFor(single, "only-cred")
	if err != nil || alias != "one.rotate" || credential.Credential != "only-cred" {
		t.Errorf("credential=%+v alias=%q err=%v", credential, alias, err)
	}
}

func TestCredentialValueShape(t *testing.T) {
	cases := []struct {
		credential registryCredential
		want       string
	}{
		{registryCredential{}, "plain"},
		{registryCredential{Value: &credentialValue{Template: "{secret}"}}, "template"},
		{registryCredential{Value: &credentialValue{Template: "{secret}", Encode: "rclone-obscure"}}, "template, encode rclone-obscure"},
	}
	for _, tc := range cases {
		if got := credentialValueShape(tc.credential); got != tc.want {
			t.Errorf("credentialValueShape(%+v) = %q, want %q", tc.credential, got, tc.want)
		}
	}
}

// TestRenderCredentialValueDoesNotAliasTheSecret guards a subtle one: the
// absent-value path returns the secret itself, and the caller zeroes its
// own copy afterwards, so the returned plaintext must be a copy.
func TestRenderCredentialValueDoesNotAliasTheSecret(t *testing.T) {
	secret := []byte("tok-fixture")
	rendered, err := renderCredentialValue(nil, secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range secret {
		secret[i] = 0
	}
	if !bytes.Equal(rendered, []byte("tok-fixture")) {
		t.Errorf("rendered plaintext = %q; it aliases the caller's buffer", rendered)
	}
}
