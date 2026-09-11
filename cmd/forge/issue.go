package main

// Issue commands (forge lines 868-1439).

import (
	"fmt"
	"strings"
)

// issueList mirrors issue_list (forge line 868).
func issueList(args []string) {
	if containsHelpFlag(args) {
		commandUsage("issue list")
		return
	}
	state := "open"
	limit := "30"
	search := ""
	milestone := ""
	var labels []string

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
		case "-l", "--label":
			requireOptionValue(args[0], len(args))
			if args[1] == "" {
				die("--label cannot be empty")
			}
			appendCSVValues(&labels, args[0], args[1])
			args = args[2:]
		case "-m", "--milestone":
			requireOptionValue(args[0], len(args))
			if milestone != "" {
				die("%s specified more than once", args[0])
			}
			milestone = args[1]
			if milestone == "" {
				die("--milestone cannot be empty")
			}
			args = args[2:]
		case "-S", "--search":
			requireOptionValue(args[0], len(args))
			search = args[1]
			args = args[2:]
		default:
			if strings.HasPrefix(args[0], "-") {
				die("unknown option for issue list: %s", args[0])
			}
			die("unexpected argument for issue list: %s", args[0])
		}
	}
	requireRepo()

	path := "/repos/" + repoOpt + "/issues?type=issues&state=" + state + "&limit=" + limit
	if len(labels) > 0 {
		path += "&labels=" + jqURI(joinCSVValues(labels))
	}
	if milestone != "" {
		path += "&milestones=" + jqURI(milestone)
	}
	if search != "" {
		path += "&q=" + jqURI(search)
	}

	items := jsonArray(mustJSON(api("GET", path, nil)))
	if len(items) == 0 {
		if state == "open" {
			fmt.Fprintf(stdout, "No open issues in %s\n", repoOpt)
		} else {
			fmt.Fprintf(stdout, "No %s issues in %s\n", state, repoOpt)
		}
		return
	}

	// jq -r '.[] | "\(.number)\t\(.title)\t\(.user.login)\t\((.labels // []) | ...)\t\(.milestone.title // "-")"'
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{
			jqString(jsonField(item, "number")),
			jqString(jsonField(item, "title")),
			jqString(jsonPath(item, "user", "login")),
			formatIssueLabelNames(item),
			formatIssueMilestoneTitle(item),
		})
	}
	fmt.Fprint(stdout, columnTable(tabLines(rows)))
}

// issueView mirrors issue_view (forge line 941), plus --json/--jq
// (allod/tools#190): with --json given, request only the issue object and
// print exactly the requested fields, nothing else.
func issueView(args []string) {
	if containsHelpFlag(args) {
		commandUsage("issue view")
		return
	}

	number, jsonFields, jqSet, jqExpr := parseViewArgs("issue view", args, issueViewJSONFields)
	requireRepo()

	if len(jsonFields) > 0 {
		runViewJSON("/repos/"+repoOpt+"/issues/"+number, jsonFields, jqSet, jqExpr, nil)
		return
	}

	issueResp := api("GET", "/repos/"+repoOpt+"/issues/"+number, nil)
	commentsResp := api("GET", "/repos/"+repoOpt+"/issues/"+number+"/comments", nil)

	// Every field below is captured through a bash command substitution in
	// the original, which strips all trailing newlines from what jq -r
	// printed, not just the one jq itself appends. jqBodyString/jqBodyAlt
	// (shared.go) chomp for that reason; the labels/milestone formatters take
	// an already decoded value, so they are chomped by hand here.
	//
	// Each is also its own fresh `echo "$issue" | jq -r ...`, so an empty
	// response body (jq over empty input prints nothing) must render as ""
	// here, not as the "null"/"-" that decoding an empty body to JSON null
	// would produce; jqBodyString/jqBodyAlt carry that same distinction for
	// the single-field extractions, and the formatters are guarded by hand.
	title := jqBodyString(issueResp, "title")
	state := jqBodyString(issueResp, "state")
	author := jqBodyString(issueResp, "user", "login")
	created := jqBodyDate(issueResp, "created_at")
	updated := jqBodyDate(issueResp, "updated_at")
	labels, milestone := "", ""
	if !jsonIsEmpty(issueResp) {
		decoded := mustJSON(issueResp)
		labels = chompNewlines(formatIssueLabelNames(decoded))
		milestone = chompNewlines(formatIssueMilestoneTitle(decoded))
	}

	// Column width 11 = len("Milestone: "), the widest label plus one space;
	// Created/Updated/Closed all fit inside it.
	const issueHeaderWidth = 11
	fmt.Fprintf(stdout, "Issue #%s: %s\n", number, title)
	fmt.Fprintf(stdout, "  %-*s%s\n", issueHeaderWidth, "State:", state)
	fmt.Fprintf(stdout, "  %-*s%s\n", issueHeaderWidth, "Author:", author)
	fmt.Fprintf(stdout, "  %-*s%s\n", issueHeaderWidth, "Created:", created)
	fmt.Fprintf(stdout, "  %-*s%s\n", issueHeaderWidth, "Updated:", updated)
	if state == "closed" {
		fmt.Fprintf(stdout, "  %-*s%s\n", issueHeaderWidth, "Closed:", jqBodyDate(issueResp, "closed_at"))
	}
	fmt.Fprintf(stdout, "  %-*s%s\n", issueHeaderWidth, "Labels:", labels)
	fmt.Fprintf(stdout, "  %-*s%s\n", issueHeaderWidth, "Milestone:", milestone)
	fmt.Fprintln(stdout)

	body := jqBodyAlt(issueResp, "", "body")
	if body != "" {
		// `echo "$body"` (forge line 969), not printf: a body of exactly "-n"
		// is an option word to the builtin and prints nothing at all. Same
		// treatment pr_view's body already gets.
		bashEcho(body)
		fmt.Fprintln(stdout)
	}

	var items []any
	if !jsonIsEmpty(commentsResp) {
		items = jsonArray(mustJSON(commentsResp))
	}
	if len(items) > 0 {
		fmt.Fprintf(stdout, "--- %d comment(s) ---\n", len(items))
		for _, c := range items {
			// `.created_at[:10]`: the first 10 Unicode codepoints, jq-style
			// (not the first 10 bytes).
			created := jqString(jsonField(c, "created_at"))
			if r := []rune(created); len(r) > 10 {
				created = string(r[:10])
			}
			login := jqString(jsonPath(c, "user", "login"))
			body := jqString(jsonField(c, "body"))
			// The template embeds one trailing "\n" after the body, and
			// jq -r appends one more after every top-level result, so each
			// block ends with a blank line -- including the last one.
			fmt.Fprintf(stdout, "[%s] %s:\n%s\n\n", created, login, body)
		}
	}
}

// issueCreate mirrors issue_create (forge line 981).
func issueCreate(args []string) {
	if containsHelpFlag(args) {
		commandUsage("issue create")
		return
	}
	title := ""
	titleSet := false
	milestone := ""
	milestoneSet := false
	var labels []string
	resetBodyOption()

	for len(args) > 0 {
		switch args[0] {
		case "-R", "--repo":
			setRepoOption(args)
			args = args[2:]
		case "-t", "--title":
			requireOptionValue(args[0], len(args))
			if titleSet {
				die("%s specified more than once", args[0])
			}
			title = args[1]
			titleSet = true
			args = args[2:]
		case "-b", "--body", "-F", "--body-file":
			requireOptionValue(args[0], len(args))
			setBodyOption(args[0], args[1])
			args = args[2:]
		case "-l", "--label":
			requireOptionValue(args[0], len(args))
			if args[1] == "" {
				die("--label cannot be empty")
			}
			appendCSVValues(&labels, args[0], args[1])
			args = args[2:]
		case "-m", "--milestone":
			requireOptionValue(args[0], len(args))
			if milestoneSet {
				die("%s specified more than once", args[0])
			}
			milestone = args[1]
			milestoneSet = true
			if milestone == "" {
				die("--milestone cannot be empty")
			}
			args = args[2:]
		default:
			if strings.HasPrefix(args[0], "-") {
				die("unknown option for issue create: %s", args[0])
			}
			die("unexpected argument for issue create: %s", args[0])
		}
	}

	if !titleSet {
		die("issue create requires --title")
	}
	if title == "" {
		die("--title cannot be empty")
	}
	requireRepo()

	labelsSet := false
	labelsJSON := "[]"
	milestoneID := "0"
	if len(labels) > 0 {
		labelsSet = true
		labelsJSON = resolveLabelIDsJSON(labels)
	}
	if milestoneSet {
		milestoneID = resolveMilestoneID(milestone)
	}

	payload := `{"title":` + jsonString(title) + `,"body":` + jsonString(bodyOpt)
	if labelsSet {
		payload += `,"labels":` + labelsJSON
	}
	if milestoneSet {
		payload += `,"milestone":` + jqArgJSON(milestoneID)
	}
	payload += "}"

	resp := api("POST", "/repos/"+repoOpt+"/issues", []byte(payload))
	fmt.Fprintf(stdout, "Issue created: %s\n", jqBodyString(resp, "html_url"))
}

// issueEdit mirrors issue_edit (forge line 1054).
func issueEdit(args []string) {
	if containsHelpFlag(args) {
		commandUsage("issue edit")
		return
	}
	number := ""
	numberSet := false
	title := ""
	titleSet := false
	milestone := ""
	milestoneSet := false
	removeMilestone := false
	var addLabels, removeLabels []string
	resetBodyOption()

	for len(args) > 0 {
		switch args[0] {
		case "-R", "--repo":
			setRepoOption(args)
			args = args[2:]
		case "-t", "--title":
			requireOptionValue(args[0], len(args))
			if titleSet {
				die("%s specified more than once", args[0])
			}
			title = args[1]
			titleSet = true
			args = args[2:]
		case "-b", "--body", "-F", "--body-file":
			requireOptionValue(args[0], len(args))
			setBodyOption(args[0], args[1])
			args = args[2:]
		case "-m", "--milestone":
			requireOptionValue(args[0], len(args))
			if milestoneSet {
				die("%s specified more than once", args[0])
			}
			milestone = args[1]
			milestoneSet = true
			if milestone == "" {
				die("--milestone cannot be empty")
			}
			args = args[2:]
		case "--remove-milestone", "--clear-milestone":
			removeMilestone = true
			args = args[1:]
		case "--add-label":
			requireOptionValue(args[0], len(args))
			if args[1] == "" {
				die("--add-label cannot be empty")
			}
			appendCSVValues(&addLabels, args[0], args[1])
			args = args[2:]
		case "--remove-label":
			requireOptionValue(args[0], len(args))
			if args[1] == "" {
				die("--remove-label cannot be empty")
			}
			appendCSVValues(&removeLabels, args[0], args[1])
			args = args[2:]
		default:
			if strings.HasPrefix(args[0], "-") {
				die("unknown option for issue edit: %s", args[0])
			}
			if numberSet {
				die("unexpected argument for issue edit: %s", args[0])
			}
			number = args[0]
			numberSet = true
			args = args[1:]
		}
	}

	if !numberSet || number == "" {
		die("usage: forge issue edit <number> [--title <title>] [--body <text> | --body-file <file>] [--milestone <milestone> | --remove-milestone] [--add-label <label>] [--remove-label <label>]")
	}
	if !(titleSet || bodySet || milestoneSet || removeMilestone || len(addLabels) > 0 || len(removeLabels) > 0) {
		die("issue edit requires --title, --body, --body-file, --milestone, --remove-milestone, --add-label, or --remove-label")
	}
	if titleSet && title == "" {
		die("--title cannot be empty")
	}
	if milestoneSet && removeMilestone {
		die("--milestone cannot be combined with --remove-milestone")
	}
	requireRepo()

	milestoneID := "0"
	if milestoneSet {
		milestoneID = resolveMilestoneID(milestone)
	}

	var fields []string
	if titleSet {
		fields = append(fields, `"title":`+jsonString(title))
	}
	if bodySet {
		fields = append(fields, `"body":`+jsonString(bodyOpt))
	}
	if milestoneSet {
		fields = append(fields, `"milestone":`+jqArgJSON(milestoneID))
	}
	if removeMilestone {
		fields = append(fields, `"milestone":0`)
	}
	payload := "{" + strings.Join(fields, ",") + "}"

	url := ""
	if titleSet || bodySet || milestoneSet || removeMilestone {
		resp := api("PATCH", "/repos/"+repoOpt+"/issues/"+number, []byte(payload))
		url = jqBodyString(resp, "html_url")
	}
	if len(addLabels) > 0 {
		body := `{"labels":` + jsonMixedLabelArrayFromArgs(addLabels) + `}`
		api("POST", "/repos/"+repoOpt+"/issues/"+number+"/labels", []byte(body))
	}
	for _, label := range removeLabels {
		api("DELETE", "/repos/"+repoOpt+"/issues/"+number+"/labels/"+jqURI(label), nil)
	}
	if url == "" {
		resp := api("GET", "/repos/"+repoOpt+"/issues/"+number, nil)
		url = jqBodyString(resp, "html_url")
	}
	fmt.Fprintf(stdout, "Issue updated: %s\n", url)
}

// issueComment mirrors issue_comment (forge line 1161): a one-line delegate
// to the shared post_comment helper (shared.go, owned by another agent).
func issueComment(args []string) { postComment("issue comment", args) }

// issueLabels mirrors issue_labels (forge line 1163).
func issueLabels(args []string) {
	if containsHelpFlag(args) {
		commandUsage("issue labels")
		return
	}
	number := ""
	numberSet := false
	clear := false
	var addLabels, removeLabels, setLabels []string

	for len(args) > 0 {
		switch args[0] {
		case "-R", "--repo":
			setRepoOption(args)
			args = args[2:]
		case "--add", "--add-label":
			requireOptionValue(args[0], len(args))
			if args[1] == "" {
				die("--add cannot be empty")
			}
			appendCSVValues(&addLabels, args[0], args[1])
			args = args[2:]
		case "--remove", "--remove-label":
			requireOptionValue(args[0], len(args))
			if args[1] == "" {
				die("--remove cannot be empty")
			}
			appendCSVValues(&removeLabels, args[0], args[1])
			args = args[2:]
		case "--set":
			requireOptionValue(args[0], len(args))
			if args[1] == "" {
				die("--set cannot be empty")
			}
			appendCSVValues(&setLabels, args[0], args[1])
			args = args[2:]
		case "--clear":
			clear = true
			args = args[1:]
		default:
			if strings.HasPrefix(args[0], "-") {
				die("unknown option for issue labels: %s", args[0])
			}
			if numberSet {
				die("unexpected argument for issue labels: %s", args[0])
			}
			number = args[0]
			numberSet = true
			args = args[1:]
		}
	}

	if !numberSet || number == "" {
		die("usage: forge issue labels <number> [--add-label <label>] [--remove-label <label>] [--set <label>] [--clear]")
	}
	if clear && (len(addLabels) > 0 || len(removeLabels) > 0 || len(setLabels) > 0) {
		die("--clear cannot be combined with label changes")
	}
	if len(setLabels) > 0 && (len(addLabels) > 0 || len(removeLabels) > 0) {
		die("--set cannot be combined with --add or --remove")
	}
	requireRepo()

	var labels []byte
	switch {
	case clear:
		api("DELETE", "/repos/"+repoOpt+"/issues/"+number+"/labels", nil)
		labels = []byte("[]")
	case len(setLabels) > 0:
		body := `{"labels":` + jsonMixedLabelArrayFromArgs(setLabels) + `}`
		labels = api("PUT", "/repos/"+repoOpt+"/issues/"+number+"/labels", []byte(body))
	default:
		if len(addLabels) > 0 {
			body := `{"labels":` + jsonMixedLabelArrayFromArgs(addLabels) + `}`
			labels = api("POST", "/repos/"+repoOpt+"/issues/"+number+"/labels", []byte(body))
		}
		for _, label := range removeLabels {
			api("DELETE", "/repos/"+repoOpt+"/issues/"+number+"/labels/"+jqURI(label), nil)
		}
		if len(removeLabels) > 0 || len(addLabels) == 0 {
			labels = api("GET", "/repos/"+repoOpt+"/issues/"+number+"/labels", nil)
		}
	}

	// echo "Issue #$number labels: $(echo "$labels" | format_labels)": the
	// inner command substitution chomps what the jq template printed.
	labelsText := ""
	if !jsonIsEmpty(labels) {
		labelsText = chompNewlines(formatLabels(mustJSON(labels)))
	}
	fmt.Fprintf(stdout, "Issue #%s labels: %s\n", number, labelsText)
}

// issueMilestone mirrors issue_milestone (forge line 1240).
func issueMilestone(args []string) {
	if containsHelpFlag(args) {
		commandUsage("issue milestone")
		return
	}
	number := ""
	numberSet := false
	milestone := ""
	milestoneSet := false
	clear := false

	for len(args) > 0 {
		switch args[0] {
		case "-R", "--repo":
			setRepoOption(args)
			args = args[2:]
		case "--clear":
			clear = true
			args = args[1:]
		default:
			if strings.HasPrefix(args[0], "-") {
				die("unknown option for issue milestone: %s", args[0])
			}
			switch {
			case !numberSet:
				number = args[0]
				numberSet = true
			case !milestoneSet:
				milestone = args[0]
				milestoneSet = true
			default:
				die("unexpected argument for issue milestone: %s", args[0])
			}
			args = args[1:]
		}
	}

	if !numberSet || number == "" {
		die("usage: forge issue milestone <number> [<milestone> | --clear]")
	}
	if milestoneSet && clear {
		die("<milestone> cannot be combined with --clear")
	}
	requireRepo()

	switch {
	case clear:
		resp := api("PATCH", "/repos/"+repoOpt+"/issues/"+number, []byte(`{"milestone":0}`))
		fmt.Fprintf(stdout, "Issue milestone cleared: %s\n", jqBodyString(resp, "html_url"))
	case milestoneSet:
		if milestone == "" {
			die("milestone cannot be empty")
		}
		milestoneID := jqArgJSON(resolveMilestoneID(milestone))
		resp := api("PATCH", "/repos/"+repoOpt+"/issues/"+number, []byte(`{"milestone":`+milestoneID+`}`))
		fmt.Fprintf(stdout, "Issue milestone updated: %s\n", jqBodyString(resp, "html_url"))
	default:
		resp := api("GET", "/repos/"+repoOpt+"/issues/"+number, nil)
		title := ""
		if !jsonIsEmpty(resp) {
			title = chompNewlines(formatIssueMilestoneTitle(mustJSON(resp)))
		}
		fmt.Fprintf(stdout, "Issue #%s milestone: %s\n", number, title)
	}
}

// resolveIssueTarget mirrors resolve_issue_target (forge line 1293). A bare
// number just needs a repo (from -R or inference); a URL on this forge names
// its own repo, overwriting whatever -R supplied, exactly as bash's direct
// REPO="$owner/$repo" assignment does. It returns the issue number; the repo
// is left in repoOpt, the package-level stand-in for bash's $REPO.
func resolveIssueTarget(target string) string {
	if isInteger(target) {
		requireRepo()
		return target
	}

	base := strings.TrimSuffix(forgeURL, "/")
	if !strings.HasPrefix(target, base+"/") {
		die("issue target must be a number or URL on %s", base)
	}
	path := target[len(base)+1:]

	owner, repo, resource, number, extra := issueTargetPathFields(path)
	if owner == "" || repo == "" || resource != "issues" || !isInteger(number) || extra != "" {
		die("issue target must be a number or URL on %s", base)
	}

	repoOpt = owner + "/" + repo
	return number
}

// normalizeIssueReference mirrors normalize_issue_reference (forge line
// 1319): a bare number becomes "#N"; a URL on this forge is validated and
// returned unchanged (not reconstructed from its parsed pieces).
func normalizeIssueReference(target string) string {
	if isInteger(target) {
		return "#" + target
	}

	base := strings.TrimSuffix(forgeURL, "/")
	if !strings.HasPrefix(target, base+"/") {
		die("duplicate target must be a number or URL on %s", base)
	}
	path := target[len(base)+1:]

	owner, repo, resource, number, extra := issueTargetPathFields(path)
	if owner == "" || repo == "" || resource != "issues" || !isInteger(number) || extra != "" {
		die("duplicate target must be a number or URL on %s", base)
	}
	return target
}

// issueTargetPathFields mirrors `IFS=/ read -r owner repo resource number
// extra <<< "$path"`: up to five slash-separated fields, with the fifth
// capturing everything left over including embedded slashes, and any field
// past the end of the input left empty. strings.SplitN(path, "/", 5) draws
// the same boundaries bash's non-whitespace-IFS field splitting does here
// (verified against bash directly, including double slashes, a trailing
// slash, and more than five segments).
//
// `read` reads one line, so everything from the first newline on is simply
// not looked at: bash accepts $'…/issues/1\njunk' as issue 1 and goes on to
// PATCH it (verified against ./forge). resolvePRTarget (pr.go) does the same
// for the identical `IFS=/ read` at forge line 771. Note that
// normalizeIssueReference still returns the whole multi-line target, junk
// included -- it prints "$target", not the fields it validated.
func issueTargetPathFields(path string) (owner, repo, resource, number, extra string) {
	if i := strings.IndexByte(path, '\n'); i >= 0 {
		path = path[:i]
	}
	var fields [5]string
	copy(fields[:], strings.SplitN(path, "/", 5))
	return fields[0], fields[1], fields[2], fields[3], fields[4]
}

// issueClose mirrors issue_close (forge line 1342).
func issueClose(args []string) {
	if containsHelpFlag(args) {
		commandUsage("issue close")
		return
	}
	target := ""
	targetSet := false
	comment := ""
	commentSet := false
	reason := ""
	reasonSet := false
	duplicateOf := ""
	duplicateSet := false

	for len(args) > 0 {
		switch args[0] {
		case "-R", "--repo":
			setRepoOption(args)
			args = args[2:]
		case "-c", "--comment":
			requireOptionValue(args[0], len(args))
			if commentSet {
				die("%s specified more than once", args[0])
			}
			comment = args[1]
			commentSet = true
			args = args[2:]
		case "-r", "--reason":
			requireOptionValue(args[0], len(args))
			if reasonSet {
				die("%s specified more than once", args[0])
			}
			reason = args[1]
			reasonSet = true
			args = args[2:]
		case "--duplicate-of":
			requireOptionValue(args[0], len(args))
			if duplicateSet {
				die("%s specified more than once", args[0])
			}
			duplicateOf = args[1]
			duplicateSet = true
			args = args[2:]
		default:
			if strings.HasPrefix(args[0], "-") {
				die("unknown option for issue close: %s", args[0])
			}
			if targetSet {
				die("unexpected argument for issue close: %s", args[0])
			}
			target = args[0]
			targetSet = true
			args = args[1:]
		}
	}

	if !targetSet || target == "" {
		die("usage: forge issue close {<number> | <url>} [--comment <text>] [--reason <reason>] [--duplicate-of <issue>]")
	}

	if reasonSet {
		switch reason {
		case "completed", "not planned", "duplicate":
		default:
			die("--reason must be one of: completed, not planned, duplicate")
		}
	} else if duplicateSet {
		reason = "duplicate"
	} else {
		reason = "completed"
	}

	if duplicateSet && reasonSet && reason != "duplicate" {
		die("--duplicate-of cannot be combined with --reason %s", reason)
	}
	if duplicateSet && duplicateOf == "" {
		die("--duplicate-of cannot be empty")
	}

	number := resolveIssueTarget(target)

	reasonNote := ""
	switch reason {
	case "not planned":
		reasonNote = "Closed as not planned."
	case "duplicate":
		if duplicateSet {
			reasonNote = "Duplicate of " + normalizeIssueReference(duplicateOf) + "."
		} else {
			reasonNote = "Closed as duplicate."
		}
	}

	closingComment := comment
	if reasonNote != "" {
		if commentSet && closingComment != "" {
			closingComment += "\n\n"
		}
		closingComment += reasonNote
	}

	if commentSet || reasonNote != "" {
		body := `{"body":` + jsonString(closingComment) + `}`
		api("POST", "/repos/"+repoOpt+"/issues/"+number+"/comments", []byte(body))
	}

	resp := api("PATCH", "/repos/"+repoOpt+"/issues/"+number, []byte(`{"state":"closed"}`))
	fmt.Fprintf(stdout, "Issue closed: %s\n", jqBodyString(resp, "html_url"))
}
