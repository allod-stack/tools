package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Symlink-resolved like traceTestWorkDir: fixture paths are compared literally.
func backpassWorkDir(t *testing.T) string {
	t.Helper()
	return traceTestWorkDir(t)
}

func backpassCopyTree(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read fixture dir %s: %v", src, err)
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			backpassCopyTree(t, srcPath, dstPath)
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", srcPath, err)
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			t.Fatalf("read %s: %v", srcPath, err)
		}
		if err := os.WriteFile(dstPath, data, info.Mode()); err != nil {
			t.Fatalf("write %s: %v", dstPath, err)
		}
	}
}

// backpassFixtureGitConfig writes a global git config carrying a fixture
// identity, so a clone 'change begin' makes (its own .git, with none of its
// own) can still commit, same as a real worker's relies on $HOME/.gitconfig.
func backpassFixtureGitConfig(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "gitconfig-global")
	content := "[user]\n\tname = fixture\n\temail = fixture@example.com\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture global gitconfig: %v", err)
	}
	return path
}

func backpassGitRepo(t *testing.T, root, name, dir string) {
	t.Helper()
	origin := filepath.Join(root, name+"-origin.git")
	workspaceTestGit(t, root, "init", "-q", "--bare", "-b", "main", origin)
	workspaceTestGit(t, dir, "init", "-q", "-b", "main")
	workspaceTestGit(t, dir, "config", "user.name", "fixture")
	workspaceTestGit(t, dir, "config", "user.email", "fixture@example.com")
	workspaceTestGit(t, dir, "add", "-A")
	workspaceTestGit(t, dir, "commit", "-q", "-m", "fixture: initial commit")
	workspaceTestGit(t, dir, "remote", "add", "origin", origin)
	workspaceTestGit(t, dir, "push", "-q", "-u", "origin", "main")
	workspaceTestGit(t, dir, "remote", "set-head", "origin", "main")
}

type backpassFixtures struct {
	root                    string
	public, private, traces string
}

// Isolates the run: temp HOME, system git config at /dev/null (the dev VM's
// global hooks refuse pushes to a path), WORK_DIR empty, no INVENTORY.
func backpassSetup(t *testing.T) backpassFixtures {
	t.Helper()
	root := backpassWorkDir(t)
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("GIT_CONFIG_GLOBAL", backpassFixtureGitConfig(t, root))
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("WORK_DIR", filepath.Join(root, "work"))
	t.Setenv("INVENTORY", "")
	if err := os.MkdirAll(filepath.Join(root, "home"), 0755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	// public and private live under WORK_DIR, as a real deployment's checkouts
	// do (vm-provisioning.md): 'change record' resolves a clone's parent by
	// scanning repos there, so a fixture repo outside it is unresolvable.
	var fx backpassFixtures
	fx.root = root
	fx.public = filepath.Join(root, "work", "public")
	fx.private = filepath.Join(root, "work", "private")
	fx.traces = filepath.Join(root, "traces")
	backpassCopyTree(t, "testdata/backpass/public", fx.public)
	backpassCopyTree(t, "testdata/backpass/private", fx.private)
	backpassCopyTree(t, "testdata/backpass/traces", fx.traces)
	backpassGitRepo(t, root, "public", fx.public)
	backpassGitRepo(t, root, "private", fx.private)
	backpassGitRepo(t, root, "traces", fx.traces)
	return fx
}

// Reads the Path: line of the prompt and prints <responses-dir>/<trace basename>.json.
// FAKE_SEAT_SLEEP delays; FAKE_SEAT_GARBAGE_N answers garbage for the first N calls
// per trace (FAKE_SEAT_COUNTER_DIR), scoped by FAKE_SEAT_GARBAGE_BASENAME when set.
const backpassFakeSeatScript = `#!/bin/sh
responses="$1"
prompt="$2"
tracepath=$(grep '^Path: ' "$prompt" | head -1 | sed 's/^Path: //')
base=$(basename "$tracepath")
if [ -n "$FAKE_SEAT_SLEEP" ]; then
  sleep "$FAKE_SEAT_SLEEP"
fi
garbage_applies=1
if [ -n "$FAKE_SEAT_GARBAGE_BASENAME" ] && [ "$base" != "$FAKE_SEAT_GARBAGE_BASENAME" ]; then
  garbage_applies=0
fi
if [ -n "$FAKE_SEAT_GARBAGE_N" ] && [ -n "$FAKE_SEAT_COUNTER_DIR" ] && [ "$garbage_applies" = 1 ]; then
  mkdir -p "$FAKE_SEAT_COUNTER_DIR"
  counter="$FAKE_SEAT_COUNTER_DIR/$base.count"
  count=0
  if [ -f "$counter" ]; then
    count=$(cat "$counter")
  fi
  count=$((count+1))
  echo "$count" > "$counter"
  if [ "$count" -le "$FAKE_SEAT_GARBAGE_N" ]; then
    echo "not valid json"
    exit 0
  fi
fi
cat "$responses/$base.json"
`

func backpassWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func backpassWriteScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("write script %s: %v", path, err)
	}
	return path
}

func backpassSeat(t *testing.T, binDir, responsesDir string) string {
	t.Helper()
	script := backpassWriteScript(t, binDir, "fake-seat.sh", backpassFakeSeatScript)
	return script + " " + responsesDir
}

func backpassResponsesDir(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/backpass/responses")
	if err != nil {
		t.Fatalf("abs responses dir: %v", err)
	}
	return abs
}

// Logs every invocation to FORGE_STUB_LOG; always lists #101 (merged, unchecked
// To relay line) and #102 (closed, unmerged).
const backpassStubForgeScript = `#!/bin/sh
if [ -n "$FORGE_STUB_LOG" ]; then
  echo "$*" >> "$FORGE_STUB_LOG"
fi
case "$1 $2" in
  "pr list")
    cat <<'LISTEOF'
101  memory-backpass: 2026-09-01  claude  merged 2026-09-05
102  memory-backpass: 2026-08-01  claude  closed
LISTEOF
    ;;
  "pr view")
    case "$3" in
      101)
        cat <<'VIEWEOF'
{"title":"memory-backpass: 2026-09-01","body":"Paragraph.\n\n## To relay\n\n- [ ] allod patch receive fixture-host:/some/path allod/memory\n"}
VIEWEOF
        ;;
      102)
        cat <<'VIEWEOF'
{"title":"memory-backpass: 2026-08-01 (rejected)","body":"Paragraph.\n"}
VIEWEOF
        ;;
      *)
        printf '{"title":"","body":""}'
        ;;
    esac
    ;;
  "pr find-by-head")
    printf ''
    ;;
  "pr create")
    echo "http://forge.example/owner/repo/pulls/999"
    ;;
  "issue edit")
    exit 0
    ;;
  *)
    exit 1
    ;;
esac
`

// backpassSetupForge writes the stub forge, points FORGE_STUB_LOG at a file
// under root, and prepends the stub's directory to PATH (keeping the real
// PATH behind it, so git and the shell utilities the fake seats need stay
// reachable).
func backpassSetupForge(t *testing.T) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	backpassWriteScript(t, dir, "forge", backpassStubForgeScript)
	logPath = filepath.Join(dir, "forge.log")
	t.Setenv("FORGE_STUB_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// backpassGitWithoutForge builds a PATH carrying only 'git', so a test can
// pin the "'forge' not found on PATH" refusal without also losing every
// other tool a wider PATH wipe would take with it.
func backpassGitWithoutForge(t *testing.T) {
	t.Helper()
	gitPath := backpassLookPath(t, "git")
	dir := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(dir, "git")); err != nil {
		t.Fatalf("symlink git: %v", err)
	}
	t.Setenv("PATH", dir)
}

// Finds the ledgered agent/memory-backpass-* clone without recomputing "today",
// which can straddle midnight relative to when the command ran.
func backpassFindAgentBranch(t *testing.T, repo string) string {
	t.Helper()
	var matches []string
	for _, entry := range changeLedgerEntries(repo) {
		if strings.HasPrefix(entry.branch, "agent/memory-backpass-") {
			matches = append(matches, entry.branch)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one agent/memory-backpass-* branch ledgered in %s, got: %q", repo, matches)
	}
	return matches[0]
}

// backpassWorktreePathForBranch locates the clone a branch was ledgered
// under, through repo's own ledger — never the clone's own config.
func backpassWorktreePathForBranch(t *testing.T, repo, branch string) string {
	t.Helper()
	for _, entry := range changeLedgerEntries(repo) {
		if entry.branch == branch {
			return entry.path
		}
	}
	t.Fatalf("no ledgered clone found for branch %s in %s", branch, repo)
	return ""
}

// backpassIsolatedEnv sets up HOME, GIT_CONFIG_GLOBAL/SYSTEM, WORK_DIR, and
// an empty INVENTORY, without building any fixture repo: the resolver
// tests build their own, placed to exercise one resolution source at a
// time.
func backpassIsolatedEnv(t *testing.T) (root, workDirRoot string) {
	t.Helper()
	root = backpassWorkDir(t)
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	workDirRoot = filepath.Join(root, "work")
	t.Setenv("WORK_DIR", workDirRoot)
	t.Setenv("INVENTORY", "")
	if err := os.MkdirAll(filepath.Join(root, "home"), 0755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	return root, workDirRoot
}

// backpassBuildRepo copies a testdata fixture tree to dst and turns it into
// a git repo with a local bare origin, named uniquely under root.
func backpassBuildRepo(t *testing.T, root, name, src, dst string) {
	t.Helper()
	backpassCopyTree(t, src, dst)
	backpassGitRepo(t, root, name, dst)
}

func backpassLookPath(t *testing.T, name string) string {
	t.Helper()
	for _, dir := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	t.Fatalf("%s not found on PATH", name)
	return ""
}
