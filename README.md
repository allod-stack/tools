# allod/tools

Command-line tools for managing a multi-repo NixOS dev environment. Shell tools
are packaged with `pkgs.writeShellApplication`; `forge` and `allod` are
standard-library-only Go binaries built with `pkgs.buildGoModule`. Dev VMs and the
host install them through their Nix composition, with no manual installation after
`nixos-rebuild switch`.

## Layout

```
cmd/allod/                Go main CLI (change, patch, pr, pm; site opt-in)
cmd/forge/                Forgejo CLI Go command
internal/forgeapi/        in-process Forgejo API client
internal/gitremote/       Git remote parsing shared by Go commands
pr-explain/               PR explanation report tool (behind `allod pr explain`)
  explain                 entry point: resolve, run the provider, assemble, publish
  validate-report          standalone report validator, usable directly
  lib.sh                  shared asset emission and validation logic
  contract.md             shared reader contract prepended to every authoring prompt
  outline-prompt.md       outline pass: section plan and front matter
  section-prompt.md       one body section per provider call
  quiz-prompt.md          closing quiz and provenance pass
  repair-prompt.md        rules for the single bounded validation repair pass
  report.css              responsive component vocabulary
  report.js               optional progressive enhancements
  component-gallery.html  visual reference and regression fixture
pm/                       PM board tools (schema, renderer, groom prompt)
workspace/                daily workspace sync and status
  pull-all                pull every repo under ~/work/
  work-diff               show staged/unstaged changes across repos
flake/                    nix flake pin management
  flake-status            inspect flake input pins across repos
  flake-update-cascade    update flake inputs across repos
  fleet-diff              check which machines a change rebuilds
git-hooks/                git hook policy and setup
  protected-refs-policy   branch protection, signing, remote restrictions
  setup-tracked-hooks     hookspath setup from repository registry
lib/                      shared shell libraries
  workspace.sh            repo, worktree, and default-branch helpers
```

### Optional namespaces

`allod site` is behind the `site` build tag. A machine that publishes no site
has no rclone remote to publish through, so it does not carry the command at
all: `allod site` there is an unknown namespace, exactly like any other word
the CLI does not know. A machine that does deploy opts in when it builds:

```nix
buildGoModule {
  subPackages = [ "cmd/allod" ];
  tags = [ "site" ];
}
```

The tagged binary defaults to the DirectAdmin hosting layout. A deployment
whose docroots are instead at `public_html/<domain>` selects the built-in
`public-html` profile when it builds the binary:

```nix
buildGoModule {
  subPackages = [ "cmd/allod" ];
  tags = [ "site" ];
  ldflags = [ "-X main.siteHostingProfileName=public-html" ];
}
```

`site.toml` remains domain-only; hosting layout and exclusions are deployment
configuration, not values copied into every site repository. There is no
runtime flag or environment override. Without the linker setting the compiled
default is `directadmin`; an unknown or malformed build-time value stops before
the deploy preflight or build. To roll back `public-html`, remove that `ldflags`
entry (or set its value to `directadmin`) and rebuild the deployment package.

Both `go test ./...` and `go test -tags site ./...` are required, and the flake
check runs both: each compiles code and tests the other does not.

## Documentation

- [Workspace tools](workspace/README.md) — `pull-all`, `work-diff`
- [Flake tools](flake/README.md) — `flake-status`, `flake-update-cascade`, `fleet-diff`
- [forge](docs/forge.md) — Forgejo CLI
- [PR explanation reports](docs/pr-explain.md) — generate and iterate on a
  comprehension-first HTML report
- [Report components](docs/components.md) — semantic visual vocabulary and
  authoring contracts
- [Git hooks](git-hooks/README.md) — `protected-refs-policy`, `setup-tracked-hooks`

## Shared Library

`lib/workspace.sh` provides repo discovery and default-branch helpers used by
`pull-all`, `work-diff`, `flake-status`, and `flake-update-cascade`.
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
| `prunable` | git can no longer reach the worktree through its admin entry | `git -C <repo> worktree prune` |
| `locked` | held by `git worktree lock` | unlock, then reassess |
| `detached` | HEAD is detached, so any commits there are unreachable by branch | create a branch at HEAD, then reassess |
| `submodule` | a populated submodule is present, which `git worktree remove` refuses | deinit the submodules, then reassess |
| `dirty` | uncommitted changes | commit or discard them |
| `unpushed` | commits that exist nowhere else | `allod change record`, or handle them |
| `unknown` | the tool cannot prove whether the commits exist on a remote | inspect the branch and repair its remote/base before cleanup |
| `clean` | nothing to lose | `allod change cleanup <path>` |

Exactly one word is reported: the strongest blocker. `clean` is reported if and
only if `allod change cleanup` on that path would succeed.

`prunable` does not mean the directory is gone. It means the link between the
worktree and the repo is broken — usually because the directory was deleted, but
also when the directory survives and its `.git` file did not. `git worktree
prune` clears the admin entry and never deletes a directory, so check what is
left behind before removing it by hand: it may still hold uncommitted work.

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

### Checking what a change rebuilds

Before merging work that could reach a real machine, evaluate the fleet against
what the change claims it does. `--expect-none` is the land-inert case; name
machines with `--expect` when the change is meant to convert them. A mismatch in
either direction exits 2.

```bash
cd ~/work/allod/deploy
fleet-diff --override archetypes/vm=1a2b3c4 --expect-none
```

### Reviewing a PR

```bash
forge pr list
forge pr view <number>
forge pr review-comments <number>
allod pr explain <number> --codex --output ./pr-explanation.html
# Read and critique the report; regenerate deliberately after an improvement:
allod pr explain <number> --codex --output ./pr-explanation.html --replace
forge pr reply <number> <comment-id> --body "looks good"
forge pr comment <number> --body "approved"
```

Use `--claude` instead of `--codex` to select the other installed subscription
CLI. The provider flag is explicit consent to disclose the PR inputs to that
provider; the command prints the repository, immutable commits, and runner
before invocation. It validates and writes only the requested local report — it
never comments, commits, pushes, or edits the PR. See
[PR explanation reports](docs/pr-explain.md) for checkout and fork resolution,
dry runs, safe replacement, and the report-review loop. Tools that need the
underlying commit metadata can use the stable, machine-readable
[`forge pr snapshot`](docs/forge.md#pr-commands) interface.
