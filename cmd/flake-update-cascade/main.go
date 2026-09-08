// Command flake-update-cascade updates named flake inputs across every
// repository in the workspace that pins them directly, with one combined
// update, commit, and optional PR per repository.
//
// This is a port of flake/flake-update-cascade. Until that Bash program is
// retired under allod/tools#159 it is the oracle: every suite under
// tests/flake runs against both, and the port reproduces the oracle's output,
// exit status, subprocess trace, and on-disk effects. That includes behavior
// the Bash owes to `set -e`: a failure that the oracle does not guard ends the
// run with the failing command's status, and the port does the same via
// mustRun. Repository processing order is the directory walk of
// lib/workspace.sh; dependency ordering is allod/tools#171, after this port.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"forge.anarch.diy/allod/tools/internal/flakelock"
)

const usageText = `Usage: flake-update-cascade <input-name>... [--dry-run] [--pr]

Update flake.lock for <input-name> across all repos that use them.
Each repo receives one combined update, commit, and optional PR.

Modes:
  (default)   Commit directly to the default branch. Skips repos whose
              default branch is in ~/.config/git/protected-branches.
  --pr        Create/update a PR branch instead of committing directly.
              Works for all eligible repos including protected ones.
  --dry-run   Show what would change without modifying anything.

Examples:
  flake-update-cascade vm
  flake-update-cascade nixpkgs vm
  flake-update-cascade nixpkgs vm --pr
  flake-update-cascade allod-tools --dry-run
`

// updateTimeout bounds one `nix flake update`, as `timeout --foreground 120`
// does in the oracle: SIGTERM on expiry, then wait for nix to exit.
const updateTimeout = 120 * time.Second

var inputNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// exitStatus carries an exit code out of the run through a panic, so that a
// failure deep inside repository processing ends the program the way the
// oracle's `set -e` does, without threading a status through every helper.
type exitStatus int

func fatal(code int) { panic(exitStatus(code)) }

func main() {
	code := 0
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				status, ok := recovered.(exitStatus)
				if !ok {
					panic(recovered)
				}
				code = int(status)
			}
		}()
		code = run(os.Args[1:])
	}()
	os.Exit(code)
}

type options struct {
	dryRun bool
	prMode bool
	names  []string
}

// parseArgs mirrors the oracle's single pass over the arguments: options may
// sit anywhere, -h wins as soon as it is seen, and the first unknown option
// ends the run. It returns the exit code to use when parsing ends the run.
func parseArgs(args []string) (opts options, code int, done bool) {
	for _, arg := range args {
		switch {
		case arg == "--dry-run":
			opts.dryRun = true
		case arg == "--pr":
			opts.prMode = true
		case arg == "-h" || arg == "--help":
			fmt.Fprint(os.Stdout, usageText)
			return opts, 0, true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "flake-update-cascade: unknown option: %s\n", arg)
			fmt.Fprint(os.Stderr, usageText)
			return opts, 1, true
		default:
			opts.names = append(opts.names, arg)
		}
	}
	return opts, 0, false
}

func run(args []string) int {
	opts, code, done := parseArgs(args)
	if done {
		return code
	}

	// Startup checks.
	if len(opts.names) == 0 {
		fmt.Fprintln(os.Stderr, "flake-update-cascade: missing required argument: <input-name>...")
		fmt.Fprint(os.Stderr, usageText)
		return 1
	}
	for _, name := range opts.names {
		if !inputNamePattern.MatchString(name) {
			fmt.Fprintf(os.Stderr, "flake-update-cascade: invalid input name '%s'\n", name)
			fmt.Fprintln(os.Stderr, "  input name must match ^[a-zA-Z0-9_-]+$")
			return 1
		}
	}
	if _, err := exec.LookPath("nix"); err != nil {
		fmt.Fprintln(os.Stderr, "flake-update-cascade: 'nix' not found on PATH")
		return 1
	}
	if opts.prMode {
		if _, err := exec.LookPath("forge"); err != nil {
			fmt.Fprintln(os.Stderr, "flake-update-cascade: 'forge' not found on PATH (required for --pr mode)")
			return 1
		}
	}

	c := &cascade{
		options:       opts,
		workDir:       workDir(),
		activePRFile:  filepath.Join(homeDir(), ".config", "git", "active-pr-branches"),
		allowedFile:   filepath.Join(homeDir(), ".config", "git", "allowed-external-remotes"),
		protectedFile: filepath.Join(homeDir(), ".config", "git", "protected-branches"),
		inputLabel:    strings.Join(opts.names, ", "),
		inputSlug:     strings.Join(opts.names, "-"),
	}
	c.repos = collectRepos(c.workDir)
	return c.runCombinedCycle()
}

func homeDir() string { return os.Getenv("HOME") }

// workDir is the oracle's WORK_DIR: taken from the environment as given, or
// $HOME/work. A trailing slash is stripped only where the oracle strips it,
// in repository discovery; repository paths are joined to the raw value.
func workDir() string {
	if value := os.Getenv("WORK_DIR"); value != "" {
		return value
	}
	return homeDir() + "/work"
}

type repoStatus string

const (
	skipSilent   repoStatus = "skip:silent"  // dir does not exist (no output)
	skipNoLock   repoStatus = "skip:no-lock" // no flake.lock found
	skipNoOrigin repoStatus = "skip:no-origin"
	skipExternal repoStatus = "skip:external-remote" // origin outside the push boundary (every mode)
	skipNoInput  repoStatus = "skip:no-input"        // no requested input has a reachable direct pin
	skipActivePR repoStatus = "skip:active-pr"
	skipProtect  repoStatus = "skip:protected" // default branch is protected (direct mode only)
	eligible     repoStatus = "eligible"
	preflightErr repoStatus = "error"
)

type cascade struct {
	options
	workDir       string
	activePRFile  string
	allowedFile   string
	protectedFile string
	inputLabel    string
	inputSlug     string
	repos         []string

	status      map[string]repoStatus
	errorMsg    map[string]string
	updatePaths map[string][]string
	remote      map[string]string
	errorRepos  []string

	// lockFile is the per-repository exclusion lock. The oracle holds it on a
	// fixed descriptor that each `exec 9>` reopens, so at most one repository
	// is locked at a time and the previous lock is released when the next
	// eligible repository is reached.
	lockFile *os.File
}

func say(format string, args ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}

func (c *cascade) repoDir(repo string) string {
	return c.workDir + "/" + repo
}

// readLock parses a repository's flake.lock. The oracle hands a malformed lock
// to jq, whose parse error ends the run; the port ends it with the file named.
func readLock(path string) *flakelock.Lock {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flake-update-cascade: %v\n", err)
		fatal(1)
	}
	lock, err := flakelock.Parse(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flake-update-cascade: %s: %v\n", path, err)
		fatal(1)
	}
	return lock
}

// remoteIsAllowed is the push boundary the protected-refs-policy hook enforces
// on push: the forge is always allowed, anything else needs a substring
// pattern in allowed-external-remotes. The hook stops a push; this stops the
// cascade from pulling, updating or committing such a repository at all.
func (c *cascade) remoteIsAllowed(remoteURL string) bool {
	if strings.Contains(remoteURL, "forge.anarch.diy") {
		return true
	}
	file, err := os.Open(c.allowedFile)
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		pattern := scanner.Text()
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		if strings.Contains(remoteURL, pattern) {
			return true
		}
	}
	return false
}

func fileHasLine(path, wanted string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if scanner.Text() == wanted {
			return true
		}
	}
	return false
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (c *cascade) runCombinedCycle() int {
	c.preflight()
	if len(c.errorRepos) > 0 {
		say("")
		say("Pre-flight checks failed — no changes made.")
		say("")
		for _, repo := range c.errorRepos {
			say("  %s: %s", repo, c.errorMsg[repo])
		}
		say("")
		say("Resolve the above issues and re-run.")
		return 1
	}
	return c.execute()
}

// preflight classifies every repository before anything is touched.
func (c *cascade) preflight() {
	c.status = make(map[string]repoStatus)
	c.errorMsg = make(map[string]string)
	c.updatePaths = make(map[string][]string)
	c.remote = make(map[string]string)

	for _, repo := range c.repos {
		dir := c.repoDir(repo)

		if !isDir(dir) {
			c.status[repo] = skipSilent
			continue
		}
		if !isFile(dir + "/flake.lock") {
			c.status[repo] = skipNoLock
			continue
		}

		// The remote check runs in every mode: a repository whose origin is
		// outside the push boundary is reported and left alone before anything
		// else about it is read.
		remoteURL, _ := gitCapture(dir, "remote", "get-url", "origin")
		if remoteURL == "" {
			c.status[repo] = skipNoOrigin
			continue
		}
		if !c.remoteIsAllowed(remoteURL) {
			c.status[repo] = skipExternal
			c.remote[repo] = remoteURL
			continue
		}

		paths := readLock(dir + "/flake.lock").UpdatePaths(c.names)
		if len(paths) == 0 {
			c.status[repo] = skipNoInput
			continue
		}
		c.updatePaths[repo] = paths

		if fileHasLine(c.activePRFile, repo) {
			c.status[repo] = skipActivePR
			continue
		}

		if !c.prMode && !c.dryRun {
			defaultBranch := defaultBranch(dir)
			if fileHasLine(c.protectedFile, "work/"+repo+" "+defaultBranch) {
				c.status[repo] = skipProtect
				continue
			}
		}

		var repoErrors []string

		currentBranch, _ := gitCapture(dir, "branch", "--show-current")
		defaultBranch := defaultBranch(dir)
		if currentBranch == "" || currentBranch != defaultBranch {
			shown := currentBranch
			if shown == "" {
				shown = "detached"
			}
			repoErrors = append(repoErrors, fmt.Sprintf("not on default branch (on '%s', expected '%s')", shown, defaultBranch))
		}

		if !gitQuiet(dir, "diff", "--quiet") {
			repoErrors = append(repoErrors, "dirty working tree (unstaged changes)")
		} else if !gitQuiet(dir, "diff", "--cached", "--quiet") {
			repoErrors = append(repoErrors, "dirty working tree (staged changes)")
		}

		if gitQuiet(dir, "rev-parse", "@{u}") {
			unpushed, ok := gitCapture(dir, "rev-list", "HEAD...@{u}", "--count")
			if !ok {
				unpushed = "0"
			}
			if count := atoi(unpushed); count > 0 {
				if count == 1 {
					repoErrors = append(repoErrors, "1 unpushed commit")
				} else {
					repoErrors = append(repoErrors, fmt.Sprintf("%d unpushed commits", count))
				}
			}
		}

		if len(repoErrors) > 0 {
			c.status[repo] = preflightErr
			c.errorMsg[repo] = strings.Join(repoErrors, ", ")
			c.errorRepos = append(c.errorRepos, repo)
		} else {
			c.status[repo] = eligible
		}
	}
}

// atoi reads a count the way `[[ $n -gt 0 ]]` does for the values git
// prints: a non-number counts as zero.
func atoi(text string) int {
	n := 0
	for _, r := range strings.TrimSpace(text) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func (c *cascade) execute() int {
	overallExit := 0
	first := true

	for _, repo := range c.repos {
		dir := c.repoDir(repo)
		status, ok := c.status[repo]
		if !ok {
			status = skipSilent
		}
		if status == skipSilent {
			continue
		}

		if first {
			first = false
		} else {
			say("")
		}
		say("==> %s", repo)

		switch status {
		case skipNoLock:
			say("  no flake.lock, skipping")
			continue
		case skipNoInput:
			say("  no directly pinned %s input found, skipping", c.inputLabel)
			continue
		case skipNoOrigin:
			say("  no origin remote, skipping")
			continue
		case skipExternal:
			say("  origin %s is not the forge and not listed in ~/.config/git/allowed-external-remotes, skipping", c.remote[repo])
			continue
		case skipActivePR:
			say("  listed in active-pr-branches (GPG-signed commits required), skipping — handle manually")
			continue
		case skipProtect:
			say("  protected branch (%s) — re-run with --pr to create a PR", defaultBranch(dir))
			continue
		}

		repoSlug := strings.ReplaceAll(repo, "/", "_")
		if !c.acquireLock("/tmp/flake-update-cascade-" + repoSlug + ".lock") {
			say("  another instance is running for %s, skipping", repo)
			overallExit = 1
			continue
		}

		defaultBranch := defaultBranch(dir)
		updatePaths := c.updatePaths[repo]

		say("  pulling...")
		if !gitInherit(dir, "pull") {
			say("  pull failed, skipping")
			overallExit = 1
			continue
		}

		var succeeded bool
		switch {
		case c.dryRun:
			succeeded = c.dryRunRepo(dir, repoSlug, updatePaths)
		case c.prMode:
			succeeded = c.prRepo(dir, repoSlug, defaultBranch, updatePaths)
		default:
			succeeded = c.directRepo(dir, repoSlug, updatePaths)
		}
		if !succeeded {
			overallExit = 1
		}
	}

	return overallExit
}

// acquireLock takes the per-repository exclusion lock, releasing whichever
// one the previous repository held. A lock another instance holds is
// reported as contention; a lock file that cannot be opened ends the run,
// as a failed `exec 9>` does in the oracle.
func (c *cascade) acquireLock(path string) bool {
	if c.lockFile != nil {
		c.lockFile.Close()
		c.lockFile = nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flake-update-cascade: %s: %v\n", path, err)
		fatal(1)
	}
	c.lockFile = file
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	return err == nil
}

func (c *cascade) reportRevisions(oldLock, newLock *flakelock.Lock, paths []string) bool {
	changed := false
	for _, path := range paths {
		oldRev := oldLock.Rev(path)
		newRev := newLock.Rev(path)
		if oldRev != newRev {
			say("  %s: %s → %s", path, short(oldRev), short(newRev))
			changed = true
		}
	}
	return changed
}

func short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

func (c *cascade) dryRunRepo(dir, repoSlug string, updatePaths []string) bool {
	tmpLock := "/tmp/" + repoSlug + ".flake.lock.new"

	say("  updating %s (dry-run)...", strings.Join(updatePaths, " "))
	args := append(append([]string{}, updatePaths...), "--flake", dir, "--output-lock-file", tmpLock)
	if !nixFlakeUpdate(args) {
		say("  nix flake update failed, skipping")
		os.Remove(tmpLock)
		return false
	}

	if !nixQuiet("flake", "metadata", "--json", dir, "--reference-lock-file", tmpLock) {
		say("  broken evaluation with updated lock, skipping")
		os.Remove(tmpLock)
		return false
	}

	changed := c.reportRevisions(readLock(dir+"/flake.lock"), readLock(tmpLock), updatePaths)
	if changed {
		say("  dry-run: no changes made")
	} else {
		say("  already up to date")
	}

	os.Remove(tmpLock)
	return true
}

// updateAndCheck is the shared front half of the two mutating modes: snapshot
// the lock, run the combined update, gate on evaluation, and report what
// moved. It returns the outcome and whether the caller should go on to commit.
func (c *cascade) updateAndCheck(dir, repoSlug string, updatePaths []string) (ok, proceed bool) {
	oldLock := "/tmp/" + repoSlug + ".flake.lock.old"
	copyFile(dir+"/flake.lock", oldLock)

	say("  updating %s...", strings.Join(updatePaths, " "))
	args := append(append([]string{}, updatePaths...), "--flake", dir)
	if !nixFlakeUpdate(args) {
		say("  nix flake update failed, skipping")
		mustGit(dir, "checkout", "--", "flake.lock")
		os.Remove(oldLock)
		return false, false
	}

	if !nixQuiet("flake", "metadata", "--json", dir) {
		say("  broken evaluation after update — reverting flake.lock")
		mustGit(dir, "checkout", "--", "flake.lock")
		os.Remove(oldLock)
		return false, false
	}

	if gitInherit(dir, "diff", "--quiet", "--", "flake.lock") {
		say("  already up to date")
		os.Remove(oldLock)
		return true, false
	}

	c.reportRevisions(readLock(oldLock), readLock(dir+"/flake.lock"), updatePaths)
	os.Remove(oldLock)
	return true, true
}

func (c *cascade) prRepo(dir, repoSlug, defaultBranch string, updatePaths []string) bool {
	prBranch := "agent/flake-update-" + c.inputSlug

	ok, proceed := c.updateAndCheck(dir, repoSlug, updatePaths)
	if !proceed {
		return ok
	}

	mustGit(dir, "add", "flake.lock")
	mustGit(dir, "checkout", "-B", prBranch)
	mustGit(dir, "commit", "-m", "flake.lock: update "+c.inputLabel)

	// Refresh the tracking ref before force-pushing. If the branch was
	// deleted after a prior PR merge, the fetch fails and the stale local ref
	// is removed explicitly — otherwise --force-with-lease rejects the push
	// because the stale ref contradicts the (absent) remote.
	if !gitRun(dir, os.Stdin, os.Stdout, nil, "fetch", "origin", prBranch) {
		gitRun(dir, os.Stdin, os.Stdout, nil, "update-ref", "-d", "refs/remotes/origin/"+prBranch)
	}
	if !gitInherit(dir, "push", "--force-with-lease", "origin", prBranch) {
		say("  push failed")
		mustGit(dir, "checkout", defaultBranch)
		mustGit(dir, "checkout", "--", "flake.lock")
		return false
	}

	// The oracle's `forge_repo=$(repo_forge_name "$dir")` ends the run under
	// `set -e` when the origin URL yields no owner/repo, so its "could not
	// infer forge repo" branch is unreachable; the port stops at the same
	// point with the same status. That leaves the checkout on the PR branch
	// in both, which is a known bug carried for parity (allod/tools#159).
	forgeRepo, ok := repoForgeName(dir)
	if !ok {
		fatal(1)
	}
	existingPR, _ := capture(nil, "forge", "-R", forgeRepo, "pr", "find-by-head", prBranch)
	if existingPR == "" {
		prURL, _ := capture(nil, "forge", "-R", forgeRepo, "pr", "create",
			"--title", "flake.lock: update "+c.inputLabel,
			"--head", prBranch,
			"--base", defaultBranch,
			"--body", "Automated flake.lock update for inputs `"+c.inputLabel+"`.")
		if prURL == "" {
			prURL = "PR created (could not fetch URL)"
		}
		say("  %s", prURL)
	} else {
		say("  PR #%s updated", existingPR)
	}

	mustGit(dir, "checkout", defaultBranch)
	mustGit(dir, "checkout", "--", "flake.lock")
	return true
}

func (c *cascade) directRepo(dir, repoSlug string, updatePaths []string) bool {
	ok, proceed := c.updateAndCheck(dir, repoSlug, updatePaths)
	if !proceed {
		return ok
	}

	mustGit(dir, "add", "flake.lock")
	if !gitInherit(dir, "commit", "-m", "flake.lock: update "+c.inputLabel) {
		say("  commit failed (hook blocked?) — reverting")
		if !gitRun(dir, os.Stdin, os.Stdout, nil, "restore", "--staged", "flake.lock") {
			mustGit(dir, "reset", "HEAD", "flake.lock")
		}
		mustGit(dir, "checkout", "--", "flake.lock")
		return false
	}

	if !gitInherit(dir, "push") {
		say("  push failed")
		return false
	}

	say("  committed and pushed")
	return true
}

// copyFile is the oracle's `cp`, whose failure ends the run under `set -e`.
func copyFile(src, dst string) {
	data, err := os.ReadFile(src)
	if err == nil {
		err = os.WriteFile(dst, data, 0o644)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "flake-update-cascade: %v\n", err)
		fatal(1)
	}
}

// --- Subprocess boundaries: git, nix, forge. ---

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

// gitRun runs git -C dir with the given standard streams; a nil writer
// discards that stream.
func gitRun(dir string, stdin io.Reader, stdout, stderr io.Writer, args ...string) bool {
	return gitStatus(dir, stdin, stdout, stderr, args...) == 0
}

func gitStatus(dir string, stdin io.Reader, stdout, stderr io.Writer, args ...string) int {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return exitCode(cmd.Run())
}

// gitInherit runs git with the cascade's own streams.
func gitInherit(dir string, args ...string) bool {
	return gitRun(dir, os.Stdin, os.Stdout, os.Stderr, args...)
}

// gitQuiet runs git with every stream discarded.
func gitQuiet(dir string, args ...string) bool {
	return gitRun(dir, nil, nil, nil, args...)
}

// gitCapture returns git's stdout with trailing newlines removed, as `$(...)`
// does, with stderr discarded.
func gitCapture(dir string, args ...string) (string, bool) {
	return capture(nil, "git", append([]string{"-C", dir}, args...)...)
}

// mustGit runs git with inherited streams and ends the run with git's exit
// status on failure, as an unguarded command does under the oracle's `set -e`.
func mustGit(dir string, args ...string) {
	if status := gitStatus(dir, os.Stdin, os.Stdout, os.Stderr, args...); status != 0 {
		fatal(status)
	}
}

func capture(stdin io.Reader, name string, args ...string) (string, bool) {
	var out strings.Builder
	cmd := exec.Command(name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = &out
	err := cmd.Run()
	return strings.TrimRight(out.String(), "\n"), err == nil
}

// nixFlakeUpdate runs one combined update with stdin from /dev/null, so nix
// declines a foreign nixConfig instead of prompting, and under the same bound
// the oracle's `timeout --foreground` applies. The child stays in this
// process group, so a terminal Ctrl-C reaches nix directly (allod/tools#143).
func nixFlakeUpdate(args []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nix", append([]string{"flake", "update"}, args...)...)
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	return cmd.Run() == nil
}

// nixQuiet runs nix with its output discarded and stdin inherited.
func nixQuiet(args ...string) bool {
	cmd := exec.Command("nix", args...)
	cmd.Stdin = os.Stdin
	return cmd.Run() == nil
}
