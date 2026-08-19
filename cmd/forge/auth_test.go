package main

// Port of tests/forge/auth.sh.
//
// Bash cases intentionally omitted as already covered (see
// cmd/forge/harness_test.go: TestAuthStatus, TestLoadTokenErrors,
// TestAuthTokenIsNotACommand):
//
//   - "auth status: valid configured token (env var)"
//     -> TestAuthStatus "valid env token" (also checks the Authorization
//     header and the absent Content-Type, which bash's suite could not).
//   - "auth status: invalid configured token"
//     -> TestAuthStatus's failures table (401 -> "unauthorized").
//   - "auth status: valid token from file"
//     -> TestAuthStatus "token file names the path".
//   - "auth status: no credential source"
//     -> TestLoadTokenErrors "no credential source".
//   - "auth status: rejects --token"
//     -> TestAuthStatus "rejects options and arguments".
//   - "negative: no auth token command"
//     -> TestAuthTokenIsNotACommand.
//   - "auth status: rejects token with newline in configured credential"
//     -> TestLoadTokenErrors "env newline" (run through `auth status`
//     exactly as bash does; the harness also covers quote and backslash,
//     which bash's auth.sh does not exercise here, only token.sh does).
//   - "auth status: strips carriage return from configured token"
//     -> TestAuthStatus "carriage return is stripped".
//
// Every one of the above is pinned byte-exact in harness_test.go, where
// bash's suite only checked substrings via assert_contains.
//
// One case remains unported: bash separately asserts that `--token-file` is
// rejected, a distinct flag from `--token`. authStatus (authcmd.go) rejects
// any argument starting with "-" through one generic branch, so this is the
// exact same code path "rejects --token" already exercises under a
// different flag name — a thin but faithful 1:1 port, not new coverage.

import "testing"

func TestAuthStatusRejectsTokenFileFlag(t *testing.T) {
	useToken(t, fakeToken)
	_, errText, code := runForge(t, "auth", "status", "--token-file", "/dev/null")
	want := "forge: unknown option for auth status: --token-file\n"
	if errText != want || code != 1 {
		t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
	}
}
