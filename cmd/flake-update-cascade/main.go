// Command flake-update-cascade updates named flake inputs across every
// repository in the workspace that pins them directly, with one combined
// update, commit, and optional PR per repository.
//
// Repositories are processed in dependency order — a repository after every
// workspace repository it pins — and a pin of a workspace repository that is
// behind its branch head is moved along with the named inputs, so a commit
// the run pushes upstream reaches every downstream lock in the same run
// (allod/tools#171).
//
// A failure the program does not guard — a restore that fails after a failed
// update, say — ends the run with the failing command's status, via mustGit,
// rather than carrying on with a checkout in an unknown state.
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

Repos are processed in dependency order, each after the workspace repos it
pins, and a pin of a workspace repo that is behind that repo's branch head
is updated too, so one run carries an upstream lock commit downstream.

Modes:
  (default)   Commit directly to the default branch. Skips repos whose
              default branch is in ~/.config/git/protected-branches.
  --pr        Create/update a PR branch instead of committing directly.
              Works for all eligible repos including protected ones. A repo
              pinning one that moved only on a PR branch waits for that PR;
              re-run the same command after it merges.
  --dry-run   Show what would change without modifying anything.

Examples:
  flake-update-cascade vm
  flake-update-cascade nixpkgs vm
  flake-update-cascade nixpkgs vm --pr
  flake-update-cascade allod-tools --dry-run
`

// updateTimeout bounds one lock-writing nix command: SIGTERM on expiry, then
// wait for nix to exit.
const updateTimeout = 120 * time.Second

var inputNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// exitStatus carries an exit code out of the run through a panic, so that a
// failure deep inside repository processing ends the program without
// threading a status through every helper.
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

// parseArgs makes a single pass over the arguments: options may
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
		heads:         make(map[string]string),
		pushed:        make(map[string]pushedHead),
		defaults:      make(map[string]string),
		protected:     make(map[string]bool),
		prMoved:       make(map[string]string),
		waiting:       make(map[string][]blocker),
		wouldMove:     make(map[string]bool),
	}
	c.repos = collectRepos(c.workDir)
	return c.runCombinedCycle()
}

func homeDir() string { return os.Getenv("HOME") }

// workDir is WORK_DIR from the environment as given, or $HOME/work. A trailing
// slash is stripped only in repository discovery; repository paths are joined
// to the raw value.
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
	skipNoInput  repoStatus = "skip:no-input"        // no requested input has a reachable direct pin, and no workspace pin
	skipActivePR repoStatus = "skip:active-pr"
	skipProtect  repoStatus = "skip:protected" // default branch is protected (direct mode only)
	eligible     repoStatus = "eligible"
	preflightErr repoStatus = "error"
)

// A blocker is a PR this run pushed a lock commit to, named with the
// repository it belongs to, that a downstream repository cannot pin until it
// merges.
type blocker struct {
	pr, repo string
}

type cascade struct {
	options
	workDir       string
	activePRFile  string
	allowedFile   string
	protectedFile string
	inputLabel    string
	inputSlug     string
	repos         []string // in directory order
	order         []string // in dependency order

	status     map[string]repoStatus
	errorMsg   map[string]string
	remote     map[string]string
	errorRepos []string

	// identity is each repository's origin reduced to what names the
	// repository, and byIdentity the inverse, for matching a pin's URL to
	// the workspace repository it names. Only repositories inside the push
	// boundary are entered: a pin of anything else is external.
	identity   map[string]string
	byIdentity map[string][]string
	// pins holds each repository's workspace pins as read before anything
	// is pulled: the graph the order is built from. defaults caches each
	// repository's default branch, and protected says whether that branch
	// is listed in protected-branches, in every mode.
	pins      map[string][]pin
	defaults  map[string]string
	protected map[string]bool

	// heads caches the branch heads read this run, keyed by URL and ref; an
	// empty value records a failed read so it is not retried. pushed holds,
	// per repository this run pushed a default branch of, what it pushed.
	heads  map[string]string
	pushed map[string]pushedHead

	// prMoved records, per repository this run pushed a lock commit for on a
	// PR branch, the PR carrying it; waiting records the PRs each deferred
	// repository is waiting on, in waitingOrder. wouldMove is the dry-run
	// counterpart of both: the repositories a real run would push to.
	prMoved      map[string]string
	waiting      map[string][]blocker
	waitingOrder []string
	wouldMove    map[string]bool
	pendingRepos []string // dry-run repositories with a pin that could not be shown

	// lockFile is the per-repository exclusion lock, held on one descriptor
	// that each repository reopens, so at most one repository is locked at a
	// time and the previous lock is released when the next eligible
	// repository is reached.
	lockFile *os.File
}

func say(format string, args ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}

func (c *cascade) repoDir(repo string) string {
	return c.workDir + "/" + repo
}

// readLock parses a repository's flake.lock; a malformed lock ends the run
// with the file named.
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

// preflight classifies every repository before anything is touched, and
// decides the order they are processed in.
func (c *cascade) preflight() {
	c.status = make(map[string]repoStatus)
	c.errorMsg = make(map[string]string)
	c.remote = make(map[string]string)
	c.identity = make(map[string]string)
	c.byIdentity = make(map[string][]string)
	c.pins = make(map[string][]pin)

	// Every repository's origin is read first, so a pin can be matched to
	// the repository it names before any lock is classified. The remote
	// check runs in every mode: a repository whose origin is outside the
	// push boundary is left alone before anything else about it is read,
	// and it is not a repository a pin can name either.
	for _, repo := range c.repos {
		dir := c.repoDir(repo)
		if !isDir(dir) {
			c.status[repo] = skipSilent
			continue
		}
		remoteURL, _ := gitCapture(dir, "remote", "get-url", "origin")
		c.remote[repo] = remoteURL
		if remoteURL == "" || !c.remoteIsAllowed(remoteURL) {
			continue
		}
		if id, ok := repoIdentity(remoteURL); ok {
			c.identity[repo] = id
			c.byIdentity[id] = append(c.byIdentity[id], repo)
		}
	}

	for _, repo := range c.repos {
		if c.status[repo] == skipSilent {
			continue
		}
		dir := c.repoDir(repo)

		if !isFile(dir + "/flake.lock") {
			c.status[repo] = skipNoLock
			continue
		}
		remoteURL := c.remote[repo]
		if remoteURL == "" {
			c.status[repo] = skipNoOrigin
			continue
		}
		if !c.remoteIsAllowed(remoteURL) {
			c.status[repo] = skipExternal
			continue
		}

		lock := readLock(dir + "/flake.lock")
		c.pins[repo] = c.workspacePins(lock)
		if len(c.updateSet(lock)) == 0 {
			c.status[repo] = skipNoInput
			continue
		}

		if fileHasLine(c.activePRFile, repo) {
			c.status[repo] = skipActivePR
			continue
		}

		defaultBranch := c.defaultBranchOf(repo)
		c.protected[repo] = fileHasLine(c.protectedFile, "work/"+repo+" "+defaultBranch)
		if !c.prMode && !c.dryRun && c.protected[repo] {
			c.status[repo] = skipProtect
			continue
		}

		var repoErrors []string

		currentBranch, _ := gitCapture(dir, "branch", "--show-current")
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
			c.addError(repo, strings.Join(repoErrors, ","))
		} else {
			c.status[repo] = eligible
		}
	}

	// The order: every repository after the ones it pins, directory order
	// among the rest. Only repositories the run goes on to process are
	// ordered; a skipped one is never committed to, so a cycle through it
	// constrains nothing, and a repository pinning itself is left to nix.
	// Repositories that pin each other have no order, and the run stops
	// here naming them.
	deps := make(map[string][]string)
	for _, repo := range c.repos {
		if c.status[repo] != eligible && c.status[repo] != preflightErr {
			continue
		}
		for _, p := range c.pins[repo] {
			if p.target != repo {
				deps[repo] = append(deps[repo], p.target)
			}
		}
	}
	order, cycle := dependencyOrder(c.repos, deps)
	for _, repo := range cycle {
		c.addError(repo, cycleMessage(cycle, repo))
	}
	c.order = order
}

// defaultBranchOf is defaultBranch for a repository, asked of git once.
func (c *cascade) defaultBranchOf(repo string) string {
	branch, ok := c.defaults[repo]
	if !ok {
		branch = defaultBranch(c.repoDir(repo))
		c.defaults[repo] = branch
	}
	return branch
}

// addError records a preflight error against a repository, joining it to any
// the repository already has the way the per-repository checks do.
func (c *cascade) addError(repo, msg string) {
	if c.status[repo] == preflightErr {
		c.errorMsg[repo] += "," + msg
		return
	}
	c.status[repo] = preflightErr
	c.errorMsg[repo] = msg
	c.errorRepos = append(c.errorRepos, repo)
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

// An outcome is what processing one repository came to: whether it succeeded,
// whether it produced a lock commit downstream repositories should see — one
// pushed to the default branch, one on a PR branch, or one a dry run would
// push — and in PR mode which PR carries it.
type outcome struct {
	ok    bool
	moved bool
	pr    string
}

func (c *cascade) execute() int {
	overallExit := 0
	first := true

	for _, repo := range c.order {
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
			say("  protected branch (%s) — re-run with --pr to create a PR", c.defaultBranchOf(repo))
			continue
		}

		repoSlug := strings.ReplaceAll(repo, "/", "_")
		if !c.acquireLock("/tmp/flake-update-cascade-" + repoSlug + ".lock") {
			say("  another instance is running for %s, skipping", repo)
			overallExit = 1
			continue
		}

		defaultBranch := c.defaultBranchOf(repo)

		say("  pulling...")
		if !gitInherit(dir, "pull") {
			say("  pull failed, skipping")
			overallExit = 1
			continue
		}

		// The pull may have changed the input graph, so the checkout is
		// judged on the lock it has now, not the one preflight classified
		// (allod/tools#175).
		lock := readLock(dir + "/flake.lock")
		updatePaths := c.updateSet(lock)
		if len(updatePaths) == 0 {
			say("  no directly pinned %s input found, skipping", c.inputLabel)
			continue
		}

		pins := c.workspacePins(lock)

		// A repository pinning one this run moved only on a PR branch cannot
		// pin that commit until the PR merges; it waits, with nothing
		// written, and the same command re-run after the merge continues
		// from here.
		if c.prMode {
			if blockers := c.blockers(pins); len(blockers) > 0 {
				for _, b := range blockers {
					say("  waiting on %s (%s)", b.pr, b.repo)
				}
				c.waiting[repo] = blockers
				c.waitingOrder = append(c.waitingOrder, repo)
				continue
			}
		}

		// A dry run of a direct run shows a protected repository's update,
		// as it always has, but says the real run skips it, and promises
		// nothing downstream on its account.
		skippedByDirect := c.dryRun && !c.prMode && c.protected[repo]
		if skippedByDirect {
			say("  protected branch (%s) — a direct run skips this repository; re-run with --pr", defaultBranch)
		}

		// A dry run cannot show the pin a downstream repository would take
		// from a commit this run would push, because nothing is pushed; it
		// says so and shows the rest.
		pending := false
		if c.dryRun {
			for _, p := range pins {
				if p.onDefault && c.wouldMove[p.target] {
					say("  %s: %s moves in this run; a dry run cannot show the revision it will pin", p.input, p.target)
					pending = true
				}
			}
			if pending {
				c.pendingRepos = append(c.pendingRepos, repo)
			}
		}

		// Read the branch head behind each path and keep only what moved, so
		// a repository that is already current costs no nix call at all.
		plan := planLock(lock, updatePaths, c.resolveHead)
		for _, u := range plan.unresolved {
			say("  %s: could not resolve %s at %s; asking nix instead", u.path, u.ref, u.url)
		}
		if plan.empty() {
			if pending {
				say("  no other changes (dry-run)")
				c.wouldMove[repo] = !skippedByDirect
			} else {
				say("  already up to date")
			}
			continue
		}

		var result outcome
		switch {
		case c.dryRun:
			result = c.dryRunRepo(dir, repoSlug, updatePaths, plan)
		case c.prMode:
			result = c.prRepo(dir, repoSlug, defaultBranch, updatePaths, plan)
		default:
			result = c.directRepo(dir, repoSlug, updatePaths, plan)
		}
		if !result.ok {
			overallExit = 1
		}
		switch {
		case c.dryRun:
			if (result.moved || pending) && !skippedByDirect {
				c.wouldMove[repo] = true
			}
		case c.prMode:
			if result.moved {
				c.prMoved[repo] = result.pr
			}
		default:
			// The push moved the head every downstream pin of this branch
			// reads; what it moved to is known here without asking.
			if result.moved {
				c.recordPush(dir, repo, defaultBranch)
			}
		}
	}

	c.summarize()
	return overallExit
}

// blockers lists the PRs a repository with the given workspace pins waits
// on: one for each pin of a default branch whose repository this run moved on
// a PR branch, and the ones each waiting target is itself waiting on, since a
// repository is processed after every repository it pins.
func (c *cascade) blockers(pins []pin) []blocker {
	var blockers []blocker
	seen := make(map[blocker]bool)
	add := func(b blocker) {
		if !seen[b] {
			seen[b] = true
			blockers = append(blockers, b)
		}
	}
	for _, p := range pins {
		if !p.onDefault {
			continue
		}
		if pr, ok := c.prMoved[p.target]; ok {
			add(blocker{pr: pr, repo: p.target})
		}
		for _, b := range c.waiting[p.target] {
			add(b)
		}
	}
	return blockers
}

// summarize ends the run with what it could not finish: in PR mode the
// repositories waiting on unmerged PRs, in a dry run the repositories whose
// pins of commits this run would push could not be shown. Neither is a
// failure.
func (c *cascade) summarize() {
	if len(c.waitingOrder) > 0 {
		say("")
		say("Waiting on unmerged PRs:")
		for _, repo := range c.waitingOrder {
			var prs []string
			for _, b := range c.waiting[repo] {
				prs = append(prs, b.pr+" ("+b.repo+")")
			}
			say("  %s: %s", repo, strings.Join(prs, ", "))
		}
		say("Re-run the same command after they merge to continue the cascade.")
	}
	if len(c.pendingRepos) > 0 {
		say("")
		say("Dry run: %s also take the commits above once they are pushed; a dry run cannot show the revisions they will pin.", strings.Join(c.pendingRepos, ", "))
	}
}

// acquireLock takes the per-repository exclusion lock, releasing whichever
// one the previous repository held. A lock another instance holds is
// reported as contention; a lock file that cannot be opened ends the run.
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

// reportRevisions prints each path whose revision differs between the two
// locks and returns those paths, in the order given.
func (c *cascade) reportRevisions(oldLock, newLock *flakelock.Lock, paths []string) []string {
	var moved []string
	for _, path := range paths {
		oldRev := oldLock.Rev(path)
		newRev := newLock.Rev(path)
		if oldRev != newRev {
			say("  %s: %s → %s", path, short(oldRev), short(newRev))
			moved = append(moved, path)
		}
	}
	return moved
}

func short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

func (c *cascade) dryRunRepo(dir, repoSlug string, updatePaths []string, plan lockPlan) outcome {
	tmpLock := "/tmp/" + repoSlug + ".flake.lock.new"

	say("  updating %s (dry-run)...", strings.Join(plan.paths, " "))
	if err := applyLock(dir, tmpLock, plan); err != nil {
		say("  %v, skipping", err)
		os.Remove(tmpLock)
		return outcome{}
	}

	if !nixQuiet("flake", "metadata", "--json", dir, "--reference-lock-file", tmpLock) {
		say("  broken evaluation with updated lock, skipping")
		os.Remove(tmpLock)
		return outcome{}
	}

	moved := c.reportRevisions(readLock(dir+"/flake.lock"), readLock(tmpLock), updatePaths)
	if len(moved) > 0 {
		say("  dry-run: no changes made")
	} else {
		say("  already up to date")
	}

	os.Remove(tmpLock)
	return outcome{ok: true, moved: len(moved) > 0}
}

// updateAndCheck is the shared front half of the two mutating modes: snapshot
// the lock, write the planned update, gate on evaluation, and report what
// moved. It returns the label naming what moved — the commit message and PR
// take it — the outcome so far, and whether the caller should go on to commit.
func (c *cascade) updateAndCheck(dir, repoSlug string, updatePaths []string, plan lockPlan) (label string, ok, proceed bool) {
	oldLock := "/tmp/" + repoSlug + ".flake.lock.old"
	copyFile(dir+"/flake.lock", oldLock)

	say("  updating %s...", strings.Join(plan.paths, " "))
	if err := applyLock(dir, "", plan); err != nil {
		say("  %v, skipping", err)
		mustGit(dir, "checkout", "--", "flake.lock")
		os.Remove(oldLock)
		return "", false, false
	}

	if !nixQuiet("flake", "metadata", "--json", dir) {
		say("  broken evaluation after update — reverting flake.lock")
		mustGit(dir, "checkout", "--", "flake.lock")
		os.Remove(oldLock)
		return "", false, false
	}

	if gitInherit(dir, "diff", "--quiet", "--", "flake.lock") {
		say("  already up to date")
		os.Remove(oldLock)
		return "", true, false
	}

	// The commit names the inputs that moved in this repository. When the
	// lock changed without any of them changing revision — nix rewrote
	// something else about a node — it names what nix was asked about.
	moved := c.reportRevisions(readLock(oldLock), readLock(dir+"/flake.lock"), updatePaths)
	if len(moved) == 0 {
		moved = plan.paths
	}
	os.Remove(oldLock)
	return strings.Join(moved, ", "), true, true
}

func (c *cascade) prRepo(dir, repoSlug, defaultBranch string, updatePaths []string, plan lockPlan) outcome {
	prBranch := "agent/flake-update-" + c.inputSlug

	label, ok, proceed := c.updateAndCheck(dir, repoSlug, updatePaths, plan)
	if !proceed {
		return outcome{ok: ok}
	}

	mustGit(dir, "add", "flake.lock")
	mustGit(dir, "checkout", "-B", prBranch)
	mustGit(dir, "commit", "-m", "flake.lock: update "+label)

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
		return outcome{}
	}

	// An origin URL with no owner/repo component ends the run here, after the
	// push and with the checkout left on the PR branch. No forge remote has
	// that shape, so the gentler path is not built.
	forgeRepo, ok := repoForgeName(dir)
	if !ok {
		fatal(1)
	}
	title := "flake.lock: update " + label
	body := "Automated flake.lock update for inputs `" + label + "`."
	var pr string
	existingPR, _ := capture(nil, "forge", "-R", forgeRepo, "pr", "find-by-head", prBranch)
	if existingPR == "" {
		prURL, created := capture(nil, "forge", "-R", forgeRepo, "pr", "create",
			"--title", title, "--head", prBranch, "--base", defaultBranch, "--body", body)
		if !created {
			// The branch is pushed but no PR carries it; nothing downstream
			// should wait on one.
			say("  PR creation failed")
			mustGit(dir, "checkout", defaultBranch)
			mustGit(dir, "checkout", "--", "flake.lock")
			return outcome{}
		}
		pr = prURL
		if prURL == "" {
			prURL = "PR created (could not fetch URL)"
			pr = "the PR for " + prBranch
		}
		say("  %s", prURL)
	} else {
		// The commit just pushed names what moved this time, which may
		// differ from what the PR was opened for.
		if _, edited := capture(nil, "forge", "-R", forgeRepo, "pr", "edit", existingPR, "--title", title, "--body", body); edited {
			say("  PR #%s updated", existingPR)
		} else {
			say("  PR #%s updated; its title could not be refreshed", existingPR)
		}
		pr = "PR #" + existingPR
	}

	mustGit(dir, "checkout", defaultBranch)
	mustGit(dir, "checkout", "--", "flake.lock")
	return outcome{ok: true, moved: true, pr: pr}
}

func (c *cascade) directRepo(dir, repoSlug string, updatePaths []string, plan lockPlan) outcome {
	label, ok, proceed := c.updateAndCheck(dir, repoSlug, updatePaths, plan)
	if !proceed {
		return outcome{ok: ok}
	}

	mustGit(dir, "add", "flake.lock")
	if !gitInherit(dir, "commit", "-m", "flake.lock: update "+label) {
		say("  commit failed (hook blocked?) — reverting")
		if !gitRun(dir, os.Stdin, os.Stdout, nil, "restore", "--staged", "flake.lock") {
			mustGit(dir, "reset", "HEAD", "flake.lock")
		}
		mustGit(dir, "checkout", "--", "flake.lock")
		return outcome{}
	}

	if !gitInherit(dir, "push") {
		say("  push failed")
		return outcome{}
	}

	say("  committed and pushed")
	return outcome{ok: true, moved: true}
}

// copyFile copies the lock aside; a failure ends the run.
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
// status on failure.
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

// nixWrite runs one lock-writing nix command, `flake lock` or `flake update`,
// with stdin from /dev/null, so nix declines a foreign nixConfig instead of
// prompting, bounded by updateTimeout. The child stays in this process group,
// so a terminal Ctrl-C reaches nix directly (allod/tools#143).
func nixWrite(args ...string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nix", args...)
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
