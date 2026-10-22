package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A path:-fetched copy of a linked worktree carries a .git file whose target
// does not exist in the copy; validDescription must not depend on that cwd.
func TestValidDescriptionDanglingWorktreeGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	git := filepath.Join(dir, ".git")
	abs, err := filepath.Abs(filepath.Join(dir, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(git, []byte("gitdir: "+abs+"/.git/worktrees/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if !validDescription("t284-worktree-parity") {
		t.Errorf("validDescription rejected a plain description under a dangling .git")
	}
	if validDescription("bad name") {
		t.Errorf("validDescription accepted a description with a space")
	}
}
