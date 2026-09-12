//go:build secret

package main

// Tests for 'allod secret rotate'. The pure ports — the format parsers, the
// registry validator, the format-mix guard, the commit-subject and
// rebuild-target helpers — are tested directly, the way TestFlipPendingToActive
// tests flipPendingToActive in secret_test.go. The command itself is driven
// through runAllod against secretFixture's real git checkout, the way
// secret_test.go drives create and rekey; rotateFixture adds what a
// registry group needs beyond secretFixture's single credential: a real
// committed ciphertext per group member (restore-on-failure needs real
// bytes to put back) and, for the structured formats, the old plaintext
// each path's fake decrypt returns.
//
// The package-level seams these helpers swap are shared mutable state, so no
// test here calls t.Parallel, matching secret_test.go.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- Direct unit tests: the pure ports ---

func TestParseCredentialStorePlaintext(t *testing.T) {
	cases := []struct {
		name, plaintext, wantUser, wantHost, wantErr string
	}{
		{"simple", "https://alice:secret-tok@forge.test", "alice", "forge.test", ""},
		{"trailing newline", "https://alice:secret-tok@forge.test\n", "alice", "forge.test", ""},
		{"multiline", "https://alice:secret-tok@forge.test\nsecond line\n", "", "", "must contain exactly one non-empty line"},
		{"blank lines tolerated", "\n\nhttps://alice:secret-tok@forge.test\n\n", "alice", "forge.test", ""},
		{"no scheme", "alice:secret-tok@forge.test", "", "", "not a supported https://user:token@host URL"},
		{"missing colon", "https://alicesecret-tok@forge.test", "", "", "not a supported https://user:token@host URL"},
		{"missing at", "https://alice:secret-tokforge.test", "", "", "not a supported https://user:token@host URL"},
		{"empty", "", "", "", "must contain exactly one non-empty line"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user, host, err := parseCredentialStorePlaintext([]byte(tc.plaintext), "secrets/x.age")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if user != tc.wantUser || host != tc.wantHost {
				t.Errorf("user=%q host=%q, want user=%q host=%q", user, host, tc.wantUser, tc.wantHost)
			}
		})
	}
}

func TestParseRcloneRemoteStanza(t *testing.T) {
	valid := "[shared]\ntype = ftp\nhost = ftp.example.org\nuser = deploy\npass = OLD-OBSCURED\nexplicit_tls = true\n"
	cases := []struct {
		name, plaintext, wantHost, wantUser, wantErr string
	}{
		{"valid", valid, "ftp.example.org", "deploy", ""},
		{"missing trailing newline", strings.TrimSuffix(valid, "\n"), "ftp.example.org", "deploy", ""},
		{"wrong remote name", strings.Replace(valid, "[shared]", "[other]", 1), "", "", "is not exactly one [shared] section"},
		{"missing explicit_tls", strings.Replace(valid, "explicit_tls = true\n", "", 1), "", "", "is not exactly one [shared] section"},
		{"extra trailing content", valid + "extra = 1\n", "", "", "is not exactly one [shared] section"},
		{"reordered fields", "[shared]\ntype = ftp\nuser = deploy\nhost = ftp.example.org\npass = OLD-OBSCURED\nexplicit_tls = true\n", "", "", "is not exactly one [shared] section"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, user, err := parseRcloneRemoteStanza([]byte(tc.plaintext), "secrets/x.age")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != tc.wantHost || user != tc.wantUser {
				t.Errorf("host=%q user=%q, want host=%q user=%q", host, user, tc.wantHost, tc.wantUser)
			}
		})
	}
}

func TestBuildRcloneRemoteStanzaRoundTripsThroughParse(t *testing.T) {
	stanza := buildRcloneRemoteStanza("ftp.example.org", "deploy", "OBS-newpass")
	host, user, err := parseRcloneRemoteStanza([]byte(stanza), "secrets/x.age")
	if err != nil {
		t.Fatalf("built stanza does not parse: %v\n%s", err, stanza)
	}
	if host != "ftp.example.org" || user != "deploy" {
		t.Errorf("host=%q user=%q", host, user)
	}
}

func TestGroupCommitSubject(t *testing.T) {
	if got := groupCommitSubject("dev-a.git", tokenGroup{Service: "forgejo"}); got != "rotate dev-a.git Forgejo token" {
		t.Errorf("forgejo subject = %q", got)
	}
	if got := groupCommitSubject("plain.service", tokenGroup{Service: "none"}); got != "rotate plain.service" {
		t.Errorf("none subject = %q", got)
	}
}

func TestUniqueRebuildTargetsDedupesAndSortsBySystemThenKind(t *testing.T) {
	group := tokenGroup{Credentials: []registryCredential{
		{Targets: []registryTarget{{System: "dev-b", Kind: "dev-vm"}, {System: "control-host", Kind: "nixos-host"}}},
		{Targets: []registryTarget{{System: "dev-b", Kind: "dev-vm"}, {System: "dev-a", Kind: "dev-vm"}}},
	}}
	got := uniqueRebuildTargets(group)
	want := []rebuildTarget{{"control-host", "nixos-host"}, {"dev-a", "dev-vm"}, {"dev-b", "dev-vm"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

// rotateDies runs f the way run() runs a namespace: it recovers the
// cliExit panic die() raises, so validateGroupMetadata, selectRotationGroup,
// and assertUniformPromptedFormat — all written to call die() directly, the
// way lookupSecret already does — can be tested without a CLI invocation.
func rotateDies(t *testing.T, f func()) (code int, message string) {
	t.Helper()
	var errBuf bytes.Buffer
	restore := swapStreams(io.Discard, &errBuf)
	defer restore()
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		if e, ok := recovered.(cliExit); ok {
			code, message = e.code, errBuf.String()
			return
		}
		panic(recovered)
	}()
	f()
	return 0, errBuf.String()
}

func validRotateGroup() tokenGroup {
	return tokenGroup{
		RegistryAlias:    "g",
		Service:          "forgejo",
		Account:          "acct",
		UITokenName:      "ui-token",
		RotationStrategy: "overlap",
		Credentials: []registryCredential{{
			Credential: "cred", SecretPath: "secrets/cred.age", Format: "raw-forgejo-token",
			Targets: []registryTarget{{
				System: "dev-a", Kind: "dev-vm", DeployedPath: "/home/x/token",
				Verify: registryVerify{Type: "forge-token-verify"},
			}},
		}},
	}
}

func TestValidateGroupMetadataRefusals(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*tokenGroup)
		wantErr string
	}{
		{"valid group passes", func(*tokenGroup) {}, ""},
		{"bad service", func(g *tokenGroup) { g.Service = "github" }, "service 'github'"},
		{"empty registry_alias", func(g *tokenGroup) { g.RegistryAlias = "" }, "registry_alias is empty"},
		{"forgejo missing account", func(g *tokenGroup) { g.Account = "" }, "needs a non-empty account"},
		{"bad rotation_strategy", func(g *tokenGroup) { g.RotationStrategy = "immediate" }, "rotation_strategy 'immediate'"},
		{"no credentials", func(g *tokenGroup) { g.Credentials = nil }, "credentials is empty"},
		{"bad format", func(g *tokenGroup) { g.Credentials[0].Format = "plaintext" }, "unsupported format 'plaintext'"},
		{"no targets", func(g *tokenGroup) { g.Credentials[0].Targets = nil }, "has no targets"},
		{"bad target kind", func(g *tokenGroup) { g.Credentials[0].Targets[0].Kind = "laptop" }, "unsupported kind 'laptop'"},
		{"bad verify type", func(g *tokenGroup) { g.Credentials[0].Targets[0].Verify.Type = "manual" }, "unsupported verify type 'manual'"},
		{"local_auth_refresh bad contract", func(g *tokenGroup) {
			g.LocalAuthRefresh = []localAuthRefreshEntry{{Contract: "run-anything", System: "dev-a", LocalUsername: "u", SourceCredential: "cred"}}
		}, "unsupported contract 'run-anything'"},
		{"local_auth_refresh unmatched source", func(g *tokenGroup) {
			g.LocalAuthRefresh = []localAuthRefreshEntry{{Contract: "nixos-netrc-from-root-git-credentials", System: "dev-a", LocalUsername: "u", SourceCredential: "nope"}}
		}, "does not name exactly one credential-store-url target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			group := validRotateGroup()
			tc.mutate(&group)
			code, message := rotateDies(t, func() { validateGroupMetadata("g", group) })
			if tc.wantErr == "" {
				if code != 0 {
					t.Fatalf("valid group refused: %s", message)
				}
				return
			}
			if code == 0 || !strings.Contains(message, tc.wantErr) {
				t.Errorf("code=%d message=%q, want it to mention %q", code, message, tc.wantErr)
			}
		})
	}
}

func TestAssertUniformPromptedFormat(t *testing.T) {
	uniform := []registryCredential{{Format: "raw-forgejo-token"}, {Format: "credential-store-url"}}
	if code, _ := rotateDies(t, func() { assertUniformPromptedFormat("g", uniform) }); code != 0 {
		t.Error("a group of only non-stanza formats was refused")
	}
	allStanza := []registryCredential{{Format: "rclone-remote-stanza"}, {Format: "rclone-remote-stanza"}}
	if code, _ := rotateDies(t, func() { assertUniformPromptedFormat("g", allStanza) }); code != 0 {
		t.Error("a group of only stanza formats was refused")
	}
	mixed := []registryCredential{{Format: "rclone-remote-stanza"}, {Format: "raw-forgejo-token"}}
	code, message := rotateDies(t, func() { assertUniformPromptedFormat("mixed.group", mixed) })
	if code == 0 || !strings.Contains(message, "mixes rclone-remote-stanza with a token format") {
		t.Errorf("code=%d message=%q", code, message)
	}
}

// --- The command, against a real fixture ---

// rotateGroupCredential is one member addGroup adds to a fixture's registry
// group: a real committed ciphertext (so restore-on-failure has real bytes
// to put back) and the registry shape validateGroupMetadata checks.
type rotateGroupCredential struct {
	name, format, state                          string // state defaults to "active"
	system, kind, user, deployedPath, verifyType string
	repoURL, context                             string
	plaintext                                    string // old plaintext the fake decrypt returns; only formats that decrypt need it
	skipCiphertext                               bool
}

// rotateFixture augments secretFixture with what rotate's tests need beyond
// a single credential: a per-path old-plaintext lookup for secretDecrypt,
// which secretFixture's own fake does not have (create and rekey only ever
// decrypt one path per run).
type rotateFixture struct {
	*secretFixture
	decrypted    map[string][]byte // checkout-relative path -> old plaintext
	decryptCalls int
}

func newRotateFixture(t *testing.T) *rotateFixture {
	t.Helper()
	fx := newSecretFixture(t)
	rf := &rotateFixture{secretFixture: fx, decrypted: map[string][]byte{}}
	secretDecrypt = func(_ string, file string) ([]byte, error) {
		rf.decryptCalls++
		rel, err := filepath.Rel(fx.checkout, file)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		plaintext, ok := rf.decrypted[rel]
		if !ok {
			return nil, fmt.Errorf("fixture: no old plaintext staged for %s", rel)
		}
		return plaintext, nil
	}
	return rf
}

// addGroup registers a fully-shaped registry group and commits a
// placeholder ciphertext for each active member, so the checkout stays
// clean and every gate rotate checks (state, ciphertext presence,
// recipients, registry agreement, metadata shape) passes on the parts a
// test does not itself mean to break.
func (rf *rotateFixture) addGroup(t *testing.T, alias string, group tokenGroup, creds ...rotateGroupCredential) {
	t.Helper()
	for _, c := range creds {
		path := "secrets/" + c.name + ".age"
		state := c.state
		if state == "" {
			state = "active"
		}
		rf.credentials[c.name] = credentialEntry{
			Name: c.name, RotationState: state,
			Consumers: []credentialConsumer{{Type: "agenix", Repo: "secrets", Secret: path}},
		}
		rf.recipients[path] = []string{fixtureHostKey, fixtureVMKey}
		group.Credentials = append(group.Credentials, registryCredential{
			Credential: c.name, SecretPath: path, Format: c.format,
			Targets: []registryTarget{{
				System: c.system, Kind: c.kind, User: c.user, DeployedPath: c.deployedPath,
				Verify: registryVerify{Type: c.verifyType, RepoURL: c.repoURL, CredentialContext: c.context},
			}},
		})
		if state == "active" && !c.skipCiphertext {
			full := filepath.Join(rf.checkout, path)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatal(err)
			}
			placeholder := "age-encryption.org/v1\n-> fixture placeholder " + c.name + "\n---\n"
			if err := os.WriteFile(full, []byte(placeholder), 0644); err != nil {
				t.Fatal(err)
			}
		}
		if c.plaintext != "" {
			rf.decrypted[path] = []byte(c.plaintext)
		}
	}
	rf.registry[alias] = group
	// A group whose only member is pending or deliberately missing its
	// ciphertext (the two refusal fixtures that want no file on disk)
	// leaves nothing to commit; skip the commit rather than fail on it.
	gitRun(t, rf.checkout, "add", "-A")
	if status := gitRun(t, rf.checkout, "status", "--porcelain"); status != "" {
		gitRun(t, rf.checkout, "commit", "-q", "-m", "fixture: add "+alias)
	}
}

// installFakeRclone puts a real, minimal 'rclone' on PATH ahead of whatever
// is already there, the way rotate-token's own tests fake it: it answers
// only 'obscure -', reads the password on stdin (never argv), and returns a
// value deterministic in the input so a test can assert the password
// crossed on stdin and landed, obscured, in the rebuilt stanza.
func installFakeRclone(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nset -e\nif [ \"$1\" = obscure ] && [ \"$2\" = - ]; then\n  input=$(cat)\n  printf 'OBS-%s' \"$input\"\n  exit 0\nfi\necho 'fixture rclone: unexpected invocation' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "rclone"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// pathWithoutRclone builds a PATH that resolves 'git' (rotate's own real
// subprocess for the landing) but can never resolve 'rclone', regardless of
// what the ambient environment happens to have installed.
func pathWithoutRclone(t *testing.T) string {
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

// --- Group resolution and gate refusals ---

func TestSecretRotateGroupSelectionRefusals(t *testing.T) {
	cases := []struct {
		name       string
		credential string
		setup      func(t *testing.T, rf *rotateFixture)
		want       string
	}{
		{"unknown credential", "nope", func(*testing.T, *rotateFixture) {}, "no rotation registry entry for 'nope'"},
		{"pending entry", "pending-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "pending.rotate", tokenGroup{RegistryAlias: "pending.rotate", Service: "forgejo", Account: "a", UITokenName: "u", RotationStrategy: "overlap"},
				rotateGroupCredential{name: "pending-cred", format: "raw-forgejo-token", state: "pending", system: "dev-a", kind: "dev-vm", user: "u", deployedPath: "/home/u/token", verifyType: "forge-token-verify"})
		}, "credential 'pending-cred' is 'pending', not 'active'"},
		{"missing ciphertext", "missing-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "missing.rotate", tokenGroup{RegistryAlias: "missing.rotate", Service: "forgejo", Account: "a", UITokenName: "u", RotationStrategy: "overlap"},
				rotateGroupCredential{name: "missing-cred", format: "raw-forgejo-token", skipCiphertext: true, system: "dev-a", kind: "dev-vm", user: "u", deployedPath: "/home/u/token", verifyType: "forge-token-verify"})
		}, "does not exist while 'missing-cred' is 'active'"},
		{"unsupported metadata", "bogus-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "bogus.rotate", tokenGroup{RegistryAlias: "bogus.rotate", Service: "forgejo", Account: "a", UITokenName: "u", RotationStrategy: "sometimes"},
				rotateGroupCredential{name: "bogus-cred", format: "raw-forgejo-token", system: "dev-a", kind: "dev-vm", user: "u", deployedPath: "/home/u/token", verifyType: "forge-token-verify"})
		}, "unsupported metadata"},
		{"mixed formats", "mixed-stanza", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "mixed.rotate", tokenGroup{RegistryAlias: "mixed.rotate", Service: "forgejo", Account: "a", UITokenName: "u", RotationStrategy: "overlap"},
				rotateGroupCredential{name: "mixed-stanza", format: "rclone-remote-stanza", system: "control-host", kind: "nixos-host", deployedPath: "/root/.config/rclone/rclone.conf", verifyType: "site-check"},
				rotateGroupCredential{name: "mixed-url", format: "credential-store-url", system: "control-host", kind: "nixos-host", deployedPath: "/root/.git-credentials", verifyType: "git-ls-remote", repoURL: "https://forge.test/x/y.git", context: "root-netrc"})
		}, "mixes rclone-remote-stanza with a token format"},
		{"dirty tree", "dirty-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "dirty.rotate", tokenGroup{RegistryAlias: "dirty.rotate", Service: "forgejo", Account: "a", UITokenName: "u", RotationStrategy: "overlap"},
				rotateGroupCredential{name: "dirty-cred", format: "raw-forgejo-token", system: "dev-a", kind: "dev-vm", user: "u", deployedPath: "/home/u/token", verifyType: "forge-token-verify"})
			if err := os.WriteFile(filepath.Join(rf.checkout, "secrets", "leftover.age"), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}, "uncommitted or untracked changes"},
		{"default branch", "branch-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "branch.rotate", tokenGroup{RegistryAlias: "branch.rotate", Service: "forgejo", Account: "a", UITokenName: "u", RotationStrategy: "overlap"},
				rotateGroupCredential{name: "branch-cred", format: "raw-forgejo-token", system: "dev-a", kind: "dev-vm", user: "u", deployedPath: "/home/u/token", verifyType: "forge-token-verify"})
			gitRun(t, rf.checkout, "switch", "-q", "master")
		}, "is on its default branch 'master'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rf := newRotateFixture(t)
			tc.setup(t, rf)
			_, errText, code := rf.run(t, "secret", "rotate", tc.credential)
			if code == 0 {
				t.Fatalf("exit 0, want a refusal")
			}
			if !strings.Contains(errText, tc.want) {
				t.Errorf("stderr lacks %q\ngot: %q", tc.want, errText)
			}
			if rf.encryptCalls != 0 {
				t.Errorf("age was asked to encrypt %d times, want 0", rf.encryptCalls)
			}
			if tc.name == "dirty tree" {
				os.Remove(filepath.Join(rf.checkout, "secrets", "leftover.age"))
			}
			// setup's own fixture commit already moved HEAD, so a clean
			// tree — not an unmoved HEAD — is the proof rotate itself
			// landed nothing.
			if got := rf.status(t); got != "" {
				t.Errorf("tree is not clean after a refusal:\n%s", got)
			}
		})
	}
}

// --- Format carry-forward ---

func TestSecretRotateRawFormatWritesValueAsIs(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "raw.rotate", tokenGroup{RegistryAlias: "raw.rotate", Service: "forgejo", Account: "agent", UITokenName: "agent-token", RotationStrategy: "overlap"},
		rotateGroupCredential{name: "raw-cred", format: "raw-forgejo-token", system: "dev-a", kind: "dev-vm", user: "testuser", deployedPath: "/home/testuser/.config/git/forgejo-token", verifyType: "forge-token-verify"})
	before := rf.head(t)
	rf.pipe("new-raw-value\n")

	out, errText, code := rf.run(t, "secret", "rotate", "raw-cred")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if string(rf.lastPlaintext) != "new-raw-value\n" {
		t.Errorf("plaintext handed to age = %q, want the stdin bytes verbatim", rf.lastPlaintext)
	}
	if want := []string{fixtureHostKey, fixtureVMKey}; strings.Join(rf.lastRecipients, "|") != strings.Join(want, "|") {
		t.Errorf("recipients = %v, want %v", rf.lastRecipients, want)
	}
	if got := rf.file(t, "secrets/raw-cred.age"); !strings.HasPrefix(got, "age-encryption.org/v1\n") {
		t.Errorf("ciphertext file = %q", got)
	}
	if got := rf.head(t); got == before {
		t.Error("no commit was made")
	}
	if got, want := gitRun(t, rf.checkout, "log", "-1", "--format=%s"), "rotate raw.rotate Forgejo token"; got != want {
		t.Errorf("commit subject = %q, want %q", got, want)
	}
	if !strings.Contains(out, "Wrote secrets/raw-cred.age, encrypted to 2 recipients from secrets.nix") {
		t.Errorf("stdout = %q", out)
	}
	if got := rf.originHead(t, "agent/landing"); got != rf.head(t) {
		t.Errorf("origin agent/landing = %q, want the new HEAD %q", got, rf.head(t))
	}
	if strings.Contains(out+errText, "new-raw-value") {
		t.Error("the plaintext was printed")
	}
}

// encryptRecorder replaces secretEncrypt so a test can see every call, not
// just the last (secretFixture's own recorder keeps one).
func encryptRecorder() (calls *[][2]any, restore func()) {
	previous := secretEncrypt
	var recorded [][2]any
	secretEncrypt = func(recipients []string, plaintext []byte) ([]byte, error) {
		recorded = append(recorded, [2]any{append([]string(nil), recipients...), append([]byte(nil), plaintext...)})
		return []byte(fmt.Sprintf("age-encryption.org/v1\n-> fixture call %d\n", len(recorded))), nil
	}
	return &recorded, func() { secretEncrypt = previous }
}

func TestSecretRotateCredentialStoreURLCarriesUserAndHostForwardAcrossGroup(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "shared.rotate", tokenGroup{RegistryAlias: "shared.rotate", Service: "forgejo", Account: "testuser", UITokenName: "shared-token", RotationStrategy: "overlap"},
		rotateGroupCredential{
			name: "cred-a", format: "credential-store-url", system: "control-host", kind: "nixos-host",
			deployedPath: "/root/.git-credentials", verifyType: "git-ls-remote",
			repoURL: "https://forge.test/testuser/private.git", context: "root-netrc",
			plaintext: "https://alice:old-shared@forge.test",
		},
		rotateGroupCredential{
			name: "cred-b", format: "credential-store-url", system: "dev-a", kind: "dev-vm",
			deployedPath: "/root/.git-credentials", verifyType: "git-ls-remote",
			repoURL: "https://forge.test/testuser/private.git", context: "root-netrc",
			plaintext: "https://bob:old-shared@forge.test",
		})

	calls, restoreEncrypt := encryptRecorder()
	defer restoreEncrypt()
	rf.pipe("new-shared-token\n")

	out, errText, code := rf.run(t, "secret", "rotate", "cred-a")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if len(*calls) != 2 {
		t.Fatalf("age was asked to encrypt %d times, want 2", len(*calls))
	}
	if got := string((*calls)[0][1].([]byte)); got != "https://alice:new-shared-token@forge.test" {
		t.Errorf("cred-a plaintext = %q", got)
	}
	if got := string((*calls)[1][1].([]byte)); got != "https://bob:new-shared-token@forge.test" {
		t.Errorf("cred-b plaintext = %q", got)
	}
	if got, want := gitRun(t, rf.checkout, "show", "--name-only", "--format=", "HEAD"), "secrets/cred-a.age\nsecrets/cred-b.age"; got != want {
		t.Errorf("commit files = %q, want %q", got, want)
	}
	for _, want := range []string{"Wrote secrets/cred-a.age, encrypted to 2 recipients", "Wrote secrets/cred-b.age, encrypted to 2 recipients"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q\ngot: %q", want, out)
		}
	}
	if strings.Contains(out+errText, "new-shared-token") || strings.Contains(out+errText, "old-shared") {
		t.Error("a token value was printed")
	}
}

func TestSecretRotateRcloneStanzaObscuresPasswordOnly(t *testing.T) {
	installFakeRclone(t)
	rf := newRotateFixture(t)
	rf.addGroup(t, "site.rotate", tokenGroup{RegistryAlias: "site.rotate", Service: "forgejo", Account: "site", UITokenName: "site-token", RotationStrategy: "overlap"},
		rotateGroupCredential{
			name: "site-cred", format: "rclone-remote-stanza", system: "control-host", kind: "nixos-host",
			deployedPath: "/root/.config/rclone/rclone.conf", verifyType: "site-check",
			plaintext: "[shared]\ntype = ftp\nhost = ftp.example.org\nuser = deploy\npass = OLD-OBSCURED\nexplicit_tls = true\n",
		})

	calls, restoreEncrypt := encryptRecorder()
	defer restoreEncrypt()
	rf.pipe("new-site-password\n")

	out, errText, code := rf.run(t, "secret", "rotate", "site-cred")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if len(*calls) != 1 {
		t.Fatalf("age was asked to encrypt %d times, want 1", len(*calls))
	}
	want := "[shared]\ntype = ftp\nhost = ftp.example.org\nuser = deploy\npass = OBS-new-site-password\nexplicit_tls = true\n"
	if got := string((*calls)[0][1].([]byte)); got != want {
		t.Errorf("plaintext = %q, want %q", got, want)
	}
	if !strings.Contains(out, "Wrote secrets/site-cred.age") {
		t.Errorf("stdout = %q", out)
	}
	if strings.Contains(out+errText, "new-site-password") || strings.Contains(out+errText, "OBS-new-site-password") || strings.Contains(out+errText, "OLD-OBSCURED") {
		t.Error("a password or its obscured form was printed")
	}
}

func TestSecretRotateMalformedOldPlaintextRefusesBeforeEncrypt(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "malformed.rotate", tokenGroup{RegistryAlias: "malformed.rotate", Service: "forgejo", Account: "a", UITokenName: "u", RotationStrategy: "overlap"},
		rotateGroupCredential{
			name: "malformed-cred", format: "credential-store-url", system: "control-host", kind: "nixos-host",
			deployedPath: "/root/.git-credentials", verifyType: "git-ls-remote", repoURL: "https://forge.test/x/y.git", context: "root-netrc",
			plaintext: "not a url at all",
		})
	before := rf.head(t)
	rf.pipe("value\n")

	_, errText, code := rf.run(t, "secret", "rotate", "malformed-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "is not a supported https://user:token@host URL") {
		t.Errorf("stderr = %q", errText)
	}
	if rf.encryptCalls != 0 {
		t.Errorf("age was asked to encrypt %d times, want 0", rf.encryptCalls)
	}
	if got := rf.head(t); got != before {
		t.Error("a commit was made")
	}
	if got := rf.status(t); got != "" {
		t.Errorf("tree is not clean:\n%s", got)
	}
}

func TestSecretRotateMissingRcloneIsClearFailure(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "site.rotate", tokenGroup{RegistryAlias: "site.rotate", Service: "forgejo", Account: "site", UITokenName: "site-token", RotationStrategy: "overlap"},
		rotateGroupCredential{
			name: "site-cred", format: "rclone-remote-stanza", system: "control-host", kind: "nixos-host",
			deployedPath: "/root/.config/rclone/rclone.conf", verifyType: "site-check",
			plaintext: "[shared]\ntype = ftp\nhost = ftp.example.org\nuser = deploy\npass = OLD-OBSCURED\nexplicit_tls = true\n",
		})
	before := rf.file(t, "secrets/site-cred.age")
	rf.pipe("new-site-password\n")
	t.Setenv("PATH", pathWithoutRclone(t))

	_, errText, code := rf.run(t, "secret", "rotate", "site-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "rclone not found on PATH") {
		t.Errorf("stderr = %q", errText)
	}
	if strings.Contains(errText, "new-site-password") {
		t.Error("the password was printed")
	}
	if got := rf.file(t, "secrets/site-cred.age"); got != before {
		t.Error("the ciphertext was rewritten")
	}
}

// --- Dry run ---

func TestSecretRotateDryRunPrintsStepsWithoutWriting(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "shared.rotate", tokenGroup{RegistryAlias: "shared.rotate", Service: "forgejo", Account: "testuser", UITokenName: "shared-token", RotationStrategy: "overlap"},
		rotateGroupCredential{
			name: "cred-a", format: "credential-store-url", system: "control-host", kind: "nixos-host",
			deployedPath: "/root/.git-credentials", verifyType: "git-ls-remote",
			repoURL: "https://forge.test/testuser/private.git", context: "root-netrc",
			plaintext: "https://alice:old-shared@forge.test",
		},
		rotateGroupCredential{
			name: "cred-b", format: "credential-store-url", system: "dev-a", kind: "dev-vm",
			deployedPath: "/root/.git-credentials", verifyType: "git-ls-remote",
			repoURL: "https://forge.test/testuser/private.git", context: "root-netrc",
			plaintext: "https://bob:old-shared@forge.test",
		})
	before := rf.head(t)

	out, errText, code := rf.run(t, "secret", "rotate", "cred-a", "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if rf.encryptCalls != 0 {
		t.Errorf("age was asked to encrypt %d times, want 0", rf.encryptCalls)
	}
	if rf.decryptCalls != 0 {
		t.Errorf("age was asked to decrypt %d times, want 0 (the banner promises no decrypt on a dry run)", rf.decryptCalls)
	}
	if rf.checkCalls != 0 {
		t.Errorf("flake check ran %d times, want 0", rf.checkCalls)
	}
	if got := rf.head(t); got != before {
		t.Error("a commit was made")
	}
	if got := rf.status(t); got != "" {
		t.Errorf("tree is not clean:\n%s", got)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing printed on a dry run", out)
	}
	for _, want := range []string{
		"Dry run: no prompt, decrypt, or encrypt will run.",
		"Forgejo group: shared.rotate",
		"secrets/cred-a.age",
		"secrets/cred-b.age",
		"control-host (nixos-host)",
		"dev-a (dev-vm)",
		`A live run commits secrets/cred-a.age secrets/cred-b.age`,
		`as "rotate shared.rotate Forgejo token" and pushes agent/landing to origin; merging stays your act.`,
		"nix flake update secrets",
		"sudo nixos-rebuild switch --flake ~/work/allod/deploy#control-host",
		"rebuild-vm-from-host dev-a",
		"GIT_TERMINAL_PROMPT=0 git ls-remote https://forge.test/testuser/private.git HEAD",
		"Revocation gate",
	} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr lacks %q\ngot: %q", want, errText)
		}
	}
	for _, notWanted := range []string{
		"git add secrets/cred-a.age",
		"git commit -m \"rotate shared.rotate",
		"In secrets repo",
	} {
		if strings.Contains(errText, notWanted) {
			t.Errorf("dry run instructed manual git on the secrets repo with %q\ngot: %q", notWanted, errText)
		}
	}
}

func TestSecretRotateDryRunStillRefusesADirtyTree(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "raw.rotate", tokenGroup{RegistryAlias: "raw.rotate", Service: "forgejo", Account: "agent", UITokenName: "agent-token", RotationStrategy: "overlap"},
		rotateGroupCredential{name: "raw-cred", format: "raw-forgejo-token", system: "dev-a", kind: "dev-vm", user: "testuser", deployedPath: "/home/testuser/.config/git/forgejo-token", verifyType: "forge-token-verify"})
	if err := os.WriteFile(filepath.Join(rf.checkout, "secrets", "leftover.age"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	_, errText, code := rf.run(t, "secret", "rotate", "raw-cred", "--dry-run")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "uncommitted or untracked changes") {
		t.Errorf("stderr = %q", errText)
	}
}

// --- Printed steps: service wording and the refresh-local-auth line ---

func TestSecretRotatePrintedStepsVaryByServiceAndLocalAuthRefresh(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "raw.rotate", tokenGroup{RegistryAlias: "raw.rotate", Service: "forgejo", Account: "agent", UITokenName: "agent-token", RotationStrategy: "overlap"},
		rotateGroupCredential{name: "raw-cred", format: "raw-forgejo-token", system: "dev-a", kind: "dev-vm", user: "testuser", deployedPath: "/home/testuser/.config/git/forgejo-token", verifyType: "forge-token-verify"})
	rf.addGroup(t, "none.rotate", tokenGroup{RegistryAlias: "none.rotate", Service: "none", RotationStrategy: "overlap"},
		rotateGroupCredential{name: "none-cred", format: "raw-forgejo-token", system: "dev-b", kind: "dev-vm", user: "testuser", deployedPath: "/home/testuser/.config/git/other-token", verifyType: "forge-token-verify"})
	rf.addGroup(t, "refresh.rotate", tokenGroup{
		RegistryAlias: "refresh.rotate", Service: "forgejo", Account: "testuser", UITokenName: "refresh-token", RotationStrategy: "in-place",
		LocalAuthRefresh: []localAuthRefreshEntry{{
			Contract: "nixos-netrc-from-root-git-credentials", System: "control-host", LocalUsername: "testuser", SourceCredential: "refresh-cred",
		}},
	}, rotateGroupCredential{
		name: "refresh-cred", format: "credential-store-url", system: "control-host", kind: "nixos-host",
		deployedPath: "/root/.git-credentials", verifyType: "git-ls-remote", repoURL: "https://forge.test/testuser/private.git", context: "root-netrc",
		plaintext: "https://dave:old@forge.test",
	})

	_, forgejoOut, code := rf.run(t, "secret", "rotate", "raw-cred", "--dry-run")
	if code != 0 {
		t.Fatalf("raw.rotate dry run: exit %d", code)
	}
	if !strings.Contains(forgejoOut, "Forgejo group: raw.rotate") || !strings.Contains(forgejoOut, "Forgejo token: agent/agent-token") {
		t.Errorf("forgejo header missing:\n%s", forgejoOut)
	}
	if !strings.Contains(forgejoOut, "Forgejo UI token 'agent-token' while logged in as 'agent'") {
		t.Errorf("forgejo revocation wording missing:\n%s", forgejoOut)
	}
	if strings.Contains(forgejoOut, "refresh-local-auth") {
		t.Errorf("a group with no local_auth_refresh printed the refresh step:\n%s", forgejoOut)
	}

	_, noneOut, code := rf.run(t, "secret", "rotate", "none-cred", "--dry-run")
	if code != 0 {
		t.Fatalf("none.rotate dry run: exit %d", code)
	}
	if !strings.Contains(noneOut, "Registry group: none.rotate") || !strings.Contains(noneOut, "Service: none (not a Forgejo token)") {
		t.Errorf("none header missing:\n%s", noneOut)
	}
	for _, notWanted := range []string{"Forgejo group:", "Forgejo token:", "Forgejo UI token"} {
		if strings.Contains(noneOut, notWanted) {
			t.Errorf("a none-service group printed %q:\n%s", notWanted, noneOut)
		}
	}
	if !strings.Contains(noneOut, "revoke the old\nvalue at the service that issued it.") {
		t.Errorf("generic revocation wording missing:\n%s", noneOut)
	}

	_, refreshOut, code := rf.run(t, "secret", "rotate", "refresh-cred", "--dry-run")
	if code != 0 {
		t.Fatalf("refresh.rotate dry run: exit %d", code)
	}
	if !strings.Contains(refreshOut, "rotate-token refresh-local-auth --group refresh.rotate") {
		t.Errorf("refresh-local-auth step missing:\n%s", refreshOut)
	}
	// No ciphertext was rotated by this dry run, so the step must describe
	// what a live run would print, never read as something to do now.
	if !strings.Contains(refreshOut, "After the real run lands the rotated secret, refresh declared local auth") {
		t.Errorf("dry run did not phrase the refresh step as what a live run would print:\n%s", refreshOut)
	}
	if strings.Contains(refreshOut, "Refresh declared local auth from the rotated encrypted secret before") {
		t.Errorf("dry run printed the refresh step as an instruction to run now:\n%s", refreshOut)
	}
	if !strings.Contains(refreshOut, "No old-token revocation step is printed for this group. Provider rotation is") {
		t.Errorf("in-place no-revocation wording missing:\n%s", refreshOut)
	}
	if strings.Contains(refreshOut, "Revocation gate\nAfter every rebuild") {
		t.Errorf("an in-place group printed the ordinary revocation gate:\n%s", refreshOut)
	}

	// A live run, by contrast, has actually rotated the secret by the time
	// it prints, so the same step is a real instruction to run now.
	rf.pipe("new-value\n")
	_, liveErrText, code := rf.run(t, "secret", "rotate", "refresh-cred")
	if code != 0 {
		t.Fatalf("refresh.rotate live run: exit %d, stderr: %s", code, liveErrText)
	}
	if !strings.Contains(liveErrText, "Refresh declared local auth from the rotated encrypted secret before any git push, flake-lock update, or rebuild fetch:\n   rotate-token refresh-local-auth --group refresh.rotate") {
		t.Errorf("a live run did not print the refresh step as an instruction:\n%s", liveErrText)
	}
	if strings.Contains(liveErrText, "After the real run lands") {
		t.Errorf("a live run used the dry-run phrasing:\n%s", liveErrText)
	}
}

// --- Landing ---

func TestSecretRotateRestoresAllCiphertextsWhenChecksFail(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "shared.rotate", tokenGroup{RegistryAlias: "shared.rotate", Service: "forgejo", Account: "testuser", UITokenName: "shared-token", RotationStrategy: "overlap"},
		rotateGroupCredential{
			name: "cred-a", format: "credential-store-url", system: "control-host", kind: "nixos-host",
			deployedPath: "/root/.git-credentials", verifyType: "git-ls-remote", repoURL: "https://forge.test/x/y.git", context: "root-netrc",
			plaintext: "https://alice:old@forge.test",
		},
		rotateGroupCredential{
			name: "cred-b", format: "credential-store-url", system: "dev-a", kind: "dev-vm",
			deployedPath: "/root/.git-credentials", verifyType: "git-ls-remote", repoURL: "https://forge.test/x/y.git", context: "root-netrc",
			plaintext: "https://bob:old@forge.test",
		})
	beforeA, beforeB := rf.file(t, "secrets/cred-a.age"), rf.file(t, "secrets/cred-b.age")
	before := rf.head(t)
	rf.checkStatus = 3
	rf.pipe("new-token\n")

	_, errText, code := rf.run(t, "secret", "rotate", "cred-a")
	if code != 3 {
		t.Errorf("exit %d, want the check's status 3", code)
	}
	if !strings.Contains(errText, "checks failed; restored secrets/cred-a.age, secrets/cred-b.age") {
		t.Errorf("stderr = %q", errText)
	}
	if got := rf.file(t, "secrets/cred-a.age"); got != beforeA {
		t.Error("cred-a was not restored")
	}
	if got := rf.file(t, "secrets/cred-b.age"); got != beforeB {
		t.Error("cred-b was not restored")
	}
	if got := rf.head(t); got != before {
		t.Error("a commit was made")
	}
	if got := rf.status(t); got != "" {
		t.Errorf("tree is not clean:\n%s", got)
	}
	if got := rf.originHead(t, "agent/landing"); got == rf.head(t) && got != before {
		t.Errorf("origin gained a commit rotate did not make")
	}
	// The restore writes through the same temp-file-and-rename helper the
	// new ciphertexts use; confirm it cleans up after itself the same way.
	entries, err := os.ReadDir(filepath.Join(rf.checkout, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".rotate-") {
			t.Errorf("a temporary restore file was left behind: %s", entry.Name())
		}
	}
}

// TestSecretRotateRefusesWhenTheBranchChangedDuringTheFlakeCheck covers the
// window the earlier pre-write branch check cannot: a nix flake check
// evaluates the whole repository and can run for a while, and the checkout
// could move to a different branch during that wait just as easily as
// during the earlier wait for the value on stdin. The fake flake check
// switches the branch as its own side effect, standing in for a concurrent
// process doing the same thing mid-check, so the assertion is that
// landCommitOrReportPush's own re-check — immediately before 'git add' —
// catches it with no window left afterward.
func TestSecretRotateRefusesWhenTheBranchChangedDuringTheFlakeCheck(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "raw.rotate", tokenGroup{RegistryAlias: "raw.rotate", Service: "forgejo", Account: "agent", UITokenName: "agent-token", RotationStrategy: "overlap"},
		rotateGroupCredential{name: "raw-cred", format: "raw-forgejo-token", system: "dev-a", kind: "dev-vm", user: "testuser", deployedPath: "/home/testuser/.config/git/forgejo-token", verifyType: "forge-token-verify"})
	before := rf.file(t, "secrets/raw-cred.age")
	rf.pipe("new-value\n")
	secretFlakeCheck = func(checkout string) int {
		rf.checkCalls++
		gitRun(t, checkout, "switch", "-q", "-c", "agent/hijacked-during-check")
		return 0
	}

	_, errText, code := rf.run(t, "secret", "rotate", "raw-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "moved from branch 'agent/landing' to 'agent/hijacked-during-check' while the checks ran") {
		t.Errorf("stderr = %q", errText)
	}
	if rf.checkCalls != 1 {
		t.Errorf("flake check ran %d times, want 1", rf.checkCalls)
	}
	if got := rf.file(t, "secrets/raw-cred.age"); got != before {
		t.Error("the ciphertext was not restored to its original bytes")
	}
	if got := gitRun(t, rf.checkout, "log", "-1", "--format=%s", "agent/hijacked-during-check"); got != "fixture: add raw.rotate" {
		t.Errorf("a commit landed on agent/hijacked-during-check: %q", got)
	}
}

func TestSecretRotateRestoresCiphertextsWhenCommitFails(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "shared.rotate", tokenGroup{RegistryAlias: "shared.rotate", Service: "forgejo", Account: "testuser", UITokenName: "shared-token", RotationStrategy: "overlap"},
		rotateGroupCredential{
			name: "cred-a", format: "credential-store-url", system: "control-host", kind: "nixos-host",
			deployedPath: "/root/.git-credentials", verifyType: "git-ls-remote", repoURL: "https://forge.test/x/y.git", context: "root-netrc",
			plaintext: "https://alice:old@forge.test",
		},
		rotateGroupCredential{
			name: "cred-b", format: "credential-store-url", system: "dev-a", kind: "dev-vm",
			deployedPath: "/root/.git-credentials", verifyType: "git-ls-remote", repoURL: "https://forge.test/x/y.git", context: "root-netrc",
			plaintext: "https://bob:old@forge.test",
		})
	beforeA, beforeB := rf.file(t, "secrets/cred-a.age"), rf.file(t, "secrets/cred-b.age")
	installFailingPreCommit(t, rf.secretFixture)
	rf.pipe("new-token\n")

	_, errText, code := rf.run(t, "secret", "rotate", "cred-a")
	if code == 0 {
		t.Fatal("exit 0 despite a failed commit")
	}
	if !strings.Contains(errText, "git commit failed; restored secrets/cred-a.age, secrets/cred-b.age") {
		t.Errorf("stderr = %q", errText)
	}
	if got := rf.file(t, "secrets/cred-a.age"); got != beforeA {
		t.Error("cred-a was not restored")
	}
	if got := rf.file(t, "secrets/cred-b.age"); got != beforeB {
		t.Error("cred-b was not restored")
	}
	if staged := gitRun(t, rf.checkout, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("files left staged: %q", staged)
	}
	if got := rf.status(t); got != "" {
		t.Errorf("tree is not clean:\n%s", got)
	}
}

// TestSecretRotateReportsPushFailureAndKeepsCommit covers the case where the
// value being rotated is what authenticates the push itself: the commit
// lands, the push fails, and the operator still needs every deploy, verify,
// and revocation instruction — not just the "push failed" error — to
// recover. The steps must appear before the fatal push-failure message, not
// be skipped because of it.
func TestSecretRotateReportsPushFailureAndKeepsCommit(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "raw.rotate", tokenGroup{RegistryAlias: "raw.rotate", Service: "forgejo", Account: "agent", UITokenName: "agent-token", RotationStrategy: "overlap"},
		rotateGroupCredential{name: "raw-cred", format: "raw-forgejo-token", system: "dev-a", kind: "dev-vm", user: "testuser", deployedPath: "/home/testuser/.config/git/forgejo-token", verifyType: "forge-token-verify"})
	before := rf.head(t)
	rf.pipe("value\n")
	gitRun(t, rf.checkout, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "gone.git"))

	_, errText, code := rf.run(t, "secret", "rotate", "raw-cred")
	if code == 0 {
		t.Fatal("exit 0 despite a failed push")
	}
	if !strings.Contains(errText, "but the push failed") || !strings.Contains(errText, "push the branch yourself") {
		t.Errorf("stderr = %q", errText)
	}
	if got := rf.head(t); got == before {
		t.Error("the commit was not kept")
	}
	if got := rf.status(t); got != "" {
		t.Errorf("tree is not clean:\n%s", got)
	}
	// The push's own error prints after the steps (see the ordering check
	// below), so the step describing it must say it follows, not that it
	// appears above.
	if strings.Contains(errText, "see the error above") {
		t.Error("the push-failure step claims the error is above it, but the error prints after the steps")
	}
	for _, want := range []string{
		"Forgejo group: raw.rotate",
		"is committed",
		"but the push failed; push it yourself (the error follows below)",
		"then merge it into its default branch",
		"Update the deploy flake lock",
		"Rebuild affected target(s):",
		"Verification",
		"Revocation gate",
	} {
		if !strings.Contains(errText, want) {
			t.Errorf("a push failure must still print the deploy/verify/revocation steps; stderr lacks %q\ngot: %q", want, errText)
		}
	}
	if pushFailedAt, stepsAt := strings.Index(errText, "but the push failed:"), strings.Index(errText, "--- Deployment steps ---"); pushFailedAt < stepsAt {
		t.Errorf("the fatal push-failure message printed before the steps, not after\ngot: %q", errText)
	}
}

// --- writeCiphertextAtomic ---

func TestWriteCiphertextAtomicReplacesFileWhollyOrNotAtAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.age")
	if err := os.WriteFile(path, []byte("old bytes"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeCiphertextAtomic(path, []byte("new bytes"), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new bytes" {
		t.Errorf("content = %q, want %q", got, "new bytes")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries after a write, want exactly the one final file (no leftover temp): %v", len(entries), entries)
	}

	// A destination whose directory does not exist fails at CreateTemp,
	// before anything touches the real file; there is nothing to restore
	// because nothing was opened for writing at the real path.
	if err := writeCiphertextAtomic(filepath.Join(dir, "no-such-dir", "x.age"), []byte("x"), 0644); err == nil {
		t.Error("a write into a missing directory did not fail")
	}
}

func TestSecretRotateLeavesNoTemporaryFilesBehind(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "raw.rotate", tokenGroup{RegistryAlias: "raw.rotate", Service: "forgejo", Account: "agent", UITokenName: "agent-token", RotationStrategy: "overlap"},
		rotateGroupCredential{name: "raw-cred", format: "raw-forgejo-token", system: "dev-a", kind: "dev-vm", user: "testuser", deployedPath: "/home/testuser/.config/git/forgejo-token", verifyType: "forge-token-verify"})
	rf.pipe("new-value\n")

	if _, errText, code := rf.run(t, "secret", "rotate", "raw-cred"); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	entries, err := os.ReadDir(filepath.Join(rf.checkout, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".rotate-") {
			t.Errorf("a temporary write file was left behind: %s", entry.Name())
		}
	}
}

// --- Structured value validation ---

func TestSecretRotateRefusesAStructuredValueWithEmbeddedWhitespaceOrReservedCharacters(t *testing.T) {
	cases := []struct {
		name, format, value, want string
	}{
		{"credential-store-url embedded newline", "credential-store-url", "token\nextra\n", "control character"},
		{"credential-store-url embedded space", "credential-store-url", "tok en\n", "whitespace"},
		{"credential-store-url embedded at-sign", "credential-store-url", "tok@en\n", "\"@\""},
		{"credential-store-url embedded slash", "credential-store-url", "tok/en\n", "\"/\""},
		// git percent-decodes a credential-store URL's password, so
		// 'tok%40x' would be stored as typed but later supplied to git as
		// 'tok@x' — a different value than the one that was encrypted.
		{"credential-store-url embedded percent", "credential-store-url", "tok%40x\n", "\"%\""},
		{"rclone-remote-stanza embedded newline", "rclone-remote-stanza", "pass\nextra\n", "control character"},
		{"rclone-remote-stanza embedded space", "rclone-remote-stanza", "pass word\n", "whitespace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rf := newRotateFixture(t)
			switch tc.format {
			case "credential-store-url":
				rf.addGroup(t, "cred.rotate", tokenGroup{RegistryAlias: "cred.rotate", Service: "forgejo", Account: "a", UITokenName: "u", RotationStrategy: "overlap"},
					rotateGroupCredential{
						name: "struct-cred", format: "credential-store-url", system: "control-host", kind: "nixos-host",
						deployedPath: "/root/.git-credentials", verifyType: "git-ls-remote", repoURL: "https://forge.test/x/y.git", context: "root-netrc",
						plaintext: "https://alice:old@forge.test",
					})
			case "rclone-remote-stanza":
				installFakeRclone(t)
				rf.addGroup(t, "cred.rotate", tokenGroup{RegistryAlias: "cred.rotate", Service: "forgejo", Account: "a", UITokenName: "u", RotationStrategy: "overlap"},
					rotateGroupCredential{
						name: "struct-cred", format: "rclone-remote-stanza", system: "control-host", kind: "nixos-host",
						deployedPath: "/root/.config/rclone/rclone.conf", verifyType: "site-check",
						plaintext: "[shared]\ntype = ftp\nhost = ftp.example.org\nuser = deploy\npass = OLD\nexplicit_tls = true\n",
					})
			}
			before := rf.file(t, "secrets/struct-cred.age")
			rf.pipe(tc.value)

			_, errText, code := rf.run(t, "secret", "rotate", "struct-cred")
			if code == 0 {
				t.Fatal("exit 0, want a refusal")
			}
			if !strings.Contains(errText, tc.want) {
				t.Errorf("stderr lacks %q\ngot: %q", tc.want, errText)
			}
			if strings.Contains(errText, tc.value) {
				t.Error("the raw value was printed")
			}
			if rf.encryptCalls != 0 {
				t.Errorf("age was asked to encrypt %d times, want 0", rf.encryptCalls)
			}
			if got := rf.file(t, "secrets/struct-cred.age"); got != before {
				t.Error("the ciphertext was rewritten")
			}
		})
	}
}

// --- rclone checked before the value is read ---

func TestSecretRotateChecksRcloneBeforeReadingTheValue(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "site.rotate", tokenGroup{RegistryAlias: "site.rotate", Service: "forgejo", Account: "site", UITokenName: "site-token", RotationStrategy: "overlap"},
		rotateGroupCredential{
			name: "site-cred", format: "rclone-remote-stanza", system: "control-host", kind: "nixos-host",
			deployedPath: "/root/.config/rclone/rclone.conf", verifyType: "site-check",
			plaintext: "[shared]\ntype = ftp\nhost = ftp.example.org\nuser = deploy\npass = OLD-OBSCURED\nexplicit_tls = true\n",
		})
	t.Setenv("PATH", pathWithoutRclone(t))
	// A reader that fails the test if it is ever read from: proof the
	// rclone check happens strictly before secretRotate asks for the value,
	// not merely before it is used.
	stdin = failIfReadFrom{t}

	_, errText, code := rf.run(t, "secret", "rotate", "site-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "rclone not found on PATH") {
		t.Errorf("stderr = %q", errText)
	}
}

type failIfReadFrom struct{ t *testing.T }

func (f failIfReadFrom) Read([]byte) (int, error) {
	f.t.Helper()
	f.t.Fatal("stdin was read before the rclone-on-PATH check ran")
	return 0, io.EOF
}

// --- Group membership must be unique ---

// TestSecretRotateRefusesWhenAnotherGroupMemberIsAlsoInASecondGroup covers a
// registry bug narrower than "the requested credential is ambiguous":
// resolving 'primary-only' finds exactly one group, but a second member of
// that same group, 'double-booked', is also listed in a different group.
// Rotating primary.rotate would silently re-encrypt double-booked's secret
// without whatever secondary.rotate's own rotation was supposed to
// coordinate, so every member is checked, not only the one named on the
// command line.
func TestSecretRotateRefusesWhenAnotherGroupMemberIsAlsoInASecondGroup(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "primary.rotate", tokenGroup{RegistryAlias: "primary.rotate", Service: "forgejo", Account: "a", UITokenName: "u", RotationStrategy: "overlap"},
		rotateGroupCredential{name: "primary-only", format: "raw-forgejo-token", system: "dev-a", kind: "dev-vm", user: "u", deployedPath: "/home/u/primary-token", verifyType: "forge-token-verify"},
		rotateGroupCredential{name: "double-booked", format: "raw-forgejo-token", system: "dev-a", kind: "dev-vm", user: "u", deployedPath: "/home/u/double-token", verifyType: "forge-token-verify"},
	)
	rf.addGroup(t, "secondary.rotate", tokenGroup{RegistryAlias: "secondary.rotate", Service: "forgejo", Account: "b", UITokenName: "v", RotationStrategy: "overlap"},
		rotateGroupCredential{name: "double-booked", format: "raw-forgejo-token", system: "dev-b", kind: "dev-vm", user: "u", deployedPath: "/home/u/double-token-2", verifyType: "forge-token-verify"},
	)

	_, errText, code := rf.run(t, "secret", "rotate", "primary-only")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	for _, want := range []string{"double-booked", "primary.rotate", "secondary.rotate"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr should name the double-booked credential and both groups; lacks %q\ngot: %q", want, errText)
		}
	}
	if rf.encryptCalls != 0 {
		t.Errorf("age was asked to encrypt %d times, want 0", rf.encryptCalls)
	}
	if got := rf.status(t); got != "" {
		t.Errorf("tree is not clean after a refusal:\n%s", got)
	}
}

// --- The branch is re-checked right before writing ---

// branchSwitchingReader fires do() the first time it is read from, then
// serves data. It simulates another process switching the secrets checkout
// to a different branch during the window rotate spends blocked reading the
// value — the one moment between the initial gate and the write this
// command cannot otherwise observe in a test.
type branchSwitchingReader struct {
	data  []byte
	fired bool
	do    func()
}

func (r *branchSwitchingReader) Read(p []byte) (int, error) {
	if !r.fired {
		r.fired = true
		r.do()
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, io.EOF
	}
	return n, nil
}

func TestSecretRotateRefusesWhenTheBranchChangedWhileWaitingForTheValue(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "raw.rotate", tokenGroup{RegistryAlias: "raw.rotate", Service: "forgejo", Account: "agent", UITokenName: "agent-token", RotationStrategy: "overlap"},
		rotateGroupCredential{name: "raw-cred", format: "raw-forgejo-token", system: "dev-a", kind: "dev-vm", user: "testuser", deployedPath: "/home/testuser/.config/git/forgejo-token", verifyType: "forge-token-verify"})
	before := rf.file(t, "secrets/raw-cred.age")
	stdin = &branchSwitchingReader{
		data: []byte("value\n"),
		do:   func() { gitRun(t, rf.checkout, "switch", "-q", "-c", "agent/hijacked") },
	}

	_, errText, code := rf.run(t, "secret", "rotate", "raw-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "moved from branch 'agent/landing' to 'agent/hijacked'") {
		t.Errorf("stderr = %q", errText)
	}
	if rf.checkCalls != 0 {
		t.Errorf("flake check ran %d times, want 0", rf.checkCalls)
	}
	if got := rf.file(t, "secrets/raw-cred.age"); got != before {
		t.Error("the ciphertext was written despite the branch change")
	}
	if got := gitRun(t, rf.checkout, "log", "-1", "--format=%s", "agent/hijacked"); got != "fixture: add raw.rotate" {
		t.Errorf("a commit landed on agent/hijacked: %q", got)
	}
}
