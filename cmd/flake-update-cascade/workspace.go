package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
)

// The functions here are lib/workspace.sh's workspace_collect_repos and
// workspace_repo_default_branch, which the Bash workspace tools still use, so
// every tool agrees on which repositories the workspace holds and in what
// order; repoForgeName is the cascade's own.

// isRepoRoot is workspace_is_repo_root: a .git entry exists and git agrees
// the directory is its own top level.
func isRepoRoot(dir string) bool {
	if _, err := os.Stat(dir + "/.git"); err != nil {
		return false
	}
	top, ok := gitCapture(dir, "rev-parse", "--show-toplevel")
	return ok && top == dir
}

// collectRepos walks workDir the way the Bash glob loop does: within each
// directory the plain entries come first, then those matching `.[!.]*`, then
// `..?*`, each group in byte order. A `.git` entry is never entered and a
// dotted entry is entered only when it is itself a repository root — checked
// once in the loop and again on entry, as the recursion does. Symlinks to
// directories count as directories, as `*/` matches them.
func collectRepos(workDir string) []string {
	workDir = strings.TrimSuffix(workDir, "/")
	var repos []string
	var visit func(dir string)
	visit = func(dir string) {
		dir = strings.TrimSuffix(dir, "/")
		if isRepoRoot(dir) {
			repos = append(repos, strings.TrimPrefix(dir, workDir+"/"))
			return
		}
		for _, name := range globDirs(dir) {
			if name == ".git" {
				continue
			}
			sub := dir + "/" + name
			if strings.HasPrefix(name, ".") && !isRepoRoot(sub) {
				continue
			}
			visit(sub)
		}
	}
	visit(workDir)
	return repos
}

func globDirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var plain, dotted, doubleDotted []string
	for _, entry := range entries {
		name := entry.Name()
		if info, err := os.Stat(dir + "/" + name); err != nil || !info.IsDir() {
			continue
		}
		switch {
		case !strings.HasPrefix(name, "."):
			plain = append(plain, name)
		case strings.HasPrefix(name, "..") && len(name) >= 3:
			doubleDotted = append(doubleDotted, name)
		case len(name) >= 2 && name[1] != '.':
			dotted = append(dotted, name)
		}
	}
	sort.Strings(plain)
	sort.Strings(dotted)
	sort.Strings(doubleDotted)
	return append(append(plain, dotted...), doubleDotted...)
}

// defaultBranch is workspace_repo_default_branch: origin's HEAD with the
// remote prefix removed, or master when git cannot say.
func defaultBranch(dir string) string {
	ref, ok := gitCapture(dir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if !ok {
		return "master"
	}
	return strings.Replace(ref, "refs/remotes/origin/", "", 1)
}

var forgeNamePattern = regexp.MustCompile(`[^/:]+/[^/:]+$`)

// repoForgeName extracts owner/repo from the origin URL: the last two
// slash-separated components after a .git suffix is dropped.
func repoForgeName(dir string) (string, bool) {
	url, ok := gitCapture(dir, "remote", "get-url", "origin")
	if !ok {
		return "", false
	}
	url = strings.TrimSuffix(url, ".git")
	name := forgeNamePattern.FindString(url)
	return name, name != ""
}
