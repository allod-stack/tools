//go:build !secret

package main

// The other half of the build-tag proof for the secret namespace.
// secret_test.go asserts that a build made with -tags secret carries
// 'declare', 'create', and 'rekey'; this file asserts that a build without
// the tag carries 'declare' alone — not that 'create' and 'rekey' refuse,
// but that those two words mean nothing, which is the difference between a
// machine that cannot land a secret and a machine that lands one badly.
// 'allod secret declare' itself is present in both builds: it writes no
// ciphertext and reads no identity, so it runs wherever agents run.
//
// Both files are compiled by 'go test' runs that exclude each other, so the
// pair only holds if both runs happen: the untagged run and the
// '-tags secret' run.

import (
	"strings"
	"testing"
)

// TestSecretNamespaceExistsWithDeclareOnly pins the shape an untagged build
// carries: the 'secret' namespace is registered, but its command table
// holds exactly 'declare'.
func TestSecretNamespaceExistsWithDeclareOnly(t *testing.T) {
	if _, ok := lookupNamespace("secret"); !ok {
		t.Fatal("the secret namespace is not registered in an untagged build")
	}
	if got := len(secretCommands); got != 1 {
		t.Fatalf("secretCommands has %d entries in an untagged build, want 1: %+v", got, secretCommands)
	}
	if secretCommands[0].name != "declare" {
		t.Errorf("the one untagged secret command is %q, want %q", secretCommands[0].name, "declare")
	}
}

// TestUntaggedSecretCreateRekeyAreUnknown pins the behaviour an operator
// sees: 'allod secret create' and 'rekey' on a machine that holds no age
// identity fail exactly the way a typo does, saying so in the same words
// 'unknown secret command' always has and then, like any unknown secret
// command, printing the short usage rather than nothing.
func TestUntaggedSecretTaggedCommandsAreUnknown(t *testing.T) {
	for _, command := range []string{"create", "rekey", "migrate", "rotate"} {
		t.Run(command, func(t *testing.T) {
			out, errText, code := runAllod(t, "secret", command, "new-token")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			want := "allod: unknown secret command: " + command + "\n" + secretShortUsage()
			if errText != want {
				t.Errorf("stderr = %q, want %q", errText, want)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
		})
	}
}

// TestUntaggedSecretUsageListsOnlyDeclare pins that an untagged build's own
// 'allod secret' usage advertises exactly what it carries: the create and
// rekey usage lines and summaries are absent, and declare's is present.
func TestUntaggedSecretUsageListsOnlyDeclare(t *testing.T) {
	for _, args := range [][]string{{"secret"}, {"secret", "--help"}, {"secret", "-h"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdoutText, stderrText, _ := runAllod(t, args...)
			out := stdoutText + stderrText
			if !strings.Contains(out, "allod secret declare <name>") {
				t.Errorf("usage does not contain the declare usage line\ngot: %q", out)
			}
			if !strings.Contains(out, "declare  Write a credential's non-secret half") {
				t.Errorf("usage does not contain the declare summary\ngot: %q", out)
			}
			for _, absent := range []string{
				"allod secret create <name> [<checkout>]",
				"allod secret rekey <name> [<checkout>]",
				"allod secret migrate <name> [<checkout>]",
				"allod secret rotate <name> [<checkout>]",
				"create   Encrypt a pending credential's value",
				"rekey    Re-encrypt an existing credential",
			} {
				if strings.Contains(out, absent) {
					t.Errorf("untagged usage names a command this build does not carry: %q\ngot: %q", absent, out)
				}
			}
		})
	}
}

// TestUntaggedSecretBareInvocationHasNoDetailProse pins the short-usage
// contract in the build that carries only 'declare': a bare 'allod secret'
// stays short even with one command registered, and '--help' still carries
// the detail prose the bare form omits.
func TestUntaggedSecretBareInvocationHasNoDetailProse(t *testing.T) {
	const detailOnly = "writes text, not ciphertext, reads no identity, and never commits"

	_, errText, code := runAllod(t, "secret")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if strings.Contains(errText, detailOnly) {
		t.Errorf("bare invocation printed declare's detail prose %q\ngot: %q", detailOnly, errText)
	}
	if want := "\nRun 'allod secret --help' for details.\n"; !strings.HasSuffix(errText, want) {
		t.Errorf("bare invocation stderr does not end with %q\ngot: %q", want, errText)
	}

	out, errText, code := runAllod(t, "secret", "--help")
	if code != 0 || errText != "" {
		t.Fatalf("exit=%d stderr=%q, want success with empty stderr", code, errText)
	}
	if !strings.Contains(out, detailOnly) {
		t.Errorf("'secret --help' is missing declare's detail prose %q\ngot: %q", detailOnly, out)
	}
}

// TestUntaggedSecretDeclareArgumentErrorPrintsOwnUsageOnly pins the
// per-command argument-error contract in the build that carries only
// 'declare': an unknown option prints the one-line message, declare's own
// Usage: line, and the pointer to declare's own '--help', but none of
// declare's detail prose — that stays behind '--help', the same way it does
// for the namespace itself.
func TestUntaggedSecretDeclareArgumentErrorPrintsOwnUsageOnly(t *testing.T) {
	_, errText, code := runAllod(t, "secret", "declare", "new-token", "--bogus")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{
		"allod: unknown option for secret declare: --bogus\n",
		"Usage:\n  allod secret declare ",
		"\nRun 'allod secret declare --help' for details.\n",
	} {
		if !strings.Contains(errText, want) {
			t.Errorf("argument error does not contain %q\ngot: %q", want, errText)
		}
	}
	if detailOnly := "writes text, not ciphertext, reads no identity, and never commits"; strings.Contains(errText, detailOnly) {
		t.Errorf("argument error printed declare's detail prose %q\ngot: %q", detailOnly, errText)
	}
}

// TestSecretIsListedInTopLevelUsage checks that the bare 'allod' usage
// names the secret namespace in every build, without claiming this build
// carries create or rekey.
func TestSecretIsListedInTopLevelUsage(t *testing.T) {
	out, _, _ := runAllod(t)
	if !strings.Contains(out, "secret") {
		t.Errorf("top-level usage does not mention the secret namespace\ngot: %q", out)
	}
}
