# allod/tools

Shell scripts for managing a multi-repo NixOS dev environment. All scripts are
packaged via `pkgs.writeShellApplication` in `profiles` (dev VMs) and
`nexus` (host machine) — no manual installation needed after
`nixos-rebuild switch`.

## Scripts

### `pull-all`

Pulls every git repo under `~/work/`. By default, each repo stays on its
current branch. Use `--switch` to return clean, fully pushed work branches to
their default branch before pulling.

```
pull-all
pull-all --switch
```

For each repo:
- **Dirty working tree** → skipped with notice
- **Current branch** → pulls and reports whether anything changed
- **With `--switch`, non-default branch with no remote tracking** → skipped with notice (local-only branch)
- **With `--switch`, non-default branch with unpushed commits** → skipped with notice
- **With `--switch`, non-default branch clean and fully pushed** → checks out the default branch, then pulls

Repos are processed in parallel and printed in workspace order.

Example `pull-all --switch` output:

```
  allod/tools            pulled     [master]
  allod/vm                  up to date [master]
  hashpool                  skipped    [on agent/my-feature — 2 unpushed commits]
  allod/nexus               up to date [master]
  allod/profiles            pulled     [master]
```

---

### `work-diff`

Shows local working tree state across all repos — staged changes, unstaged
changes, current branch.

```
work-diff                  # all repos
work-diff --all            # all repos
work-diff <repo-name>      # single repo
```

Use this to see what's in-flight before syncing or after returning to a machine.

---

### `flake-status`

Shows which flake inputs each repo pins and at what revision.

```
flake-status                           # all inputs, all repos
flake-status <input-name>              # one input across all repos
flake-status <input-name> --upstream   # compare pins to upstream HEAD
```

**No args** — full table per repo:
```
==> allod/nexus
  allod-tools        97b57e1  2026-06-03
  home-manager          3ee51fb  2026-05-23
  nixpkgs               b77b3de  2026-05-22
  ...
```

**Named input** — consistency check across all repos:
```
$ flake-status allod-tools
allod-tools — all repos consistent at 97b57e1 (2026-06-03)

  allod/vm              (not an input)
  allod/nexus           97b57e1  2026-06-03
  allod/profiles        97b57e1  2026-06-03
```

If repos are out of sync, the header says `INCONSISTENT` and the stale rows are
marked `← stale`.

**`--upstream`** makes a network call to compare local pins against the input's
remote HEAD. In all-inputs mode, stale pins are marked with `→ <rev>` and the
output suggests `flake-update-cascade` commands for outdated inputs.

---

### `flake-update-cascade`

Updates one or more named flake inputs across all repos that pin them directly,
running pre-flight checks before touching anything.

```
flake-update-cascade <input-name>... [--pr] [--dry-run]
```

The requested names are resolved through each repository's lock graph. All
reachable direct pins with those names are passed to one `nix flake update`
invocation, producing at most one commit and one PR per repository.

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
- Listed in `~/.config/git/active-pr-branches` → skip with notice (GPG-signed commits required)

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

# Update multiple inputs together in each repository
flake-update-cascade nixpkgs home-manager

# Update allod-tools across all repos via PRs (works on protected branches)
flake-update-cascade allod-tools --pr
```

---

### `forge`

Forgejo CLI — `gh` but for a self-hosted Forgejo instance.

```
forge [-R|--repo owner/repo] <resource> <command> [args]
```

**Config:**

| Variable | Meaning |
|---|---|
| `FORGE_URL` | Forgejo base URL; defaults to `https://forge.anarch.diy` |
| `FORGEJO_TOKEN` | Token value; overrides the token file when set |
| `FORGE_TOKEN_FILE` | Token file path; defaults to `~/.config/git/forgejo-token` |

When `FORGEJO_TOKEN` is unset, `forge` reads the token from `FORGE_TOKEN_FILE`.
Repo is inferred from `git remote get-url origin` when `-R`/`--repo` is omitted,
and `-R`/`--repo` may appear before the resource or after the command.

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

**Auth commands:**

```bash
forge auth status       # verify configured credentials
```

`auth status` checks the configured token source (`FORGEJO_TOKEN` env var or
token file) without exposing token material in output or process arguments.

**Token commands:**

```bash
extract-token-safely | forge token verify
```

`token verify` reads a candidate token from stdin and checks it against the
Forgejo API. It does not use the configured token — stdin is the only input.
If stdin is a TTY (no pipe), it exits immediately with a usage hint.

Safe calling patterns — avoid putting the token in shell history or argv:

```bash
# Read interactively without echo, then verify
read -rs tok; printf '%s' "$tok" | forge token verify

# From a password manager or secret store
pass show forgejo/token | forge token verify
```

**Issue commands:**

```bash
forge issue list
forge issue view <number>
forge issue create --title <title> [--body <text> | --body-file <file>]
forge issue edit <number> [--title <title>] [--body <text> | --body-file <file>]
forge issue close {<number> | <url>} [--comment <text>] \
  [--reason completed|"not planned"|duplicate] [--duplicate-of <issue>]
```

Pass `-` to `--body-file` to read from stdin:

```bash
forge issue create --title "Bug report" --body-file issue.md
printf '%s\n' "Updated description" | forge issue edit 20 --body-file -
```

`issue close` follows `gh issue close` syntax, including `-c`/`--comment`,
`-r`/`--reason`, and `--duplicate-of`. Forgejo does not expose close-reason
metadata through its API, so `not planned` and duplicate reasons are recorded
in the closing comment.

---

## Workflow

### Morning sync / getting up to speed

```bash
pull-all --switch # return clean pushed branches to default, then pull
work-diff         # see anything still in-flight
flake-status      # spot pin drift across repos
```

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
