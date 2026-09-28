// Package protection resolves a checkout against
// ~/.config/git/protected-branches, the one rule every Go program that must
// not write to a protected branch reads that list by.
//
// It returns errors, never exits and never prints: the message and the exit
// code belong to the command. Like internal/gitremote it runs git itself.
package protection

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"forge.anarch.diy/allod/tools/internal/gitremote"
)

// ErrNotRepo reports that git would not answer for the directory, so the
// checkout cannot be resolved at all. A caller that renders its own message
// for an unreadable list tells the two apart with errors.Is.
var ErrNotRepo = errors.New("not a git repository")

func gitOutput(dir string, args ...string) (string, bool) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimRight(string(out), "\n"), true
}

func homeDir() string { return os.Getenv("HOME") }

func listPath() string {
	return filepath.Join(homeDir(), ".config", "git", "protected-branches")
}

// The common dir is the repository's own .git in the ordinary layout only: a
// submodule and --separate-git-dir put it elsewhere, so its parent is the answer
// for a linked worktree alone. Same rule as the hook's $main_repo.
func MainRepoDir(dir string) (string, error) {
	common, commonOK := gitOutput(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	gitDir, gitDirOK := gitOutput(dir, "rev-parse", "--path-format=absolute", "--git-dir")
	if !commonOK || !gitDirOK {
		return "", fmt.Errorf("%w: %s", ErrNotRepo, dir)
	}
	if gitDir != common {
		return filepath.Dir(common), nil
	}
	top, ok := gitOutput(dir, "rev-parse", "--show-toplevel")
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotRepo, dir)
	}
	return top, nil
}

// git reports resolved paths, so an unresolved $HOME would fail to prefix them
// and turn a correctly placed checkout into a misplaced one.
func physicalDir(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// RepoKey is the $HOME-relative path of the repository a checkout belongs to,
// and false for a checkout outside $HOME.
func RepoKey(dir string) (string, bool, error) {
	main, err := MainRepoDir(dir)
	if err != nil {
		return "", false, err
	}
	rel, relErr := filepath.Rel(physicalDir(homeDir()), main)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false, nil
	}
	return filepath.ToSlash(rel), true, nil
}

type branchListEntry struct{ path, branch string }

// bufio.Scanner default of 64 KiB ends the scan at a longer line, which with
// the scanner.Err check below would hide every entry behind it.
const branchListLineLimit = 1 << 20

func readBranchList(path string) ([]branchListEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	var entries []branchListEntry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, branchListLineLimit)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		// A trailing slash names the same directory, and leaving it on would
		// defeat both the path match and the remote suffix match.
		path := strings.TrimRight(fields[0], "/")
		if path == "" {
			continue
		}
		entries = append(entries, branchListEntry{path: path, branch: fields[1]})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return entries, nil
}

// identityPath is the $HOME-relative path of the repository a checkout belongs
// to — the main repository, never a linked worktree's own directory. A checkout
// outside $HOME gets its absolute path, which matches no entry: entries are
// $HOME-relative, so such a checkout can only ever be misplaced.
func identityPath(dir string) (string, error) {
	key, ok, err := RepoKey(dir)
	if err != nil {
		return "", err
	}
	if ok {
		return key, nil
	}
	return MainRepoDir(dir)
}

// originRepo is the owner/repo a checkout's origin names, or "" when there is no
// origin or its URL carries no such tail. Not patch.go's remoteIdentity, which
// keeps the host so that two remote URLs can be compared.
func originRepo(dir string) string {
	url, ok := gitOutput(dir, "remote", "get-url", "origin")
	if !ok {
		return ""
	}
	return gitremote.RepoFromURL(url)
}

// Status is how one checkout resolves against a branch list. A non-empty
// Expected means the list recognises the repository by its origin but places it
// somewhere else: the checkout is misplaced, and no rail may read that as
// "unprotected".
type Status struct {
	branches []string
	Expected string
	Actual   string
	Identity string
}

func (p Status) Misplaced() bool { return p.Expected != "" }

// Covers reports whether the list constrains this branch. A repository may have
// several entries, and every branch they name is listed, not only the first.
func (p Status) Covers(branch string) bool {
	for _, listed := range p.branches {
		if listed == branch {
			return true
		}
	}
	return false
}

// Start is the branch 'change begin' branches from: the first the list names.
func (p Status) Start() string {
	if len(p.branches) == 0 {
		return ""
	}
	return p.branches[0]
}

// remoteMatches reports whether an entry path names the repository identity:
// the whole path, or its final owner/repo components. A '/' is required before
// the identity so that work/xacme/widget does not answer for acme/widget, and
// the comparison ignores case because the forge does: origin Acme/Widget is the
// repository the entry work/acme/widget names. The path match stays exact.
func remoteMatches(entryPath, identity string) bool {
	entryPath, identity = strings.ToLower(entryPath), strings.ToLower(identity)
	return entryPath == identity || strings.HasSuffix(entryPath, "/"+identity)
}

// Lookup resolves a checkout against the protected-branches list. An absent
// list is no policy on this machine; a present unreadable one is never an
// absence of policy, and comes back as an error.
//
// Its bash twin is branch_listed in git-hooks/protected-refs-policy, and
// tests/fixtures/protection-cases.tsv is what keeps the two agreeing.
func Lookup(dir string) (Status, bool, error) {
	actual, err := identityPath(dir)
	if err != nil {
		return Status{}, false, err
	}
	entries, err := readBranchList(listPath())
	if err != nil {
		return Status{}, false, err
	}

	var branches []string
	for _, entry := range entries {
		if entry.path == actual {
			branches = append(branches, entry.branch)
		}
	}
	if len(branches) > 0 {
		return Status{branches: branches, Actual: actual}, true, nil
	}

	identity := originRepo(dir)
	if identity == "" {
		return Status{}, false, nil
	}
	expected := ""
	for _, entry := range entries {
		if !remoteMatches(entry.path, identity) {
			continue
		}
		if expected == "" {
			expected = entry.path
		}
		branches = append(branches, entry.branch)
	}
	if len(branches) == 0 {
		return Status{}, false, nil
	}
	return Status{branches: branches, Expected: expected, Actual: actual, Identity: identity}, true, nil
}
