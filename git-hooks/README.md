# Git Hooks

Git hook policy enforcement and setup scripts.

## `protected-refs-policy`

Hook dispatcher that enforces branch protection, signing requirements, and
external remote restrictions, and rejects AGit `refs/for/*` submissions.
Invoked as a git hook (pre-commit, pre-rebase,
pre-merge-commit, pre-push) and delegates to per-repo tracked hooks and
`.git/hooks/` hooks after running policy checks.

Policy is driven by config files under `~/.config/git/`:
- `protected-branches` -- branches where direct commits are blocked
- `signing-required-branches` -- branches requiring GPG-signed commits
- `active-pr-branches` -- remote branches requiring GPG-signed pushes
- `allowed-external-remotes` -- remotes permitted for push (forge.anarch.diy always allowed)

Also blocks non-fast-forward (force) pushes on `agent/*` and active PR branches.

### How a repository is recognised

`protected-branches` and `signing-required-branches` are keyed on the repository
the checkout belongs to — its path relative to `$HOME` — so a linked worktree
resolves to its main repository rather than to its own directory, and the
policies apply inside worktrees as they do in the checkout they came from.

When no entry names that path but one names the repository `origin` points at,
the checkout is somewhere its entry does not put it. That is a misconfiguration,
not an absence of protection: the hook blocks that entry's branch anyway and
names the expected and the actual path beside it. An entry matches the origin
when its path is the remote's `owner/repo` or ends in `/owner/repo`, so
`work/allod/profiles` answers for `allod/profiles` while `work/xallod/profiles`
does not. A repository with no `origin`, or whose `origin` matches no entry,
stays unprotected and silent.

`allod change begin` and `allod change record` apply the same rule and refuse a
misplaced checkout outright, on every branch, with exit code 8.
`tests/fixtures/protection-cases.tsv` is the one table both implementations of
the rule are tested against.

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
