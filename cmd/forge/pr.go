package main

// Pull request commands (forge lines 399-866).
//
// The rendering here is where bash leans hardest on its pipeline: `pr list`
// pipes a jq template into `column -t`, and `pr view` glues several jq
// templates together with command substitution, whose newline stripping is
// itself part of the output. Both are reproduced rather than tidied, so the
// helpers at the bottom of the file are jq's semantics and column's, not Go's.

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"forge.anarch.diy/allod/tools/internal/gitremote"
)

// currentBranch is the test seam for `git branch --show-current`, the head
// branch pr_create falls back on (forge line 672). It matches the inferRepo
// seam in main.go: the subprocess lives in internal/gitremote, and nothing in
// this package spawns one.
var currentBranch = gitremote.CurrentBranch

// prList mirrors pr_list (forge line 399), extended with the state filter
// and merged marker from allod/tools#193: `-s/--state` selects open, closed,
// or all pull requests (default open, unchanged), and a closed or all listing
// gains a status column so a closed-but-unmerged pull request is never
// mistaken for one that landed.
func prList(args []string) {
	if containsHelpFlag(args) {
		commandUsage("pr list")
		return
	}
	state := "open"
	limit := "50"

	for len(args) > 0 {
		switch args[0] {
		case "-R", "--repo":
			setRepoOption(args)
			args = args[2:]
		case "-s", "--state":
			requireOptionValue(args[0], len(args))
			switch args[1] {
			case "open", "closed", "all":
				state = args[1]
			default:
				die("--state must be one of: open, closed, all")
			}
			args = args[2:]
		case "-L", "--limit":
			requireOptionValue(args[0], len(args))
			limit = parsePositiveInt(args[0], args[1])
			args = args[2:]
		default:
			if strings.HasPrefix(args[0], "-") {
				die("unknown option for pr list: %s", args[0])
			}
			die("unexpected argument for pr list: %s", args[0])
		}
	}
	requireRepo()

	result := mustJSON(api("GET", "/repos/"+repoOpt+"/pulls?state="+state+"&limit="+limit, nil))

	if jqLengthOf(result) == 0 {
		switch state {
		case "open":
			fmt.Fprintf(stdout, "No open pull requests in %s\n", repoOpt)
		case "closed":
			fmt.Fprintf(stdout, "No closed pull requests in %s\n", repoOpt)
		default:
			fmt.Fprintf(stdout, "No pull requests in %s\n", repoOpt)
		}
		return
	}

	// jq -r '.[] | "\(.number)\t\(.title)\t\(.user.login)\t\(.head.label) → \(.base.label)"'
	// A tab or a newline inside a title is not escaped by jq, so it reaches
	// column as a real separator: tabLines builds the text first and
	// columnTable splits it into cells afterwards, exactly like the pipeline.
	pulls := jsonArray(result)
	rows := make([][]string, 0, len(pulls))
	for _, pr := range pulls {
		row := []string{
			jqString(jsonField(pr, "number")),
			jqString(jsonField(pr, "title")),
			jqString(jsonPath(pr, "user", "login")),
			jqString(jsonPath(pr, "head", "label")) + " → " + jqString(jsonPath(pr, "base", "label")),
		}
		if state != "open" {
			row = append(row, prStatusCell(pr))
		}
		rows = append(rows, row)
	}
	fmt.Fprint(stdout, columnTable(tabLines(rows)))
}

// prStatusCell is the status column `pr list` appends for a closed or all
// listing (allod/tools#193 design): the pull request's own state when it is
// open, "closed" when it is closed and was never merged, and
// "merged YYYY-MM-DD" -- the first 10 characters of merged_at, jq-style
// (jqPrefixSlice) -- when the API's merged is true. A merged pull request
// with a null merged_at renders "merged" alone, with no trailing space.
func prStatusCell(pr any) string {
	state := jqString(jsonField(pr, "state"))
	if state != "closed" {
		return state
	}
	merged, _ := jsonField(pr, "merged").(bool)
	if !merged {
		return "closed"
	}
	mergedAt := jsonField(pr, "merged_at")
	if mergedAt == nil {
		return "merged"
	}
	return "merged " + jqString(jqPrefixSlice(mergedAt, 10))
}

// prView mirrors pr_view (forge line 415), plus --json/--jq
// (allod/tools#190): with --json given, request only the PR object and print
// exactly the requested fields, nothing else.
func prView(args []string) {
	if containsHelpFlag(args) {
		commandUsage("pr view")
		return
	}

	number, jsonFields, jqSet, jqExpr := parseViewArgs("pr view", args, prViewJSONFields)
	requireRepo()

	if len(jsonFields) > 0 {
		runViewJSON("/repos/"+repoOpt+"/pulls/"+number, jsonFields, jqSet, jqExpr, prOnlyJSONFields)
		return
	}

	pr := api("GET", "/repos/"+repoOpt+"/pulls/"+number, nil)
	comments := api("GET", "/repos/"+repoOpt+"/issues/"+number+"/comments", nil)
	reviews := api("GET", "/repos/"+repoOpt+"/pulls/"+number+"/reviews", nil)

	state := jqBodyString(pr, "state")

	// Column width 10 = the widest label (Created:/Updated:, 8 characters)
	// plus two spaces; every other label pads out to it.
	const prHeaderWidth = 10
	fmt.Fprintf(stdout, "PR #%s: %s\n", number, jqBodyString(pr, "title"))
	fmt.Fprintf(stdout, "  %-*s%s\n", prHeaderWidth, "State:", state)
	fmt.Fprintf(stdout, "  %-*s%s\n", prHeaderWidth, "Author:", jqBodyString(pr, "user", "login"))
	fmt.Fprintf(stdout, "  %-*s%s\n", prHeaderWidth, "Created:", jqBodyDate(pr, "created_at"))
	fmt.Fprintf(stdout, "  %-*s%s\n", prHeaderWidth, "Updated:", jqBodyDate(pr, "updated_at"))
	if state == "closed" {
		fmt.Fprintf(stdout, "  %-*s%s\n", prHeaderWidth, "Closed:", jqBodyDate(pr, "closed_at"))
		if jqBodyString(pr, "merged") == "true" {
			fmt.Fprintf(stdout, "  %-*s%s\n", prHeaderWidth, "Merged:", "yes "+jqBodyDate(pr, "merged_at"))
		} else {
			fmt.Fprintf(stdout, "  %-*s%s\n", prHeaderWidth, "Merged:", "no")
		}
	}
	fmt.Fprintf(stdout, "  %-*s%s → %s\n", prHeaderWidth, "Branch:", jqBodyString(pr, "head", "label"), jqBodyString(pr, "base", "label"))
	fmt.Fprintln(stdout)

	// body=$(echo "$pr" | jq -r '.body // ""'): command substitution strips
	// every trailing newline (jqBodyAlt chomps), so a body that ends in blank
	// lines loses them and the `echo` below puts exactly one back.
	body := jqBodyAlt(pr, "", "body")
	if body != "" {
		bashEcho(body)
		fmt.Fprintln(stdout)
	}

	commentList := mustJSON(comments)
	if count := jqLengthOf(commentList); count > 0 {
		fmt.Fprintf(stdout, "--- %d comment(s) ---\n", count)
		// jq -r '.[] | "[\(.created_at[:10])] \(.user.login):\n\(.body)\n"',
		// where jq's own newline after each result makes the blank line.
		for _, comment := range jsonArray(commentList) {
			fmt.Fprintf(stdout, "[%s] %s:\n%s\n\n",
				jqString(jqPrefixSlice(jsonField(comment, "created_at"), 10)),
				jqString(jsonPath(comment, "user", "login")),
				jqString(jsonField(comment, "body")))
		}
	}

	reviewIDs := prReviewIDsWithComments(reviews)
	if reviewIDs == "" {
		return
	}

	totalInline := 0
	var inlineOutput strings.Builder
	for _, rid := range hereStringLines(reviewIDs) {
		rc := mustJSON(api("GET", "/repos/"+repoOpt+"/pulls/"+number+"/reviews/"+rid+"/comments", nil))
		totalInline += jqLengthOf(rc)

		var block strings.Builder
		for _, comment := range jsonArray(rc) {
			fmt.Fprint(&block, prInlineComment(comment))
		}
		// inline_output+=$(...) strips every trailing newline from the block,
		// and the += $'\n' that follows adds exactly one back — including for
		// a review whose comment list came back empty, which contributes a
		// bare newline to the output.
		inlineOutput.WriteString(chompNewlines(block.String()))
		inlineOutput.WriteString("\n")
	}

	if totalInline > 0 {
		fmt.Fprintf(stdout, "--- %d inline comment(s) ---\n", totalInline)
		// printf '%s\n' "$inline_output"
		fmt.Fprintf(stdout, "%s\n", inlineOutput.String())
	}
}

// prReviewComments mirrors pr_review_comments (forge line 475).
func prReviewComments(args []string) {
	if containsHelpFlag(args) {
		commandUsage("pr review-comments")
		return
	}
	parseRepoPositionals("pr review-comments", args)
	if len(positionalArgs) != 1 {
		die("usage: forge pr review-comments <number>")
	}
	requireRepo()
	number := positionalArgs[0]

	reviews := api("GET", "/repos/"+repoOpt+"/pulls/"+number+"/reviews", nil)
	reviewIDs := prReviewIDsWithComments(reviews)
	if reviewIDs == "" {
		fmt.Fprintf(stdout, "No inline comments on PR #%s\n", number)
		return
	}

	for _, rid := range hereStringLines(reviewIDs) {
		rc := mustJSON(api("GET", "/repos/"+repoOpt+"/pulls/"+number+"/reviews/"+rid+"/comments", nil))
		for _, comment := range jsonArray(rc) {
			fmt.Fprintf(stdout, "id %s %s", jqString(jsonField(comment, "id")), prInlineComment(comment))
		}
	}
}

// prEdit mirrors pr_edit (forge line 553).
func prEdit(args []string) { editResource("pr edit", "pulls", "PR", args) }

// prComment mirrors pr_comment (forge line 592).
func prComment(args []string) { postComment("pr comment", args) }

// prReply mirrors pr_reply (forge line 595).
//
// A reply has to be posted to the review that owns the original comment:
// posting to a new review does not thread, whatever path, position or
// in_reply_to it carries. So the comment is looked for review by review.
func prReply(args []string) {
	if containsHelpFlag(args) {
		commandUsage("pr reply")
		return
	}

	number, numberSet := "", false
	commentID, commentIDSet := "", false
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
			die("unknown option for pr reply: %s", args[0])
		default:
			switch {
			case !numberSet:
				number = args[0]
				numberSet = true
			case !commentIDSet:
				commentID = args[0]
				commentIDSet = true
			default:
				die("unexpected argument for pr reply: %s", args[0])
			}
			args = args[1:]
		}
	}

	if !numberSet || !commentIDSet || number == "" || commentID == "" {
		die("usage: forge pr reply <number> <comment-id> [--body <text> | --body-file <file>]")
	}
	if !bodySet {
		die("pr reply requires --body or --body-file")
	}
	requireRepo()

	reviews := api("GET", "/repos/"+repoOpt+"/pulls/"+number+"/reviews", nil)
	reviewIDs := prReviewIDsWithComments(reviews)

	origReviewID, origPath, origPosition := "", "", "0"
	// bash guards neither the loop nor the id: with no reviewed comments at
	// all the here-string still yields one empty line, and the request goes to
	// ".../reviews//comments", whose failure ends the run.
	for _, rid := range hereStringLines(reviewIDs) {
		rc := mustJSON(api("GET", "/repos/"+repoOpt+"/pulls/"+number+"/reviews/"+rid+"/comments", nil))

		// jq -r --argjson cid "$comment_id" 'first(.[] | select(.id == $cid)) // empty'
		// The id is parsed as JSON, not compared as text, and a comment-id
		// that is not JSON at all is jq's error to report.
		cid := argJSONValue(commentID)
		var match any
		for _, comment := range jsonArray(rc) {
			if jqEqualValues(jsonField(comment, "id"), cid) {
				match = comment
				break
			}
		}
		if match == nil || match == false {
			continue
		}

		origReviewID = rid
		// Both are their own `$(echo "$match" | jq -r ...)` capture.
		origPath = chompNewlines(jqString(jsonField(match, "path")))
		origPosition = chompNewlines(jqAlt(jsonField(match, "position"), "0"))
		break
	}

	if origReviewID == "" {
		fmt.Fprintf(stderr, "forge: comment %s not found on PR #%s\n", commentID, number)
		exit(1)
	}

	// jq -n --arg path --argjson new_position --arg body
	payload := `{"path":` + jsonString(origPath) +
		`,"new_position":` + jsonLiteral(argJSONValue(origPosition)) +
		`,"body":` + jsonString(bodyOpt) + `}`
	url := jqBodyString(api("POST", "/repos/"+repoOpt+"/pulls/"+number+"/reviews/"+origReviewID+"/comments", []byte(payload)), "html_url")
	fmt.Fprintf(stdout, "Reply posted: %s\n", url)
}

// prCreate mirrors pr_create (forge line 671).
func prCreate(args []string) {
	if containsHelpFlag(args) {
		commandUsage("pr create")
		return
	}

	title, head, base := "", "", ""
	titleSet, headSet, baseSet := false, false, false
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
		case args[0] == "-H" || args[0] == "--head":
			requireOptionValue(args[0], len(args))
			if headSet {
				die("%s specified more than once", args[0])
			}
			head = args[1]
			headSet = true
			args = args[2:]
		case args[0] == "-B" || args[0] == "--base":
			requireOptionValue(args[0], len(args))
			if baseSet {
				die("%s specified more than once", args[0])
			}
			base = args[1]
			baseSet = true
			args = args[2:]
		case args[0] == "-b" || args[0] == "--body" || args[0] == "-F" || args[0] == "--body-file":
			requireOptionValue(args[0], len(args))
			setBodyOption(args[0], args[1])
			args = args[2:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for pr create: %s", args[0])
		default:
			die("unexpected argument for pr create: %s", args[0])
		}
	}

	if !titleSet {
		die("pr create requires --title")
	}
	if title == "" {
		die("--title cannot be empty")
	}
	if headSet && head == "" {
		die("--head cannot be empty")
	}
	if baseSet && base == "" {
		die("--base cannot be empty")
	}
	requireRepo()

	if !headSet {
		head = currentBranch()
		if head == "" {
			die("cannot infer PR head branch; use --head")
		}
	}

	if !baseSet {
		// jq -r '.default_branch // empty': a missing default branch prints
		// nothing, which is the same empty string as a body without one.
		base = jqBodyAlt(api("GET", "/repos/"+repoOpt, nil), "", "default_branch")
		if base == "" {
			die("could not determine repository default branch; use --base")
		}
	}

	payload := `{"title":` + jsonString(title) +
		`,"head":` + jsonString(head) +
		`,"base":` + jsonString(base) +
		`,"body":` + jsonString(bodyOpt) + `}`
	url := jqBodyString(api("POST", "/repos/"+repoOpt+"/pulls", []byte(payload)), "html_url")
	fmt.Fprintf(stdout, "PR created: %s\n", url)
}

// prFindByHead mirrors pr_find_by_head (forge line 746): the PR number for an
// open PR with this head branch, and nothing at all — not even a newline —
// when there is none. The pulls list endpoint ignores a head filter, so the
// matching happens here.
func prFindByHead(args []string) {
	if containsHelpFlag(args) {
		commandUsage("pr find-by-head")
		return
	}
	parseRepoPositionals("pr find-by-head", args)
	if len(positionalArgs) != 1 {
		die("usage: forge pr find-by-head <branch>")
	}
	requireRepo()
	headBranch := positionalArgs[0]

	number := prNumberForHead(api("GET", "/repos/"+repoOpt+"/pulls?state=open&limit=50", nil), headBranch)
	if number != "" {
		fmt.Fprintln(stdout, number)
	}
}

// resolvePRTarget mirrors resolve_pr_target (forge line 758): a number, a URL
// on this forge, or the head branch of an open PR.
//
// bash leaves the answer in the PR_NUMBER global and lets the URL form
// overwrite REPO; the number is returned here instead, but the assignment to
// repoOpt stays, because a URL names the repository it belongs to.
func resolvePRTarget(target string) string {
	if isInteger(target) {
		requireRepo()
		return target
	}

	base := strings.TrimSuffix(forgeURL, "/")
	if strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") {
		if !strings.HasPrefix(target, base+"/") {
			die("PR target must be a number, URL on %s, or branch name", base)
		}
		path := strings.TrimPrefix(target, base+"/")
		// IFS=/ read -r owner repo resource number extra: one line only, and
		// the last name collects everything after the fourth separator, so a
		// deeper path lands in extra and a bare trailing slash does not.
		if i := strings.IndexByte(path, '\n'); i >= 0 {
			path = path[:i]
		}
		fields := strings.SplitN(path, "/", 5)
		for len(fields) < 5 {
			fields = append(fields, "")
		}
		owner, repo, resource, number, extra := fields[0], fields[1], fields[2], fields[3], fields[4]
		if owner == "" || repo == "" || resource != "pulls" || !isInteger(number) || extra != "" {
			die("PR target must be a number, URL on %s, or branch name", base)
		}
		repoOpt = owner + "/" + repo
		return number
	}

	requireRepo()
	number := prNumberForHead(api("GET", "/repos/"+repoOpt+"/pulls?state=open&limit=50", nil), target)
	if number == "" {
		die("no open PR found for branch: %s", target)
	}
	return number
}

// prClose mirrors pr_close (forge line 791).
func prClose(args []string) {
	if containsHelpFlag(args) {
		commandUsage("pr close")
		return
	}

	target, targetSet := "", false
	comment, commentSet := "", false
	deleteBranch := false

	for len(args) > 0 {
		switch {
		case args[0] == "-R" || args[0] == "--repo":
			setRepoOption(args)
			args = args[2:]
		case args[0] == "-c" || args[0] == "--comment":
			requireOptionValue(args[0], len(args))
			if commentSet {
				die("%s specified more than once", args[0])
			}
			comment = args[1]
			if comment == "" {
				die("--comment cannot be empty")
			}
			commentSet = true
			args = args[2:]
		case args[0] == "-d" || args[0] == "--delete-branch":
			deleteBranch = true
			args = args[1:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for pr close: %s", args[0])
		default:
			if targetSet {
				die("unexpected argument for pr close: %s", args[0])
			}
			target = args[0]
			targetSet = true
			args = args[1:]
		}
	}

	if !targetSet || target == "" {
		die("usage: forge pr close {<number> | <url> | <branch>} [-c <text>] [-d] [-R <owner/repo>]")
	}

	number := resolvePRTarget(target)

	if commentSet {
		api("POST", "/repos/"+repoOpt+"/issues/"+number+"/comments", []byte(`{"body":`+jsonString(comment)+`}`))
	}

	headRef := ""
	if deleteBranch {
		prData := api("GET", "/repos/"+repoOpt+"/pulls/"+number, nil)
		headRef = jqBodyString(prData, "head", "ref")
		baseRef := jqBodyString(prData, "base", "ref")
		if headRef == baseRef {
			fmt.Fprintf(stderr, "forge: warning: not deleting branch %s (same as base branch)\n", headRef)
			deleteBranch = false
		}
	}

	url := jqBodyString(api("PATCH", "/repos/"+repoOpt+"/pulls/"+number, []byte(`{"state":"closed"}`)), "html_url")

	if deleteBranch && headRef != "" {
		// The only guarded call in the script: branch deletion needs repo
		// write, so it is the one most likely to be refused, and its stderr is
		// deliberately not suppressed. A refusal is a warning, not a failure —
		// the PR is closed either way.
		if _, rc := apiTry("DELETE", "/repos/"+repoOpt+"/branches/"+jqURI(headRef), nil); rc == 0 {
			fmt.Fprintf(stdout, "PR closed: %s (branch %s deleted)\n", url, headRef)
		} else {
			fmt.Fprintf(stdout, "PR closed: %s\n", url)
			fmt.Fprintf(stderr, "forge: warning: failed to delete branch %s\n", headRef)
		}
		return
	}
	fmt.Fprintf(stdout, "PR closed: %s\n", url)
}

// --- PR rendering helpers ---

// prReviewIDsWithComments is
//
//	jq -r '.[] | select(.comments_count > 0) | .id'
//
// over a reviews response: one id per line, as one string, because bash keeps
// it as one string and feeds it to `read` line by line. Empty means no review
// reported any comments — which the callers each answer differently.
func prReviewIDsWithComments(reviews []byte) string {
	var ids []string
	for _, review := range jsonArray(mustJSON(reviews)) {
		if jqGreaterThanZero(jsonField(review, "comments_count")) {
			ids = append(ids, jqString(jsonField(review, "id")))
		}
	}
	// review_ids=$(...): the capture chomps, so a last id that is a string
	// ending in "\n" does not turn into an extra empty line for `read`.
	return chompNewlines(strings.Join(ids, "\n"))
}

// prInlineComment renders one inline comment the way both templates that use
// it do, including the blank line jq's own newline leaves behind:
//
//	"[\(.created_at[:10])] \(.user.login) on \(.path) line \(.line // "?"):\n\(.diff_hunk)\n> \(.body)\n"
func prInlineComment(comment any) string {
	return fmt.Sprintf("[%s] %s on %s line %s:\n%s\n> %s\n\n",
		jqString(jqPrefixSlice(jsonField(comment, "created_at"), 10)),
		jqString(jsonPath(comment, "user", "login")),
		jqString(jsonField(comment, "path")),
		jqAlt(jsonField(comment, "line"), "?"),
		jqString(jsonField(comment, "diff_hunk")),
		jqString(jsonField(comment, "body")))
}

// prNumberForHead is
//
//	jq -r --arg head "$h" 'first(.[] | select(.head.ref == $h) | .number) // empty'
//
// over a pulls list: the number of the first open PR with that head branch, or
// "" when there is none — which is also what a null number yields, since the
// alternative operator swallows it.
func prNumberForHead(pulls []byte, head string) string {
	for _, pr := range jsonArray(mustJSON(pulls)) {
		if ref, ok := jsonPath(pr, "head", "ref").(string); !ok || ref != head {
			continue
		}
		number := jsonField(pr, "number")
		if number == nil || number == false {
			return ""
		}
		// PR_NUMBER=$(api ... | jq -r '...'): a capture, so it chomps.
		return chompNewlines(jqString(number))
	}
	return ""
}

// bashEcho writes one argument the way the bash `echo` builtin does.
//
// It reads a leading argument of the form -[neE]+ as options rather than as
// data, so a PR body of exactly "-n" prints nothing at all and one of "-e"
// prints only the newline. There are no further arguments here, so the escape
// processing -e would enable never applies.
func bashEcho(s string) {
	text, newline := s, true
	if isEchoOptionWord(s) {
		text, newline = "", !strings.ContainsRune(s, 'n')
	}
	fmt.Fprint(stdout, text)
	if newline {
		fmt.Fprintln(stdout)
	}
}

func isEchoOptionWord(s string) bool {
	if len(s) < 2 || s[0] != '-' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] != 'n' && s[i] != 'e' && s[i] != 'E' {
			return false
		}
	}
	return true
}

// hereStringLines splits a value the way `while IFS= read -r x; done <<< "$s"`
// iterates it. The here-string appends a newline, so an empty value is one
// empty line rather than none — the difference between pr_reply skipping its
// lookup and pr_reply asking for ".../reviews//comments".
func hereStringLines(s string) []string {
	return strings.Split(s, "\n")
}

// --- jq semantics ---

// jqLengthOf is jq's length/0: the number of elements, keys or characters, and
// for a number its absolute value. jq rejects it for booleans, which no
// response body reaches these call sites with.
func jqLengthOf(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case []any:
		return len(t)
	case map[string]any:
		return len(t)
	case string:
		return len([]rune(t))
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return 0
		}
		if f < 0 {
			f = -f
		}
		return int(f)
	}
	return 0
}

// jqPrefixSlice is jq's `.[:n]`: the first n characters of a string or the
// first n elements of an array. Slicing null yields null, which interpolates
// as the text "null" — what a comment with no created_at renders as.
func jqPrefixSlice(v any, n int) any {
	switch t := v.(type) {
	case string:
		runes := []rune(t)
		if len(runes) > n {
			runes = runes[:n]
		}
		return string(runes)
	case []any:
		if len(t) > n {
			return t[:n]
		}
		return t
	}
	return v
}

// jqGreaterThanZero is jq's `v > 0`, which orders values by type before value:
// null and booleans sort below every number, and strings, arrays and objects
// sort above every number. So a comments_count that arrived as a string is
// greater than zero however it reads.
func jqGreaterThanZero(v any) bool {
	switch t := v.(type) {
	case nil, bool:
		return false
	case json.Number:
		f, err := t.Float64()
		return err == nil && f > 0
	}
	return true
}

// jqEqualValues is jq's `==`. Numbers compare by value rather than by
// spelling, so 99 equals 99.0; everything else must match in type and content.
//
// jq keeps a number's literal precision rather than rounding it to a double,
// so it can tell 9007199254740992 from 9007199254740993 (verified against jq
// 1.8.1). Two integral literals are therefore compared as int64 first; only
// when one of them is not an integer -- or is too big for an int64 -- does the
// float comparison, which cannot separate those two, take over.
func jqEqualValues(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		if xi, xerr := x.Int64(); xerr == nil {
			if yi, yerr := y.Int64(); yerr == nil {
				return xi == yi
			}
		}
		xf, xerr := x.Float64()
		yf, yerr := y.Float64()
		if xerr != nil || yerr != nil {
			return x.String() == y.String()
		}
		return xf == yf
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jqEqualValues(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, present := y[k]
			if !present || !jqEqualValues(xv, yv) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

// argJSONValue parses one value the way jq's --argjson does: the text must be
// exactly one JSON value, with nothing but whitespace around it. jq rejects
// anything else with a usage error and exit status 2, which is what a
// comment-id like "abc" gets — bash's errexit then ends the run there.
//
// jq's parser is laxer than encoding/json's about leading zeroes, and takes
// `forge pr reply 1 001 -b hi` as comment 1 rather than failing (verified
// against jq 1.8.1 and end to end against ./forge), so a digit-only argument
// is normalized before the strict decode.
func argJSONValue(text string) any {
	if digits := strings.TrimSpace(text); isInteger(digits) {
		text = jqArgJSONInteger(digits)
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		jqArgJSONError()
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		jqArgJSONError()
	}
	return v
}

// jqArgJSONError reproduces jq's own diagnostic, which is on the outside of
// the script and therefore part of its behaviour.
func jqArgJSONError() {
	fmt.Fprint(stderr, "jq: invalid JSON text passed to --argjson\n"+
		"Use jq --help for help with command-line options,\n"+
		"or see the jq manpage, or online docs  at https://jqlang.org\n")
	exit(2)
}

// jsonLiteral renders a decoded value back as JSON text, the way jq embeds an
// --argjson value into a payload: numbers keep the spelling they arrived with,
// and strings are not HTML-escaped.
func jsonLiteral(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case json.Number:
		return t.String()
	case string:
		return jsonString(t)
	default:
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return "null"
		}
		return strings.TrimSuffix(buf.String(), "\n")
	}
}
