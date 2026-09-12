//go:build secret

package main

// Tests for the secret namespace. They run the CLI in process through
// runAllod against a real git repository — a checkout on a landing branch
// with a bare origin — while nix, age, the flake checks, and the terminal
// are replaced by the seams in secret.go. Git is real because the commit
// and push are the landing; everything else is a fake that records what it
// was asked.
//
// This file is compiled only with -tags secret, so it is also half the
// proof that the namespace exists exactly in the builds that asked for it;
// secret_absent_test.go is the other half.
//
// The package-level seams these helpers swap are shared mutable state, so no
// test here calls t.Parallel.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	fixtureHostKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHostKeyFixtureHostKeyFixtureHostKeyFixture host"
	fixtureVMKey    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIVmKeyFixtureVmKeyFixtureVmKeyFixtureVmKeyF dev-a"
	fixtureOtherKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOtherKeyFixtureOtherKeyFixtureOtherKeyFix other"
)

// fixtureCredentialsNix is the literal file the command edits. Two entries:
// one pending with no ciphertext, one active with one.
const fixtureCredentialsNix = `let
  mk = name: { inherit name; };
  generated = mk "generated";
in
{
  new-token = {
    name           = "new-token";
    kind           = "agent";
    owner          = "allod-agent";
    public_key     = null;
    consumers      = [
      { type = "agenix"; repo = "secrets"; secret = "secrets/new-token.age"; }
    ];
    rotation_state = "pending";
  };

  old-token = {
    name           = "old-token";
    kind           = "agent";
    owner          = "allod-agent";
    public_key     = null;
    consumers      = [
      { type = "agenix"; repo = "secrets"; secret = "secrets/old-token.age"; }
    ];
    rotation_state = "active";
  };
}
`

const fixtureOldCiphertext = "age-encryption.org/v1\n-> fixture old\n--- old\n"

// secretFixture is one secrets checkout plus the fakes behind every seam.
type secretFixture struct {
	checkout string
	origin   string
	identity string

	credentials map[string]credentialEntry
	registry    map[string]tokenGroup
	recipients  map[string][]string

	encryptCalls   int
	lastRecipients []string
	lastPlaintext  []byte
	encryptErr     error
	encryptOutput  []byte

	decryptCalls  int
	decryptResult []byte
	decryptErr    error

	encodings []string

	checkCalls  int
	checkStatus int

	terminal      bool
	terminalValue string
	terminalErr   error
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newSecretFixture builds the checkout on branch agent/landing with origin
// tracking master, installs every seam, and points AGE_IDENTITY at a
// fixture identity whose .pub is the host key secrets.nix lists.
func newSecretFixture(t *testing.T) *secretFixture {
	t.Helper()
	root := t.TempDir()
	fx := &secretFixture{
		checkout: filepath.Join(root, "secrets"),
		origin:   filepath.Join(root, "origin.git"),
		identity: filepath.Join(root, "host"),
		credentials: map[string]credentialEntry{
			"new-token": {Name: "new-token", RotationState: "pending", Consumers: []credentialConsumer{
				{Type: "agenix", Repo: "secrets", Secret: "secrets/new-token.age"},
			}},
			"old-token": {Name: "old-token", RotationState: "active", Consumers: []credentialConsumer{
				{Type: "agenix", Repo: "secrets", Secret: "secrets/old-token.age"},
			}},
		},
		registry: map[string]tokenGroup{
			"dev-a.git": {Credentials: []registryCredential{
				{Credential: "new-token", SecretPath: "secrets/new-token.age"},
				{Credential: "old-token", SecretPath: "secrets/old-token.age"},
			}},
		},
		recipients: map[string][]string{
			"secrets/new-token.age": {fixtureHostKey, fixtureVMKey},
			"secrets/old-token.age": {fixtureHostKey, fixtureVMKey, fixtureOtherKey},
		},
		decryptResult: []byte("old value\n"),
		encodings:     []string{"rclone-obscure"},
	}

	if err := os.WriteFile(fx.identity, []byte("fixture private key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fx.identity+".pub", []byte(fixtureHostKey+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGE_IDENTITY", fx.identity)
	t.Setenv("WORK_DIR", filepath.Join(root, "no-such-work"))
	// The command's own git runs with the test process's environment. A
	// developer machine's global config installs the workspace push-policy
	// hooks, which would refuse the fixture origin; the flake check's sandbox
	// has no such config, and neither should this run.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "fixture")
	t.Setenv("GIT_AUTHOR_EMAIL", "fixture@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "fixture")
	t.Setenv("GIT_COMMITTER_EMAIL", "fixture@example.com")

	gitRun(t, root, "init", "-q", "--bare", "-b", "master", fx.origin)
	// git runs 'gc --auto' in the background after a push, and that
	// background process races t.TempDir's cleanup of the fixture
	// repositories. Neither repository outlives one test, so there is
	// nothing for it to collect.
	gitRun(t, fx.origin, "config", "gc.auto", "0")
	gitRun(t, fx.origin, "config", "receive.autogc", "false")
	if err := os.MkdirAll(filepath.Join(fx.checkout, "secrets"), 0755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "init", "-q", "-b", "master", fx.checkout)
	for name, content := range map[string]string{
		"flake.nix":             "{ outputs = _: {}; }\n",
		"secrets.nix":           "{ }\n",
		"credentials.nix":       fixtureCredentialsNix,
		"secrets/old-token.age": fixtureOldCiphertext,
	} {
		if err := os.WriteFile(filepath.Join(fx.checkout, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, fx.checkout, "config", "gc.auto", "0")
	gitRun(t, fx.checkout, "add", "-A")
	gitRun(t, fx.checkout, "commit", "-q", "-m", "fixture")
	gitRun(t, fx.checkout, "remote", "add", "origin", fx.origin)
	gitRun(t, fx.checkout, "push", "-q", "-u", "origin", "master")
	gitRun(t, fx.checkout, "remote", "set-head", "origin", "master")
	gitRun(t, fx.checkout, "switch", "-q", "-c", "agent/landing")

	previousCredentials, previousRegistry, previousRecipients := secretEvalCredentials, secretEvalRegistry, secretEvalRecipients
	previousEncodings := secretEvalEncodings
	previousEncrypt, previousDecrypt, previousCheck := secretEncrypt, secretDecrypt, secretFlakeCheck
	previousTerminal, previousAsk, previousStdin := secretStdinIsTerminal, secretAskOnTerminal, stdin
	t.Cleanup(func() {
		secretEvalCredentials, secretEvalRegistry, secretEvalRecipients = previousCredentials, previousRegistry, previousRecipients
		secretEvalEncodings = previousEncodings
		secretEncrypt, secretDecrypt, secretFlakeCheck = previousEncrypt, previousDecrypt, previousCheck
		secretStdinIsTerminal, secretAskOnTerminal, stdin = previousTerminal, previousAsk, previousStdin
	})

	// The fake evaluator answers from the fixture map, but reads each
	// entry's rotation_state out of the file the way nix would, so the
	// command's own flip is what it sees afterwards.
	secretEvalCredentials = func(checkout string) (map[string]credentialEntry, error) {
		text, err := os.ReadFile(filepath.Join(checkout, "credentials.nix"))
		if err != nil {
			return nil, err
		}
		result := make(map[string]credentialEntry, len(fx.credentials))
		for name, entry := range fx.credentials {
			state := regexp.MustCompile(`(?s)\b` + regexp.QuoteMeta(name) + ` = \{.*?rotation_state = "([a-z]+)";`)
			if match := state.FindStringSubmatch(string(text)); match != nil {
				entry.RotationState = match[1]
			}
			result[name] = entry
		}
		return result, nil
	}
	secretEvalRegistry = func(string) (map[string]tokenGroup, error) { return fx.registry, nil }
	secretEvalEncodings = func(string) ([]string, error) { return fx.encodings, nil }
	secretEvalRecipients = func(_ string, path string) ([]string, error) {
		recipients, ok := fx.recipients[path]
		if !ok {
			return nil, fmt.Errorf("attribute '%s' missing", path)
		}
		return recipients, nil
	}
	secretEncrypt = func(recipients []string, plaintext []byte) ([]byte, error) {
		fx.encryptCalls++
		fx.lastRecipients = append([]string(nil), recipients...)
		fx.lastPlaintext = append([]byte(nil), plaintext...)
		if fx.encryptErr != nil {
			return nil, fx.encryptErr
		}
		if fx.encryptOutput != nil {
			return fx.encryptOutput, nil
		}
		return []byte(fmt.Sprintf("age-encryption.org/v1\n-> fixture %d recipients, %d bytes\n", len(recipients), len(plaintext))), nil
	}
	secretDecrypt = func(string, string) ([]byte, error) {
		fx.decryptCalls++
		return fx.decryptResult, fx.decryptErr
	}
	secretFlakeCheck = func(string) int {
		fx.checkCalls++
		return fx.checkStatus
	}
	secretStdinIsTerminal = func() bool { return fx.terminal }
	secretAskOnTerminal = func(string) (string, error) { return fx.terminalValue, fx.terminalErr }
	stdin = bytes.NewReader(nil)
	return fx
}

func (fx *secretFixture) pipe(value string) { stdin = bytes.NewReader([]byte(value)) }

func (fx *secretFixture) run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return runAllod(t, append(args, fx.checkout)...)
}

func (fx *secretFixture) file(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fx.checkout, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func (fx *secretFixture) exists(name string) bool {
	_, err := os.Lstat(filepath.Join(fx.checkout, name))
	return err == nil
}

func (fx *secretFixture) status(t *testing.T) string {
	t.Helper()
	return gitRun(t, fx.checkout, "status", "--porcelain")
}

func (fx *secretFixture) head(t *testing.T) string {
	t.Helper()
	return gitRun(t, fx.checkout, "rev-parse", "HEAD")
}

func (fx *secretFixture) originHead(t *testing.T, branch string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = fx.origin
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func (fx *secretFixture) commitFiles(t *testing.T) string {
	t.Helper()
	return gitRun(t, fx.checkout, "show", "--name-only", "--format=", "HEAD")
}

// assertUntouched is what every refusal must leave behind: nothing
// encrypted, nothing written, nothing committed, a clean tree.
func (fx *secretFixture) assertUntouched(t *testing.T, before string) {
	t.Helper()
	if fx.encryptCalls != 0 {
		t.Errorf("age was asked to encrypt %d times, want 0", fx.encryptCalls)
	}
	if fx.exists("secrets/new-token.age") {
		t.Error("secrets/new-token.age was written")
	}
	if got := fx.file(t, "credentials.nix"); got != fixtureCredentialsNix {
		t.Error("credentials.nix was changed")
	}
	if got := fx.head(t); got != before {
		t.Error("a commit was made")
	}
	if got := fx.status(t); got != "" {
		t.Errorf("tree is not clean:\n%s", got)
	}
}

// --- Registration ---

func TestSecretNamespaceIsRegistered(t *testing.T) {
	if _, ok := lookupNamespace("secret"); !ok {
		t.Fatal("the secret namespace is not registered in a tagged build")
	}
	out, _, _ := runAllod(t)
	if !strings.Contains(out, "secret   Write or land a credential") {
		t.Errorf("top-level usage does not list the namespace\ngot: %q", out)
	}
	out, errText, code := runAllod(t, "secret", "--help")
	if code != 0 || errText != "" || !strings.Contains(out, "allod secret create <name> [<checkout>]") {
		t.Errorf("secret --help: code=%d stderr=%q stdout=%q", code, errText, out)
	}
	_, errText, code = runAllod(t, "secret")
	if code != 1 || !strings.HasPrefix(errText, "Usage:\n") {
		t.Errorf("bare secret: code=%d stderr=%q", code, errText)
	}
	_, errText, code = runAllod(t, "secret", "frobnicate")
	if code != 1 || !strings.HasPrefix(errText, "allod: unknown secret command: frobnicate\n") {
		t.Errorf("unknown command: code=%d stderr=%q", code, errText)
	}
	_, errText, code = runAllod(t, "secret", "create")
	if code != 1 || !strings.Contains(errText, "requires a credential name") {
		t.Errorf("create without a name: code=%d stderr=%q", code, errText)
	}
	_, errText, code = runAllod(t, "secret", "create", "--to", "dev-a", "x")
	if code != 1 || !strings.Contains(errText, "unknown option for secret create: --to") {
		t.Errorf("an option is refused: code=%d stderr=%q", code, errText)
	}
}

// TestSecretTaggedBuildCarriesEveryCommand pins the shape a secret-tagged
// build carries: 'declare' from the untagged files plus the four secret.go's
// init() adds, and nothing else.
func TestSecretTaggedBuildCarriesEveryCommand(t *testing.T) {
	want := []string{"declare", "create", "rekey", "migrate", "rotate"}
	if got := len(secretCommands); got != len(want) {
		t.Fatalf("secretCommands has %d entries in a tagged build, want %d: %+v", got, len(want), secretCommands)
	}
	for _, name := range want {
		if _, ok := secretCommandEntry(name); !ok {
			t.Errorf("secretCommands has no %q entry: %+v", name, secretCommands)
		}
	}
}

// --- create ---

func TestSecretCreateLandsPendingEntry(t *testing.T) {
	fx := newSecretFixture(t)
	before := fx.head(t)
	fx.pipe("the value\nwith a second line\n")

	out, errText, code := fx.run(t, "secret", "create", "new-token")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}

	if string(fx.lastPlaintext) != "the value\nwith a second line\n" {
		t.Errorf("plaintext handed to age = %q, want the stdin bytes verbatim", fx.lastPlaintext)
	}
	if want := []string{fixtureHostKey, fixtureVMKey}; strings.Join(fx.lastRecipients, "|") != strings.Join(want, "|") {
		t.Errorf("recipients = %v, want the secrets.nix list %v", fx.lastRecipients, want)
	}
	if got := fx.file(t, "secrets/new-token.age"); !strings.HasPrefix(got, "age-encryption.org/v1\n") || strings.Contains(got, "the value") {
		t.Errorf("ciphertext file = %q", got)
	}
	credentials := fx.file(t, "credentials.nix")
	if !strings.Contains(credentials, "rotation_state = \"active\";\n  };\n\n  old-token") {
		t.Errorf("new-token was not flipped to active in place:\n%s", credentials)
	}
	if strings.Count(credentials, `"active"`) != 2 || strings.Contains(credentials, `"pending"`) {
		t.Errorf("unexpected states after the flip:\n%s", credentials)
	}
	if fx.checkCalls != 1 {
		t.Errorf("flake check ran %d times, want 1", fx.checkCalls)
	}
	if got := fx.head(t); got == before {
		t.Error("no commit was made")
	}
	if got := fx.commitFiles(t); got != "credentials.nix\nsecrets/new-token.age" {
		t.Errorf("commit files = %q", got)
	}
	if got := fx.originHead(t, "agent/landing"); got != fx.head(t) {
		t.Errorf("origin agent/landing = %q, want the new HEAD %q", got, fx.head(t))
	}
	if got := fx.originHead(t, "master"); got != before {
		t.Errorf("origin master moved to %q", got)
	}
	if got := fx.status(t); got != "" {
		t.Errorf("tree is not clean after landing:\n%s", got)
	}
	for _, want := range []string{
		"Wrote secrets/new-token.age, encrypted to 2 recipients from secrets.nix\n",
		"credentials.nix: new-token pending -> active\n",
		"on agent/landing and pushed to origin",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q\ngot: %q", want, out)
		}
	}
	if strings.Contains(out+errText, "the value") {
		t.Error("the plaintext was printed")
	}
}

func TestSecretCreateReadsOneHiddenLineFromTerminal(t *testing.T) {
	fx := newSecretFixture(t)
	fx.terminal, fx.terminalValue = true, "typed-value"
	fx.pipe("this pipe must be ignored\n")

	if _, errText, code := fx.run(t, "secret", "create", "new-token"); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if string(fx.lastPlaintext) != "typed-value" {
		t.Errorf("plaintext = %q, want the typed line without its newline", fx.lastPlaintext)
	}
}

// TestSecretCreateRendersEachDeclaredValueShape drives the shared renderer
// through 'create' for every value shape the contract has. It runs the same
// table TestSecretRotateRendersEachDeclaredValueShape drives through
// 'rotate', so the two commands are pinned to byte-identical output rather
// than each to its own idea of one.
func TestSecretCreateRendersEachDeclaredValueShape(t *testing.T) {
	for _, tc := range credentialRenderCases() {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsRclone {
				installFakeRclone(t)
			}
			fx := newSecretFixture(t)
			fx.registry["dev-a.git"].Credentials[0].Value = tc.value
			fx.pipe(tc.secret)

			out, errText, code := fx.run(t, "secret", "create", "new-token")
			if code != 0 {
				t.Fatalf("exit %d, stderr: %s", code, errText)
			}
			if string(fx.lastPlaintext) != tc.want {
				t.Errorf("plaintext handed to age = %q, want %q", fx.lastPlaintext, tc.want)
			}
			if strings.Contains(out+errText, tc.secret) || strings.Contains(out+errText, tc.want) {
				t.Error("the value or the rendered plaintext was printed")
			}
		})
	}
}

func TestSecretCreateRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, fx *secretFixture)
		want  string
	}{
		{"default branch", func(t *testing.T, fx *secretFixture) {
			gitRun(t, fx.checkout, "switch", "-q", "master")
		}, "is on its default branch 'master'"},
		{"dirty tree", func(t *testing.T, fx *secretFixture) {
			os.WriteFile(filepath.Join(fx.checkout, "secrets", "leftover.age"), []byte("x"), 0644)
		}, "uncommitted or untracked changes"},
		{"unknown name", func(*testing.T, *secretFixture) {}, "no credential named 'nope'"},
		{"not pending", func(*testing.T, *secretFixture) {}, "credential 'old-token' is 'active', not 'pending'"},
		{"ciphertext already present", func(t *testing.T, fx *secretFixture) {
			os.WriteFile(filepath.Join(fx.checkout, "secrets", "new-token.age"), []byte(fixtureOldCiphertext), 0644)
			gitRun(t, fx.checkout, "add", "-A")
			gitRun(t, fx.checkout, "commit", "-q", "-m", "leftover committed")
		}, "secrets/new-token.age already exists while 'new-token' is pending"},
		{"no secrets.nix line", func(_ *testing.T, fx *secretFixture) {
			delete(fx.recipients, "secrets/new-token.age")
		}, "secrets.nix in"},
		{"empty recipients", func(_ *testing.T, fx *secretFixture) {
			fx.recipients["secrets/new-token.age"] = nil
		}, "lists no recipients"},
		{"host identity not a recipient", func(_ *testing.T, fx *secretFixture) {
			fx.recipients["secrets/new-token.age"] = []string{fixtureVMKey}
		}, "do not include this host's identity"},
		{"no registry entry", func(_ *testing.T, fx *secretFixture) {
			fx.registry = map[string]tokenGroup{}
		}, "no rotation registry entry for 'new-token'"},
		{"registry path disagrees", func(_ *testing.T, fx *secretFixture) {
			fx.registry["dev-a.git"].Credentials[0].SecretPath = "secrets/other.age"
		}, "names 'secrets/other.age' but its credentials.nix consumer is 'secrets/new-token.age'"},
		{"two agenix consumers", func(_ *testing.T, fx *secretFixture) {
			entry := fx.credentials["new-token"]
			entry.Consumers = append(entry.Consumers, credentialConsumer{Type: "agenix", Repo: "secrets", Secret: "secrets/second.age"})
			fx.credentials["new-token"] = entry
		}, "has 2 agenix consumers"},
		{"legacy registry entry", func(_ *testing.T, fx *secretFixture) {
			fx.registry["dev-a.git"].Credentials[0].Format = "credential-store-url"
		}, "run 'allod secret migrate new-token' before landing a value for it"},
		{"template with no placeholder", func(_ *testing.T, fx *secretFixture) {
			fx.registry["dev-a.git"].Credentials[0].Value = &credentialValue{Template: "https://user:token@example.test"}
		}, "value.template must contain exactly one {secret} placeholder"},
		{"template with two placeholders", func(_ *testing.T, fx *secretFixture) {
			fx.registry["dev-a.git"].Credentials[0].Value = &credentialValue{Template: "{secret}:{secret}"}
		}, "value.template must contain exactly one {secret} placeholder"},
		{"unknown encoder", func(_ *testing.T, fx *secretFixture) {
			fx.registry["dev-a.git"].Credentials[0].Value = &credentialValue{Template: "{secret}", Encode: "rot13"}
		}, `value.encode "rot13" is not exported by lib.credentialEncodings`},
		{"empty value", func(_ *testing.T, fx *secretFixture) { fx.pipe("") }, "the value is empty"},
		{"whitespace value", func(_ *testing.T, fx *secretFixture) { fx.pipe(" \n\t\n") }, "the value is empty"},
		{"age fails", func(_ *testing.T, fx *secretFixture) {
			fx.encryptErr = errors.New("no recipients")
		}, "age failed to encrypt"},
		{"age output is not an age file", func(_ *testing.T, fx *secretFixture) {
			fx.encryptOutput = []byte("garbage")
		}, "not an age file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newSecretFixture(t)
			fx.pipe("value\n")
			tc.setup(t, fx)
			before := fx.head(t)
			name := "new-token"
			switch tc.name {
			case "unknown name":
				name = "nope"
			case "not pending":
				name = "old-token"
			}
			_, errText, code := fx.run(t, "secret", "create", name)
			if code == 0 {
				t.Fatalf("exit 0, want a refusal")
			}
			if !strings.Contains(errText, tc.want) {
				t.Errorf("stderr lacks %q\ngot: %q", tc.want, errText)
			}
			if tc.name == "age fails" || tc.name == "age output is not an age file" {
				// age was called; nothing may have been written afterwards.
				fx.encryptCalls = 0
			}
			if tc.name == "ciphertext already present" {
				// The leftover is fixture state here, not a write by the command.
				return
			}
			if tc.name == "dirty tree" {
				os.Remove(filepath.Join(fx.checkout, "secrets", "leftover.age"))
			}
			if fx.checkCalls != 0 {
				t.Errorf("flake check ran %d times on a refusal", fx.checkCalls)
			}
			fx.assertUntouched(t, before)
		})
	}
}

// A checkout whose origin/HEAD is unset must be refused rather than compared
// against a guessed 'master': on a repository whose default is 'main', the
// guess would let the landing be pushed straight to the default branch.
func TestSecretCreateRefusesWhenDefaultBranchIsUnknown(t *testing.T) {
	fx := newSecretFixture(t)
	gitRun(t, fx.checkout, "remote", "set-head", "origin", "-d")
	fx.pipe("value\n")
	before := fx.head(t)

	_, errText, code := fx.run(t, "secret", "create", "new-token")
	if code != 1 || !strings.Contains(errText, "could not resolve the default branch") || !strings.Contains(errText, "remote set-head origin -a") {
		t.Errorf("code=%d stderr=%q", code, errText)
	}
	fx.assertUntouched(t, before)
}

// installFailingPreCommit makes every commit in the fixture fail, the way a
// policy hook or a missing author identity would on a real host.
func installFailingPreCommit(t *testing.T, fx *secretFixture) {
	t.Helper()
	hook := filepath.Join(fx.checkout, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'policy: no' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestSecretCreateRestoresTreeWhenCommitFails(t *testing.T) {
	fx := newSecretFixture(t)
	installFailingPreCommit(t, fx)
	fx.pipe("value\n")
	before := fx.head(t)

	_, errText, code := fx.run(t, "secret", "create", "new-token")
	if code == 0 {
		t.Fatal("exit 0 despite a failed commit")
	}
	if !strings.Contains(errText, "git commit failed; restored") || !strings.Contains(errText, "policy: no") {
		t.Errorf("stderr = %q", errText)
	}
	if fx.checkCalls != 1 {
		t.Errorf("flake check ran %d times, want 1", fx.checkCalls)
	}
	fx.encryptCalls = 0
	fx.assertUntouched(t, before)
	if staged := gitRun(t, fx.checkout, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("files left staged: %q", staged)
	}
}

func TestSecretRekeyRestoresCiphertextWhenCommitFails(t *testing.T) {
	fx := newSecretFixture(t)
	installFailingPreCommit(t, fx)
	before := fx.head(t)

	if _, errText, code := fx.run(t, "secret", "rekey", "old-token"); code == 0 || !strings.Contains(errText, "git commit failed; restored") {
		t.Errorf("code=%d stderr=%q", code, errText)
	}
	if got := fx.file(t, "secrets/old-token.age"); got != fixtureOldCiphertext {
		t.Error("the original ciphertext was not restored")
	}
	fx.encryptCalls = 0
	fx.assertUntouched(t, before)
}

func TestSecretCreateRestoresBothFilesWhenChecksFail(t *testing.T) {
	fx := newSecretFixture(t)
	fx.checkStatus = 3
	fx.pipe("value\n")
	before := fx.head(t)

	_, errText, code := fx.run(t, "secret", "create", "new-token")
	if code != 3 {
		t.Errorf("exit %d, want the check's status 3", code)
	}
	if !strings.Contains(errText, "checks failed; restored credentials.nix and removed secrets/new-token.age") {
		t.Errorf("stderr = %q", errText)
	}
	if fx.encryptCalls != 1 || fx.checkCalls != 1 {
		t.Errorf("encrypt=%d check=%d, want 1 and 1", fx.encryptCalls, fx.checkCalls)
	}
	fx.encryptCalls = 0
	fx.assertUntouched(t, before)
	if got := fx.originHead(t, "agent/landing"); got != "" {
		t.Errorf("origin gained agent/landing = %q", got)
	}
}

func TestSecretCreateReportsPushFailureAndKeepsCommit(t *testing.T) {
	fx := newSecretFixture(t)
	fx.pipe("value\n")
	before := fx.head(t)
	gitRun(t, fx.checkout, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "gone.git"))

	_, errText, code := fx.run(t, "secret", "create", "new-token")
	if code == 0 {
		t.Fatal("exit 0 despite a failed push")
	}
	if !strings.Contains(errText, "but the push failed") || !strings.Contains(errText, "push the branch yourself") {
		t.Errorf("stderr = %q", errText)
	}
	if got := fx.head(t); got == before {
		t.Error("the commit was not kept")
	}
	if got := fx.status(t); got != "" {
		t.Errorf("tree is not clean:\n%s", got)
	}
}

// --- rekey ---

func TestSecretRekeyRewritesToCurrentRecipients(t *testing.T) {
	fx := newSecretFixture(t)
	before := fx.head(t)

	out, errText, code := fx.run(t, "secret", "rekey", "old-token")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if fx.decryptCalls != 1 {
		t.Errorf("decrypt ran %d times, want 1", fx.decryptCalls)
	}
	if string(fx.lastPlaintext) != "old value\n" {
		t.Errorf("re-encrypted plaintext = %q, want the decrypted bytes", fx.lastPlaintext)
	}
	if want := []string{fixtureHostKey, fixtureVMKey, fixtureOtherKey}; strings.Join(fx.lastRecipients, "|") != strings.Join(want, "|") {
		t.Errorf("recipients = %v, want %v", fx.lastRecipients, want)
	}
	if got := fx.file(t, "secrets/old-token.age"); got == fixtureOldCiphertext || !strings.HasPrefix(got, "age-encryption.org/v1\n") {
		t.Errorf("ciphertext after rekey = %q", got)
	}
	if got := fx.file(t, "credentials.nix"); got != fixtureCredentialsNix {
		t.Error("rekey changed credentials.nix")
	}
	if got := fx.commitFiles(t); got != "secrets/old-token.age" {
		t.Errorf("commit files = %q", got)
	}
	if got := fx.originHead(t, "agent/landing"); got != fx.head(t) || got == before {
		t.Errorf("origin agent/landing = %q, HEAD %q, before %q", got, fx.head(t), before)
	}
	if !strings.Contains(out, "Rewrote secrets/old-token.age, encrypted to 3 recipients") {
		t.Errorf("stdout = %q", out)
	}
	if strings.Contains(out+errText, "old value") {
		t.Error("the plaintext was printed")
	}
}

func TestSecretRekeyRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(fx *secretFixture)
		cred  string
		want  string
	}{
		{"pending entry", func(*secretFixture) {}, "new-token", "is pending, so there is no ciphertext to rekey"},
		{"decrypt fails", func(fx *secretFixture) { fx.decryptErr = errors.New("no identity matched") }, "old-token", "could not decrypt secrets/old-token.age"},
		{"decrypts empty", func(fx *secretFixture) { fx.decryptResult = []byte("\n") }, "old-token", "decrypts to an empty value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newSecretFixture(t)
			tc.setup(fx)
			before := fx.head(t)
			_, errText, code := fx.run(t, "secret", "rekey", tc.cred)
			if code == 0 {
				t.Fatal("exit 0, want a refusal")
			}
			if !strings.Contains(errText, tc.want) {
				t.Errorf("stderr lacks %q\ngot: %q", tc.want, errText)
			}
			if got := fx.file(t, "secrets/old-token.age"); got != fixtureOldCiphertext {
				t.Error("the ciphertext was rewritten")
			}
			fx.assertUntouched(t, before)
		})
	}
}

func TestSecretRekeyRestoresCiphertextWhenChecksFail(t *testing.T) {
	fx := newSecretFixture(t)
	fx.checkStatus = 1
	before := fx.head(t)

	if _, errText, code := fx.run(t, "secret", "rekey", "old-token"); code != 1 || !strings.Contains(errText, "restored secrets/old-token.age") {
		t.Errorf("code=%d stderr=%q", code, errText)
	}
	if got := fx.file(t, "secrets/old-token.age"); got != fixtureOldCiphertext {
		t.Error("the original ciphertext was not restored")
	}
	fx.encryptCalls = 0
	fx.assertUntouched(t, before)
}

// --- The checkout ---

func TestSecretResolvesCheckoutFromWorkDir(t *testing.T) {
	fx := newSecretFixture(t)
	work := t.TempDir()
	t.Setenv("WORK_DIR", work)
	if err := os.MkdirAll(filepath.Join(work, "allod"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fx.checkout, filepath.Join(work, "allod", "secrets")); err != nil {
		t.Fatal(err)
	}
	fx.pipe("value\n")
	if _, errText, code := runAllod(t, "secret", "create", "new-token"); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if !fx.exists("secrets/new-token.age") {
		t.Error("the checkout under WORK_DIR was not used")
	}
}

func TestSecretRefusesADirectoryThatIsNotASecretsCheckout(t *testing.T) {
	newSecretFixture(t)
	other := t.TempDir()
	gitRun(t, other, "init", "-q", "-b", "master", ".")
	_, errText, code := runAllod(t, "secret", "create", "new-token", other)
	if code != 1 || !strings.Contains(errText, "is not a secrets checkout: no flake.nix") {
		t.Errorf("code=%d stderr=%q", code, errText)
	}
}

// --- The edit ---

func TestFlipPendingToActive(t *testing.T) {
	got, err := flipPendingToActive(fixtureCredentialsNix, "new-token")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(fixtureCredentialsNix, "rotation_state = \"pending\";", "rotation_state = \"active\";", 1)
	if got != want {
		t.Errorf("flip changed more than one line:\n%s", got)
	}

	// A comment quoting the field is neither a second assignment nor a
	// line to rewrite.
	commented := strings.Replace(fixtureCredentialsNix,
		"    rotation_state = \"pending\";",
		"    # rotation_state = \"pending\" until allod secret create lands it\n    rotation_state = \"pending\";", 1)
	got, err = flipPendingToActive(commented, "new-token")
	if err != nil {
		t.Fatalf("commented entry: %v", err)
	}
	if !strings.Contains(got, "# rotation_state = \"pending\" until allod secret create lands it\n    rotation_state = \"active\";") {
		t.Errorf("commented entry: comment rewritten or field not flipped:\n%s", got)
	}

	// A brace inside a string or a comment does not close the entry.
	braced := strings.Replace(fixtureCredentialsNix,
		"    kind           = \"agent\";",
		"    kind           = \"agent\";\n    description    = \"} not a closer \\\" either\";\n    # nor this: }", 1)
	got, err = flipPendingToActive(braced, "new-token")
	if err != nil {
		t.Fatalf("braced entry: %v", err)
	}
	if !strings.Contains(got, "# nor this: }\n    owner          = \"allod-agent\";") || strings.Count(got, "rotation_state = \"active\";") != 2 {
		t.Errorf("braced entry: wrong flip:\n%s", got)
	}

	quoted := strings.Replace(fixtureCredentialsNix, "  new-token = {", "  \"new-token\" = {", 1)
	if got, err := flipPendingToActive(quoted, "new-token"); err != nil || !strings.Contains(got, "\"new-token\" = {\n    name           = \"new-token\";\n    kind           = \"agent\";\n    owner          = \"allod-agent\";\n    public_key     = null;\n    consumers      = [\n      { type = \"agenix\"; repo = \"secrets\"; secret = \"secrets/new-token.age\"; }\n    ];\n    rotation_state = \"active\";") {
		t.Errorf("quoted attribute name: err=%v\n%s", err, got)
	}

	for _, tc := range []struct{ name, text, want string }{
		{"already active", fixtureCredentialsNix, "found 0"},
		{"no literal entry", fixtureCredentialsNix, "no literal 'generated = { ... }' entry"},
		{"two pendings", strings.Replace(fixtureCredentialsNix, "rotation_state = \"pending\";", "rotation_state = \"pending\";\n    rotation_state = \"pending\";", 1), "found 2"},
		{"unclosed", "{\n  new-token = {\n    rotation_state = \"pending\";\n", "no closing brace"},
	} {
		name := "new-token"
		switch tc.name {
		case "already active":
			name = "old-token"
		case "no literal entry":
			name = "generated"
		}
		if _, err := flipPendingToActive(tc.text, name); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

func TestKeyMaterialIgnoresComment(t *testing.T) {
	if keyMaterial("ssh-ed25519 AAAA host") != keyMaterial("ssh-ed25519 AAAA\n") {
		t.Error("comment or newline changed the key material")
	}
	if keyMaterial("ssh-ed25519 AAAA") == keyMaterial("ssh-ed25519 BBBB") {
		t.Error("different keys compare equal")
	}
}
