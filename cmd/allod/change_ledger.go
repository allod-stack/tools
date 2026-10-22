package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// key is the ledger filename (the description): how removeChangeLedgerEntry
// finds the entry again, never derived from path or branch at removal time.
type changeLedgerEntry struct {
	path, branch, key string
}

// changeLedgerDir: one file per clone under the parent's own .git, where a sandboxed
// worker cannot write, keyed by description so two concurrent 'begin -d' calls for
// one description race on one filename and the O_EXCL loser refuses.
func changeLedgerDir(repo string) (string, bool) {
	gitDir, ok := gitOutput(repo, "rev-parse", "--path-format=absolute", "--git-dir")
	if !ok {
		return "", false
	}
	return filepath.Join(gitDir, "allod-changes"), true
}

// claimChangeLedgerEntry is the atomic step of 'begin -d'; see changeLedgerDir.
var writeChangeLedgerEntry = func(file *os.File, data string) error {
	_, err := file.WriteString(data)
	return err
}

func claimChangeLedgerEntry(repo, key string, entry changeLedgerEntry) error {
	dir, ok := changeLedgerDir(repo)
	if !ok {
		return fmt.Errorf("could not resolve git dir for %s", repo)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, key), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	writeErr := writeChangeLedgerEntry(file, entry.path+"\n"+entry.branch+"\n")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		if err := os.Remove(filepath.Join(dir, key)); err != nil {
			return fmt.Errorf("ledger write failed and claim could not be removed: %v", err)
		}
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	return nil
}

func parseChangeLedgerEntry(data []byte) (changeLedgerEntry, bool) {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return changeLedgerEntry{}, false
	}
	return changeLedgerEntry{path: lines[0], branch: lines[1]}, true
}

// changeLedgerEntries lists every clone repo's ledger names, skipping a file that
// fails to parse rather than letting one bad entry hide the rest.
func changeLedgerEntries(repo string) []changeLedgerEntry {
	dir, ok := changeLedgerDir(repo)
	if !ok {
		return nil
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var entries []changeLedgerEntry
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			continue
		}
		if entry, ok := parseChangeLedgerEntry(data); ok {
			entry.key = file.Name()
			entries = append(entries, entry)
		}
	}
	return entries
}

func changeLedgerHasBranch(repo, branch string) bool {
	for _, entry := range changeLedgerEntries(repo) {
		if entry.branch == branch {
			return true
		}
	}
	return false
}

// removeChangeLedgerEntry removes the entry keyed by key (the description
// claimChangeLedgerEntry used, never a clone path or its basename).
func removeChangeLedgerEntry(repo, key string) error {
	dir, ok := changeLedgerDir(repo)
	if !ok {
		return fmt.Errorf("could not resolve git dir for %s", repo)
	}
	return os.Remove(filepath.Join(dir, key))
}

func ledgerFilePath(parent string, entry changeLedgerEntry) (string, bool) {
	dir, ok := changeLedgerDir(parent)
	if !ok {
		return "", false
	}
	return filepath.Join(dir, entry.key), true
}

type changeLedgerLock struct {
	file *os.File
}

func (l *changeLedgerLock) Release() {
	if l == nil || l.file == nil {
		return
	}
	syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	l.file.Close()
}

// Convert the preflight lock to a shared worker lock without a release gap.
func (l *changeLedgerLock) Downgrade() bool {
	if l == nil || l.file == nil {
		return false
	}
	return syscall.Flock(int(l.file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) == nil
}

func lockLedgerShared(path string) (lock *changeLedgerLock, ok bool) {
	file, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, false
	}
	return &changeLedgerLock{file: file}, true
}

// Outside commands refuse immediately if a worker still holds this ledger.
func lockLedgerExclusive(path string) (lock *changeLedgerLock, ok bool) {
	file, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, false
	}
	return &changeLedgerLock{file: file}, true
}

// physicalPath resolves symlinks the way git reports paths (protection.physicalDir):
// a $HOME behind a symlink must compare equal to git's spelling of the same directory.
func physicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

type ledgerHit struct {
	parent string
	entry  changeLedgerEntry
}

func ledgerIndex() map[string]ledgerHit {
	wd := workDir()
	index := make(map[string]ledgerHit)
	for _, name := range collectRepos(wd) {
		repo := filepath.Join(wd, filepath.FromSlash(name))
		for _, candidate := range changeLedgerEntries(repo) {
			index[physicalPath(candidate.path)] = ledgerHit{parent: repo, entry: candidate}
		}
	}
	return index
}

// resolveClone finds the repository whose ledger names clonePath.
func resolveClone(clonePath string) (parent string, entry changeLedgerEntry, ok bool) {
	hit, found := ledgerIndex()[physicalPath(clonePath)]
	if !found {
		return "", changeLedgerEntry{}, false
	}
	return hit.parent, hit.entry, true
}

// Resolve by parent-owned ledger before invoking Git against worker files.
func matchLedgerClone(explicitPath string) (repo, parentRepo string, entry changeLedgerEntry, resolvedStart string, ok bool) {
	index := ledgerIndex()
	start := explicitPath
	walkUp := explicitPath == ""
	if walkUp {
		start, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(start); err == nil {
		start = abs
	}
	target := physicalPath(start)
	for {
		if hit, found := index[target]; found {
			return hit.entry.path, hit.parent, hit.entry, start, true
		}
		if !walkUp {
			break
		}
		up := filepath.Dir(target)
		if up == target {
			break
		}
		target = up
	}
	return "", "", changeLedgerEntry{}, start, false
}

func resolveCloneOrRepo(explicitPath string) (repo, parentRepo string, entry changeLedgerEntry, isClone bool) {
	if repo, parentRepo, entry, _, ok := matchLedgerClone(explicitPath); ok {
		return repo, parentRepo, entry, true
	}
	return resolveGitRepo(explicitPath), "", changeLedgerEntry{}, false
}

func resolveLedgeredClone(explicitPath string) (repo, parentRepo string, entry changeLedgerEntry, resolvedStart string, ok bool) {
	return matchLedgerClone(explicitPath)
}
