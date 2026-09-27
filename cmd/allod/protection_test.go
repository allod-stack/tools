package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// protectionCasesFile is read by tests/git-hooks/protected-refs-policy.sh too;
// its header states the columns.
var protectionCasesFile = filepath.Join("..", "..", "tests", "fixtures", "protection-cases.tsv")

const protectionCaseFields = 9

type protectionCase struct {
	name     string
	entries  string
	origin   string
	checkout string
	layout   string
	home     string
	branch   string
	verdict  string
	expected string
}

func loadProtectionCases(t *testing.T) []protectionCase {
	t.Helper()
	file, err := os.Open(protectionCasesFile)
	if err != nil {
		t.Fatalf("open %s: %v", protectionCasesFile, err)
	}
	defer file.Close()

	var cases []protectionCase
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != protectionCaseFields {
			t.Fatalf("%s: want %d tab-separated fields, got %d in %q",
				protectionCasesFile, protectionCaseFields, len(fields), line)
		}
		cases = append(cases, protectionCase{
			name: fields[0], entries: fields[1], origin: fields[2], checkout: fields[3],
			layout: fields[4], home: fields[5], branch: fields[6], verdict: fields[7],
			expected: fields[8],
		})
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", protectionCasesFile, err)
	}
	if len(cases) == 0 {
		t.Fatalf("%s holds no cases", protectionCasesFile)
	}
	return cases
}

// caseHome returns the $HOME a case runs under, which is not always a plain
// directory: git reports physical paths, so a symlinked or slash-suffixed $HOME
// is a way for a correctly placed checkout to read as misplaced.
func caseHome(t *testing.T, testCase protectionCase) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	switch testCase.home {
	case "plain":
		return root
	case "trailing-slash":
		return root + string(filepath.Separator)
	case "symlink":
		real := filepath.Join(root, "real-home")
		link := filepath.Join(root, "home")
		if err := os.MkdirAll(real, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		return link
	default:
		t.Fatalf("case %s: unknown home %q", testCase.name, testCase.home)
		return ""
	}
}

func writeCaseBranchList(t *testing.T, home string, testCase protectionCase) {
	t.Helper()
	config := filepath.Join(home, ".config", "git")
	if err := os.MkdirAll(config, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(config, "protected-branches")
	if testCase.entries == "@directory" {
		if err := os.MkdirAll(target, 0755); err != nil {
			t.Fatal(err)
		}
		return
	}
	var list strings.Builder
	if testCase.entries != "-" {
		for _, entry := range strings.Split(testCase.entries, ";") {
			path, branch, ok := strings.Cut(entry, "=")
			if !ok {
				t.Fatalf("case %s: malformed entry %q", testCase.name, entry)
			}
			list.WriteString(path + " " + branch + "\n")
		}
	}
	if err := os.WriteFile(target, []byte(list.String()), 0644); err != nil {
		t.Fatal(err)
	}
}

// captureCliExit runs a rail helper that ends the program through die and reports
// the exit code it asked for and what it wrote to stderr.
func captureCliExit(t *testing.T, run func()) (int, string) {
	t.Helper()
	var buffer bytes.Buffer
	previous := stderr
	stderr = &buffer
	defer func() { stderr = previous }()

	code := 0
	func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if wanted, ok := recovered.(cliExit); ok {
				code = wanted.code
				return
			}
			panic(recovered)
		}()
		run()
	}()
	return code, buffer.String()
}

func initCaseRepo(t *testing.T, dir, branch string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	workspaceTestGit(t, dir, "init", "-q", "--initial-branch="+branch)
	workspaceTestGit(t, dir, "commit", "-q", "--allow-empty", "-m", "initial")
}

func setCaseOrigin(t *testing.T, dir, origin string) {
	t.Helper()
	if _, ok := gitOutput(dir, "remote", "get-url", "origin"); ok {
		workspaceTestGit(t, dir, "remote", "remove", "origin")
	}
	if origin != "-" {
		workspaceTestGit(t, dir, "remote", "add", "origin", origin)
	}
}

// buildProtectionFixture lays one case out under home and returns the directory
// the rails are pointed at.
func buildProtectionFixture(t *testing.T, home string, testCase protectionCase) string {
	t.Helper()
	writeCaseBranchList(t, home, testCase)
	repo := filepath.Join(home, filepath.FromSlash(testCase.checkout))

	switch testCase.layout {
	case "plain":
		initCaseRepo(t, repo, "bootstrap")
	case "worktree":
		initCaseRepo(t, repo, "bootstrap")
	case "submodule":
		super := filepath.Dir(repo)
		source := filepath.Join(home, "submodule-sources", testCase.name)
		initCaseRepo(t, source, "bootstrap")
		initCaseRepo(t, super, "bootstrap")
		workspaceTestGit(t, super, "-c", "protocol.file.allow=always",
			"submodule", "add", "-q", source, filepath.Base(repo))
		workspaceTestGit(t, super, "commit", "-q", "-m", "add submodule")
	case "separate-git-dir":
		gitDir := filepath.Join(home, "gitdirs", testCase.name)
		if err := os.MkdirAll(filepath.Dir(gitDir), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(repo, 0755); err != nil {
			t.Fatal(err)
		}
		workspaceTestGit(t, repo, "init", "-q", "--separate-git-dir="+gitDir,
			"--initial-branch=bootstrap")
		workspaceTestGit(t, repo, "commit", "-q", "--allow-empty", "-m", "initial")
	default:
		t.Fatalf("case %s: unknown layout %q", testCase.name, testCase.layout)
	}

	setCaseOrigin(t, repo, testCase.origin)

	// One branch cannot be checked out twice, so a worktree case keeps the branch
	// under test out of the repository it belongs to.
	if testCase.layout == "worktree" {
		linked := filepath.Join(home, "worktrees", testCase.name)
		workspaceTestGit(t, repo, "worktree", "add", "-q", "-b", testCase.branch, linked)
		return linked
	}
	workspaceTestGit(t, repo, "checkout", "-q", "-b", testCase.branch)
	return repo
}

func TestLookupProtectionSharedCases(t *testing.T) {
	for _, testCase := range loadProtectionCases(t) {
		t.Run(testCase.name, func(t *testing.T) {
			home := caseHome(t, testCase)
			t.Setenv("HOME", home)
			dir := buildProtectionFixture(t, home, testCase)

			if testCase.verdict == "error" {
				code, message := captureCliExit(t, func() { lookupProtection(dir) })
				if code == 0 {
					t.Fatalf("lookupProtection returned instead of failing; stderr %q", message)
				}
				if !strings.Contains(message, "protected-branches") {
					t.Fatalf("failure does not name the branch list: %q", message)
				}
				return
			}

			found, ok := lookupProtection(dir)
			verdict := "unprotected"
			if ok && found.covers(testCase.branch) {
				verdict = "protected"
				if found.misplaced() {
					verdict = "mismatch"
				}
			}
			if verdict != testCase.verdict {
				t.Fatalf("verdict = %q, want %q (protection %+v, listed %v)",
					verdict, testCase.verdict, found, ok)
			}
			if testCase.verdict != "mismatch" {
				return
			}
			if found.expected != testCase.expected {
				t.Errorf("expected path = %q, want %q", found.expected, testCase.expected)
			}
			if found.actual != testCase.checkout {
				t.Errorf("actual path = %q, want %q", found.actual, testCase.checkout)
			}
			if found.identity == "" {
				t.Error("identity is empty; the refusal cannot name the repository")
			}
		})
	}
}

// A line longer than bufio.Scanner's default token must not swallow the entries
// behind it: the rail would read a shorter list than the hook's awk does.
func TestReadBranchListKeepsEntriesBehindALongLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "protected-branches")
	content := "# " + strings.Repeat("x", 200*1024) + "\nwork/acme/widget master\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	entries, err := readBranchList(path)
	if err != nil {
		t.Fatalf("readBranchList: %v", err)
	}
	if len(entries) != 1 || entries[0].path != "work/acme/widget" || entries[0].branch != "master" {
		t.Fatalf("entries = %+v, want one work/acme/widget master entry", entries)
	}
}

func TestReadBranchListAbsentFileIsNoPolicy(t *testing.T) {
	entries, err := readBranchList(filepath.Join(t.TempDir(), "absent"))
	if err != nil || entries != nil {
		t.Fatalf("readBranchList on an absent file = (%+v, %v), want (nil, nil)", entries, err)
	}
}

func TestReadBranchListUnreadableFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "protected-branches")
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := readBranchList(path); err == nil {
		t.Fatal("readBranchList on a directory returned no error")
	}
}

// A misplaced checkout is misplaced whatever is checked out: the rails refuse it
// on every branch, unlike the hook, which blocks only the entry's branch.
func TestLookupProtectionMisplacedOnEveryBranch(t *testing.T) {
	testCase := protectionCase{
		name:     "misplaced-on-agent-branch",
		entries:  "work/acme/widget=master",
		origin:   "ssh://git@forge.example:2222/acme/widget.git",
		checkout: "work/acme-widget",
		layout:   "plain",
		home:     "plain",
		branch:   "agent/x",
	}
	home := caseHome(t, testCase)
	t.Setenv("HOME", home)
	dir := buildProtectionFixture(t, home, testCase)

	found, ok := lookupProtection(dir)
	if !ok || !found.misplaced() {
		t.Fatalf("lookupProtection = (%+v, %v), want a misplaced checkout", found, ok)
	}
	if found.expected != "work/acme/widget" || found.actual != "work/acme-widget" {
		t.Fatalf("paths = (%q, %q), want (\"work/acme/widget\", \"work/acme-widget\")",
			found.expected, found.actual)
	}
}
