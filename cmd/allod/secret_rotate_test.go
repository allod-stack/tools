//go:build secret

package main

// Tests for 'allod secret rotate'. The pure ports — the registry
// validator, the encoding-mix guard, the commit-subject and rebuild-target
// helpers — are tested directly, the way TestFlipPendingToActive tests
// flipPendingToActive in secret_test.go. The command itself is driven
// through runAllod against secretFixture's real git checkout, the way
// secret_test.go drives create and rekey; rotateFixture adds what a
// registry group needs beyond secretFixture's single credential: a real
// committed ciphertext per group member, since restore-on-failure needs
// real bytes to put back.
//
// Nothing here stages an old plaintext, because rotation no longer reads
// one: a credential's non-secret half is declared in the registry, so the
// only decrypt left in this namespace belongs to 'rekey' and 'migrate'.
//
// The package-level seams these helpers swap are shared mutable state, so no
// test here calls t.Parallel, matching secret_test.go.
//
// Fixture machines, users, and hosts are invented (fixture-host, dev-a,
// fixture-user, example.test); none of them names a real deployment.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- Direct unit tests: the pure ports ---

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
		{Targets: []registryTarget{{System: "dev-b", Kind: "dev-vm"}, {System: "fixture-host", Kind: "nixos-host"}}},
		{Targets: []registryTarget{{System: "dev-b", Kind: "dev-vm"}, {System: "dev-a", Kind: "dev-vm"}}},
	}}
	got := uniqueRebuildTargets(group)
	want := []rebuildTarget{{"dev-a", "dev-vm"}, {"dev-b", "dev-vm"}, {"fixture-host", "nixos-host"}}
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
// cliExit panic die() raises, so validateGroupMetadata,
// selectRotationGroup, and assertUniformGroupEncoding — all written to call
// die() directly, the way lookupSecret already does — can be tested without
// a CLI invocation.
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

func fixtureVerify(command string) json.RawMessage {
	encoded, err := json.Marshal(command)
	if err != nil {
		panic(err)
	}
	return encoded
}

func validRotateGroup() tokenGroup {
	return tokenGroup{
		RegistryAlias:    "g",
		Service:          "forgejo",
		Account:          "acct",
		UITokenName:      "ui-token",
		RotationStrategy: "overlap",
		Credentials: []registryCredential{{
			Credential: "cred", SecretPath: "secrets/cred.age",
			Targets: []registryTarget{{
				System: "dev-a", Kind: "dev-vm", DeployedPath: "/home/fixture-user/token",
				Verify: fixtureVerify("forge token verify < /home/fixture-user/token"),
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
		{"no targets", func(g *tokenGroup) { g.Credentials[0].Targets = nil }, "has no targets"},
		{"bad target kind", func(g *tokenGroup) { g.Credentials[0].Targets[0].Kind = "laptop" }, "unsupported kind 'laptop'"},
		{"local_auth_refresh bad contract", func(g *tokenGroup) {
			g.LocalAuthRefresh = []localAuthRefreshEntry{{Contract: "run-anything", System: "dev-a", LocalUsername: "u", SourceCredential: "cred"}}
		}, "unsupported contract 'run-anything'"},
		{"local_auth_refresh unmatched source", func(g *tokenGroup) {
			g.LocalAuthRefresh = []localAuthRefreshEntry{{Contract: "nixos-netrc-from-root-git-credentials", System: "dev-a", LocalUsername: "u", SourceCredential: "nope"}}
		}, "does not name exactly one credential-store URL target"},
		{"local_auth_refresh source is not a credential-store template", func(g *tokenGroup) {
			g.Credentials[0].Value = &credentialValue{Template: "{secret}"}
			g.Credentials[0].Targets[0].DeployedPath = "/root/.git-credentials"
			g.LocalAuthRefresh = []localAuthRefreshEntry{{Contract: "nixos-netrc-from-root-git-credentials", System: "dev-a", LocalUsername: "u", SourceCredential: "cred"}}
		}, "does not name exactly one credential-store URL target"},
		{"local_auth_refresh legacy source passes", func(g *tokenGroup) {
			g.Credentials[0].Format = "credential-store-url"
			g.Credentials[0].Targets[0].DeployedPath = "/root/.git-credentials"
			g.LocalAuthRefresh = []localAuthRefreshEntry{{Contract: "nixos-netrc-from-root-git-credentials", System: "dev-a", LocalUsername: "u", SourceCredential: "cred"}}
		}, ""},
		{"local_auth_refresh template source passes", func(g *tokenGroup) {
			g.Credentials[0].Value = &credentialValue{Template: "https://fixture-user:{secret}@example.test"}
			g.Credentials[0].Targets[0].DeployedPath = "/root/.git-credentials"
			g.LocalAuthRefresh = []localAuthRefreshEntry{{Contract: "nixos-netrc-from-root-git-credentials", System: "dev-a", LocalUsername: "u", SourceCredential: "cred"}}
		}, ""},
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

func TestAssertUniformGroupEncoding(t *testing.T) {
	plain := []registryCredential{{}, {Value: &credentialValue{Template: "{secret}"}}}
	if code, _ := rotateDies(t, func() { assertUniformGroupEncoding("g", plain) }); code != 0 {
		t.Error("a group with no encodings was refused")
	}
	encoded := []registryCredential{
		{Value: &credentialValue{Template: "a{secret}", Encode: "rclone-obscure"}},
		{Value: &credentialValue{Template: "b{secret}", Encode: "rclone-obscure"}},
	}
	if code, _ := rotateDies(t, func() { assertUniformGroupEncoding("g", encoded) }); code != 0 {
		t.Error("a group sharing one encoding was refused")
	}
	mixed := []registryCredential{
		{Value: &credentialValue{Template: "a{secret}", Encode: "rclone-obscure"}},
		{},
	}
	code, message := rotateDies(t, func() { assertUniformGroupEncoding("mixed.group", mixed) })
	if code == 0 || !strings.Contains(message, "mixes value encodings (none, rclone-obscure)") {
		t.Errorf("code=%d message=%q", code, message)
	}
}

// --- The command, against a real fixture ---

// rotateGroupCredential is one member addGroup adds to a fixture's registry
// group: a real committed ciphertext (so restore-on-failure has real bytes
// to put back) and the registry shape validateGroupMetadata checks.
type rotateGroupCredential struct {
	name, state                   string // state defaults to "active"
	system, kind, deployedPath    string
	verify                        string // the declared verify command
	rawVerify                     json.RawMessage
	user                          string // legacy only
	format                        string // legacy only
	value                         *credentialValue
	skipCiphertext, skipRecipient bool
}

// rotateFixture augments secretFixture with a record of every decrypt
// attempt, so a test can assert that rotation never made one.
type rotateFixture struct {
	*secretFixture
	decryptCalls int
}

func newRotateFixture(t *testing.T) *rotateFixture {
	t.Helper()
	fx := newSecretFixture(t)
	rf := &rotateFixture{secretFixture: fx}
	secretDecrypt = func(_ string, file string) ([]byte, error) {
		rf.decryptCalls++
		return nil, fmt.Errorf("fixture: rotate must not decrypt %s", file)
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
		if !c.skipRecipient {
			rf.recipients[path] = []string{fixtureHostKey, fixtureVMKey}
		}
		verify := c.rawVerify
		if verify == nil && c.verify != "" {
			verify = fixtureVerify(c.verify)
		}
		group.Credentials = append(group.Credentials, registryCredential{
			Credential: c.name, SecretPath: path, Format: c.format, Value: c.value,
			Targets: []registryTarget{{
				System: c.system, Kind: c.kind, User: c.user, DeployedPath: c.deployedPath, Verify: verify,
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

// forgejoGroup is the group shape most fixtures want: a Forgejo service
// group whose metadata validateGroupMetadata accepts.
func forgejoGroup(alias string) tokenGroup {
	return tokenGroup{RegistryAlias: alias, Service: "forgejo", Account: "fixture-user", UITokenName: "fixture-token", RotationStrategy: "overlap"}
}

// installFakeRclone puts a real, minimal 'rclone' on PATH ahead of whatever
// is already there, the way rotate-token's own tests fake it: it answers
// only 'obscure -', reads the password on stdin (never argv), and returns a
// value deterministic in the input so a test can assert the password
// crossed on stdin and landed, obscured, in the rendered stanza.
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
			rf.addGroup(t, "pending.rotate", forgejoGroup("pending.rotate"),
				rotateGroupCredential{name: "pending-cred", state: "pending", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})
		}, "credential 'pending-cred' is 'pending', not 'active'"},
		{"missing ciphertext", "missing-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "missing.rotate", forgejoGroup("missing.rotate"),
				rotateGroupCredential{name: "missing-cred", skipCiphertext: true, system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})
		}, "does not exist while 'missing-cred' is 'active'"},
		{"unsupported metadata", "bogus-cred", func(t *testing.T, rf *rotateFixture) {
			group := forgejoGroup("bogus.rotate")
			group.RotationStrategy = "sometimes"
			rf.addGroup(t, "bogus.rotate", group,
				rotateGroupCredential{name: "bogus-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})
		}, "unsupported metadata"},
		{"mixed encodings", "mixed-encoded", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "mixed.rotate", forgejoGroup("mixed.rotate"),
				rotateGroupCredential{name: "mixed-encoded", system: "fixture-host", kind: "nixos-host", deployedPath: "/root/.config/rclone/rclone.conf", verify: "allod site check",
					value: &credentialValue{Template: fixtureRcloneTemplate, Encode: "rclone-obscure"}},
				rotateGroupCredential{name: "mixed-plain", system: "fixture-host", kind: "nixos-host", deployedPath: "/root/.git-credentials", verify: "sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote https://example.test/fixture/repo.git HEAD",
					value: &credentialValue{Template: "https://fixture-user:{secret}@example.test"}})
		}, "mixes value encodings (none, rclone-obscure)"},
		{"missing verify", "no-verify-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "noverify.rotate", forgejoGroup("noverify.rotate"),
				rotateGroupCredential{name: "no-verify-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token"})
		}, "has no verify command; run 'allod secret migrate no-verify-cred'"},
		{"structured verify on a new-shape credential", "structured-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "structured.rotate", forgejoGroup("structured.rotate"),
				rotateGroupCredential{name: "structured-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token",
					rawVerify: json.RawMessage(`{"type":"forge-token-verify"}`)})
		}, "has structured legacy verify data, not one command string; run 'allod secret migrate structured-cred'"},
		{"multi-line verify", "multiline-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "multiline.rotate", forgejoGroup("multiline.rotate"),
				rotateGroupCredential{name: "multiline-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token",
					verify: "allod site check\nrm -rf /"})
		}, "has a verify command spanning more than one line"},
		{"template with no placeholder", "bad-template-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "badtemplate.rotate", forgejoGroup("badtemplate.rotate"),
				rotateGroupCredential{name: "bad-template-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "allod site check",
					value: &credentialValue{Template: "https://fixture-user:token@example.test"}})
		}, "value.template must contain exactly one {secret} placeholder"},
		{"unknown encoder", "bad-encoder-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "badencoder.rotate", forgejoGroup("badencoder.rotate"),
				rotateGroupCredential{name: "bad-encoder-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "allod site check",
					value: &credentialValue{Template: "{secret}", Encode: "rot13"}})
		}, `value.encode "rot13" is not exported by lib.credentialEncodings`},
		{"dirty tree", "dirty-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "dirty.rotate", forgejoGroup("dirty.rotate"),
				rotateGroupCredential{name: "dirty-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})
			if err := os.WriteFile(filepath.Join(rf.checkout, "secrets", "leftover.age"), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}, "uncommitted or untracked changes"},
		{"default branch", "branch-cred", func(t *testing.T, rf *rotateFixture) {
			rf.addGroup(t, "branch.rotate", forgejoGroup("branch.rotate"),
				rotateGroupCredential{name: "branch-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})
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
			if rf.decryptCalls != 0 {
				t.Errorf("age was asked to decrypt %d times, want 0", rf.decryptCalls)
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

// --- Legacy entries are named, not rotated ---

func TestSecretRotateNamesMigrateForALegacyCredential(t *testing.T) {
	cases := []struct {
		name       string
		credential string
		want       string
	}{
		{"the selected credential is legacy", "legacy-cred", "credential 'legacy-cred' uses legacy format 'credential-store-url'; run 'allod secret migrate legacy-cred' before rotating it"},
		{"another group member is legacy", "new-shape-cred", "rotation registry group 'legacy.rotate' also lists legacy credential 'legacy-cred' (format 'credential-store-url'); run 'allod secret migrate legacy-cred' before rotating this group"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rf := newRotateFixture(t)
			rf.addGroup(t, "legacy.rotate", forgejoGroup("legacy.rotate"),
				rotateGroupCredential{
					name: "new-shape-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token",
					verify: "forge token verify < /home/fixture-user/token",
					value:  &credentialValue{Template: "https://fixture-user:{secret}@example.test"},
				},
				rotateGroupCredential{
					name: "legacy-cred", format: "credential-store-url", system: "fixture-host", kind: "nixos-host",
					deployedPath: "/root/.git-credentials", user: "fixture-user",
					rawVerify: json.RawMessage(`{"type":"git-ls-remote","repo_url":"https://example.test/fixture/repo.git"}`),
				})
			before := rf.head(t)
			rf.pipe("tok-fixture\n")

			_, errText, code := rf.run(t, "secret", "rotate", tc.credential)
			if code == 0 {
				t.Fatal("exit 0, want a refusal")
			}
			if !strings.Contains(errText, tc.want) {
				t.Errorf("stderr lacks %q\ngot: %q", tc.want, errText)
			}
			if rf.encryptCalls != 0 || rf.decryptCalls != 0 {
				t.Errorf("encrypt=%d decrypt=%d, want 0 and 0", rf.encryptCalls, rf.decryptCalls)
			}
			if got := rf.head(t); got != before {
				t.Error("a commit was made")
			}
		})
	}
}

// TestSecretRotateRefusesACredentialCarryingBothShapes covers the
// coexistence rule from the other side: a registry may hold legacy
// credentials and migrated ones side by side, but one credential carrying
// both 'format' and 'value' has not passed the registry's own check, and
// guessing which half is authoritative would encrypt a value into the wrong
// text.
func TestSecretRotateRefusesACredentialCarryingBothShapes(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "mixed.rotate", forgejoGroup("mixed.rotate"),
		rotateGroupCredential{
			name: "mixed-cred", format: "credential-store-url", system: "dev-a", kind: "dev-vm",
			deployedPath: "/root/.git-credentials", verify: "allod site check",
			value: &credentialValue{Template: "https://fixture-user:{secret}@example.test"},
		})
	rf.pipe("tok-fixture\n")

	_, errText, code := rf.run(t, "secret", "rotate", "mixed-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "carries both a legacy 'format' and a declared 'value'") {
		t.Errorf("stderr = %q", errText)
	}
	if rf.encryptCalls != 0 {
		t.Errorf("age was asked to encrypt %d times, want 0", rf.encryptCalls)
	}
}

// TestSecretRotateAcceptsARegistryHoldingBothShapes is the positive half:
// a registry where one group is still legacy and another has migrated
// decodes and rotates the migrated one without complaint.
func TestSecretRotateAcceptsARegistryHoldingBothShapes(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "legacy.rotate", forgejoGroup("legacy.rotate"),
		rotateGroupCredential{
			name: "legacy-cred", format: "credential-store-url", system: "fixture-host", kind: "nixos-host",
			deployedPath: "/root/.git-credentials", user: "fixture-user",
			rawVerify: json.RawMessage(`{"type":"git-ls-remote","repo_url":"https://example.test/fixture/repo.git"}`),
		})
	rf.addGroup(t, "new.rotate", forgejoGroup("new.rotate"),
		rotateGroupCredential{
			name: "new-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token",
			verify: "forge token verify < /home/fixture-user/token",
			value:  &credentialValue{Template: "https://fixture-user:{secret}@example.test"},
		})
	rf.pipe("tok-fixture")

	_, errText, code := rf.run(t, "secret", "rotate", "new-cred")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if string(rf.lastPlaintext) != "https://fixture-user:tok-fixture@example.test" {
		t.Errorf("plaintext = %q", rf.lastPlaintext)
	}
	if got := rf.commitFiles(t); got != "secrets/new-cred.age" {
		t.Errorf("commit files = %q, want only the rotated credential", got)
	}
}

// --- Rendering ---

// TestSecretRotateRendersEachDeclaredValueShape drives the same table
// TestSecretCreateRendersEachDeclaredValueShape drives through 'create', so
// both commands are pinned to the same bytes rather than each to its own.
func TestSecretRotateRendersEachDeclaredValueShape(t *testing.T) {
	for _, tc := range credentialRenderCases() {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsRclone {
				installFakeRclone(t)
			}
			rf := newRotateFixture(t)
			rf.addGroup(t, "render.rotate", forgejoGroup("render.rotate"),
				rotateGroupCredential{
					name: "render-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token",
					verify: "forge token verify < /home/fixture-user/token", value: tc.value,
				})
			rf.pipe(tc.secret)

			out, errText, code := rf.run(t, "secret", "rotate", "render-cred")
			if code != 0 {
				t.Fatalf("exit %d, stderr: %s", code, errText)
			}
			if string(rf.lastPlaintext) != tc.want {
				t.Errorf("plaintext handed to age = %q, want %q", rf.lastPlaintext, tc.want)
			}
			if rf.decryptCalls != 0 {
				t.Errorf("rotate decrypted %d times, want 0", rf.decryptCalls)
			}
			if strings.Contains(out+errText, tc.secret) || strings.Contains(out+errText, tc.want) {
				t.Error("the value or the rendered plaintext was printed")
			}
		})
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

// TestSecretRotateRendersEveryGroupMemberFromOneValue is the group contract:
// one value on stdin, one rendering per member from that member's own
// declared template, one commit naming every path.
func TestSecretRotateRendersEveryGroupMemberFromOneValue(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "shared.rotate", forgejoGroup("shared.rotate"),
		rotateGroupCredential{
			name: "cred-a", system: "fixture-host", kind: "nixos-host", deployedPath: "/root/.git-credentials",
			verify: "sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote https://example.test/fixture/repo.git HEAD",
			value:  &credentialValue{Template: "https://fixture-user:{secret}@example.test"},
		},
		rotateGroupCredential{
			name: "cred-b", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token",
			verify: "forge token verify < /home/fixture-user/token",
		})

	calls, restoreEncrypt := encryptRecorder()
	defer restoreEncrypt()
	rf.pipe("tok-fixture")

	out, errText, code := rf.run(t, "secret", "rotate", "cred-a")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if len(*calls) != 2 {
		t.Fatalf("age was asked to encrypt %d times, want 2", len(*calls))
	}
	if got := string((*calls)[0][1].([]byte)); got != "https://fixture-user:tok-fixture@example.test" {
		t.Errorf("cred-a plaintext = %q", got)
	}
	if got := string((*calls)[1][1].([]byte)); got != "tok-fixture" {
		t.Errorf("cred-b plaintext = %q, want the value verbatim", got)
	}
	if got, want := gitRun(t, rf.checkout, "show", "--name-only", "--format=", "HEAD"), "secrets/cred-a.age\nsecrets/cred-b.age"; got != want {
		t.Errorf("commit files = %q, want %q", got, want)
	}
	if got, want := gitRun(t, rf.checkout, "log", "-1", "--format=%s"), "rotate shared.rotate Forgejo token"; got != want {
		t.Errorf("commit subject = %q, want %q", got, want)
	}
	for _, want := range []string{"Wrote secrets/cred-a.age, encrypted to 2 recipients", "Wrote secrets/cred-b.age, encrypted to 2 recipients"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q\ngot: %q", want, out)
		}
	}
	if got := rf.originHead(t, "agent/landing"); got != rf.head(t) {
		t.Errorf("origin agent/landing = %q, want the new HEAD %q", got, rf.head(t))
	}
	if strings.Contains(out+errText, "tok-fixture") {
		t.Error("the value was printed")
	}
}

func TestSecretRotateMissingRcloneIsClearFailure(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "site.rotate", forgejoGroup("site.rotate"),
		rotateGroupCredential{
			name: "site-cred", system: "fixture-host", kind: "nixos-host",
			deployedPath: "/root/.config/rclone/rclone.conf", verify: "allod site check",
			value: &credentialValue{Template: fixtureRcloneTemplate, Encode: "rclone-obscure"},
		})
	before := rf.file(t, "secrets/site-cred.age")
	rf.pipe("pw-fixture\n")
	t.Setenv("PATH", pathWithoutRclone(t))

	_, errText, code := rf.run(t, "secret", "rotate", "site-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "rclone not found on PATH") {
		t.Errorf("stderr = %q", errText)
	}
	if strings.Contains(errText, "pw-fixture") {
		t.Error("the password was printed")
	}
	if got := rf.file(t, "secrets/site-cred.age"); got != before {
		t.Error("the ciphertext was rewritten")
	}
}

// --- Dry run ---

func TestSecretRotateDryRunPrintsStepsWithoutWriting(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "shared.rotate", forgejoGroup("shared.rotate"),
		rotateGroupCredential{
			name: "cred-a", system: "fixture-host", kind: "nixos-host", deployedPath: "/root/.git-credentials",
			verify: "sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote https://example.test/fixture/repo.git HEAD",
			value:  &credentialValue{Template: "https://fixture-user:{secret}@example.test"},
		},
		rotateGroupCredential{
			name: "cred-b", system: "dev-a", kind: "dev-vm", deployedPath: "/root/.git-credentials",
			verify: "sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote https://example.test/fixture/repo.git HEAD",
			value:  &credentialValue{Template: "https://fixture-user:{secret}@example.test"},
		})
	before := rf.head(t)
	stdin = failIfReadFrom{t}

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
		"fixture-host (nixos-host)",
		"dev-a (dev-vm)",
		`A live run commits secrets/cred-a.age secrets/cred-b.age`,
		`as "rotate shared.rotate Forgejo token" and pushes agent/landing to origin; merging stays your act.`,
		"nix flake update secrets",
		"sudo nixos-rebuild switch --flake ~/work/allod/deploy#fixture-host",
		"rebuild-vm-from-host dev-a",
		"GIT_TERMINAL_PROMPT=0 git ls-remote https://example.test/fixture/repo.git HEAD",
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
	rf.addGroup(t, "raw.rotate", forgejoGroup("raw.rotate"),
		rotateGroupCredential{name: "raw-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})
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

// --- Printed steps: value shape, service wording, refresh-local-auth ---

func TestSecretRotatePrintedStepsVaryByServiceAndLocalAuthRefresh(t *testing.T) {
	installFakeRclone(t)
	installFakeRefreshLocalAuth(t)
	rf := newRotateFixture(t)
	rf.addGroup(t, "raw.rotate", forgejoGroup("raw.rotate"),
		rotateGroupCredential{name: "raw-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})
	noneGroup := forgejoGroup("none.rotate")
	noneGroup.Service, noneGroup.Account, noneGroup.UITokenName = "none", "", ""
	rf.addGroup(t, "none.rotate", noneGroup,
		rotateGroupCredential{name: "none-cred", system: "dev-b", kind: "dev-vm", deployedPath: "/home/fixture-user/other-token", verify: "allod site check",
			value: &credentialValue{Template: fixtureRcloneTemplate, Encode: "rclone-obscure"}})
	refreshGroup := forgejoGroup("refresh.rotate")
	refreshGroup.RotationStrategy = "in-place"
	refreshGroup.LocalAuthRefresh = []localAuthRefreshEntry{{
		Contract: "nixos-netrc-from-root-git-credentials", System: "fixture-host", LocalUsername: "fixture-user", SourceCredential: "refresh-cred",
	}}
	rf.addGroup(t, "refresh.rotate", refreshGroup, rotateGroupCredential{
		name: "refresh-cred", system: "fixture-host", kind: "nixos-host", deployedPath: "/root/.git-credentials",
		verify: "sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote https://example.test/fixture/repo.git HEAD",
		value:  &credentialValue{Template: "https://fixture-user:{secret}@example.test"},
	})

	_, forgejoOut, code := rf.run(t, "secret", "rotate", "raw-cred", "--dry-run")
	if code != 0 {
		t.Fatalf("raw.rotate dry run: exit %d\n%s", code, forgejoOut)
	}
	if !strings.Contains(forgejoOut, "Forgejo group: raw.rotate") || !strings.Contains(forgejoOut, "Forgejo token: fixture-user/fixture-token") {
		t.Errorf("forgejo header missing:\n%s", forgejoOut)
	}
	if !strings.Contains(forgejoOut, "  - secrets/raw-cred.age (raw-cred, plain)") {
		t.Errorf("a credential with no declared value did not print as plain:\n%s", forgejoOut)
	}
	if !strings.Contains(forgejoOut, "Forgejo UI token 'fixture-token' while logged in as 'fixture-user'") {
		t.Errorf("forgejo revocation wording missing:\n%s", forgejoOut)
	}
	if strings.Contains(forgejoOut, "refresh-local-auth") {
		t.Errorf("a group with no local_auth_refresh printed the refresh step:\n%s", forgejoOut)
	}

	_, noneOut, code := rf.run(t, "secret", "rotate", "none-cred", "--dry-run")
	if code != 0 {
		t.Fatalf("none.rotate dry run: exit %d\n%s", code, noneOut)
	}
	if !strings.Contains(noneOut, "Registry group: none.rotate") || !strings.Contains(noneOut, "Service: none (not a Forgejo token)") {
		t.Errorf("none header missing:\n%s", noneOut)
	}
	if !strings.Contains(noneOut, "  - secrets/none-cred.age (none-cred, template, encode rclone-obscure)") {
		t.Errorf("an encoded template did not print its encoding:\n%s", noneOut)
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
		t.Fatalf("refresh.rotate dry run: exit %d\n%s", code, refreshOut)
	}
	if !strings.Contains(refreshOut, "  - secrets/refresh-cred.age (refresh-cred, template)") {
		t.Errorf("a template with no encoding did not print as template:\n%s", refreshOut)
	}
	if !strings.Contains(refreshOut, "refresh-local-auth --group refresh.rotate") {
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
	rf.pipe("tok-fixture\n")
	_, liveErrText, code := rf.run(t, "secret", "rotate", "refresh-cred")
	if code != 0 {
		t.Fatalf("refresh.rotate live run: exit %d, stderr: %s", code, liveErrText)
	}
	if !strings.Contains(liveErrText, "Refresh declared local auth from the rotated encrypted secret before any git push, flake-lock update, or rebuild fetch:\n   refresh-local-auth --group refresh.rotate") {
		t.Errorf("a live run did not print the refresh step as an instruction:\n%s", liveErrText)
	}
	if strings.Contains(liveErrText, "After the real run lands") {
		t.Errorf("a live run used the dry-run phrasing:\n%s", liveErrText)
	}
}

// TestSecretRotateRefusesALocalAuthRefreshGroupWithoutRefreshLocalAuthOnPATH
// pins the same PATH gate migrate has: a group with a local_auth_refresh
// entry is refused, before the value is ever read, when 'refresh-local-auth'
// cannot be found on PATH — on a dry run and on a live run alike, since a
// live run would otherwise land a rotation the operator could not finish.
func TestSecretRotateRefusesALocalAuthRefreshGroupWithoutRefreshLocalAuthOnPATH(t *testing.T) {
	rf := newRotateFixture(t)
	group := forgejoGroup("refresh.rotate")
	group.LocalAuthRefresh = []localAuthRefreshEntry{{
		Contract: "nixos-netrc-from-root-git-credentials", System: "fixture-host", LocalUsername: "fixture-user", SourceCredential: "refresh-cred",
	}}
	rf.addGroup(t, "refresh.rotate", group, rotateGroupCredential{
		name: "refresh-cred", system: "fixture-host", kind: "nixos-host", deployedPath: "/root/.git-credentials",
		verify: "sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote https://example.test/fixture/repo.git HEAD",
		value:  &credentialValue{Template: "https://fixture-user:{secret}@example.test"},
	})
	beforeHead := rf.head(t)
	t.Setenv("PATH", pathWithoutRefreshLocalAuth(t))

	for _, args := range [][]string{
		{"secret", "rotate", "refresh-cred", "--dry-run"},
		{"secret", "rotate", "refresh-cred"},
	} {
		_, errText, code := rf.run(t, args...)
		if code == 0 {
			t.Fatalf("%v: exit 0, want a refusal", args)
		}
		if !strings.Contains(errText, "rotation registry group 'refresh.rotate' needs a local auth refresh after rotation") {
			t.Errorf("%v: stderr = %q", args, errText)
		}
		if !strings.Contains(errText, "'refresh-local-auth' was not found on PATH") {
			t.Errorf("%v: stderr = %q", args, errText)
		}
		if !strings.Contains(errText, "this host's nexus pin predates allod/nexus#52") {
			t.Errorf("%v: stderr = %q", args, errText)
		}
		if rf.encryptCalls != 0 || rf.decryptCalls != 0 {
			t.Errorf("%v: encrypt=%d decrypt=%d, want 0 and 0 — refused before any value was read", args, rf.encryptCalls, rf.decryptCalls)
		}
	}
	if got := rf.head(t); got != beforeHead {
		t.Error("a commit was made despite the refusal")
	}
	if got := rf.status(t); got != "" {
		t.Errorf("tree is not clean after a refusal:\n%s", got)
	}
}

// TestSecretRotateWithoutLocalAuthRefreshIgnoresARefreshLocalAuthGate pins
// that the new PATH gate is scoped to groups that actually carry a
// local_auth_refresh entry: an ordinary group still rotates on the exact
// PATH the previous test shows refuses one that does carry one.
func TestSecretRotateWithoutLocalAuthRefreshIgnoresARefreshLocalAuthGate(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "plain.rotate", forgejoGroup("plain.rotate"),
		rotateGroupCredential{name: "plain-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})
	rf.pipe("tok-fixture\n")
	t.Setenv("PATH", pathWithoutRefreshLocalAuth(t))

	_, errText, code := rf.run(t, "secret", "rotate", "plain-cred")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
}

// --- Verification ---

// TestPrintVerificationQuotesRemoteCommandsAndDetectsTheLocalHost pins the
// exact printed text for both halves of the local/remote rule, with a
// single quote embedded in the command and in the system name so the POSIX
// quoting is exercised rather than assumed.
func TestPrintVerificationQuotesRemoteCommandsAndDetectsTheLocalHost(t *testing.T) {
	previous := secretHostName
	defer func() { secretHostName = previous }()
	secretHostName = func() (string, error) { return "fixture-host", nil }

	group := tokenGroup{Credentials: []registryCredential{{
		Credential: "cred",
		Targets: []registryTarget{
			{System: "fixture-host", Kind: "nixos-host", Verify: fixtureVerify("allod site check")},
			{System: "dev-a", Kind: "dev-vm", Verify: fixtureVerify("forge token verify < /home/fixture-user/token")},
			{System: "dev-b", Kind: "dev-vm", Verify: fixtureVerify(`allod site check --note 'it'`)},
			{System: `dev'c`, Kind: "dev-vm", Verify: fixtureVerify("tailscale status --peers=false")},
		},
	}}}
	var out bytes.Buffer
	printVerification(&out, group)
	want := "\n--- Verification ---\n" +
		"fixture-host (nixos-host):\n" +
		"   allod site check\n" +
		"dev-a (dev-vm):\n" +
		"   ssh 'dev-a' 'forge token verify < /home/fixture-user/token'\n" +
		"dev-b (dev-vm):\n" +
		`   ssh 'dev-b' 'allod site check --note '\''it'\'''` + "\n" +
		`dev'c (dev-vm):` + "\n" +
		`   ssh 'dev'\''c' 'tailscale status --peers=false'` + "\n"
	if got := out.String(); got != want {
		t.Errorf("printVerification wrote\n%q\nwant\n%q", got, want)
	}
}

// TestSecretRotatePrintsVerificationThroughTheHostNameSeam drives the same
// rule through the command, so the seam is the one the command consults.
func TestSecretRotatePrintsVerificationThroughTheHostNameSeam(t *testing.T) {
	previous := secretHostName
	defer func() { secretHostName = previous }()
	secretHostName = func() (string, error) { return "dev-a", nil }

	rf := newRotateFixture(t)
	rf.addGroup(t, "verify.rotate", forgejoGroup("verify.rotate"),
		rotateGroupCredential{name: "local-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"},
		rotateGroupCredential{name: "remote-cred", system: "dev-b", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})

	_, errText, code := rf.run(t, "secret", "rotate", "local-cred", "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if !strings.Contains(errText, "dev-a (dev-vm):\n   forge token verify < /home/fixture-user/token\n") {
		t.Errorf("a target on this machine was not printed bare:\n%s", errText)
	}
	if !strings.Contains(errText, "dev-b (dev-vm):\n   ssh 'dev-b' 'forge token verify < /home/fixture-user/token'\n") {
		t.Errorf("a remote target was not ssh-wrapped:\n%s", errText)
	}
}

// --- Landing ---

func TestSecretRotateRestoresAllCiphertextsWhenChecksFail(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "shared.rotate", forgejoGroup("shared.rotate"),
		rotateGroupCredential{name: "cred-a", system: "fixture-host", kind: "nixos-host", deployedPath: "/root/.git-credentials", verify: "allod site check",
			value: &credentialValue{Template: "https://fixture-user:{secret}@example.test"}},
		rotateGroupCredential{name: "cred-b", system: "dev-a", kind: "dev-vm", deployedPath: "/root/.git-credentials", verify: "allod site check",
			value: &credentialValue{Template: "https://fixture-user:{secret}@example.test"}})
	beforeA, beforeB := rf.file(t, "secrets/cred-a.age"), rf.file(t, "secrets/cred-b.age")
	before := rf.head(t)
	rf.checkStatus = 3
	rf.pipe("tok-fixture")

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
	rf.addGroup(t, "raw.rotate", forgejoGroup("raw.rotate"),
		rotateGroupCredential{name: "raw-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})
	before := rf.file(t, "secrets/raw-cred.age")
	rf.pipe("tok-fixture\n")
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
	rf.addGroup(t, "shared.rotate", forgejoGroup("shared.rotate"),
		rotateGroupCredential{name: "cred-a", system: "fixture-host", kind: "nixos-host", deployedPath: "/root/.git-credentials", verify: "allod site check",
			value: &credentialValue{Template: "https://fixture-user:{secret}@example.test"}},
		rotateGroupCredential{name: "cred-b", system: "dev-a", kind: "dev-vm", deployedPath: "/root/.git-credentials", verify: "allod site check",
			value: &credentialValue{Template: "https://fixture-user:{secret}@example.test"}})
	beforeA, beforeB := rf.file(t, "secrets/cred-a.age"), rf.file(t, "secrets/cred-b.age")
	installFailingPreCommit(t, rf.secretFixture)
	rf.pipe("tok-fixture")

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
	rf.addGroup(t, "raw.rotate", forgejoGroup("raw.rotate"),
		rotateGroupCredential{name: "raw-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})
	before := rf.head(t)
	rf.pipe("tok-fixture\n")
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

func TestSecretRotateLeavesNoTemporaryFilesBehind(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "raw.rotate", forgejoGroup("raw.rotate"),
		rotateGroupCredential{name: "raw-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})
	rf.pipe("tok-fixture\n")

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

// --- rclone checked before the value is read ---

func TestSecretRotateChecksRcloneBeforeReadingTheValue(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "site.rotate", forgejoGroup("site.rotate"),
		rotateGroupCredential{
			name: "site-cred", system: "fixture-host", kind: "nixos-host",
			deployedPath: "/root/.config/rclone/rclone.conf", verify: "allod site check",
			value: &credentialValue{Template: fixtureRcloneTemplate, Encode: "rclone-obscure"},
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
	f.t.Fatal("stdin was read before the gates that run without it")
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
	rf.addGroup(t, "primary.rotate", forgejoGroup("primary.rotate"),
		rotateGroupCredential{name: "primary-only", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/primary-token", verify: "forge token verify < /home/fixture-user/primary-token"},
		rotateGroupCredential{name: "double-booked", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/double-token", verify: "forge token verify < /home/fixture-user/double-token"},
	)
	rf.addGroup(t, "secondary.rotate", forgejoGroup("secondary.rotate"),
		rotateGroupCredential{name: "double-booked", system: "dev-b", kind: "dev-vm", deployedPath: "/home/fixture-user/double-token-2", verify: "forge token verify < /home/fixture-user/double-token-2"},
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
	rf.addGroup(t, "raw.rotate", forgejoGroup("raw.rotate"),
		rotateGroupCredential{name: "raw-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token", verify: "forge token verify < /home/fixture-user/token"})
	before := rf.file(t, "secrets/raw-cred.age")
	stdin = &branchSwitchingReader{
		data: []byte("tok-fixture\n"),
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

// --- The encoder's own output never carries the candidate out ---

// installEchoingRclone puts an 'rclone' on PATH that copies its stdin to
// stderr and fails, which is what a broken build, a debug build, or an
// 'rclone' put there by somebody else can do with a value handed to it on
// stdin. Nothing the command prints may contain that value.
func installEchoingRclone(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "rclone"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestSecretRotateNeverPrintsAnEncodersOutput(t *testing.T) {
	installEchoingRclone(t)
	rf := newRotateFixture(t)
	rf.addGroup(t, "obscured.rotate", forgejoGroup("obscured.rotate"),
		rotateGroupCredential{name: "obscured-cred", system: "fixture-host", kind: "nixos-host",
			deployedPath: "/root/.config/rclone/rclone.conf", verify: "allod site check",
			value: &credentialValue{Template: fixtureRcloneTemplate, Encode: "rclone-obscure"}})
	const candidate = "correct-horse-fixture"
	rf.pipe(candidate)

	out, errText, code := rf.run(t, "secret", "rotate", "obscured-cred")
	if code == 0 {
		t.Fatal("exit 0 despite a failing encoder")
	}
	if strings.Contains(out, candidate) || strings.Contains(errText, candidate) {
		t.Errorf("the candidate value reached the output\nstdout: %q\nstderr: %q", out, errText)
	}
	if want := "'rclone obscure -' failed (exit status 1)"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	if rf.encryptCalls != 0 {
		t.Errorf("age was asked to encrypt %d times, want 0", rf.encryptCalls)
	}
	if got := rf.status(t); got != "" {
		t.Errorf("tree is not clean after a refusal:\n%s", got)
	}
}

// --- One entry per credential, in one group ---

// TestSecretRotateRefusesADuplicateEntryInOneGroup covers what counting
// groups instead of entries used to allow: two entries of the same name in
// one group are one group, and rotate would render the new value through
// whichever declaration came first.
func TestSecretRotateRefusesADuplicateEntryInOneGroup(t *testing.T) {
	rf := newRotateFixture(t)
	rf.addGroup(t, "dup.rotate", forgejoGroup("dup.rotate"),
		rotateGroupCredential{name: "dup-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token",
			verify: "forge token verify < /home/fixture-user/token"})
	// A second entry with the same name, the shape a hand edit produces.
	group := rf.registry["dup.rotate"]
	group.Credentials = append(group.Credentials, group.Credentials[0])
	rf.registry["dup.rotate"] = group
	rf.pipe("tok-fixture\n")

	_, errText, code := rf.run(t, "secret", "rotate", "dup-cred")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "credential 'dup-cred' is listed 2 times in rotation registry group 'dup.rotate'"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	if rf.encryptCalls != 0 || rf.decryptCalls != 0 {
		t.Errorf("encrypt=%d decrypt=%d, want 0 and 0", rf.encryptCalls, rf.decryptCalls)
	}
	if got := rf.status(t); got != "" {
		t.Errorf("tree is not clean after a refusal:\n%s", got)
	}
}

// TestSecretRotateSendsAPlainLegacyEntryToARegistryEdit pins the other half
// of the legacy refusal: 'migrate' has no container to take apart for a
// plain legacy value, so naming it would send the operator on a hop that
// ends in a refusal. The command that refuses says what actually works.
func TestSecretRotateSendsAPlainLegacyEntryToARegistryEdit(t *testing.T) {
	cases := []struct {
		name       string
		credential string
		want       string
	}{
		{"the selected credential is legacy", "plain-legacy-cred",
			"credential 'plain-legacy-cred' uses legacy format 'raw-forgejo-token'; it is a plain legacy value with no container to take apart"},
		{"another group member is legacy", "new-shape-cred",
			"rotation registry group 'plain.rotate' also lists legacy credential 'plain-legacy-cred' (format 'raw-forgejo-token'); it is a plain legacy value with no container to take apart"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rf := newRotateFixture(t)
			rf.addGroup(t, "plain.rotate", forgejoGroup("plain.rotate"),
				rotateGroupCredential{
					name: "new-shape-cred", system: "dev-a", kind: "dev-vm", deployedPath: "/home/fixture-user/token",
					verify: "forge token verify < /home/fixture-user/token",
					value:  &credentialValue{Template: "https://fixture-user:{secret}@example.test"},
				},
				rotateGroupCredential{
					name: "plain-legacy-cred", format: "raw-forgejo-token", system: "fixture-host", kind: "nixos-host",
					deployedPath: "/root/.token",
					rawVerify:    json.RawMessage(`{"type":"forge-token-verify"}`),
				})
			rf.pipe("tok-fixture\n")

			_, errText, code := rf.run(t, "secret", "rotate", tc.credential)
			if code == 0 {
				t.Fatal("exit 0, want a refusal")
			}
			if !strings.Contains(errText, tc.want) {
				t.Errorf("stderr lacks %q\ngot: %q", tc.want, errText)
			}
			if strings.Contains(errText, "allod secret migrate") {
				t.Errorf("the refusal names migrate for a format migrate refuses\ngot: %q", errText)
			}
			if want := "drops its 'format'"; !strings.Contains(errText, want) {
				t.Errorf("stderr lacks the registry edit it should name\ngot: %q", errText)
			}
		})
	}
}
