package main

import (
	"reflect"
	"strings"
	"testing"

	"forge.anarch.diy/allod/tools/internal/flakelock"
)

func rev(c byte) string { return strings.Repeat(string(c), 40) }

func TestRefSpecs(t *testing.T) {
	cases := map[string][]string{
		"HEAD":            {"HEAD"},
		"refs/heads/main": {"refs/heads/main"},
		"refs/tags/v1":    {"refs/tags/v1"},
		"main":            {"refs/heads/main", "refs/tags/main"},
	}
	for ref, want := range cases {
		if got := refSpecs(ref); !reflect.DeepEqual(got, want) {
			t.Errorf("refSpecs(%q) = %v, want %v", ref, got, want)
		}
	}
}

// A branch wins over a tag of the same name; an annotated tag yields its
// peeled commit rather than the tag object; a lightweight tag is its commit.
func TestPickHeadPrefersBranchThenPeeledTag(t *testing.T) {
	specs := refSpecs("v1")
	both := rev('a') + "\trefs/heads/v1\n" + rev('b') + "\trefs/tags/v1\n" + rev('c') + "\trefs/tags/v1^{}\n"
	if got := pickHead(both, specs); got != rev('a') {
		t.Errorf("branch and tag: got %s", got)
	}
	annotated := rev('b') + "\trefs/tags/v1\n" + rev('c') + "\trefs/tags/v1^{}\n"
	if got := pickHead(annotated, specs); got != rev('c') {
		t.Errorf("annotated tag: got %s, want the peeled commit", got)
	}
	lightweight := rev('b') + "\trefs/tags/v1\n"
	if got := pickHead(lightweight, specs); got != rev('b') {
		t.Errorf("lightweight tag: got %s", got)
	}
	if got := pickHead("", specs); got != "" {
		t.Errorf("no refs: got %q", got)
	}
	if got := pickHead(rev('a')+"\tHEAD\n", refSpecs("HEAD")); got != rev('a') {
		t.Errorf("HEAD: got %s", got)
	}
}

// planLock asks about every readable branch once, pins the ones that moved,
// hands nix the ones it cannot read or could not reach, and leaves a fixed or
// current input alone.
func TestPlanLockSortsPathsByWhatMoved(t *testing.T) {
	lock, err := flakelock.Parse([]byte(`{"nodes":{
	  "root": {"inputs": {"current": "current", "moved": "moved", "fixed": "fixed", "dark": "dark", "plain": "plain"}},
	  "current": {"original": {"type": "github", "owner": "acme", "repo": "same"}, "locked": {"rev": "` + rev('a') + `"}},
	  "moved": {"original": {"type": "github", "owner": "acme", "repo": "demo", "ref": "main"}, "locked": {"rev": "` + rev('a') + `"}},
	  "fixed": {"original": {"type": "github", "owner": "acme", "repo": "demo", "rev": "` + rev('a') + `"}, "locked": {"rev": "` + rev('a') + `"}},
	  "dark": {"original": {"type": "git", "url": "ssh://forge.example/gone.git"}, "locked": {"rev": "` + rev('a') + `"}},
	  "plain": {"locked": {"rev": "` + rev('a') + `"}}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	heads := map[string]string{
		"https://github.com/acme/same\tHEAD": rev('a'),
		"https://github.com/acme/demo\tmain": rev('b'),
	}
	var asked []string
	resolve := func(url, ref string) (string, bool) {
		asked = append(asked, url+"\t"+ref)
		head, ok := heads[url+"\t"+ref]
		return head, ok
	}

	plan := planLock(lock, []string{"current", "moved", "fixed", "dark", "plain"}, resolve)
	want := lockPlan{
		paths:      []string{"moved", "dark", "plain"},
		overrides:  []string{"--override-input", "moved", "github:acme/demo/" + rev('b')},
		fallback:   []string{"dark", "plain"},
		unresolved: []unresolvedPath{{"dark", "HEAD", "ssh://forge.example/gone.git"}},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("plan = %+v, want %+v", plan, want)
	}
	wantAsked := []string{"https://github.com/acme/same\tHEAD", "https://github.com/acme/demo\tmain", "ssh://forge.example/gone.git\tHEAD"}
	if !reflect.DeepEqual(asked, wantAsked) {
		t.Errorf("asked %v, want %v", asked, wantAsked)
	}
	if plan.empty() {
		t.Error("a plan with work reads as empty")
	}
	if !planLock(lock, []string{"current", "fixed"}, resolve).empty() {
		t.Error("current and fixed inputs left work in the plan")
	}
}
