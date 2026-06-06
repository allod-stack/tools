# allod/tools

Shell scripts for managing a multi-repo NixOS dev environment. All scripts are
packaged via `pkgs.writeShellApplication` in `machine-profiles` (dev VMs) and
`host-config` (host machine) — no manual installation needed after
`nixos-rebuild switch`.

## Scripts

### `pull-all`

Syncs every git repo under `~/work/` to its default branch.

```
pull-all
```

For each repo:
- **Dirty working tree** → skipped with notice
- **Non-default branch, no remote tracking** → skipped with notice (local-only branch)
- **Non-default branch, unpushed commits** → skipped with notice
- **Non-default branch, clean and fully pushed** → auto-checks out default branch, then pulls
- **Already on default branch** → pulls

```
  allod/tools            pulled     [master]
  dev-vm-config             up to date [master]
  hashpool                  skipped    [on agent/my-feature — 2 unpushed commits]
  host-config               up to date [master]
  machine-profiles          pulled     [main]
```

---

### `work-diff`

Shows local working tree state across all repos — staged changes, unstaged
changes, current branch.

```
work-diff                  # all repos
work-diff <repo-name>      # single repo
```

Use this to see what's in-flight before syncing or after returning to a machine.

---

### `flake-status`

Shows which flake inputs each repo pins and at what revision.

```
flake-status                           # all inputs, all repos
flake-status <input-name>              # one input across all repos
flake-status <input-name> --check-upstream   # compare pins to upstream HEAD
```

**No args** — full table per repo:
```
==> host-config
  allod-tools        97b57e1  2026-06-03
  home-manager          3ee51fb  2026-05-23
  nixpkgs               b77b3de  2026-05-22
  ...
```

**Named input** — consistency check across all repos:
```
$ flake-status allod-tools
allod-tools — all repos consistent at 97b57e1 (2026-06-03)

  dev-vm-config         (not an input)
  host-config           97b57e1  2026-06-03
  machine-profiles      97b57e1  2026-06-03
```

If repos are out of sync, the header says `INCONSISTENT` and the stale rows are
marked `← stale`.

**`--check-upstream`** makes a network call to compare local pins against the
input's remote HEAD. Useful before deciding whether an update is worth running.

---

### `flake-update-cascade`

Updates a named flake input across all repos that pin it directly, running
pre-flight checks before touching anything.

```
flake-update-cascade <input-name> [--pr] [--dry-run]
```

**Modes:**

| Flag | Behaviour |
|---|---|
| *(none)* | Commit directly to the default branch. Skips repos listed in `~/.config/git/protected-branches`. |
| `--pr` | Create/update a PR branch (`agent/flake-update-<input>`) for each repo. Works on protected repos. |
| `--dry-run` | Show what would change without modifying anything. |

**Pre-flight checks** (runs on all repos before any changes):
- Not on default branch → error
- Dirty working tree → error
- Unpushed commits → error
- No `flake.lock` → skip with notice
- Input not present / is a `follows` → skip with notice

If any repo fails pre-flight, the cascade aborts before touching anything.

**`--pr` mode details:**

Each eligible repo gets a branch named `agent/flake-update-<input>`. On
re-runs, the branch is force-updated and the existing PR is noted rather than a
new one being created. Requires `forge` on PATH.

**Examples:**

```bash
# See what a nixpkgs update would do, without changing anything
flake-update-cascade nixpkgs --dry-run

# Update nixpkgs across all repos, committing directly (non-protected only)
flake-update-cascade nixpkgs

# Update allod-tools across all repos via PRs (works on protected branches)
flake-update-cascade allod-tools --pr
```

---

### `forge`

Forgejo CLI — `gh` but for a self-hosted Forgejo instance.

```
forge [-R owner/repo] <resource> <command> [args]
```

**Config:**

| Variable | Default |
|---|---|
| `FORGE_URL` | `https://forge.anarch.diy` |
| `FORGEJO_TOKEN` | read from `~/.config/git/forgejo-token` |

Repo is inferred from `git remote get-url origin` when `-R` is omitted.

**PR commands:**

```bash
forge pr list
forge pr view <number>
forge pr create --title <title> [--head <branch>] [--base <branch>] \
  [--body <text> | --body-file <file>]
forge pr comment <number> [--body <text> | --body-file <file>]
forge pr edit <number> [--title <title>] [--body <text> | --body-file <file>]
forge pr review-comments <number>          # list inline comments with IDs
forge pr reply <number> <comment-id> [--body <text> | --body-file <file>]
forge pr find-by-head <branch>             # print PR number if open PR exists for branch
```

`pr create` defaults `--head` to the current branch and `--base` to the
repository's default branch. The `gh` short aliases are also supported:
`-t`, `-b`, `-F`, `-H`, and `-B`.

**Issue commands:**

```bash
forge issue list
forge issue view <number>
forge issue create --title <title> [--body <text> | --body-file <file>]
forge issue edit <number> [--title <title>] [--body <text> | --body-file <file>]
```

Pass `-` to `--body-file` to read from stdin:

```bash
forge issue create --title "Bug report" --body-file issue.md
printf '%s\n' "Updated description" | forge issue edit 20 --body-file -
```

---

## Workflow

### Morning sync / getting up to speed

```bash
pull-all          # sync all repos to default branch; returns merged PR branches automatically
work-diff         # see anything still in-flight
flake-status      # spot pin drift across repos
```

### Updating a flake input

```bash
# 1. Check if an update is available
flake-status allod-tools --check-upstream

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
