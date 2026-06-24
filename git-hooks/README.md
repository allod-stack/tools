# Git Hooks

Git hook policy enforcement and setup scripts.

## `protected-refs-policy`

Hook dispatcher that enforces branch protection, signing requirements, and
external remote restrictions. Invoked as a git hook (pre-commit, pre-rebase,
pre-merge-commit, pre-push) and delegates to per-repo tracked hooks and
`.git/hooks/` hooks after running policy checks.

Policy is driven by config files under `~/.config/git/`:
- `protected-branches` -- branches where direct commits are blocked
- `signing-required-branches` -- branches requiring GPG-signed commits
- `active-pr-branches` -- remote branches requiring GPG-signed pushes
- `allowed-external-remotes` -- remotes permitted for push (forge.anarch.diy always allowed)

Also blocks non-fast-forward (force) pushes on `agent/*` and active PR branches.

---

## `setup-tracked-hooks`

Sets up `.hookspath` for repos that declare a `hookspath` field in the
repository registry (`inventory/scripts/repositories.json`). For each
matching repo cloned under `~/work/`:

1. Writes `.hookspath` in the repo root pointing to the declared hooks directory
2. Adds `.hookspath` to `.git/info/exclude` so it doesn't dirty `git status`

Idempotent -- safe to run repeatedly. Exits cleanly if the registry or repo
is missing.

```
setup-tracked-hooks
```

Runs automatically on every `nixos-rebuild switch` via home-manager
activation. Useful for upstream repos (e.g. `cdk`) where you can't commit
`.hookspath` to the repo itself. The `protected-refs-policy` hook dispatcher
picks up the hooks via `run_tracked_hook()`.

To add tracked hooks for a new repo, add a `hookspath` field to its entry in
`repositories.json`:

```json
"cdk-upstream": {
  "source": "git",
  "remote": "https://github.com/cashubtc/cdk.git",
  "checkout": "cdk",
  "hookspath": "misc/git-hooks"
}
```
