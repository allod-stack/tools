# Workspace Tools

Daily workspace sync and status tools. Both scripts operate on every git repo
under `~/work/`. `pull-all` mutates repos, so it stays on those checkouts only;
`work-diff` is read-only and also reports each repo's linked worktrees.

## `pull-all`

Pulls every git repo under `~/work/`. By default, each repo stays on its
current branch. Use `--switch` to return clean, fully pushed work branches to
their default branch before pulling.

```
pull-all
pull-all --switch
PULL_ALL_JOBS=2 pull-all
```

For each repo:
- **Dirty working tree** -> skipped with notice
- **Current branch** -> pulls and reports whether anything changed
- **With `--switch`, non-default branch with no remote tracking** -> skipped with notice (local-only branch)
- **With `--switch`, non-default branch with unpushed commits** -> skipped with notice
- **With `--switch`, non-default branch clean and fully pushed** -> checks out the default branch, then pulls

Repos are processed in parallel and printed in workspace order. Concurrency is
capped at 4 pulls by default; set `PULL_ALL_JOBS` to a positive integer to tune
the limit.

Example `pull-all --switch` output:

```
  allod/tools            pulled     [master]
  allod/vm                  up to date [master]
  my-project                skipped    [on agent/my-feature — 2 unpushed commits]
  allod/nexus               up to date [master]
  allod/profiles            pulled     [master]
```

---

## `work-diff`

Shows local working tree state across all repos -- staged changes, unstaged
changes, current branch.

```
work-diff                  # all repos
work-diff --all            # all repos
work-diff <repo-name>      # single repo
```

Use this to see what's in-flight before syncing or after returning to a machine.

Each repo's linked worktrees are shown under it, attributed to their branch, so
agent branch work stays visible even though it lives outside `~/work/`.
Worktrees are enumerated with `git worktree list`, never by scanning a
directory, so the report is correct wherever they are sited.

```
════════════════════════════════════════════════════════════
  allod/tools  [master]
════════════════════════════════════════════════════════════
  (clean)

  ↳ worktree /home/you/changes/tools-fix-collector  [agent/fix-collector]
 M lib/workspace.sh
```
