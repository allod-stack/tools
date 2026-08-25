package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
	case "-h", "--help":
		fmt.Fprint(stdout, changeUsageText)
	default:
		die(1, "unknown change command: %s", command)
	}
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
	return gitQuiet("", "check-ref-format", "--branch", "agent/"+description)
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

func rollbackWorktree(repo, path, branch string) {
	_ = os.RemoveAll(path)
	_ = runCommand(repo, nil, io.Discard, io.Discard, "git", "worktree", "prune")
	listing, _ := gitOutput(repo, "worktree", "list", "--porcelain")
	needle := "\nworktree " + path + "\n"
	if strings.Contains("\n"+listing+"\n", needle) {
		fmt.Fprintf(stderr, "allod: worktree %s is still registered; run: git -C %s worktree prune\n", path, repo)
	}
	fmt.Fprintf(stderr, "allod: branch %s may have been left behind; check: git -C %s branch --list 'agent/*'\n", branch, repo)
}

func changeBegin(args []string) {
	description, repoArg := "", ""
	descriptionSet := false
	for len(args) > 0 {
		switch args[0] {
		case "-d":
			requireValue(args, args[0])
			description, descriptionSet = args[1], true
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
	protected, isProtected := protectedBranch(repo)
	if !descriptionSet {
		if isProtected {
			die(1, "change begin requires -d <description> to branch in protected repo '%s'", repo)
		}
		fmt.Fprintln(stdout, repo)
		return
	}
	if !validDescription(description) {
		die(1, "invalid description '%s'; use only letters, numbers, '.', '_', and '-'", description)
	}
	branch := "agent/" + description
	if !gitQuiet(repo, "remote", "get-url", "origin") {
		die(1, "repository '%s' has no 'origin' remote; an isolated change needs one", repo)
	}
	base := protected
	if !isProtected {
		if !gitQuiet(repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD") {
			die(1, "could not resolve the default branch of '%s'; run: git -C %s remote set-head origin -a", repo, repo)
		}
		base = defaultRemoteBranch(repo)
	}
	if base == "" {
		die(1, "could not resolve the default branch of '%s'; run: git -C %s remote set-head origin -a", repo, repo)
	}
	if localRefExists(repo, branch) {
		die(5, "branch '%s' already exists locally; use a different -d or clean it up", branch)
	}
	if remoteRefExists(repo, branch) {
		die(5, "branch '%s' already exists on origin; use a different -d or clean it up", branch)
	}
	if output, status := captureCommand(repo, nil, true, "git", "fetch", "origin"); status != 0 {
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
		exit(status)
	}
	if !gitQuiet(repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+base+"^{commit}") {
		die(1, "'origin/%s' does not exist in '%s' after fetching origin; check the branch name", base, repo)
	}

	changes := filepath.Join(homeDir(), "changes")
	if err := os.MkdirAll(changes, 0755); err != nil {
		die(1, "could not create changes directory: %s", changes)
	}
	path, err := makeTempDir(changes, repoSlug(repo)+"-"+description+"-", 6)
	if err != nil {
		die(1, "could not create worktree directory")
	}
	if output, status := captureCommand(repo, nil, true, "git", "worktree", "add", path, "-b", branch, "origin/"+base); status != 0 {
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
		rollbackWorktree(repo, path, branch)
		exit(status)
	}
	gitDir, ok := gitOutput(path, "rev-parse", "--path-format=absolute", "--git-dir")
	if !ok || os.WriteFile(filepath.Join(gitDir, "allod-change-branch"), []byte(branch+"\n"), 0644) != nil {
		fmt.Fprintf(stderr, "allod: could not write the branch handoff file for %s\n", path)
		rollbackWorktree(repo, path, branch)
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

func hasPopulatedSubmodule(path string) bool {
	output, status := captureCommand(path, nil, false, "git", "ls-files", "--stage", "-z")
	if status != 0 {
		return false
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
			return true
		}
	}
	return false
}

type unpushedResult int

const (
	unpushedYes unpushedResult = iota
	unpushedNo
	unpushedUnknown
)

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
		defaultBranch := defaultRemoteBranch(dir)
		if defaultBranch == "" {
			return unpushedUnknown, "the default branch of origin cannot be resolved"
		}
		if !gitQuiet(dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+defaultBranch+"^{commit}") {
			return unpushedUnknown, "origin/" + defaultBranch + " cannot be resolved"
		}
		base = "refs/remotes/origin/" + defaultBranch
	} else {
		return unpushedUnknown, "the repository has no origin remote"
	}
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
	if hasPopulatedSubmodule(path) {
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

func listRepo(repoDir string) {
	main := mainRepoDir(repoDir)
	name := main
	wd := strings.TrimRight(workDir(), "/")
	if rel, err := filepath.Rel(wd, main); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		name = filepath.ToSlash(rel)
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
		listRepo(resolveGitRepo(repoArg))
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
	repo := resolveGitRepo("")
	branch := currentBranch(repo)
	if protected, ok := protectedBranch(repo); ok && branch == protected {
		die(2, "refusing to commit directly to protected branch '%s'; run 'allod change begin' first", branch)
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
	repo := resolveGitRepo("")
	branch := currentBranch(repo)
	if base == "" {
		base = defaultRemoteBranch(repo)
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

func changeCleanup(args []string) {
	if len(args) != 1 || args[0] == "" {
		die(1, "usage: allod change cleanup <worktree-path>")
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
