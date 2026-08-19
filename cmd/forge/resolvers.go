package main

// Label and milestone resolution, and the small jq render templates that go
// with them. Transliterated from forge lines 337-396.

import "strings"

// findLabelIDByName mirrors find_label_id_by_name (forge line 337): look the
// name up in the first 100 repository labels and refuse to guess between
// duplicates.
func findLabelIDByName(identifier string) string {
	labels := api("GET", "/repos/"+repoOpt+"/labels?limit=100", nil)

	items := jsonArray(mustJSON(labels))
	count := 0
	var first any
	for _, item := range items {
		if name, ok := jsonField(item, "name").(string); ok && name == identifier {
			count++
			if count == 1 {
				first = item
			}
		}
	}
	if count > 1 {
		die("label name is ambiguous: %s", identifier)
	}
	if count == 0 {
		return ""
	}
	// `.id // empty`: a null or false id yields nothing at all.
	id := jsonField(first, "id")
	if id == nil || id == false {
		return ""
	}
	// bash: id=$(find_label_id_by_name ...) — a command substitution, so a
	// string id ending in newlines arrives with them gone.
	return chompNewlines(jqString(id))
}

// resolveLabelID mirrors resolve_label_id (forge line 346). A digits-only
// identifier is taken as an ID without a lookup.
func resolveLabelID(identifier string) string {
	if isInteger(identifier) {
		return identifier
	}
	id := findLabelIDByName(identifier)
	if id == "" {
		die("label not found: %s", identifier)
	}
	return id
}

// resolveLabelIDsJSON mirrors resolve_label_ids_json (forge line 359): a JSON
// array of numeric IDs, since bash builds it with jq --argjson.
//
// --argjson is where a hand-written "-l 0012" loses its leading zeroes; the ID
// itself keeps them everywhere else, which is why the normalization lives here
// and not in resolveLabelID (label edit/delete put the raw text in the URL).
func resolveLabelIDsJSON(labels []string) string {
	ids := make([]string, 0, len(labels))
	for _, label := range labels {
		ids = append(ids, jqArgJSON(resolveLabelID(label)))
	}
	return "[" + strings.Join(ids, ",") + "]"
}

// resolveMilestoneID mirrors resolve_milestone_id (forge line 369). The title
// is sent as a server-side filter and matched exactly again here, because the
// filter is a substring match.
func resolveMilestoneID(identifier string) string {
	if isInteger(identifier) {
		return identifier
	}

	milestones := api("GET", "/repos/"+repoOpt+"/milestones?state=all&name="+jqURI(identifier)+"&limit=100", nil)

	items := jsonArray(mustJSON(milestones))
	count := 0
	var first any
	for _, item := range items {
		if title, ok := jsonField(item, "title").(string); ok && title == identifier {
			count++
			if count == 1 {
				first = item
			}
		}
	}
	if count == 0 {
		die("milestone not found: %s", identifier)
	}
	if count != 1 {
		die("milestone title is ambiguous: %s", identifier)
	}
	// bash: id=$(echo "$milestones" | jq -r '...') — a command substitution.
	return chompNewlines(jqString(jsonField(first, "id")))
}

// --- Render templates ---

// formatLabels mirrors format_labels (forge line 385): the names of a label
// array joined by commas, or "-" when there are none. v is a decoded value,
// what bash pipes in as raw JSON.
func formatLabels(v any) string {
	items := jsonArray(v)
	if len(items) == 0 {
		return "-"
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, jqJoinElement(jsonField(item, "name")))
	}
	return strings.Join(names, ",")
}

// formatIssueLabelNames mirrors format_issue_label_names (forge line 389): the
// same rendering applied to an issue's .labels, which may be missing.
func formatIssueLabelNames(v any) string {
	labels := jsonField(v, "labels")
	if labels == nil || labels == false {
		labels = []any{}
	}
	return formatLabels(labels)
}

// formatIssueMilestoneTitle mirrors format_issue_milestone_title (forge line
// 393): `.milestone.title // "-"`.
func formatIssueMilestoneTitle(v any) string {
	return jqAlt(jsonPath(v, "milestone", "title"), "-")
}

// jqJoinElement renders one array element the way jq's join/1 does: null
// contributes an empty string rather than the text "null".
func jqJoinElement(v any) string {
	if v == nil {
		return ""
	}
	return jqString(v)
}
