package main

// The known-sites listing 'view' and 'serve --vm' print in place of the two
// messages that used to leave a reader with nothing but a file path: no site
// id given, and an id the registry does not have. Kept out of
// site_serve.go and site_view.go, which allod/tools#249 edits at the same
// time, so the two branches touch none of the same lines.

import (
	"fmt"
	"slices"
	"strings"
)

// siteIDDetail explains what a site id is, shared rather than duplicated
// between 'serve' and 'view' — the two commands that take one — the way
// '--config <path>' is shared by deploy, check, and config in site.go.
// {{registryPath}} is filled in at render time (site_common.go's
// renderSiteDetail): the path depends on $INVENTORY, which a real
// invocation resolves fresh and a test sets per run, so a fixed path baked
// in here would go stale.
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

// siteListEntry is one line of the known-sites listing: a registry id that
// carries a preview_port, with that port when it parsed, and the machines
// vm-specs.json lists it under when the caller asked for them.
type siteListEntry struct {
	id       string
	port     int
	portOK   bool
	machines []string
}

// siteListEntries returns, sorted by id, every registry entry whose
// preview_port field is set at all. previewPortValue's own "absent" case
// (empty or 'null') is not a site with a preview and is left out; a value
// that is set but does not parse is kept and marked unusable rather than
// hidden or, as previewPort in registry.go would, made to die — the list
// must survive one bad entry. withMachines asks vm-specs.json for the
// machines that list each site, which costs a file read; every caller here
// can afford it, since 'view' and 'serve --vm' already read that file on
// the same machine for the paths this replaces.
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

// line renders one entry as siteListText prints it.
func (entry siteListEntry) line() string {
	port := "preview_port is not usable"
	if entry.portOK {
		port = fmt.Sprint(entry.port)
	}
	text := "  " + entry.id + "  " + port
	if len(entry.machines) > 0 {
		text += "  (" + strings.Join(entry.machines, " ") + ")"
	}
	return text
}

// siteListText renders entries for standard error: one line each, or, when
// there are none, the one-line statement that names the registry file so the
// reader knows where a previewable entry would go.
func siteListText(entries []siteListEntry) string {
	if len(entries) == 0 {
		return fmt.Sprintf("no entry in %s carries a preview_port\n", registryPath())
	}
	var text strings.Builder
	text.WriteString("Known sites:\n")
	for _, entry := range entries {
		text.WriteString(entry.line())
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

// siteListMissingID reports "no site id" in the shape siteCommandUsageError
// reports any other argument mistake in the same command — the one-line
// message, then this command's own Usage: lines, then its '--help' pointer —
// with the known-sites list between the message and that tail, so a first
// run with no id becomes a lookup instead of a dead end at the registry
// file. Exit code and destination (standard error) are unchanged: 1, there.
func siteListMissingID(command, message string, withMachines bool) {
	fmt.Fprintf(stderr, "allod: %s\n", message)
	fmt.Fprint(stderr, siteListText(siteListEntries(withMachines)))
	fmt.Fprint(stderr, siteCommandUsageLines(command))
	exit(1)
}

// siteListUnknown reports "unknown site": the file that has no such entry,
// the one closest id when exactly one known id is close enough to name, and
// the same known-sites list a missing id gets. Exit code and destination are
// unchanged: 1, standard error.
func siteListUnknown(site string, withMachines bool) {
	entries := siteListEntries(withMachines)
	fmt.Fprintf(stderr, "allod: unknown site: %s has no entry in %s\n", site, registryPath())
	if closest, ok := siteListClosest(entries, site); ok {
		fmt.Fprintf(stderr, "allod: closest known id: %s\n", closest)
	}
	fmt.Fprint(stderr, siteListText(entries))
	exit(1)
}
