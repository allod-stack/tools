# Flake Tools

Nix flake pin inspection and update automation.

## `flake-status`

Shows which flake inputs each repo pins and at what revision.

```
flake-status                           # all inputs, all repos
flake-status <input-name>              # one input across all repos
flake-status <input-name> --upstream   # compare pins to upstream HEAD
```

**No args** -- full table per repo:
```
==> allod/nexus
  allod-tools        97b57e1  2026-06-03
  home-manager          3ee51fb  2026-05-23
  nixpkgs               b77b3de  2026-05-22
  ...
```

**Named input** -- consistency check across all repos:
```
$ flake-status allod-tools
allod-tools — all repos consistent at 97b57e1 (2026-06-03)

  allod/vm              (not an input)
  allod/nexus           97b57e1  2026-06-03
  allod/profiles        97b57e1  2026-06-03
```

If repos are out of sync, the header says `INCONSISTENT` and the stale rows are
marked `<- stale`.

**`--upstream`** makes a network call to compare local pins against the input's
remote HEAD. In all-inputs mode, stale pins are marked with `-> <rev>` and the
output suggests `flake-update-cascade` commands for outdated inputs.

---

## `flake-update-cascade`

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
- Not on default branch -> error
- Dirty working tree -> error
- Unpushed commits -> error
- No `flake.lock` -> skip with notice
- Input not present / is a `follows` -> skip with notice
- Listed in `~/.config/git/active-pr-branches` -> skip with notice (GPG-signed commits required)

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
