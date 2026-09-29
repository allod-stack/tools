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

// siteListEntry is one line of the known-sites listing.
type siteListEntry struct {
	id       string
	port     int
	portOK   bool
	machines []string
}

// siteListEntries lists every registry entry with a preview_port set. A
// malformed one is kept and marked unusable rather than hidden or, as
// previewPort in registry.go would, made to die: the list must survive one
// bad entry.
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

// line renders one entry padded so idWidth columns line up across entries.
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

// siteListText prints one line per entry, id and port columns aligned, or,
// with none, a one-line statement naming the registry file instead of an
// empty list.
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

// siteListEditDistance is how many single-character edits an id may be from
// the given text and still count as close enough to guess: enough to catch a
// typo, small enough that two genuinely different ids rarely both qualify.
const siteListEditDistance = 2

// siteListClosest names the one entry that plausibly is what was meant, in
// two tiers rather than one pool: if exactly one entry's id contains given,
// name it, and two or more containing it name none regardless of distance.
// Only when none contain it does an id within siteListEditDistance edits of
// given qualify, again naming one only when exactly one does.
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

// editDistance is the Levenshtein distance between a and b: single-character
// insertions, deletions, and substitutions, each costing 1.
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

// siteListMissingID reports a missing site id the way siteCommandUsageError
// reports any other argument mistake, with the known-sites list inserted
// before the Usage: block.
func siteListMissingID(command, message string, withMachines bool) {
	fmt.Fprintf(stderr, "allod: %s\n", message)
	fmt.Fprint(stderr, siteListText(siteListEntries(withMachines)))
	fmt.Fprint(stderr, siteCommandUsageLines(command))
	exit(1)
}

// siteListUnknown reports an unknown site id: the registry file, a closest
// match when exactly one qualifies, and the known-sites list.
func siteListUnknown(site string, withMachines bool) {
	entries := siteListEntries(withMachines)
	fmt.Fprintf(stderr, "allod: unknown site: %s has no entry in %s\n", site, registryPath())
	if closest, ok := siteListClosest(entries, site); ok {
		fmt.Fprintf(stderr, "allod: closest known id: %s\n", closest)
	}
	fmt.Fprint(stderr, siteListText(entries))
	exit(1)
}
