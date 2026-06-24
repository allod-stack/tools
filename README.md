# allod/tools

Shell scripts for managing a multi-repo NixOS dev environment. All scripts are
packaged via `pkgs.writeShellApplication` in `profiles` (dev VMs) and
`nexus` (host machine) — no manual installation needed after
`nixos-rebuild switch`.

## Layout

```
allod                     main CLI (change begin/record/submit/cleanup)
forge                     Forgejo CLI
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
  workspace.sh            repo discovery and default-branch helpers
```

## Documentation

- [Workspace tools](docs/workspace.md) — `pull-all`, `work-diff`
- [Flake tools](docs/flake.md) — `flake-status`, `flake-update-cascade`
- [forge](docs/forge.md) — Forgejo CLI
- [Git hooks](docs/git-hooks.md) — `protected-refs-policy`, `setup-tracked-hooks`

## Shared Library

`lib/workspace.sh` provides repo discovery and default-branch helpers used by
`allod`, `pull-all`, `work-diff`, `flake-status`, and `flake-update-cascade`.
It also sets `WORK_DIR` (defaults to `~/work/`, overridable via the environment).

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
