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
  legacy linked worktree): repo, path, branch, state. The state is the one
  thing standing between the entry and `cleanup`: `missing`, `prunable`,
  `locked`, `detached`, `submodule`, `dirty`, `unpushed`, `unknown`,
  `unsafe`, `running`, `moved` or `clean`. It never dies on an unsafe clone.
- `record -m <msg> | -M <file|-> [--files <path>...]` commits and pushes.
  Staging is exclusive: named paths and nothing else, or `git add -u` with
  no path, which a shared checkout refuses when it would sweep another
  agent's tracked edit, and a clone refuses when an untracked file is
  present. The push is additive, never forced, and retried on a branch with
  no upstream. Exit 4 when nothing changed and nothing is unpushed. Refused
  before staging: a protected branch, a checkout moved off the ledger's
  branch (exit 3), an origin that no longer matches the parent (exit 9), a
  clone the config-safety gate rejects (exit 11), a clone whose ledger file
  is locked by a running worker (exit 13), a tracked hook that differs from
  the parent's `origin/<default>` by a byte, a mode or an extra file.
- `submit -t <title> (-b <body> | -F <file|->) [--base <branch>]
  [--depends-on <text>] [--dry-run]` opens the pull request through `forge
  pr create` on PATH, with the body from the file and the `--depends-on`
  line appended; the base is the parent's default branch unless given.
  Refused: a branch with an open PR already (names `forge pr edit`), a
  detached HEAD, a missing `forge`, an unresolvable base, and every clone
  refusal `record` has. `--dry-run` prints branch, base, title and the
  assembled body and needs no `forge`. `submit` pushes nothing.
- `cleanup <path>` fetches with `--prune`, judges unpushed against the real
  remote (`ls-remote`, then ancestry; a branch absent on origin — rebase-merged
  and deleted — by patch equivalence against the default branch), and removes a
  clean, pushed clone plus its ledger entry. A `missing` directory drops the ledger
  entry and says so. Refused: dirty, unpushed, a fetch that fails, an
  unreachable remote, a populated submodule, a config-gate failure, a path
  that exists but is not a directory, a stat error other than not-exist,
  and `.git/info/grafts`, `.git/shallow` or any `refs/replace/*`.
- `run [<clone>...] [--memory <size>] -- <command...>` runs the command in
  a bubblewrap sandbox that sees the named clones read-write, the runner
  state directories read-only, an allowlist under `~/.config/git`, no
  `~/.ssh`, a cleared environment with a fixed allowlist, and a private
  `/tmp`, inside a `systemd-run --user --scope` with a memory cap. Refused:
  a path matching no ledger entry (exit 10), a trusted Nix user (exit 12),
  a runner directory that is a symlink or holds a multiply hard-linked file
  (exit 14), a readable netrc nix would expose (exit 15), a missing or
  failing `bwrap`, and a clone another command holds the lock on.

## How a user reaches it

- `allod change <command>` on any dev VM. The repository sits under
  `$WORK_DIR` (default `~/work`) with an `origin` and a resolvable
  `origin/HEAD`; `~/.config/git/protected-branches` names the protected
  repositories by origin and registered path.
- `submit` needs `forge` on PATH with a credential that can open a pull
  request on the repository's origin.
- `run` needs `bwrap`, `systemd-run --user` and an invoking user outside
  Nix's `trusted-users`.
- A sandboxed worker reaches `record`, `submit`, `list` and `cleanup` not
  at all: the overseer runs them from outside against the clone.

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
| `list` | `list prints one row per ledgered clone`; `list reports missing for a deleted clone`; `list does not die when one of several clones is unsafe`; `list reports dirty ahead of unpushed`; `list reports detached ahead of dirty`; `explicit clone list honors the ledger lock` (state `running`) |
| `record` success, push | `record commits and pushes non-protected branch`; `record commits and pushes a protected repo's clone`; `record uses additive push command`; `record never force-pushes`; `record retries push when local commit is unpushed`; `record push retry works on branch with no upstream` |
| `record` staging | `record refuses to sweep an unnamed tracked change in a main checkout`; `record refuses to commit with an untracked file present`; `record records the named untracked file`; `record ignores gitignored files`; `record still stages tracked modifications in a clone with no --files` |
| `record` empty and message | `record exits 4 when nothing changed and nothing unpushed`; `record rejects empty commit message`; `record reads commit message from file`; `record reads commit message from stdin` |
| `record` refusals | `record refuses protected branch before staging`; `record refuses a checkout moved off the branch begin handed out`; `record refuses a clone whose origin no longer matches its parent's`; `record refuses a clone whose push URL no longer matches its parent's`; `record refuses a clone whose config could run a command`; `record refuses a clone with an added executable hook`; `record refuses a clone with an added tracked hook (.hooks/pre-commit) the parent's origin does not have`; `record refuses a clone whose tracked hook was chmod +x'd, even with unchanged content`; `record refuses while the clone's own ledger file is locked`; `record refuses a symlink at .git/FETCH_HEAD before git can write through it` |
| `submit` | `submit creates PR with validation body`; `submit passes body file to forge`; `submit appends depends-on line`; `submit checks for existing PR`; `submit rejects branch with existing PR`; `submit dry-run succeeds without forge on PATH`; `submit dry-run prints assembled body`; `submit fails when forge is not on PATH`; `submit refuses a PR base it cannot resolve rather than guessing master` |
| `cleanup` | `cleanup removes a clean clone`; `cleanup does not push anything`; `cleanup refuses a clone whose only evidence of being pushed is its own local remote-tracking ref`; `cleanup succeeds once the branch is genuinely pushed`; `cleanup on a missing clone's path drops its ledger entry instead of refusing`; `cleanup unpushed failure is actionable`; `cleanup dirty failure is actionable`; `cleanup refuses rather than treat a failed 'git status' as clean`; `cleanup refuses outright the moment a replace ref exists ...`; `cleanup refuses a clone checked out on a branch its ledger does not list`; `the fixture rebase-merge really produced a hash the clone has never seen`; `list without a fetch reports the never-fetched rewrite as unknown, not clean`; `cleanup fetches, so a rebase-merged branch deleted on origin cleans up instead of dying on a missing object`; `cleanup removes the rebase-merged clone`; `a patch-equivalent relayed clone reads as landed, not unpushed` |
| `run` (second suite) | its closing `All N allod change run tests passed` line; the suite covers the mount set, the environment allowlist, every numbered refusal, the ledger lock and the scope |

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
`agent/probe`; after `record -m x -f <file>` the bare origin has
`refs/heads/agent/probe` at the clone's `HEAD`; `cleanup "$path"` then
removes the directory and the ledger file.

## What usually lies

- **A suite run without `ALLOD_UNDER_TEST` drives the deployed `allod`.**
  `tests/allod-change.sh` falls back to `command -v allod`, so a green run
  on a machine whose generation predates the change proves the old binary.
  The health check in the README is the guard.
- **`record` accepts a Claude attribution trailer in a clone.** Host-side
  git on a ledgered clone runs with `core.hooksPath=/dev/null` and global
  config off, so the tracked `commit-msg` hook that rejects
  `Co-Authored-By: Claude` never runs; the commit lands on origin with exit
  0 (allod/tools#293). Until that lands, the proof for a `record` path reads
  the pushed message back from the origin, `git -C <bare> log -1
  --format=%B <branch>`, and greps it.
- **A clone's own refs are the worker's.** `git status`, `git log
  origin/..`, `@{u}` and `refs/remotes/origin/*` inside a clone are all
  rewritable without a push, which is why `list`, `record` and `cleanup` ask
  the real remote. An inspection that reads them inside the clone can show
  a branch as pushed that the origin never saw; read the bare origin.
- **`ls` cannot see a hardlink.** Isolation is proved by comparing inodes
  of one object in the clone and the parent (`stat -c %i`), on a `$HOME`
  where both sit on one filesystem, or the flag could be lost and nothing
  would show.
- **The suite stops at its first `not ok`.** A transcript with 300 `ok`
  lines and no closing `All N ... passed` line failed, and every case after
  the failure is unproved.
- **`submit` succeeding in the suite is the mock `forge` answering.** Live,
  `forge pr create` returns `HTTP 404` when the branch was never pushed,
  because `submit` pushes nothing; only `record` does.
- **An exit status read through a pipe is the last command's.** `allod
  change record ... | tee log` reports `tee`'s 0 on a refusal; read `$?`
  directly or `PIPESTATUS[0]` (`shell.md` in allod/memory).
- **`list` printing `clean` for a legacy worktree is not the clone
  contract.** A linked worktree from before the clone model shares the
  parent's ref store, so its `clean` was judged by the parent's own
  remote-tracking refs; the row is right for what it is, but it is not
  evidence about the clone path.
