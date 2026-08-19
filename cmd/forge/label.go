package main

// Repository label commands (forge lines 1441-1715).

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// labelList mirrors label_list (forge line 1441). The fetch is a single page
// (-L/--limit, default 30); search, sort and asc/desc order are all applied
// client-side over that page, then the rendered rows are aligned the way
// `column -t -s $'\t'` (forge line 1499) aligns them.
func labelList(args []string) {
	if containsHelpFlag(args) {
		commandUsage("label list")
		return
	}
	limit := "30"
	search := ""
	sortBy := "created"
	order := "asc"
	for len(args) > 0 {
		switch {
		case args[0] == "-R" || args[0] == "--repo":
			setRepoOption(args)
			args = args[2:]
		case args[0] == "-L" || args[0] == "--limit":
			requireOptionValue(args[0], len(args))
			limit = parsePositiveInt(args[0], args[1])
			args = args[2:]
		case args[0] == "-S" || args[0] == "--search":
			requireOptionValue(args[0], len(args))
			search = args[1]
			args = args[2:]
		case args[0] == "--sort":
			requireOptionValue(args[0], len(args))
			switch args[1] {
			case "created", "name":
				sortBy = args[1]
			default:
				die("--sort must be one of: created, name")
			}
			args = args[2:]
		case args[0] == "--order":
			requireOptionValue(args[0], len(args))
			switch args[1] {
			case "asc", "desc":
				order = args[1]
			default:
				die("--order must be one of: asc, desc")
			}
			args = args[2:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for label list: %s", args[0])
		default:
			die("unexpected argument for label list: %s", args[0])
		}
	}
	requireRepo()

	result := api("GET", "/repos/"+repoOpt+"/labels?limit="+limit, nil)
	items := jsonArray(mustJSON(result))

	// select(($search == "") or (name contains) or (description contains)),
	// case-insensitive over ASCII only, like jq's ascii_downcase.
	searchLower := asciiDowncase(search)
	var filtered []any
	for _, item := range items {
		if search == "" ||
			strings.Contains(asciiDowncase(jqAlt(jsonField(item, "name"), "")), searchLower) ||
			strings.Contains(asciiDowncase(jqAlt(jsonField(item, "description"), "")), searchLower) {
			filtered = append(filtered, item)
		}
	}

	// sort_by(...) is always ascending; desc is a separate reverse afterwards
	// (forge line 1491), not a descending comparator, so a stable sort here
	// must be reversed rather than have its comparator flipped: reversing
	// after a stable ascending sort also reverses the order of ties, which a
	// flipped comparator would not.
	sort.SliceStable(filtered, func(i, j int) bool {
		if sortBy == "name" {
			return jqAlt(jsonField(filtered[i], "name"), "") < jqAlt(jsonField(filtered[j], "name"), "")
		}
		return numberOrZero(jsonField(filtered[i], "id")) < numberOrZero(jsonField(filtered[j], "id"))
	})
	if order == "desc" {
		for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
			filtered[i], filtered[j] = filtered[j], filtered[i]
		}
	}

	if len(filtered) == 0 {
		fmt.Fprintf(stdout, "No labels in %s\n", repoOpt)
		return
	}

	// jq -r '.[] | "\(.id)\t\(.name)\t\(.color)\t\(.exclusive // false)\t\(.is_archived // false)\t\(.description // "")"'
	rows := make([][]string, 0, len(filtered))
	for _, item := range filtered {
		rows = append(rows, []string{
			jqString(jsonField(item, "id")),
			jqString(jsonField(item, "name")),
			jqString(jsonField(item, "color")),
			jqAlt(jsonField(item, "exclusive"), "false"),
			jqAlt(jsonField(item, "is_archived"), "false"),
			jqAlt(jsonField(item, "description"), ""),
		})
	}
	fmt.Fprint(stdout, columnTable(tabLines(rows)))
}

// labelCreate mirrors label_create (forge line 1502). The name may come from
// -n/--name or from the first bare positional; the two are interchangeable
// but not both may be given, and neither is documented in the label-create
// usage text (forge's own `command_usage "label create"` heredoc omits
// -n/--name too, so this is a preexisting bash quirk, not a gap in this
// port).
func labelCreate(args []string) {
	if containsHelpFlag(args) {
		commandUsage("label create")
		return
	}
	name, color, description := "", "", ""
	nameSet, colorSet, descriptionSet := false, false, false
	exclusive, archived, force := false, false, false
	for len(args) > 0 {
		switch {
		case args[0] == "-R" || args[0] == "--repo":
			setRepoOption(args)
			args = args[2:]
		case args[0] == "-n" || args[0] == "--name":
			requireOptionValue(args[0], len(args))
			if nameSet {
				die("%s specified more than once", args[0])
			}
			name = args[1]
			nameSet = true
			args = args[2:]
		case args[0] == "-c" || args[0] == "--color":
			requireOptionValue(args[0], len(args))
			if colorSet {
				die("%s specified more than once", args[0])
			}
			color = normalizeColor(args[1])
			colorSet = true
			args = args[2:]
		case args[0] == "-f" || args[0] == "--force":
			force = true
			args = args[1:]
		case args[0] == "-d" || args[0] == "--description":
			requireOptionValue(args[0], len(args))
			if descriptionSet {
				die("%s specified more than once", args[0])
			}
			description = args[1]
			descriptionSet = true
			args = args[2:]
		case args[0] == "--exclusive":
			exclusive = true
			args = args[1:]
		case args[0] == "--archived":
			archived = true
			args = args[1:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for label create: %s", args[0])
		default:
			if nameSet {
				die("unexpected argument for label create: %s", args[0])
			}
			name = args[0]
			nameSet = true
			args = args[1:]
		}
	}

	if !nameSet {
		die("label create requires a name")
	}
	if name == "" {
		die("label name cannot be empty")
	}
	if !colorSet {
		color = randomLabelColor()
	}
	requireRepo()

	// Built once and reused verbatim for both branches below (forge lines
	// 1563-1582): a forced update PATCHes with the very same {name, color,
	// ...} object a plain create would have POSTed.
	b := &jsonObjectBuilder{}
	b.str("name", name)
	b.str("color", color)
	if descriptionSet {
		b.str("description", description)
	}
	if exclusive {
		b.bool("exclusive", true)
	}
	if archived {
		b.bool("is_archived", true)
	}
	payload := b.bytes()

	if force {
		existingID := findLabelIDByName(name)
		if existingID != "" {
			label := mustJSON(api("PATCH", "/repos/"+repoOpt+"/labels/"+existingID, payload))
			fmt.Fprintf(stdout, "Label updated: %s %s\n", jqString(jsonField(label, "id")), jqString(jsonField(label, "name")))
			return
		}
	}
	label := mustJSON(api("POST", "/repos/"+repoOpt+"/labels", payload))
	fmt.Fprintf(stdout, "Label created: %s %s\n", jqString(jsonField(label, "id")), jqString(jsonField(label, "name")))
}

// labelEdit mirrors label_edit (forge line 1586). --exclusive/--no-exclusive
// and --archived/--no-archived are genuinely tri-state: the payload key is
// absent when neither form was given, and present as true or false when one
// was, which is why exclusive/archived each carry a separate *Set flag rather
// than folding into the value itself.
func labelEdit(args []string) {
	if containsHelpFlag(args) {
		commandUsage("label edit")
		return
	}
	target := ""
	targetSet := false
	name, color, description := "", "", ""
	nameSet, colorSet, descriptionSet := false, false, false
	exclusive, exclusiveSet := false, false
	archived, archivedSet := false, false
	for len(args) > 0 {
		switch {
		case args[0] == "-R" || args[0] == "--repo":
			setRepoOption(args)
			args = args[2:]
		case args[0] == "-n" || args[0] == "--name":
			requireOptionValue(args[0], len(args))
			if nameSet {
				die("%s specified more than once", args[0])
			}
			name = args[1]
			nameSet = true
			args = args[2:]
		case args[0] == "-c" || args[0] == "--color":
			requireOptionValue(args[0], len(args))
			if colorSet {
				die("%s specified more than once", args[0])
			}
			color = normalizeColor(args[1])
			colorSet = true
			args = args[2:]
		case args[0] == "-d" || args[0] == "--description":
			requireOptionValue(args[0], len(args))
			if descriptionSet {
				die("%s specified more than once", args[0])
			}
			description = args[1]
			descriptionSet = true
			args = args[2:]
		case args[0] == "--exclusive":
			if exclusiveSet {
				die("exclusive specified more than once")
			}
			exclusive = true
			exclusiveSet = true
			args = args[1:]
		case args[0] == "--no-exclusive":
			if exclusiveSet {
				die("exclusive specified more than once")
			}
			exclusive = false
			exclusiveSet = true
			args = args[1:]
		case args[0] == "--archived":
			if archivedSet {
				die("archived specified more than once")
			}
			archived = true
			archivedSet = true
			args = args[1:]
		case args[0] == "--no-archived":
			if archivedSet {
				die("archived specified more than once")
			}
			archived = false
			archivedSet = true
			args = args[1:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for label edit: %s", args[0])
		default:
			if targetSet {
				die("unexpected argument for label edit: %s", args[0])
			}
			target = args[0]
			targetSet = true
			args = args[1:]
		}
	}

	if !targetSet || target == "" {
		die("usage: forge label edit <id-or-name> [--name <name>] [--color <hex>] [--description <text>] [--exclusive | --no-exclusive] [--archived | --no-archived]")
	}
	if !(nameSet || colorSet || descriptionSet || exclusiveSet || archivedSet) {
		die("label edit requires a change")
	}
	if nameSet && name == "" {
		die("--name cannot be empty")
	}
	requireRepo()

	id := resolveLabelID(target)
	b := &jsonObjectBuilder{}
	if nameSet {
		b.str("name", name)
	}
	if colorSet {
		b.str("color", color)
	}
	if descriptionSet {
		b.str("description", description)
	}
	if exclusiveSet {
		b.bool("exclusive", exclusive)
	}
	if archivedSet {
		b.bool("is_archived", archived)
	}
	label := mustJSON(api("PATCH", "/repos/"+repoOpt+"/labels/"+id, b.bytes()))
	fmt.Fprintf(stdout, "Label updated: %s %s\n", jqString(jsonField(label, "id")), jqString(jsonField(label, "name")))
}

// labelDelete mirrors label_delete (forge line 1684). --yes is a hard
// precondition, not a confirmation prompt: without it the command refuses
// outright (die, exit 1) before requireRepo or any network call, and stdin is
// never read.
func labelDelete(args []string) {
	if containsHelpFlag(args) {
		commandUsage("label delete")
		return
	}
	target := ""
	targetSet := false
	yes := false
	for len(args) > 0 {
		switch {
		case args[0] == "-R" || args[0] == "--repo":
			setRepoOption(args)
			args = args[2:]
		case args[0] == "--yes":
			yes = true
			args = args[1:]
		case strings.HasPrefix(args[0], "-"):
			die("unknown option for label delete: %s", args[0])
		default:
			if targetSet {
				die("unexpected argument for label delete: %s", args[0])
			}
			target = args[0]
			targetSet = true
			args = args[1:]
		}
	}
	if !targetSet || target == "" {
		die("usage: forge label delete <name> [--yes]")
	}
	if !yes {
		die("label delete requires --yes")
	}
	requireRepo()
	id := resolveLabelID(target)
	api("DELETE", "/repos/"+repoOpt+"/labels/"+id, nil)
	fmt.Fprintf(stdout, "Label deleted: %s\n", target)
}

// --- Shared rendering helpers (used by label.go and milestone.go) ---

// jsonObjectBuilder assembles a JSON object literal the way bash builds
// mutation payloads with `jq -n --arg/--argjson '{...} | if $set then . +
// {...} else . end'`: a key is included only when the corresponding call is
// made, key order never matters, and the type (string vs bool) is chosen by
// which method is called, mirroring --arg vs --argjson.
type jsonObjectBuilder struct {
	fields []string
}

// str adds a string-valued field, jq --arg style.
func (b *jsonObjectBuilder) str(key, value string) {
	b.fields = append(b.fields, jsonString(key)+":"+jsonString(value))
}

// bool adds a boolean-valued field, jq --argjson style.
func (b *jsonObjectBuilder) bool(key string, value bool) {
	lit := "false"
	if value {
		lit = "true"
	}
	b.fields = append(b.fields, jsonString(key)+":"+lit)
}

// bytes renders the accumulated fields as a JSON object, ready for api().
func (b *jsonObjectBuilder) bytes() []byte {
	return []byte("{" + strings.Join(b.fields, ",") + "}")
}

// asciiDowncase mirrors jq's ascii_downcase builtin, used by label list's
// search filter (forge line 1488): only A-Z is folded to a-z, so a non-ASCII
// letter (or a UTF-8 continuation byte, which is never in that range) passes
// through untouched. strings.ToLower is not a substitute: it case-folds all
// of Unicode, which jq's ascii_downcase deliberately does not.
func asciiDowncase(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// firstNRunes mirrors jq's `.[0:n]` string slice: the first n Unicode code
// points, or the whole string when it is shorter, with no error either way.
// Used for the due_on truncation in milestone list (forge line 1757) and
// milestone view (forge line 1773).
func firstNRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// numberOrZero mirrors `. // 0` applied to a numeric field: the decoded
// number, or 0 when the field is absent, null, or false (or, in principle,
// some non-numeric JSON value -- not a shape a real label ID takes). Used to
// sort labels by ID when --sort is not "name" (forge line 1490).
func numberOrZero(v any) float64 {
	if n, ok := v.(json.Number); ok {
		if f, err := n.Float64(); err == nil {
			return f
		}
	}
	return 0
}
