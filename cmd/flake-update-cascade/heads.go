package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"forge.anarch.diy/allod/tools/internal/flakelock"
)

// headTimeout bounds one `git ls-remote`.
const headTimeout = 60 * time.Second

// A lockPlan is what one repository's update needs from nix once the branch
// head behind each update path has been read: pinned revisions for the paths
// that moved, written with one `nix flake lock`, and the paths the tool could
// not resolve itself, left to one `nix flake update`. A path whose head equals
// the lock, or whose declaration names a revision, needs nothing.
type lockPlan struct {
	paths      []string // every path nix is asked about, in update-path order
	overrides  []string // --override-input arguments for the paths that moved
	fallback   []string // paths nix resolves itself
	unresolved []unresolvedPath
}

// An unresolvedPath is a branch the tool tried to read and could not. It is
// also in fallback.
type unresolvedPath struct {
	path, ref, url string
}

func (p lockPlan) empty() bool {
	return len(p.paths) == 0
}

// planLock reads the branch head behind each update path and decides what nix
// has to do. resolve reads one head, reporting false when it cannot.
func planLock(lock *flakelock.Lock, paths []string, resolve func(url, ref string) (string, bool)) lockPlan {
	var plan lockPlan
	for _, path := range paths {
		src := lock.Source(path)
		switch src.Kind {
		case flakelock.SourceFixed:
		case flakelock.SourceBranch:
			head, ok := resolve(src.URL, src.Ref)
			switch {
			case !ok:
				plan.unresolved = append(plan.unresolved, unresolvedPath{path, src.Ref, src.URL})
				plan.fallback = append(plan.fallback, path)
				plan.paths = append(plan.paths, path)
			case head != src.Rev:
				plan.overrides = append(plan.overrides, "--override-input", path, src.Prefix+head)
				plan.paths = append(plan.paths, path)
			}
		default:
			plan.fallback = append(plan.fallback, path)
			plan.paths = append(plan.paths, path)
		}
	}
	return plan
}

// resolveHead reads the head of a branch over the git protocol, once per
// branch per run: a branch several repositories pin is read once, and a
// failed read is not retried. A ref advertisement costs nothing against
// GitHub's REST budget of sixty unauthenticated requests an hour per address,
// which every machine behind one address shares and which `nix flake update`
// spends one of per named GitHub input per repository just to learn that
// nothing moved.
func (c *cascade) resolveHead(url, ref string) (string, bool) {
	// A branch this run has just pushed is at the revision it pushed; no
	// remote can say otherwise, so it is not asked.
	if id, ok := repoIdentity(url); ok {
		if p, pushed := c.pushed[id]; pushed && (ref == "HEAD" || ref == p.branch || ref == "refs/heads/"+p.branch) {
			return p.rev, true
		}
	}
	key := url + "\t" + ref
	head, seen := c.heads[key]
	if !seen {
		head = lsRemoteHead(url, ref)
		c.heads[key] = head
	}
	return head, head != ""
}

// lsRemoteHead asks the remote for the refs that can carry ref, with stdin
// from /dev/null and terminal prompts off so a remote that wants credentials
// fails instead of asking, and returns the revision of the best match or "".
func lsRemoteHead(url, ref string) string {
	specs := refSpecs(ref)
	ctx, cancel := context.WithTimeout(context.Background(), headTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"ls-remote", "--exit-code", url}, specs...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	if cmd.Run() != nil {
		return ""
	}
	return pickHead(out.String(), specs)
}

// refSpecs lists the refs to ask for, most preferred first: HEAD and a full
// ref as given, anything else as a branch and then as a tag, which is how nix
// reads a bare name.
func refSpecs(ref string) []string {
	if ref == "HEAD" || strings.HasPrefix(ref, "refs/") {
		return []string{ref}
	}
	return []string{"refs/heads/" + ref, "refs/tags/" + ref}
}

// pickHead reads ls-remote output — one `<rev>\t<name>` line per ref, an
// annotated tag also listing its peeled commit as `<name>^{}` — and returns
// the revision of the first spec present, preferring the peeled commit, which
// is what a lock records.
func pickHead(output string, specs []string) string {
	revs := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		if rev, name, ok := strings.Cut(line, "\t"); ok {
			revs[name] = rev
		}
	}
	for _, spec := range specs {
		if rev, ok := revs[spec+"^{}"]; ok {
			return rev
		}
		if rev, ok := revs[spec]; ok {
			return rev
		}
	}
	return ""
}

// applyLock writes a plan: pinned revisions first with one `nix flake lock`,
// then whatever nix has to resolve with one `nix flake update`. With out set
// both write there and the second reads from there, so the working tree's
// lock is left alone, which is what a dry run needs.
func applyLock(dir, out string, plan lockPlan) error {
	if len(plan.overrides) > 0 {
		args := append([]string{"flake", "lock", dir}, plan.overrides...)
		if out != "" {
			args = append(args, "--output-lock-file", out)
		}
		if !nixWrite(args...) {
			return errors.New("nix flake lock failed")
		}
	}
	if len(plan.fallback) > 0 {
		args := append(append([]string{"flake", "update"}, plan.fallback...), "--flake", dir)
		if out != "" {
			if len(plan.overrides) > 0 {
				args = append(args, "--reference-lock-file", out)
			}
			args = append(args, "--output-lock-file", out)
		}
		if !nixWrite(args...) {
			return errors.New("nix flake update failed")
		}
	}
	return nil
}

// A pushedHead is the default branch of a repository this run pushed to and
// the revision it pushed: what every downstream pin of that branch reads from
// here on, without a network round trip.
type pushedHead struct {
	branch, rev string
}

// recordPush notes what a direct push left at the head of a repository's
// default branch — the checkout's HEAD, since preflight put the checkout on
// that branch. When git cannot say what HEAD is, the cached heads are
// forgotten instead, so the next reader goes back to the remote.
func (c *cascade) recordPush(dir, repo, branch string) {
	identity := c.identity[repo]
	if identity == "" {
		return
	}
	if rev, ok := gitCapture(dir, "rev-parse", "HEAD"); ok && rev != "" {
		c.pushed[identity] = pushedHead{branch: branch, rev: rev}
		return
	}
	c.forgetHeads(identity)
}

// forgetHeads drops every cached head of one repository, so the next
// repository that pins it reads the head this run has just pushed rather than
// the one read before the push.
func (c *cascade) forgetHeads(identity string) {
	if identity == "" {
		return
	}
	for key := range c.heads {
		url, _, _ := strings.Cut(key, "\t")
		if id, ok := repoIdentity(url); ok && id == identity {
			delete(c.heads, key)
		}
	}
}
