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

func protectedBranch(dir string) (string, bool) {
	key, ok := repoLookupKey(dir)
	if !ok {
		return "", false
	}
	file, err := os.Open(filepath.Join(homeDir(), ".config", "git", "protected-branches"))
	if err != nil {
		return "", false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if len(fields) > 1 && fields[0] == key {
			return fields[1], true
		}
	}
	return "", false
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

func defaultRemoteBranch(dir string) string {
	ref, ok := gitOutput(dir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if !ok {
		return "master"
	}
	return strings.TrimPrefix(ref, "refs/remotes/origin/")
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

func delegatePM(args []string) {
	tools := strings.TrimRight(os.Getenv("ALLOD_TOOLS_DIR"), "/")
	wd := workDir()
	configured := ""
	if tools != "" {
		configured = filepath.Join(tools, "pm", "pm")
	}
	script := findExecutable([]string{
		configured,
		filepath.Join(executableDir(), "pm", "pm"),
		filepath.Join(wd, "allod", "tools", "pm", "pm"),
	}, "pm tool files not found; set ALLOD_TOOLS_DIR to an allod/tools checkout")
	env := append(os.Environ(), "WORK_DIR="+wd)
	replaceProcess("bash", append([]string{script}, args...), env)
}
