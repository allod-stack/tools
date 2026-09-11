package main

// Tests for 'allod secret declare' against a fixture checkout — a plain git
// repository holding invented flake.nix, credentials.nix, secrets.nix, and
// forgejo-token-groups.json content shaped like the secrets template's real
// files. declareLoadVMSpecs is the one seam declare has (see
// secret_declare.go); everything else is exercised through the real code
// paths against real files on disk, since this command's whole job is
// producing exact file text.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const declareFixtureFlake = "{ outputs = _: {}; }\n"

const declareFixtureCredentials = `let
  mk = name: { inherit name; };
in
{
  existing-token = {
    name           = "existing-token";
    kind           = "agent";
    owner          = "allod-agent";
    public_key     = null;
    consumers      = [
      { type = "agenix"; repo = "secrets"; secret = "secrets/existing-token.age"; }
    ];
    rotation_state = "active";
  };
}
`

const declareFixtureSecrets = `let
  hostKey = "ssh-ed25519 AAAAHostKeyFixture host";
  vmKeys = vm: { dev-a = [ "ssh-ed25519 AAAADevAKeyFixture dev-a" ]; dev-b = [ "ssh-ed25519 AAAADevBKeyFixture dev-b" ]; }.${vm};
in {
  "secrets/existing-token.age".publicKeys = [ hostKey ] ++ vmKeys "dev-a";
}
`

const declareFixtureRegistry = `{
  "existing-token": {
    "service": "none",
    "registry_alias": "existing-token",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "existing-token",
        "secret_path": "secrets/existing-token.age",
        "format": "raw-forgejo-token",
        "targets": [
          { "system": "dev-a", "kind": "dev-vm", "deployed_path": "/root/.token", "verify": { "type": "git-ls-remote" } }
        ]
      }
    ]
  },
  "other-alias": {
    "service": "none",
    "registry_alias": "other-alias",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "shadow-token",
        "secret_path": "secrets/unrelated.age",
        "format": "raw-forgejo-token",
        "targets": [
          { "system": "dev-a", "kind": "dev-vm", "deployed_path": "/root/.other", "verify": { "type": "git-ls-remote" } }
        ]
      },
      {
        "credential": "unrelated-name",
        "secret_path": "secrets/collide-path-token.age",
        "format": "raw-forgejo-token",
        "targets": [
          { "system": "dev-a", "kind": "dev-vm", "deployed_path": "/root/.other2", "verify": { "type": "git-ls-remote" } }
        ]
      }
    ]
  }
}
`

// declareSabotageRegistry deliberately ends in a '}' that closes nothing:
// it lies inside an unterminated string, not the top-level object. It still
// satisfies the mechanical "ends in '}'" precondition insertion works from,
// so only the re-parse guard catches it.
const declareSabotageRegistry = "{\n  \"existing-token\": \"unterminated string }\n}\n"

// declareSabotageCredentials is missing the outer attrset's opening '{', so
// even a mechanically well-formed insertion before its final '}' leaves the
// brace count unbalanced.
const declareSabotageCredentials = `let
  mk = name: { inherit name; };
in
  existing-token = {
    rotation_state = "active";
  };
}
`

type declareFixture struct {
	checkout string
}

func newDeclareFixtureFiles(t *testing.T, credentials, secrets, registry string) *declareFixture {
	t.Helper()
	dir := t.TempDir()
	fx := &declareFixture{checkout: dir}
	for name, content := range map[string]string{
		"flake.nix":                 declareFixtureFlake,
		"credentials.nix":           credentials,
		"secrets.nix":               secrets,
		"forgejo-token-groups.json": registry,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("git", "init", "-q", dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	previous := declareLoadVMSpecs
	t.Cleanup(func() { declareLoadVMSpecs = previous })
	declareLoadVMSpecs = func() map[string]declareVMSpec {
		return map[string]declareVMSpec{
			"dev-a":     {Repos: []string{"allod/tools"}},
			"dev-b":     {Repos: []string{"allod/tools"}},
			"privacy-1": {Repos: nil},
		}
	}
	return fx
}

func newDeclareFixture(t *testing.T) *declareFixture {
	t.Helper()
	return newDeclareFixtureFiles(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
}

func (fx *declareFixture) run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	full := append([]string{"secret", "declare"}, args...)
	full = append(full, fx.checkout)
	return runAllod(t, full...)
}

func (fx *declareFixture) file(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fx.checkout, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// assertUntouched is what every refusal must leave behind: all three files
// exactly as the fixture wrote them.
func (fx *declareFixture) assertUntouched(t *testing.T, credentials, secrets, registry string) {
	t.Helper()
	if got := fx.file(t, "credentials.nix"); got != credentials {
		t.Errorf("credentials.nix changed:\n%s", got)
	}
	if got := fx.file(t, "secrets.nix"); got != secrets {
		t.Errorf("secrets.nix changed:\n%s", got)
	}
	if got := fx.file(t, "forgejo-token-groups.json"); got != registry {
		t.Errorf("forgejo-token-groups.json changed:\n%s", got)
	}
}

// --- Happy path: one machine, service none ---

const declareExpectedCredentialsOneMachine = `let
  mk = name: { inherit name; };
in
{
  existing-token = {
    name           = "existing-token";
    kind           = "agent";
    owner          = "allod-agent";
    public_key     = null;
    consumers      = [
      { type = "agenix"; repo = "secrets"; secret = "secrets/existing-token.age"; }
    ];
    rotation_state = "active";
  };

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
}
`

const declareExpectedSecretsOneMachine = `let
  hostKey = "ssh-ed25519 AAAAHostKeyFixture host";
  vmKeys = vm: { dev-a = [ "ssh-ed25519 AAAADevAKeyFixture dev-a" ]; dev-b = [ "ssh-ed25519 AAAADevBKeyFixture dev-b" ]; }.${vm};
in {
  "secrets/existing-token.age".publicKeys = [ hostKey ] ++ vmKeys "dev-a";
  "secrets/new-token.age".publicKeys = [ hostKey ] ++ vmKeys "dev-a";
}
`

const declareExpectedRegistryOneMachine = `{
  "existing-token": {
    "service": "none",
    "registry_alias": "existing-token",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "existing-token",
        "secret_path": "secrets/existing-token.age",
        "format": "raw-forgejo-token",
        "targets": [
          { "system": "dev-a", "kind": "dev-vm", "deployed_path": "/root/.token", "verify": { "type": "git-ls-remote" } }
        ]
      }
    ]
  },
  "other-alias": {
    "service": "none",
    "registry_alias": "other-alias",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "shadow-token",
        "secret_path": "secrets/unrelated.age",
        "format": "raw-forgejo-token",
        "targets": [
          { "system": "dev-a", "kind": "dev-vm", "deployed_path": "/root/.other", "verify": { "type": "git-ls-remote" } }
        ]
      },
      {
        "credential": "unrelated-name",
        "secret_path": "secrets/collide-path-token.age",
        "format": "raw-forgejo-token",
        "targets": [
          { "system": "dev-a", "kind": "dev-vm", "deployed_path": "/root/.other2", "verify": { "type": "git-ls-remote" } }
        ]
      }
    ]
  },
  "new-token": {
    "service": "none",
    "registry_alias": "new-token",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "new-token",
        "secret_path": "secrets/new-token.age",
        "format": "raw-forgejo-token",
        "targets": [
          {
            "system": "dev-a",
            "kind": "dev-vm",
            "deployed_path": "/root/.token",
            "verify": {
              "type": "git-ls-remote"
            }
          }
        ]
      }
    ]
  }
}
`

func TestSecretDeclareOneMachineServiceNone(t *testing.T) {
	fx := newDeclareFixture(t)
	out, errText, code := fx.run(t, "new-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if want := "credentials.nix\nsecrets.nix\nforgejo-token-groups.json\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	if errText != "" {
		t.Errorf("stderr = %q, want empty", errText)
	}
	if got := fx.file(t, "credentials.nix"); got != declareExpectedCredentialsOneMachine {
		t.Errorf("credentials.nix =\n%s\nwant\n%s", got, declareExpectedCredentialsOneMachine)
	}
	if got := fx.file(t, "secrets.nix"); got != declareExpectedSecretsOneMachine {
		t.Errorf("secrets.nix =\n%s\nwant\n%s", got, declareExpectedSecretsOneMachine)
	}
	if got := fx.file(t, "forgejo-token-groups.json"); got != declareExpectedRegistryOneMachine {
		t.Errorf("forgejo-token-groups.json =\n%s\nwant\n%s", got, declareExpectedRegistryOneMachine)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(declareExpectedRegistryOneMachine), &decoded); err != nil {
		t.Fatalf("golden registry text does not parse as JSON: %v", err)
	}
}

// --- Happy path: two machines, service forgejo, comma-separated --to ---

const declareExpectedCredentialsMultiMachine = `let
  mk = name: { inherit name; };
in
{
  existing-token = {
    name           = "existing-token";
    kind           = "agent";
    owner          = "allod-agent";
    public_key     = null;
    consumers      = [
      { type = "agenix"; repo = "secrets"; secret = "secrets/existing-token.age"; }
    ];
    rotation_state = "active";
  };

  multi-token = {
    name           = "multi-token";
    kind           = "agent";
    owner          = "allod-agent";
    public_key     = null;
    consumers      = [
      { type = "agenix"; repo = "secrets"; secret = "secrets/multi-token.age"; }
    ];
    rotation_state = "pending";
  };
}
`

const declareExpectedSecretsMultiMachine = `let
  hostKey = "ssh-ed25519 AAAAHostKeyFixture host";
  vmKeys = vm: { dev-a = [ "ssh-ed25519 AAAADevAKeyFixture dev-a" ]; dev-b = [ "ssh-ed25519 AAAADevBKeyFixture dev-b" ]; }.${vm};
in {
  "secrets/existing-token.age".publicKeys = [ hostKey ] ++ vmKeys "dev-a";
  "secrets/multi-token.age".publicKeys = [ hostKey ] ++ vmKeys "dev-a" ++ vmKeys "dev-b";
}
`

const declareExpectedGroupMultiMachine = `  "multi-token": {
    "service": "forgejo",
    "account": "allod-agent",
    "ui_token_name": "multi-token",
    "registry_alias": "multi-token",
    "rotation_strategy": "overlap",
    "credentials": [
      {
        "credential": "multi-token",
        "secret_path": "secrets/multi-token.age",
        "format": "credential-store-url",
        "targets": [
          {
            "system": "dev-a",
            "kind": "dev-vm",
            "deployed_path": "/root/.git-credentials",
            "verify": {
              "type": "git-ls-remote"
            }
          },
          {
            "system": "dev-b",
            "kind": "dev-vm",
            "deployed_path": "/root/.git-credentials",
            "verify": {
              "type": "git-ls-remote"
            }
          }
        ]
      }
    ]
  }
}
`

func TestSecretDeclareTwoMachinesServiceForgejo(t *testing.T) {
	fx := newDeclareFixture(t)
	_, errText, code := fx.run(t, "multi-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a,dev-b",
		"--format", "credential-store-url", "--deployed-path", "/root/.git-credentials", "--verify", "git-ls-remote",
		"--service", "forgejo", "--account", "allod-agent", "--ui-token-name", "multi-token")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if errText != "" {
		t.Errorf("stderr = %q, want empty", errText)
	}
	if got := fx.file(t, "credentials.nix"); got != declareExpectedCredentialsMultiMachine {
		t.Errorf("credentials.nix =\n%s\nwant\n%s", got, declareExpectedCredentialsMultiMachine)
	}
	if got := fx.file(t, "secrets.nix"); got != declareExpectedSecretsMultiMachine {
		t.Errorf("secrets.nix =\n%s\nwant\n%s", got, declareExpectedSecretsMultiMachine)
	}
	registry := fx.file(t, "forgejo-token-groups.json")
	if !strings.HasSuffix(registry, declareExpectedGroupMultiMachine) {
		t.Errorf("forgejo-token-groups.json does not end with the expected group:\n%s", registry)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(registry), &decoded); err != nil {
		t.Fatalf("written registry does not parse as JSON: %v", err)
	}
}

// --- --to nexus: the host, not a VM ---

func TestSecretDeclareTargetingNexusNeedsNoVMLookup(t *testing.T) {
	fx := newDeclareFixture(t)
	declareLoadVMSpecs = func() map[string]declareVMSpec {
		t.Fatal("declare looked up the VM registry for a --to list containing only 'nexus'")
		return nil
	}
	out, errText, code := fx.run(t, "host-token",
		"--kind", "machine-host", "--owner", "nexus", "--to", "nexus",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "site-check")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errText)
	}
	if errText != "" {
		t.Errorf("stderr = %q, want empty", errText)
	}
	if want := "credentials.nix\nsecrets.nix\nforgejo-token-groups.json\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	secrets := fx.file(t, "secrets.nix")
	if !strings.Contains(secrets, `"secrets/host-token.age".publicKeys = [ hostKey ];`) {
		t.Errorf("secrets.nix missing the host-only recipient line:\n%s", secrets)
	}
	registry := fx.file(t, "forgejo-token-groups.json")
	if !strings.Contains(registry, `"kind": "nixos-host"`) {
		t.Errorf("registry group does not carry the derived nixos-host kind:\n%s", registry)
	}
}

// --- Collisions ---

func TestSecretDeclareCollisions(t *testing.T) {
	validArgs := []string{
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote",
	}
	cases := []struct {
		name string
		want string
	}{
		{"existing-token", "credentials.nix: an entry named 'existing-token' already exists"},
		{"shadow-token", "forgejo-token-groups.json: group 'other-alias' already names credential 'shadow-token'"},
		{"collide-path-token", "forgejo-token-groups.json: group 'other-alias' already uses secret path 'secrets/collide-path-token.age'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newDeclareFixture(t)
			args := append([]string{tc.name}, validArgs...)
			_, errText, code := fx.run(t, args...)
			if code == 0 {
				t.Fatal("exit 0, want a refusal")
			}
			if !strings.Contains(errText, tc.want) {
				t.Errorf("stderr lacks %q\ngot: %q", tc.want, errText)
			}
			if !strings.Contains(errText, "declare never edits an existing declaration") {
				t.Errorf("stderr lacks the standing refusal message\ngot: %q", errText)
			}
			fx.assertUntouched(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
		})
	}
}

// TestSecretDeclareSecretsLineCollisionAlone exercises the secrets.nix-only
// branch: a publicKeys line already exists for the path a new name would
// use, with no matching credentials.nix entry or registry group — a state
// that should not occur from declare's own writes, but the check must catch
// it regardless of how it arose.
func TestSecretDeclareSecretsLineCollisionAlone(t *testing.T) {
	secrets := declareFixtureSecrets[:len(declareFixtureSecrets)-len("}\n")] +
		`  "secrets/orphan.age".publicKeys = [ hostKey ];` + "\n}\n"
	fx := newDeclareFixtureFiles(t, declareFixtureCredentials, secrets, declareFixtureRegistry)
	_, errText, code := fx.run(t, "orphan",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "secrets.nix: a publicKeys line for 'secrets/orphan.age' already exists"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	fx.assertUntouched(t, declareFixtureCredentials, secrets, declareFixtureRegistry)
}

// --- Missing and invalid flags ---

func TestSecretDeclareFlagErrors(t *testing.T) {
	base := map[string]string{
		"--kind": "agent", "--owner": "allod-agent", "--to": "dev-a",
		"--format": "raw-forgejo-token", "--deployed-path": "/root/.token", "--verify": "git-ls-remote",
	}
	buildArgs := func(overrides map[string]string, omit string) []string {
		args := []string{"new-token"}
		for flag, value := range base {
			if flag == omit {
				continue
			}
			args = append(args, flag, value)
		}
		for flag, value := range overrides {
			args = append(args, flag, value)
		}
		return args
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing --kind", buildArgs(nil, "--kind"), "--kind is required"},
		{"missing --owner", buildArgs(nil, "--owner"), "--owner is required"},
		{"missing --to", buildArgs(nil, "--to"), "--to is required"},
		{"missing --format", buildArgs(nil, "--format"), "--format is required"},
		{"missing --deployed-path", buildArgs(nil, "--deployed-path"), "--deployed-path is required"},
		{"missing --verify", buildArgs(nil, "--verify"), "--verify is required"},
		{"invalid --kind", buildArgs(map[string]string{"--kind": "bogus"}, ""), "invalid --kind \"bogus\""},
		{"invalid --format", buildArgs(map[string]string{"--format": "bogus"}, ""), "invalid --format \"bogus\""},
		{"invalid --verify", buildArgs(map[string]string{"--verify": "bogus"}, ""), "invalid --verify \"bogus\""},
		{"invalid --service", buildArgs(map[string]string{"--service": "bogus"}, ""), "invalid --service \"bogus\""},
		{"invalid --strategy", buildArgs(map[string]string{"--strategy": "bogus"}, ""), "invalid --strategy \"bogus\""},
		{"forgejo needs account", buildArgs(map[string]string{"--service": "forgejo", "--ui-token-name": "x"}, ""), "--service forgejo requires --account and --ui-token-name"},
		{"forgejo needs ui-token-name", buildArgs(map[string]string{"--service": "forgejo", "--account": "x"}, ""), "--service forgejo requires --account and --ui-token-name"},
		{"none refuses account", buildArgs(map[string]string{"--account": "x"}, ""), "--service none refuses --account and --ui-token-name"},
		{"invalid name", []string{"Bad_Name!"}, "invalid credential name"},
		{"too many positionals", []string{"new-token", "a", "b"}, "at most one checkout path"},
		{"unknown option", []string{"new-token", "--bogus"}, "unknown option for secret declare: --bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newDeclareFixture(t)
			_, errText, code := fx.run(t, tc.args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(errText, tc.want) {
				t.Errorf("stderr lacks %q\ngot: %q", tc.want, errText)
			}
			fx.assertUntouched(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
		})
	}
}

// TestSecretDeclareRequiresAName is separate from the flag-error table
// because fx.run always appends the fixture checkout as a trailing
// positional, which would otherwise be read as the credential name when no
// name is given at all.
func TestSecretDeclareRequiresAName(t *testing.T) {
	_, errText, code := runAllod(t, "secret", "declare")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "requires a credential name"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
}

// --- Machine absent from the inventory registry ---

func TestSecretDeclareRefusesUnknownMachine(t *testing.T) {
	fx := newDeclareFixture(t)
	_, errText, code := fx.run(t, "new-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "ghost-vm",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if !strings.Contains(errText, "machine 'ghost-vm' named in --to is not in") || !strings.Contains(errText, "vm-specs.json") {
		t.Errorf("stderr = %q", errText)
	}
	fx.assertUntouched(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
}

// --- The JSON re-parse guard ---

// TestDeclareInsertRegistryGroupReparseGuard pins the low-level guard
// directly: a registry text whose final '}' closes an unterminated string,
// not the top-level object, still satisfies the mechanical "ends in a '}'"
// precondition insertion works from, so the insertion itself succeeds —
// but the result must still fail json.Valid, which is what secretDeclare's
// post-write re-parse guard checks before ever writing the file.
func TestDeclareInsertRegistryGroupReparseGuard(t *testing.T) {
	group := declareGroup{
		Service: "none", RegistryAlias: "new-token", RotationStrategy: "overlap",
		Credentials: []declareCredentialEntry{{
			Credential: "new-token", SecretPath: "secrets/new-token.age", Format: "raw-forgejo-token",
		}},
	}
	got, err := declareInsertRegistryGroup(declareSabotageRegistry, "new-token", group)
	if err != nil {
		t.Fatalf("declareInsertRegistryGroup: %v", err)
	}
	if json.Valid([]byte(got)) {
		t.Fatalf("expected the sabotaged tail to still not parse after insertion, got valid JSON:\n%s", got)
	}
}

// TestSecretDeclareRefusesWhenRegistryDoesNotParse is the end-to-end half:
// a checkout whose forgejo-token-groups.json is already broken — the state
// the low-level guard above exists to prevent one command from causing —
// is refused before anything is written, rather than declare compounding
// the damage.
func TestSecretDeclareRefusesWhenRegistryDoesNotParse(t *testing.T) {
	fx := newDeclareFixtureFiles(t, declareFixtureCredentials, declareFixtureSecrets, declareSabotageRegistry)
	_, errText, code := fx.run(t, "new-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "is not valid JSON"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	fx.assertUntouched(t, declareFixtureCredentials, declareFixtureSecrets, declareSabotageRegistry)
}

// --- The nix brace-balance guard ---

func TestSecretDeclareRefusesWhenCredentialsWouldNotBalance(t *testing.T) {
	fx := newDeclareFixtureFiles(t, declareSabotageCredentials, declareFixtureSecrets, declareFixtureRegistry)
	_, errText, code := fx.run(t, "new-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "the edit would leave unbalanced braces; refusing to write"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	fx.assertUntouched(t, declareSabotageCredentials, declareFixtureSecrets, declareFixtureRegistry)
}

// --- Name validation ---

func TestDeclareNamePattern(t *testing.T) {
	for _, valid := range []string{"a", "agent-pr-token", "a1-b2"} {
		if !declareNamePattern.MatchString(valid) {
			t.Errorf("%q should match the name pattern", valid)
		}
	}
	for _, invalid := range []string{"", "-a", "Agent", "agent_token", "agent token"} {
		if declareNamePattern.MatchString(invalid) {
			t.Errorf("%q should not match the name pattern", invalid)
		}
	}
}
