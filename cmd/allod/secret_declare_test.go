package main

// Tests for 'allod secret declare' against a fixture checkout — a plain git
// repository holding invented flake.nix, credentials.nix, secrets.nix, and
// forgejo-token-groups.json content shaped like the secrets template's real
// files. declare has no Go-level seam for the inventory lookup: it shells
// out to the real 'nix eval', so declareFakeNix puts a fake 'nix' on PATH
// that answers by machine name, the way TestRcloneSiteRemoteCheck
// (site_test.go) fakes 'rclone'. Everything else is exercised through the
// real code paths against real files on disk, since this command's whole
// job is producing exact file text.

import (
	"encoding/json"
	"fmt"
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

// declareFakeCredentialsJSON renders the object a fake 'nix eval
// path:.#lib.credentials' answers with: one empty object per name, which is
// all declareEvalCredentialNames reads (the keys, not the values).
func declareFakeCredentialsJSON(names []string) string {
	credentials := make(map[string]any, len(names))
	for _, name := range names {
		credentials[name] = map[string]any{}
	}
	data, _ := json.Marshal(credentials)
	return string(data)
}

// declareFakeNixMachineCases renders the shell 'case' arms one fake-nix
// script variant below shares: one per machine name in types, answering
// its JSON-quoted type, and a catch-all that fails the way an absent
// inventory entry would.
func declareFakeNixMachineCases(types map[string]string) string {
	var cases strings.Builder
	for machine, machineType := range types {
		fmt.Fprintf(&cases, "    %s) printf '\"%s\"' ;;\n", machine, machineType)
	}
	cases.WriteString("    *) echo \"error: attribute 'machines.$machine.type' missing\" >&2; exit 1 ;;\n")
	return cases.String()
}

// declareInstallFakeNix writes script as the one 'nix' executable on an
// otherwise-unchanged PATH (so 'git', which resolveSecretsCheckout still
// needs, stays reachable).
func declareInstallFakeNix(t *testing.T, script string) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "nix"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// declareFakeNix puts a fake 'nix' executable on PATH that answers 'nix
// eval --json path:.#machines.<name>.type' for every machine named in
// types (failing for an absent one, as a real inventory would), and 'nix
// eval --json path:.#lib.credentials' with an object naming every entry in
// evaluatedCredentialNames — the names declare's own evaluated-inventory
// collision check (declareEvalCredentialNames) must see, independent of
// what credentials.nix's text happens to say.
func declareFakeNix(t *testing.T, types map[string]string, evaluatedCredentialNames []string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" != eval ] || [ \"$2\" != --json ]; then echo \"fake nix: unexpected args: $*\" >&2; exit 1; fi\n" +
		"if [ \"$3\" = 'path:.#lib.credentials' ]; then printf '%s' \"$DECLARE_TEST_CREDENTIALS_JSON\"; exit 0; fi\n" +
		"attr=${3#path:.#machines.}\n" +
		"machine=${attr%.type}\n" +
		"case \"$machine\" in\n" +
		declareFakeNixMachineCases(types) +
		"esac\n"
	declareInstallFakeNix(t, script)
	t.Setenv("DECLARE_TEST_CREDENTIALS_JSON", declareFakeCredentialsJSON(evaluatedCredentialNames))
}

// declareFakeNixFailingCredentialsEval is declareFakeNix except 'nix eval
// path:.#lib.credentials' itself fails, the way a checkout that does not
// evaluate at all would. It pins that declare refuses outright rather than
// falling through to the text checks alone when the evaluated-inventory
// check cannot run.
func declareFakeNixFailingCredentialsEval(t *testing.T, types map[string]string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" != eval ] || [ \"$2\" != --json ]; then echo \"fake nix: unexpected args: $*\" >&2; exit 1; fi\n" +
		"if [ \"$3\" = 'path:.#lib.credentials' ]; then echo 'error: infinite recursion encountered' >&2; exit 1; fi\n" +
		"attr=${3#path:.#machines.}\n" +
		"machine=${attr%.type}\n" +
		"case \"$machine\" in\n" +
		declareFakeNixMachineCases(types) +
		"esac\n"
	declareInstallFakeNix(t, script)
}

// declareFakeNixWithSideEffect is declareFakeNix plus one extra effect: on
// every invocation, before it answers, it appends sideEffectText to the
// file named by sideEffectPath. This is how the concurrent-edit tests
// simulate another process changing a file in the window between declare
// reading it and declare writing it — the only real gap in one run is the
// 'nix eval' calls declare makes (for lib.credentials and for each
// target's machine type), so that is where the simulated edit happens,
// through the real production code path rather than a test-only hook
// inside declare itself.
func declareFakeNixWithSideEffect(t *testing.T, types map[string]string, evaluatedCredentialNames []string, sideEffectPath, sideEffectText string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" != eval ] || [ \"$2\" != --json ]; then echo \"fake nix: unexpected args: $*\" >&2; exit 1; fi\n" +
		"printf '%s' \"$DECLARE_TEST_SIDE_EFFECT_TEXT\" >> \"$DECLARE_TEST_SIDE_EFFECT_PATH\"\n" +
		"if [ \"$3\" = 'path:.#lib.credentials' ]; then printf '%s' \"$DECLARE_TEST_CREDENTIALS_JSON\"; exit 0; fi\n" +
		"attr=${3#path:.#machines.}\n" +
		"machine=${attr%.type}\n" +
		"case \"$machine\" in\n" +
		declareFakeNixMachineCases(types) +
		"esac\n"
	declareInstallFakeNix(t, script)
	t.Setenv("DECLARE_TEST_CREDENTIALS_JSON", declareFakeCredentialsJSON(evaluatedCredentialNames))
	t.Setenv("DECLARE_TEST_SIDE_EFFECT_PATH", sideEffectPath)
	t.Setenv("DECLARE_TEST_SIDE_EFFECT_TEXT", sideEffectText)
}

// declareFixtureEvaluatedCredentialNames is what the fake nix answers 'nix
// eval path:.#lib.credentials' with by default: 'existing-token', matching
// what declareFixtureCredentials actually declares as a literal entry, the
// way a real evaluation of that file would.
var declareFixtureEvaluatedCredentialNames = []string{"existing-token"}

func newDeclareFixtureFilesWithEvaluatedNames(t *testing.T, credentials, secrets, registry string, evaluatedCredentialNames []string) *declareFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
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

	t.Setenv("INVENTORY", t.TempDir())
	declareFakeNix(t, map[string]string{
		"dev-a":     "dev",
		"dev-b":     "dev",
		"privacy-1": "privacy",
		"nexus":     "hypervisor",
	}, evaluatedCredentialNames)
	return fx
}

// newDeclareFixtureFiles is newDeclareFixtureFilesWithEvaluatedNames with
// the evaluated inventory answering exactly declareFixtureEvaluatedCredentialNames
// — the right default for every fixture built from declareFixtureCredentials
// or a text-only variant of it (a split-line or commented rewrite still
// declares the same one name). Tests that need the evaluated inventory to
// disagree with the text call the *WithEvaluatedNames form directly.
func newDeclareFixtureFiles(t *testing.T, credentials, secrets, registry string) *declareFixture {
	t.Helper()
	return newDeclareFixtureFilesWithEvaluatedNames(t, credentials, secrets, registry, declareFixtureEvaluatedCredentialNames)
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
              "type": "git-ls-remote",
              "repo_url": "https://forge.example/allod/example.git"
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
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
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
              "type": "git-ls-remote",
              "repo_url": "https://forge.example/allod/multi.git",
              "credential_context": "the multi-token repository"
            }
          },
          {
            "system": "dev-b",
            "kind": "dev-vm",
            "deployed_path": "/root/.git-credentials",
            "verify": {
              "type": "git-ls-remote",
              "repo_url": "https://forge.example/allod/multi.git",
              "credential_context": "the multi-token repository"
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
		"--verify-repo-url", "https://forge.example/allod/multi.git", "--verify-context", "the multi-token repository",
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

// TestSecretDeclareTargetingNexusReadsHypervisorType pins that 'nexus'
// reaches "nixos-host" through the same inventory lookup as every other
// machine (its type is "hypervisor" in the flake), not a literal name
// special case.
func TestSecretDeclareTargetingNexusReadsHypervisorType(t *testing.T) {
	fx := newDeclareFixture(t)
	out, errText, code := fx.run(t, "host-token",
		"--kind", "service", "--owner", "nexus", "--to", "nexus",
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

// --- --kind is restricted to what declare can write correctly ---

// TestSecretDeclareRefusesGeneratedKinds covers allod/tools#196 review
// finding 1: forge-git and machine-host are real credential-inventory
// kinds, but the shape declare writes for any --kind (public_key null, one
// agenix consumer) is exactly what credential-inventory rejects for them —
// a forge-git entry needs a real public_key and one forge-key-secret plus
// one forgejo-ssh consumer, and machine-host entries are generated by
// hostEntries, never hand-declared. Both are refused, naming the three
// --kind does accept.
func TestSecretDeclareRefusesGeneratedKinds(t *testing.T) {
	for _, kind := range []string{"forge-git", "machine-host"} {
		t.Run(kind, func(t *testing.T) {
			fx := newDeclareFixture(t)
			_, errText, code := fx.run(t, "new-token",
				"--kind", kind, "--owner", "allod-agent", "--to", "dev-a",
				"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "site-check")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			for _, want := range []string{
				"invalid --kind \"" + kind + "\"",
				"expected one of: user, agent, service",
				"generated by the template, not declared",
			} {
				if !strings.Contains(errText, want) {
					t.Errorf("stderr lacks %q\ngot: %q", want, errText)
				}
			}
			fx.assertUntouched(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
		})
	}
}

// --- Collisions ---

func TestSecretDeclareCollisions(t *testing.T) {
	validArgs := []string{
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote",
		"--verify-repo-url", "https://forge.example/allod/example.git",
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
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "secrets.nix: a publicKeys line for 'secrets/orphan.age' already exists"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	fx.assertUntouched(t, declareFixtureCredentials, secrets, declareFixtureRegistry)
}

// TestSecretDeclareCredentialsCollisionAcrossLines covers allod/tools#196
// review finding 3: the collision regex must match across whitespace
// including newlines, not just spaces and tabs on one line, or an existing
// declaration split across lines slips through the gate and a duplicate
// gets inserted.
func TestSecretDeclareCredentialsCollisionAcrossLines(t *testing.T) {
	credentials := strings.Replace(declareFixtureCredentials,
		"  existing-token = {", "  existing-token\n    = {", 1)
	fx := newDeclareFixtureFiles(t, credentials, declareFixtureSecrets, declareFixtureRegistry)
	_, errText, code := fx.run(t, "existing-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "credentials.nix: an entry named 'existing-token' already exists"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	fx.assertUntouched(t, credentials, declareFixtureSecrets, declareFixtureRegistry)
}

// TestSecretDeclareSecretsLineCollisionAcrossLines is the secrets.nix half
// of the same finding: '.publicKeys' and '=' split across lines must still
// be found. It uses an orphan line (no matching credentials.nix entry or
// registry group, as TestSecretDeclareSecretsLineCollisionAlone does) so
// the refusal it pins can only come from the secrets.nix check.
func TestSecretDeclareSecretsLineCollisionAcrossLines(t *testing.T) {
	secrets := declareFixtureSecrets[:len(declareFixtureSecrets)-len("}\n")] +
		"  \"secrets/orphan.age\"\n    .publicKeys\n    = [ hostKey ];\n}\n"
	fx := newDeclareFixtureFiles(t, declareFixtureCredentials, secrets, declareFixtureRegistry)
	_, errText, code := fx.run(t, "orphan",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "secrets.nix: a publicKeys line for 'secrets/orphan.age' already exists"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	fx.assertUntouched(t, declareFixtureCredentials, secrets, declareFixtureRegistry)
}

// TestSecretDeclareCredentialsCollisionAcrossAComment covers allod/tools#196
// review finding 5: the collision regexes span newlines but must also span
// a nix line comment sitting between the name and '=' — a comment hides
// nothing from nix either, so it must hide nothing from the collision
// check.
func TestSecretDeclareCredentialsCollisionAcrossAComment(t *testing.T) {
	credentials := strings.Replace(declareFixtureCredentials,
		"  existing-token = {", "  existing-token # already active\n    = {", 1)
	fx := newDeclareFixtureFiles(t, credentials, declareFixtureSecrets, declareFixtureRegistry)
	_, errText, code := fx.run(t, "existing-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "credentials.nix: an entry named 'existing-token' already exists"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	fx.assertUntouched(t, credentials, declareFixtureSecrets, declareFixtureRegistry)
}

// TestSecretDeclareSecretsLineCollisionAcrossAComment is the secrets.nix
// half of the same finding.
func TestSecretDeclareSecretsLineCollisionAcrossAComment(t *testing.T) {
	secrets := declareFixtureSecrets[:len(declareFixtureSecrets)-len("}\n")] +
		"  \"secrets/orphan.age\" # already declared\n    .publicKeys = [ hostKey ];\n}\n"
	fx := newDeclareFixtureFiles(t, declareFixtureCredentials, secrets, declareFixtureRegistry)
	_, errText, code := fx.run(t, "orphan",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "secrets.nix: a publicKeys line for 'secrets/orphan.age' already exists"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	fx.assertUntouched(t, declareFixtureCredentials, secrets, declareFixtureRegistry)
}

// TestSecretDeclareRefusesWhenOnlyTheEvaluatedInventoryKnowsTheName covers
// allod/tools#196 review finding 3: credentials.nix is 'hostEntries //
// activeEntries // stagedEntries // forgeGitEntries // { ...literal...;
// }', so a name produced by one of the generated attrsets is invisible to
// the textual scan, which only sees the literal block. This fixture's
// credentials.nix text names only 'existing-token', but the fake
// evaluated inventory also knows 'generated-only-token' — standing in for
// a name credentials.nix generates rather than writes out — and that must
// refuse just as a textual collision would, naming the evaluated source.
func TestSecretDeclareRefusesWhenOnlyTheEvaluatedInventoryKnowsTheName(t *testing.T) {
	fx := newDeclareFixtureFilesWithEvaluatedNames(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry,
		[]string{"existing-token", "generated-only-token"})
	_, errText, code := fx.run(t, "generated-only-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "lib.credentials (evaluated): an entry named 'generated-only-token' already exists"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	fx.assertUntouched(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
}

// TestSecretDeclareRefusesWhenCredentialsEvalFails covers the other half of
// finding 3: "a failed evaluation refuses; it never falls through to the
// text checks alone." A name absent from every text file would otherwise
// sail through the textual checks; with the evaluated-inventory check
// itself failing (the checkout does not evaluate, say), declare must
// refuse outright rather than treating the text checks as sufficient on
// their own.
func TestSecretDeclareRefusesWhenCredentialsEvalFails(t *testing.T) {
	fx := newDeclareFixture(t)
	declareFakeNixFailingCredentialsEval(t, map[string]string{"dev-a": "dev"})
	_, errText, code := fx.run(t, "new-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "could not evaluate"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	if strings.Contains(errText, "already exists") {
		t.Errorf("a failed evaluation must refuse outright, not report a (non-)collision: %q", errText)
	}
	fx.assertUntouched(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
}

// --- Missing and invalid flags ---

func TestSecretDeclareFlagErrors(t *testing.T) {
	base := map[string]string{
		"--kind": "agent", "--owner": "allod-agent", "--to": "dev-a",
		"--format": "raw-forgejo-token", "--deployed-path": "/root/.token", "--verify": "git-ls-remote",
		"--verify-repo-url": "https://forge.example/allod/example.git",
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
		{"missing --verify-repo-url", buildArgs(nil, "--verify-repo-url"), "--verify git-ls-remote requires --verify-repo-url"},
		{"verify-repo-url misuse", buildArgs(map[string]string{"--verify": "site-check"}, ""), "--verify-repo-url and --verify-context apply only to --verify git-ls-remote"},
		{"verify-user misuse", buildArgs(map[string]string{"--verify-user": "root"}, ""), "--verify-user applies only to --verify forge-token-verify"},
		{
			"forge-token-verify needs verify-user",
			[]string{"new-token", "--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
				"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "forge-token-verify"},
			"--verify forge-token-verify requires --verify-user",
		},
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

// --- Injection through flag values ---

// TestSecretDeclareRefusesInjectionThroughStringFlags covers allod/tools#196
// review finding 1: every flag value that lands in generated nix or JSON
// text is validated as a shape before it reaches any template, so a value
// built to break out of its quoting is refused as a usage error rather
// than landing as attacker-controlled nix or JSON. --owner is the sharpest
// case (interpolated raw into a double-quoted nix string in
// credentials.nix); --to is the other one that reaches nix text (the
// 'machines.<name>.type' attribute path the inventory is evaluated at, and
// the secrets.nix recipient line); --kind, --account, and --ui-token-name
// are held to the same identifier pattern, and --deployed-path to its own
// absolute-path shape, for the same reason even though they reach only
// JSON today. --verify-repo-url needs a host as well as the scheme,
// because rotate-token prints it into 'git ls-remote <repo-url> HEAD'.
func TestSecretDeclareRefusesInjectionThroughStringFlags(t *testing.T) {
	base := map[string]string{
		"--kind": "agent", "--owner": "allod-agent", "--to": "dev-a",
		"--format": "raw-forgejo-token", "--deployed-path": "/root/.token", "--verify": "git-ls-remote",
		"--verify-repo-url": "https://forge.example/allod/example.git",
	}
	buildArgs := func(overrides map[string]string) []string {
		args := []string{"new-token"}
		for flag, value := range base {
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
		{
			"owner breaks out of a nix string",
			buildArgs(map[string]string{"--owner": `foo"; kind = "bar`}),
			`invalid --owner "foo\"; kind = \"bar"`,
		},
		{
			"owner nix interpolation",
			buildArgs(map[string]string{"--owner": `${builtins.abort "x"}`}),
			"invalid --owner",
		},
		{
			"account breaks out of a nix string",
			buildArgs(map[string]string{"--service": "forgejo", "--account": `x"; y = "z`, "--ui-token-name": "spare"}),
			"invalid --account",
		},
		{
			"ui-token-name breaks out of a nix string",
			buildArgs(map[string]string{"--service": "forgejo", "--account": "allod-agent", "--ui-token-name": `x"; y = "z`}),
			"invalid --ui-token-name",
		},
		{
			"deployed-path is not absolute",
			buildArgs(map[string]string{"--deployed-path": "root/.token"}),
			"invalid --deployed-path",
		},
		{
			"deployed-path carries a dollar sign",
			buildArgs(map[string]string{"--deployed-path": "/root/$HOME/.token"}),
			"invalid --deployed-path",
		},
		{
			"deployed-path carries a quote",
			buildArgs(map[string]string{"--deployed-path": `/root/".token`}),
			"invalid --deployed-path",
		},
		{
			"deployed-path carries whitespace",
			buildArgs(map[string]string{"--deployed-path": "/root/ .token"}),
			"invalid --deployed-path",
		},
		{
			"to is a quoted name",
			buildArgs(map[string]string{"--to": `"dev-a"`}),
			"invalid --to",
		},
		{
			"to carries a dollar sign",
			buildArgs(map[string]string{"--to": "${builtins.abort \"x\"}"}),
			"invalid --to",
		},
		{
			"to carries whitespace",
			buildArgs(map[string]string{"--to": "dev-a dev-b"}),
			"invalid --to",
		},
		{
			"verify-repo-url is not https",
			buildArgs(map[string]string{"--verify-repo-url": "http://forge.example/allod/example.git"}),
			"invalid --verify-repo-url",
		},
		{
			"verify-repo-url has no host",
			buildArgs(map[string]string{"--verify-repo-url": "https://"}),
			"invalid --verify-repo-url",
		},
		{
			"verify-repo-url host starts with a slash",
			buildArgs(map[string]string{"--verify-repo-url": "https:///allod/example.git"}),
			"invalid --verify-repo-url",
		},
		{
			"verify-repo-url host starts with a query",
			buildArgs(map[string]string{"--verify-repo-url": "https://?query"}),
			"invalid --verify-repo-url",
		},
		{
			"verify-repo-url carries a dollar sign",
			buildArgs(map[string]string{"--verify-repo-url": "https://forge.example/$USER/example.git"}),
			"invalid --verify-repo-url",
		},
		{
			"verify-context carries a control character",
			buildArgs(map[string]string{"--verify-context": "the example\trepository"}),
			"invalid --verify-context",
		},
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

// --- Machine absent from the inventory registry ---

func TestSecretDeclareRefusesUnknownMachine(t *testing.T) {
	fx := newDeclareFixture(t)
	_, errText, code := fx.run(t, "new-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "ghost-vm",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	for _, want := range []string{"could not read the type of machine 'ghost-vm'", "accepted types: dev, privacy, hypervisor, service"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr lacks %q\ngot: %q", want, errText)
		}
	}
	fx.assertUntouched(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
}

// TestSecretDeclareRefusesUnexpectedMachineType covers the third failure
// mode declareTargetKind names: the inventory flake answers for the
// machine, but with a type outside the four the flake itself enforces.
func TestSecretDeclareRefusesUnexpectedMachineType(t *testing.T) {
	fx := newDeclareFixtureFiles(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
	t.Setenv("INVENTORY", t.TempDir())
	declareFakeNix(t, map[string]string{"dev-a": "dev", "weird-vm": "laptop"}, declareFixtureEvaluatedCredentialNames)
	_, errText, code := fx.run(t, "new-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "weird-vm",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	for _, want := range []string{"machine 'weird-vm'", "has type 'laptop'", "accepted types: dev, privacy, hypervisor, service"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr lacks %q\ngot: %q", want, errText)
		}
	}
	fx.assertUntouched(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
}

// --- The JSON re-parse guard ---

// TestDeclareInsertRegistryGroupReparseGuard pins declareInsertRegistryGroup's
// own mechanics directly, not through secretDeclare: a registry text whose
// final '}' closes an unterminated string, not the top-level object, still
// satisfies the mechanical "ends in a '}'" precondition insertion works
// from, so the insertion itself succeeds — but the result must still fail
// json.Valid.
//
// This has to be a unit test of the function, not an end-to-end one,
// because no fixture reaches secretDeclare's post-build json.Valid guard
// through the CLI: any registryText malformed enough to survive insertion
// as invalid JSON already fails the upfront json.Unmarshal secretDeclare
// runs for collision-checking (TestSecretDeclareRefusesWhenRegistryDoesNotParse,
// below, pins that path), and for genuinely valid JSON input,
// declareInsertRegistryGroup's insertion point is always the end of a
// complete prior token, so appending a comma there cannot produce invalid
// JSON — this function's own analysis, not an assumption. The post-build
// guard is real defense-in-depth against a future bug in that insertion
// logic; this test is what actually exercises it, since the CLI cannot.
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

// TestSecretDeclareRefusesWhenRegistryDoesNotParse is the CLI-level half
// review finding 6 asked for: it drives secretDeclare itself against the
// sabotage registry fixture and asserts both the refusal message and that
// all three files are byte-identical afterwards, so a change that broke
// declare's actual refuse-and-restore behavior would fail here even if the
// unit test above kept passing.
func TestSecretDeclareRefusesWhenRegistryDoesNotParse(t *testing.T) {
	fx := newDeclareFixtureFiles(t, declareFixtureCredentials, declareFixtureSecrets, declareSabotageRegistry)
	_, errText, code := fx.run(t, "new-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
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
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	if want := "the edit would leave unbalanced braces; refusing to write"; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	fx.assertUntouched(t, declareSabotageCredentials, declareFixtureSecrets, declareFixtureRegistry)
}

// --- Atomic, all-or-nothing writes ---

// TestDeclareAtomicWriteReplacesInFullOrNotAtAll pins declareAtomicWrite's
// own contract directly: a successful call leaves the new bytes in full,
// and a call whose temp file cannot be created (the directory does not
// exist) leaves the original file completely untouched and returns an
// error, rather than a truncated file and a panic.
func TestDeclareAtomicWriteReplacesInFullOrNotAtAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := declareAtomicWrite(path, []byte("replaced\n")); err != nil {
		t.Fatalf("declareAtomicWrite: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "replaced\n" {
		t.Fatalf("file = %q, err = %v, want \"replaced\\n\"", got, err)
	}

	// A target whose directory does not exist cannot even get a temp file
	// created, so the failure happens before any rename is attempted.
	if err := declareAtomicWrite(filepath.Join(dir, "no-such-directory", "g.txt"), []byte("replaced\n")); err == nil {
		t.Fatal("declareAtomicWrite into a missing directory returned no error")
	}
}

// TestDeclareUnchangedSinceDetectsAConcurrentEdit pins declareUnchangedSince
// directly: it reports true against the bytes a file was just written
// with, and false the moment those bytes change underneath.
func TestDeclareUnchangedSinceDetectsAConcurrentEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	original := []byte("original\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	if unchanged, err := declareUnchangedSince(path, original); err != nil || !unchanged {
		t.Fatalf("unchanged = %v, err = %v, want true, nil", unchanged, err)
	}
	if err := os.WriteFile(path, []byte("edited by someone else\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if unchanged, err := declareUnchangedSince(path, original); err != nil || unchanged {
		t.Fatalf("unchanged = %v, err = %v, want false, nil", unchanged, err)
	}
}

// TestSecretDeclareRefusesAndRestoresWhenAFileChangesUnderneath drives the
// whole command end-to-end for review findings 4 and 5: it uses
// declareFakeNixWithSideEffect so the fake 'nix' invocation that derives
// dev-a's target kind — the one real gap between declare reading every
// file and declare writing any of them — also appends to secrets.nix,
// simulating another process editing the checkout in that window.
// credentials.nix, written first, is replaced successfully (declare cannot
// know at that point that secrets.nix has changed); secrets.nix's own
// re-read-before-replace catches the edit and refuses, and that refusal
// must restore credentials.nix to its original bytes — proving both the
// concurrent-edit guard and the restore-already-replaced-files path in one
// real run, not through a Go-level seam.
func TestSecretDeclareRefusesAndRestoresWhenAFileChangesUnderneath(t *testing.T) {
	fx := newDeclareFixtureFiles(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
	t.Setenv("INVENTORY", t.TempDir())
	secretsPath := filepath.Join(fx.checkout, "secrets.nix")
	declareFakeNixWithSideEffect(t, map[string]string{"dev-a": "dev"}, declareFixtureEvaluatedCredentialNames, secretsPath, "\n# edited concurrently\n")

	_, errText, code := fx.run(t, "new-token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
	if code == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	for _, want := range []string{"secrets.nix changed since declare read it", "restored"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr lacks %q\ngot: %q", want, errText)
		}
	}
	if got, err := os.ReadFile(filepath.Join(fx.checkout, "credentials.nix")); err != nil || string(got) != declareFixtureCredentials {
		t.Errorf("credentials.nix was not restored to its original bytes: err=%v got=%q", err, got)
	}
	if got, err := os.ReadFile(filepath.Join(fx.checkout, "forgejo-token-groups.json")); err != nil || string(got) != declareFixtureRegistry {
		t.Errorf("forgejo-token-groups.json changed: err=%v got=%q", err, got)
	}
}

// --- Name validation ---

func TestDeclareNamePattern(t *testing.T) {
	for _, valid := range []string{"a", "agent-pr-token", "a1-b2"} {
		if !declareNamePattern.MatchString(valid) {
			t.Errorf("%q should match the name pattern", valid)
		}
	}
	// A leading digit is refused: the name becomes an unquoted nix
	// attribute key, and '1token = { ... }' does not parse as nix.
	for _, invalid := range []string{"", "-a", "Agent", "agent_token", "agent token", "1token", "0"} {
		if declareNamePattern.MatchString(invalid) {
			t.Errorf("%q should not match the name pattern", invalid)
		}
	}
}

// TestSecretDeclareRefusesALeadingDigitName drives the CLI, not just the
// regex: a name starting with a digit is refused before anything is read
// or written, since credentials.nix would not evaluate an unquoted
// '1token = { ... }' attribute.
func TestSecretDeclareRefusesALeadingDigitName(t *testing.T) {
	fx := newDeclareFixture(t)
	_, errText, code := fx.run(t, "1token",
		"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
		"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "invalid credential name \"1token\""; !strings.Contains(errText, want) {
		t.Errorf("stderr lacks %q\ngot: %q", want, errText)
	}
	fx.assertUntouched(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
}

// TestSecretDeclareRefusesNixKeywordNames covers allod/tools#196 review
// finding 4: every name in declareNixKeywords matches declareNamePattern
// (lowercase letters only) but is still not usable as an unquoted nix
// attribute name — 'let = { ... };' is a syntax error, not an attribute
// named "let".
func TestSecretDeclareRefusesNixKeywordNames(t *testing.T) {
	for _, keyword := range declareNixKeywords {
		t.Run(keyword, func(t *testing.T) {
			if !declareNamePattern.MatchString(keyword) {
				t.Fatalf("%q should match declareNamePattern, or this test proves nothing about the keyword check", keyword)
			}
			fx := newDeclareFixture(t)
			_, errText, code := fx.run(t, keyword,
				"--kind", "agent", "--owner", "allod-agent", "--to", "dev-a",
				"--format", "raw-forgejo-token", "--deployed-path", "/root/.token", "--verify", "git-ls-remote", "--verify-repo-url", "https://forge.example/allod/example.git")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if want := "invalid credential name \"" + keyword + "\": a nix keyword"; !strings.Contains(errText, want) {
				t.Errorf("stderr lacks %q\ngot: %q", want, errText)
			}
			fx.assertUntouched(t, declareFixtureCredentials, declareFixtureSecrets, declareFixtureRegistry)
		})
	}
}

// --- INVENTORY override ---

// TestInventoryCheckoutFromEnvironment covers allod/tools#196 review
// finding 3: INVENTORY=/ used to be trimmed to the empty string, which
// 'nix eval' would then read as "run in the process working directory"
// rather than "run in /". filepath.Clean keeps the root a root and still
// strips a trailing slash everywhere else.
func TestInventoryCheckoutFromEnvironment(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"/", "/"},
		{"/tmp/x/", "/tmp/x"},
		{"/tmp/x", "/tmp/x"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("INVENTORY", tc.value)
			if got := inventoryCheckout(); got != tc.want {
				t.Errorf("inventoryCheckout() = %q, want %q", got, tc.want)
			}
		})
	}
}
