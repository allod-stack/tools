package main

// The two commands that the PR group and the issue group share, and the
// response-reading helper both of them need.
//
// bash writes them once and calls them with the noun bound at the call site:
//
//	pr_edit()    { edit_resource "pr edit" "pulls" "PR" "$@"; }      (line 553)
//	pr_comment() { post_comment  "pr comment" "$@"; }                (line 592)
//	issue_edit() { edit_resource "issue edit" "issues" "Issue" ...; }
//
// so the ports keep the same shape: one implementation, one line per command.

import (
	"fmt"
	"strings"
)

// editResource mirrors edit_resource (forge line 497). context is the usage
// key and the word in every error ("pr edit"), apiPath is the REST collection
// ("pulls"), and noun is what the success line calls the thing ("PR").
func editResource(context, apiPath, noun string, args []string) {
	if containsHelpFlag(args) {
		commandUsage(context)
		return
	}

	number, numberSet := "", false
	title, titleSet := "", false
	resetBodyOption()

	for len(args) > 0 {
		switch {
		case args[0] == "-R" || args[0] == "--repo":
			setRepoOption(args)
			args = args[2:]
		case args[0] == "-t" || args[0] == "--title":
			requireOptionValue(args[0], len(args))
			if titleSet {
				die("%s specified more than once", args[0])
			}
			title = args[1]
			titleSet = true
			args = args[2:]
		case args[0] == "-b" || args[0] == "--body" || args[0] == "-F" || args[0] == "--body-file":
			requireOptionValue(args[0], len(args))
			setBodyOption(args[0], args[1])
			args = args[2:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for %s: %s", context, args[0])
		default:
			if numberSet {
				die("unexpected argument for %s: %s", context, args[0])
			}
			number = args[0]
			numberSet = true
			args = args[1:]
		}
	}

	if !numberSet || number == "" {
		die("usage: forge %s <number> [--title <title>] [--body <text> | --body-file <file>]", context)
	}
	if !titleSet && !bodySet {
		die("%s requires --title, --body, or --body-file", context)
	}
	if titleSet && title == "" {
		die("--title cannot be empty")
	}
	requireRepo()

	// jq -n '{} | if $title_set then . + {title: $title} else . end
	//           | if $body_set  then . + {body: $body}  else . end'
	// Both come from --arg, so both are always strings; the flags decide
	// presence, not emptiness.
	fields := make([]string, 0, 2)
	if titleSet {
		fields = append(fields, `"title":`+jsonString(title))
	}
	if bodySet {
		fields = append(fields, `"body":`+jsonString(bodyOpt))
	}
	payload := "{" + strings.Join(fields, ",") + "}"

	url := jqBodyString(api("PATCH", "/repos/"+repoOpt+"/"+apiPath+"/"+number, []byte(payload)), "html_url")
	fmt.Fprintf(stdout, "%s updated: %s\n", noun, url)
}

// postComment mirrors post_comment (forge line 555). Both resources comment
// through the issues endpoint, which is why the PR variant passes no path.
func postComment(context string, args []string) {
	if containsHelpFlag(args) {
		commandUsage(context)
		return
	}

	number, numberSet := "", false
	resetBodyOption()

	for len(args) > 0 {
		switch {
		case args[0] == "-R" || args[0] == "--repo":
			setRepoOption(args)
			args = args[2:]
		case args[0] == "-b" || args[0] == "--body" || args[0] == "-F" || args[0] == "--body-file":
			requireOptionValue(args[0], len(args))
			setBodyOption(args[0], args[1])
			args = args[2:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for %s: %s", context, args[0])
		default:
			if numberSet {
				die("unexpected argument for %s: %s", context, args[0])
			}
			number = args[0]
			numberSet = true
			args = args[1:]
		}
	}

	if !numberSet || number == "" {
		die("usage: forge %s <number> [--body <text> | --body-file <file>]", context)
	}
	if !bodySet {
		die("%s requires --body or --body-file", context)
	}
	requireRepo()

	payload := `{"body":` + jsonString(bodyOpt) + `}`
	url := jqBodyString(api("POST", "/repos/"+repoOpt+"/issues/"+number+"/comments", []byte(payload)), "html_url")
	fmt.Fprintf(stdout, "Comment posted: %s\n", url)
}

// --- Reading one field out of a response ---
//
// bash pipes each captured body into a fresh jq for every field it wants
// (`url=$(api ... | jq -r '.html_url')`), so these do the same: one decode per
// field, and the same answer for the same body.
//
// Both are capture boundaries, so both chomp: `$(...)` removes every trailing
// newline from what jq printed, which is one more than the one jq itself
// appends. A comment whose html_url is "ok\n" therefore renders as
// "Comment posted: ok", not "Comment posted: ok\n".

// jqBodyString renders `jq -r '.a.b'` over an API response body.
//
// An empty body is not null: jq over empty input prints nothing at all, which
// bash captures as the empty string, where a decoded null would print "null".
// That is the difference between "Comment posted: " and "Comment posted: null"
// when a forge answers 2xx with no content.
func jqBodyString(body []byte, keys ...string) string {
	if jsonIsEmpty(body) {
		return ""
	}
	return chompNewlines(jqString(jsonPath(mustJSON(body), keys...)))
}

// jqBodyAlt is jqBodyString with jq's alternative operator applied:
// `jq -r '.body // ""'`. An empty body still yields "" rather than the
// fallback, for the reason above.
func jqBodyAlt(body []byte, fallback string, keys ...string) string {
	if jsonIsEmpty(body) {
		return ""
	}
	return chompNewlines(jqAlt(jsonPath(mustJSON(body), keys...), fallback))
}
