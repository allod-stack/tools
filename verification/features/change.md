# change

`allod change` is how every agent change moves: an isolated clone, a commit
pushed from it, a pull request, and the clone reclaimed. The long form of
each contract is `docs/allod-change.md` and `docs/allod-change-run.md`; this
file says what to drive and what the result must look like.

## What exists

- `begin -d <description> <repo>` makes a self-contained clone under
  `~/changes`: objects copied with `git clone --no-hardlinks`, no
  `objects/info/alternates`, `origin` repointed at the parent's own origin
  URL and fetched, `origin/HEAD` re-resolved, a new `agent/<description>`
  branch cut from `origin/<default>`, the handoff file
  `.git/allod-change-branch` written, and one ledger entry
  `.git/allod-changes/<description>` in the parent (path, then branch),
  claimed with `O_EXCL` before the clone is made. It prints the clone path.
  Refused: a repository outside `$WORK_DIR`, one with no `origin`, one whose
  `origin/HEAD` is unresolved, a base branch missing on origin, a bad
  description, a description the ledger already claims, an `agent/<desc>`
  branch that already exists locally or on origin, and a protected checkout
  not at its registered path.
- `begin <repo>` with no `-d` prints the shared checkout path and creates
  nothing; a protected repository refuses it and names `-d`.
- `begin --resume <description> <repo>` rebuilds the clone from
  `origin/agent/<description>` with upstream tracking; refused when that
  branch is missing on origin, already checked out locally, or combined with
  `-d`.
- `list [<repo>]` prints one tab-separated row per ledgered clone (and per
  legacy linked worktree): repo, path, branch, state. A clone's state comes
  from the forge, never its own disk: `missing`, `running`, `unsubmitted`,
  `open`, `merged`, `closed`, or `unknown` (the forge did not answer). A
  legacy worktree keeps `missing`, `prunable`, `locked`, `detached`,
  `submodule`, `dirty`, `unpushed`, `unknown`, or `clean`. It never dies on
  one bad row.
- `record -m <msg> | -M <file|-> [--files <path>...]` commits and pushes.
  Staging is exclusive: named paths and nothing else, or `git add -u` with
  no path, which a shared checkout refuses when it would sweep another
  agent's tracked edit, and (inside the sandbox) a clone refuses when an
  untracked file is present. The push is additive, never forced, and
  retried on a branch with no upstream. Exit 4 when nothing changed and
  nothing is unpushed. Outside the sandbox, `record` refuses a clone this
  repo's ledger lists outright (exit 11), before any git command, naming
  `allod change run`. Inside the sandbox (`ALLOD_CHANGE_SANDBOX=1`, set
  only by `run`), refused before staging: a protected branch (exit 2), a
  checkout moved off its own handoff branch (exit 3).
- `submit -t <title> (-b <body> | -F <file|->) [--base <branch>]
  [--depends-on <text>] [--dry-run]` opens the pull request through `forge
  pr create` on PATH, with the body from the file and the `--depends-on`
  line appended; the base is the parent's default branch, or (inside the
  sandbox, for a clone) the clone's own, unless given. Refused: a branch
  with an open PR already (names `forge pr edit`), a detached HEAD, a
  missing `forge`, an unresolvable base, and outside the sandbox every
  ledgered clone outright (exit 11). `--dry-run` prints branch, base, title
  and the assembled body and needs no `forge`. `submit` pushes nothing.
- `cleanup <path>` asks `forge pr find-by-head -s all <branch>` (run with
  the parent as `forge`'s working directory) and removes the clone plus
  its ledger entry once the pull request is merged or closed. A `missing`
  directory drops the ledger entry and says so, without asking the forge
  anything. This includes a patch-equivalent or rebase landing under a new
  hash followed by deletion of the source branch: the forge's all-state PR
  result is primary. No git command ever runs in the clone. Refused: no pull
  request yet (names the worker's own submit command), an open one, the
  forge not answering, a path that exists but is not a directory, a stat
  error other than not-exist, and a clone another command holds the
  ledger lock on (exit 13).
- `run [<clone>...] [--memory <size>] -- <command...>` runs the command in
  a bubblewrap sandbox that sees the named clones read-write, the runner
  state directories read-only, the VM's own forge SSH identity and token
  bound by name read-only, an allowlist under `~/.config/git`, no `~/.ssh`
  as a whole directory, a cleared environment with a fixed allowlist plus
  `ALLOD_CHANGE_SANDBOX=1`, and a private `/tmp`, inside a `systemd-run
  --user --scope` with a memory cap. Refused: a path matching no ledger
  entry or not on its ledger's `agent/*` branch (exit 10), a trusted Nix
  user (exit 12), a runner directory that is a symlink or holds a multiply
  hard-linked file (exit 14), a readable netrc nix would expose (exit 15),
  a missing or failing `bwrap`, and a clone another command holds the
  lock on.

## How a user reaches it

- `allod change <command>` on any dev VM. The repository sits under
  `$WORK_DIR` (default `~/work`) with an `origin` and a resolvable
  `origin/HEAD`; `~/.config/git/protected-branches` names the protected
  repositories by origin and registered path.
- `submit` needs `forge` on PATH with a credential that can open a pull
  request on the repository's origin; `cleanup` and `list` need the same
  binary, read-only, to ask about a clone's branch.
- `run` needs `bwrap`, `systemd-run --user` and an invoking user outside
  Nix's `trusted-users`.
- A sandboxed worker reaches `record` and `submit` from inside its own
  sandbox alone, with the VM's own forge SSH key and token `run` bound in;
  the overseer reaches `list` and `cleanup` against the clone from
  outside, and `record` and `submit` not at all from there — a ledgered
  clone refuses them outright (exit 11) and names the worker's own
  `allod change run` invocation instead.

## How to drive it

The suite is the driver. In the clone at `$HEAD`, after the README's launch
and health check:

```
bash tests/allod-change.sh          # with ALLOD_UNDER_TEST exported
nix develop -c bash tests/allod-change-run.sh
```

The first runs every path below except `run`'s against the built binary in
a temporary `$HOME` with bare fixture origins; the second builds a
mock-nix test binary from the tree and drives `run`'s sandbox on the VM
(it is not in `allod check` because the Nix build sandbox has no `/etc`,
`/run/current-system` or `/usr` to bind). Each passes only when its closing
line reads `All N allod change tests passed` and `All N allod change run
tests passed`. The expected observable result per path is the `ok` line
named here, by description (numbers shift as cases are added):

| Entry point and path | Expected `ok` line |
| --- | --- |
| `begin -d` success, isolation | `begin -d creates a standalone clone, not a linked worktree`; `begin -d never makes an alternates clone`; `begin -d copies the parent's objects rather than hardlinking them` (inode compare); `begin -d leaves the shared checkout clean`; `the ledger entry's first line is the clone's path`, `... second line is the clone's branch` |
| `begin -d` origin reset | `begin -d refuses a repo with no origin remote`; `begin -d refuses an unprotected repo with no origin/HEAD`; `begin -d refuses a base branch that is missing on origin`; `submit dry-run succeeds for a clone whose own origin/HEAD was moved` (base read from the parent) |
| `begin -d` refusals | `begin -d refuses a repository outside the work directory`; `begin -d refuses a description an existing ledger claim already holds`; `begin rejects an existing local agent branch`; `begin rejects an existing remote agent branch`; `begin -d refuses a misplaced protected checkout` |
| `begin` without `-d` | `begin without -d prints the shared checkout path`; `begin without -d creates no worktree`; `begin requires -d on protected repo`; `begin requires -d in a --separate-git-dir protected repo` |
| `begin --resume` | `begin --resume picks up an existing remote agent branch`; `begin --resume refuses a branch that is missing on origin`; `begin --resume refuses a branch already checked out locally`; `begin refuses -d and --resume together` |
| `list`, clone states, from the forge | `list row carries repo, path, branch, and state: a fresh clone with no pull request yet is unsubmitted ...`; `list reports open for a branch with an open pull request`; `list reports merged after a patch-equivalent landing deletes the pull request's branch`; `list reports closed for a branch with a closed, unmerged pull request`; `list reports unknown when the forge does not answer`; `list reports missing for a deleted clone`; `list reports running, never asks the forge at all, while the clone's own ledger file is locked` |
| `record`/`submit`, outside the sandbox | `record refuses a ledgered clone from outside the sandbox`; `the outside record refusal names the worker's own sandboxed command`; `no git command runs against the clone before the outside refusal`; `submit refuses a ledgered clone from outside the sandbox`; `record refuses the clone from outside the sandbox even when $HOME is a symlink` |
| `record`/`submit`, inside the sandbox | `record inside the sandbox commits and pushes a protected repo's clone`; `the bare origin, not the clone, shows the pushed commit's message`; `record inside the sandbox refuses a checkout moved off its own handoff branch`; `record inside the sandbox still refuses a protected branch`; `submit --dry-run inside the sandbox resolves the base from the clone's own origin/HEAD` |
| `record` success, push (legacy and shared checkout) | `record commits and pushes non-protected branch`; `record commits and pushes a protected repo's clone`; `record uses additive push command`; `record never force-pushes`; `record retries push when local commit is unpushed`; `record push retry works on branch with no upstream` |
| `record` staging | `record refuses to sweep an unnamed tracked change in a main checkout`; `record refuses to commit with an untracked file present`; `record records the named untracked file`; `record ignores gitignored files`; `record inside the sandbox still stages tracked modifications in a clone with no --files` |
| `record` empty and message | `record exits 4 when nothing changed and nothing unpushed`; `record rejects empty commit message`; `record reads commit message from file`; `record reads commit message from stdin` |
| `submit` | `submit creates PR with validation body`; `submit passes body file to forge`; `submit appends depends-on line`; `submit checks for existing PR`; `submit rejects branch with existing PR`; `submit dry-run succeeds without forge on PATH`; `submit dry-run prints assembled body`; `submit fails when forge is not on PATH`; `submit refuses a PR base it cannot resolve rather than guessing master` |
| `cleanup`, from the forge | `cleanup refuses a clone whose branch has no pull request yet`; `cleanup refuses a clone whose pull request is still open`; `cleanup removes the branch-deleted clone whose pull request is merged`; `cleanup never ran the clone's own core.hooksPath hook`; `cleanup removes a clone whose pull request is closed without merging`; `cleanup refuses when the forge does not answer`; `cleanup on a missing clone's path drops its ledger entry instead of refusing`; `cleanup refuses while the clone's own ledger file is locked` |
| `cleanup`, legacy patch fallback | `list reports clean for a legacy worktree whose deleted branch landed patch-equivalently`; `cleanup succeeds where list reported clean: a legacy worktree whose deleted branch landed patch-equivalently` |
| `cleanup`, path errors | `cleanup refuses rather than treat a non-ENOENT stat error as missing`; `cleanup refuses when the clone path exists but is not a directory`; `cleanup regular repo failure is actionable` |
| `run` (second suite) | its closing `All N allod change run tests passed` line; the suite covers the mount set, the credential binds, the environment allowlist, every numbered refusal, the ledger lock and the scope |

A path a change could affect that has no `ok` line here is driven by hand
against `$ALLOD_UNDER_TEST` with this fixture, and the proof row names the
command and its output:

```
T=$(mktemp -d); export HOME=$T/home
mkdir -p $HOME/.config/git $HOME/work $T/remotes
touch $HOME/.config/git/protected-branches
git config --global user.name T; git config --global user.email t@example.invalid
git init -q --bare $T/remotes/fx.git
git init -q -b master $HOME/work/fx && cd $HOME/work/fx
echo a > a && git add a && git commit -qm initial
git remote add origin $T/remotes/fx.git && git push -q -u origin master
git remote set-head origin -a
path=$("$ALLOD_UNDER_TEST" change begin -d probe $HOME/work/fx)
# ... drive the path in "$path"; inspect $T/remotes/fx.git and $HOME/work/fx/.git/allod-changes/probe
rm -rf "$T"
```

Expected from that fixture: `begin -d` prints a path under `$HOME/changes`;
`stat -c %i` of one object under `$path/.git/objects` differs from the
parent's; `cat $HOME/work/fx/.git/allod-changes/probe` is the path then
`agent/probe`; after `ALLOD_CHANGE_SANDBOX=1 record -m x -f <file>` (run
from outside the sandbox this way only stands in for a worker inside
one — plain `record` refuses the clone outright, exit 11) the bare origin
has `refs/heads/agent/probe` at the clone's `HEAD`; `cleanup "$path"`
refuses with exit 1 (`no pull request yet`) until `forge` reports one
merged or closed for `agent/probe`; drop `$path` and the ledger entry by
hand instead to finish the probe.

## What usually lies

- **A suite run without `ALLOD_UNDER_TEST` drives the deployed `allod`.**
  `tests/allod-change.sh` falls back to `command -v allod`, so a green run
  on a machine whose generation predates the change proves the old binary.
  The health check in the README is the guard.
- **`ALLOD_CHANGE_SANDBOX=1` run by hand is not the sandbox.** Setting the
  variable directly proves `record`/`submit`'s own branch of the inside/
  outside check, not that `run` actually sets it, nor that nothing else on
  the fixed pass-through allowlist leaks it in from outside; the by-hand
  run only ever exercises the sandboxed code path, never the real
  bubblewrap boundary around it.
- **`ls` cannot see a hardlink.** Isolation is proved by comparing inodes
  of one object in the clone and the parent (`stat -c %i`), on a `$HOME`
  where both sit on one filesystem, or the flag could be lost and nothing
  would show.
- **The suite stops at its first `not ok`.** A transcript with 300 `ok`
  lines and no closing `All N ... passed` line failed, and every case after
  the failure is unproved.
- **`submit` and `cleanup` succeeding in the suite is the mock `forge`
  answering, and the mock answers by branch name alone.** Live, `forge pr
  create` returns `HTTP 404` when the branch was never pushed, because
  `submit` pushes nothing; only `record` does. The live forge also rewrites
  a landed pull request's `head.ref` to `refs/pull/<n>/head` the moment its
  branch is deleted, keeping the branch name only in `head.label`; the
  suite's mock never does that rewrite, so `cleanup` removing a clone there
  proves the mock's answer was read correctly, not that a landed branch is
  still found once the forge has stopped naming it in `head.ref`. A proof
  for `cleanup` reads `forge pr find-by-head -s all` against a real landed
  branch on the live forge.
- **An exit status read through a pipe is the last command's.** `allod
  change record ... | tee log` reports `tee`'s 0 on a refusal; read `$?`
  directly or `PIPESTATUS[0]` (`shell.md` in allod/memory).
- **`list` printing `clean` for a legacy worktree is not the clone
  contract.** A linked worktree from before the clone model shares the
  parent's ref store, so its `clean` was judged by the parent's own
  remote-tracking refs; the row is right for what it is, but it is not
  evidence about the clone path.
