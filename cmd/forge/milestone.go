package main

// Repository milestone commands (forge lines 1717-1925).

import (
	"fmt"
	"strings"
)

// parseMilestoneState mirrors parse_milestone_state (forge line 1717).
func parseMilestoneState(state string) string {
	switch state {
	case "open", "closed", "all":
		return state
	default:
		die("--state must be one of: open, closed, all")
	}
	return ""
}

// milestoneList mirrors milestone_list (forge line 1725).
func milestoneList(args []string) {
	if containsHelpFlag(args) {
		commandUsage("milestone list")
		return
	}
	state := "open"
	for len(args) > 0 {
		switch {
		case args[0] == "-R" || args[0] == "--repo":
			setRepoOption(args)
			args = args[2:]
		case args[0] == "-s" || args[0] == "--state":
			requireOptionValue(args[0], len(args))
			state = parseMilestoneState(args[1])
			args = args[2:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for milestone list: %s", args[0])
		default:
			die("unexpected argument for milestone list: %s", args[0])
		}
	}
	requireRepo()

	result := api("GET", "/repos/"+repoOpt+"/milestones?state="+state+"&limit=100", nil)
	items := jsonArray(mustJSON(result))
	if len(items) == 0 {
		if state == "all" {
			fmt.Fprintf(stdout, "No milestones in %s\n", repoOpt)
		} else {
			fmt.Fprintf(stdout, "No %s milestones in %s\n", state, repoOpt)
		}
		return
	}

	// jq -r '.[] | "\(.id)\t\(.title)\t\(.state)\t\(.open_issues // 0)\t\(.closed_issues // 0)\t\((.due_on // "")[0:10])"'
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{
			jqString(jsonField(item, "id")),
			jqString(jsonField(item, "title")),
			jqString(jsonField(item, "state")),
			jqAlt(jsonField(item, "open_issues"), "0"),
			jqAlt(jsonField(item, "closed_issues"), "0"),
			firstNRunes(jqAlt(jsonField(item, "due_on"), ""), 10),
		})
	}
	fmt.Fprint(stdout, columnTable(tabLines(rows)))
}

// milestoneView mirrors milestone_view (forge line 1761).
func milestoneView(args []string) {
	if containsHelpFlag(args) {
		commandUsage("milestone view")
		return
	}
	parseRepoPositionals("milestone view", args)
	if len(positionalArgs) != 1 {
		die("usage: forge milestone view <id-or-title>")
	}
	requireRepo()

	id := resolveMilestoneID(positionalArgs[0])
	milestone := mustJSON(api("GET", "/repos/"+repoOpt+"/milestones/"+id, nil))
	fmt.Fprintf(stdout, "Milestone #%s: %s\n  State:  %s\n  Open:   %s\n  Closed: %s\n  Due:    %s\n\n%s\n",
		jqString(jsonField(milestone, "id")),
		jqString(jsonField(milestone, "title")),
		jqString(jsonField(milestone, "state")),
		jqAlt(jsonField(milestone, "open_issues"), "0"),
		jqAlt(jsonField(milestone, "closed_issues"), "0"),
		firstNRunes(jqAlt(jsonField(milestone, "due_on"), "-"), 10),
		jqAlt(jsonField(milestone, "description"), ""),
	)
}

// milestoneCreate mirrors milestone_create (forge line 1778). Unlike -t/-d,
// --due and -s/--state are not guarded against repetition (forge has no
// "specified more than once" check for either here); the last one given
// simply wins, and that asymmetry is preserved rather than "fixed".
func milestoneCreate(args []string) {
	if containsHelpFlag(args) {
		commandUsage("milestone create")
		return
	}
	title, description, dueOn, state := "", "", "", ""
	titleSet, descriptionSet, dueSet, stateSet := false, false, false, false
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
		case args[0] == "-d" || args[0] == "--description":
			requireOptionValue(args[0], len(args))
			if descriptionSet {
				die("%s specified more than once", args[0])
			}
			description = args[1]
			descriptionSet = true
			args = args[2:]
		case args[0] == "--due":
			requireOptionValue(args[0], len(args))
			dueOn = normalizeDueOn(args[1])
			dueSet = true
			args = args[2:]
		case args[0] == "-s" || args[0] == "--state":
			requireOptionValue(args[0], len(args))
			state = parseMilestoneState(args[1])
			if state == "all" {
				die("--state for milestone create must be open or closed")
			}
			stateSet = true
			args = args[2:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for milestone create: %s", args[0])
		default:
			die("unexpected argument for milestone create: %s", args[0])
		}
	}

	if !titleSet {
		die("milestone create requires --title")
	}
	if title == "" {
		die("--title cannot be empty")
	}
	requireRepo()

	b := &jsonObjectBuilder{}
	b.str("title", title)
	if descriptionSet {
		b.str("description", description)
	}
	if dueSet {
		b.str("due_on", dueOn)
	}
	if stateSet {
		b.str("state", state)
	}
	milestone := mustJSON(api("POST", "/repos/"+repoOpt+"/milestones", b.bytes()))
	fmt.Fprintf(stdout, "Milestone created: %s %s\n", jqString(jsonField(milestone, "id")), jqString(jsonField(milestone, "title")))
}

// milestoneEdit mirrors milestone_edit (forge line 1841). As with create,
// --due and -s/--state have no "specified more than once" guard; only -t/-d
// do.
func milestoneEdit(args []string) {
	if containsHelpFlag(args) {
		commandUsage("milestone edit")
		return
	}
	target := ""
	targetSet := false
	title, description, dueOn, state := "", "", "", ""
	titleSet, descriptionSet, dueSet, stateSet := false, false, false, false
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
		case args[0] == "-d" || args[0] == "--description":
			requireOptionValue(args[0], len(args))
			if descriptionSet {
				die("%s specified more than once", args[0])
			}
			description = args[1]
			descriptionSet = true
			args = args[2:]
		case args[0] == "--due":
			requireOptionValue(args[0], len(args))
			dueOn = normalizeDueOn(args[1])
			dueSet = true
			args = args[2:]
		case args[0] == "-s" || args[0] == "--state":
			requireOptionValue(args[0], len(args))
			state = parseMilestoneState(args[1])
			if state == "all" {
				die("--state for milestone edit must be open or closed")
			}
			stateSet = true
			args = args[2:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for milestone edit: %s", args[0])
		default:
			if targetSet {
				die("unexpected argument for milestone edit: %s", args[0])
			}
			target = args[0]
			targetSet = true
			args = args[1:]
		}
	}

	if !targetSet || target == "" {
		die("usage: forge milestone edit <id-or-title> [--title <title>] [--description <text>] [--due <date>] [--state open|closed]")
	}
	if !(titleSet || descriptionSet || dueSet || stateSet) {
		die("milestone edit requires a change")
	}
	if titleSet && title == "" {
		die("--title cannot be empty")
	}
	requireRepo()

	id := resolveMilestoneID(target)
	b := &jsonObjectBuilder{}
	if titleSet {
		b.str("title", title)
	}
	if descriptionSet {
		b.str("description", description)
	}
	if dueSet {
		b.str("due_on", dueOn)
	}
	if stateSet {
		b.str("state", state)
	}
	milestone := mustJSON(api("PATCH", "/repos/"+repoOpt+"/milestones/"+id, b.bytes()))
	fmt.Fprintf(stdout, "Milestone updated: %s %s\n", jqString(jsonField(milestone, "id")), jqString(jsonField(milestone, "title")))
}

// milestoneDelete mirrors milestone_delete (forge line 1915). Unlike label
// delete, there is no --yes gate at all: given a resolvable target it deletes
// immediately.
func milestoneDelete(args []string) {
	if containsHelpFlag(args) {
		commandUsage("milestone delete")
		return
	}
	parseRepoPositionals("milestone delete", args)
	if len(positionalArgs) != 1 {
		die("usage: forge milestone delete <id-or-title>")
	}
	requireRepo()

	target := positionalArgs[0]
	id := resolveMilestoneID(target)
	api("DELETE", "/repos/"+repoOpt+"/milestones/"+id, nil)
	fmt.Fprintf(stdout, "Milestone deleted: %s\n", target)
}
