package gitremote

import (
	"errors"
	"os/exec"
	"testing"
)

// initRepo creates a fresh git repository in a temp directory, optionally with
// an origin remote, and makes it the working directory for the test. Real git
// is used deliberately: the point of this package is what git actually prints.
func initRepo(t *testing.T, origin string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	run(t, "git", "init", "-q")
	if origin != "" {
		run(t, "git", "remote", "add", "origin", origin)
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func TestInferRepo(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   string
	}{
		{"ssh url with port", "ssh://git@forge.example:2222/acme/widget.git", "acme/widget"},
		{"ssh url without .git", "ssh://git@forge.example:2222/acme/widget", "acme/widget"},
		{"scp style", "git@forge.example:acme/widget.git", "acme/widget"},
		{"scp style without .git", "git@forge.example:acme/widget", "acme/widget"},
		{"https", "https://forge.example/acme/widget.git", "acme/widget"},
		{"https without .git", "https://forge.example/acme/widget", "acme/widget"},
		{"deep path keeps last two components", "https://forge.example/a/b/acme/widget.git", "acme/widget"},
		// "${url%.git}" strips one suffix only, so the second .git stays part
		// of the repo name. Bug-for-bug with bash.
		{"double .git strips one", "https://forge.example/acme/widget.git.git", "acme/widget.git"},
		{"local path", "/srv/git/acme/widget.git", "acme/widget"},
		// grep finds nothing: a trailing slash leaves no second component.
		{"trailing slash yields nothing", "https://forge.example/acme/widget/", ""},
		{"no separator yields nothing", "widget", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initRepo(t, tt.origin)
			got, err := InferRepo()
			if err != nil {
				t.Fatalf("InferRepo() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("InferRepo() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInferRepoNoOrigin(t *testing.T) {
	initRepo(t, "")
	got, err := InferRepo()
	if !errors.Is(err, ErrNoOrigin) {
		t.Fatalf("InferRepo() error = %v, want ErrNoOrigin", err)
	}
	if got != "" {
		t.Errorf("InferRepo() = %q, want empty", got)
	}
}

func TestCurrentBranch(t *testing.T) {
	initRepo(t, "")
	// A branch exists before the first commit, which is exactly the state a
	// caller is in when it opens a PR from a fresh branch.
	run(t, "git", "checkout", "-q", "-b", "feature/current")
	if got := CurrentBranch(); got != "feature/current" {
		t.Errorf("CurrentBranch() = %q, want %q", got, "feature/current")
	}
}

// With no branch checked out git prints nothing, which bash treats exactly
// like a failure: an empty head that the caller refuses to guess at.
func TestCurrentBranchDetachedHead(t *testing.T) {
	initRepo(t, "")
	run(t, "git", "-c", "user.email=t@example", "-c", "user.name=t",
		"commit", "-q", "--allow-empty", "-m", "empty")
	run(t, "git", "checkout", "-q", "--detach", "HEAD")
	if got := CurrentBranch(); got != "" {
		t.Errorf("CurrentBranch() = %q, want empty on a detached HEAD", got)
	}
}

// git's own stderr ("error: No such remote 'origin'") must not surface: bash
// sends it to /dev/null. Output capture is the test binary's job, so this only
// checks that the command's stderr is not wired to ours.
func TestInferRepoDiscardsGitStderr(t *testing.T) {
	initRepo(t, "")
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Skip("git unexpectedly succeeded without an origin remote")
	}
	if len(out) == 0 {
		t.Skip("git printed no diagnostic to suppress")
	}
	if _, err := InferRepo(); err == nil {
		t.Fatal("InferRepo() succeeded without an origin remote")
	}
}
