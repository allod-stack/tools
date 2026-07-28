# allod/tools

Shell scripts for managing a multi-repo NixOS dev environment. All scripts are
packaged via `pkgs.writeShellApplication` in `profiles` (dev VMs) and
`nexus` (host machine) — no manual installation needed after
`nixos-rebuild switch`.

## Layout

```
allod                     main CLI (change, patch, pm)
forge                     Forgejo CLI
pm/                       PM board tools (schema, renderer, groom prompt)
workspace/                daily workspace sync and status
  pull-all                pull every repo under ~/work/
  work-diff               show staged/unstaged changes across repos
flake/                    nix flake pin management
  flake-status            inspect flake input pins across repos
  flake-update-cascade    update flake inputs across repos
git-hooks/                git hook policy and setup
  protected-refs-policy   branch protection, signing, remote restrictions
  setup-tracked-hooks     hookspath setup from repository registry
lib/                      shared shell libraries
  workspace.sh            repo, worktree, and default-branch helpers
```

## Documentation

- [Workspace tools](workspace/README.md) — `pull-all`, `work-diff`
- [Flake tools](flake/README.md) — `flake-status`, `flake-update-cascade`
- [forge](docs/forge.md) — Forgejo CLI
- [Git hooks](git-hooks/README.md) — `protected-refs-policy`, `setup-tracked-hooks`

## Shared Library

`lib/workspace.sh` provides repo discovery and default-branch helpers used by
`allod`, `pull-all`, `work-diff`, `flake-status`, and `flake-update-cascade`.
It also sets `WORK_DIR` (defaults to `~/work/`, overridable via the environment).

`workspace_collect_repos` returns exactly the checkouts under `WORK_DIR`, which
is what the tools that mutate repos consume. `workspace_collect_worktrees`
returns a repo's linked worktrees, wherever they are sited; only the read-only
`work-diff` uses it.

## Workflow

### Morning sync / getting up to speed

```bash
pull-all --switch # return clean pushed branches to default, then pull
work-diff         # see anything still in-flight
flake-status      # spot pin drift across repos
```

### Making a change

`-d` is the isolation switch. With it, `begin` creates a worktree and an
`agent/<description>` branch for every repo, protected or not, so two agents
changing the same repo never move each other's HEAD:

```bash
path=$(allod change begin -d fix-thing ~/work/allod/tools)
cd "$path"
# edit
allod change record -m "fix the thing"
allod change submit -t "Fix the thing" -F body.md
```

Worktrees land under `~/changes/<slug>-<description>-XXXXXX`, outside `~/work`
so the workspace stays exactly the checkouts the registry declares. Nothing may
depend on that path — enumerate with `git worktree list` or `allod change list`.

Without `-d`, `begin` prints the shared checkout path and creates nothing. That
is the in-place flow for committing to a repo's default branch, which git cannot
isolate anyway since one branch cannot be checked out in two worktrees. A
protected repo has no legitimate in-place change, so it refuses instead.

```bash
cd "$(allod change begin ~/work/allod/memory)"
```

### Reclaiming worktrees

`allod change list` prints one tab-separated row per linked worktree — repo,
path, branch, state — for one repo or, with no argument, the whole workspace. It
only reads: nothing is ever removed implicitly, because no local signal tells a
dead agent from a working one.

| State | Meaning | Reclaim |
|---|---|---|
| `prunable` | the directory is gone; only the admin entry is left | `git -C <repo> worktree prune` |
| `locked` | held by `git worktree lock` | unlock, then reassess |
| `detached` | HEAD is detached, so any commits there are unreachable by branch | create a branch at HEAD, then reassess |
| `dirty` | uncommitted changes | commit or discard them |
| `unpushed` | commits that exist nowhere else | `allod change record`, or handle them |
| `clean` | nothing to lose | `allod change cleanup <path>` |

Exactly one word is reported: the strongest blocker. `clean` is reported if and
only if `allod change cleanup` on that path would succeed.

### Updating a flake input

```bash
# 1. Check if an update is available
flake-status allod-tools --upstream

# 2. Preview what would change
flake-update-cascade allod-tools --dry-run

# 3. Create update PRs across all repos
flake-update-cascade allod-tools --pr

# 4. Review and merge PRs on Forgejo

# 5. Sync and verify
pull-all
flake-status allod-tools
```

### Reviewing a PR

```bash
forge pr list
forge pr view <number>
forge pr review-comments <number>
forge pr reply <number> <comment-id> --body "looks good"
forge pr comment <number> --body "approved"
```
