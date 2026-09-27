package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// workspaceTestGit runs one git command against dir for this file's own
// fixtures, carrying a fixed identity so a commit needs no repo config. It is
// deliberately self-contained rather than reusing secret_test.go's gitRun,
// which sits behind the 'secret' build tag and so is not always in the
// binary this test is part of.
func workspaceTestGit(t *testing.T, dir string, args ...string) {
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

// TestDefaultRemoteBranchFailsWithoutGuessing is defaultRemoteBranch's
// witness for allod/tools#126: a repo whose origin/HEAD does not resolve
// must fail, not guess 'master'. The fixture reaches that state the way the
// issue documents it — `git remote set-head origin -d` with no fetch since —
// rather than by omitting the usual clone/fetch that sets origin/HEAD.
func TestDefaultRemoteBranchFailsWithoutGuessing(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "repo")

	workspaceTestGit(t, root, "init", "-q", "--bare", "-b", "main", origin)
	workspaceTestGit(t, root, "init", "-q", "-b", "main", repo)
	workspaceTestGit(t, repo, "commit", "-q", "--allow-empty", "-m", "initial")
	workspaceTestGit(t, repo, "remote", "add", "origin", origin)
	workspaceTestGit(t, repo, "push", "-q", "-u", "origin", "main")
	workspaceTestGit(t, repo, "remote", "set-head", "origin", "main")

	if branch, ok := defaultRemoteBranch(repo); !ok || branch != "main" {
		t.Fatalf("defaultRemoteBranch with origin/HEAD set = (%q, %v), want (\"main\", true)", branch, ok)
	}

	workspaceTestGit(t, repo, "remote", "set-head", "origin", "-d")

	branch, ok := defaultRemoteBranch(repo)
	if ok {
		t.Fatalf("defaultRemoteBranch with origin/HEAD unset = (%q, true), want failure", branch)
	}
	if branch != "" {
		t.Fatalf("defaultRemoteBranch on failure returned %q, want empty", branch)
	}
}
