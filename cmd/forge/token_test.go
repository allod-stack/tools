package main

// Retained token scenarios from the retired shell suite.
//
// Bash cases intentionally omitted as already covered (see
// cmd/forge/harness_test.go: TestTokenVerify):
//
//   - "token verify: stdin valid token" -> "valid token from stdin" (also
//     checks that the configured credential is never sent).
//   - "token verify: stdin invalid token" -> "invalid token".
//   - "token verify: no stdin" -> "empty stdin".
//   - "token verify: rejects --token" -> "rejects options and arguments".
//   - "token verify: strips trailing newline from stdin"
//     -> "trailing whitespace is stripped" (the "\n" case; the harness also
//     covers "\r" and "\n\n", which bash's suite tests separately below).
//   - "token verify: works without configured token"
//     -> "works without a configured credential".
//   - "token verify: rejects newline in token" -> the invalid-chars table, "newline".
//   - "token verify: rejects double quote in token" -> the invalid-chars table, "quote".
//   - "token verify: rejects backslash in token" -> the invalid-chars table, "backslash".
//   - "token verify: strips carriage return"
//     -> "trailing whitespace is stripped" (the "\r" case).
//
// Every one of the above is pinned byte-exact in harness_test.go, where
// bash's suite only checked substrings via assert_contains.
//
// Not portable: "token verify: TTY detection" spawns `script(1)` to allocate
// a real pseudo-terminal and checks that a bare invocation on a TTY prints
// the "reads from stdin" hint instead of trying to read. This package never
// spawns a subprocess, so no PTY-allocating helper is available, and
// stdinIsTerminal() (cmd/forge/authcmd.go) is a real isatty — TCGETS on the
// stdin descriptor — so the only thing that can drive it true is an actual
// terminal on the stdin seam, which means a PTY or the process's own
// controlling terminal, neither reliably available under `go test`. Left
// unported; the false path is exercised implicitly by every other test in
// this file, all of which use useStdin, and pinned explicitly for the
// character devices that are not terminals by TestStdinIsTerminal
// (parity_test.go) — /dev/null, so `token verify </dev/null` reaches "stdin
// is empty", and /dev/zero, so it reads rather than printing the hint.
//
// One case remains unported: bash separately asserts that `--token-file` is
// rejected, a distinct flag from `--token`. tokenVerify (authcmd.go) rejects
// any argument starting with "-" through one generic branch, so this is the
// exact same code path "rejects --token" already exercises under a
// different flag name — a thin but faithful 1:1 port, not new coverage.

import "testing"

func TestTokenVerifyRejectsTokenFileFlag(t *testing.T) {
	useStdin(t, fakeToken)
	_, errText, code := runForge(t, "token", "verify", "--token-file", "/dev/null")
	want := "forge: unknown option for token verify: --token-file\n"
	if errText != want || code != 1 {
		t.Errorf("got (%q, %d), want (%q, 1)", errText, code, want)
	}
}
