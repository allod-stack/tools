package main

// Stable pull request snapshots. The bash implementation validates the API
// response with jq before projecting it into the versioned schema below. Keep
// the validation here at the same boundary: malformed or contradictory Forgejo
// metadata must never become a partial snapshot consumed by another tool.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type prSnapshot struct {
	SchemaVersion int                 `json:"schema_version"`
	PullRequest   snapshotPullRequest `json:"pull_request"`
	Base          snapshotSide        `json:"base"`
	Head          snapshotSide        `json:"head"`
}

type snapshotPullRequest struct {
	Number    json.Number `json:"number"`
	URL       string      `json:"url"`
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	State     string      `json:"state"`
	CreatedAt string      `json:"created_at"`
	UpdatedAt string      `json:"updated_at"`
	ClosedAt  *string     `json:"closed_at"`
	Merged    bool        `json:"merged"`
	MergedAt  *string     `json:"merged_at"`
}

type snapshotSide struct {
	Repository snapshotRepository `json:"repository"`
	Ref        string             `json:"ref"`
	SHA        string             `json:"sha"`
}

type snapshotRepository struct {
	Owner    string `json:"owner"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
}

type snapshotURL struct {
	host string
	path string
}

var (
	snapshotSafeNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	snapshotOIDRE      = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	snapshotURLRE      = regexp.MustCompile(`^https://([^/@\s]+)/([^?#]+)$`)
	snapshotBadRefRE   = regexp.MustCompile(`[:^~?*\[\\]`)
)

func prSnapshotCommand(args []string) {
	if containsHelpFlag(args) {
		commandUsage("pr snapshot")
		return
	}
	parseRepoPositionals("pr snapshot", args)
	if len(positionalArgs) != 1 {
		die("usage: forge pr snapshot <number>")
	}
	number := positionalArgs[0]
	if !isInteger(number) || !bashArithPositive(number) {
		die("PR number must be a positive integer")
	}
	requireRepo()

	body := api("GET", "/repos/"+repoOpt+"/pulls/"+number, nil)
	snapshot, ok := projectPRSnapshot(body, number)
	if !ok {
		die("PR #%s response is missing or has malformed snapshot fields", number)
	}

	var output bytes.Buffer
	enc := json.NewEncoder(&output)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snapshot); err != nil {
		// Every field in the fixed schema is directly encodable. Keep this
		// failure closed in case that invariant changes later.
		die("PR #%s response is missing or has malformed snapshot fields", number)
	}
	writeSnapshotJSON(output.Bytes())
}

func projectPRSnapshot(body []byte, requestedNumber string) (prSnapshot, bool) {
	root, ok := decodeSnapshotObject(body)
	if !ok {
		return prSnapshot{}, false
	}

	number, ok := root["number"].(json.Number)
	if !ok || !snapshotNumberEquals(number, requestedNumber) {
		return prSnapshot{}, false
	}
	url, ok := snapshotSafeString(root["html_url"])
	if !ok {
		return prSnapshot{}, false
	}
	webURL, ok := parseSnapshotURL(url)
	if !ok {
		return prSnapshot{}, false
	}
	title, ok := snapshotSafeString(root["title"])
	if !ok {
		return prSnapshot{}, false
	}

	bodyText := ""
	if value, present := root["body"]; present && value != nil {
		bodyText, ok = value.(string)
		if !ok {
			return prSnapshot{}, false
		}
	}

	state, ok := snapshotState(root["state"])
	if !ok {
		return prSnapshot{}, false
	}
	createdAt, ok := snapshotRFC3339(root["created_at"])
	if !ok {
		return prSnapshot{}, false
	}
	updatedAt, ok := snapshotRFC3339(root["updated_at"])
	if !ok {
		return prSnapshot{}, false
	}
	closedAt, ok := snapshotRFC3339OrNull(root["closed_at"])
	if !ok {
		return prSnapshot{}, false
	}
	merged, ok := root["merged"].(bool)
	if !ok {
		return prSnapshot{}, false
	}
	mergedAt, ok := snapshotRFC3339OrNull(root["merged_at"])
	if !ok {
		return prSnapshot{}, false
	}
	// Contradictory metadata: a merge flag without a merge date or vice
	// versa, a close date on a PR the API still calls open, a closed PR
	// with no close date (the snapshot documents closed_at as null only
	// until the PR is closed), or a merged PR the API still calls open
	// (Forgejo always closes a PR it merges).
	if merged != (mergedAt != nil) {
		return prSnapshot{}, false
	}
	if state == "open" && closedAt != nil {
		return prSnapshot{}, false
	}
	if state == "closed" && closedAt == nil {
		return prSnapshot{}, false
	}
	if merged && state != "closed" {
		return prSnapshot{}, false
	}

	base, baseURL, ok := projectSnapshotSide(root["base"], requestedNumber, false)
	if !ok {
		return prSnapshot{}, false
	}
	head, headURL, ok := projectSnapshotSide(root["head"], requestedNumber, true)
	if !ok || len(base.SHA) != len(head.SHA) {
		return prSnapshot{}, false
	}

	if asciiLower(webURL.host) != asciiLower(baseURL.host) ||
		asciiLower(webURL.host) != asciiLower(headURL.host) {
		return prSnapshot{}, false
	}
	prSuffix := base.Repository.Owner + "/" + base.Repository.Name + "/pulls/" + jqArgJSONInteger(requestedNumber)
	if !snapshotPathEnds(webURL.path, prSuffix) {
		return prSnapshot{}, false
	}

	return prSnapshot{
		SchemaVersion: 1,
		PullRequest: snapshotPullRequest{
			Number:    number,
			URL:       url,
			Title:     title,
			Body:      bodyText,
			State:     state,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
			ClosedAt:  closedAt,
			Merged:    merged,
			MergedAt:  mergedAt,
		},
		Base: base,
		Head: head,
	}, true
}

// snapshotState accepts only the two values Forgejo's pull request state
// carries; pr_view's Closed:/Merged: header lines gate on the same values.
func snapshotState(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	if text != "open" && text != "closed" {
		return "", false
	}
	return text, true
}

// snapshotRFC3339 requires a non-empty string parseable as RFC 3339, the
// format Forgejo's timestamp fields use.
func snapshotRFC3339(value any) (string, bool) {
	text, ok := value.(string)
	if !ok || text == "" {
		return "", false
	}
	if _, err := time.Parse(time.RFC3339, text); err != nil {
		return "", false
	}
	return text, true
}

// snapshotRFC3339OrNull is snapshotRFC3339 with jq's `// null` shape: a JSON
// null (or an absent key, decoded the same way) is valid and yields a nil
// pointer, distinguishing "not closed/merged yet" from a malformed date.
func snapshotRFC3339OrNull(value any) (*string, bool) {
	if value == nil {
		return nil, true
	}
	text, ok := snapshotRFC3339(value)
	if !ok {
		return nil, false
	}
	return &text, true
}

func writeSnapshotJSON(encoded []byte) {
	// encoding/json escapes the two JavaScript line separators even when HTML
	// escaping is disabled; jq writes them as UTF-8. Replace only real JSON
	// escapes (an odd-length backslash run), never a literal string such as
	// `\\u2028` that merely contains those six characters.
	for i := 0; i < len(encoded); {
		if encoded[i] != '\\' {
			stdout.Write(encoded[i : i+1])
			i++
			continue
		}
		end := i
		for end < len(encoded) && encoded[end] == '\\' {
			end++
		}
		run := end - i
		separator := ""
		if run%2 == 1 && end+5 <= len(encoded) {
			switch string(encoded[end : end+5]) {
			case "u2028":
				separator = "\u2028"
			case "u2029":
				separator = "\u2029"
			}
		}
		if separator != "" {
			stdout.Write(encoded[i : end-1])
			fmt.Fprint(stdout, separator)
			i = end + 5
			continue
		}
		stdout.Write(encoded[i:end])
		i = end
	}
}

func decodeSnapshotObject(body []byte) (map[string]any, bool) {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, false
	}
	object, ok := value.(map[string]any)
	return object, ok
}

func projectSnapshotSide(value any, requestedNumber string, head bool) (snapshotSide, snapshotURL, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return snapshotSide{}, snapshotURL{}, false
	}
	sha, ok := object["sha"].(string)
	if !ok || !validSnapshotOID(sha) {
		return snapshotSide{}, snapshotURL{}, false
	}
	ref, ok := object["ref"].(string)
	if !ok || !validSnapshotRef(ref, requestedNumber, head) {
		return snapshotSide{}, snapshotURL{}, false
	}
	repository, cloneURL, ok := projectSnapshotRepository(object["repo"])
	if !ok {
		return snapshotSide{}, snapshotURL{}, false
	}
	return snapshotSide{Repository: repository, Ref: ref, SHA: sha}, cloneURL, true
}

func projectSnapshotRepository(value any) (snapshotRepository, snapshotURL, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return snapshotRepository{}, snapshotURL{}, false
	}
	ownerObject, ok := object["owner"].(map[string]any)
	if !ok {
		return snapshotRepository{}, snapshotURL{}, false
	}
	owner, ok := snapshotSafeString(ownerObject["login"])
	if !ok || !snapshotSafeNameRE.MatchString(owner) {
		return snapshotRepository{}, snapshotURL{}, false
	}
	name, ok := snapshotSafeString(object["name"])
	if !ok || !snapshotSafeNameRE.MatchString(name) {
		return snapshotRepository{}, snapshotURL{}, false
	}
	fullName, ok := snapshotSafeString(object["full_name"])
	if !ok || fullName != owner+"/"+name {
		return snapshotRepository{}, snapshotURL{}, false
	}
	cloneURLText, ok := snapshotSafeString(object["clone_url"])
	if !ok {
		return snapshotRepository{}, snapshotURL{}, false
	}
	cloneURL, ok := parseSnapshotURL(cloneURLText)
	if !ok || !snapshotPathEnds(cloneURL.path, owner+"/"+name+".git") {
		return snapshotRepository{}, snapshotURL{}, false
	}
	return snapshotRepository{Owner: owner, Name: name, FullName: fullName, CloneURL: cloneURLText}, cloneURL, true
}

func snapshotSafeString(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok && text != "" && !snapshotContainsControl(text)
}

func snapshotContainsControl(text string) bool {
	for _, r := range text {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func parseSnapshotURL(text string) (snapshotURL, bool) {
	if text == "" || snapshotContainsControl(text) {
		return snapshotURL{}, false
	}
	parts := snapshotURLRE.FindStringSubmatch(text)
	if parts == nil || strings.IndexFunc(parts[1], unicode.IsSpace) >= 0 {
		return snapshotURL{}, false
	}
	return snapshotURL{host: parts[1], path: parts[2]}, true
}

func snapshotPathEnds(path, suffix string) bool {
	return path == suffix || strings.HasSuffix(path, "/"+suffix)
}

func validSnapshotOID(oid string) bool {
	if snapshotOIDRE.MatchString(oid) {
		return true
	}
	// jq's `$` also matches immediately before one final newline. The bash
	// validator therefore accepts that spelling when both sides have the same
	// length; retain it until the snapshot contract deliberately changes.
	return strings.HasSuffix(oid, "\n") && snapshotOIDRE.MatchString(strings.TrimSuffix(oid, "\n"))
}

func validSnapshotRef(ref, requestedNumber string, head bool) bool {
	if ordinarySnapshotRef(ref) {
		return true
	}
	return head && ref == "refs/pull/"+jqArgJSONInteger(requestedNumber)+"/head"
}

func ordinarySnapshotRef(ref string) bool {
	return ref != "" &&
		!snapshotContainsControl(ref) &&
		!strings.ContainsAny(ref, " \t") &&
		!strings.HasPrefix(ref, "-") &&
		!strings.HasPrefix(ref, "refs/") &&
		!snapshotBadRefRE.MatchString(ref) &&
		!strings.HasPrefix(ref, ".") &&
		!strings.Contains(ref, "/.") &&
		!strings.Contains(ref, "..") &&
		!strings.Contains(ref, "@{") &&
		ref != "@" &&
		!strings.HasSuffix(ref, "/") &&
		!strings.HasPrefix(ref, "/") &&
		!strings.Contains(ref, "//") &&
		!strings.HasSuffix(ref, ".lock")
}

func snapshotNumberEquals(number json.Number, digits string) bool {
	got, ok := new(big.Rat).SetString(number.String())
	if !ok {
		return false
	}
	wantInt, ok := new(big.Int).SetString(jqArgJSONInteger(digits), 10)
	if !ok {
		return false
	}
	return got.IsInt() && got.Num().Cmp(wantInt) == 0
}

func asciiLower(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, text)
}
