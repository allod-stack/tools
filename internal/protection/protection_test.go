package protection

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

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

// git reports physical paths, so a symlinked or slash-suffixed $HOME is a way
// for a correctly placed checkout to read as misplaced.
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

func initCaseRepo(t *testing.T, dir, branch string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	testGit(t, dir, "init", "-q", "--initial-branch="+branch)
	testGit(t, dir, "commit", "-q", "--allow-empty", "-m", "initial")
}

func setCaseOrigin(t *testing.T, dir, origin string) {
	t.Helper()
	if _, ok := gitOutput(dir, "remote", "get-url", "origin"); ok {
		testGit(t, dir, "remote", "remove", "origin")
	}
	if origin != "-" {
		testGit(t, dir, "remote", "add", "origin", origin)
	}
}

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
		testGit(t, super, "-c", "protocol.file.allow=always",
			"submodule", "add", "-q", source, filepath.Base(repo))
		testGit(t, super, "commit", "-q", "-m", "add submodule")
	case "separate-git-dir":
		gitDir := filepath.Join(home, "gitdirs", testCase.name)
		if err := os.MkdirAll(filepath.Dir(gitDir), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(repo, 0755); err != nil {
			t.Fatal(err)
		}
		testGit(t, repo, "init", "-q", "--separate-git-dir="+gitDir,
			"--initial-branch=bootstrap")
		testGit(t, repo, "commit", "-q", "--allow-empty", "-m", "initial")
	default:
		t.Fatalf("case %s: unknown layout %q", testCase.name, testCase.layout)
	}

	setCaseOrigin(t, repo, testCase.origin)

	// One branch cannot be checked out twice, so a worktree case keeps the branch
	// under test out of the repository it belongs to.
	if testCase.layout == "worktree" {
		linked := filepath.Join(home, "worktrees", testCase.name)
		testGit(t, repo, "worktree", "add", "-q", "-b", testCase.branch, linked)
		return linked
	}
	testGit(t, repo, "checkout", "-q", "-b", testCase.branch)
	return repo
}

func TestLookupSharedCases(t *testing.T) {
	for _, testCase := range loadProtectionCases(t) {
		t.Run(testCase.name, func(t *testing.T) {
			home := caseHome(t, testCase)
			t.Setenv("HOME", home)
			dir := buildProtectionFixture(t, home, testCase)

			found, ok, err := Lookup(dir)
			if testCase.verdict == "error" {
				if err == nil {
					t.Fatalf("Lookup returned (%+v, %v, nil) instead of an error", found, ok)
				}
				if !strings.Contains(err.Error(), "protected-branches") {
					t.Fatalf("failure does not name the branch list: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}

			verdict := "unprotected"
			if ok && found.Covers(testCase.branch) {
				verdict = "protected"
				if found.Misplaced() {
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
			if found.Expected != testCase.expected {
				t.Errorf("expected path = %q, want %q", found.Expected, testCase.expected)
			}
			if found.Actual != testCase.checkout {
				t.Errorf("actual path = %q, want %q", found.Actual, testCase.checkout)
			}
			if found.Identity == "" {
				t.Error("identity is empty; the refusal cannot name the repository")
			}
		})
	}
}

// A long line must not swallow the entries behind it: the rail would then read a
// shorter list than the hook's awk does.
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

func TestLookupMisplacedOnEveryBranch(t *testing.T) {
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

	found, ok, err := Lookup(dir)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !ok || !found.Misplaced() {
		t.Fatalf("Lookup = (%+v, %v), want a misplaced checkout", found, ok)
	}
	if found.Expected != "work/acme/widget" || found.Actual != "work/acme-widget" {
		t.Fatalf("paths = (%q, %q), want (\"work/acme/widget\", \"work/acme-widget\")",
			found.Expected, found.Actual)
	}
}
