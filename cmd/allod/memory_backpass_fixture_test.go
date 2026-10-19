package main

// Fixture plumbing shared by the memory backpass integration tests: turning
// testdata/backpass's public/private/traces trees into isolated git repos
// with local bare origins, and generating the fake seats and stub forge the
// brief calls for instead of a real model or a real forge server.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// backpassWorkDir is traceTestWorkDir's symlink-resolution precaution,
// reused here because the fixture git repos' paths are compared literally
// in several assertions.
func backpassWorkDir(t *testing.T) string {
	t.Helper()
	return traceTestWorkDir(t)
}

// backpassCopyTree copies every regular file under src into dst, preserving
// each file's mode so the .hooks/memory-budget fixture keeps its +x bit.
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

// backpassGitRepo turns dir into a git repository with a local bare origin,
// origin/HEAD set to "main", and user.name/user.email set locally so a
// commit made later by the command under test (not by this helper) still
// succeeds without relying on GIT_AUTHOR_* environment variables.
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

// backpassFixtures is one full set of isolated repos for a backpass run.
type backpassFixtures struct {
	root                    string
	public, private, traces string
}

// backpassSetup builds the three fixture repos from testdata/backpass and
// isolates the test from the real environment exactly as the brief
// describes: a temp HOME, GIT_CONFIG_GLOBAL/SYSTEM pointed at /dev/null (the
// dev VM's global hooks refuse pushes to a path), and an empty WORK_DIR with
// no INVENTORY, so --public/--private must be passed explicitly.
func backpassSetup(t *testing.T) backpassFixtures {
	t.Helper()
	root := backpassWorkDir(t)
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("WORK_DIR", filepath.Join(root, "work"))
	t.Setenv("INVENTORY", "")
	if err := os.MkdirAll(filepath.Join(root, "home"), 0755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	var fx backpassFixtures
	fx.root = root
	fx.public = filepath.Join(root, "public")
	fx.private = filepath.Join(root, "private")
	fx.traces = filepath.Join(root, "traces")
	backpassCopyTree(t, "testdata/backpass/public", fx.public)
	backpassCopyTree(t, "testdata/backpass/private", fx.private)
	backpassCopyTree(t, "testdata/backpass/traces", fx.traces)
	backpassGitRepo(t, root, "public", fx.public)
	backpassGitRepo(t, root, "private", fx.private)
	backpassGitRepo(t, root, "traces", fx.traces)
	return fx
}

// backpassFakeSeatScript is the fake seat shared by every seat test: it
// reads the "Path: " line from the prompt file, optionally sleeps
// (FAKE_SEAT_SLEEP) or answers garbage for its first FAKE_SEAT_GARBAGE_N
// calls against a given trace (tracked per trace in FAKE_SEAT_COUNTER_DIR),
// and otherwise prints <responses-dir>/<trace-basename>.json.
const backpassFakeSeatScript = `#!/bin/sh
responses="$1"
prompt="$2"
tracepath=$(grep '^Path: ' "$prompt" | head -1 | sed 's/^Path: //')
base=$(basename "$tracepath")
if [ -n "$FAKE_SEAT_SLEEP" ]; then
  sleep "$FAKE_SEAT_SLEEP"
fi
if [ -n "$FAKE_SEAT_GARBAGE_N" ] && [ -n "$FAKE_SEAT_COUNTER_DIR" ]; then
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

// backpassWriteFile writes content to path, creating parent directories as
// needed: a small shared helper for unit tests that build a tiny file tree.
func backpassWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// backpassWriteScript writes an executable script and returns its path.
func backpassWriteScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("write script %s: %v", path, err)
	}
	return path
}

// backpassSeat returns a --bulk-seat/--fallback-seat value that runs the
// fake seat script against responsesDir: "<script> <responsesDir>", split
// on whitespace the same way the command itself splits a seat value.
func backpassSeat(t *testing.T, binDir, responsesDir string) string {
	t.Helper()
	script := backpassWriteScript(t, binDir, "fake-seat.sh", backpassFakeSeatScript)
	return script + " " + responsesDir
}

// backpassResponsesDir is testdata/backpass/responses's absolute path, read
// directly: the fake seat only reads from it, so fixtures need no copy.
func backpassResponsesDir(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/backpass/responses")
	if err != nil {
		t.Fatalf("abs responses dir: %v", err)
	}
	return abs
}

// backpassStubForgeScript is a minimal 'forge' stand-in: it logs every
// invocation to FORGE_STUB_LOG and answers just enough of 'pr list', 'pr
// view', 'pr find-by-head', 'pr create', and 'issue edit' for the write-mode
// and relay-check tests. One merged memory-backpass PR (#101, an unchecked
// "To relay" line) and one closed, unmerged one (#102) are always listed.
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
        printf '{"title":"memory-backpass: 2026-09-01","body":"Paragraph.\n\n## To relay\n\n- [ ] allod patch receive fixture-host:/some/path allod/memory\n"}'
        ;;
      102)
        printf '{"title":"memory-backpass: 2026-08-01 (rejected)","body":"Paragraph.\n"}'
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
