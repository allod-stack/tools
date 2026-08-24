package main

// The API call itself and the rendering of its failures. Everything here
// preserves the contract of the retired Bash api() and the errexit that
// followed its call sites; the Go API-error tests pin exact text and exit codes.

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"forge.anarch.diy/allod/tools/internal/forgeapi"
)

// api performs one API call and returns the response body, or ends the run.
//
// bash reports the HTTP status and the API's own message on failure and
// returns non-zero; every unguarded call site then dies through errexit. This
// folds the two together, because all but one call site is unguarded.
func api(method, path string, body []byte) []byte {
	out, rc := apiTry(method, path, body)
	if rc != 0 {
		exit(rc)
	}
	return out
}

// apiTry is api() without the exit, for the one bash call site that guards the
// call with `if` (forge line 855, deleting a branch after closing a PR). The
// failure line is still printed; only the exit is the caller's decision.
func apiTry(method, path string, body []byte) ([]byte, int) {
	loadToken()

	// curl parses the URL before it opens anything, so a URL it refuses costs
	// no request at all. bash reports that through the same line every other
	// transport failure uses. This has to happen here, not in the client: Go
	// would percent-encode the offending byte and send the request.
	if curlRejectsURL(forgeURL + "/api/v1" + path) {
		fmt.Fprintf(stderr, "forge: %s %s failed: curl exit 3\n", method, path)
		return nil, 3
	}

	client := &forgeapi.Client{BaseURL: forgeURL, Token: token}
	resp, err := client.Do(forgeapi.Request{
		Method: method,
		Path:   "/api/v1" + path,
		// bash passes -H "Content-Type: application/json" on every api()
		// call, including GETs that send no body.
		ContentType: "application/json",
		Body:        body,
	})
	if err != nil {
		rc := curlExitCode(err)
		fmt.Fprintf(stderr, "forge: %s %s failed: curl exit %d\n", method, path, rc)
		return nil, rc
	}

	// bash appends the status to the body as a trailing line and splits it
	// back off with sed; the surrounding command substitution then strips
	// every trailing newline from what is left.
	out := trimTrailingNewlines(resp.Body)

	// Anything but 2xx is a failure. Redirects included: no -L is passed, so a
	// 3xx body is never the resource that was asked for.
	if resp.Status < 200 || resp.Status > 299 {
		message := apiErrorMessage(out)
		suffix := ""
		if message != "" {
			suffix = ": " + message
		}
		fmt.Fprintf(stderr, "forge: %s %s failed: HTTP %d%s\n", method, path, resp.Status, suffix)
		return nil, 22
	}
	return out, 0
}

// trimTrailingNewlines reproduces what bash command substitution does to every
// captured body.
func trimTrailingNewlines(b []byte) []byte {
	return bytes.TrimRight(b, "\n")
}

// apiErrorMessage builds the trailing text of the failure line: the API's own
// message when the body carries one, otherwise the start of the raw body.
//
// bash captures jq's output with `message=$(... | jq -r '...')`, so the chomp
// happens before the emptiness test and before the sanitize pipeline: a
// message of "bad\n" renders as ": bad", and a message of only newlines is
// empty and falls back to the raw body. body itself arrived through the same
// treatment (trimTrailingNewlines in apiTry).
func apiErrorMessage(body []byte) string {
	message, ok := jsonMessageField(body)
	message = chompNewlines(message)
	if !ok || message == "" {
		message = string(body)
	}
	return sanitizeAPIMessage(message)
}

// jsonMessageField reproduces the jq filter bash uses to pull .message out of a
// Forgejo error body:
//
//	if type == "object" and (.message | type) == "string" then .message else empty end
//
// jq reads every top-level value in the stream and prints one line per result.
// A parse error (an HTML proxy page, say) makes bash discard jq's output
// entirely, which is the false return here.
func jsonMessageField(body []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var lines []string
	for {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", false
		}
		if obj, ok := v.(map[string]any); ok {
			if s, ok := obj["message"].(string); ok {
				lines = append(lines, s)
			}
		}
	}
	return strings.Join(lines, "\n"), true
}

// sanitizeAPIMessage reproduces
//
//	tr -d '\000-\010\013\014\016-\037\177' | tr '\n\r\t' '   ' | cut -c1-200
//
// so that a stray newline or escape sequence in a payload cannot forge extra
// output. Control bytes are dropped before the truncation, and GNU cut -c
// counts bytes, not characters, so a multi-byte character can be cut in half.
func sanitizeAPIMessage(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c <= 0x08, c == 0x0b, c == 0x0c, c >= 0x0e && c <= 0x1f, c == 0x7f:
			// deleted
		case c == '\n', c == '\r', c == '\t':
			b = append(b, ' ')
		default:
			b = append(b, c)
		}
	}
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

// curlRejectsURL reports whether curl would refuse the URL outright: "URL
// rejected: Malformed input to a URL function", exit 3, and no connection
// opened. Go is more forgiving -- http.NewRequest percent-encodes a space and
// sends the request -- so without this check `forge pr view "1 2"` would reach
// the forge where bash never left the process.
//
// The reject class was determined empirically against the curl on this machine
// (8.20.0) by putting every byte from 0x01 to 0xFF into a URL path: 0x01-0x1F,
// the space, and 0x7F are rejected; everything from 0x21 up is accepted,
// including the whole of 0x80-0xFF, which curl passes through untouched. The
// position does not matter -- userinfo, host, path, query and fragment all
// reject alike -- so the whole composed URL is scanned, which is exactly the
// string bash hands curl ("$FORGE_URL/api/v1$path"). Bytes, not runes: the
// rejected set is ASCII and a UTF-8 continuation byte is never in it.
//
// Deliberately not reproduced: curl's URL globbing. To curl, `{a,b}` and
// `[1-3]` are expansions -- one request per expansion, all before the URL
// parser runs -- and an unbalanced `{` or `[` is its own exit 3. Go
// percent-encodes those four characters and sends exactly one request. That
// divergence is documented rather than emulated; emulating it would mean
// porting curl's glob engine, and no forge path is meant to contain them.
func curlRejectsURL(rawURL string) bool {
	for i := 0; i < len(rawURL); i++ {
		if c := rawURL[i]; c <= 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}

// curlExitCode maps a Go transport error onto the curl exit status bash would
// have reported. docs/forge.md promises curl's own code for a transport
// failure, so the causes curl distinguishes are mapped explicitly:
//
//	3   URL malformed             url.Parse rejected FORGE_URL + path
//	6   could not resolve host    *net.DNSError
//	28  operation timed out       a net.Error that reports Timeout()
//	35  SSL connect error         TLS handshake failure
//	52  empty reply from server   connection closed before any response
//	60  peer certificate invalid  certificate verification failure
//	7   failed to connect to host everything else
//
// Go does not expose curl's full taxonomy, so this is a mapping rather than an
// identity. What matters, and what the tests pin, is that a transport failure
// never exits 22: 22 means the forge answered and the answer was not a success.
func curlExitCode(err error) int {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Op == "parse" {
		return 3
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return 6
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return 60
	}
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		return 60
	}
	var recErr tls.RecordHeaderError
	if errors.As(err, &recErr) {
		return 35
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return 28
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return 52
	}
	return 7
}

// --- jq-equivalent decoding ---
//
// bash reads every response with jq. These helpers give the same traversal in
// Go: numbers keep the formatting they arrived with (json.Number), missing
// keys are null rather than an error, and rendering matches jq -r.

// jsonIsEmpty reports whether a body carries no JSON value at all. jq over
// empty input prints nothing and exits 0, which bash captures as an empty
// string; callers that would otherwise render "null" must check this first.
func jsonIsEmpty(data []byte) bool {
	return len(bytes.TrimSpace(data)) == 0
}

// decodeJSON parses one JSON value, preserving number formatting.
func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// mustJSON decodes a response body or ends the run.
//
// bash pipes these bodies into jq, which prints its own diagnostic and exits 2
// when the text does not parse. jq's wording names jq and its input position,
// so it cannot be reproduced; the shape and the exit status are. An empty body
// decodes to null here, where jq would have produced no output at all --
// callers that care use jsonIsEmpty.
func mustJSON(data []byte) any {
	if jsonIsEmpty(data) {
		return nil
	}
	v, err := decodeJSON(data)
	if err != nil {
		fmt.Fprintf(stderr, "jq: error (at <stdin>:0): %v\n", err)
		exit(2)
	}
	return v
}

// jsonObject returns v as an object, or nil when it is not one.
func jsonObject(v any) map[string]any {
	obj, _ := v.(map[string]any)
	return obj
}

// jsonArray returns v as an array, or nil when it is not one. jq's `.[]` over
// a missing or empty array yields nothing, which is a nil slice here.
func jsonArray(v any) []any {
	arr, _ := v.([]any)
	return arr
}

// jsonField is a nil-safe `.key`.
func jsonField(v any, key string) any {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return obj[key]
}

// jsonPath is a nil-safe `.a.b.c`.
func jsonPath(v any, keys ...string) any {
	for _, key := range keys {
		v = jsonField(v, key)
	}
	return v
}

// jqString renders one decoded value the way `jq -r` prints it: raw for
// strings, the original literal for numbers, and compact JSON for anything
// composite, which is also how jq renders values interpolated into a string.
func jqString(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// jqAlt renders v with jq's alternative operator applied: `v // "fallback"`
// takes the fallback when v is null or false.
func jqAlt(v any, fallback string) string {
	if v == nil || v == false {
		return fallback
	}
	return jqString(v)
}
