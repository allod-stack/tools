//go:build !secret

package main

// The other half of the build-tag proof for the secret namespace.
// secret_test.go asserts that a build made with -tags secret carries it;
// this file asserts that a build without the tag carries no such word — not
// that 'allod secret create' refuses, but that it is unknown, which is the
// difference between a machine that cannot land a secret and a machine
// that lands one badly. That absence is the whole boundary: an agent's
// machine builds without the tag, so nothing there can be talked into it.
//
// Both files are compiled by 'go test' runs that exclude each other, so the
// pair only holds if both runs happen: the untagged run and the
// '-tags secret' run.

import (
	"strings"
	"testing"
)

func TestSecretNamespaceIsAbsentWithoutTheTag(t *testing.T) {
	if _, ok := lookupNamespace("secret"); ok {
		t.Fatal("the secret namespace is registered in a build without -tags secret")
	}
	out, _, _ := runAllod(t)
	if strings.Contains(out, "secret") {
		t.Errorf("top-level usage names a namespace this build does not carry:\n%s", out)
	}
	for _, args := range [][]string{
		{"secret"},
		{"secret", "create", "new-token"},
		{"secret", "rekey", "new-token"},
		{"secret", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdoutText, stderrText, code := runAllod(t, args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if want := "allod: unknown command namespace: secret\n"; stderrText != want {
				t.Errorf("stderr = %q, want %q", stderrText, want)
			}
			if stdoutText != "" {
				t.Errorf("stdout = %q, want empty", stdoutText)
			}
		})
	}
}
