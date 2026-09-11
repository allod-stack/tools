package main

// Shared --json/--jq machinery for `issue view` and `pr view` (allod/tools#190).
//
// gh's shape, restricted honestly: --json takes a comma-separated field list
// (repeatable, gh-style multi-value handling; cli-design.md), --jq applies a
// simple dotted field path to the result. With --json given, the command
// requests only the issue/PR object -- no comments, no reviews -- and prints
// exactly the requested fields as one JSON object with sorted keys and
// nothing else: no header, no comments, no rendered summary.
//
// The body field is the reason this exists: it must reach stdout exactly as
// the API returned it, so an edit can be built from what is actually there.
// Every builder below therefore reads the decoded JSON value directly
// (jsonField/jsonPath), never through jqBodyString/jqBodyAlt, which chomp
// trailing newlines the way bash's command substitution did.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// jsonFieldBuilder projects one gh-style field out of a decoded issue/PR
// object (root, from mustJSON -- nil when the response body was empty).
type jsonFieldBuilder func(root any) any

// viewJSONFields are the builders for every field both `issue view` and
// `pr view` expose. pr view adds prOnlyJSONFields on top of this set.
var viewJSONFields = map[string]jsonFieldBuilder{
	"number":    func(root any) any { return jsonField(root, "number") },
	"title":     func(root any) any { return jsonField(root, "title") },
	"body":      viewJSONBody,
	"state":     func(root any) any { return jsonField(root, "state") },
	"author":    viewJSONAuthor,
	"labels":    viewJSONLabels,
	"milestone": viewJSONMilestone,
	"url":       func(root any) any { return jsonField(root, "html_url") },
	"createdAt": func(root any) any { return jsonField(root, "created_at") },
	"updatedAt": func(root any) any { return jsonField(root, "updated_at") },
	"closedAt":  func(root any) any { return jsonField(root, "closed_at") },
}

// prOnlyJSONFields are the two fields `pr view --json` adds beyond
// viewJSONFields.
var prOnlyJSONFields = map[string]jsonFieldBuilder{
	"headRefName": func(root any) any { return jsonPath(root, "head", "ref") },
	"baseRefName": func(root any) any { return jsonPath(root, "base", "ref") },
}

// issueViewJSONFields lists the fields `issue view --json` accepts, in the
// order named in an "unknown field" error.
var issueViewJSONFields = []string{
	"number", "title", "body", "state", "author", "labels", "milestone",
	"url", "createdAt", "updatedAt", "closedAt",
}

// prViewJSONFields is issueViewJSONFields plus the two PR-only fields.
var prViewJSONFields = append(append([]string{}, issueViewJSONFields...), "headRefName", "baseRefName")

// viewJSONBody is the whole reason for this file: the body reaches stdout as
// the API returned it. A field the API omits renders as the empty string,
// matching the pr snapshot convention (docs/forge.md), not null.
func viewJSONBody(root any) any {
	if v := jsonField(root, "body"); v != nil {
		return v
	}
	return ""
}

// viewJSONAuthor projects gh's `author: {login}` shape. A missing user
// renders the whole field null, per the "field the API omits renders as
// null" rule -- not `{"login": null}`.
func viewJSONAuthor(root any) any {
	user := jsonField(root, "user")
	if user == nil {
		return nil
	}
	return map[string]any{"login": jsonField(user, "login")}
}

// viewJSONLabels projects gh's `labels: [{name}]` shape. A missing or null
// .labels renders null; an empty array renders `[]`, which jsonArray
// distinguishes from a missing field by returning a non-nil empty slice.
func viewJSONLabels(root any) any {
	labels, ok := jsonField(root, "labels").([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(labels))
	for _, item := range labels {
		out = append(out, map[string]any{"name": jsonField(item, "name")})
	}
	return out
}

// viewJSONMilestone projects gh's `milestone: {title}` shape, or null when
// the issue/PR has none.
func viewJSONMilestone(root any) any {
	milestone := jsonField(root, "milestone")
	if milestone == nil {
		return nil
	}
	return map[string]any{"title": jsonField(milestone, "title")}
}

// validateJSONFields dies on the first field not in valid, listing every
// valid field in the message -- an unknown field is a fatal error whose
// message lists the valid fields (allod/tools#190 design).
func validateJSONFields(fields, valid []string) {
	for _, f := range fields {
		if !containsString(valid, f) {
			die("unknown --json field %q; valid fields: %s", f, strings.Join(valid, ", "))
		}
	}
}

// dieEmptyJSONFields is the "--json named no field" error, shared by every
// path that can produce an empty field list: the flag given an empty value
// outright, and a value whose only content is on a line appendCSVValues never
// looks at (append_csv_values (forge line 292) mirrors `IFS=, read -r -a` --
// one line, silently dropping everything after the first newline and any
// trailing empty item). `forge issue view 20 --json $'\nbody'` is exactly
// that: non-empty by the raw string, but zero usable fields once parsed.
func dieEmptyJSONFields(valid []string) {
	die("--json requires at least one field; valid fields: %s", strings.Join(valid, ", "))
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// jqPathRE matches exactly a dotted field path of identifiers: `.body`,
// `.author.login`. Anything else -- a pipe, a filter, an index -- is refused
// rather than partially interpreted.
var jqPathRE = regexp.MustCompile(`^\.[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// validateJQExpr dies unless expr is a simple dotted field path whose first
// segment names a field that was requested with --json. It runs before any
// API request, alongside validateJSONFields, so a bad --jq expression never
// costs a request.
func validateJQExpr(expr string, requested []string) {
	if !jqPathRE.MatchString(expr) {
		die("only simple field paths such as .body are supported for --jq")
	}
	first := strings.SplitN(strings.TrimPrefix(expr, "."), ".", 2)[0]
	if !containsString(requested, first) {
		die("--jq field %q was not requested with --json", first)
	}
}

// evaluateJQPath applies an already-validated --jq expression (validateJQExpr
// ran first) to the object --json projected.
func evaluateJQPath(expr string, projected map[string]any) any {
	segments := strings.Split(strings.TrimPrefix(expr, "."), ".")
	v := projected[segments[0]]
	for _, seg := range segments[1:] {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = obj[seg]
	}
	return v
}

// buildProjection resolves each requested field to its value for root,
// looking first in extra (the command-specific fields) and then in
// viewJSONFields. Fields must already be validated against the matching
// field list, so every lookup here succeeds.
func buildProjection(root any, fields []string, extra map[string]jsonFieldBuilder) map[string]any {
	projected := make(map[string]any, len(fields))
	for _, f := range fields {
		builder, ok := extra[f]
		if !ok {
			builder = viewJSONFields[f]
		}
		projected[f] = builder(root)
	}
	return projected
}

// printJSONFields writes the --json object: exactly the requested fields,
// keys sorted the way encoding/json sorts a map, HTML escaping off, one
// trailing newline -- nothing else on stdout.
func printJSONFields(projected map[string]any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(projected); err != nil {
		die("could not encode --json output: %v", err)
	}
	stdout.Write(buf.Bytes())
}

// printJQResult renders one value the way `jq -r` prints a top-level result:
// a string raw plus one newline (this is the body's round-trip path -- no
// chomping, no normalization, whatever bytes the API sent), null as the four
// characters "null", and anything else as compact JSON on one line.
func printJQResult(v any) {
	if s, ok := v.(string); ok {
		fmt.Fprintln(stdout, s)
		return
	}
	fmt.Fprintln(stdout, jqCompactJSON(v))
}

func jqCompactJSON(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "null"
	}
	return strings.TrimRight(buf.String(), "\n")
}

// parseViewArgs is the option loop shared by issue view and pr view: -R/--repo,
// --json (comma-separated, repeatable), --jq, and exactly one positional (the
// issue/PR number). It also runs every fatal check the two commands share, so
// by the time it returns the caller either has a validated result or the
// process has already exited:
//
//   - wrong positional count -> "usage: forge <context> <number>"
//   - --jq without --json
//   - --json named no field, whether from an outright empty value or one
//     appendCSVValues silently reduced to nothing (dieEmptyJSONFields)
//   - an unknown --json field (validateJSONFields)
//   - an unsupported --jq expression, or one whose first segment was not
//     requested with --json (validateJQExpr)
//
// fields is empty exactly when --json was not given at all: every path that
// sets it non-nil either fills it or dies first, so callers tell "no --json"
// from "field validated" by len(fields) alone.
func parseViewArgs(context string, args []string, validFields []string) (number string, fields []string, jqSet bool, jqExpr string) {
	positionalArgs = nil
	var jsonFields []string
	jsonSet := false

	for len(args) > 0 {
		switch args[0] {
		case "-R", "--repo":
			setRepoOption(args)
			args = args[2:]
		case "--json":
			requireOptionValue(args[0], len(args))
			if args[1] == "" {
				dieEmptyJSONFields(validFields)
			}
			jsonSet = true
			appendCSVValues(&jsonFields, args[0], args[1])
			args = args[2:]
		case "--jq":
			requireOptionValue(args[0], len(args))
			if jqSet {
				die("%s specified more than once", args[0])
			}
			jqExpr = args[1]
			jqSet = true
			args = args[2:]
		default:
			if strings.HasPrefix(args[0], "-") {
				die("unknown option for %s: %s", context, args[0])
			}
			positionalArgs = append(positionalArgs, args[0])
			args = args[1:]
		}
	}

	if len(positionalArgs) != 1 {
		die("usage: forge %s <number>", context)
	}
	if jqSet && !jsonSet {
		die("cannot use --jq without specifying --json")
	}
	if jsonSet {
		if len(jsonFields) == 0 {
			dieEmptyJSONFields(validFields)
		}
		validateJSONFields(jsonFields, validFields)
	}
	if jqSet {
		validateJQExpr(jqExpr, jsonFields)
	}

	return positionalArgs[0], jsonFields, jqSet, jqExpr
}

// runViewJSON is the --json/--jq tail shared by issue view and pr view: fetch
// the object, project the requested fields, then print either the --jq result
// or the whole projected object.
// jqExpr must already be validated (validateJQExpr) when jqSet is true.
func runViewJSON(apiPath string, fields []string, jqSet bool, jqExpr string, extra map[string]jsonFieldBuilder) {
	root := mustJSON(api("GET", apiPath, nil))
	projected := buildProjection(root, fields, extra)
	if jqSet {
		printJQResult(evaluateJQPath(jqExpr, projected))
		return
	}
	printJSONFields(projected)
}
