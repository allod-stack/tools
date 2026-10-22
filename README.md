# allod/tools

Command-line tools for managing a multi-repo NixOS dev environment. Shell tools
are packaged with `pkgs.writeShellApplication`; `forge`, `allod`, and
`flake-update-cascade` are standard-library-only Go binaries built with
`pkgs.buildGoModule`. Dev VMs and the
host install them through their Nix composition, with no manual installation after
`nixos-rebuild switch`.

## Layout

```
cmd/allod/                Go main CLI (change, check, memory backpass, patch, pr, skills, trace, site serve and view; deploy/check/config and secret opt-in)
cmd/flake-update-cascade/ flake input update cascade Go command
cmd/forge/                Forgejo CLI Go command
cmd/wake-agent/           detached event watcher and headless Pi continuation
internal/flakelock/       flake.lock graph walking for the cascade
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
workspace/                daily workspace sync and status
  pull-all                pull every repo under ~/work/
  work-diff               show staged/unstaged changes across repos
flake/                    nix flake pin management
  flake-status            inspect flake input pins across repos
  fleet-diff              check which machines a change rebuilds
git-hooks/                git hook policy and setup
  protected-refs-policy   branch protection, signing, remote restrictions
  setup-tracked-hooks     hookspath setup from repository registry
lib/                      shared shell libraries
  workspace.sh            repo, worktree, and default-branch helpers
verification/             behavior-level feature map (features/) and recorded sweeps (sweeps/)
```

### Optional namespaces

`allod site serve` and `allod site view` are compiled into every build:
preview runs the `preview` app a site repository's own flake exposes, as a user
systemd unit on loopback in a dev VM, and view forwards that port to a browser
on the hypervisor. Neither machine needs a hosting credential or an rclone
configuration, and neither command reads one. See
[allod site serve and view](docs/allod-site-preview.md).

`allod site deploy`, `check`, and `config` are behind the `site` build tag. A
machine that publishes no site has no rclone remote to publish through, so it
does not carry these three at all: `allod site deploy` there fails as
"unknown site command", exactly the way a typo would, and `allod site` on
its own lists `preview` and `view` only. A machine that does deploy opts in when
it builds:

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

`allod secret create`, `rekey`, and `rotate` are behind the
`secret` build tag for the same reason: they land an encrypted credential
in the secrets repository, which only the machine holding the age identity
can do. Everywhere else those three are unknown secret commands — the
capability is absent, not refused — which is what makes it a boundary
rather than a rule an agent has to remember. `allod secret declare`, which
writes a credential's non-secret half and encrypts nothing, needs no tag
and is present everywhere. The host opts in with
`tags = [ "site" "secret" ];`. See [allod secret](docs/allod-secret.md).

`go test ./...`, `-tags site`, `-tags secret`, and `-tags site,secret` are all
required, and the flake check runs all four: each tag's tests assert the
namespace present, and each untagged run asserts it absent.

A namespace absent from the dispatch table entirely, not just one gated by a
build tag, is still not a dead end: `allod <word>` runs `allod-<word>` from
PATH with the remaining arguments, git-style, so a deployment can add its
own commands under the public name without a build tag or a change to this
binary. A compiled-in namespace always wins over a file on PATH, and if
neither has the word the answer stays the existing "unknown command
namespace" failure.

## Documentation

- [allod change](docs/allod-change.md) — clone-per-change, the parent's ledger, and `begin`/`list`/`record`/`submit`/`cleanup`
- [allod change run](docs/allod-change-run.md) — run a command inside a bubblewrap sandbox that can reach nothing but the named clones
- [Workspace tools](workspace/README.md) — `pull-all`, `work-diff`
- [Flake tools](flake/README.md) — `flake-status`, `flake-update-cascade`, `fleet-diff`
- [forge](docs/forge.md) — Forgejo CLI
- [PR explanation reports](docs/pr-explain.md) — generate and iterate on a
  comprehension-first HTML report
- [Report components](docs/components.md) — semantic visual vocabulary and
  authoring contracts
- [allod site serve and view](docs/allod-site-preview.md) — serve a site in a dev VM from the `preview` app in its own flake, and view it from the hypervisor
- [Git hooks](git-hooks/README.md) — `protected-refs-policy`, `setup-tracked-hooks`
- [allod check](docs/allod-check.md) — run a repository's machines and checks, one `nix` process at a time, in place of `nix flake check`
- [allod trace](docs/allod-trace.md) — distill local Claude, Codex, and Pi session logs into redacted markdown traces
- [allod memory](docs/allod-memory.md) — run the memory backward pass over session traces (backpass) as code, spending a model only on its two prompts
- [wake-agent](docs/wake-agent.md) — resume one bounded headless Pi session after an external event
- `allod skill` — list the agent skills a skills directory exposes: one aligned line each, the name and its `summary:` (a skill without a summary lists its `description` truncated; a name over 16 characters or a summary over 70 refuses). `allod skill <name>` prints one in full. Without `--from`, sources are every registry entry marked `"memory": true`, in sorted registry-id order, falling back to `allod/memory` when the registry marks none.

## Shared Library

`lib/workspace.sh` provides repo discovery and default-branch helpers used by
`pull-all`, `work-diff`, and `flake-status`.
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

`-d` is the isolation switch. With it, `begin` makes a self-contained clone
under `~/changes` — objects copied from the repo, never hardlinked (a
hardlink shares the parent's own inode, and the clone's owner could `chmod`
or overwrite through it), origin set to the repo's own origin — on a new
`agent/<description>` branch, for every repo whether
protected or not, so two agents changing the same repo never move each
other's HEAD. The repo records the clone in its own ledger
(`.git/allod-changes`); see [allod change](docs/allod-change.md) for the
ledger format and why a sandboxed worker can be handed nothing but the clone
directory and still commit safely.

```bash
path=$(allod change begin -d fix-thing ~/work/allod/tools)
cd "$path"
# edit
allod change record -m "fix the thing"
allod change submit -t "Fix the thing" -F body.md
```

Clones land under `~/changes/<slug>-<description>-XXXXXX`, outside `~/work`
so the workspace stays exactly the checkouts the registry declares. Nothing
may depend on that path — enumerate with `allod change list`. A worktree
`begin -d` made before this change keeps working in `list`, `record`, and
`cleanup` until it drains; `begin` makes no new ones.

`--resume` picks up a branch another session already pushed. It makes the
same clone, on a local `agent/<description>` that tracks the one on origin,
so `record` and `submit` continue the branch instead of starting a new one. It
refuses when the branch is missing on origin, when a description already
claimed by an open clone or legacy worktree names the same branch, or when
`-d` is also given:

```bash
path=$(allod change begin --resume fix-thing ~/work/allod/tools)
```

Without `-d`, `begin` prints the shared checkout path and creates nothing. That
is the in-place flow for committing to a repo's default branch, which git cannot
isolate anyway since one branch cannot be checked out in two worktrees. A
protected repo has no legitimate in-place change, so it refuses instead.

```bash
cd "$(allod change begin ~/work/allod/memory)"
```

Whether a repo is protected is decided by the repo `origin` points at, not by
where it happens to sit: `begin` and `record` refuse outright, with exit code 8,
when `origin` names a repo `~/.config/git/protected-branches` lists and the
checkout is not at that entry's path. The message names the expected and the
actual path; move the checkout there. A repo whose `origin` matches no entry is
unprotected as before. `record` and `submit` resolve a clone to the repo whose
ledger lists it and apply that repo's protection, so a clone is judged by its
parent and never flagged as a misplaced checkout of itself. The
`protected-refs-policy` git hook applies the same rule to the parent — see
[git-hooks/README.md](git-hooks/README.md).

`record` refuses three situations rather than committing or pushing something
wrong. If the checkout was switched to a different branch after `begin` set
it up — another agent's `git switch`, say — `record` names the branch it
expected and the branch that is actually checked out, and refuses. With no
`--files` named, `record` normally stages every tracked change; that is safe
in a clone or worktree from `begin -d`, which belongs to one agent alone, but
in a shared checkout it could sweep in another agent's edit, so there
`record` lists the unnamed changed paths and refuses — pass `--files` to say
what to commit. And before pushing, `record` refuses, with exit code 9, a
clone whose `origin` no longer matches the repo that issued it: a worker can
edit its own clone's `.git/config`, and an overseer's push must not follow it
anywhere else.

### Reclaiming clones and worktrees

`allod change list` prints one tab-separated row per ledgered clone, plus one
per remaining linked worktree from before this change — repo, path, branch,
state — for one repo or, with no argument, the whole workspace. It only
reads: nothing is ever removed implicitly, because no local signal tells a
dead agent from a working one.

| State | Meaning | Reclaim |
|---|---|---|
| `missing` | a clone's directory is gone | nothing to clean up by hand; the ledger entry just has nothing left to resolve |
| `prunable` | git can no longer reach a linked worktree through its admin entry | `git -C <repo> worktree prune` |
| `locked` | a linked worktree held by `git worktree lock` | unlock, then reassess |
| `detached` | HEAD is detached, so any commits there are unreachable by branch | create a branch at HEAD, then reassess |
| `submodule` | a populated submodule is present, which `git worktree remove` (or `cleanup`, for a clone) refuses | deinit the submodules, then reassess |
| `dirty` | uncommitted changes | commit or discard them |
| `unpushed` | commits that exist nowhere else | `allod change record`, or handle them |
| `unknown` | the tool cannot prove whether the commits exist on a remote | inspect the branch and repair its remote/base before cleanup |
| `clean` | nothing to lose | `allod change cleanup <path>` |

Exactly one word is reported: the strongest blocker. `clean` is reported if and
only if `allod change cleanup` on that path would succeed. `missing` and
`prunable` both mean the directory is gone; they differ in whether anything
beyond that is left for `cleanup` or a human to resolve, since a clone has no
`git worktree`-level admin entry the way a linked worktree does.

`prunable` does not mean the directory is gone alone. It means the link between
the worktree and the repo is broken — usually because the directory was
deleted, but also when the directory survives and its `.git` file did not.
`git worktree prune` clears the admin entry and never deletes a directory, so
check what is left behind before removing it by hand: it may still hold
uncommitted work.

A clone's own remote-tracking refs are independent of its parent's, unlike a
linked worktree's shared ref store: `list` and `cleanup` only know about a
push that landed through another path (the parent, a patch relay) once
something fetches inside the clone itself.

### Running a repository's checks

`cd ~/work/<checkout> && allod check` replaces `nix flake check`: one `nix`
process per machine and per check, every failing step reported in one run, and a
run that evaluated checks and built none refused. Input overrides and
`allod-check.toml`: [allod check](docs/allod-check.md).

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

### Verifying a PR before it merges

The behavior-level feature map in [verification/features/](verification/features/README.md)
names, per area, what exists, how to reach it, how to drive it and what usually lies.
The `verify-merge` skill in allod/memory drives it; a change to a behavior fixes
the map in the same PR, and a PR that removes or narrows a map entry or a check
is never merged below R2. Recorded sweeps live under `verification/sweeps/`.
