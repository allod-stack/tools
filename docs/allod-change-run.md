# allod change run

Runs a command inside a bubblewrap sandbox that can reach nothing but the
clones named on the command line, the toolchain, the network, and the VM's
own forge identity bound in by name: a worker given `allod change run` and
nothing else can push its own branch and open its own pull request, and
cannot touch a sibling's tree, a shared checkout, or anything else under
`$HOME`. The mount set is specified below.

```
allod change run [<clone-path>...] [--memory <size>] -- <command...>
```

Each `<clone-path>` is resolved to a ledgered clone the way `record` resolves
one, through the parent repository's own ledger rather than the clone's own
`.git/config`; with no path, one clone is resolved from the working
directory. More than one path is the cross-repo case: every named clone is
mounted read-write, and nothing else is. Everything after `--` is the
command and its arguments, passed through unexamined; `--` is required even
with a single clone and a single command word, and a following option still
ends the `<clone-path>` list the same way it does for `record --files`.

## Refusals

A path that matches no ledger entry — including one that is not a
repository at all — is refused with exit code 10 before any git command
ever runs against it: `run` resolves a path to its ledgered clone the same
git-free way `record` and `submit`'s own outside refusal does
(docs/allod-change.md, "The ledger"), but unlike them, never falls back to
resolving a plain, unledgered repo through `git rev-parse` — `run` only
ever sandboxes a ledgered clone, so there is no reason to run git against
anything else on the way to this refusal. A checkout not on the `agent/*`
branch its ledger lists is refused the same way, confirmed before
sandboxing it by reading the clone's own `.git/HEAD` directly rather than
running git: a line `ref: refs/heads/<branch>` gives the branch, and
anything else — a detached HEAD, an unreadable file, a `.git` that is a
file rather than a directory — refuses the same way, naming what was
read. `bwrap` missing from PATH, or `bwrap` failing to start, is refused
(exit 1); `run` never falls back to an unsandboxed command.

`run` refuses, with exit code 12, when the invoking user is one of Nix's
own trusted-users — `nix config show trusted-users`, checked by name, by
`@group` against the user's own groups, and by a bare `*` entry, which
trusts every user and so always matches. The query uses
`/run/current-system/sw/bin/nix`, regardless of `PATH`, with `NIX_CONFIG`
and `NIX_CONF_DIR` removed from the environment entirely and
`NIX_USER_CONF_FILES` forced to an explicit empty value, so the invoking
user cannot make it report anything but the system-wide `nix.conf` only
root controls — the same view the daemon itself, running as root,
actually honors when it decides trust; `NIX_CONFIG` can inline an
arbitrary override outright, `NIX_CONF_DIR` can point the system-config
lookup itself at a directory the invoking user controls, and simply
leaving `NIX_USER_CONF_FILES` unset is not enough either, since `nix`'s
own default search list it would then fall back to includes the invoking
user's own, ordinarily user-writable `nix.conf`. Only the query's own
stdout is ever read as the answer: stdout and stderr are captured
separately, since `nix` routinely writes warnings to stderr even on a
successful exit, and merging the two could let a stray word in that
warning text (a username, a group name, or `*`) get parsed as if it were
part of the trusted-users list itself. A `nix config show
trusted-users` that exits non-zero, or prints nothing this parses as a
value, is itself a refusal (exit 1) naming the error — never read as "no
trusted users configured" and let through, since that reading would be
exactly wrong were the query actually failing because the invoking user
is trusted and something about that is unusual enough to upset `nix`
itself; the same is true of a failure to resolve the invoking user's own
account or group membership to compare against that list. The Nix daemon accepts
privileged build options from a trusted uid, including `post-build-hook`,
which it runs as root, so binding the daemon socket into the sandbox for
a trusted user would hand a worker root on the VM. allod/vm#12 removes
the trust fleet-wide; until then, `run` refuses outright rather than open
that path. It refuses, with exit code 14, rather than follow a symlink,
when `~/.claude`, `~/.codex`, `~/.pi`, `~/.claude.json`, `~/.gitconfig`, or
`~/.config/git` is itself a symlink: binding whatever it resolves to as if
it were the real thing is not this command's call to make silently.

`run` also refuses, with exit code 14, when any regular file anywhere
under a runner's whole base directory (`~/.claude`, `~/.codex`, `~/.pi`)
has more than one hard link — not only under the writable subdirectory
bound read-write on top (`~/.claude/projects`, `~/.codex/sessions`,
`~/.codex/log`, `~/.pi/agent/sessions`). A hard link under the base but
outside that writable subdirectory still aliases whatever it links to —
a hard link to the forgejo verifier token placed directly under
`~/.claude`, say, reachable by a path this check never previously walked,
is the same underlying inode as the real file wherever else it is bound
read-only;
narrowing the walk to only the writable subdirectories, where the write
risk is obvious, missed that the alias itself is the problem, not merely
writing through it. The symlink check stays scoped to each writable
subdirectory only: one resolving back inside that runner's own base
directory is refused, as a second line of defense against a hard link's
same aliasing risk by a different mechanism; one resolving entirely
outside the base is not refused, since a real, legitimate runner artifact
can be exactly that shape — Claude Code itself plants
`~/.claude/projects/<project>/memory` as a symlink to a project's own
memory checkout, elsewhere in `$HOME` entirely. Walked before `run` ever
binds anything.

`run` refuses, with exit code 15, when `nix config show netrc-file` names
a file the invoking user can read, or `/etc/nix/netrc` exists and is
readable. `/etc` is bound whole (see the mount set below); either file
would be exposed inside the sandbox exactly like anything else under
`/etc`, so `run` refuses to start rather than let that happen. A `nix`
that names no file does not trigger this: there is nothing to compare
against. A `nix config show netrc-file` that exits non-zero, or prints
something this cannot parse as an answer, is instead a refusal in its own
right (exit 1) naming the error — read as "cannot be answered", never as
"names no file" and let through; the same `NIX_CONFIG`/
`NIX_USER_CONF_FILES`/`NIX_CONF_DIR` scrub and stdout-only capture the
trusted-users query above applies covers this query too. A root-only file under `/etc` otherwise (an sshd host
key, say) is protected by its own mode bits, not by this check or by
`/etc`'s absence — see "What the sandbox does not do" below.

Once the sandboxed command starts, its own exit status is `run`'s exit
status.

## The scope

The command runs inside `systemd-run --user --scope --quiet -p
MemoryMax=<size> --unit <name>`. `<size>` defaults to `2G` and is set with
`--memory`. `<name>` is `allod-run-` plus every named clone's own branch
(each with `/` replaced by `-`, since branch names are not valid unit-name
characters), joined with `:` in the order given (`+` is not a legal
systemd unit-name character at all; `:` is, and a description can never
contain one, so this stays unambiguous), plus a short random
suffix so two runs over the same clone set do not collide —
`systemctl --user list-units 'allod-run-*'` lists a swarm for any clone
regardless of how many clones one run spans, for an operator to inspect or
kill by hand. Nothing else parses that listing back apart any more: the
actual guard against an outside command acting on a clone `run` still
spans is a `flock` on the clone's own ledger file (docs/allod-change.md,
"list and cleanup, from the forge"), taken and held for the whole sandboxed
command's lifetime, one clone at a time, entirely independent of the
scope's name or whether `systemd-run` is even still alive to answer a
query about it — the scope name exists for `systemctl --user` to show a
human which branches a given unit covers, not for any command's own
refusal to depend on. Exceeding the memory cap kills the scope, and only
the scope: nothing else in the VM dies, and the shell that launched `run`
is unaffected, and the ledger lock it held is released the moment its
process exits, the same as a normal exit.

`MemoryMax` is a cgroup limit on the scope: it covers the worker's own
processes, the ones `systemd-run` actually places in that cgroup. A `nix
build` the worker triggers runs on the Nix daemon's own process, outside
the scope entirely, bounded by whatever the daemon's own settings (its
own `cores`/`max-jobs`, the system's memory) allow — the cap does not
follow work the worker merely asked the daemon to do on its behalf.

## The environment

The sandbox's environment starts cleared (`bwrap --clearenv`), never
inherited: a dev VM's environment can carry session tokens unrelated to the
clone or the toolchain (a `CLAUDE_CODE_MESSAGING_TOKEN` has been seen there
in practice), and passing everything through would hand a worker whatever
happens to be set.

Only a fixed allowlist passes, each variable exactly as the caller's own
environment set it: `HOME`, `PATH`, `TERM`, `LANG`, `LC_ALL`, every `LC_*`
variable, `USER`, `LOGNAME`, `SHELL`, `TZ`, `COLORTERM`, and `NIX_REMOTE` if
set. `TMPDIR` is always `/tmp`, the sandbox's own private tmpfs, never the
caller's `TMPDIR`, and `ALLOD_CHANGE_SANDBOX` is always `1`: neither is on
the allowlist, so only `run` itself ever sets either one.
`ALLOD_CHANGE_SANDBOX=1` is how `allod change record` and `allod change
submit`, run inside, know they are the worker, not the overseer acting on
the clone from outside (`docs/allod-change.md`, "Inside and outside the
sandbox"). Git identity inside comes from the bound global Git config,
never from a `GIT_*` environment variable — the allowlist does not carry
those, by design, even if the caller's own `user.name` happens to be set
that way.

The allowlist is fixed in code, with no flag to extend it: a tool that
ships a bypass flag is a tool that ships a bypass, and a variable that
turns out to be needed is a code change with a reason, not a flag on the
command line.

## The mount set

Inside the sandbox (`bwrap --unshare-user --unshare-pid --unshare-ipc
--unshare-uts --unshare-cgroup-try --share-net --die-with-parent
--new-session --cap-drop ALL`), everything not listed here is absent.
Each namespace is unshared by its own explicit flag, not `--unshare-all`:
`--unshare-all` tries every namespace it can and silently continues if
one (the user namespace, say) cannot be created, while a named flag hard
fails the whole sandbox instead — a user namespace that fails to create
is exactly the case where falling back to running unsandboxed would be
the fail-silent case this stack refuses. `--cap-drop ALL` drops every
Linux capability the sandboxed process would otherwise have. Network is
shared, not filtered. `--new-session` means the sandboxed command has no
controlling terminal of its own and cannot inject input into the one that
launched `run` — bubblewrap's own README names TIOCSTI as the reason. The
invoker's own stdin, stdout, and stderr pass through unchanged, so a
redirect the invoker chose (stdin from a file, say) is visible inside;
that channel, and what goes through it, is the invoker's own
responsibility, not something `run` inspects or restricts.

| Path | Inside the sandbox | Why |
| --- | --- | --- |
| `/nix` | read-only | the toolchain, `nix build`/`nix flake check` included |
| `/nix/var/nix/daemon-socket/socket` | read-write, on top of the read-only `/nix` | a read-only bind of the socket still allows `connect`, but the daemon's own write side needs the read-write bind too. Required, not skipped if absent: a dev VM always has this socket, and a sandbox whose builds can silently not reach the daemon is the fail-silent case this stack refuses |
| `/etc`, `/run/current-system`, `/bin`, `/usr` | read-only | the rest of the system toolchain. `/etc` is bound whole: a root-only file under it (an sshd host key, say) is protected by its own mode bits, not by absence — `run` only refuses to start on its own account for the one case checked above, a readable netrc |
| `/run/wrappers` | read-only, if it exists | setuid wrappers (e.g. `sudo`) where the machine has them |
| `/dev`, `/proc` | fresh (`--dev`, `--proc`) | the minimum a normal process expects |
| `/tmp` | fresh tmpfs | scratch that dies with the sandbox |
| `$HOME` | fresh tmpfs, then: | everything below is bound inside this empty tmpfs; nothing else of `$HOME` is visible |
| each named clone | read-write, at its real path | the worker's own tree — the only thing it may write to |
| every registry repository flagged `"memory": true` | read-only, at its real checkout path under the work dir, with an empty `--tmpfs` over its own `.git` on top | the memory tree the worker reads first; the empty tmpfs over `.git` hides that checkout's own ledger (`.git/allod-changes`) and history — a memory checkout is bound in to be read, not to hand a worker another repo's change ledger |
| `~/.gitconfig` | read-only, when present | legacy global Git config; `~/.config/git/config` supplies it on managed VMs |
| `~/.ssh/config`, `~/.ssh/known_hosts` | read-only, `--ro-bind-try` | the VM's own forge SSH identity, by name; never `~/.ssh` as a whole directory |
| every `IdentityFile` `~/.ssh/config` names | read-only, `--ro-bind-try`, each with its `.pub` sibling when present | the SSH private key (and public key) `run` resolves out of the config it just bound, with a leading `~` and ssh's own home-directory token expanded to `$HOME`; a name that does not resolve to a regular file is silently skipped, never substituted |
| `~/.config/git/config`, managed hooks, `protected-refs-policy`, the named policy files, and `~/.config/git/forgejo-token` | read-only | Only allowlisted names are mounted. Hook files must resolve into `/nix/store`; unmanaged files under `hooks/` are skipped. A global Git config, the managed `pre-commit` hook, and `protected-refs-policy` are required before the run starts. `forgejo-token` is the worker's own forge credential, the author account's, bound exactly like any other allowlisted name; `forgejo-verifier-token`, the merge token, stays absent by never being on the list. |
| each runner's state directory (see "Runner state directories" below) | read-only as a whole, with its own session/log directories read-write on top | the runner's session state the worker needs to write, without handing it the runner's own configuration (hooks, MCP servers, extensions) to edit for the overseer's unsandboxed session to execute later |
| `~/.claude.json` | a private copy, read-write, discarded when the run ends | Claude Code's account and onboarding state. Copied to a temp file before the run and bound at `~/.claude.json` in its place, so a worker's writes to it never reach the real file — a read-only bind makes Claude Code warn, and leaving it absent makes Claude Code recreate it from scratch either way |
| `~/.ssh` as a whole directory, any netrc, every shared checkout, every other clone | absent | `~/.ssh`'s own files are bound individually, above; nothing else of it, and none of the rest of this row, is the point of the sandbox |

## Runner state directories

A runner's state directory holds both its own configuration — hooks,
MCP server definitions, extension scripts, things that execute — and its
own per-session state — session transcripts, logs, things that are only
ever data. Binding the whole directory read-write, the way an earlier cut
of this command did, lets a sandboxed worker rewrite its own runner's
configuration and have that execute later, in the overseer's own
unsandboxed session, the next time the overseer runs that runner there.
So each runner directory is bound read-only as a whole, and only its
named session/log subdirectories are bound read-write on top of that.

| Runner | Read-only (whole directory) | Read-write (on top) |
| --- | --- | --- |
| `~/.claude` | everything, including `settings.json`'s hooks and any MCP config kept there | `projects`, plus `todos` and `statsig` if present |
| `~/.codex` | everything, including `config.toml` | `sessions`, `log` |
| `~/.pi` | everything, including `agent/extensions/*.ts` | `agent/sessions` |
| `~/.claude.json` | n/a — see below | the whole file, but as a private copy, not the real one |

`~/.claude.json` is not a directory and does not fit this split: it is
Claude Code's single account/onboarding file, written to wholesale, not
by subdirectory. Instead of a read-only/read-write split, `run` copies it
to a private temporary file at the start of the run and binds that copy,
read-write, at `~/.claude.json` inside the sandbox; the copy is discarded
when the run ends, and the real `~/.claude.json` is never touched. The
discard runs explicitly on every exit path past the point the copy
exists — not only through a deferred call that would otherwise depend on
a panic correctly unwinding through every frame in between, from a
refusal deep inside `buildBwrapArgs` (an unreadable Git config source,
say) to the final exit once the sandboxed command itself has finished —
so no `allod-claude-json-*` file is ever left behind in `TMPDIR`.

A new runner needs its own row here — and its own entry in the code's
runner table (`cmd/allod/change_run.go`) — naming which of its
subdirectories are session/log state safe to hand back read-write, before
`run` binds its directory in at all. Binding a new runner's directory
whole, read-write, "to get it working" is exactly the mistake this table
exists to catch.

These writable subdirectories survive `cleanup` by design — `cleanup`
only ever removes the clone directory and its ledger entry, never
anything under `$HOME` directly — because the session log under each is
what `allod trace` reads afterward. They are also still shared between
concurrent workers' sandboxes exactly as they are between concurrent
unsandboxed sessions today, so one worker can read (and write into)
another's session directory; a per-worker session directory is a
follow-up, not this command's job.

## What the sandbox does not do

No network filtering: the worker can reach whatever the VM can reach,
including its model provider, because the sandbox shares the network
namespace (`--share-net`). A per-worker network allowlist is
allod/strategy#14's declared-capability work, out of scope here.

The runner's own provider credential stays inside the sandbox because its
state directory (`~/.claude`, `~/.claude.json`, `~/.codex`, `~/.pi`) is
bound in, not because the environment carries it — the environment is
cleared. A runner whose credential lives only in an environment variable
outside the fixed allowlist will not see it inside the sandbox. Moving
provider credentials out of the agent environment entirely is
allod/strategy#13; this command narrows what a worker can reach, not what
the runner itself holds.

A worker holding the VM's own forge SSH key and token can disable or
redirect its own clone's hooks, point `core.hooksPath`
elsewhere, add an executable of its own under `.git/hooks`, push commits
onto a sibling's `agent/*` branch, open PRs, and comment under the agent
account — nothing in the sandbox stops any of that, and nothing outside
the sandbox reads the clone's config to catch it either, because nothing
outside the sandbox runs git in a clone at all any more
(docs/allod-change.md, "Inside and outside the sandbox"). The walls this
command relies on are the forge's own server-side rules — protected
default branches, no force-push on `agent/*`, the token's existing scope
— and the overseer's review of the branch once it reaches the forge, not
a local gate reading the clone's config before acting on it.

## Store paths persist on the VM

A `nix build` or `nix flake check` a worker triggers inside the sandbox
goes through the real Nix daemon (see "The scope" above), and the daemon
builds on the VM itself, not inside anything this command tears down:
whatever lands in the store stays in the store after the sandbox, and the
scope, are both gone. That is the build channel working as designed, not
a leak this command is meant to plug — it is bounded by the daemon's own
settings (its `cores`/`max-jobs`, the system's disk and memory, garbage
collection) and by allod/vm#12, not by `allod change run`.

## Tests

`tests/allod-change-run.sh` and `tests/allod-change-run-by-hand.sh` build a test-only binary whose nix path is a link-time variable, so each case can answer the trusted-user and netrc queries itself; the production binary's path is fixed and no flag or variable moves it. Both scripts therefore run from the repository's devShell, where a Go toolchain exists: `nix develop -c bash tests/allod-change-run.sh`, and the same for the by-hand script, which also needs a real user session bus, bubblewrap and the nix daemon, which is why it is not a flake check.
