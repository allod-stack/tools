package main

// Credential commands: `forge auth status`, `forge token verify`, and the
// `forge project` stub. Transliterated from forge lines 1927-2017.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"forge.anarch.diy/allod/tools/internal/forgeapi"
)

// projectUnavailable mirrors project_unavailable (forge line 1927). The
// dispatcher passes the command word back in, so a --help anywhere in the
// invocation still reaches the usage text.
func projectUnavailable(args []string) {
	if containsHelpFlag(args) {
		commandUsage("project")
		return
	}
	die("project commands are unavailable: this Forgejo API does not expose repository project endpoints")
}

// verifyTokenHTTP mirrors verify_token_http (forge line 1934): GET /user with
// the given token and nothing else. It is deliberately not api(): no
// Content-Type header is sent, no configured token is loaded, and a non-2xx
// status is the answer rather than a failure.
//
// A transport failure prints the same line bash does and ends the run, which
// is what errexit does to bash at both call sites.
func verifyTokenHTTP(candidate string) (status int, login string) {
	// The path is fixed here, so only FORGE_URL can carry a byte curl's URL
	// parser refuses -- but this curl is as subject to that as api()'s is.
	if curlRejectsURL(forgeURL + "/api/v1/user") {
		fmt.Fprint(stderr, "forge: GET /user failed: curl exit 3\n")
		exit(3)
	}

	client := &forgeapi.Client{BaseURL: forgeURL, Token: candidate}
	resp, err := client.Do(forgeapi.Request{Method: "GET", Path: "/api/v1/user"})
	if err != nil {
		rc := curlExitCode(err)
		fmt.Fprintf(stderr, "forge: GET /user failed: curl exit %d\n", rc)
		exit(rc)
	}

	body := trimTrailingNewlines(resp.Body)
	if resp.Status == 200 && !jsonIsEmpty(body) {
		// VERIFY_LOGIN=$(printf '%s' "$body" | jq -r '.login'): a login of
		// "alice\n" reaches "Authenticated as alice (...)" with the newline
		// already gone.
		login = chompNewlines(jqString(jsonField(mustJSON(body), "login")))
	}
	return resp.Status, login
}

// verifyFailureReason mirrors the case statement both callers share.
func verifyFailureReason(status int) string {
	switch status {
	case 401:
		return "unauthorized"
	case 403:
		return "forbidden"
	default:
		return fmt.Sprintf("HTTP %d", status)
	}
}

// tokenVerify mirrors token_verify (forge line 1954). The candidate token
// comes from stdin and only from stdin: the configured credential is never
// read, so a token can be checked without installing it first.
func tokenVerify(args []string) {
	if containsHelpFlag(args) {
		commandUsage("token verify")
		return
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			die("unknown option for token verify: %s", arg)
		}
		die("unexpected argument for token verify: %s", arg)
	}

	if stdinIsTerminal() {
		die("token verify reads from stdin; pipe a token to it")
	}

	// bash: validate_token "$(cat)" "stdin" — the command substitution strips
	// NUL bytes as it captures and every trailing newline before validation
	// sees the value.
	data, _ := io.ReadAll(stdin)
	candidate := validateToken(chompNewlines(string(stripNULs(data))), "stdin")

	status, login := verifyTokenHTTP(candidate)
	if status == 200 {
		fmt.Fprintf(stdout, "Token valid: authenticated as %s\n", login)
		return
	}
	fmt.Fprintf(stderr, "Token invalid: %s\n", verifyFailureReason(status))
	exit(1)
}

// authStatus mirrors auth_status (forge line 1985): verify the credential the
// CLI would actually use, and name where it came from.
func authStatus(args []string) {
	if containsHelpFlag(args) {
		commandUsage("auth status")
		return
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			die("unknown option for auth status: %s", arg)
		}
		die("unexpected argument for auth status: %s", arg)
	}

	loadToken()

	// The token file is the only credential source (allod/tools#57), so it
	// is always what the report names.
	status, login := verifyTokenHTTP(token)
	if status == 200 {
		fmt.Fprintf(stdout, "Authenticated as %s (%s)\n", login, forgeTokenFile)
		return
	}
	fmt.Fprintf(stderr, "Authentication failed: %s (%s)\n", verifyFailureReason(status), forgeTokenFile)
	exit(1)
}

// stdinIsTerminal mirrors `[ -t 0 ]`, which is isatty(0) and nothing else.
//
// isatty is TCGETS on the descriptor: a terminal answers with its line
// discipline settings, anything else fails with ENOTTY. That is the whole
// test, so /dev/null, /dev/zero and /dev/urandom are all "not a terminal"
// here exactly as they are to bash -- `forge token verify </dev/zero` reads
// forever rather than printing the hint, and `</dev/null` reaches the "stdin
// is empty" error. A character-device check cannot make that distinction; it
// called every one of them a terminal.
//
// The stdin seam is an io.Reader, so anything a test installs that is not an
// *os.File is not a terminal by construction.
func stdinIsTerminal() bool {
	f, ok := stdin.(*os.File)
	if !ok {
		return false
	}
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(),
		uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}
