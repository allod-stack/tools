package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLedgerWriteFailureReleasesClaim(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	original := writeChangeLedgerEntry
	writeChangeLedgerEntry = func(file *os.File, data string) error {
		_, _ = file.WriteString(data[:1])
		return errors.New("sabotaged partial write")
	}
	t.Cleanup(func() { writeChangeLedgerEntry = original })
	entry := changeLedgerEntry{path: "/test/clone", branch: "agent/test"}
	if err := claimChangeLedgerEntry(repo, "test", entry); err == nil {
		t.Fatal("partial write unexpectedly succeeded")
	}
	dir, ok := changeLedgerDir(repo)
	if !ok {
		t.Fatal("cannot find ledger")
	}
	if _, err := os.Lstat(filepath.Join(dir, "test")); !os.IsNotExist(err) {
		t.Fatalf("failed claim remains: %v", err)
	}
	writeChangeLedgerEntry = original
	if err := claimChangeLedgerEntry(repo, "test", entry); err != nil {
		t.Fatalf("same description could not be claimed after failure: %v", err)
	}
}
