package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// port is the raw JSON text of preview_port; "" omits the field.
type siteListFixtureEntry struct {
	checkout, port string
}

func writeSiteListRegistry(t *testing.T, dir string, entries map[string]siteListFixtureEntry) {
	t.Helper()
	scripts := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scripts, 0755); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	body.WriteString(`{"repositories": {`)
	first := true
	for id, entry := range entries {
		if !first {
			body.WriteString(", ")
		}
		first = false
		fmt.Fprintf(&body, "%q: {\"checkout\": %q", id, entry.checkout)
		if entry.port != "" {
			fmt.Fprintf(&body, `, "preview_port": %s`, entry.port)
		}
		body.WriteString("}")
	}
	body.WriteString("}}")
	if err := os.WriteFile(filepath.Join(scripts, "repositories.json"), []byte(body.String()), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INVENTORY", dir)
}

func threeSiteFixture(t *testing.T) {
	t.Helper()
	writeSiteListRegistry(t, t.TempDir(), map[string]siteListFixtureEntry{
		"allod/blog": {checkout: "sites/blog", port: "18601"},
		"allod/docs": {checkout: "sites/docs", port: "18602"},
		"allod/wiki": {checkout: "sites/wiki"}, // no preview_port: never listed
	})
}

func TestSiteListEntriesSortedWithPreviewPortOnly(t *testing.T) {
	threeSiteFixture(t)
	entries := siteListEntries(false)
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2: %v", len(entries), entries)
	}
	if entries[0].id != "allod/blog" || entries[0].port != 18601 || !entries[0].portOK {
		t.Errorf("entries[0] = %+v, want allod/blog 18601 ok", entries[0])
	}
	if entries[1].id != "allod/docs" || entries[1].port != 18602 || !entries[1].portOK {
		t.Errorf("entries[1] = %+v, want allod/docs 18602 ok", entries[1])
	}
}

func TestSiteListEntriesMalformedPortIsShownNotHidden(t *testing.T) {
	writeSiteListRegistry(t, t.TempDir(), map[string]siteListFixtureEntry{
		"allod/blog": {checkout: "sites/blog", port: "18601"},
		"allod/bad":  {checkout: "sites/bad", port: `"not-a-number"`},
	})
	entries := siteListEntries(false)
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2: %v", len(entries), entries)
	}
	if entries[0].id != "allod/bad" || entries[0].portOK {
		t.Errorf("entries[0] = %+v, want allod/bad marked unusable", entries[0])
	}
	if entries[1].id != "allod/blog" || !entries[1].portOK {
		t.Errorf("entries[1] = %+v, want allod/blog ok", entries[1])
	}
}

func TestSiteListEntriesEmptyRegistryYieldsNone(t *testing.T) {
	writeSiteListRegistry(t, t.TempDir(), map[string]siteListFixtureEntry{
		"allod/wiki": {checkout: "sites/wiki"}, // no preview_port
	})
	if entries := siteListEntries(false); len(entries) != 0 {
		t.Errorf("entries = %v, want none", entries)
	}
}

func TestSiteListEntriesIncludesMachinesWhenAsked(t *testing.T) {
	dir := t.TempDir()
	writeSiteListRegistry(t, dir, map[string]siteListFixtureEntry{
		"allod/blog": {checkout: "sites/blog", port: "18601"},
	})
	previewWrite(t, filepath.Join(dir, "scripts", "vm-specs.json"),
		`{"vm-two": {"repos": ["allod/blog"]}, "vm-one": {"repos": ["allod/blog"]}}`)

	entries := siteListEntries(true)
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1: %v", len(entries), entries)
	}
	if got := strings.Join(entries[0].machines, " "); got != "vm-one vm-two" {
		t.Errorf("machines = %q, want sorted %q", got, "vm-one vm-two")
	}
}

func TestSiteListEntriesNoMachinesWhenNotAsked(t *testing.T) {
	writeSiteListRegistry(t, t.TempDir(), map[string]siteListFixtureEntry{
		"allod/blog": {checkout: "sites/blog", port: "18601"},
	})
	entries := siteListEntries(false)
	if len(entries) != 1 || entries[0].machines != nil {
		t.Errorf("entries = %+v, want one entry with no machines", entries)
	}
}

func TestSiteListTextEmptyRegistryNamesTheFile(t *testing.T) {
	writeSiteListRegistry(t, t.TempDir(), map[string]siteListFixtureEntry{
		"allod/wiki": {checkout: "sites/wiki"},
	})
	text := siteListText(siteListEntries(false))
	want := fmt.Sprintf("no entry in %s carries a preview_port\n", registryPath())
	if text != want {
		t.Errorf("siteListText = %q, want %q", text, want)
	}
}

func TestSiteListTextOneLinePerEntrySorted(t *testing.T) {
	threeSiteFixture(t)
	text := siteListText(siteListEntries(false))
	blog, docs := strings.Index(text, "allod/blog"), strings.Index(text, "allod/docs")
	if blog < 0 || docs < 0 || blog > docs {
		t.Errorf("siteListText did not list allod/blog before allod/docs:\n%s", text)
	}
	if strings.Contains(text, "allod/wiki") {
		t.Errorf("siteListText named allod/wiki, which carries no preview_port:\n%s", text)
	}
	for _, want := range []string{"18601", "18602"} {
		if !strings.Contains(text, want) {
			t.Errorf("siteListText does not contain %q:\n%s", want, text)
		}
	}
}

func TestEditDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "abd", 1},  // one substitution
		{"abc", "ab", 1},   // one deletion
		{"abc", "abcd", 1}, // one insertion
		{"kitten", "sitting", 3},
		{"ab", "xy", 2},   // no character in common, equal length
		{"abc", "xyz", 3}, // no character in common, equal length
	}
	for _, test := range tests {
		if got := editDistance(test.a, test.b); got != test.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
		}
		if got := editDistance(test.b, test.a); got != test.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d (symmetry)", test.b, test.a, got, test.want)
		}
	}
}

// Strings sharing no character, so containment never decides.
func TestSiteListClosestEditDistanceBoundary(t *testing.T) {
	within := []siteListEntry{{id: "ab"}}
	if got, ok := siteListClosest(within, "xy"); !ok || got != "ab" {
		t.Errorf(`siteListClosest(%v, "xy") = %q, %v, want "ab", true (distance %d = threshold)`,
			within, got, ok, siteListEditDistance)
	}
	beyond := []siteListEntry{{id: "abc"}}
	if got, ok := siteListClosest(beyond, "xyz"); ok {
		t.Errorf(`siteListClosest(%v, "xyz") = %q, %v, want "", false (distance %d > threshold)`,
			beyond, got, ok, siteListEditDistance+1)
	}
}

func TestSiteListClosestContainmentOutranksDistance(t *testing.T) {
	entries := []siteListEntry{{id: "allod/blog"}, {id: "allod/docs"}, {id: "allod/xl"}}
	if got, ok := siteListClosest(entries, "allod/bl"); !ok || got != "allod/blog" {
		t.Errorf(`siteListClosest(..., "allod/bl") = %q, %v, want "allod/blog", true`, got, ok)
	}
}

func TestSiteListClosestContainmentAmbiguousNamesNone(t *testing.T) {
	entries := []siteListEntry{{id: "allod/blog"}, {id: "allod/docs"}}
	if got, ok := siteListClosest(entries, "allod/"); ok {
		t.Errorf(`siteListClosest(..., "allod/") = %q, %v, want "", false`, got, ok)
	}
}

func TestSiteListClosestDistanceAmbiguousNamesNone(t *testing.T) {
	entries := []siteListEntry{{id: "ab"}, {id: "ac"}}
	if got, ok := siteListClosest(entries, "zz"); ok {
		t.Errorf(`siteListClosest(..., "zz") = %q, %v, want "", false`, got, ok)
	}
}

func TestSiteListNoIDListsKnownSites(t *testing.T) {
	tests := []struct {
		name string
		args []string
		says string
	}{
		{"view", []string{"site", "view"}, "site view needs a site id"},
		{"serve --vm", []string{"site", "serve", "--vm", "vm-one"}, "--vm needs a site id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			threeSiteFixture(t)
			_, errText, code := runAllod(t, test.args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			for _, want := range []string{test.says, "Known sites:", "allod/blog", "18601", "allod/docs", "18602"} {
				if !strings.Contains(errText, want) {
					t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
				}
			}
			if strings.Contains(errText, "allod/wiki") {
				t.Errorf("stderr named allod/wiki, which carries no preview_port\ngot: %q", errText)
			}
			if want := "Usage:\n"; !strings.Contains(errText, want) {
				t.Errorf("stderr does not still contain the usual usage block\ngot: %q", errText)
			}
		})
	}
}

func TestSiteListUnknownIDListsKnownSites(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"view", []string{"site", "view", "allod/absent"}},
		{"serve", []string{"site", "serve", "allod/absent"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			threeSiteFixture(t)
			_, errText, code := runAllod(t, test.args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			for _, want := range []string{"unknown site: allod/absent has no entry in", "Known sites:",
				"allod/blog", "18601", "allod/docs", "18602"} {
				if !strings.Contains(errText, want) {
					t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
				}
			}
		})
	}
}

func TestSiteListUnknownIDNamesTheOneCloseMatch(t *testing.T) {
	threeSiteFixture(t)
	_, errText, code := runAllod(t, "site", "view", "allod/bl")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "closest known id: allod/blog"; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
	}
}

func TestSiteListUnknownIDAmbiguousNamesNone(t *testing.T) {
	threeSiteFixture(t)
	_, errText, code := runAllod(t, "site", "view", "allod/")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if strings.Contains(errText, "closest known id") {
		t.Errorf("stderr named a closest id although two known ids match\ngot: %q", errText)
	}
	if !strings.Contains(errText, "Known sites:") {
		t.Errorf("stderr does not contain the list\ngot: %q", errText)
	}
}

// example/bad is within two edits of both prefixes.
func TestSiteListClosestSharedOwnerPrefix(t *testing.T) {
	writeSiteListRegistry(t, t.TempDir(), map[string]siteListFixtureEntry{
		"example/blog": {checkout: "sites/blog", port: "18601"},
		"example/docs": {checkout: "sites/docs", port: "18602"},
		"example/wiki": {checkout: "sites/wiki"}, // no preview_port
		"example/bad":  {checkout: "sites/bad", port: `"80"`},
	})
	tests := []struct {
		given string
		want  string
	}{
		{"example/bl", "example/blog"},
		{"example/d", "example/docs"},
	}
	for _, test := range tests {
		t.Run(test.given, func(t *testing.T) {
			_, errText, code := runAllod(t, "site", "view", test.given)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if want := "closest known id: " + test.want; !strings.Contains(errText, want) {
				t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
			}
		})
	}

	t.Run("example", func(t *testing.T) {
		_, errText, code := runAllod(t, "site", "view", "example")
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if strings.Contains(errText, "closest known id") {
			t.Errorf("stderr named a closest id although three known ids contain the given text\ngot: %q", errText)
		}
	})
}

func TestSiteListEmptyRegistryStatesFileAndExitsOne(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"view, no id", []string{"site", "view"}},
		{"view, unknown id", []string{"site", "view", "allod/absent"}},
		{"serve --vm, no id", []string{"site", "serve", "--vm", "vm-one"}},
		{"serve, unknown id", []string{"site", "serve", "allod/absent"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSiteListRegistry(t, dir, map[string]siteListFixtureEntry{
				"allod/wiki": {checkout: "sites/wiki"}, // no preview_port
			})
			_, errText, code := runAllod(t, test.args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			want := fmt.Sprintf("no entry in %s carries a preview_port", registryPath())
			if !strings.Contains(errText, want) {
				t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
			}
			if strings.Contains(errText, "Known sites:") {
				t.Errorf("stderr printed a list header with nothing to list\ngot: %q", errText)
			}
		})
	}
}

func TestSiteListSurvivesAMalformedPreviewPortElsewhere(t *testing.T) {
	writeSiteListRegistry(t, t.TempDir(), map[string]siteListFixtureEntry{
		"allod/blog": {checkout: "sites/blog", port: "18601"},
		"allod/bad":  {checkout: "sites/bad", port: `"not-a-number"`},
	})
	_, errText, code := runAllod(t, "site", "view")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{"allod/blog", "18601", "allod/bad", "preview_port is not usable"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
		}
	}
}

func TestSiteListHelpExplainsTheID(t *testing.T) {
	dir := t.TempDir()
	writeSiteListRegistry(t, dir, map[string]siteListFixtureEntry{})
	want := fmt.Sprintf("the entry's own key under \"repositories\" in %s", registryPath())
	for _, args := range [][]string{{"site", "view", "--help"}, {"site", "serve", "--help"}} {
		out, errText, code := runAllod(t, args...)
		if code != 0 || errText != "" {
			t.Fatalf("%v: exit=%d stderr=%q, want success with empty stderr", args, code, errText)
		}
		if !strings.Contains(out, want) {
			t.Errorf("%v: help does not contain %q\ngot: %q", args, want, out)
		}
	}
}
