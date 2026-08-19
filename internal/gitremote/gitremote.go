// Package gitremote answers the two questions the CLI asks git about the
// current checkout: which repository it belongs to, and which branch is
// checked out.
//
// It is the only package that runs a subprocess, and it exists to keep that
// subprocess away from everything that touches the API token: nothing here
// ever sees a credential.
//
// The behaviour is a transliteration of bash infer_repo (forge line 105):
//
//	url=$(git remote get-url origin 2>/dev/null) || { ...; exit 1; }
//	url="${url%.git}"
//	echo "$url" | grep -oE '[^/:]+/[^/:]+$'
//
// and of the head-branch default in pr_create (forge line 672):
//
//	head=$(git branch --show-current 2>/dev/null || true)
package gitremote

import (
	"errors"
	"os/exec"
	"regexp"
	"strings"
)

// ErrNoOrigin reports that `git remote get-url origin` failed: not a git repo,
// no origin remote, or no git at all. bash prints
// "forge: not in a git repo and --repo not specified" and exits 1; rendering
// that message is the caller's job so this package stays free of CLI output.
var ErrNoOrigin = errors.New("git remote get-url origin failed")

// repoTail mirrors grep -oE '[^/:]+/[^/:]+$': the last two path components,
// where a component may not contain a slash or a colon. That handles
// ssh://git@host:port/owner/repo, git@host:owner/repo and https://host/owner/repo
// alike. Like grep, it simply finds nothing when the URL has no such tail.
var repoTail = regexp.MustCompile(`[^/:]+/[^/:]+$`)

// InferRepo returns the owner/repo slug taken from the origin remote URL.
//
// It returns ErrNoOrigin when git fails. It returns an empty string with a nil
// error when the URL has no owner/repo tail: bash's grep prints nothing in
// that case, leaving REPO empty, and errexit ends the run with no message at
// all. Callers reproduce that by exiting 1 silently.
func InferRepo() (string, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	// Stderr stays nil, which discards it: bash redirects it to /dev/null.
	out, err := cmd.Output()
	if err != nil {
		return "", ErrNoOrigin
	}
	// bash command substitution strips every trailing newline.
	url := strings.TrimRight(string(out), "\n")
	// "${url%.git}" strips one trailing .git, not a repeated suffix.
	url = strings.TrimSuffix(url, ".git")
	return repoTail.FindString(url), nil
}

// CurrentBranch returns the name of the checked-out branch, or "" when there
// is none to report.
//
// bash writes this as `head=$(git branch --show-current 2>/dev/null || true)`
// (forge line 672): every failure — no git, no repository, a detached HEAD —
// collapses to an empty value that the caller turns into "cannot infer PR head
// branch; use --head". There is no error to return because bash never looks at
// one.
func CurrentBranch() string {
	cmd := exec.Command("git", "branch", "--show-current")
	// Stderr stays nil, which discards it: bash redirects it to /dev/null.
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	// bash command substitution strips every trailing newline.
	return strings.TrimRight(string(out), "\n")
}
