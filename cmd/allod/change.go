package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"forge.anarch.diy/allod/tools/internal/protection"
)

// changeSandboxEnv is the marker 'run' sets inside the sandbox it builds
// (never on the pass-through allowlist, so nothing outside can set it):
// with it set, 'record' and 'submit' resolve their repository as an
// ordinary one rather than refuse a worker's own clone as the overseer's to
// handle from outside.
const changeSandboxEnv = "ALLOD_CHANGE_SANDBOX"

func insideSandbox() bool {
	return os.Getenv(changeSandboxEnv) == "1"
}

func changeMain(args []string) {
	if len(args) == 0 {
		fmt.Fprint(stderr, changeUsageText)
		exit(1)
	}
	command, args := args[0], args[1:]
	switch command {
	case "begin":
		changeBegin(args)
	case "list":
		changeList(args)
	case "record":
		changeRecord(args)
	case "submit":
		changeSubmit(args)
	case "cleanup":
		changeCleanup(args)
	case "run":
		changeRun(args)
	case "-h", "--help":
		fmt.Fprint(stdout, changeUsageText)
	default:
		die(1, "unknown change command: %s", command)
	}
}

// exitMisplacedCheckout is unused by every other refusal in 'change begin' and
// 'change record', so a caller can tell this one apart.
const exitMisplacedCheckout = 8

// exitLedgeredClone is unused by every other refusal, so a caller can tell
// "this path is a worker's clone, and the worker itself records and submits
// it from inside its own sandbox" apart from the rest.
const exitLedgeredClone = 11

func requireCanonicalCheckout(found protection.Status) {
	if !found.Misplaced() {
		return
	}
	die(exitMisplacedCheckout,
		"protected repository '%s' is checked out at '%s', but its protected-branches entry is '%s'; move the checkout to '%s' so its branch protections apply",
		found.Identity, found.Actual, found.Expected, found.Expected)
}

func validDescription(description string) bool {
	if description == "" {
		return false
	}
	for _, r := range description {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	// check-ref-format needs no repository, and the caller's cwd may be a
	// copied tree whose .git is a dangling worktree pointer; pin the cwd outside it.
	return gitQuiet(os.TempDir(), "check-ref-format", "--branch", "agent/"+description)
}

func localRefExists(repo, branch string) bool {
	return gitQuiet(repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
}

func remoteRefExists(repo, branch string) bool {
	output, status := captureCommand(repo, nil, true, "git", "ls-remote", "--exit-code", "--heads", "origin", branch)
	switch status {
	case 0:
		return true
	case 2:
		return false
	default:
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
		die(1, "could not check origin for branch '%s'", branch)
		return false
	}
}

// rollbackClone only removes the clone directory; call it before any ledger
// claim exists (a failed claim itself), rollbackClaim after.
func rollbackClone(path string) {
	if err := os.RemoveAll(path); err != nil {
		fmt.Fprintf(stderr, "allod: could not remove %s after a failed begin: %v\n", path, err)
	}
}

// rollbackClaim undoes a successful claimChangeLedgerEntry along with the
// clone directory it names, for every failure from the claim onward.
func rollbackClaim(repo, key, path string) {
	if err := removeChangeLedgerEntry(repo, key); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "allod: could not remove ledger claim '%s' after a failed begin: %v\n", key, err)
	}
	rollbackClone(path)
}

// repoUnderWorkDir: resolveClone only walks workDir(), so a clone issued from a
// repository outside it would carry a ledger entry nothing could find again.
func repoUnderWorkDir(repo string) bool {
	rel, err := filepath.Rel(physicalPath(workDir()), physicalPath(repo))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func cloneFrom(parent, path, originURL string) (string, int) {
	if output, status := captureCommand("", nil, true, "git", "clone", "--no-hardlinks", "--no-checkout", parent, path); status != 0 {
		return output, status
	}
	if output, status := captureCommand(path, nil, true, "git", "remote", "set-url", "origin", originURL); status != 0 {
		return output, status
	}
	if output, status := captureCommand(path, nil, true, "git", "fetch", "--prune", "origin"); status != 0 {
		return output, status
	}
	// The local clone step sets origin/HEAD to whatever the parent had
	// checked out, not the real remote's default; ask the real remote.
	if output, status := captureCommand(path, nil, true, "git", "remote", "set-head", "origin", "-a"); status != 0 {
		return output, status
	}
	return "", 0
}

func changeBegin(args []string) {
	description, repoArg := "", ""
	descriptionSet, resume := false, false
	for len(args) > 0 {
		switch args[0] {
		case "-d":
			requireValue(args, args[0])
			if descriptionSet {
				die(1, "change begin takes one of -d or --resume, not both")
			}
			description, descriptionSet = args[1], true
			args = args[2:]
		case "--resume":
			requireValue(args, args[0])
			if descriptionSet {
				die(1, "change begin takes one of -d or --resume, not both")
			}
			description, descriptionSet, resume = args[1], true, true
			args = args[2:]
		case "-h", "--help":
			fmt.Fprint(stdout, changeUsageText)
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for change begin: %s", args[0])
			}
			if repoArg != "" {
				die(1, "change begin accepts at most one repo path")
			}
			repoArg, args = args[0], args[1:]
		}
	}

	repo := resolveGitRepo(repoArg)
	found, isProtected := lookupProtection(repo)
	requireCanonicalCheckout(found)
	protected := found.Start()
	if !descriptionSet {
		if isProtected {
			die(1, "change begin requires -d <description> to branch in protected repo '%s'", repo)
		}
		fmt.Fprintln(stdout, repo)
		return
	}
	if !repoUnderWorkDir(repo) {
		die(1, "'%s' is not under the work directory '%s'; 'begin -d' and '--resume' can only clone a repository 'list', 'record', and 'cleanup' can also find", repo, workDir())
	}
	if !validDescription(description) {
		die(1, "invalid description '%s'; use only letters, numbers, '.', '_', and '-'", description)
	}
	branch := "agent/" + description
	originURL, ok := gitOutput(repo, "remote", "get-url", "origin")
	if !ok || originURL == "" {
		die(1, "repository '%s' has no 'origin' remote; an isolated change needs one", repo)
	}
	// A description already claimed collides whether that claim is a ledgered
	// clone or, during the drain window, a legacy worktree's local branch.
	claimed := func() bool { return localRefExists(repo, branch) || changeLedgerHasBranch(repo, branch) }
	start := ""
	if resume {
		start = branch
		if claimed() {
			die(5, "branch '%s' already exists locally; resume creates it from origin, so use that checkout or clean it up", branch)
		}
		if !remoteRefExists(repo, branch) {
			die(5, "branch '%s' does not exist on origin; use -d to start a new change", branch)
		}
	} else {
		start = protected
		if !isProtected {
			resolved, ok := defaultRemoteBranch(repo)
			if !ok {
				die(1, "could not resolve the default branch of '%s'; run: git -C %s remote set-head origin -a", repo, repo)
			}
			start = resolved
		}
		if start == "" {
			die(1, "could not resolve the default branch of '%s'; run: git -C %s remote set-head origin -a", repo, repo)
		}
		if claimed() {
			die(5, "branch '%s' already exists locally; use a different -d or clean it up", branch)
		}
		if remoteRefExists(repo, branch) {
			die(5, "branch '%s' already exists on origin; use a different -d or clean it up", branch)
		}
	}
	if output, status := captureCommand(repo, nil, true, "git", "fetch", "origin"); status != 0 {
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
		exit(status)
	}
	if !gitQuiet(repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+start+"^{commit}") {
		die(1, "'origin/%s' does not exist in '%s' after fetching origin; check the branch name", start, repo)
	}

	changes := filepath.Join(homeDir(), "changes")
	if err := os.MkdirAll(changes, 0755); err != nil {
		die(1, "could not create changes directory: %s", changes)
	}
	path, err := makeTempDir(changes, repoSlug(repo)+"-"+description+"-", 6)
	if err != nil {
		die(1, "could not create clone directory")
	}
	// claimed() above is advisory; this O_EXCL claim is the atomic step, and every
	// failure after it undoes the claim too (rollbackClaim), not just the directory.
	if err := claimChangeLedgerEntry(repo, description, changeLedgerEntry{path: path, branch: branch}); err != nil {
		rollbackClone(path)
		if os.IsExist(err) {
			die(5, "branch '%s' already exists locally; use a different -d or clean it up", branch)
		}
		fmt.Fprintf(stderr, "allod: could not record %s in the parent's ledger: %v\n", path, err)
		exit(1)
	}
	if output, status := cloneFrom(repo, path, originURL); status != 0 {
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
		rollbackClaim(repo, description, path)
		exit(status)
	}
	initialBranch, _ := gitOutput(path, "branch", "--show-current")
	output, status := "", 0
	if resume {
		output, status = captureCommand(path, nil, true, "git", "checkout", "-b", branch, "--track", "origin/"+branch)
	} else {
		output, status = captureCommand(path, nil, true, "git", "checkout", "-b", branch, "origin/"+start)
	}
	if status != 0 {
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
		rollbackClaim(repo, description, path)
		exit(status)
	}
	if initialBranch != "" && initialBranch != branch {
		if output, status := captureCommand(path, nil, true, "git", "config", "--remove-section", "branch."+initialBranch); status != 0 {
			if output != "" {
				fmt.Fprintln(stderr, output)
			}
			rollbackClaim(repo, description, path)
			exit(status)
		}
	}
	if output, status := captureCommand(path, nil, true, "git", "config", "branch."+branch+".remote", "origin"); status != 0 {
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
		rollbackClaim(repo, description, path)
		exit(status)
	}
	if output, status := captureCommand(path, nil, true, "git", "config", "branch."+branch+".merge", "refs/heads/"+branch); status != 0 {
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
		rollbackClaim(repo, description, path)
		exit(status)
	}
	gitDir, ok := gitOutput(path, "rev-parse", "--path-format=absolute", "--git-dir")
	if !ok || os.WriteFile(filepath.Join(gitDir, "allod-change-branch"), []byte(branch+"\n"), 0644) != nil {
		fmt.Fprintf(stderr, "allod: could not write the branch handoff file for %s\n", path)
		rollbackClaim(repo, description, path)
		exit(1)
	}
	fmt.Fprintln(stdout, path)
}

type worktreeRecord struct {
	path             string
	branch           string
	detached, locked bool
	prunable         bool
}

func parseWorktrees(text string) []worktreeRecord {
	var records []worktreeRecord
	current := worktreeRecord{}
	flush := func() {
		if current.path != "" {
			records = append(records, current)
		}
		current = worktreeRecord{}
	}
	for _, line := range strings.Split(text+"\n", "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			current.path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			current.branch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "detached":
			current.detached = true
		case line == "locked" || strings.HasPrefix(line, "locked "):
			current.locked = true
		case line == "prunable" || strings.HasPrefix(line, "prunable "):
			current.prunable = true
		case line == "":
			flush()
		}
	}
	return records
}

// The second return is non-empty when ls-files itself failed; that is never "no submodule".
func hasPopulatedSubmodule(path string) (bool, string) {
	output, status := captureCommand(path, nil, false, "git", "ls-files", "--stage", "-z")
	if status != 0 {
		return false, fmt.Sprintf("'git ls-files --stage' could not be read (exit %d): %s", status, output)
	}
	for _, entry := range strings.Split(output, "\x00") {
		if !strings.HasPrefix(entry, "160000 ") {
			continue
		}
		parts := strings.SplitN(entry, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		if _, err := os.Lstat(filepath.Join(path, parts[1], ".git")); err == nil {
			return true, ""
		}
	}
	if info, err := os.Stat(filepath.Join(path, ".git", "modules")); err == nil && info.IsDir() {
		return true, ""
	}
	return false, ""
}

type unpushedResult int

const (
	unpushedYes unpushedResult = iota
	unpushedNo
	unpushedUnknown
)

// unpushedAgainstBase is unpushedCommits' own tail, once it has resolved
// what to compare HEAD against.
func unpushedAgainstBase(dir, base string) (unpushedResult, string) {
	merges, status := captureCommand(dir, nil, false, "git", "rev-list", "--merges", base+"..HEAD")
	if status != 0 {
		return unpushedUnknown, "git could not compare HEAD with " + base
	}
	if merges != "" {
		return unpushedYes, ""
	}
	cherry, status := captureCommand(dir, nil, false, "git", "cherry", base, "HEAD")
	if status != 0 {
		return unpushedUnknown, "git could not compare HEAD with " + base
	}
	for _, line := range strings.Split(cherry, "\n") {
		if strings.HasPrefix(line, "+") {
			return unpushedYes, ""
		}
	}
	return unpushedNo, ""
}

func unpushedCommits(dir string) (unpushedResult, string) {
	branch, _ := gitOutput(dir, "branch", "--show-current")
	if branch == "" {
		return unpushedUnknown, "HEAD is detached"
	}
	base := ""
	if upstream, ok := gitOutput(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); ok && upstream != "" {
		base = upstream
	} else if gitQuiet(dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch+"^{commit}") {
		base = "refs/remotes/origin/" + branch
	} else if gitQuiet(dir, "remote", "get-url", "origin") {
		defaultBranch, ok := defaultRemoteBranch(dir)
		if !ok {
			return unpushedUnknown, "the default branch of origin cannot be resolved"
		}
		if !gitQuiet(dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+defaultBranch+"^{commit}") {
			return unpushedUnknown, "origin/" + defaultBranch + " cannot be resolved"
		}
		base = "refs/remotes/origin/" + defaultBranch
	} else {
		return unpushedUnknown, "the repository has no origin remote"
	}
	return unpushedAgainstBase(dir, base)
}

// workingTreeState is a legacy linked worktree's state once it is known the
// directory exists and is not detached; a standalone clone's state comes
// from the forge instead, never from its own disk.
func workingTreeState(path string) string {
	if populated, _ := hasPopulatedSubmodule(path); populated {
		return "submodule"
	}
	if status, _ := gitOutput(path, "status", "--porcelain"); status != "" {
		return "dirty"
	}
	result, _ := unpushedCommits(path)
	switch result {
	case unpushedYes:
		return "unpushed"
	case unpushedNo:
		return "clean"
	default:
		return "unknown"
	}
}

func worktreeState(path string, record worktreeRecord) string {
	if record.prunable {
		return "prunable"
	}
	if record.locked {
		return "locked"
	}
	if record.detached {
		return "detached"
	}
	return workingTreeState(path)
}

// forgePRStateForBranch returns "" for no pull request, a short PR state when
// one exists, and ok=false when the forge cannot be reached.
func forgePRStateForBranch(parentRepo, branch string) (state string, ok bool) {
	output, status := captureCommand(parentRepo, nil, true, "forge", "pr", "find-by-head", "-s", "all", branch)
	if status != 0 {
		return "", false
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return "", true
	}
	_, state, found := strings.Cut(output, "\t")
	if !found {
		return "", false
	}
	return state, true
}

// Hold the ledger lock across every clone read. A running worker gets a
// "running" row without a host-side Git command against its clone; every
// other row is read from the forge, never from the clone's own disk.
func listRow(main, name string, entry changeLedgerEntry) {
	lockPath, ok := ledgerFilePath(main, entry)
	if !ok {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tunknown\n", name, entry.path, entry.branch)
		return
	}
	lock, held := lockLedgerExclusive(lockPath)
	if !held {
		fmt.Fprintf(stdout, "%s\t%s\t%s\trunning\n", name, entry.path, entry.branch)
		return
	}
	defer lock.Release()
	if _, err := os.Stat(entry.path); os.IsNotExist(err) {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tmissing\n", name, entry.path, entry.branch)
		return
	}
	state, ok := forgePRStateForBranch(main, entry.branch)
	if !ok {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tunknown\n", name, entry.path, entry.branch)
		return
	}
	if state == "" {
		state = "unsubmitted"
	}
	fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", name, entry.path, entry.branch, state)
}

func listRepo(repoDir string) {
	main := mainRepoDir(repoDir)
	name := main
	wd := strings.TrimRight(workDir(), "/")
	if rel, err := filepath.Rel(wd, main); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		name = filepath.ToSlash(rel)
	}
	for _, entry := range changeLedgerEntries(main) {
		listRow(main, name, entry)
	}
	listing, _ := gitOutput(main, "worktree", "list", "--porcelain")
	records := parseWorktrees(listing)
	for index, record := range records {
		if index == 0 {
			continue
		}
		branch := record.branch
		if branch == "" {
			branch = "(detached)"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", name, record.path, branch, worktreeState(record.path, record))
	}
}

func changeList(args []string) {
	repoArg := ""
	for len(args) > 0 {
		switch args[0] {
		case "-h", "--help":
			fmt.Fprint(stdout, changeUsageText)
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for change list: %s", args[0])
			}
			if repoArg != "" {
				die(1, "change list accepts at most one repo path")
			}
			repoArg, args = args[0], args[1:]
		}
	}
	if repoArg != "" {
		if _, parent, _, _, ok := matchLedgerClone(repoArg); ok {
			listRepo(parent)
		} else {
			listRepo(resolveGitRepo(repoArg))
		}
		return
	}
	wd := workDir()
	for _, name := range collectRepos(wd) {
		listRepo(filepath.Join(wd, filepath.FromSlash(name)))
	}
}

func changeRecord(args []string) {
	message := ""
	messageSet := false
	var files []string
	for len(args) > 0 {
		switch args[0] {
		case "-m", "--message":
			requireValue(args, args[0])
			if messageSet {
				die(1, "commit message specified more than once")
			}
			message, messageSet = args[1], true
			args = args[2:]
		case "-M", "--message-file":
			requireValue(args, args[0])
			if messageSet {
				die(1, "commit message specified more than once")
			}
			message, messageSet = readValue(args[1]), true
			args = args[2:]
		case "-f", "--files":
			requireValue(args, args[0])
			if strings.HasPrefix(args[1], "-") {
				die(1, "%s requires a value", args[0])
			}
			args = args[1:]
			for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
				files = append(files, args[0])
				args = args[1:]
			}
		case "--":
			files = append(files, args[1:]...)
			args = nil
		case "-h", "--help":
			fmt.Fprint(stdout, changeUsageText)
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for change record: %s", args[0])
			}
			die(1, "unexpected argument for change record: %s", args[0])
		}
	}
	sandboxed := insideSandbox()
	repo, _, entry, isClone := resolveCloneOrRepo("")
	if isClone && !sandboxed {
		die(exitLedgeredClone,
			"'%s' is a sandboxed worker's clone; the worker records and submits it from inside its own sandbox, not the overseer from outside: run 'allod change run %s -- allod change record ...'",
			repo, entry.path)
	}

	found, isProtected := lookupProtection(repo)
	if !sandboxed {
		requireCanonicalCheckout(found)
	}

	branch := currentBranch(repo)

	// The handoff pin catches an accidental branch move; a worker can rewrite
	// the pin, but only deliberately.
	gitDir, ok := gitOutput(repo, "rev-parse", "--path-format=absolute", "--git-dir")
	if !ok {
		exit(1)
	}
	if handoff, err := os.ReadFile(filepath.Join(gitDir, "allod-change-branch")); err == nil {
		if expected := strings.TrimSpace(string(handoff)); expected != branch {
			die(3, "checkout '%s' now has branch '%s' checked out, but 'allod change begin' handed out branch '%s'; the checkout was moved after begin ran, so record refuses to commit into the wrong branch", repo, branch, expected)
		}
	}

	if isProtected && found.Covers(branch) {
		die(2, "refusing to commit directly to protected branch '%s'; run 'allod change begin' first", branch)
	}

	// A sandboxed worker's clone and a linked worktree both belong to one
	// agent; only a shared main checkout can hold another's edits.
	if len(files) == 0 && !sandboxed && mainRepoDir(repo) == repo {
		if diff, ok := gitOutput(repo, "diff", "--name-only"); ok && diff != "" {
			die(7, "refusing to stage unnamed tracked changes in a shared checkout: %s; use --files to name what to record", strings.ReplaceAll(diff, "\n", ", "))
		}
	}

	// New files are invisible to 'git add -u': recording without naming them pushes a commit that silently leaves them out.
	if remaining := untrackedNotNamed(repo, files); len(remaining) > 0 {
		die(1, "refusing to record with untracked files the commit would not include: %s; name them with --files, ignore them in .gitignore, or delete them", strings.Join(remaining, ", "))
	}

	if len(files) > 0 {
		gitArgs := append([]string{"add", "--"}, files...)
		if status := gitInherit(repo, gitArgs...); status != 0 {
			exit(status)
		}
	} else if status := gitInherit(repo, "add", "-u"); status != 0 {
		exit(status)
	}
	shouldCommit := !gitQuiet(repo, "diff", "--cached", "--quiet")
	if !shouldCommit {
		result, detail := unpushedCommits(repo)
		switch result {
		case unpushedYes:
		case unpushedNo:
			die(4, "nothing to commit and no unpushed commits to push")
		case unpushedUnknown:
			die(4, "nothing to commit; cannot determine whether commits are unpushed: %s", detail)
		}
	}
	if shouldCommit {
		if !messageSet || message == "" {
			die(1, "change record requires a non-empty commit message")
		}
		file, err := os.CreateTemp("", "allod-message-")
		if err != nil {
			die(1, "could not create temporary message file")
		}
		name := file.Name()
		defer os.Remove(name)
		if _, err := file.WriteString(message); err != nil || file.Close() != nil {
			die(1, "could not write temporary message file")
		}
		if status := gitInherit(repo, "commit", "-F", name); status != 0 {
			exit(status)
		}
	}
	if status := gitInherit(repo, "push", "-u", "origin", "HEAD"); status != 0 {
		exit(status)
	}
	commit, ok := gitOutput(repo, "rev-parse", "--short", "HEAD")
	if !ok {
		exit(1)
	}
	fmt.Fprintf(stdout, "Branch: %s\nCommit: %s\n", branch, commit)
}

// Paths are normalized before comparison ('./x', absolute) so the refusal
// never fires on a file the named path covers.
func untrackedNotNamed(repo string, named []string) []string {
	output, ok := gitOutput(repo, "ls-files", "--others", "--exclude-standard")
	if !ok || output == "" {
		return nil
	}
	var paths []string
	for _, untracked := range strings.Split(output, "\n") {
		if untracked == "" {
			continue
		}
		covered := false
		for _, name := range named {
			clean := filepath.Clean(name)
			if filepath.IsAbs(clean) {
				rel, err := filepath.Rel(repo, clean)
				if err != nil || strings.HasPrefix(rel, "..") {
					continue
				}
				clean = rel
			}
			if untracked == clean || strings.HasPrefix(untracked, clean+"/") {
				covered = true
				break
			}
		}
		if !covered {
			paths = append(paths, untracked)
		}
	}
	return paths
}

func changeSubmit(args []string) {
	title, body, bodySource, base, depends := "", "", "", "", ""
	titleSet, bodySet, dryRun := false, false, false
	for len(args) > 0 {
		switch args[0] {
		case "-t", "--title":
			requireValue(args, args[0])
			if titleSet {
				die(1, "title specified more than once")
			}
			title, titleSet, args = args[1], true, args[2:]
		case "-b", "--body":
			requireValue(args, args[0])
			if bodySet {
				die(1, "%s cannot be combined with %s", args[0], bodySource)
			}
			body, bodySet, bodySource, args = args[1], true, args[0], args[2:]
		case "-F", "--body-file":
			requireValue(args, args[0])
			if bodySet {
				die(1, "%s cannot be combined with %s", args[0], bodySource)
			}
			body, bodySet, bodySource, args = readValue(args[1]), true, args[0], args[2:]
		case "--base":
			requireValue(args, args[0])
			if args[1] == "" {
				die(1, "--base cannot be empty")
			}
			base, args = args[1], args[2:]
		case "--depends-on":
			requireValue(args, args[0])
			if depends != "" {
				die(1, "--depends-on specified more than once")
			}
			depends, args = args[1], args[2:]
		case "--dry-run":
			dryRun, args = true, args[1:]
		case "-h", "--help":
			fmt.Fprint(stdout, changeUsageText)
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for change submit: %s", args[0])
			}
			die(1, "unexpected argument for change submit: %s", args[0])
		}
	}
	if !titleSet || title == "" {
		die(1, "change submit requires a non-empty title")
	}
	sandboxed := insideSandbox()
	repo, _, entry, isClone := resolveCloneOrRepo("")
	if isClone && !sandboxed {
		die(exitLedgeredClone,
			"'%s' is a sandboxed worker's clone; the worker records and submits it from inside its own sandbox, not the overseer from outside: run 'allod change run %s -- allod change submit ...'",
			repo, entry.path)
	}

	found, _ := lookupProtection(repo)
	if !sandboxed {
		requireCanonicalCheckout(found)
	}
	branch := currentBranch(repo)
	gitDir, ok := gitOutput(repo, "rev-parse", "--path-format=absolute", "--git-dir")
	if !ok {
		exit(1)
	}
	if handoff, err := os.ReadFile(filepath.Join(gitDir, "allod-change-branch")); err == nil {
		if expected := strings.TrimSpace(string(handoff)); expected != branch {
			die(3, "checkout '%s' now has branch '%s' checked out, but 'allod change begin' handed out branch '%s'; the checkout was moved after begin ran, so submit refuses to create a pull request from the wrong branch", repo, branch, expected)
		}
	}
	if base == "" {
		resolved, ok := defaultRemoteBranch(repo)
		if !ok {
			die(1, "could not resolve the default branch of '%s' to use as the PR base; run: git -C %s remote set-head origin -a, or pass --base", repo, repo)
		}
		base = resolved
	}
	if !dryRun {
		if _, err := exec.LookPath("forge"); err != nil {
			die(1, "'forge' not found on PATH")
		}
	}
	if depends != "" {
		body += "\n\nDepends on: " + depends
	}
	if dryRun {
		fmt.Fprintf(stdout, "Branch: %s\nBase: %s\nTitle: %s\nBody:\n%s\n", branch, base, title, body)
		return
	}
	existing, status := captureCommand(repo, nil, true, "forge", "pr", "find-by-head", branch)
	if status != 0 {
		if existing != "" {
			fmt.Fprintln(stderr, existing)
		}
		exit(status)
	}
	if existing != "" {
		die(6, "PR #%s already exists for '%s'; use 'forge pr edit' instead", existing, branch)
	}
	file, err := os.CreateTemp("", "allod-body-")
	if err != nil {
		die(1, "could not create temporary body file")
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.WriteString(body); err != nil || file.Close() != nil {
		die(1, "could not write temporary body file")
	}
	output, status := captureCommand(repo, nil, true, "forge", "pr", "create", "-t", title, "-H", branch, "-B", base, "-F", name)
	if status != 0 {
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
		exit(status)
	}
	fmt.Fprintln(stdout, output)
}

// cleanupClone deletes no branch in the parent: a clone's branch never lived there.
// cleanupClone never runs git in the clone: a clone is removable once the
// forge shows its branch's pull request merged or closed, judged from the
// parent, not from the clone's own disk.
func cleanupClone(parentRepo string, entry changeLedgerEntry) {
	lock := requireExclusiveLedgerLock(parentRepo, entry)
	defer lock.Release()
	info, statErr := os.Stat(entry.path)
	switch {
	case statErr != nil && os.IsNotExist(statErr):
		if err := removeChangeLedgerEntry(parentRepo, entry.key); err != nil {
			die(1, "clone '%s' is missing but its ledger entry could not be dropped: %v", entry.path, err)
		}
		fmt.Fprintf(stdout, "clone '%s' is missing; dropped its ledger entry in '%s'\n", entry.path, parentRepo)
		return
	case statErr != nil:
		die(1, "could not check clone '%s': %v", entry.path, statErr)
	case !info.IsDir():
		die(1, "clone '%s' exists but is not a directory; this refuses rather than treat that as the clone being missing", entry.path)
	}
	state, ok := forgePRStateForBranch(parentRepo, entry.branch)
	if !ok {
		die(1, "could not ask the forge for the pull request on branch '%s'", entry.branch)
	}
	switch state {
	case "":
		die(1, "branch '%s' has no pull request yet; the worker submits it with 'allod change run %s -- allod change submit ...', or remove '%s' with standard tools and run cleanup again to drop its ledger entry", entry.branch, entry.path, entry.path)
	case "open":
		die(1, "pull request for branch '%s' is still open; cleanup refuses until it is merged or closed", entry.branch)
	case "merged", "closed":
	default:
		die(1, "forge reported an unrecognized state '%s' for the pull request on branch '%s'", state, entry.branch)
	}
	if err := os.RemoveAll(entry.path); err != nil {
		die(1, "could not remove clone directory: %s", entry.path)
	}
	if err := removeChangeLedgerEntry(parentRepo, entry.key); err != nil {
		fmt.Fprintf(stderr, "allod: removed clone %s but could not drop its ledger entry: %v\n", entry.path, err)
		exit(1)
	}
}

func changeCleanup(args []string) {
	if len(args) != 1 || args[0] == "" {
		die(1, "usage: allod change cleanup <path>")
	}
	// The ledger is matched before anything requires a git repository: a 'missing'
	// clone has no directory for resolveGitRepo to accept, and its entry must still drop.
	target, err := filepath.Abs(args[0])
	if err != nil {
		die(1, "could not resolve path: %s", args[0])
	}
	if parentRepo, entry, isClone := resolveClone(target); isClone {
		cleanupClone(parentRepo, entry)
		return
	}
	repo := resolveGitRepo(args[0])
	gitDir, ok := gitOutput(repo, "rev-parse", "--path-format=absolute", "--git-dir")
	if !ok {
		exit(1)
	}
	commonDir, ok := gitOutput(repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if !ok {
		exit(1)
	}
	if gitDir == commonDir {
		die(1, "'%s' is a regular repository, not a linked worktree", repo)
	}
	branch := currentBranch(repo)
	main := filepath.Dir(commonDir)
	if dirty, _ := gitOutput(repo, "status", "--porcelain"); dirty != "" {
		die(1, "worktree '%s' is dirty; clean it before cleanup", repo)
	}
	result, detail := unpushedCommits(repo)
	switch result {
	case unpushedYes:
		die(1, "worktree '%s' has unpushed commits; push or handle them before cleanup", repo)
	case unpushedUnknown:
		die(1, "cannot determine whether worktree '%s' has unpushed commits: %s; inspect it before deliberately removing it with standard git", repo, detail)
	}
	if status := gitInherit(main, "worktree", "remove", repo); status != 0 {
		exit(status)
	}
	if strings.HasPrefix(branch, "agent/") {
		if status := gitInherit(main, "branch", "-D", branch); status != 0 {
			exit(status)
		}
	} else {
		fmt.Fprintf(stderr, "allod: removed worktree; leaving non-agent branch %s\n", branch)
	}
	if status := gitInherit(main, "worktree", "prune"); status != 0 {
		exit(status)
	}
}
