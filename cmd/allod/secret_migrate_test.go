//go:build secret

package main

// Tests for 'allod secret migrate'. Unlike the other secret tests, these
// keep the registry on disk and read it back through the same file the
// command edits: migrate's whole job is producing exact registry text out
// of bytes it decrypted once, so the fixture's lib.forgejoTokenGroups is
// parsed from that same file rather than handed over as a Go map.
//
// Every fixture name is invented — fixture-host, dev-a, fixture-user,
// example.test — and every secret is an obvious placeholder, so a leaked
// fixture value would be visibly a fixture.
//
// The package-level seams these helpers swap are shared mutable state, so no
// test here calls t.Parallel, matching secret_test.go.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// migrateFixture is a secrets checkout whose registry lives on disk. The
// decrypt seam answers per path and records every call, so a test can pin
// both what was decrypted and that nothing else was.
type migrateFixture struct {
	*secretFixture
	registryPath string
	plaintext    map[string][]byte
	decryptedFor []string
}

func newMigrateFixture(t *testing.T, registryJSON string) *migrateFixture {
	t.Helper()
	fx := newSecretFixture(t)
	mf := &migrateFixture{
		secretFixture: fx,
		registryPath:  filepath.Join(fx.checkout, "forgejo-token-groups.json"),
		plaintext:     map[string][]byte{},
	}
	if err := os.WriteFile(mf.registryPath, []byte(registryJSON), 0644); err != nil {
		t.Fatal(err)
	}
	// The command reads lib.forgejoTokenGroups and edits the JSON file that
	// export is built from, so the fixture derives one from the other
	// instead of letting them drift.
	secretEvalRegistry = func(checkout string) (map[string]tokenGroup, error) {
		data, err := os.ReadFile(filepath.Join(checkout, "forgejo-token-groups.json"))
		if err != nil {
			return nil, err
		}
		var groups map[string]tokenGroup
		if err := json.Unmarshal(data, &groups); err != nil {
			return nil, err
		}
		return groups, nil
	}
	secretDecrypt = func(_ string, file string) ([]byte, error) {
		rel, err := filepath.Rel(fx.checkout, file)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		mf.decryptedFor = append(mf.decryptedFor, rel)
		value, ok := mf.plaintext[rel]
		if !ok {
			return nil, fmt.Errorf("fixture: no plaintext staged for %s", rel)
		}
		return value, nil
	}
	return mf
}

// addCredential gives one registry credential the non-secret half every
// landing gate checks: a credentials.nix entry, a secrets.nix recipient
// list, and a committed ciphertext, plus the plaintext the decrypt seam
// will answer with.
func (mf *migrateFixture) addCredential(t *testing.T, name, plaintext string) {
	t.Helper()
	path := "secrets/" + name + ".age"
	mf.credentials[name] = credentialEntry{
		Name: name, RotationState: "active",
		Consumers: []credentialConsumer{{Type: "agenix", Repo: "secrets", Secret: path}},
	}
	mf.recipients[path] = []string{fixtureHostKey, fixtureVMKey}
	mf.plaintext[path] = []byte(plaintext)
	full := filepath.Join(mf.checkout, path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("age-encryption.org/v1\n-> fixture "+name+"\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func (mf *migrateFixture) commit(t *testing.T) {
	t.Helper()
	gitRun(t, mf.checkout, "add", "-A")
	if status := gitRun(t, mf.checkout, "status", "--porcelain"); status != "" {
		gitRun(t, mf.checkout, "commit", "-q", "-m", "fixture: registry")
	}
}

// assertAbsentFromCheckout is the strongest form of "the plaintext never
// reached disk" this fixture can make: every byte under the checkout, the
// git object store included, is searched for the secret.
func (mf *migrateFixture) assertAbsentFromCheckout(t *testing.T, secret string) {
	t.Helper()
	err := filepath.WalkDir(mf.checkout, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), secret) {
			rel, _ := filepath.Rel(mf.checkout, path)
			t.Errorf("the decrypted secret was written to %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// --- Registry fixtures ---

const migrateStoreRegistry = `{
  "store.rotate": {
    "service": "forgejo",
    "account": "fixture-user",
    "ui_token_name": "fixture-token",
    "registry_alias": "store.rotate",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "store-cred",
        "secret_path": "secrets/store-cred.age",
        "format": "credential-store-url",
        "targets": [
          {
            "system": "fixture-host",
            "kind": "nixos-host",
            "deployed_path": "/root/.git-credentials",
            "verify": {
              "type": "git-ls-remote",
              "repo_url": "https://example.test/fixture/repo.git",
              "credential_context": "the fixture repository"
            }
          }
        ]
      }
    ]
  },
  "other.rotate": {
    "service": "none",
    "registry_alias": "other.rotate",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "untouched-cred",
        "secret_path": "secrets/untouched-cred.age",
        "value": { "template": "https://fixture-user:{secret}@example.test" },
        "targets": [
          { "system": "dev-a", "kind": "dev-vm", "deployed_path": "/root/.git-credentials", "verify": "allod site check" }
        ]
      }
    ]
  }
}
`

const migrateRcloneRegistry = `{
  "site.rotate": {
    "service": "none",
    "registry_alias": "site.rotate",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "site-cred",
        "secret_path": "secrets/site-cred.age",
        "format": "rclone-remote-stanza",
        "targets": [
          {
            "system": "fixture-host",
            "kind": "nixos-host",
            "deployed_path": "/root/.config/rclone/rclone.conf",
            "verify": { "type": "site-check" }
          }
        ]
      }
    ]
  }
}
`

const migrateCanonicalStanza = "[shared]\ntype = ftp\nhost = ftp.example.test\nuser = fixture-user\npass = OLD-OBSCURED\nexplicit_tls = true\n"

// --- The happy paths ---

// TestSecretMigrateCredentialStoreWritesTheTemplateAndLeavesEverythingElse
// pins the whole contract in one run: the edited credential's exact new
// JSON, every other byte of the registry unchanged, the ciphertext
// untouched, one decrypt and no encrypt, and the secret nowhere on disk or
// on either stream.
func TestSecretMigrateCredentialStoreWritesTheTemplateAndLeavesEverythingElse(t *testing.T) {
	mf := newMigrateFixture(t, migrateStoreRegistry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
	mf.commit(t)
	beforeCiphertext := mf.file(t, "secrets/store-cred.age")
	beforeHead := mf.head(t)

	out, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}

	want := `{
  "store.rotate": {
    "service": "forgejo",
    "account": "fixture-user",
    "ui_token_name": "fixture-token",
    "registry_alias": "store.rotate",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "store-cred",
        "secret_path": "secrets/store-cred.age",
        "value": {
          "template": "https://fixture-user:{secret}@example.test"
        },
        "targets": [
          {
            "system": "fixture-host",
            "kind": "nixos-host",
            "deployed_path": "/root/.git-credentials",
            "verify": "sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote https://example.test/fixture/repo.git HEAD"
          }
        ]
      }
    ]
  },
  "other.rotate": {
    "service": "none",
    "registry_alias": "other.rotate",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "untouched-cred",
        "secret_path": "secrets/untouched-cred.age",
        "value": { "template": "https://fixture-user:{secret}@example.test" },
        "targets": [
          { "system": "dev-a", "kind": "dev-vm", "deployed_path": "/root/.git-credentials", "verify": "allod site check" }
        ]
      }
    ]
  }
}
`
	if got := mf.file(t, "forgejo-token-groups.json"); got != want {
		t.Errorf("forgejo-token-groups.json =\n%s\nwant\n%s", got, want)
	}
	if got := mf.file(t, "secrets/store-cred.age"); got != beforeCiphertext {
		t.Error("the ciphertext was rewritten; migrate changes registry text only")
	}
	if mf.encryptCalls != 0 {
		t.Errorf("age was asked to encrypt %d times, want 0", mf.encryptCalls)
	}
	if len(mf.decryptedFor) != 1 || mf.decryptedFor[0] != "secrets/store-cred.age" {
		t.Errorf("decrypted %v, want exactly secrets/store-cred.age once", mf.decryptedFor)
	}
	if got := mf.head(t); got == beforeHead {
		t.Error("no commit was made")
	}
	if got := mf.commitFiles(t); got != "forgejo-token-groups.json" {
		t.Errorf("commit files = %q, want the registry alone", got)
	}
	if got := mf.originHead(t, "agent/landing"); got != mf.head(t) {
		t.Errorf("origin agent/landing = %q, want the new HEAD %q", got, mf.head(t))
	}
	if got := mf.status(t); got != "" {
		t.Errorf("tree is not clean after landing:\n%s", got)
	}
	if strings.Contains(out+errText, "tok-fixture") {
		t.Error("the decrypted secret was printed")
	}
	mf.assertAbsentFromCheckout(t, "tok-fixture")
	for _, wantLine := range []string{
		"forgejo-token-groups.json: store-cred in group store.rotate now declares a value template; secrets/store-cred.age is unchanged",
		"on agent/landing and pushed to origin",
	} {
		if !strings.Contains(out, wantLine) {
			t.Errorf("stdout lacks %q\ngot: %q", wantLine, out)
		}
	}
}

// TestSecretMigrateCredentialStoreKeepsATrailingNewline pins the
// round-trip rule where it actually bites: whichever byte shape the
// ciphertext held is the one the template must reproduce.
func TestSecretMigrateCredentialStoreKeepsATrailingNewline(t *testing.T) {
	mf := newMigrateFixture(t, migrateStoreRegistry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test\n")
	mf.commit(t)

	if _, errText, code := mf.run(t, "secret", "migrate", "store-cred"); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	registry := mf.file(t, "forgejo-token-groups.json")
	if want := `"template": "https://fixture-user:{secret}@example.test\n"`; !strings.Contains(registry, want) {
		t.Errorf("registry lacks %q\ngot:\n%s", want, registry)
	}
}

// TestSecretMigrateRcloneStanzaDeclaresTheEncoder covers the issue's
// contract for an rclone stanza: the template names the encoder even though
// the ciphertext still holds the already-obscured pass, because the encoder
// describes what the next rotation does rather than what this migration did.
func TestSecretMigrateRcloneStanzaDeclaresTheEncoder(t *testing.T) {
	mf := newMigrateFixture(t, migrateRcloneRegistry)
	mf.addCredential(t, "site-cred", migrateCanonicalStanza)
	mf.commit(t)
	beforeCiphertext := mf.file(t, "secrets/site-cred.age")

	out, errText, code := mf.run(t, "secret", "migrate", "site-cred")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	registry := mf.file(t, "forgejo-token-groups.json")
	want := `        "value": {
          "template": "[shared]\ntype = ftp\nhost = ftp.example.test\nuser = fixture-user\npass = {secret}\nexplicit_tls = true\n",
          "encode": "rclone-obscure"
        },`
	if !strings.Contains(registry, want) {
		t.Errorf("registry lacks\n%s\ngot\n%s", want, registry)
	}
	if !strings.Contains(registry, `"verify": "allod site check"`) {
		t.Errorf("registry lacks the converted site-check command\n%s", registry)
	}
	if got := mf.file(t, "secrets/site-cred.age"); got != beforeCiphertext {
		t.Error("the ciphertext was rewritten")
	}
	if strings.Contains(out+errText, "OLD-OBSCURED") {
		t.Error("the obscured password was printed")
	}
	mf.assertAbsentFromCheckout(t, "OLD-OBSCURED")
}

// --- Only the canonical bytes parse ---

func TestLegacyValueTemplateAcceptsOnlyCanonicalBytes(t *testing.T) {
	cases := []struct {
		name, format, raw string
		wantTemplate      string
		wantSecret        string
		wantErr           string
	}{
		{
			name: "credential store", format: "credential-store-url",
			raw:          "https://fixture-user:tok-fixture@example.test",
			wantTemplate: "https://fixture-user:{secret}@example.test", wantSecret: "tok-fixture",
		},
		{
			name: "credential store with a trailing newline", format: "credential-store-url",
			raw:          "https://fixture-user:tok-fixture@example.test\n",
			wantTemplate: "https://fixture-user:{secret}@example.test\n", wantSecret: "tok-fixture",
		},
		{name: "credential store, two lines", format: "credential-store-url",
			raw: "https://fixture-user:tok-fixture@example.test\nsecond line\n", wantErr: "not one canonical"},
		{name: "credential store, blank line first", format: "credential-store-url",
			raw: "\nhttps://fixture-user:tok-fixture@example.test", wantErr: "not one canonical"},
		{name: "credential store, no scheme", format: "credential-store-url",
			raw: "fixture-user:tok-fixture@example.test", wantErr: "not one canonical"},
		{name: "credential store, no user", format: "credential-store-url",
			raw: "https://:tok-fixture@example.test", wantErr: "not one canonical"},
		{name: "credential store, host carries a path", format: "credential-store-url",
			raw: "https://fixture-user:tok-fixture@example.test/repo", wantErr: "not one canonical"},
		{name: "credential store, empty", format: "credential-store-url", raw: "", wantErr: "not one canonical"},
		{
			name: "rclone stanza", format: "rclone-remote-stanza", raw: migrateCanonicalStanza,
			wantTemplate: "[shared]\ntype = ftp\nhost = ftp.example.test\nuser = fixture-user\npass = {secret}\nexplicit_tls = true\n",
			wantSecret:   "OLD-OBSCURED",
		},
		{name: "rclone stanza without its trailing newline", format: "rclone-remote-stanza",
			raw: strings.TrimSuffix(migrateCanonicalStanza, "\n"), wantErr: "not one canonical"},
		{name: "rclone stanza, wrong remote name", format: "rclone-remote-stanza",
			raw: strings.Replace(migrateCanonicalStanza, "[shared]", "[other]", 1), wantErr: "not one canonical"},
		{name: "rclone stanza, reordered fields", format: "rclone-remote-stanza",
			raw: "[shared]\ntype = ftp\nuser = fixture-user\nhost = ftp.example.test\npass = OLD-OBSCURED\nexplicit_tls = true\n", wantErr: "not one canonical"},
		{name: "rclone stanza, extra trailing content", format: "rclone-remote-stanza",
			raw: migrateCanonicalStanza + "extra = 1\n", wantErr: "not one canonical"},
		{name: "rclone stanza, missing explicit_tls", format: "rclone-remote-stanza",
			raw: strings.Replace(migrateCanonicalStanza, "explicit_tls = true\n", "", 1), wantErr: "not one canonical"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, secret, err := legacyValueTemplate(tc.format, []byte(tc.raw), "secrets/x.age")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if value.Template != tc.wantTemplate {
				t.Errorf("template = %q, want %q", value.Template, tc.wantTemplate)
			}
			if string(secret) != tc.wantSecret {
				t.Errorf("secret = %q, want %q", secret, tc.wantSecret)
			}
			rendered := strings.Replace(value.Template, "{secret}", string(secret), 1)
			if rendered != tc.raw {
				t.Errorf("round trip produced %q, want the original %q", rendered, tc.raw)
			}
		})
	}
}

// TestSecretMigrateRefusesWhenTheSecretAlsoAppearsInTheTemplate is the
// leak guard: a token that also occurs in the host would be written into
// the registry, in plain text, as part of the "non-secret" half.
func TestSecretMigrateRefusesWhenTheSecretAlsoAppearsInTheTemplate(t *testing.T) {
	mf := newMigrateFixture(t, migrateStoreRegistry)
	mf.addCredential(t, "store-cred", "https://fixture-user:example@example.test")
	mf.commit(t)
	before := mf.file(t, "forgejo-token-groups.json")
	beforeHead := mf.head(t)

	out, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "also occurs in the non-secret text around it") {
		t.Errorf("stderr = %q", errText)
	}
	if strings.Contains(out+errText, "https://fixture-user:example@example.test") {
		t.Error("the decrypted plaintext was printed")
	}
	if got := mf.file(t, "forgejo-token-groups.json"); got != before {
		t.Error("the registry was changed despite the refusal")
	}
	if got := mf.head(t); got != beforeHead {
		t.Error("a commit was made")
	}
}

func TestSecretMigrateRefusesANonCanonicalPlaintextWithoutWriting(t *testing.T) {
	mf := newMigrateFixture(t, migrateStoreRegistry)
	mf.addCredential(t, "store-cred", "not a credential store url at all")
	mf.commit(t)
	before := mf.file(t, "forgejo-token-groups.json")

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "is not one canonical 'https://<user>:<token>@<host>' credential-store line") {
		t.Errorf("stderr = %q", errText)
	}
	if got := mf.file(t, "forgejo-token-groups.json"); got != before {
		t.Error("the registry was changed despite the refusal")
	}
	if mf.checkCalls != 0 {
		t.Errorf("flake check ran %d times on a refusal", mf.checkCalls)
	}
}

// --- Verify conversion ---

func TestLegacyVerifyCommand(t *testing.T) {
	cases := []struct {
		name    string
		target  migrateTarget
		want    string
		wantErr string
	}{
		{
			name: "git-ls-remote",
			target: migrateTarget{System: "fixture-host", Kind: "nixos-host", DeployedPath: "/root/.git-credentials",
				Verify: json.RawMessage(`{"type":"git-ls-remote","repo_url":"https://example.test/fixture/repo.git"}`)},
			want: "sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote https://example.test/fixture/repo.git HEAD",
		},
		{
			name: "git-ls-remote on a vm keeps the same inner command",
			target: migrateTarget{System: "dev-a", Kind: "dev-vm", DeployedPath: "/root/.git-credentials",
				Verify: json.RawMessage(`{"type":"git-ls-remote","repo_url":"https://example.test/fixture/repo.git"}`)},
			want: "sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote https://example.test/fixture/repo.git HEAD",
		},
		{
			name: "forge-token-verify on a nixos host",
			target: migrateTarget{System: "fixture-host", Kind: "nixos-host", User: "fixture-user", DeployedPath: "/home/fixture-user/token",
				Verify: json.RawMessage(`{"type":"forge-token-verify"}`)},
			want: "sudo -u fixture-user forge token verify < /home/fixture-user/token",
		},
		{
			name: "forge-token-verify on a vm",
			target: migrateTarget{System: "dev-a", Kind: "dev-vm", DeployedPath: "/home/fixture-user/token",
				Verify: json.RawMessage(`{"type":"forge-token-verify"}`)},
			want: "forge token verify < /home/fixture-user/token",
		},
		{
			name:   "site-check",
			target: migrateTarget{System: "fixture-host", Kind: "nixos-host", Verify: json.RawMessage(`{"type":"site-check"}`)},
			want:   "allod site check",
		},
		{
			name:   "tailscale-status",
			target: migrateTarget{System: "dev-a", Kind: "dev-vm", Verify: json.RawMessage(`{"type":"tailscale-status"}`)},
			want:   "tailscale status --peers=false",
		},
		{
			name:    "git-ls-remote with no repo_url",
			target:  migrateTarget{System: "dev-a", Kind: "dev-vm", Verify: json.RawMessage(`{"type":"git-ls-remote"}`)},
			wantErr: "carries no repo_url",
		},
		{
			name: "forge-token-verify on a nixos host with no user",
			target: migrateTarget{System: "fixture-host", Kind: "nixos-host", DeployedPath: "/home/fixture-user/token",
				Verify: json.RawMessage(`{"type":"forge-token-verify"}`)},
			wantErr: "carries no user",
		},
		{
			name:    "forge-token-verify with no deployed_path",
			target:  migrateTarget{System: "dev-a", Kind: "dev-vm", Verify: json.RawMessage(`{"type":"forge-token-verify"}`)},
			wantErr: "carries no deployed_path",
		},
		{
			name:    "an unknown probe type",
			target:  migrateTarget{System: "dev-a", Kind: "dev-vm", Verify: json.RawMessage(`{"type":"ping"}`)},
			wantErr: "its verify type 'ping' is not one migrate can translate",
		},
		{
			name:    "already a command string",
			target:  migrateTarget{System: "dev-a", Kind: "dev-vm", Verify: json.RawMessage(`"allod site check"`)},
			wantErr: "not a structured legacy probe object",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := legacyVerifyCommand(tc.target)
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
				t.Errorf("command = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSecretMigrateRefusesAnUnknownVerifyTypeBeforeDecrypting proves the
// order of operations: everything checkable without the plaintext is
// checked first, so a registry migrate cannot translate costs no decrypt.
func TestSecretMigrateRefusesAnUnknownVerifyTypeBeforeDecrypting(t *testing.T) {
	registry := strings.Replace(migrateStoreRegistry, `"type": "git-ls-remote",`, `"type": "ping",`, 1)
	mf := newMigrateFixture(t, registry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
	mf.commit(t)
	before := mf.file(t, "forgejo-token-groups.json")

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "its verify type 'ping' is not one migrate can translate") {
		t.Errorf("stderr = %q", errText)
	}
	if len(mf.decryptedFor) != 0 {
		t.Errorf("decrypted %v before refusing on registry data alone", mf.decryptedFor)
	}
	if got := mf.file(t, "forgejo-token-groups.json"); got != before {
		t.Error("the registry was changed despite the refusal")
	}
}

// TestSecretMigrateRefusesARegistryFieldItCannotCarryForward is the
// fail-closed half of re-rendering the credential object: a field migrate
// does not model would be silently dropped, and dropping registry data
// during a migration is the failure the whole command exists to avoid.
func TestSecretMigrateRefusesARegistryFieldItCannotCarryForward(t *testing.T) {
	registry := strings.Replace(migrateStoreRegistry,
		`"format": "credential-store-url",`,
		`"format": "credential-store-url",
        "rotation_note": "something a future schema added",`, 1)
	mf := newMigrateFixture(t, registry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
	mf.commit(t)

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "carries something migrate cannot rewrite without losing it") {
		t.Errorf("stderr = %q", errText)
	}
	if len(mf.decryptedFor) != 0 {
		t.Errorf("decrypted %v before refusing on registry data alone", mf.decryptedFor)
	}
}

// --- Refusals that need no plaintext ---

func TestSecretMigrateRefusals(t *testing.T) {
	cases := []struct {
		name       string
		registry   string
		credential string
		want       string
	}{
		{
			name:       "already migrated",
			registry:   migrateStoreRegistry,
			credential: "untouched-cred",
			want:       "credential 'untouched-cred' already carries a declared value",
		},
		{
			name:       "no registry entry",
			registry:   migrateStoreRegistry,
			credential: "nope",
			want:       "no rotation registry entry for 'nope'",
		},
		{
			name: "two registry groups",
			registry: strings.Replace(migrateStoreRegistry,
				`"credential": "untouched-cred",
        "secret_path": "secrets/untouched-cred.age",
        "value": { "template": "https://fixture-user:{secret}@example.test" },`,
				`"credential": "store-cred",
        "secret_path": "secrets/store-cred.age",
        "value": { "template": "https://fixture-user:{secret}@example.test" },`, 1),
			credential: "store-cred",
			want:       "registered in more than one rotation registry group (other.rotate, store.rotate)",
		},
		{
			name:       "a plain legacy entry needs no decryption",
			registry:   strings.Replace(migrateStoreRegistry, `"format": "credential-store-url"`, `"format": "raw-forgejo-token"`, 1),
			credential: "store-cred",
			want:       "A plain legacy entry needs no decryption: edit forgejo-token-groups.json to drop its 'format'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mf := newMigrateFixture(t, tc.registry)
			mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
			mf.addCredential(t, "untouched-cred", "https://fixture-user:tok-fixture@example.test")
			mf.commit(t)
			before := mf.file(t, "forgejo-token-groups.json")
			beforeHead := mf.head(t)

			_, errText, code := mf.run(t, "secret", "migrate", tc.credential)
			if code == 0 {
				t.Fatal("exit 0, want a refusal")
			}
			if !strings.Contains(errText, tc.want) {
				t.Errorf("stderr lacks %q\ngot: %q", tc.want, errText)
			}
			if len(mf.decryptedFor) != 0 {
				t.Errorf("decrypted %v; every refusal here is settled from registry data alone", mf.decryptedFor)
			}
			if got := mf.file(t, "forgejo-token-groups.json"); got != before {
				t.Error("the registry was changed despite the refusal")
			}
			if got := mf.head(t); got != beforeHead {
				t.Error("a commit was made")
			}
			if got := mf.status(t); got != "" {
				t.Errorf("tree is not clean after a refusal:\n%s", got)
			}
		})
	}
}

// --- local_auth_refresh sources ---

// migrateStoreLocalAuthRegistry is migrateStoreRegistry with a
// local_auth_refresh entry naming store-cred as its source_credential, the
// same insertion point and fields validateGroupMetadata expects (system and
// deployed_path matching the credential's own credential-store-url
// target).
var migrateStoreLocalAuthRegistry = strings.Replace(migrateStoreRegistry,
	`"rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "store-cred",`,
	`"rotation_strategy": "overlap",
    "local_auth_refresh": [
      { "contract": "nixos-netrc-from-root-git-credentials", "system": "fixture-host", "local_username": "fixture-user", "source_credential": "store-cred" }
    ],
    "credentials": [
      {
        "credential": "store-cred",`, 1)

// installFakeRefreshLocalAuth puts an executable file named
// 'refresh-local-auth' on PATH ahead of whatever is already there. Migrate
// only asks whether it resolves; it never runs it, so the file's content
// does not matter.
func installFakeRefreshLocalAuth(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "refresh-local-auth"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// pathWithoutRefreshLocalAuth builds a PATH that resolves 'git' (migrate's
// own real subprocess for the landing) but can never resolve
// 'refresh-local-auth', regardless of what the ambient environment happens
// to have installed, the way secret_rotate_test.go's pathWithoutRclone
// builds one that can never resolve 'rclone'.
func pathWithoutRefreshLocalAuth(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(git, filepath.Join(dir, "git")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSecretMigrateLocalAuthSourceMigratesWithRefreshLocalAuthOnPATH pins
// that a credential-store-url credential named as a local_auth_refresh
// source migrates exactly like an ordinary one once 'refresh-local-auth'
// resolves on PATH: the golden registry bytes, the untouched ciphertext,
// the landed commit, and that the migrated group still passes
// validateGroupMetadata.
func TestSecretMigrateLocalAuthSourceMigratesWithRefreshLocalAuthOnPATH(t *testing.T) {
	installFakeRefreshLocalAuth(t)
	mf := newMigrateFixture(t, migrateStoreLocalAuthRegistry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
	mf.commit(t)
	beforeCiphertext := mf.file(t, "secrets/store-cred.age")
	beforeHead := mf.head(t)

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}

	want := `{
  "store.rotate": {
    "service": "forgejo",
    "account": "fixture-user",
    "ui_token_name": "fixture-token",
    "registry_alias": "store.rotate",
    "rotation_strategy": "overlap",
    "local_auth_refresh": [
      { "contract": "nixos-netrc-from-root-git-credentials", "system": "fixture-host", "local_username": "fixture-user", "source_credential": "store-cred" }
    ],
    "credentials": [
      {
        "credential": "store-cred",
        "secret_path": "secrets/store-cred.age",
        "value": {
          "template": "https://fixture-user:{secret}@example.test"
        },
        "targets": [
          {
            "system": "fixture-host",
            "kind": "nixos-host",
            "deployed_path": "/root/.git-credentials",
            "verify": "sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote https://example.test/fixture/repo.git HEAD"
          }
        ]
      }
    ]
  },
  "other.rotate": {
    "service": "none",
    "registry_alias": "other.rotate",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "untouched-cred",
        "secret_path": "secrets/untouched-cred.age",
        "value": { "template": "https://fixture-user:{secret}@example.test" },
        "targets": [
          { "system": "dev-a", "kind": "dev-vm", "deployed_path": "/root/.git-credentials", "verify": "allod site check" }
        ]
      }
    ]
  }
}
`
	if got := mf.file(t, "forgejo-token-groups.json"); got != want {
		t.Errorf("forgejo-token-groups.json =\n%s\nwant\n%s", got, want)
	}
	if got := mf.file(t, "secrets/store-cred.age"); got != beforeCiphertext {
		t.Error("the ciphertext was rewritten; migrate changes registry text only")
	}
	if got := mf.head(t); got == beforeHead {
		t.Error("no commit was made")
	}

	var groups map[string]tokenGroup
	if err := json.Unmarshal([]byte(want), &groups); err != nil {
		t.Fatalf("could not decode the migrated registry: %s", err)
	}
	validateGroupMetadata("store.rotate", groups["store.rotate"])
}

// TestSecretMigrateLocalAuthSourceRefusedWithoutRefreshLocalAuthOnPATH pins
// that the same credential is refused, before anything is decrypted, when
// 'refresh-local-auth' cannot be found on PATH, and that the registry is
// left untouched.
func TestSecretMigrateLocalAuthSourceRefusedWithoutRefreshLocalAuthOnPATH(t *testing.T) {
	t.Setenv("PATH", pathWithoutRefreshLocalAuth(t))
	mf := newMigrateFixture(t, migrateStoreLocalAuthRegistry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
	mf.commit(t)
	before := mf.file(t, "forgejo-token-groups.json")
	beforeHead := mf.head(t)

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "'refresh-local-auth' was not found on PATH") {
		t.Errorf("stderr = %q", errText)
	}
	if !strings.Contains(errText, "this host's nexus pin predates allod/nexus#52") {
		t.Errorf("stderr does not explain why: %q", errText)
	}
	if len(mf.decryptedFor) != 0 {
		t.Errorf("decrypted %v; refresh-local-auth's absence is settled from PATH alone", mf.decryptedFor)
	}
	if got := mf.file(t, "forgejo-token-groups.json"); got != before {
		t.Error("the registry was changed despite the refusal")
	}
	if got := mf.head(t); got != beforeHead {
		t.Error("a commit was made")
	}
}

// TestSecretMigrateRefusesAnRcloneStanzaAsALocalAuthSource pins that a
// container format that can never render a netrc line is refused before
// decrypt, naming the format, regardless of whether refresh-local-auth is
// on PATH.
func TestSecretMigrateRefusesAnRcloneStanzaAsALocalAuthSource(t *testing.T) {
	installFakeRefreshLocalAuth(t)
	registry := strings.Replace(migrateRcloneRegistry,
		`"rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "site-cred",`,
		`"rotation_strategy": "overlap",
    "local_auth_refresh": [
      { "contract": "nixos-netrc-from-root-git-credentials", "system": "fixture-host", "local_username": "fixture-user", "source_credential": "site-cred" }
    ],
    "credentials": [
      {
        "credential": "site-cred",`, 1)
	mf := newMigrateFixture(t, registry)
	mf.addCredential(t, "site-cred", migrateCanonicalStanza)
	mf.commit(t)
	before := mf.file(t, "forgejo-token-groups.json")
	beforeHead := mf.head(t)

	_, errText, code := mf.run(t, "secret", "migrate", "site-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "only a 'credential-store-url' container can migrate it") {
		t.Errorf("stderr = %q", errText)
	}
	if !strings.Contains(errText, "legacy format 'rclone-remote-stanza'") {
		t.Errorf("stderr does not name the format: %q", errText)
	}
	if len(mf.decryptedFor) != 0 {
		t.Errorf("decrypted %v before refusing on registry data alone", mf.decryptedFor)
	}
	if got := mf.file(t, "forgejo-token-groups.json"); got != before {
		t.Error("the registry was changed despite the refusal")
	}
	if got := mf.head(t); got != beforeHead {
		t.Error("a commit was made")
	}
}

func TestSecretMigrateRefusesTheDefaultBranchAndADirtyTree(t *testing.T) {
	t.Run("default branch", func(t *testing.T) {
		mf := newMigrateFixture(t, migrateStoreRegistry)
		mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
		mf.commit(t)
		gitRun(t, mf.checkout, "switch", "-q", "master")
		_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
		if code == 0 || !strings.Contains(errText, "is on its default branch 'master'") {
			t.Errorf("code=%d stderr=%q", code, errText)
		}
	})
	t.Run("dirty tree", func(t *testing.T) {
		mf := newMigrateFixture(t, migrateStoreRegistry)
		mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
		mf.commit(t)
		if err := os.WriteFile(filepath.Join(mf.checkout, "secrets", "leftover.age"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
		if code == 0 || !strings.Contains(errText, "uncommitted or untracked changes") {
			t.Errorf("code=%d stderr=%q", code, errText)
		}
	})
}

// --- Landing ---

func TestSecretMigrateRestoresTheRegistryWhenChecksFail(t *testing.T) {
	mf := newMigrateFixture(t, migrateStoreRegistry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
	mf.commit(t)
	before := mf.file(t, "forgejo-token-groups.json")
	beforeHead := mf.head(t)
	mf.checkStatus = 3

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code != 3 {
		t.Errorf("exit %d, want the check's status 3", code)
	}
	if !strings.Contains(errText, "the repository's checks failed; restored forgejo-token-groups.json") {
		t.Errorf("stderr = %q", errText)
	}
	if got := mf.file(t, "forgejo-token-groups.json"); got != before {
		t.Errorf("the registry was not restored byte for byte:\n%s", got)
	}
	if got := mf.head(t); got != beforeHead {
		t.Error("a commit was made")
	}
	if got := mf.status(t); got != "" {
		t.Errorf("tree is not clean:\n%s", got)
	}
	mf.assertAbsentFromCheckout(t, "tok-fixture")
}

func TestSecretMigrateReportsPushFailureAndKeepsCommit(t *testing.T) {
	mf := newMigrateFixture(t, migrateStoreRegistry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
	mf.commit(t)
	beforeHead := mf.head(t)
	gitRun(t, mf.checkout, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "gone.git"))

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code == 0 {
		t.Fatal("exit 0 despite a failed push")
	}
	if !strings.Contains(errText, "but the push failed") || !strings.Contains(errText, "push the branch yourself") {
		t.Errorf("stderr = %q", errText)
	}
	if got := mf.head(t); got == beforeHead {
		t.Error("the commit was not kept")
	}
	if got := mf.status(t); got != "" {
		t.Errorf("tree is not clean:\n%s", got)
	}
}

func TestSecretMigrateRestoresTheRegistryWhenTheCommitFails(t *testing.T) {
	mf := newMigrateFixture(t, migrateStoreRegistry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
	mf.commit(t)
	before := mf.file(t, "forgejo-token-groups.json")
	installFailingPreCommit(t, mf.secretFixture)

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code == 0 {
		t.Fatal("exit 0 despite a failed commit")
	}
	if !strings.Contains(errText, "git commit failed; restored forgejo-token-groups.json") {
		t.Errorf("stderr = %q", errText)
	}
	if got := mf.file(t, "forgejo-token-groups.json"); got != before {
		t.Error("the registry was not restored")
	}
	if staged := gitRun(t, mf.checkout, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("files left staged: %q", staged)
	}
}

// --- The registry span finder ---

func TestFindRegistryCredentialSpan(t *testing.T) {
	text := []byte(migrateStoreRegistry)
	start, end, err := findRegistryCredentialSpan(text, "store.rotate", "store-cred")
	if err != nil {
		t.Fatalf("findRegistryCredentialSpan: %v", err)
	}
	var entry migrateCredential
	if err := json.Unmarshal(text[start:end], &entry); err != nil {
		t.Fatalf("the located span is not one credential object: %v\n%s", err, text[start:end])
	}
	if entry.Credential != "store-cred" || entry.Format != "credential-store-url" {
		t.Errorf("located %+v", entry)
	}
	if got := registryEntryIndent(text, start); got != "      " {
		t.Errorf("indent = %q, want six spaces", got)
	}
	if _, _, err := findRegistryCredentialSpan(text, "store.rotate", "absent"); err == nil {
		t.Error("an absent credential was located")
	}
	if _, _, err := findRegistryCredentialSpan(text, "absent.rotate", "store-cred"); err == nil {
		t.Error("an absent group was located")
	}
}

// --- The legacy 'user' field ---

// migrateTokenRegistry carries the one legacy shape that still has a
// target-level 'user': a forge-token-verify probe on a nixos-host, where
// rotate-token baked the user into 'sudo -u <user>'.
const migrateTokenRegistry = `{
  "token.rotate": {
    "service": "forgejo",
    "account": "fixture-user",
    "ui_token_name": "fixture-token",
    "registry_alias": "token.rotate",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "store-cred",
        "secret_path": "secrets/store-cred.age",
        "format": "credential-store-url",
        "targets": [
          {
            "system": "fixture-host",
            "kind": "nixos-host",
            "user": "fixture-user",
            "deployed_path": "/home/fixture-user/token",
            "verify": { "type": "forge-token-verify" }
          }
        ]
      }
    ]
  }
}
`

// TestSecretMigrateBakesTheLegacyUserIntoTheCommand pins the disappearance
// of the 'user' field end to end: it is consumed by the command string and
// then gone from the registry, because command-specific metadata now lives
// in the string rather than in a field beside it.
func TestSecretMigrateBakesTheLegacyUserIntoTheCommand(t *testing.T) {
	mf := newMigrateFixture(t, migrateTokenRegistry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
	mf.commit(t)

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	want := `{
  "token.rotate": {
    "service": "forgejo",
    "account": "fixture-user",
    "ui_token_name": "fixture-token",
    "registry_alias": "token.rotate",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "store-cred",
        "secret_path": "secrets/store-cred.age",
        "value": {
          "template": "https://fixture-user:{secret}@example.test"
        },
        "targets": [
          {
            "system": "fixture-host",
            "kind": "nixos-host",
            "deployed_path": "/home/fixture-user/token",
            "verify": "sudo -u fixture-user forge token verify < /home/fixture-user/token"
          }
        ]
      }
    ]
  }
}
`
	if got := mf.file(t, "forgejo-token-groups.json"); got != want {
		t.Errorf("forgejo-token-groups.json =\n%s\nwant\n%s", got, want)
	}
}

// TestSecretMigrateRefusesAUserItCannotCarry is the other half: 'user' is
// registry data, and only forge-token-verify has anywhere to put it. On any
// other verify type, dropping it would lose data the migration exists to
// carry forward.
func TestSecretMigrateRefusesAUserItCannotCarry(t *testing.T) {
	registry := strings.Replace(migrateStoreRegistry,
		`"kind": "nixos-host",`,
		`"kind": "nixos-host",
            "user": "fixture-user",`, 1)
	mf := newMigrateFixture(t, registry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
	mf.commit(t)
	before := mf.file(t, "forgejo-token-groups.json")

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "its verify type 'git-ls-remote' has no use for the target's 'user' field"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	if len(mf.decryptedFor) != 0 {
		t.Errorf("decrypted %v before refusing on registry data alone", mf.decryptedFor)
	}
	if got := mf.file(t, "forgejo-token-groups.json"); got != before {
		t.Error("the registry was changed despite the refusal")
	}
}

// TestSecretMigrateRefusesAnUnknownVerifyField is the same fail-closed rule
// applied inside the verify object: a field this command does not model
// would vanish when the object becomes a string.
func TestSecretMigrateRefusesAnUnknownVerifyField(t *testing.T) {
	registry := strings.Replace(migrateStoreRegistry,
		`"type": "git-ls-remote",`,
		`"type": "git-ls-remote",
              "timeout_seconds": 30,`, 1)
	mf := newMigrateFixture(t, registry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
	mf.commit(t)

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "not a structured legacy probe object migrate can translate without losing a field"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	if len(mf.decryptedFor) != 0 {
		t.Errorf("decrypted %v before refusing on registry data alone", mf.decryptedFor)
	}
}

// --- Legacy metadata is not shell syntax ---

// TestSecretMigrateRefusesLegacyMetadataThatWouldBecomeShellText covers the
// fields that reach the verify command unquoted. The legacy shape never
// constrained them, and a one-line string passes the new validator, so a
// 'user' or a 'repo_url' carrying shell syntax would become a second
// command in a line printed for an operator to paste into a shell.
func TestSecretMigrateRefusesLegacyMetadataThatWouldBecomeShellText(t *testing.T) {
	cases := []struct {
		name     string
		registry string
		want     string
	}{
		{
			name: "a user carrying a command separator",
			registry: strings.Replace(migrateTokenRegistry,
				`"user": "fixture-user",`, `"user": "root; touch /tmp/migrate-fixture #",`, 1),
			want: `its user "root; touch /tmp/migrate-fixture #" does not match`,
		},
		{
			name: "a repo_url carrying a command separator",
			registry: strings.Replace(migrateStoreRegistry,
				`"repo_url": "https://example.test/fixture/repo.git",`,
				`"repo_url": "https://example.test/fixture/repo.git; touch /tmp/migrate-fixture #",`, 1),
			want: "is not an https://<host>/... URL",
		},
		{
			name: "a deployed_path carrying a quote",
			registry: strings.Replace(migrateTokenRegistry,
				`"deployed_path": "/home/fixture-user/token",`,
				`"deployed_path": "/home/fixture-user/'token'",`, 1),
			want: "is not an absolute path free of whitespace",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mf := newMigrateFixture(t, tc.registry)
			mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
			mf.commit(t)
			before := mf.file(t, "forgejo-token-groups.json")
			beforeHead := mf.head(t)

			_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
			if code == 0 {
				t.Fatal("exit 0, want a refusal")
			}
			if !strings.Contains(errText, tc.want) {
				t.Errorf("stderr lacks %q\ngot: %q", tc.want, errText)
			}
			if !strings.Contains(errText, "write") || !strings.Contains(errText, "by hand") {
				t.Errorf("the refusal does not tell the operator to write the command by hand\ngot: %q", errText)
			}
			if len(mf.decryptedFor) != 0 {
				t.Errorf("decrypted %v before refusing on registry data alone", mf.decryptedFor)
			}
			if got := mf.file(t, "forgejo-token-groups.json"); got != before {
				t.Error("the registry was changed despite the refusal")
			}
			if got := mf.head(t); got != beforeHead {
				t.Error("a commit was made")
			}
		})
	}
}

// --- Gates that must precede the decrypt ---

// TestSecretMigrateRefusesAnUnexportedEncoderBeforeDecrypting pins the
// order: an rclone migration declares value.encode "rclone-obscure", and a
// checkout that no longer exports it would have its registry rejected by
// its own flake check — after the ciphertext had been opened for nothing.
func TestSecretMigrateRefusesAnUnexportedEncoderBeforeDecrypting(t *testing.T) {
	mf := newMigrateFixture(t, migrateRcloneRegistry)
	mf.encodings = nil
	mf.addCredential(t, "site-cred", migrateCanonicalStanza)
	mf.commit(t)
	before := mf.file(t, "forgejo-token-groups.json")

	_, errText, code := mf.run(t, "secret", "migrate", "site-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := `declares value.encode "rclone-obscure"`; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	if len(mf.decryptedFor) != 0 {
		t.Errorf("decrypted %v before refusing on registry data alone", mf.decryptedFor)
	}
	if got := mf.file(t, "forgejo-token-groups.json"); got != before {
		t.Error("the registry was changed despite the refusal")
	}
	if mf.checkCalls != 0 {
		t.Errorf("flake check ran %d times on a refusal", mf.checkCalls)
	}
}

// --- The written declaration is validated too ---

// TestSecretMigrateRefusesATemplateTheRegistryWouldReject is the case both
// byte proofs pass and the declaration is still unusable: a legacy host
// containing a literal '{secret}' round-trips exactly, and the extracted
// token appears nowhere in the surrounding text, yet the template it
// produces holds two placeholders.
func TestSecretMigrateRefusesATemplateTheRegistryWouldReject(t *testing.T) {
	mf := newMigrateFixture(t, migrateStoreRegistry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@x{secret}y")
	mf.commit(t)
	before := mf.file(t, "forgejo-token-groups.json")
	beforeHead := mf.head(t)

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "value.template must contain exactly one {secret} placeholder"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	if got := mf.file(t, "forgejo-token-groups.json"); got != before {
		t.Error("the registry was changed despite the refusal")
	}
	if got := mf.head(t); got != beforeHead {
		t.Error("a commit was made")
	}
}

// TestSecretMigrateRefusesANonUTF8Template pins the byte-preserving
// contract at the one place it cannot be kept: a template is JSON text, and
// encoding/json would replace the invalid byte with U+FFFD, storing a
// declaration that renders something the credential never held.
func TestSecretMigrateRefusesANonUTF8Template(t *testing.T) {
	mf := newMigrateFixture(t, migrateStoreRegistry)
	mf.addCredential(t, "store-cred", "https://fix\x80ture-user:tok-fixture@example.test")
	mf.commit(t)
	before := mf.file(t, "forgejo-token-groups.json")
	beforeCiphertext := mf.file(t, "secrets/store-cred.age")
	beforeHead := mf.head(t)

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "is not valid UTF-8"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	if got := mf.file(t, "forgejo-token-groups.json"); got != before {
		t.Error("the registry was changed despite the refusal")
	}
	if got := mf.file(t, "secrets/store-cred.age"); got != beforeCiphertext {
		t.Error("the ciphertext was rewritten despite the refusal")
	}
	if got := mf.head(t); got != beforeHead {
		t.Error("a commit was made")
	}
}

// --- One entry, in one group ---

// TestSecretMigrateRefusesADuplicateEntryInOneGroup covers the shape the
// "one group" check used to let through: two entries of the same name in
// one group are one group, and migrate would rewrite the first textual
// entry and commit, leaving the second in the legacy shape.
func TestSecretMigrateRefusesADuplicateEntryInOneGroup(t *testing.T) {
	registry := strings.Replace(migrateStoreRegistry,
		`    "credentials": [
      {
        "credential": "store-cred",`,
		`    "credentials": [
      {
        "credential": "store-cred",
        "secret_path": "secrets/store-cred.age",
        "format": "credential-store-url",
        "targets": [
          {
            "system": "dev-a",
            "kind": "dev-vm",
            "deployed_path": "/root/.git-credentials",
            "verify": { "type": "site-check" }
          }
        ]
      },
      {
        "credential": "store-cred",`, 1)
	mf := newMigrateFixture(t, registry)
	mf.addCredential(t, "store-cred", "https://fixture-user:tok-fixture@example.test")
	mf.commit(t)
	before := mf.file(t, "forgejo-token-groups.json")
	beforeHead := mf.head(t)

	_, errText, code := mf.run(t, "secret", "migrate", "store-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "credential 'store-cred' is listed 2 times in rotation registry group 'store.rotate'"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	if len(mf.decryptedFor) != 0 {
		t.Errorf("decrypted %v before refusing on registry data alone", mf.decryptedFor)
	}
	if got := mf.file(t, "forgejo-token-groups.json"); got != before {
		t.Error("the registry was changed despite the refusal")
	}
	if got := mf.head(t); got != beforeHead {
		t.Error("a commit was made")
	}
}
