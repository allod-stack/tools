package main

// Redaction runs over the full rendered trace text, once, before it is
// written to disk. It is a coarse net for the token shapes that actually
// show up in agent session logs (API keys, bearer tokens, .netrc lines), not
// a guarantee: a secret shaped like ordinary prose survives it. The pem
// pattern spans the BEGIN/END markers and everything between, which is why
// this runs over the whole trace rather than one physical line at a time —
// a line-by-line pass would only ever catch the marker lines and leave the
// key material between them untouched.
//
// 40-hex git commit IDs and 64-hex sha256 digests are common in these logs
// and must survive redaction; none of the patterns below match a bare hex
// run, so no case-out is needed for them.

import "regexp"

type traceRedactionRule struct {
	kind    string
	pattern *regexp.Regexp
}

var traceRedactionRules = []traceRedactionRule{
	{"bearer", regexp.MustCompile(`(?i)Bearer [A-Za-z0-9._~+/=-]+`)},
	{"openai", regexp.MustCompile(`sk-\w{20,}`)},
	{"github", regexp.MustCompile(`gh[pousr]_\w{30,}`)},
	{"slack", regexp.MustCompile(`xox[abpr]-[A-Za-z0-9-]{10,}`)},
	{"aws", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"age", regexp.MustCompile(`AGE-SECRET-KEY-1[A-Z2-7]+`)},
	{"pem", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)},
	{"netrc", regexp.MustCompile(`password[= ]\S+`)},
	{"authorization", regexp.MustCompile(`(?i)Authorization:\s*.+`)},
	{"url-credential", regexp.MustCompile(`://[^\s/@]+:[^\s/@]+@`)},
}

// traceRedact replaces every match of every rule with "[redacted:<kind>]",
// rules applied in table order over the whole trace text.
func traceRedact(text string) string {
	for _, rule := range traceRedactionRules {
		text = rule.pattern.ReplaceAllString(text, "[redacted:"+rule.kind+"]")
	}
	return text
}
