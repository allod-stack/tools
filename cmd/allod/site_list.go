package main

import (
	"fmt"
	"slices"
	"strings"
)

const siteIDDetail = `A site id is the repository's key in the registry, not its domain or a
shortened name: the entry's own key under "repositories" in {{registryPath}}.
`

func init() {
	siteSharedDetails = append(siteSharedDetails, siteSharedDetail{
		intro:    "<site>, accepted by serve and view:",
		text:     siteIDDetail,
		commands: []string{"serve", "view"},
	})
}

type siteListEntry struct {
	id       string
	port     int
	portOK   bool
	machines []string
}

// Not previewPort: it dies on a malformed preview_port, and the list must not.
func siteListEntries(withMachines bool) []siteListEntry {
	all := registryEntries()
	ids := make([]string, 0, len(all))
	for id, entry := range all {
		text := strings.TrimSpace(string(entry.PreviewPort))
		if text == "" || text == "null" {
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	entries := make([]siteListEntry, 0, len(ids))
	for _, id := range ids {
		port, ok := previewPortValue(string(all[id].PreviewPort))
		listed := siteListEntry{id: id, port: port, portOK: ok}
		if withMachines {
			listed.machines = vmsWithRepo(id)
		}
		entries = append(entries, listed)
	}
	return entries
}

func (entry siteListEntry) line(idWidth int) string {
	port := "preview_port is not usable"
	if entry.portOK {
		port = fmt.Sprint(entry.port)
	}
	text := fmt.Sprintf("  %-*s  %s", idWidth, entry.id, port)
	if len(entry.machines) > 0 {
		text += "  (" + strings.Join(entry.machines, " ") + ")"
	}
	return text
}

func siteListText(entries []siteListEntry) string {
	if len(entries) == 0 {
		return fmt.Sprintf("no entry in %s carries a preview_port\n", registryPath())
	}
	width := 0
	for _, entry := range entries {
		width = max(width, len(entry.id))
	}
	var text strings.Builder
	text.WriteString("Known sites:\n")
	for _, entry := range entries {
		text.WriteString(entry.line(width))
		text.WriteString("\n")
	}
	return text.String()
}

const siteListEditDistance = 2

// An id containing given outranks edit distance, which decides only when no id
// contains it: ids in one registry share an owner prefix and sit within a few
// edits of each other. Two or more candidates in the deciding tier name none.
func siteListClosest(entries []siteListEntry, given string) (id string, ok bool) {
	found, count := "", 0
	for _, entry := range entries {
		if strings.Contains(entry.id, given) {
			found, count = entry.id, count+1
		}
	}
	if count > 0 {
		return found, count == 1
	}
	for _, entry := range entries {
		if editDistance(entry.id, given) <= siteListEditDistance {
			found, count = entry.id, count+1
		}
	}
	return found, count == 1
}

// Levenshtein.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	previous := make([]int, len(rb)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		current := make([]int, len(rb)+1)
		current[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			current[j] = min(current[j-1]+1, previous[j]+1, previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(rb)]
}

func siteListMissingID(command, message string, withMachines bool) {
	fmt.Fprintf(stderr, "allod: %s\n", message)
	fmt.Fprint(stderr, siteListText(siteListEntries(withMachines)))
	fmt.Fprint(stderr, siteCommandUsageLines(command))
	exit(1)
}

func siteListUnknown(site string, withMachines bool) {
	entries := siteListEntries(withMachines)
	fmt.Fprintf(stderr, "allod: unknown site: %s has no entry in %s\n", site, registryPath())
	if closest, ok := siteListClosest(entries, site); ok {
		fmt.Fprintf(stderr, "allod: closest known id: %s\n", closest)
	}
	fmt.Fprint(stderr, siteListText(entries))
	exit(1)
}
