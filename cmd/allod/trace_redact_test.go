package main

import (
	"strings"
	"testing"
)

func TestTraceRedact(t *testing.T) {
	cases := []struct {
		kind  string
		input string
	}{
		{"bearer", "Authorization header carried Bearer abc123.def-456_ghi"},
		{"openai", "found key sk-abcdefghijklmnopqrstuvwxyz012345"},
		{"github", "token gho_abcdefghijklmnopqrstuvwxyz0123456789"},
		{"slack", "webhook uses xoxb-1234567890-abcdefghij"},
		{"aws", "access key AKIAABCDEFGHIJKLMNOP in the log"},
		{"age", "identity AGE-SECRET-KEY-1QYQSZQGPQYQSZQGPQYQSZQGPQYQSZQGPQYQSZQGPQYQSZQGPQYQSZQGPQY in the log"},
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\nZmFrZWtleWRhdGE=\n-----END RSA PRIVATE KEY-----"},
		{"netrc", "machine example.com login agent password hunter2verylong"},
		{"authorization", "Authorization: Basic dXNlcjpwYXNz"},
		{"url-credential", "fetching https://user:hunter2@example.com/path"},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			got := traceRedact(c.input)
			want := "[redacted:" + c.kind + "]"
			if !strings.Contains(got, want) {
				t.Errorf("traceRedact(%q) = %q, want it to contain %q", c.input, got, want)
			}
		})
	}
}

// TestTraceRedactHashesSurvive pins the one thing every redaction rule must
// not touch: a 40-hex git commit ID or a 64-hex sha256 digest, both common
// in these logs, must reach the trace unredacted.
func TestTraceRedactHashesSurvive(t *testing.T) {
	commit := "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	input := "commit " + commit + " sha256 " + digest
	got := traceRedact(input)
	if got != input {
		t.Errorf("traceRedact(%q) = %q, want it unchanged", input, got)
	}
}
