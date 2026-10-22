// Command allod manages workspace changes, patch transfer, site deploys, and
// PR explanations. The Bash implementation at the repository root remains the
// compatibility oracle until the Phase 4 cleanup in allod/tools#98.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

var (
	stdin  io.Reader = os.Stdin
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// externalNamespaceExec replaces the process with path, so its stdio,
// signals, and exit status become the external command's own.
var externalNamespaceExec = func(path string, argv []string) {
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		die(1, "could not run %s: %s", argv[0], err)
	}
}

type cliExit struct{ code int }

func exit(code int) { panic(cliExit{code: code}) }

func die(code int, format string, args ...any) {
	fmt.Fprintf(stderr, "allod: "+format+"\n", args...)
	exit(code)
}

// A namespace is one top-level command group: the word that selects it, the
// line that describes it in the usage text, and the function that runs it.
type namespace struct {
	name    string
	summary string
	main    func(args []string)
}

// namespaces is the dispatch table, and it is the whole of it: a namespace
// that is not in this slice does not exist. The core three are here; an
// optional namespace appends itself from the init() of a file its build tag
// selects, so a build that does not opt in carries neither the code nor the
// word. That is the point of the arrangement. 'allod site' on a machine that
// deploys nothing fails the way 'allod frobnicate' does — the capability is
// absent — rather than dispatching into a command that was never going to
// work and failing several minutes later for a reason the operator has to
// decode.
//
// Package-level variables are initialised before any init() runs, so the core
// namespaces are always registered first and an optional one always lists
// last, whatever the compiler's file order happens to be.
var namespaces = []namespace{
	{"change", "Manage code changes (begin, list, record, submit, cleanup)", changeMain},
	{"patch", "Transfer patches between environments (fetch, apply, receive)", patchMain},
	{"pr", "Work with pull requests (explain)", delegatePR},
	{"skill", "List and show agent skills", skillMain},
}

// registerNamespace adds one namespace to the dispatch table. It panics on a
// duplicate because that is a mistake in the program, not in its input: two
// files claiming one word would otherwise make dispatch depend on file order.
func registerNamespace(entry namespace) {
	if _, exists := lookupNamespace(entry.name); exists {
		panic("duplicate command namespace: " + entry.name)
	}
	namespaces = append(namespaces, entry)
}

func lookupNamespace(name string) (namespace, bool) {
	for _, entry := range namespaces {
		if entry.name == name {
			return entry, true
		}
	}
	return namespace{}, false
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) (code int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if e, ok := recovered.(cliExit); ok {
				code = e.code
				return
			}
			panic(recovered)
		}
	}()

	if len(args) == 0 {
		fmt.Fprint(stdout, usageText())
		return 1
	}

	if entry, ok := lookupNamespace(args[0]); ok {
		entry.main(args[1:])
		return 0
	}

	// "-h"/"--help" and "" must not become a PATH lookup for "allod---help"/"allod-".
	if args[0] != "" && !strings.HasPrefix(args[0], "-") {
		if path, err := exec.LookPath("allod-" + args[0]); err == nil {
			externalNamespaceExec(path, append([]string{"allod-" + args[0]}, args[1:]...))
			return 0
		}
	}

	switch args[0] {
	case "-h", "--help":
		fmt.Fprint(stdout, usageText())
	default:
		die(1, "unknown command namespace: %s", args[0])
	}
	return 0
}

// usageText renders the top-level usage from the dispatch table, so a build
// advertises exactly the namespaces it carries and no others.
func usageText() string {
	var text strings.Builder
	text.WriteString("Usage: allod <namespace> <command> [options]\n\nNamespaces:\n")
	for _, entry := range namespaces {
		fmt.Fprintf(&text, "  %-8s %s\n", entry.name, entry.summary)
	}
	text.WriteString("\nA namespace not listed runs allod-<namespace> from PATH.\n")
	return text.String()
}

const changeUsageText = `Usage:
  allod change begin [-d <description> | --resume <description>] [<repo-path>]
  allod change list [<repo-path>]
  allod change record -m <message> [--files <file>...]
  allod change record -M <file|-> [--files <file>...]
  allod change submit -t <title> (-b <body> | -F <file|->) [--base <branch>] [--depends-on <text>] [--dry-run]
  allod change cleanup <path>

'-d' is the isolation switch, for every repo whether protected or not: it
makes a self-contained clone under ~/changes — objects copied from this
repo, never hardlinked (a hardlink shares the parent's own inode, and the
clone's owner can chmod or overwrite through it) and never through
alternates (a parent gc can prune what that still references) — origin set
to this repo's origin, checked out on a new agent/<description> branch cut
from origin/<start>, and records the clone in this repo's own ledger
(.git/allod-changes), the only place the clone's parent is trusted from; see
docs/allod-change.md. Without -d, begin prints
the shared checkout path and creates nothing, which is the in-place flow for
committing to a repo's default branch. A description already claimed by a
clone this repo's ledger still lists, or by a worktree from before this
change, is refused.

'--resume' picks up an agent/<description> branch that already exists on
origin: the same clone under ~/changes, on a local branch tracking the
remote one, so record and submit continue it. It refuses a branch that is
missing on origin or already claimed locally, and cannot be combined with -d.

A worktree 'begin -d' made before this change keeps working in 'list',
'record', and 'cleanup' until it drains; 'begin' makes no new ones.

'list' prints one tab-separated row per clone this repo's ledger lists, plus
one per remaining linked worktree from before this change — repo, path,
branch, state — where the state is the one thing standing between that entry
and 'cleanup': missing, prunable, locked, detached, submodule, dirty,
unpushed, unknown, unsafe, running, moved, or clean. 'missing' and 'prunable'
both mean the directory is gone; 'cleanup' on a 'missing' clone drops its
ledger entry directly and says so, since there is no admin side-channel to
clear first, while 'prunable' is a worktree whose admin entry 'git worktree
prune' still clears. 'unsafe' means the clone fails the config-safety gate
described below, or its own origin no longer matches the parent's (checked
the same non-dying way, with no git command run); 'list' never dies on
either — its row is printed with state 'unsafe', and listing continues to
the next clone. 'running' means the clone's ledger file is currently locked
by something else (almost always a 'run' still sandboxing a command inside
it), so 'list' takes no lock and runs no git command against it either.
'moved' means the clone itself is not checked out on the ledger's own
branch. Both exist so 'clean' keeps meaning exactly "'cleanup' would succeed
here right now".

'cleanup' treats a clone as 'missing' only when checking it reports exactly
that (os.IsNotExist). Any other error checking it — permission denied, or a
component of its path turning out not to be a directory, among them —
refuses with that error's own text rather than guess the clone is gone and
drop its ledger entry on a guess; so does a path that exists and stats
cleanly but is a plain file, not a directory, which is "something else is
there", never "missing", even though checking it raised no error at all.

'--files' takes one or more paths and may be repeated, so '--files a b' and
'--files a --files b' both stage a and b. It stops at the next option, so it
can go anywhere in the line; pass a path that begins with '-' after '--',
which ends option parsing. With no path named, record stages every tracked
change — safe in a clone or worktree from 'begin -d', which belongs to one
agent alone, but in a shared checkout that would include another agent's, so
there record refuses and names the paths; pass --files to say what to stage.

'record' and 'submit' also refuse, with exit code 3, if the checkout was
moved to a different branch: for a ledgered clone the trusted branch is
always the parent's ledger entry, never anything inside the clone itself,
including its own handoff file; a legacy worktree, which carries no ledger
entry, is still judged by the handoff file 'begin' wrote. 'cleanup' applies
the same ledger check to a clone before it judges or discards anything on
whatever happens to be checked out. For a ledgered clone, 'list', 'record',
and 'cleanup' judge "unpushed" against the real remote directly, never a
local remote-tracking ref or the clone's own configured upstream (both a
worker's to rewrite without ever pushing anything): 'git -C <clone>
ls-remote --exit-code origin refs/heads/<ledger branch>' gives the
branch's actual head there (absent means nothing pushed under that branch
name yet), and the branch counts as pushed only once 'git merge-base
--is-ancestor HEAD <remote sha>' holds. 'cleanup' also refuses, naming the
failure, on any git error along the way — that ls-remote, that
merge-base, or its own 'git status --porcelain' dirty check — never
reading a failure as clean or pushed.

A repo is protected by what its 'origin' names, not by where it sits: if
origin names a repo ~/.config/git/protected-branches lists and the checkout is
not at that entry's path, 'begin' and 'record' refuse on every branch, naming
the expected and the actual path. Move the checkout there. A repo whose origin
matches no entry is unprotected. 'record' and 'submit' resolve a clone to the
repo whose ledger lists it and apply that repo's protection, so a ledgered
clone is judged by its parent and never flagged as a misplaced checkout of
itself.

'record', 'submit', and 'cleanup' refuse, with exit code 9, a clone whose
origin no longer matches the repo that issued it: both the fetch and the
push URL (each with '--all', since 'git push' pushes to every configured
push URL while 'get-url --push' without '--all' shows only the first) must
be exactly one URL, equal to the parent's own fetch URL.

'begin -d' and '--resume' refuse a repository that is not under the work
directory ($WORK_DIR, default ~/work): 'list', 'record', 'submit', and
'cleanup' resolve a clone back to its parent by scanning the ledgers of
every repository under there, so a clone issued from anywhere else would
get a ledger entry none of them could ever find again.

The overseer runs git against a worker's clone from outside the sandbox,
and git itself can execute what the clone's own '.git/config' names:
credential.helper, core.hooksPath, core.fsmonitor, core.sshCommand,
core.gitProxy, core.editor, diff.external, a filter's clean or smudge
command, a merge driver, gpg.program, include.path, and includeIf.* among
them. 'list', 'record', 'submit', 'cleanup', and 'run' all refuse, with
exit code 11, before their first git command against a clone: if '.git' is
missing, not an ordinary directory (a gitfile or a symlink can point
anywhere a worker controls, whose config this gate would otherwise never
see but git still loads), or has no 'config' to read; if '.git' holds a
'commondir' or 'gitdir' file (how a linked worktree's own '.git' redirects
git to load config, refs, and hooks from another directory entirely, one
this gate never reads); if a symlink exists anywhere under '.git' at all
(not only in '.git/logs'), since git writes many files there without a
lockfile first (FETCH_HEAD, ORIG_HEAD, COMMIT_EDITMSG, packed-refs, and
reflogs among them) and a symlink in place of any of them lets an outside
command's write go through it to wherever a worker chose; if the clone
has a populated submodule or a '.git/modules' directory ('git status' in
the parent descends into one and loads its own config — sandboxed clones
do not support them; a failing 'git ls-files --stage' there is itself
unsafe, never read as 'no submodule'); if '.git/hooks' holds an
executable file not named '*.sample'; if a
'.git/config.worktree' exists; or if '.git/config' itself carries a key
outside a fixed allowlist (the core and remote.origin settings an ordinary
clone has, plus extensions.refstorage and extensions.objectformat, which
git itself writes for a reftable or sha256 repository —
extensions.worktreeconfig and any other extensions.* key stay refused;
remote.origin.fetch and core.bare are pinned the same way, to the one
value an ordinary clone has, rather than accepted with any value) —
branch.* is allowed only for the ledger's own branch, and only 'remote =
origin' and 'merge = refs/heads/<ledger branch>', the one shape 'begin'
itself sets; every other branch.* key, for that branch or any other, is
refused; if a '.git/info/grafts' file, a '.git/shallow' file, or any ref
under refs/replace/ exists ('git for-each-ref refs/replace', with the
clone's own ledger lock already held) — each rewrites a commit's own
parentage as git reports it, which merge-base --is-ancestor walks to
judge a branch pushed, so any one is refused outright, whatever it
currently points to, rather than asked to behave; or if a tracked hook is unsafe. The config is read with 'git
config --file <path> --list', which only reads the file and never runs
anything, and a non-zero exit from that read is itself unsafe, never
treated as an empty file with nothing to vet; the populated-submodule
check runs last, once the config itself has already passed every other
check, because it is a real 'git ls-files' invocation inside the clone —
unlike the '--file'-scoped config read, this one loads the clone's
'.git/config' in full, so it must never run against a clone whose config
has not yet been vetted. Every outside command also runs git with
GIT_NO_REPLACE_OBJECTS=1, so a worker's own refs/replace/<sha> inside the
clone cannot substitute a different object's content wherever that sha is
looked up (a parent commit during a merge-base walk, say) to fake a branch
as already pushed.

A tracked hook is git-hooks/protected-refs-policy's own concern, not
'.git's: that global hook dispatcher, which every runner's own hooks
under ~/.config/git/hooks point at, also runs '$repo/.hooks/<hook>' — or
the directory '$repo/.hookspath' names — straight from the clone's own
working tree, before the clone's own '.git/hooks', checking only whether
the file is executable on disk, never whether git tracks it. This gate
refuses unless: '.hookspath' is absent from the clone or byte-identical
to the parent's own working-tree '.hookspath'; every file under the
tracked hooks directory that precedence settles on is byte-identical to
the parent's own 'origin/<default branch>' blob at that path (read with
'git show', run in the parent, trusted, never the clone); that same
file's git mode, in both the clone's own index ('git ls-files --stage')
and its working-tree executable bit, matches the parent's
'origin/<default branch>' tree entry for it — a worker's own 'chmod +x'
on an otherwise byte-identical, non-executable tracked file is refused
too, since the dispatcher runs any file there by hook name, whatever its
mode; that directory holds no file the base does not; and no untracked or
ignored file exists under it. The contract this makes literally true: a
change to a tracked hook never comes from a sandboxed worker; the
overseer makes it in the parent's own change flow, the same as any other
change to the repository.

A path is resolved to its clone (and its clone to its parent's ledger
entry) by matching physical, absolute paths against every ledger under the
work directory, in pure Go — no git command runs against the target path
to do this. Only once that match decides whether the path is a clone does
this gate read its config, and only once the gate passes does 'list',
'record', 'submit', 'cleanup', or 'run' run any git command against it;
see docs/allod-change.md for the contract this gate is enforcing.

'record', 'submit', 'cleanup', and 'list' also take an exclusive,
non-blocking flock on the clone's own ledger file (the two-line file
under the parent's .git/allod-changes that names the clone and its
branch) before their first git command against it, and hold it until
they are done ('list' holds it through every one of its own git commands
for that row — the config gate's, the origin check's, the branch check's,
and cloneState's own 'git status'/'git ls-remote' alike — one row at a
time, never only probed and released up front); 'run' takes that same
exclusive lock first too, for its own pre-flight (the config gate, the
tracked-hooks check, the branch check), then converts it to shared,
atomically (a second flock call on the same descriptor converts the
lock's own type rather than releasing and reacquiring it), for the whole
life of the sandboxed command. Two concurrent 'run's over the same clone
serialize their pre-flight rather than race each other's git commands. A
command that cannot get the lock it needs refuses, with exit code 13,
naming the clone, rather than wait: outside commands act on a clone only
when nothing is running inside it, and a file-level lock either succeeds
or it does not, closing the race a systemd-scope query could not — no
scope name, unit, or session bus to list or parse back apart, and no
window between asking "is anything running" and finding out for sure.

`

const patchUsageText = `Usage: allod patch <command> [options]

Commands:
  fetch    Fetch patches from a remote source repo via SSH
  apply    Apply fetched patches to a local destination repo
  receive  Fetch and apply patches in one step

  allod patch fetch <ssh-host>:<source-repo> [--base <ref>] [--fallback-base <sha>] [--output <dir>]
  allod patch apply <artifact-dir> [--repo <destination-repo>]
  allod patch receive <ssh-host>:<source-repo> <destination-repo> [--base <ref>]

'--base <ref>' sets the base ref for the patch range (default: source branch
upstream, same-named origin branch, or origin default branch; if none of
those exist, fall back to '--fallback-base' and then to export from root).
'--fallback-base <sha>' is used only when '--base' and the default
resolution both find nothing, and only if the commit exists in the source
and is an ancestor of its HEAD; 'receive' sets this to the destination's own
HEAD automatically, so a source with no remote at all — a host-provided
checkout, every repository on a forge-less dev VM — still relays by shared
history instead of exporting from root.
'--output <dir>' sets the local directory for a fetched artifact (default:
auto-generated in /tmp).
'--repo <destination-repo>' sets the apply destination (default: current
directory).

Repo arguments: a <destination-repo> (and apply's --repo) that is absolute
or begins with '~', '.', or '..' is a path; anything else is looked up in
the repository registry first and, when no entry matches, treated as a
relative path. A <source-repo> is stricter, since it names a path on the
SOURCE machine: only an absolute path there, a '~/'-prefixed path there, or
a registry id is accepted, because a path relative to a remote working
directory has no meaning over SSH; anything else fails with "source repo
must be an absolute path or a registry id". A registry-id source is sent to
the remote as work/<checkout> and resolved against that machine's own
$HOME, so it relies on the source checkout living at ~/work/<checkout>
there; a source machine with a different WORK_DIR must be named by path.
`
