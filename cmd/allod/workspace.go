package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"forge.anarch.diy/allod/tools/internal/gitremote"
)

func isRepoRoot(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	top, ok := gitOutput(dir, "rev-parse", "--show-toplevel")
	return ok && top == dir
}

func resolveGitRepo(input string) string {
	if input == "" {
		input, _ = os.Getwd()
	}
	info, err := os.Stat(input)
	if err != nil || !info.IsDir() {
		die(1, "not a directory: %s", input)
	}
	top, ok := gitOutput(input, "rev-parse", "--show-toplevel")
	if !ok {
		die(1, "not a git repository: %s", input)
	}
	if !isRepoRoot(top) {
		die(1, "could not resolve git repository root for: %s", input)
	}
	return top
}

func resolvePatchDestination(input string) string {
	if input == "" {
		input, _ = os.Getwd()
	}
	info, err := os.Stat(input)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "allod: destination repository path is not a directory: %s\n", input)
		fmt.Fprintln(stderr, "allod: while resolving: <destination-repo>")
		fmt.Fprintln(stderr, "allod: to fix: pass an existing clone path for the repository that should receive the patches")
		exit(1)
	}
	top, ok := gitOutput(input, "rev-parse", "--show-toplevel")
	if !ok {
		fmt.Fprintf(stderr, "allod: destination path is not inside a git repository: %s\n", input)
		fmt.Fprintln(stderr, "allod: while resolving: <destination-repo>")
		fmt.Fprintln(stderr, "allod: to fix: clone or initialize the destination repository, then rerun with that path")
		exit(1)
	}
	if !isRepoRoot(top) {
		fmt.Fprintf(stderr, "allod: could not resolve destination repository root for: %s\n", input)
		fmt.Fprintf(stderr, "allod: resolved git top-level: %s\n", top)
		fmt.Fprintln(stderr, "allod: to fix: pass a path inside a normal git worktree that should receive the patches")
		exit(1)
	}
	return top
}

// isPatchRepoPath reports whether a patch repo argument must be read as a
// filesystem path rather than a repository-registry id: absolute, or
// beginning with ~, ., or .. — the rule stated in patchUsageText.
func isPatchRepoPath(value string) bool {
	return filepath.IsAbs(value) || strings.HasPrefix(value, "~") || strings.HasPrefix(value, ".")
}

// expandUserHome expands a leading ~ to $HOME for a literal tilde that
// reached the program unexpanded (an interactive shell would already have
// expanded it). Any other leading-~ form (~otheruser/...) is left alone and
// fails the directory check as it would today.
func expandUserHome(value string) string {
	if value == "~" {
		return homeDir()
	}
	if strings.HasPrefix(value, "~/") {
		return filepath.Join(homeDir(), strings.TrimPrefix(value, "~/"))
	}
	return value
}

// resolvePatchDestinationArg resolves a receive/apply destination argument,
// disambiguating a registry id from a path per patchUsageText's rule. A path
// form behaves exactly as resolvePatchDestination always has; a value that
// is neither a path form nor a registry id falls back to the existing
// relative-path handling, so an ordinary relative clone path still works.
func resolvePatchDestinationArg(input string) string {
	if input == "" || isPatchRepoPath(input) {
		return resolvePatchDestination(expandUserHome(input))
	}
	if checkout, ok := registryCheckout(input); ok {
		return resolvePatchDestination(filepath.Join(workDir(), checkout))
	}
	if info, err := os.Stat(input); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "allod: destination is neither a registry id nor a directory: %s\n", input)
		fmt.Fprintln(stderr, "allod: while resolving: <destination-repo>")
		fmt.Fprintln(stderr, "allod: to fix: pass a registry id from inventory/scripts/repositories.json or an existing clone path for the repository that should receive the patches")
		exit(1)
	}
	return resolvePatchDestination(input)
}

// resolvePatchSourceArg resolves a fetch/receive source repo argument on the
// LOCAL machine — the registry is the same tracked file on every machine,
// and vm-provisioning.md requires every machine to check out at
// $HOME/work/<checkout> — into the value sent to the remote host: an
// absolute path as-is, or a path relative to the remote's own $HOME, which
// remoteGenerateScript resolves against its own $HOME since this process
// cannot see the remote's.
func resolvePatchSourceArg(sourceRepo string) string {
	if filepath.IsAbs(sourceRepo) {
		return sourceRepo
	}
	if sourceRepo == "~" {
		return ""
	}
	if strings.HasPrefix(sourceRepo, "~/") {
		return strings.TrimPrefix(sourceRepo, "~/")
	}
	if !isPatchRepoPath(sourceRepo) {
		if checkout, ok := registryCheckout(sourceRepo); ok {
			return workRelative + "/" + checkout
		}
	}
	die(1, "source repo must be an absolute path or a registry id: %s", sourceRepo)
	return ""
}

func mainRepoDir(dir string) string {
	common, ok := gitOutput(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if !ok {
		die(1, "not a git repository: %s", dir)
	}
	return filepath.Dir(common)
}

func repoLookupKey(dir string) (string, bool) {
	main := mainRepoDir(dir)
	rel, err := filepath.Rel(homeDir(), main)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// branchListEntry is one line of a branch list under ~/.config/git: the
// repository the line is about, and the branch it constrains.
type branchListEntry struct{ path, branch string }

func readBranchList(path string) []branchListEntry {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	var entries []branchListEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		entries = append(entries, branchListEntry{path: fields[0], branch: fields[1]})
	}
	return entries
}

// identityPath is the $HOME-relative path of the repository a checkout belongs
// to — the main repository, never a linked worktree's own directory. A checkout
// outside $HOME gets its absolute path, which matches no entry: entries are
// $HOME-relative, so such a checkout can only ever be misplaced.
func identityPath(dir string) string {
	if key, ok := repoLookupKey(dir); ok {
		return key
	}
	return mainRepoDir(dir)
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

// protection is how one checkout resolves against a branch list. A non-empty
// expected means the list recognises the repository by its origin but places it
// somewhere else: the checkout is misplaced, and no rail may read that as
// "unprotected".
type protection struct {
	branch   string
	expected string
	actual   string
	identity string
}

func (p protection) misplaced() bool { return p.expected != "" }

// remoteMatches reports whether an entry path names the repository identity:
// the whole path, or its final owner/repo components. A '/' is required before
// the identity so that work/xacme/widget does not answer for acme/widget.
func remoteMatches(entryPath, identity string) bool {
	return entryPath == identity || strings.HasSuffix(entryPath, "/"+identity)
}

// lookupProtection resolves a checkout against the protected-branches list.
// Its bash twin is branch_listed in git-hooks/protected-refs-policy, and
// tests/fixtures/protection-cases.tsv is what keeps the two agreeing.
func lookupProtection(dir string) (protection, bool) {
	actual := identityPath(dir)
	entries := readBranchList(filepath.Join(homeDir(), ".config", "git", "protected-branches"))
	for _, entry := range entries {
		if entry.path == actual {
			return protection{branch: entry.branch, actual: actual}, true
		}
	}
	identity := originRepo(dir)
	if identity == "" {
		return protection{}, false
	}
	for _, entry := range entries {
		if remoteMatches(entry.path, identity) {
			return protection{
				branch:   entry.branch,
				expected: entry.path,
				actual:   actual,
				identity: identity,
			}, true
		}
	}
	return protection{}, false
}

var unsafeSlug = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func repoSlug(dir string) string {
	value, ok := repoLookupKey(dir)
	if !ok {
		value = filepath.Base(mainRepoDir(dir))
	}
	return unsafeSlug.ReplaceAllString(value, "-")
}

func currentBranch(dir string) string {
	branch, _ := gitOutput(dir, "branch", "--show-current")
	if branch == "" {
		die(1, "HEAD is detached; check out a branch before continuing")
	}
	return branch
}

// Reports false when origin/HEAD does not resolve; callers name the repair, never guess.
func defaultRemoteBranch(dir string) (string, bool) {
	ref, ok := gitOutput(dir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if !ok {
		return "", false
	}
	return strings.TrimPrefix(ref, "refs/remotes/origin/"), true
}

func collectRepos(root string) []string {
	var repos []string
	var visit func(string)
	visit = func(dir string) {
		if isRepoRoot(dir) {
			rel, _ := filepath.Rel(root, dir)
			repos = append(repos, filepath.ToSlash(rel))
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name() == ".git" {
				continue
			}
			if strings.HasPrefix(entry.Name(), ".") && !isRepoRoot(filepath.Join(dir, entry.Name())) {
				continue
			}
			visit(filepath.Join(dir, entry.Name()))
		}
	}
	visit(root)
	return repos
}

func executableDir() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(path)
}

func findExecutable(candidates []string, failure string) string {
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return candidate
		}
	}
	die(1, failure)
	return ""
}

func replaceProcess(name string, args []string, env []string) {
	path, err := exec.LookPath(name)
	if err != nil {
		die(1, "%s not found on PATH", name)
	}
	if err := syscall.Exec(path, append([]string{name}, args...), env); err != nil {
		die(1, "could not execute %s", name)
	}
}

func delegatePR(args []string) {
	tools := strings.TrimRight(os.Getenv("ALLOD_TOOLS_DIR"), "/")
	wd := workDir()
	configured := ""
	if tools != "" {
		configured = filepath.Join(tools, "pr-explain", "explain")
	}
	script := findExecutable([]string{
		configured,
		filepath.Join(executableDir(), "pr-explain", "explain"),
		filepath.Join(wd, "allod", "tools", "pr-explain", "explain"),
	}, "PR explanation tool files not found; set ALLOD_TOOLS_DIR to an allod/tools checkout")
	replaceProcess("bash", append([]string{script}, args...), os.Environ())
}
