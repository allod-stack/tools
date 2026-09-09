package flakelock

import (
	"reflect"
	"testing"
)

func mustParse(t *testing.T, text string) *Lock {
	t.Helper()
	lock, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return lock
}

// The fixture from tests/flake/flake-update-cascade-multiple-inputs.sh: a
// root-level follows for nixpkgs must resolve to the canonical vm/nixpkgs pin.
const multiInputLock = `{
  "nodes": {
    "root": {"inputs": {"allod-tools": "allod-tools", "vm": "vm", "nixpkgs": ["vm", "nixpkgs"]}},
    "allod-tools": {"locked": {"rev": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
    "vm": {"inputs": {"nixpkgs": "nixpkgs"}, "locked": {"rev": "cccccccccccccccccccccccccccccccccccccccc"}},
    "nixpkgs": {"locked": {"rev": "dddddddddddddddddddddddddddddddddddddddd"}}
  }
}`

func TestUpdatePathsResolvesRootFollowsToCanonicalPin(t *testing.T) {
	lock := mustParse(t, multiInputLock)
	got := lock.UpdatePaths([]string{"nixpkgs", "allod-tools"})
	want := []string{"allod-tools", "vm/nixpkgs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UpdatePaths = %v, want %v", got, want)
	}
}

func TestUpdatePathsIgnoresUnknownAndFollowsOnly(t *testing.T) {
	lock := mustParse(t, `{"nodes":{"root":{"inputs":{"demo":["base","demo"]}}}}`)
	if got := lock.UpdatePaths([]string{"demo"}); len(got) != 0 {
		t.Fatalf("a follows-only input matched: %v", got)
	}
	lock = mustParse(t, `{"nodes":{"root":{"inputs":{}}}}`)
	if got := lock.UpdatePaths([]string{"demo"}); len(got) != 0 {
		t.Fatalf("an absent input matched: %v", got)
	}
}

func TestUpdatePathsWalksDiamondsAndStopsAtCycles(t *testing.T) {
	lock := mustParse(t, `{"nodes":{
	  "root": {"inputs": {"a": "a", "b": "b"}},
	  "a": {"inputs": {"shared": "shared"}},
	  "b": {"inputs": {"shared": "shared", "loop": "root"}},
	  "shared": {"inputs": {"deep": "deep"}},
	  "deep": {}
	}}`)
	got := lock.UpdatePaths([]string{"deep"})
	want := []string{"a/shared/deep", "b/shared/deep"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UpdatePaths = %v, want %v", got, want)
	}
}

func TestRevResolvesPinsAndFollows(t *testing.T) {
	lock := mustParse(t, multiInputLock)
	cases := map[string]string{
		"allod-tools":     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"vm/nixpkgs":      "dddddddddddddddddddddddddddddddddddddddd",
		"nixpkgs":         "dddddddddddddddddddddddddddddddddddddddd",
		"missing":         "",
		"vm/missing":      "",
		"vm/nixpkgs/deep": "",
	}
	for path, want := range cases {
		if got := lock.Rev(path); got != want {
			t.Errorf("Rev(%q) = %q, want %q", path, got, want)
		}
	}
}

// The fixture from tests/flake/flake-update-cascade-follows.sh, post-update:
// a nested pin collapsed into a follows array must resolve through it.
func TestRevResolvesCollapsedFollows(t *testing.T) {
	lock := mustParse(t, `{"nodes":{
	  "root": {"inputs": {"archetypes": "archetypes"}},
	  "archetypes": {"inputs": {"nexus": "nexus", "vm": "vm"}, "locked": {"rev": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
	  "nexus": {"inputs": {"vm": ["archetypes", "vm"]}, "locked": {"rev": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
	  "vm": {"locked": {"rev": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}}
	}}`)
	if got := lock.Rev("archetypes/nexus/vm"); got != "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" {
		t.Fatalf("collapsed path resolved to %q", got)
	}
	if got := lock.Rev("archetypes/nexus/vm/x"); got != "" {
		t.Fatalf("path beyond a leaf resolved to %q", got)
	}
}

func TestRevToleratesMissingRevAndNodes(t *testing.T) {
	lock := mustParse(t, `{"nodes":{"root":{"inputs":{"a":"a","gone":"gone","weird":5}},"a":{"locked":{"rev":null}}}}`)
	for _, path := range []string{"a", "gone", "weird", "gone/x"} {
		if got := lock.Rev(path); got != "" {
			t.Errorf("Rev(%q) = %q, want empty", path, got)
		}
	}
	if got := lock.UpdatePaths([]string{"weird", "gone"}); !reflect.DeepEqual(got, []string{"gone"}) {
		t.Errorf("UpdatePaths = %v, want [gone]", got)
	}
}

func TestParseRejectsMalformedJSON(t *testing.T) {
	if _, err := Parse([]byte("{")); err == nil {
		t.Fatal("malformed lock parsed")
	}
}

// RootPins lists the inputs the flake pins itself — a follows is not a pin,
// and the walk never enters nested nodes.
func TestRootPins(t *testing.T) {
	lock := mustParse(t, multiInputLock)
	if got := lock.RootPins(); !reflect.DeepEqual(got, []string{"allod-tools", "vm"}) {
		t.Fatalf("RootPins = %v", got)
	}
	if got := mustParse(t, `{"nodes":{}}`).RootPins(); len(got) != 0 {
		t.Fatalf("RootPins of a lock with no root = %v", got)
	}
}
