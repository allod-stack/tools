package main

import (
	"reflect"
	"testing"

	"forge.anarch.diy/allod/tools/internal/flakelock"
)

// Every way a workspace repository can be named — an ssh origin with user and
// port, an https pin, an scp-like remote, a .git suffix or not, a query —
// reduces to one identity; a local path keeps its path; forms the function
// cannot read match nothing.
func TestRepoIdentity(t *testing.T) {
	same := []string{
		"ssh://git@forge.anarch.diy:2222/allod/vm.git",
		"https://forge.anarch.diy/allod/vm.git",
		"https://forge.anarch.diy/allod/vm",
		"https://Forge.Anarch.diy/Allod/VM/",
		"git@forge.anarch.diy:allod/vm.git",
		"https://forge.anarch.diy/allod/vm?x=1",
		" https://forge.anarch.diy/allod/vm.git\n",
	}
	for _, url := range same {
		got, ok := repoIdentity(url)
		if !ok || got != "forge.anarch.diy/allod/vm" {
			t.Errorf("repoIdentity(%q) = %q, %v", url, got, ok)
		}
	}
	cases := map[string]string{
		"https://github.com/acme/demo":       "github.com/acme/demo",
		"ssh://git@[::1]:2222/acme/demo.git": "[::1]/acme/demo",
		"file:///srv/git/leaf.git":           "/srv/git/leaf",
		"/srv/git/leaf.git/":                 "/srv/git/leaf",
		"file:///srv/git/Leaf":               "/srv/git/Leaf",
	}
	for url, want := range cases {
		if got, ok := repoIdentity(url); !ok || got != want {
			t.Errorf("repoIdentity(%q) = %q, %v; want %q", url, got, ok, want)
		}
	}
	for _, url := range []string{"", "nonsense", "https://", "https://host/", "../leaf.git", "host/path"} {
		if got, ok := repoIdentity(url); ok {
			t.Errorf("repoIdentity(%q) = %q, want no identity", url, got)
		}
	}
	if a, _ := repoIdentity("https://forge.anarch.diy/allod/vm"); a == "" {
		t.Fatal("identity empty")
	}
	if a, _ := repoIdentity("https://forge.anarch.diy/allod/vm"); a == mustIdentity(t, "https://forge.anarch.diy/allod/nexus") {
		t.Error("distinct repositories share an identity")
	}
}

func mustIdentity(t *testing.T, url string) string {
	t.Helper()
	id, ok := repoIdentity(url)
	if !ok {
		t.Fatalf("repoIdentity(%q) failed", url)
	}
	return id
}

// The order follows the pins and, among repositories nothing orders, the
// given order; a pin of a repository outside the list orders nothing; a
// cycle is reported in pin order and described from each member's side.
func TestDependencyOrder(t *testing.T) {
	repos := []string{"archetypes", "deploy", "inventory", "nexus", "secrets", "tools", "vm"}
	deps := map[string][]string{
		"archetypes": {"inventory", "nexus", "secrets", "tools", "vm"},
		"deploy":     {"archetypes", "inventory", "secrets"},
		"secrets":    {"inventory"},
		"tools":      {"nixpkgs-clone-elsewhere"},
	}
	order, cycle := dependencyOrder(repos, deps)
	want := []string{"inventory", "nexus", "secrets", "tools", "vm", "archetypes", "deploy"}
	if cycle != nil || !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v (cycle %v), want %v", order, cycle, want)
	}

	// Nothing chains: the given order is the order.
	order, cycle = dependencyOrder(repos, nil)
	if cycle != nil || !reflect.DeepEqual(order, repos) {
		t.Errorf("unordered: %v (cycle %v)", order, cycle)
	}

	// Directory order puts top first; the pins still put it last.
	order, _ = dependencyOrder([]string{"1-top", "2-mid", "3-leaf"}, map[string][]string{"1-top": {"2-mid"}, "2-mid": {"3-leaf"}})
	if want := []string{"3-leaf", "2-mid", "1-top"}; !reflect.DeepEqual(order, want) {
		t.Errorf("reversed: %v, want %v", order, want)
	}

	// The walk into the cycle starts at a, which only leads to it.
	order, cycle = dependencyOrder([]string{"a", "b", "c", "d"}, map[string][]string{"a": {"d"}, "b": {"c"}, "c": {"d"}, "d": {"b"}})
	if order != nil || !reflect.DeepEqual(cycle, []string{"d", "b", "c"}) {
		t.Errorf("cycle: order %v, cycle %v", order, cycle)
	}
	if got := cycleMessage(cycle, "c"); got != "dependency cycle: c → d → b → c" {
		t.Errorf("cycleMessage = %q", got)
	}
	if got := cycleMessage([]string{"a"}, "a"); got != "dependency cycle: a → a" {
		t.Errorf("self cycle = %q", got)
	}
}

// Workspace pins are the root pins whose branch the tool can read and whose
// repository the workspace holds inside the push boundary; the update set is
// those plus the requested names, once each, sorted.
func TestWorkspacePinsAndUpdateSet(t *testing.T) {
	lock, err := flakelock.Parse([]byte(`{"nodes":{
	  "root": {"inputs": {"vm": "vm", "nexus": "nexus", "fixed": "fixed", "sub": "sub", "outside": "outside", "nixpkgs": "nixpkgs", "alias": ["vm"]}},
	  "vm": {"original": {"type": "git", "url": "https://forge.anarch.diy/allod/vm.git"}, "locked": {"rev": "` + rev('a') + `"}},
	  "nexus": {"original": {"type": "git", "url": "ssh://git@forge.anarch.diy:2222/allod/nexus.git", "ref": "main"}, "locked": {"rev": "` + rev('a') + `"}},
	  "fixed": {"original": {"type": "git", "url": "https://forge.anarch.diy/allod/inventory.git", "rev": "` + rev('a') + `"}, "locked": {"rev": "` + rev('a') + `"}},
	  "sub": {"original": {"type": "git", "url": "https://forge.anarch.diy/allod/secrets.git", "submodules": true}, "locked": {"rev": "` + rev('a') + `"}},
	  "outside": {"original": {"type": "git", "url": "https://forge.anarch.diy/allod/elsewhere.git"}, "locked": {"rev": "` + rev('a') + `"}},
	  "nixpkgs": {"original": {"type": "github", "owner": "NixOS", "repo": "nixpkgs", "ref": "nixos-unstable"}, "locked": {"rev": "` + rev('a') + `"}}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	c := &cascade{options: options{names: []string{"nixpkgs"}}, byIdentity: map[string][]string{
		"forge.anarch.diy/allod/vm":        {"allod/vm"},
		"forge.anarch.diy/allod/nexus":     {"allod/nexus"},
		"forge.anarch.diy/allod/inventory": {"allod/inventory"},
		"forge.anarch.diy/allod/secrets":   {"allod/secrets"},
	}}
	pins := c.workspacePins(lock)
	want := []pin{{"nexus", "allod/nexus"}, {"vm", "allod/vm"}}
	if !reflect.DeepEqual(pins, want) {
		t.Errorf("workspacePins = %v, want %v", pins, want)
	}
	if got := c.updateSet(lock); !reflect.DeepEqual(got, []string{"nexus", "nixpkgs", "vm"}) {
		t.Errorf("updateSet = %v", got)
	}
	c.names = []string{"vm"}
	if got := c.updateSet(lock); !reflect.DeepEqual(got, []string{"nexus", "vm"}) {
		t.Errorf("updateSet with a pinned name = %v", got)
	}
}

// Dropping a repository's heads leaves every other cached head alone and
// matches the pin's URL to the origin's identity, not its spelling.
func TestForgetHeads(t *testing.T) {
	c := &cascade{heads: map[string]string{
		"https://forge.anarch.diy/allod/vm.git\tHEAD":    rev('a'),
		"https://forge.anarch.diy/allod/vm.git\tmain":    rev('b'),
		"https://forge.anarch.diy/allod/nexus.git\tHEAD": rev('c'),
		"https://github.com/acme/demo\tmain":             "",
	}}
	c.forgetHeads(mustIdentity(t, "ssh://git@forge.anarch.diy:2222/allod/vm.git"))
	want := map[string]string{
		"https://forge.anarch.diy/allod/nexus.git\tHEAD": rev('c'),
		"https://github.com/acme/demo\tmain":             "",
	}
	if !reflect.DeepEqual(c.heads, want) {
		t.Errorf("heads = %v, want %v", c.heads, want)
	}
	c.forgetHeads("")
	if !reflect.DeepEqual(c.heads, want) {
		t.Error("an empty identity dropped heads")
	}
}
