package main

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
)

// exitNotSandboxable is unused by every other refusal, so a caller can tell
// "this path is not something 'run' may sandbox" apart from the rest.
const exitNotSandboxable = 10

// exitTrustedNixUser is unused by every other refusal, so a caller can tell
// "this user must not be sandboxed" apart from the rest.
const exitTrustedNixUser = 12

// exitSandboxSourceUnsafe is unused by every other refusal, so a caller can
// tell "something 'run' would bind is a symlink" apart from the rest.
const exitSandboxSourceUnsafe = 14

// exitActiveRunScope is unused by every other refusal, so a caller can tell
// "something is still running inside this clone" apart from the rest.
const exitActiveRunScope = 13

// exitNetrcReadable is unused by every other refusal, so a caller can tell
// "/etc holds a readable netrc this sandbox would expose" apart from the rest.
const exitNetrcReadable = 15

// Outside commands hold this lock until exit; an active worker makes them refuse.
func requireExclusiveLedgerLock(parent string, entry changeLedgerEntry) *changeLedgerLock {
	path, ok := ledgerFilePath(parent, entry)
	if !ok {
		die(1, "could not resolve the ledger file for '%s' to lock it", entry.path)
	}
	lock, ok := lockLedgerExclusive(path)
	if !ok {
		die(exitActiveRunScope,
			"clone '%s' is locked, almost certainly by a 'run' still sandboxing a command inside it; outside commands act on a clone only when nothing is running inside it, so this refuses until it stops",
			entry.path)
	}
	return lock
}

func changeRun(args []string) {
	memory := "2G"
	memorySet := false
	var clonePaths, command []string
	sawDashDash := false
	for len(args) > 0 {
		switch args[0] {
		case "--memory":
			requireValue(args, args[0])
			memory, memorySet = args[1], true
			args = args[2:]
		case "--":
			command = args[1:]
			sawDashDash = true
			args = nil
		case "-h", "--help":
			fmt.Fprint(stdout, changeUsageText)
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for change run: %s", args[0])
			}
			clonePaths = append(clonePaths, args[0])
			args = args[1:]
		}
	}
	if !sawDashDash {
		die(1, "change run requires '--' before the command")
	}
	if len(command) == 0 {
		die(1, "change run requires a command after '--'")
	}
	if memorySet && memory == "" {
		die(1, "--memory requires a non-empty value")
	}

	requireUntrustedNixUser()
	requireNoReadableNetrc()
	requireSandboxSourcesSafe(homeDir())
	requireRunnerDirsSafe(homeDir())
	requireGitRails(homeDir())

	if len(clonePaths) == 0 {
		clonePaths = []string{""}
	}
	clones := make([]string, 0, len(clonePaths))
	branches := make([]string, 0, len(clonePaths))
	var locks []*changeLedgerLock
	defer func() {
		for _, lock := range locks {
			lock.Release()
		}
	}()
	for _, clonePath := range clonePaths {
		repo, parentRepo, entry, resolvedStart, isClone := resolveLedgeredClone(clonePath)
		if !isClone {
			die(exitNotSandboxable,
				"'%s' is not a clone 'allod change begin -d' made; 'allod change run' only sandboxes a ledgered clone",
				resolvedStart)
		}
		lockPath, ok := ledgerFilePath(parentRepo, entry)
		if !ok {
			die(1, "could not resolve the ledger file for '%s' to lock it", repo)
		}
		lock, ok := lockLedgerExclusive(lockPath)
		if !ok {
			die(exitActiveRunScope,
				"clone '%s' is locked by an outside command, or by another 'run' still in its own pre-flight; 'run' refuses to start until it finishes",
				repo)
		}
		enableCloneGitEnv()
		requireCloneConfigSafe(repo, entry.branch, parentRepo)
		branch := currentBranch(repo)
		if branch != entry.branch || !strings.HasPrefix(branch, "agent/") {
			die(exitNotSandboxable,
				"'%s' is checked out on branch '%s', not the 'agent/*' branch its ledger lists ('%s'); 'allod change run' refuses to sandbox anything else",
				repo, branch, entry.branch)
		}
		if !lock.Downgrade() {
			die(1, "could not convert the pre-flight lock on '%s' to a shared one for the sandboxed command", repo)
		}
		locks = append(locks, lock)
		clones = append(clones, repo)
		branches = append(branches, branch)
	}

	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		die(1, "'bwrap' not found on PATH; 'allod change run' never falls back to running unsandboxed")
	}

	claudeJSONCopy, discardClaudeJSONCopy := prepareClaudeJSONCopy(homeDir())
	defer discardClaudeJSONCopy()

	systemdArgs := []string{
		"--user", "--scope", "--quiet",
		"-p", "MemoryMax=" + memory,
		"--unit", runScopeName(branches),
		"--", bwrapPath,
	}
	systemdArgs = append(systemdArgs, buildBwrapArgs(homeDir(), clones, os.Environ(), claudeJSONCopy, len(locks))...)
	systemdArgs = append(systemdArgs, "--")
	systemdArgs = append(systemdArgs, command...)

	status := runSandboxCommand(locks, systemdArgs)
	discardClaudeJSONCopy()
	exit(status)
}

// Every clone's ledger lock is duplicated into systemd-run via ExtraFiles (fd 3, 4, ...): flock
// follows the open file description, so an inheritor keeps it; see buildBwrapArgs on --sync-fd.
func runSandboxCommand(locks []*changeLedgerLock, systemdArgs []string) int {
	cmd := exec.Command("systemd-run", systemdArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	for _, lock := range locks {
		if lock != nil && lock.file != nil {
			cmd.ExtraFiles = append(cmd.ExtraFiles, lock.file)
		}
	}
	return commandExitCode(cmd.Run())
}

func requireNoReadableNetrc() {
	if netrc := nixNetrcFilePath(); netrc != "" && fileReadable(netrc) {
		die(exitNetrcReadable,
			"'%s' (nix's own netrc-file) exists and is readable; /etc is bound whole, which would expose it inside the sandbox, so run refuses to start",
			netrc)
	}
	if fileReadable("/etc/nix/netrc") {
		die(exitNetrcReadable,
			"'/etc/nix/netrc' exists and is readable; /etc is bound whole, which would expose it inside the sandbox, so run refuses to start")
	}
}

func nixNetrcFilePath() string {
	output, status := nixConfigShowScrubbed("netrc-file")
	if status != 0 {
		die(1, "could not ask nix for its own netrc-file setting, so this preflight cannot be answered at all: %s", output)
	}
	return strings.TrimSpace(output)
}

func fileReadable(path string) bool {
	if path == "" {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	file.Close()
	return true
}

// Give the worker a private writable copy; failure must not expose or drop state.
func prepareClaudeJSONCopy(home string) (copyPath string, discard func()) {
	noop := func() {}
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			die(exitSandboxSourceUnsafe, "cannot copy .claude.json: %v", err)
		}
		return "", noop
	}
	file, err := os.CreateTemp("", "allod-claude-json-")
	if err != nil {
		die(exitSandboxSourceUnsafe, "cannot create private .claude.json copy: %v", err)
	}
	path := file.Name()
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(path)
		die(exitSandboxSourceUnsafe, "cannot write private .claude.json copy")
	}
	return path, func() { os.Remove(path) }
}

func requireGitRails(home string) {
	for _, rel := range []string{
		filepath.Join(".config", "git", "protected-refs-policy"),
		filepath.Join(".config", "git", "hooks", "pre-commit"),
	} {
		path := filepath.Join(home, rel)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			die(exitSandboxSourceUnsafe, "required Git rail '%s' is missing or unreadable", path)
		}
		if strings.HasSuffix(path, "hooks/pre-commit") && !strings.HasPrefix(physicalPath(path), nixStoreRoot+string(filepath.Separator)) {
			die(exitSandboxSourceUnsafe, "required Git hook '%s' must be managed in the Nix store", path)
		}
	}
	globalConfig := filepath.Join(home, ".gitconfig")
	if _, err := os.Stat(globalConfig); os.IsNotExist(err) {
		globalConfig = filepath.Join(home, ".config", "git", "config")
	}
	info, err := os.Stat(globalConfig)
	if err != nil || !info.Mode().IsRegular() {
		die(exitSandboxSourceUnsafe, "required Git config '%s' is missing or unreadable", globalConfig)
	}
}

// A PATH override must not change the trusted-users answer; tests replace
// this path at link time to exercise Nix responses.
var nixExecutablePath = "/run/current-system/sw/bin/nix"

func nixConfigShowScrubbed(key string) (string, int) {
	cmd := exec.Command(nixExecutablePath, "config", "show", key)
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "NIX_CONFIG" || name == "NIX_USER_CONF_FILES" || name == "NIX_CONF_DIR" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "NIX_USER_CONF_FILES=")
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	status := commandExitCode(cmd.Run())
	if status != 0 {
		combined := out.String()
		if errOut.Len() > 0 {
			if combined != "" {
				combined += "\n"
			}
			combined += errOut.String()
		}
		return strings.TrimRight(combined, "\n"), status
	}
	return strings.TrimRight(out.String(), "\n"), status
}

func requireUntrustedNixUser() {
	output, status := nixConfigShowScrubbed("trusted-users")
	if status != 0 {
		die(1, "could not ask nix for its own trusted-users list, so this preflight cannot be answered at all: %s", output)
	}
	trusted := strings.Fields(output)
	if len(trusted) == 0 {
		die(1, "nix returned an empty trusted-users list; this preflight cannot be answered")
	}
	current, err := user.Current()
	if err != nil {
		die(1, "could not look up the invoking user to compare against Nix's own trusted-users list, so this preflight cannot be answered at all: %v", err)
	}
	groups, err := currentUserGroups(current)
	if err != nil {
		die(1, "could not look up the invoking user's own groups to compare against Nix's own trusted-users list, so this preflight cannot be answered at all: %v", err)
	}
	for _, name := range trusted {
		if name == "*" {
			die(exitTrustedNixUser,
				"Nix's own trusted-users names '*', trusting every user including this one; the daemon accepts privileged build options from a trusted uid, including post-build-hook, which it runs as root, so 'run' refuses to bind the daemon socket into the sandbox until allod/vm#12 removes the trust fleet-wide")
		}
		if name == current.Username {
			die(exitTrustedNixUser,
				"this user ('%s') is one of Nix's own trusted-users; the daemon accepts privileged build options from a trusted uid, including post-build-hook, which it runs as root, so 'run' refuses to bind the daemon socket into the sandbox until allod/vm#12 removes the trust fleet-wide",
				current.Username)
		}
		if group, ok := strings.CutPrefix(name, "@"); ok {
			for _, memberOf := range groups {
				if memberOf == group {
					die(exitTrustedNixUser,
						"this user's group ('@%s') is one of Nix's own trusted-users; the daemon accepts privileged build options from a trusted uid, including post-build-hook, which it runs as root, so 'run' refuses to bind the daemon socket into the sandbox until allod/vm#12 removes the trust fleet-wide",
						group)
				}
			}
		}
	}
}

func currentUserGroups(current *user.User) ([]string, error) {
	gids, err := current.GroupIds()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(gids))
	for _, gid := range gids {
		group, err := user.LookupGroupId(gid)
		if err != nil {
			return nil, err
		}
		names = append(names, group.Name)
	}
	return names, nil
}

func requireSandboxSourcesSafe(home string) {
	for _, name := range []string{".claude", ".codex", ".pi", ".claude.json", ".gitconfig", filepath.Join(".config", "git")} {
		path := filepath.Join(home, name)
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			die(exitSandboxSourceUnsafe, "sandbox source '%s' cannot be inspected: %v", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			die(exitSandboxSourceUnsafe,
				"'%s' is a symlink; 'run' refuses to follow it rather than bind whatever it resolves to as if it were the real thing",
				path)
		}
	}
}

// Refuse aliases and incomplete walks before binding runner state writable.
func requireRunnerDirsSafe(home string) {
	for _, spec := range runnerDirs {
		base := filepath.Join(home, spec.dir)
		baseResolved := physicalPath(base)
		for _, sub := range spec.writableSubs {
			ancestor := base
			for _, part := range strings.Split(sub, string(filepath.Separator)) {
				ancestor = filepath.Join(ancestor, part)
				info, err := os.Lstat(ancestor)
				if os.IsNotExist(err) {
					break
				}
				if err != nil {
					die(exitSandboxSourceUnsafe, "runner writable source '%s' cannot be inspected: %v", ancestor, err)
				}
				if info.Mode()&os.ModeSymlink != 0 {
					die(exitSandboxSourceUnsafe, "runner writable source '%s' is a symlink", ancestor)
				}
			}
		}

		if err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, lstatErr := os.Lstat(path)
			if lstatErr != nil {
				return lstatErr
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
				die(exitSandboxSourceUnsafe,
					"'%s' has %d hard links; a hard link anywhere under '%s' aliases some other file's exact content under this path, so this refuses rather than bind it",
					path, stat.Nlink, base)
			}
			return nil
		}); err != nil && !os.IsNotExist(err) {
			die(exitSandboxSourceUnsafe, "runner source '%s' cannot be inspected: %v", base, err)
		}

		for _, sub := range spec.writableSubs {
			subPath := filepath.Join(base, sub)
			if err := filepath.WalkDir(subPath, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				info, lstatErr := os.Lstat(path)
				if lstatErr != nil {
					return lstatErr
				}
				if info.Mode()&os.ModeSymlink == 0 {
					return nil
				}
				resolved := physicalPath(path)
				if rel, relErr := filepath.Rel(baseResolved, resolved); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					die(exitSandboxSourceUnsafe,
						"'%s' is a symlink resolving back inside '%s', the same runner directory this writable subdirectory sits under read-only; whatever it resolves to is reachable through this writable mount instead, so this refuses rather than follow it",
						path, base)
				}
				return nil
			}); err != nil && !os.IsNotExist(err) {
				die(exitSandboxSourceUnsafe, "runner writable source '%s' cannot be inspected: %v", subPath, err)
			}
		}
	}
}

// runScopeSuffixLen is the length of the random suffix runScopeName
// always appends.
const runScopeSuffixLen = 6

func runScopeName(branches []string) string {
	slugs := make([]string, len(branches))
	for i, branch := range branches {
		slugs[i] = strings.ReplaceAll(branch, "/", "-")
	}
	suffix := make([]byte, runScopeSuffixLen)
	if _, err := rand.Read(suffix); err == nil {
		for i := range suffix {
			suffix[i] = tempAlphabet[int(suffix[i])%len(tempAlphabet)]
		}
	}
	return "allod-run-" + strings.Join(slugs, ":") + "-" + string(suffix)
}

var envAllowlist = []string{
	"HOME", "PATH", "TERM", "LANG", "LC_ALL",
	"USER", "LOGNAME", "SHELL", "TZ", "COLORTERM", "NIX_REMOTE",
}

func buildEnvArgs(environ []string) []string {
	allowed := make(map[string]bool, len(envAllowlist))
	for _, name := range envAllowlist {
		allowed[name] = true
	}
	args := []string{"--clearenv"}
	for _, kv := range environ {
		name, value, found := strings.Cut(kv, "=")
		if !found {
			continue
		}
		if allowed[name] || strings.HasPrefix(name, "LC_") {
			args = append(args, "--setenv", name, value)
		}
	}
	return append(args, "--setenv", "TMPDIR", "/tmp")
}

const nixStoreRoot = "/nix/store"

var gitConfigAllowlist = map[string]bool{
	"config":                    true,
	"protected-refs-policy":     true,
	"protected-branches":        true,
	"signing-required-branches": true,
	"allowed-external-remotes":  true,
	"forge-hosts":               true,
	"active-pr-branches":        true,
}

// Bind only policy files and Nix-store-managed hooks; arbitrary files under
// hooks can be credentials under a second name.
func bindGitConfigTree(args []string, gitConfigDir string) []string {
	tokenPath := filepath.Join(gitConfigDir, "forgejo-token")
	tokenInfo, tokenErr := os.Stat(tokenPath)
	tokenContent, tokenContentErr := os.ReadFile(tokenPath)
	configRoot := physicalPath(gitConfigDir)
	walk := func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			die(exitSandboxSourceUnsafe, "Git config source '%s' cannot be inspected: %v", path, err)
		}
		linkInfo, lstatErr := os.Lstat(path)
		if lstatErr != nil {
			die(exitSandboxSourceUnsafe, "Git config source '%s' cannot be inspected: %v", path, lstatErr)
		}
		if linkInfo.Mode()&os.ModeSymlink != 0 {
			resolved := physicalPath(path)
			if targetInfo, statErr := os.Stat(resolved); statErr == nil && targetInfo.IsDir() {
				return nil
			}
			insideConfig := func() bool {
				rel, relErr := filepath.Rel(configRoot, resolved)
				if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return false
				}
				first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
				return first == "hooks" || gitConfigAllowlist[first]
			}()
			insideStore := resolved == nixStoreRoot || strings.HasPrefix(resolved, nixStoreRoot+string(filepath.Separator))
			if !insideConfig && !insideStore {
				return nil
			}
		}
		if entry.IsDir() {
			return nil
		}
		if rel, relErr := filepath.Rel(filepath.Join(gitConfigDir, "hooks"), path); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			resolved := physicalPath(path)
			if !strings.HasPrefix(resolved, nixStoreRoot+string(filepath.Separator)) {
				return nil
			}
		}
		if tokenErr == nil {
			if info, statErr := os.Stat(path); statErr == nil && os.SameFile(info, tokenInfo) {
				return nil
			}
		}
		if tokenContentErr == nil {
			if content, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(content, tokenContent) {
				return nil
			}
		}
		bindFlag := "--ro-bind-try"
		if path == filepath.Join(gitConfigDir, "config") || path == filepath.Join(gitConfigDir, "protected-refs-policy") || path == filepath.Join(gitConfigDir, "hooks", "pre-commit") {
			bindFlag = "--ro-bind"
		}
		args = append(args, bindFlag, path, path)
		return nil
	}
	filepath.WalkDir(filepath.Join(gitConfigDir, "hooks"), walk)
	for name := range gitConfigAllowlist {
		filepath.WalkDir(filepath.Join(gitConfigDir, name), walk)
	}
	return args
}

type runnerDirSpec struct {
	dir          string
	writableSubs []string
}

var runnerDirs = []runnerDirSpec{
	{".claude", []string{"projects", "todos", "statsig"}},
	{".codex", []string{"sessions", "log"}},
	{".pi", []string{filepath.Join("agent", "sessions")}},
}

func buildBwrapArgs(home string, clones, environ []string, claudeJSONCopy string, lockFDCount int) []string {
	args := []string{
		// Named unshares hard-fail where --unshare-all would silently continue without a namespace.
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--unshare-cgroup-try", "--share-net", "--die-with-parent",
		"--new-session", "--cap-drop", "ALL",
	}
	// bwrap closes inherited fds it does not recognize; --sync-fd keeps one through its setup, so
	// the first clone's lock outlives allod. bwrap honours only one --sync-fd: a real limitation.
	if lockFDCount > 0 {
		args = append(args, "--sync-fd", "3")
	}
	args = append(args, buildEnvArgs(environ)...)
	args = append(args,
		"--ro-bind", "/nix", "/nix",
		// Required, not '-try': a build that silently can't reach the daemon
		// is the fail-silent case this stack refuses (architecture.md, fail
		// loud), and a dev VM always has this socket.
		"--bind", "/nix/var/nix/daemon-socket/socket", "/nix/var/nix/daemon-socket/socket",
		"--ro-bind", "/etc", "/etc",
		"--ro-bind", "/run/current-system", "/run/current-system",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind-try", "/run/wrappers", "/run/wrappers",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--tmpfs", home,
	)
	for _, clone := range clones {
		args = append(args, "--bind", clone, clone)
	}
	for _, id := range memoryRegistryIds() {
		checkout, ok := registryCheckout(id)
		if !ok {
			continue
		}
		path := filepath.Join(workDir(), checkout)
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
			args = append(args, "--ro-bind-try", path, path)
			// No ledger or history of a memory repo is visible inside: its
			// own '.git/allod-changes' ledger would otherwise be bound
			// read-only along with everything else in the checkout.
			args = append(args, "--tmpfs", filepath.Join(path, ".git"))
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".gitconfig")); err == nil {
		args = append(args, "--ro-bind", filepath.Join(home, ".gitconfig"), filepath.Join(home, ".gitconfig"))
	}
	args = bindGitConfigTree(args, filepath.Join(home, ".config", "git"))
	for _, spec := range runnerDirs {
		base := filepath.Join(home, spec.dir)
		args = append(args, "--ro-bind-try", base, base)
		for _, sub := range spec.writableSubs {
			subPath := filepath.Join(base, sub)
			args = append(args, "--bind-try", subPath, subPath)
		}
	}
	if claudeJSONCopy != "" {
		args = append(args, "--bind", claudeJSONCopy, filepath.Join(home, ".claude.json"))
	}
	args = append(args, "--chdir", clones[0])
	return args
}
