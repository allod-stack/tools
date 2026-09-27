package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cascadeTestGit runs one git command against dir for this file's fixtures,
// carrying a fixed identity so a commit needs no repo config.
func cascadeTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// TestDefaultBranchFailsWithoutGuessing is this package's own copy of
// workspace_repo_default_branch's contract (allod/tools#126): a repo whose
// origin/HEAD does not resolve must fail, not guess 'master'. The fixture
// reaches that state the way the issue documents it — `git remote set-head
// origin -d` with no fetch since.
func TestDefaultBranchFailsWithoutGuessing(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "repo")

	cascadeTestGit(t, root, "init", "-q", "--bare", "-b", "main", origin)
	cascadeTestGit(t, root, "init", "-q", "-b", "main", repo)
	cascadeTestGit(t, repo, "commit", "-q", "--allow-empty", "-m", "initial")
	cascadeTestGit(t, repo, "remote", "add", "origin", origin)
	cascadeTestGit(t, repo, "push", "-q", "-u", "origin", "main")
	cascadeTestGit(t, repo, "remote", "set-head", "origin", "main")

	if branch, ok := defaultBranch(repo); !ok || branch != "main" {
		t.Fatalf("defaultBranch with origin/HEAD set = (%q, %v), want (\"main\", true)", branch, ok)
	}

	cascadeTestGit(t, repo, "remote", "set-head", "origin", "-d")

	branch, ok := defaultBranch(repo)
	if ok {
		t.Fatalf("defaultBranch with origin/HEAD unset = (%q, true), want failure", branch)
	}
	if branch != "" {
		t.Fatalf("defaultBranch on failure returned %q, want empty", branch)
	}
}

// TestDefaultBranchOfRefusesRatherThanGuess is the same contract through the
// cascade's own cache, which is what preflight consults before deciding
// whether a repository is eligible: defaultBranchOf must report failure (and
// remember it, rather than re-asking git every time) instead of handing back
// a guessed branch name.
func TestDefaultBranchOfRefusesRatherThanGuess(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "repo")

	cascadeTestGit(t, root, "init", "-q", "--bare", "-b", "main", origin)
	cascadeTestGit(t, root, "init", "-q", "-b", "main", repo)
	cascadeTestGit(t, repo, "commit", "-q", "--allow-empty", "-m", "initial")
	cascadeTestGit(t, repo, "remote", "add", "origin", origin)
	cascadeTestGit(t, repo, "push", "-q", "-u", "origin", "main")

	c := &cascade{
		workDir:        root,
		defaults:       make(map[string]string),
		defaultsFailed: make(map[string]bool),
	}
	if branch, ok := c.defaultBranchOf("repo"); ok {
		t.Fatalf("defaultBranchOf with unset origin/HEAD = (%q, true), want failure", branch)
	}
	if !c.defaultsFailed["repo"] {
		t.Fatal("defaultBranchOf did not cache the failure")
	}
	if branch, ok := c.defaultBranchOf("repo"); ok || branch != "" {
		t.Fatalf("cached defaultBranchOf = (%q, %v), want (\"\", false)", branch, ok)
	}
}
