# workspace

`pull-all` and `work-diff` are the two commands every session runs before
touching anything: one syncs every checkout under `~/work`, the other shows
what is uncommitted across them. Both are bash in `workspace/`, sharing
`lib/workspace.sh`, and neither needs a build.

## What exists

- `pull-all [--switch]` walks every git repository under `$WORK_DIR`
  (default `~/work`), nested and dot-named ones included, never a linked
  worktree or a clone under `~/changes`, and pulls each current branch in
  parallel, `PULL_ALL_JOBS` at a time (default 4). One row per repository,
  in discovery order, always exit 0. Rows:
  - `skipped    [host-provided: no origin]` for a checkout with no `origin`;
  - `skipped    [dirty working tree]` when `git diff` or `--cached` is
    non-empty; untracked files do not count;
  - `up to date [<branch>]` when `git pull` printed `Already up to date`,
    `pulled     [<branch>]` otherwise;
  - `error      [pull failed: <first line of git's output>]`.
  - With `--switch`, before pulling a repository not on its default branch:
    `skipped [default branch unresolved — run: git -C <dir> remote set-head
    origin -a]`, `skipped [on <branch> — no remote tracking branch]`,
    `skipped [on <branch> — N unpushed commit(s)]`, `error [checkout
    <default> failed]`, otherwise the default branch is checked out and
    pulled.
  - `--help` prints the header comment even with an invalid
    `PULL_ALL_JOBS`; an invalid `PULL_ALL_JOBS` otherwise exits 1 naming it;
    an unknown option exits 1.
- `work-diff [--all | <repo-name>]` prints, per repository under
  `$WORK_DIR`: a banner with name and branch, `(clean)` or the porcelain
  status followed by `--- staged ---` and `--- unstaged ---` diffs, then each
  linked worktree of that repository under `↳ worktree <path> [<branch>]`
  with the same tree state, `[detached]` for a detached one, and nothing for
  one whose directory is gone. `<repo-name>` restricts it to one repository
  and its worktrees; a missing name exits 1 with `Error: '<name>' not
  found`; an unknown option exits 1 with `Unknown option`. The script works
  with `lib/workspace.sh` embedded in front of it, the shape the Nix
  packaging ships.

## How a user reaches it

- `pull-all` and `work-diff` are bare commands on PATH on every dev VM and
  the hypervisor (not `allod` subcommands). `git-workflow.md` in allod/memory
  makes `work-diff` then `pull-all` the first two commands of a session.
- `flake-status` and `flake-update-cascade` read the same
  `workspace_collect_repos` set, so a repository `pull-all` discovers is one
  they see too.

## How to drive it

The suites, from the clone at `$HEAD` (they run `workspace/pull-all` and
`workspace/work-diff` from the tree they sit in, so there is nothing to
build and nothing to point at):

```
bash tests/workspace/pull-all.sh
bash tests/workspace/work-diff.sh
```

`pull-all.sh` drives the script against a mock `git` that logs every call,
so it proves which git commands ran and which did not; `work-diff.sh` uses
real git on fixture repositories and linked worktrees. Each passes only on
its closing `✅ All N ... tests passed.` line. The expected line per path:

| Entry point and path | Expected `✅` line |
| --- | --- |
| `pull-all` clean, up to date | `default: reports an up-to-date default branch`; `default: exits 0 even with a host-provided checkout and other skips/errors present` |
| `pull-all` dirty | `default: skips a dirty working tree`; `default: never pulls dirty repos` |
| `pull-all` host-provided (no origin) | `default: skips a host-provided checkout with no origin`; `default: never pulls a host-provided checkout`; `default: a failing git remote is not treated as host-provided` |
| `pull-all` no upstream | `default: reports pull error for branch without tracking`; `--switch: skips a branch without remote tracking` |
| `pull-all` pull failure | `default: reports the first line of a pull failure` |
| `pull-all` non-default branch, no switch | `default: pulls non-default branch in place`; `default: never checks out non-default branches` |
| `pull-all --switch` unpushed | `--switch: skips a branch with unpushed commits`; `--switch: never pulls repositories that were skipped` |
| `pull-all --switch` default unresolved | `--switch: skips a repo whose default branch cannot be resolved, naming the repair`; `--switch: never checks out a repo whose default branch cannot be resolved` |
| `pull-all --switch` success | `--switch: checks out the default branch before pulling`; `--switch: pulls after switching branches` |
| `pull-all` discovery | `default: discovers and pulls a nested repository`; `default: discovers and pulls a dot-named repository`; `default: does not recurse through a dot-named non-repo directory`; `default: never enumerates worktrees` |
| `pull-all` options | `--help mentions --switch flag`; `--help works even with an invalid PULL_ALL_JOBS`; `rejects invalid PULL_ALL_JOBS`; `PULL_ALL_JOBS limits concurrent pulls` |
| `work-diff` clean, changed, untracked | `reports a clean working tree`; `shows staged porcelain status`; `shows unstaged porcelain status`; `renders staged diff content`; `renders unstaged diff content`; `shows untracked-only porcelain status`; `renders every repo without aborting on an untracked-only working tree` |
| `work-diff` worktrees | `shows a linked worktree attributed to its branch`; `renders uncommitted worktree changes`; `labels a worktree with a detached HEAD`; `skips a worktree whose directory no longer exists`; `nests a worktree under the repo that owns it` |
| `work-diff` discovery and targeting | `recursively discovers a nested repository`; `discovers a dot-named repository`; `does not recurse through a dot-named non-repo directory`; `shows the requested repository in targeted mode`; `excludes other repositories in targeted mode` |
| `work-diff` errors and embedding | `prints usage for --help`; `explains an unknown-option failure`; `explains a missing-repository failure`; `works when the shared library is embedded by Nix packaging` |

The mock `git` never diverges and never carries an unpushed commit on the
default branch, so two paths are driven by hand against real git, with the
tree's `workspace/pull-all` and a `$HOME` of fixture clones whose bare
origins sit beside them:

```
T=$(mktemp -d); export HOME=$T/home; mkdir -p $HOME/work $T/remotes
git config --global user.name T; git config --global user.email t@example.invalid
mk() { git init -q --bare $T/remotes/$1.git; git clone -q $T/remotes/$1.git $HOME/work/$1 2>/dev/null
       cd $HOME/work/$1; echo a > a; git add a; git commit -qm initial; git push -q -u origin master
       git remote set-head origin -a >/dev/null; }
mk ahead;    cd $HOME/work/ahead;    echo b > b; git add b; git commit -qm local
mk diverged; cd $HOME/work/diverged; echo b > b; git add b; git commit -qm local
             git clone -q $T/remotes/diverged.git $T/o; cd $T/o; echo c > c; git add c; git commit -qm remote; git push -q origin master
mk noup;     cd $HOME/work/noup;     git switch -qc feature; echo b > b; git add b; git commit -qm feature
workspace/pull-all; workspace/pull-all --switch
git -C $HOME/work/ahead status -sb; git -C $HOME/work/diverged status -sb
rm -rf "$T"
```

Expected today, both observed at the first sweep:

| Fixture | `pull-all` | `pull-all --switch` |
| --- | --- | --- |
| `ahead` (master, one unpushed commit) | `up to date [master]` and the commit still unpushed: `## master...origin/master [ahead 1]` | the same |
| `diverged` (master, one local and one remote commit) | `error [pull failed: <first line>]`, nothing merged: `[ahead 1, behind 1]` | the same |
| `noup` (branch with no upstream) | `error [pull failed: There is no tracking information for the current branch.]` | `skipped [on feature — no remote tracking branch]` |

The `ahead` row is the behavior allod/tools#287 changes; when it lands, the
expected row becomes `skipped [on master — 1 unpushed commit]` in both
columns and this table is updated in the same PR.

## What usually lies

- **An ahead default branch reads `up to date`.** `pull-all` judges only
  what `git pull` printed, so a checkout carrying commits its origin never
  received is reported healthy, and the next patch relay into it conflicts
  (allod/tools#287). Until that lands, the proof of a synced workspace is
  `git status -sb` in each checkout, or `git rev-list --count @{u}..HEAD`,
  not the `up to date` row.
- **The error row's reason is git's first line, which may be the fetch
  banner.** On a diverged branch the first line was `From
  /tmp/.../remotes/diverged` on one run and `hint: You have divergent
  branches ...` on the next; the row says a pull failed and not reliably
  why. Read the repository.
- **Exit 0 always.** `pull-all` exits 0 with every row an error; a script
  that gates on its status gates on nothing. The rows are the only signal.
- **`pulled` versus `up to date` is a grep for `Already up to date`.** A
  git whose messages are localized prints something else, and every row
  reads `pulled`. The suite's mock git speaks English.
- **The suite's green proves the state machine, not git.** The mock answers
  `rev-list HEAD...@{u} --count` and `pull` from a table; a change to how
  git itself reports divergence or tracking is invisible to it. The
  real-git fixture above is the other half.
- **`work-diff` `(clean)` means the porcelain status is empty.** Unpushed
  commits, ignored files and a stale branch are all `(clean)`; pair it with
  `pull-all`, or with `git status -sb`, before calling a checkout untouched.
- **A gone worktree is silent in `work-diff`.** Its directory is skipped by
  design, so a prunable entry (`allod change list` says `prunable`) leaves
  no trace here; absence in `work-diff` is not absence of leftover state.
- **Clones under `~/changes` are outside both commands.** Neither
  discovers them; `allod change list` is their inventory, and a worker's
  unpushed work there never shows in a workspace sweep.
