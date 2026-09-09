package main

// Shared helpers preserving the frozen CLI contract inherited from the retired
// Bash implementation. Exported behaviour is pinned by the Go test suite and
// docs/forge.md; historical line references in comments name that implementation.
//
// Package-level state mirrors the bash globals. Command implementations read
// and write these directly, the way the bash functions read $REPO and $BODY:
//
//	repoOpt        $REPO          set by -R/--repo or inferred by requireRepo
//	positionalArgs $POSITIONAL_ARGS filled by parseRepoPositionals
//	bodyOpt        $BODY          filled by setBodyOption
//	bodySet        $BODY_SET      whether a body option was already given
//	bodySource     $BODY_SOURCE   which option supplied it, for the error text
//	forgeURL       $FORGE_URL     read from the environment by resetState
//	forgeTokenFile $FORGE_TOKEN_FILE
//
// They are named with an "Opt" suffix where the bare name would be an
// inviting local variable: a shadowed global would silently read as empty.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// --- Exiting ---

// cliExit is the panic value used to unwind to run(). bash exits from the
// middle of any helper; Go has no such thing, so exit() panics with this and
// run() recovers it. That keeps every bash `exit`/`die` call site a one-liner
// instead of threading errors through signatures that bash never had.
type cliExit struct{ code int }

// exit ends the run with the given status, like bash `exit`.
func exit(code int) {
	panic(cliExit{code})
}

// die mirrors bash die() (forge line 142): one line on stderr prefixed with
// "forge: ", then exit 1.
//
// With no args the format string is printed verbatim, so a message containing
// a literal % is safe; with args it is a printf format. Prefer die("%s", v)
// when v is data.
func die(format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	fmt.Fprintf(stderr, "forge: %s\n", msg)
	exit(1)
}

// --- Command substitution ---

// chompNewlines reproduces what `$(...)` does to the text it captures: every
// trailing newline is removed, not just the one the inner command printed.
//
// bash reads almost every value the CLI renders through a command
// substitution (`url=$(api ... | jq -r '.html_url')`), so a field whose JSON
// string ends in "\n" reaches the output with that newline already gone. It is
// applied at the capture boundaries — the helpers that stand in for one
// `$(...)` — and never as a filter over finished output, because the newlines
// bash's own `echo`/`printf` add afterwards must survive.
func chompNewlines(s string) string {
	return strings.TrimRight(s, "\n")
}

// stripNULs reproduces the other thing command substitution does to captured
// bytes: a NUL is dropped outright, because the value becomes a C string.
//
// bash also writes "warning: command substitution: ignored null byte in
// input" to stderr for each read that contained one. That line names the
// script and the line number inside it, so it cannot be reproduced here; only
// the payload that reaches the wire is.
func stripNULs(b []byte) []byte {
	if bytes.IndexByte(b, 0) < 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c != 0 {
			out = append(out, c)
		}
	}
	return out
}

// --- URL encoding ---

// jqURI reproduces `jq -sRr @uri` (forge line 59): every byte outside the
// RFC 3986 unreserved set becomes %XX with upper-case hex, per UTF-8 byte.
// Space is %20, never +, so url.QueryEscape is not a substitute.
func jqURI(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

// --- Auth ---

// validateToken mirrors validate_token (forge line 79). It strips one trailing
// carriage return, rejects newline/quote/backslash, rejects empty, and returns
// the cleaned token. source names the credential for the error text.
func validateToken(tok, source string) string {
	tok = strings.TrimSuffix(tok, "\r")
	if strings.ContainsAny(tok, "\n\"\\") {
		fmt.Fprintf(stderr, "forge: %s contains invalid characters (newline, quote, or backslash)\n", source)
		exit(1)
	}
	if tok == "" {
		fmt.Fprintf(stderr, "forge: %s is empty\n", source)
		exit(1)
	}
	return tok
}

// loadToken mirrors load_token (forge line 90). It runs at most once per run,
// like the TOKEN_LOADED guard.
//
// diverges from bash: allod/tools#57. The token comes only from the token
// file. A non-empty FORGEJO_TOKEN is refused rather than ignored, so nobody
// can believe the variable is in use when it is not: an environment variable
// is inherited by every child process, while a 0600 file is read only by code
// that opens it.
func loadToken() {
	if tokenLoaded {
		return
	}
	if os.Getenv("FORGEJO_TOKEN") != "" {
		fmt.Fprintf(stderr, "forge: FORGEJO_TOKEN is no longer read; unset it and put the token in a mode-0600 file named by FORGE_TOKEN_FILE (currently %s)\n", forgeTokenFile)
		exit(1)
	}
	if !fileReadable(forgeTokenFile) {
		fmt.Fprintf(stderr, "forge: no token found — ensure %s exists or point FORGE_TOKEN_FILE at a mode-0600 token file\n", forgeTokenFile)
		exit(1)
	}
	// bash: validate_token "$(cat "$FORGE_TOKEN_FILE")" — command
	// substitution strips every trailing newline. A read failure leaves
	// the substitution empty, which lands on the "is empty" error.
	data, _ := os.ReadFile(forgeTokenFile)
	token = validateToken(chompNewlines(string(data)), forgeTokenFile)
	tokenLoaded = true
}

// fileReadable approximates the `[ -r path ]` test bash uses.
func fileReadable(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// --- Repo ---

// requireRepo mirrors require_repo (forge line 138): use -R when given, else
// ask git.
func requireRepo() {
	if repoOpt != "" {
		return
	}
	slug, err := inferRepo()
	if err != nil {
		fmt.Fprintln(stderr, "forge: not in a git repo and --repo not specified")
		exit(1)
	}
	if slug == "" {
		// bash: grep matched nothing, so infer_repo printed nothing and the
		// `REPO=$(infer_repo)` assignment failed under errexit. No message.
		exit(1)
	}
	repoOpt = slug
}

// --- Argument parsing ---

// containsHelpFlag mirrors contains_help_flag (forge line 147).
func containsHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

// requireOptionValue mirrors require_option_value (forge line 156). remaining
// is the number of arguments left including the option itself, i.e. bash's $#.
func requireOptionValue(option string, remaining int) {
	if remaining < 2 {
		die("%s requires a value", option)
	}
}

// setRepoOption mirrors set_repo_option (forge line 162). args starts at the
// option, so args[1] is its value.
func setRepoOption(args []string) {
	requireOptionValue(args[0], len(args))
	repoOpt = args[1]
}

// parseRepoOnlyArgs mirrors parse_repo_only_args (forge line 168): -R/--repo
// and nothing else.
func parseRepoOnlyArgs(context string, args []string) {
	for len(args) > 0 {
		switch {
		case args[0] == "-R" || args[0] == "--repo":
			setRepoOption(args)
			args = args[2:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for %s: %s", context, args[0])
		default:
			die("unexpected argument for %s: %s", context, args[0])
		}
	}
}

// parseRepoPositionals mirrors parse_repo_positionals (forge line 185):
// -R/--repo plus free positionals, which land in positionalArgs.
func parseRepoPositionals(context string, args []string) {
	positionalArgs = nil
	for len(args) > 0 {
		switch {
		case args[0] == "-R" || args[0] == "--repo":
			setRepoOption(args)
			args = args[2:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for %s: %s", context, args[0])
		default:
			positionalArgs = append(positionalArgs, args[0])
			args = args[1:]
		}
	}
}

// resetBodyOption mirrors reset_body_option (forge line 205).
func resetBodyOption() {
	bodyOpt = ""
	bodySet = false
	bodySource = ""
}

// setBodyOption mirrors set_body_option (forge line 211). -F/--body-file reads
// a file, or stdin when the value is "-". bash guards the read with a \x1f
// sentinel so trailing newlines survive command substitution; reading the
// bytes directly has the same effect.
//
// What the sentinel does not save is a NUL: the read still goes through
// `$(...)`, which drops every NUL byte, so `printf 'a\0b' | forge ... -F -`
// puts "ab" on the wire. stripNULs does the same here.
func setBodyOption(option, value string) {
	if bodySet {
		die("%s cannot be combined with %s", option, bodySource)
	}

	if option == "-F" || option == "--body-file" {
		if value != "-" && !fileReadable(value) {
			die("cannot read body file: %s", value)
		}
		var data []byte
		if value == "-" {
			data, _ = io.ReadAll(stdin)
		} else {
			data, _ = os.ReadFile(value)
		}
		bodyOpt = string(stripNULs(data))
	} else {
		bodyOpt = value
	}

	bodySet = true
	bodySource = option
}

// --- Value helpers ---

// isInteger mirrors is_integer (forge line 275): ^[0-9]+$.
func isInteger(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// jqArgJSONInteger normalizes a digit-only literal the way jq's --argjson
// does. jq's JSON parser accepts leading zeroes where the grammar does not,
// and prints the number back canonically, so `jq -n --argjson v 001 '$v'`
// answers 1 and `--argjson v 00` answers 0. Verified against jq 1.8.1 here and
// end to end through bash: `forge issue labels 2 --add 001` puts
// {"labels":[1]} on the wire, not {"labels":[001]}.
//
// Only the leading zeroes go: jq keeps the rest of the digits exactly, however
// many there are (0009007199254740993 stays 9007199254740993, past the range
// of a double), so this is a text transformation and not a reparse.
func jqArgJSONInteger(digits string) string {
	i := 0
	for i < len(digits)-1 && digits[i] == '0' {
		i++
	}
	return digits[i:]
}

// jqArgJSON embeds one piece of already-JSON text into a payload the way
// `jq -n --argjson k "$v" '{k: $v}'` does. Only the digit-only case is
// normalized; every other spelling is passed through as written, which keeps
// the documented divergence over jq's exponent canonicalization ("1e2" stays
// "1e2" rather than becoming "1E+2") where it already was.
func jqArgJSON(text string) string {
	if isInteger(text) {
		return jqArgJSONInteger(text)
	}
	return text
}

// jsonMixedLabelArrayFromArgs mirrors json_mixed_label_array_from_args (forge
// line 279): integer-looking values become JSON numbers (jq --argjson),
// everything else becomes a JSON string (jq --arg).
func jsonMixedLabelArrayFromArgs(values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if isInteger(value) {
			parts = append(parts, jqArgJSON(value))
		} else {
			parts = append(parts, jsonString(value))
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// jsonString encodes one Go string as a JSON string the way jq --arg does,
// without Go's default HTML escaping of <, > and &.
func jsonString(s string) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// appendCSVValues mirrors append_csv_values (forge line 292): split a
// comma-separated value and reject empty items.
func appendCSVValues(dest *[]string, option, value string) {
	if strings.HasSuffix(value, ",") {
		die("%s cannot contain an empty value", option)
	}

	// `IFS=, read -r -a items <<< "$value"` reads a single line, so anything
	// after the first newline is dropped, and a trailing delimiter does not
	// produce a trailing empty field.
	line := value
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	items := strings.Split(line, ",")
	if n := len(items); n > 0 && items[n-1] == "" {
		items = items[:n-1]
	}

	for _, item := range items {
		if item == "" {
			die("%s cannot contain an empty value", option)
		}
		*dest = append(*dest, item)
	}
}

// joinCSVValues mirrors join_csv_values (forge line 306).
func joinCSVValues(values []string) string {
	return strings.Join(values, ",")
}

// parsePositiveInt mirrors parse_positive_int (forge line 311). The value is
// returned verbatim, not renormalised: bash prints "$value", so "0755" reaches
// the URL as "0755".
func parsePositiveInt(option, value string) string {
	if !isInteger(value) || !bashArithPositive(value) {
		die("%s must be a positive integer", option)
	}
	return value
}

// bashArithPositive evaluates a digit string the way `[[ "$value" -gt 0 ]]`
// does: a leading zero makes it octal, so "08" is not a number at all, and the
// arithmetic is 64-bit signed with wraparound.
//
// bash also writes its own diagnostic ("value too great for base") to stderr
// before the comparison fails. That line names the script and the line number
// inside it, so it cannot be reproduced here; only the final "must be a
// positive integer" line and the exit status are.
func bashArithPositive(digits string) bool {
	base := uint64(10)
	if len(digits) > 1 && digits[0] == '0' {
		base = 8
	}
	var v uint64
	for i := 0; i < len(digits); i++ {
		d := uint64(digits[i] - '0')
		if d >= base {
			return false
		}
		v = v*base + d
	}
	return int64(v) > 0
}

var hexColorRE = regexp.MustCompile(`^[0-9A-Fa-f]{6}$`)

// normalizeColor mirrors normalize_color (forge line 317).
func normalizeColor(color string) string {
	color = strings.TrimPrefix(color, "#")
	if !hexColorRE.MatchString(color) {
		die("color must be a 6-digit hex value")
	}
	return "#" + strings.ToLower(color)
}

// randomLabelColor mirrors random_label_color (forge line 324): three random
// bytes as lower-case hex.
func randomLabelColor() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return "#" + hex.EncodeToString(b[:])
}

var dueDateRE = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// normalizeDueOn mirrors normalize_due_on (forge line 328): a bare date gains
// a midnight-UTC time, anything else passes through untouched.
func normalizeDueOn(due string) string {
	if dueDateRE.MatchString(due) {
		return due + "T00:00:00Z"
	}
	return due
}
