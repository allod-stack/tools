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
  allod change run [<clone-path>...] [--memory <size>] -- <command...>

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
branch, state. A legacy worktree's state is the one thing standing between
it and 'cleanup': missing, prunable, locked, detached, submodule, dirty,
unpushed, unknown, or clean. A clone's state instead comes from the forge,
never from its own disk (see "Inside and outside the sandbox" below):
missing, running, unsubmitted, open, merged, closed, or unknown.

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
change — safe inside a sandboxed worker's own clone or a legacy worktree
from 'begin -d', which belongs to one agent alone, but in a shared checkout
that would include another agent's, so there record refuses and names the
paths; pass --files to say what to stage.

'record' and 'submit' also refuse, with exit code 3, if the checkout was
moved to a different branch: the trusted branch is always the one
'allod change begin' handed out, in the handoff file at
'.git/allod-change-branch', never whatever is checked out when the worker
switched branches inside its own clone after that. A repo is protected by
what its 'origin' names, not by where it sits: if origin names a repo
~/.config/git/protected-branches lists and the checkout is not at that
entry's path, 'begin' and, outside the sandbox, 'record' refuse on every
branch, naming the expected and the actual path; move the checkout there.
A repo whose origin matches no entry is unprotected.

'begin -d' and '--resume' refuse a repository that is not under the work
directory ($WORK_DIR, default ~/work): 'list', 'record', 'submit', and
'cleanup' resolve a clone back to its parent by scanning the ledgers of
every repository under there, so a clone issued from anywhere else would
get a ledger entry none of them could ever find again.

Inside and outside the sandbox: no command outside the sandbox ever runs
git in a worker's clone any more, not even the branch check 'run' itself
makes before sandboxing, which reads the clone's own .git/HEAD directly
rather than running git. Outside, 'record' and 'submit'
refuse a clone this repo's ledger lists, with exit code 11, before any git
command and before any lock: the worker records and submits its own clone
from inside its own sandbox, not the overseer from outside — run 'allod
change run <clone> -- allod change record ...' (or 'submit ...'). Inside
the sandbox, ALLOD_CHANGE_SANDBOX=1 — set only by 'run', in the sandboxed
environment it builds, never on the pass-through allowlist, so nothing
outside can set it — tells 'record' and 'submit' they are that worker:
they resolve the repository as an ordinary one (the parent's ledger is
invisible inside anyway), skip the misplaced-checkout refusal above (exit
8) and the shared-checkout sweep refusal above, and otherwise behave
exactly as they do for any repository: branch protection by origin, the
moved-branch refusal (exit 3) from the clone's own handoff file, an
additive 'git push -u origin HEAD', and 'forge pr create' with the bound
token.

'cleanup' and 'list' never run git in a clone either: a clone's
removability, and its row in 'list', both come from 'forge pr find-by-head
-s all <branch>', asked with the parent repository as the forge's working
directory, never the clone's own disk. No pull request for the branch:
'cleanup' refuses, naming the worker's own submit command; 'list' prints
'unsubmitted'. Open: 'cleanup' refuses; 'list' prints 'open'. Merged or
closed: 'cleanup' removes the clone directory and drops its ledger entry;
'list' prints the state. The forge not answering is a refusal naming the
error for 'cleanup', or 'unknown' for 'list'. 'cleanup' still takes the
clone's own ledger lock first — the same two-line file under the parent's
.git/allod-changes that names the clone and its branch — and refuses with
exit code 13, naming the clone, while a running worker holds it; a
'missing' clone drops its ledger entry without asking the forge anything.

'run' runs <command> inside a bubblewrap sandbox that can reach nothing but
the named clones, never falling back to an unsandboxed run: a path that
matches no ledger entry — including one that is not a repository at all —
is refused with exit code 10 without any git command running against it;
unlike 'record', 'submit', and 'cleanup', 'run' never falls back to
resolving a plain, unledgered repo, since it only ever sandboxes a
ledgered clone. A checkout not on the 'agent/*' branch its ledger lists is
refused the same way, the branch read from the clone's own .git/HEAD
rather than from git. A missing 'bwrap' on PATH, or one that fails to
start, is refused rather than skipped. With no <clone-path>, the clone is
resolved from the working directory, the way 'record' does. Everything
after '--' is the command and its own arguments, unexamined; a following
option still ends the <clone-path> list, and '--' itself is required even
with one clone.

'run' also refuses, with exit code 12, when the invoking user is one of
Nix's own trusted-users ('nix config show trusted-users', by name, by
'@group', or by a bare '*' entry, which trusts every user and so always
matches), that query run with NIX_CONFIG and NIX_CONF_DIR removed from
the environment entirely and NIX_USER_CONF_FILES forced to an explicit
empty value, so the invoking user cannot make it report anything but the
system-wide nix.conf only root controls — NIX_CONFIG can inline an
arbitrary override outright, NIX_CONF_DIR can point the system-config
lookup itself at a directory the invoking user controls, and simply
leaving NIX_USER_CONF_FILES unset falls back to nix's own default search
list, which includes the invoking user's own, ordinarily user-writable
nix.conf: the daemon accepts privileged build options from a trusted uid,
including post-build-hook, which it runs as root, so binding the daemon
socket into the sandbox would hand a worker root on the VM. allod/vm#12
removes the trust fleet-wide; until then, this refuses rather than open
that path. The query's stdout and stderr are captured separately and only
stdout is ever parsed as the answer, since nix routinely writes warnings
to stderr even on a successful exit and merging the two could let a
stray word in that noise (a username, a group, or '*') get read as part
of the trusted-users list itself. A 'nix config show trusted-users'
query that exits non-zero, or prints nothing this parses as a value, is
itself a refusal (exit 1) naming the error, never read as "no trusted
users" and let through — the same is true of a failure to resolve the
invoking user's own account or group membership to compare against that
list. It refuses with exit code 14,
rather than follow it, when ~/.claude, ~/.codex, ~/.pi, ~/.claude.json,
~/.gitconfig, or ~/.config/git is itself a symlink: binding whatever it
resolves to as if it were the real thing is not this command's call to
make silently.

'run' also refuses, with exit code 14, when any regular file anywhere
under a runner's whole base directory (~/.claude, ~/.codex, or ~/.pi) has
more than one hard link — not only under the writable subdirectory bound
read-write on top (~/.claude/projects, ~/.codex/sessions, ~/.codex/log,
~/.pi/agent/sessions). A hard link under the base but outside that
writable subdirectory (one planted directly under ~/.claude, say) still
aliases whatever it links to, wherever else that is bound read-only; the
check covers the whole base, not just where the write risk is obvious.
The symlink check stays scoped to each writable subdirectory: one
resolving back inside that runner's own base directory is refused as a
second line of defense; one resolving entirely outside the base is not
refused, since a real runner artifact can be exactly that shape (Claude
Code's own ~/.claude/projects/<project>/memory, a symlink elsewhere in
$HOME). Walked before anything is bound.

'run' also refuses, with exit code 15, when 'nix config show netrc-file'
names a file the user can read, or '/etc/nix/netrc' exists and is
readable: /etc is bound whole (below), so either would be exposed inside
the sandbox. A nix that names no file does not trigger this; a 'nix
config show netrc-file' that exits non-zero, or prints something this
cannot parse as an answer, is instead a refusal in its own right (exit 1)
naming the error, never read as "names no file" and let through — the
same NIX_CONFIG/NIX_USER_CONF_FILES/NIX_CONF_DIR scrub and stdout-only
capture the trusted-users query uses covers this query too. A root-only
file under /etc (an sshd host key,
say) is protected by its own mode bits, not by this check or by /etc's
absence, and this does not refuse on its account.

The command runs inside 'systemd-run --user --scope --quiet -p
MemoryMax=<size>' (default 2G, set with --memory), named 'allod-run-' plus
every named clone's own branch (each with '/' replaced by '-'), joined
with ':' in the order given ('+' is not a legal systemd unit-name
character), plus a random suffix, so 'systemctl --user
list-units allod-run-*' lists every run for any clone's swarm regardless
of how many clones one run spans, and two runs over the same clone set
never collide — this naming is purely for an operator to inspect or kill a
scope by hand; no outside command's own refusal depends on querying
systemctl, which 'cleanup's own ledger lock (above) does instead.
Exceeding the memory cap kills the scope and only the scope; the
command's own exit status is this process's exit status either way.

Inside the sandbox: every named clone is read-write at its real path;
~/.gitconfig and every registry repository flagged "memory": true are
read-only at their real paths, with an empty tmpfs mounted over that
checkout's own '.git' on top, so no ledger or history of a memory repo
(including its own '.git/allod-changes') is visible inside. The VM's own
forge SSH identity is bound read-only, by name, never ~/.ssh as a whole:
'~/.ssh/config', '~/.ssh/known_hosts', and every 'IdentityFile'
'~/.ssh/config' names (a leading tilde, and ssh's own home-directory
token, expanded to $HOME, bound along with its '.pub' sibling when
present), each with '--ro-bind-try' so a fixture
$HOME without any of them still runs. Under ~/.config/git/ only an
allowlist is bound read-only, never an exclusion list: 'config', the
'hooks' directory (walked recursively), 'protected-refs-policy',
'protected-branches', 'signing-required-branches',
'allowed-external-remotes', 'forge-hosts', 'active-pr-branches', and
'forgejo-token' — the worker's own forge credential, the author account's,
bound exactly like any other allowlisted name — and nothing else under
~/.config/git is bound, whatever its own name: 'forgejo-verifier-token'
(the merge token, the verifier account's, never the sandbox's) stays
absent by never being on the list. A symlink's own target must itself be
allowlisted too, not merely somewhere physically under ~/.config/git:
'hooks/x -> ../some-other-credential' resolves inside the tree, but if
'some-other-credential' is not itself one of the allowlisted names, it is
never walked on its own either, so this walk skips any symlink, at any
depth, whose resolved target is not itself an allowlisted name (or, for
one inside the Nix store — /nix/store, already read-only and
world-readable, bound whole at /nix regardless of this walk, and exactly
where every symlink a home-manager managed ~/.config/git resolves to —
anywhere under it) — and never follows a symlinked directory into one.

Each runner's state directory (~/.claude, ~/.codex, ~/.pi) is bound
read-only as a whole, with only its own session and log directories bound
read-write on top, so a worker can read but never rewrite the overseer's
own runner configuration (hooks, MCP servers, extensions) for it to run
unsandboxed later; docs/allod-change-run.md carries the table of which
directories are which, per runner, and the rule that a new runner needs
its own row there. ~/.claude.json is handled differently: it is copied to
a private temporary file at the start of the run, which is bound
read-write at ~/.claude.json inside the sandbox and discarded when the run
ends, so a worker's writes to it never reach the real file.

Nothing else of $HOME is visible, and ~/.ssh is never bound as a whole
directory — only the files named above. /nix, /etc,
/run/current-system, /bin, and /usr are read-only, plus /run/wrappers if
it exists; /etc is bound whole, so a root-only file under it is protected
by its own mode bits, not by absence — see the netrc refusal above for the
one case this command checks for itself rather than leave to mode bits.
The Nix daemon socket is bound read-write on top of the read-only /nix so
builds and checks still go through it — required, not skipped if absent,
since a dev VM always has it and a sandbox that silently cannot reach the
daemon is the failure this command refuses to hide. The sandboxed command
starts a new session ('bwrap --new-session'),
so it has no controlling terminal and cannot inject input into the one
that launched it (bubblewrap's own README names TIOCSTI as the reason).
The working directory starts at the first clone; the invoker's own
stdin/stdout/stderr pass through unchanged, so a redirect the invoker
chose (stdin from a file, say) is visible inside — that channel, and
what goes through it, is the invoker's own responsibility, not this
command's. The sandbox's environment starts cleared ('bwrap
--clearenv'), not inherited: a dev VM's environment can carry session
tokens (a messaging token has been seen there), and "inherit everything"
would hand a worker whatever happens to be in it. Only a fixed allowlist
passes through, each as the caller's own environment set it: HOME, PATH,
TERM, LANG, LC_ALL, any LC_* variable, USER, LOGNAME, SHELL, TZ,
COLORTERM, and NIX_REMOTE if set; TMPDIR is always set to '/tmp', the
sandbox's own private tmpfs, and ALLOD_CHANGE_SANDBOX is always set to
'1' (see "Inside and outside the sandbox" above) — neither is on the
allowlist, so only 'run' itself ever sets either one. The allowlist is
fixed in code, not a flag: a variable that turns out to be needed is a
code change with a reason, not a bypass on the command line. Git identity
inside comes from the bound ~/.gitconfig, never from a GIT_* environment
variable, which the allowlist does not carry. See docs/allod-change-run.md
for the mount table and what the sandbox does not do.
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
