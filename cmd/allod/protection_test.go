package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// protectionCasesFile is read by tests/git-hooks/protected-refs-policy.sh too;
// its header states the columns.
var protectionCasesFile = filepath.Join("..", "..", "tests", "fixtures", "protection-cases.tsv")

type protectionCase struct {
	name     string
	entries  string
	origin   string
	checkout string
	worktree string
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
		if len(fields) != 8 {
			t.Fatalf("%s: want 8 tab-separated fields, got %d in %q", protectionCasesFile, len(fields), line)
		}
		cases = append(cases, protectionCase{
			name: fields[0], entries: fields[1], origin: fields[2], checkout: fields[3],
			worktree: fields[4], branch: fields[5], verdict: fields[6], expected: fields[7],
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

// buildProtectionFixture lays one case out under home and returns the directory
// the rails are pointed at: the main repository, or its linked worktree.
func buildProtectionFixture(t *testing.T, home string, testCase protectionCase) string {
	t.Helper()
	config := filepath.Join(home, ".config", "git")
	if err := os.MkdirAll(config, 0755); err != nil {
		t.Fatal(err)
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
	if err := os.WriteFile(filepath.Join(config, "protected-branches"), []byte(list.String()), 0644); err != nil {
		t.Fatal(err)
	}

	main := filepath.Join(home, filepath.FromSlash(testCase.checkout))
	if err := os.MkdirAll(main, 0755); err != nil {
		t.Fatal(err)
	}
	workspaceTestGit(t, main, "init", "-q", "--initial-branch=bootstrap")
	workspaceTestGit(t, main, "commit", "-q", "--allow-empty", "-m", "initial")
	if testCase.origin != "-" {
		workspaceTestGit(t, main, "remote", "add", "origin", testCase.origin)
	}
	// One branch cannot be checked out twice, so a worktree case leaves the main
	// repository on the branch it was created with and never on the one tested.
	if testCase.worktree == "yes" {
		linked := filepath.Join(home, "worktrees", testCase.name)
		workspaceTestGit(t, main, "worktree", "add", "-q", "-b", testCase.branch, linked)
		return linked
	}
	workspaceTestGit(t, main, "checkout", "-q", "-b", testCase.branch)
	return main
}

func TestLookupProtectionSharedCases(t *testing.T) {
	for _, testCase := range loadProtectionCases(t) {
		t.Run(testCase.name, func(t *testing.T) {
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			dir := buildProtectionFixture(t, home, testCase)

			found, ok := lookupProtection(dir)
			verdict := "unprotected"
			if ok && found.branch == testCase.branch {
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

// A misplaced checkout is misplaced whatever is checked out: the rails refuse it
// on every branch, unlike the hook, which blocks only the entry's branch.
func TestLookupProtectionMisplacedOnEveryBranch(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	dir := buildProtectionFixture(t, home, protectionCase{
		name:     "misplaced-on-agent-branch",
		entries:  "work/acme/widget=master",
		origin:   "ssh://git@forge.example:2222/acme/widget.git",
		checkout: "work/acme-widget",
		worktree: "no",
		branch:   "agent/x",
	})

	found, ok := lookupProtection(dir)
	if !ok || !found.misplaced() {
		t.Fatalf("lookupProtection = (%+v, %v), want a misplaced checkout", found, ok)
	}
	if found.expected != "work/acme/widget" || found.actual != "work/acme-widget" {
		t.Fatalf("paths = (%q, %q), want (\"work/acme/widget\", \"work/acme-widget\")",
			found.expected, found.actual)
	}
}
