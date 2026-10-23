package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"forge.anarch.diy/allod/tools/internal/protection"
)

func changeMain(args []string) {
	defer func() { cloneGitEnv = nil }()
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

// exitCloneOriginMismatch is unused by every other refusal, so a caller can
// tell a clone whose origin no longer matches its parent's apart from the rest.
const exitCloneOriginMismatch = 9

// exitCloneConfigUnsafe is unused by every other refusal, so a caller can tell
// a clone whose own '.git/config' could run a command apart from the rest.
const exitCloneConfigUnsafe = 11

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

// nonEmptyLines splits git's one-URL-per-line output, dropping blanks.
func nonEmptyLines(output string) []string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// Compare every fetch and push URL; Git can push to multiple pushurl values.
func requireCloneOriginMatches(parent, clone string) {
	if reason := cloneOriginMismatchReason(parent, clone); reason != "" {
		die(exitCloneOriginMismatch, "%s", reason)
	}
}

func cloneOriginMismatchReason(parent, clone string) string {
	if info, err := os.Stat(clone); err != nil || !info.IsDir() {
		return ""
	}
	parentFetch, _ := gitOutput(parent, "remote", "get-url", "origin")
	cloneFetchAll, _ := gitOutput(clone, "remote", "get-url", "--all", "origin")
	clonePushAll, _ := gitOutput(clone, "remote", "get-url", "--push", "--all", "origin")
	cloneFetch := nonEmptyLines(cloneFetchAll)
	clonePush := nonEmptyLines(clonePushAll)
	if len(cloneFetch) == 1 && cloneFetch[0] == parentFetch &&
		len(clonePush) == 1 && clonePush[0] == parentFetch {
		return ""
	}
	return fmt.Sprintf(
		"clone '%s' has fetch URL(s) %v and push URL(s) %v, but the repo that ledgered it, '%s', has fetch URL '%s'; a worker can edit its own clone's remote, so this refuses to push or judge anything against an origin its overseer did not set",
		clone, cloneFetch, clonePush, parent, parentFetch)
}

// Clone config is worker owned. Only inert keys and pinned branch settings pass.
var changeConfigAllowlist = map[string]bool{
	"core.repositoryformatversion": true,
	"core.filemode":                true,
	"core.logallrefupdates":        true,
	"core.ignorecase":              true,
	"core.precomposeunicode":       true,
	"remote.origin.url":            true,
	"remote.origin.pushurl":        true,
	// git itself writes these for a reftable or sha256 repository; neither
	// runs anything. extensions.worktreeconfig, and any other extensions.*
	// key, stays refused — not named here, on purpose.
	"extensions.refstorage":   true,
	"extensions.objectformat": true,
	// core.bare and remote.origin.fetch are not here, on purpose: both are
	// pinned to the one value an ordinary clone has, below, rather than
	// accepted with any value at all.
}

// changeConfigFetchRefspec is the fetch refspec an ordinary 'git clone'
// writes; 'remote.origin.fetch' is allowed only with exactly this value.
const changeConfigFetchRefspec = "+refs/heads/*:refs/remotes/origin/*"

func changeConfigKeyAllowed(key, value, ledgerBranch string) bool {
	if changeConfigAllowlist[key] {
		return true
	}
	switch key {
	case "core.bare":
		return value == "false"
	case "remote.origin.fetch":
		return value == changeConfigFetchRefspec
	}
	if rest, ok := strings.CutPrefix(key, "branch."); ok {
		name, ok := strings.CutPrefix(rest, ledgerBranch+".")
		if !ok {
			return false
		}
		switch name {
		case "remote":
			return value == "origin"
		case "merge":
			return value == "refs/heads/"+ledgerBranch
		}
		return false
	}
	return false
}

// git rewrites FETCH_HEAD, ORIG_HEAD, COMMIT_EDITMSG, packed-refs and reflogs without a
// lockfile, so a symlink anywhere under .git redirects an outside command's write.
func gitDirSymlinkUnsafeReason(clone, gitPath string) string {
	var reason string
	filepath.WalkDir(gitPath, func(path string, entry fs.DirEntry, err error) error {
		if reason != "" {
			return fs.SkipAll
		}
		if path == gitPath {
			return nil
		}
		if err != nil {
			reason = fmt.Sprintf("clone '%s' has a '.git' entry that could not be walked: %v", clone, err)
			return fs.SkipAll
		}
		if entry.Type()&os.ModeSymlink != 0 {
			rel, relErr := filepath.Rel(gitPath, path)
			if relErr != nil {
				rel = path
			}
			reason = fmt.Sprintf(
				"clone '%s' has a symlink at '.git/%s'; git writes many files under .git without a lockfile, so an outside command's write there could go through it to wherever a worker chose, outside the clone entirely",
				clone, filepath.ToSlash(rel))
			return fs.SkipAll
		}
		return nil
	})
	return reason
}

// Inspect worker metadata before any host-side Git command uses the clone.
// Empty means safe; list uses this form to keep reporting other clones.
func cloneConfigUnsafeReason(clone, ledgerBranch, parent string) string {
	if _, err := os.Stat(clone); err != nil {
		return ""
	}
	gitPath := filepath.Join(clone, ".git")
	if info, err := os.Lstat(gitPath); err != nil || !info.IsDir() {
		return fmt.Sprintf(
			"clone '%s' has no ordinary '.git' directory (missing, a gitfile, or a symlink); a worker can point '.git' at a directory of its own whose config this gate never reads but git still loads, so this refuses rather than follow it",
			clone)
	}
	for _, worktreeFile := range []string{"commondir", "gitdir"} {
		if _, err := os.Stat(filepath.Join(gitPath, worktreeFile)); err == nil {
			return fmt.Sprintf(
				"clone '%s' has a '.git/%s' file; that is how a linked worktree's own '.git' redirects git to load config, refs, and hooks from another directory entirely, one this gate never reads, so this refuses rather than trust it",
				clone, worktreeFile)
		}
	}
	if reason := gitDirSymlinkUnsafeReason(clone, gitPath); reason != "" {
		return reason
	}
	configPath := filepath.Join(gitPath, "config")
	if _, err := os.Stat(configPath); err != nil {
		return fmt.Sprintf("clone '%s' has no '.git/config' to read", clone)
	}
	output, status := captureCommand("", nil, true, "git", "config", "--file", configPath, "--list")
	if status != 0 {
		return fmt.Sprintf(
			"clone '%s' has a '.git/config' that 'git config --file --list' could not read (exit %d); this refuses rather than treat an unreadable config as an empty, safe one",
			clone, status)
	}
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		if changeConfigKeyAllowed(key, value, ledgerBranch) {
			continue
		}
		return fmt.Sprintf(
			"clone '%s' has a '.git/config' key '%s' that is not on the allowlist; a worker's own config can run a command the moment anything outside the sandbox next touches the clone, so this refuses first",
			clone, key)
	}
	hooksDir := filepath.Join(gitPath, "hooks")
	if entries, err := os.ReadDir(hooksDir); err == nil {
		for _, hookEntry := range entries {
			if strings.HasSuffix(hookEntry.Name(), ".sample") {
				continue
			}
			info, err := hookEntry.Info()
			if err != nil {
				return fmt.Sprintf("clone '%s' has a hook that cannot be inspected: %v", clone, err)
			}
			if info.IsDir() || info.Mode()&0111 == 0 {
				continue
			}
			return fmt.Sprintf(
				"clone '%s' has an executable hook '.git/hooks/%s'; a worker's own hook runs the moment anything outside the sandbox next touches the clone, so this refuses first",
				clone, hookEntry.Name())
		}
	} else if !os.IsNotExist(err) {
		return fmt.Sprintf("clone '%s' has hooks that cannot be inspected: %v", clone, err)
	}
	if _, err := os.Stat(filepath.Join(gitPath, "config.worktree")); err == nil {
		return fmt.Sprintf(
			"clone '%s' has a '.git/config.worktree', a second place to carry a key the allowlist never sees; this refuses it outright",
			clone)
	}
	if _, err := os.Stat(filepath.Join(gitPath, "info", "grafts")); err == nil {
		return fmt.Sprintf(
			"clone '%s' has a '.git/info/grafts' file; it rewrites a commit's own parentage as git reports it, which 'merge-base --is-ancestor' walks to judge a branch pushed, so this refuses rather than trust it",
			clone)
	}
	if _, err := os.Stat(filepath.Join(gitPath, "shallow")); err == nil {
		return fmt.Sprintf(
			"clone '%s' has a '.git/shallow' file; a shallow clone's own grafted-in boundary commits rewrite parentage the same way '.git/info/grafts' does, so this refuses rather than trust it",
			clone)
	}
	if _, err := os.Lstat(filepath.Join(gitPath, "objects", "info", "commit-graph")); err == nil {
		return fmt.Sprintf("clone '%s' has a worker-controlled commit-graph; ancestry checks refuse it", clone)
	} else if !os.IsNotExist(err) {
		return fmt.Sprintf("clone '%s' commit-graph cannot be inspected: %v", clone, err)
	}
	if _, err := os.Lstat(filepath.Join(gitPath, "objects", "info", "commit-graphs")); err == nil {
		return fmt.Sprintf("clone '%s' has worker-controlled commit-graphs; ancestry checks refuse them", clone)
	} else if !os.IsNotExist(err) {
		return fmt.Sprintf("clone '%s' commit-graphs cannot be inspected: %v", clone, err)
	}
	if output, status := captureCommand(clone, nil, true, "git", "for-each-ref", "refs/replace"); status != 0 {
		return fmt.Sprintf(
			"clone '%s': 'git for-each-ref refs/replace' could not be read (exit %d): %s; this refuses rather than treat an unreadable answer as 'no replace refs'",
			clone, status, output)
	} else if strings.TrimSpace(output) != "" {
		return fmt.Sprintf(
			"clone '%s' has at least one ref under 'refs/replace/'; it substitutes a different object's content wherever the replaced sha is looked up during traversal, which could make an unpushed commit's parent chain look descended from a remote head it never was, so this refuses rather than trust it",
			clone)
	}
	if populated, errReason := hasPopulatedSubmodule(clone); errReason != "" {
		return fmt.Sprintf("clone '%s' could not be checked for a populated submodule: %s; this refuses rather than treat an unreadable answer as 'no submodule'", clone, errReason)
	} else if populated {
		return fmt.Sprintf(
			"clone '%s' has a populated submodule, or a '.git/modules' directory; 'git status' in the parent descends into a populated submodule and loads its own config, which this gate never reads, so sandboxed clones do not support them",
			clone)
	}
	if reason := trackedHooksUnsafeReason(clone, parent); reason != "" {
		return reason
	}
	return ""
}

func requireCloneConfigSafe(clone, ledgerBranch, parent string) {
	if reason := cloneConfigUnsafeReason(clone, ledgerBranch, parent); reason != "" {
		die(exitCloneConfigUnsafe, "%s", reason)
	}
}

func trackedHooksDirName(repo string) string {
	if data, err := os.ReadFile(filepath.Join(repo, ".hookspath")); err == nil {
		line, _, _ := strings.Cut(string(data), "\n")
		return line
	}
	if info, err := os.Stat(filepath.Join(repo, ".hooks")); err == nil && info.IsDir() {
		return ".hooks"
	}
	return ""
}

func gitShowBytes(dir, object string) ([]byte, bool) {
	cmd := exec.Command("git", "--no-replace-objects", "-C", dir, "show", object)
	cmd.Env = gitCommandEnv()
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	return out.Bytes(), true
}

// The worker's tracked hooks must match the parent's remote base byte for byte.
// An overseer-side dispatcher otherwise executes worker edits as host user.
func trackedHooksUnsafeReason(clone, parent string) string {
	cloneHookspath, cloneErr := os.ReadFile(filepath.Join(clone, ".hookspath"))
	parentHookspath, parentErr := os.ReadFile(filepath.Join(parent, ".hookspath"))
	cloneHas, parentHas := cloneErr == nil, parentErr == nil
	if cloneHas != parentHas || !bytes.Equal(cloneHookspath, parentHookspath) {
		return fmt.Sprintf(
			"clone '%s' has a '.hookspath' that differs from the parent checkout's own working-tree '.hookspath'; git-hooks/protected-refs-policy runs whatever directory it names from the clone's own working tree, so this refuses rather than trust a worker-controlled redirect",
			clone)
	}

	hooksDirName := trackedHooksDirName(clone)
	if hooksDirName == "" {
		return ""
	}
	hooksDirPath := filepath.Join(clone, filepath.FromSlash(hooksDirName))
	if info, err := os.Stat(hooksDirPath); err != nil || !info.IsDir() {
		return ""
	}

	defaultBranch, ok := defaultRemoteBranch(parent)
	if !ok {
		return fmt.Sprintf(
			"clone '%s' has a tracked hooks directory ('%s'), but the parent's own 'origin/<default branch>' cannot be resolved to check it against; this refuses rather than trust it unverified",
			clone, hooksDirName)
	}
	base := "origin/" + defaultBranch

	status, ok := gitOutput(clone, "status", "--porcelain", "--ignored", "--", hooksDirName)
	if !ok {
		return fmt.Sprintf(
			"clone '%s' has a tracked hooks directory ('%s') whose 'git status --porcelain --ignored' could not be read; this refuses rather than skip the untracked/ignored check",
			clone, hooksDirName)
	}
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "??") || strings.HasPrefix(line, "!!") {
			return fmt.Sprintf(
				"clone '%s' has an untracked or ignored file under its tracked hooks directory ('%s'): %s",
				clone, hooksDirName, strings.TrimSpace(line))
		}
	}

	var reason string
	filepath.WalkDir(hooksDirPath, func(path string, entry fs.DirEntry, err error) error {
		if reason != "" {
			return fs.SkipAll
		}
		if err != nil {
			reason = fmt.Sprintf("clone '%s' has a tracked hooks directory ('%s') that could not be walked: %v", clone, hooksDirName, err)
			return fs.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(clone, path)
		if relErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		cloneContent, readErr := os.ReadFile(path)
		if readErr != nil {
			reason = fmt.Sprintf("clone '%s' has a tracked hooks file '%s' that could not be read: %v", clone, relSlash, readErr)
			return fs.SkipAll
		}
		baseContent, ok := gitShowBytes(parent, base+":"+relSlash)
		if !ok {
			reason = fmt.Sprintf(
				"clone '%s' has a file under its tracked hooks directory ('%s') that '%s' does not have; a worker's own addition there runs the moment git-hooks/protected-refs-policy next touches the clone, so this refuses first",
				clone, relSlash, base)
			return fs.SkipAll
		}
		if !bytes.Equal(cloneContent, baseContent) {
			reason = fmt.Sprintf(
				"clone '%s' has a tracked hook ('%s') that differs from '%s''s own copy; a worker's own edit there runs the moment git-hooks/protected-refs-policy next touches the clone, so this refuses first",
				clone, relSlash, base)
			return fs.SkipAll
		}
		baseMode := treeEntryMode(parent, base, relSlash)
		cloneIndexMode := indexEntryMode(clone, relSlash)
		if baseMode != "" && cloneIndexMode != "" && cloneIndexMode != baseMode {
			reason = fmt.Sprintf(
				"clone '%s' has a tracked hook ('%s') whose index mode (%s) differs from '%s''s own tree entry (%s); a worker's own 'git update-index --chmod' there runs the moment git-hooks/protected-refs-policy next touches the clone, so this refuses first",
				clone, relSlash, cloneIndexMode, base, baseMode)
			return fs.SkipAll
		}
		fileInfo, statErr := os.Stat(path)
		if statErr != nil {
			reason = fmt.Sprintf("clone '%s' has a tracked hooks file '%s' that could not be stat'd: %v", clone, relSlash, statErr)
			return fs.SkipAll
		}
		baseExecutable := baseMode == "100755"
		cloneExecutable := fileInfo.Mode()&0111 != 0
		if baseMode != "" && baseExecutable != cloneExecutable {
			reason = fmt.Sprintf(
				"clone '%s' has a tracked hook ('%s') whose working-tree executable bit (%v) differs from '%s''s own tree entry (%s); a worker's own 'chmod' there runs the moment git-hooks/protected-refs-policy next touches the clone, so this refuses first",
				clone, relSlash, cloneExecutable, base, baseMode)
			return fs.SkipAll
		}
		return nil
	})
	return reason
}

// treeEntryMode reads the git mode ('100644', '100755', ...) of path in
// gitRef within dir, with 'git ls-tree', or "" if it cannot be resolved.
func treeEntryMode(dir, gitRef, path string) string {
	output, ok := gitOutput(dir, "ls-tree", gitRef, "--", path)
	if !ok || output == "" {
		return ""
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// indexEntryMode reads the git mode path carries in dir's own index, with
// 'git ls-files --stage', or "" if it is not staged there at all.
func indexEntryMode(dir, path string) string {
	output, ok := gitOutput(dir, "ls-files", "--stage", "--", path)
	if !ok || output == "" {
		return ""
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
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
	// A parent that has gc'd carries objects/info/commit-graph, which clone copies in and the
	// gate would refuse; clear it here, since it is the parent's doing, not the worker's.
	os.Remove(filepath.Join(path, ".git", "objects", "info", "commit-graph"))
	os.RemoveAll(filepath.Join(path, ".git", "objects", "info", "commit-graphs"))
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

// unpushedAgainstBase is unpushedCommits' and unpushedCommitsForClone's
// shared tail, once each has resolved what to compare HEAD against.
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

func remoteDefaultBranchSHA(dir string) (string, bool) {
	output, status := captureCommand(dir, nil, true, "git", "ls-remote", "--symref", "origin", "HEAD")
	if status != 0 {
		return "", false
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "HEAD" && fields[0] != "ref:" {
			return fields[0], true
		}
	}
	return "", false
}

// Ask the real remote; a worker can forge remote-tracking refs and upstreams.
// Unknown ancestry never counts as pushed.
func unpushedCommitsForClone(dir, ledgerBranch string) (unpushedResult, string) {
	output, status := captureCommand(dir, nil, true, "git", "ls-remote", "--exit-code", "origin", "refs/heads/"+ledgerBranch)
	switch status {
	case 0:
		fields := strings.Fields(output)
		if len(fields) == 0 {
			return unpushedUnknown, "ls-remote origin returned no commit for refs/heads/" + ledgerBranch
		}
		remoteSHA := fields[0]
		ancestorOutput, ancestorStatus := captureCommand(dir, nil, true, "git", "merge-base", "--is-ancestor", "HEAD", remoteSHA)
		switch ancestorStatus {
		case 0:
			return unpushedNo, ""
		case 1:
			return unpushedYes, ""
		default:
			return unpushedUnknown, "could not compare HEAD to the remote's '" + remoteSHA + "': " + ancestorOutput
		}
	case 2:
		defaultSHA, ok := remoteDefaultBranchSHA(dir)
		if !ok {
			return unpushedUnknown, "refs/heads/" + ledgerBranch + " is absent on origin, and origin's default branch head could not be resolved either"
		}
		// A rebase-merge lands under a new hash; judge by patch, as unpushedCommits does.
		return unpushedAgainstBase(dir, defaultSHA)
	default:
		return unpushedUnknown, "could not check origin for refs/heads/" + ledgerBranch + ": " + output
	}
}

// workingTreeState is the tail a linked worktree shares with a standalone
// clone once it is known the directory exists and is not detached.
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

func workingTreeStateForClone(path, ledgerBranch string) string {
	status, ok := gitOutput(path, "status", "--porcelain")
	if !ok {
		return "unknown"
	}
	if status != "" {
		return "dirty"
	}
	result, _ := unpushedCommitsForClone(path, ledgerBranch)
	switch result {
	case unpushedYes:
		return "unpushed"
	case unpushedNo:
		return "clean"
	default:
		return "unknown"
	}
}

func cloneState(path, ledgerBranch string) string {
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return "missing"
	}
	if branch, _ := gitOutput(path, "branch", "--show-current"); branch == "" {
		return "detached"
	}
	return workingTreeStateForClone(path, ledgerBranch)
}

// Hold the ledger lock across every clone read. A running worker gets a
// "running" row without a host-side Git command against its clone.
func listRow(main, name string, entry changeLedgerEntry) {
	lockPath, ok := ledgerFilePath(main, entry)
	if !ok {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tunsafe\n", name, entry.path, entry.branch)
		return
	}
	lock, held := lockLedgerExclusive(lockPath)
	if !held {
		fmt.Fprintf(stdout, "%s\t%s\t%s\trunning\n", name, entry.path, entry.branch)
		return
	}
	defer lock.Release()
	enableCloneGitEnv()
	// A clone that fails the gate gets its own row, state 'unsafe', and
	// no further git command ever runs in it; one unsafe clone must not
	// stop 'list' from reporting every other one.
	if cloneConfigUnsafeReason(entry.path, entry.branch, main) != "" {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tunsafe\n", name, entry.path, entry.branch)
		return
	}
	// A clone whose origin no longer matches the parent's is exactly
	// what 'cleanup' refuses (exit 9): 'list' must never call that
	// 'clean', the one state that promises cleanup would succeed.
	if cloneOriginMismatchReason(main, entry.path) != "" {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tunsafe\n", name, entry.path, entry.branch)
		return
	}
	if branch, _ := gitOutput(entry.path, "branch", "--show-current"); branch != "" && branch != entry.branch {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tmoved\n", name, entry.path, entry.branch)
		return
	}
	fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", name, entry.path, entry.branch, cloneState(entry.path, entry.branch))
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
	repo, parentRepo, entry, isClone := resolveCloneOrRepo("")
	if isClone {
		lock := requireExclusiveLedgerLock(parentRepo, entry)
		defer lock.Release()
		enableCloneGitEnv()
		requireCloneConfigSafe(repo, entry.branch, parentRepo)
	}
	protectionRepo := repo
	if isClone {
		protectionRepo = parentRepo
	}
	found, isProtected := lookupProtection(protectionRepo)
	requireCanonicalCheckout(found)

	if isClone {
		requireCloneOriginMatches(parentRepo, repo)
	}

	branch := currentBranch(repo)

	// Only the ledger's branch is trusted for a clone: a worker can rewrite the
	// clone-owned handoff file, not the parent's ledger. Legacy worktrees keep the handoff check.
	if isClone {
		if branch != entry.branch {
			die(3, "clone '%s' now has branch '%s' checked out, but its ledger lists branch '%s'; the checkout was moved after begin ran, so record refuses to commit into the wrong branch", repo, branch, entry.branch)
		}
	} else {
		gitDir, ok := gitOutput(repo, "rev-parse", "--path-format=absolute", "--git-dir")
		if !ok {
			exit(1)
		}
		if handoff, err := os.ReadFile(filepath.Join(gitDir, "allod-change-branch")); err == nil {
			if expected := strings.TrimSpace(string(handoff)); expected != branch {
				die(3, "checkout '%s' now has branch '%s' checked out, but 'allod change begin' handed out branch '%s'; the checkout was moved after begin ran, so record refuses to commit into the wrong branch", repo, branch, expected)
			}
		}
	}

	if isProtected && found.Covers(branch) {
		die(2, "refusing to commit directly to protected branch '%s'; run 'allod change begin' first", branch)
	}

	// A clone and a linked worktree both belong to one agent; only a shared main checkout can hold another's edits.
	if len(files) == 0 && !isClone && mainRepoDir(repo) == repo {
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
		var result unpushedResult
		var detail string
		if isClone {
			result, detail = unpushedCommitsForClone(repo, entry.branch)
		} else {
			result, detail = unpushedCommits(repo)
		}
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
	repo, parentRepo, entry, isClone := resolveCloneOrRepo("")
	protectionRepo := repo
	if isClone {
		lock := requireExclusiveLedgerLock(parentRepo, entry)
		defer lock.Release()
		enableCloneGitEnv()
		requireCloneConfigSafe(repo, entry.branch, parentRepo)
		protectionRepo = parentRepo
		requireCloneOriginMatches(parentRepo, repo)
	}
	found, _ := lookupProtection(protectionRepo)
	requireCanonicalCheckout(found)
	branch := currentBranch(repo)
	// Same family as record's moved-checkout refusal: the ledger's branch is
	// the only one trusted for a clone, checked before it is read for the PR.
	if isClone && branch != entry.branch {
		die(3, "clone '%s' now has branch '%s' checked out, but its ledger lists branch '%s'; the checkout was moved after begin ran, so submit refuses to open a PR from the wrong branch", repo, branch, entry.branch)
	}
	if base == "" {
		// A clone's own refs/remotes/origin/HEAD is worker-writable, so
		// the PR base is resolved against the parent, never the clone.
		baseRepo := repo
		if isClone {
			baseRepo = parentRepo
		}
		resolved, ok := defaultRemoteBranch(baseRepo)
		if !ok {
			die(1, "could not resolve the default branch of '%s' to use as the PR base; run: git -C %s remote set-head origin -a, or pass --base", baseRepo, baseRepo)
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
func cleanupClone(parentRepo string, entry changeLedgerEntry) {
	lock := requireExclusiveLedgerLock(parentRepo, entry)
	defer lock.Release()
	enableCloneGitEnv()
	requireCloneConfigSafe(entry.path, entry.branch, parentRepo)
	requireCloneOriginMatches(parentRepo, entry.path)
	// Judge and delete only the ledger's branch: a worker could check out a clean
	// pushed branch over it and let cleanup discard the ledgered branch's commits.
	if branch, _ := gitOutput(entry.path, "branch", "--show-current"); branch != entry.branch {
		die(3, "clone '%s' has branch '%s' checked out, but its ledger lists branch '%s'; cleanup refuses to judge or discard commits on the wrong branch", entry.path, branch, entry.branch)
	}
	dirty, dirtyStatus := captureCommand(entry.path, nil, true, "git", "status", "--porcelain")
	if dirtyStatus != 0 {
		die(1, "could not check whether clone '%s' is dirty: %s", entry.path, dirty)
	}
	if dirty != "" {
		die(1, "clone '%s' is dirty; clean it before cleanup", entry.path)
	}
	// Fetch before judging: the comparison below needs the remote's objects, and
	// a rebase-merge plus branch deletion is what an unfetched clone cannot see.
	// A stale local ref cannot make the answer wrong; a missing object can.
	if output, status := captureCommand(entry.path, nil, true, "git", "fetch", "--prune", "origin"); status != 0 {
		die(1, "could not fetch origin in clone '%s': %s", entry.path, output)
	}
	result, detail := unpushedCommitsForClone(entry.path, entry.branch)
	switch result {
	case unpushedYes:
		die(1, "clone '%s' has unpushed commits; push or handle them before cleanup", entry.path)
	case unpushedUnknown:
		die(1, "cannot determine whether clone '%s' has unpushed commits: %s; inspect it before deliberately removing it with standard git", entry.path, detail)
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
