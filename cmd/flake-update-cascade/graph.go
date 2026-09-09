package main

import (
	"sort"
	"strings"

	"forge.anarch.diy/allod/tools/internal/flakelock"
)

// A pin is a root input of one workspace repository locked to a branch of
// another. It is the edge the dependency order is built from and the pin the
// run propagates: a repository is processed after every repository it pins,
// and a pin that is behind the head of the branch it names is moved whether or
// not its input was on the command line.
type pin struct {
	input  string // the input's name, which is also its update path
	target string // the workspace repository it pins
	ref    string // the branch it names, as the lock declares it
	// onDefault says the branch is the target's default branch, the only
	// one a commit this run makes can land on: a pin of a release branch
	// or a tag is ordered on and read like any other but never waits for,
	// or is promised, a commit from this run.
	onDefault bool
}

// repoIdentity reduces a repository URL to what identifies the repository —
// host and path, without scheme, user, port, query, or a .git suffix — so an
// ssh://git@host:2222/owner/repo.git origin and an https://host/owner/repo pin
// name the same repository. Host and path are lowercased, as forges compare
// them; a local path is kept as given. It reports false for a form it cannot
// read, which then matches nothing.
func repoIdentity(url string) (string, bool) {
	url = strings.TrimSpace(url)
	var host, path string
	switch {
	case strings.Contains(url, "://"):
		scheme, rest, _ := strings.Cut(url, "://")
		if scheme == "file" {
			return cleanPath(rest)
		}
		host, path, _ = strings.Cut(rest, "/")
	case strings.HasPrefix(url, "/"):
		return cleanPath(url)
	default:
		// scp-like: [user@]host:path, where the host part holds no slash.
		var ok bool
		host, path, ok = strings.Cut(url, ":")
		if !ok || strings.Contains(host, "/") {
			return "", false
		}
	}
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if colon := strings.LastIndex(host, ":"); colon >= 0 && !strings.Contains(host[colon:], "]") {
		host = host[:colon]
	}
	path, _, _ = strings.Cut(path, "?")
	path, _, _ = strings.Cut(path, "#")
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || path == "" {
		return "", false
	}
	return strings.ToLower(host + "/" + path), true
}

func cleanPath(path string) (string, bool) {
	path, _, _ = strings.Cut(path, "?")
	path = strings.TrimSuffix(strings.TrimRight(path, "/"), ".git")
	if !strings.HasPrefix(path, "/") {
		return "", false
	}
	return path, true
}

// workspacePins lists the root pins of a lock that name a workspace repository
// and that the tool can move on its own: a branch whose head it can read. A
// pin that names a revision, or that carries an attribute an override could
// not reproduce, is neither propagated nor ordered on, and a pin of a
// repository the workspace does not hold is external. The pins come in input
// name order.
func (c *cascade) workspacePins(lock *flakelock.Lock) []pin {
	var pins []pin
	for _, name := range lock.RootPins() {
		src := lock.Source(name)
		if src.Kind != flakelock.SourceBranch {
			continue
		}
		id, ok := repoIdentity(src.URL)
		if !ok {
			continue
		}
		for _, target := range c.byIdentity[id] {
			branch := c.defaultBranchOf(target)
			pins = append(pins, pin{
				input:     name,
				target:    target,
				ref:       src.Ref,
				onDefault: src.Ref == "HEAD" || src.Ref == branch || src.Ref == "refs/heads/"+branch,
			})
		}
	}
	return pins
}

// updateSet is what one repository's lock asks the run to look at: the paths
// the requested names resolve to through the lock graph, plus every workspace
// pin, which moves only if it turns out to be behind the branch it names.
// Sorted and deduplicated, so the plan, the report and the commit message
// share one order.
func (c *cascade) updateSet(lock *flakelock.Lock) []string {
	set := make(map[string]bool)
	for _, path := range lock.UpdatePaths(c.names) {
		set[path] = true
	}
	for _, p := range c.workspacePins(lock) {
		set[p.input] = true
	}
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// dependencyOrder sorts repos so that each one follows every repository it
// pins, keeping the given order among repositories nothing orders: at each
// step the earliest repository whose pins are all placed comes next. deps maps
// a repository to the repositories it pins. When the pins form a cycle no
// order exists: order is nil and cycle names the repositories on one cycle,
// each pinning the next and the last pinning the first.
func dependencyOrder(repos []string, deps map[string][]string) (order, cycle []string) {
	known := make(map[string]bool, len(repos))
	for _, repo := range repos {
		known[repo] = true
	}
	// pending holds, per repository, the pinned repositories not yet placed.
	pending := make(map[string]map[string]bool, len(repos))
	for _, repo := range repos {
		pending[repo] = make(map[string]bool)
		for _, dep := range deps[repo] {
			if known[dep] {
				pending[repo][dep] = true
			}
		}
	}
	placed := make(map[string]bool, len(repos))
	for len(order) < len(repos) {
		next := ""
		for _, repo := range repos {
			if !placed[repo] && len(pending[repo]) == 0 {
				next = repo
				break
			}
		}
		if next == "" {
			return nil, findCycle(repos, placed, deps)
		}
		order = append(order, next)
		placed[next] = true
		for _, repo := range repos {
			delete(pending[repo], next)
		}
	}
	return order, nil
}

// findCycle walks the pins among the unplaced repositories until one repeats.
// Every unplaced repository pins another unplaced one, so the walk cannot end
// anywhere else.
func findCycle(repos []string, placed map[string]bool, deps map[string][]string) []string {
	unplaced := make(map[string]bool)
	for _, repo := range repos {
		if !placed[repo] {
			unplaced[repo] = true
		}
	}
	var path []string
	at := make(map[string]int)
	var repo string
	for _, candidate := range repos {
		if unplaced[candidate] {
			repo = candidate
			break
		}
	}
	for {
		if start, seen := at[repo]; seen {
			return path[start:]
		}
		at[repo] = len(path)
		path = append(path, repo)
		for _, dep := range deps[repo] {
			if unplaced[dep] {
				repo = dep
				break
			}
		}
	}
}

// cycleMessage describes a cycle from one member's point of view, that member
// first: "a → b → a" reads as a pins b, which pins a.
func cycleMessage(cycle []string, member string) string {
	start := 0
	for i, repo := range cycle {
		if repo == member {
			start = i
			break
		}
	}
	rotated := append(append([]string(nil), cycle[start:]...), cycle[:start]...)
	return "dependency cycle: " + strings.Join(append(rotated, member), " → ")
}
