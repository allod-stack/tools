# allod change

Isolate a change on its own branch, record and push it, open a PR, and reclaim the tree afterward — the whole lifecycle instead of manual git and Forge steps. A sandboxed worker (`allod change run`) records and submits its own clone from inside its own sandbox, with the VM's own forge credentials; the overseer runs `record`, `submit`, `cleanup`, and `list` against a ledgered clone only from outside it, and `record` and `submit` refuse to touch one at all — see "Inside and outside the sandbox" below.

```
allod change begin [-d <description> | --resume <description>] [<repo-path>]
allod change list [<repo-path>]
allod change record -m <message> [--files <file>...]
allod change record -M <file|-> [--files <file>...]
allod change submit -t <title> (-b <body> | -F <file|->) [--base <branch>] [--depends-on <text>] [--dry-run]
allod change cleanup <path>
```

## Clones, not linked worktrees

`begin -d` makes a self-contained local clone under `~/changes` instead of a linked worktree. `git clone --no-hardlinks --no-checkout <repo> <path>` is a local clone copying objects over the optimized local transport, never hardlinking them: a hardlink shares the parent's own inode, and the clone's owner — the worker — can `chmod` or overwrite through it, straight through "the parent is untouched". Never an alternates clone either (`objects/info/alternates`), because a `git gc` in the parent can prune what an alternates clone still references. The clone's `origin` is then repointed at the parent's own `origin` URL (a local clone's default origin is the parent's path, not the forge), fetched and pruned, and `origin/HEAD` is re-resolved against the real remote: the local clone step copies whichever branch the parent happened to have checked out, not necessarily the remote's actual default. `agent/<description>` is cut from `origin/<start>` (`--resume` from `origin/agent/<description>` instead, with upstream tracking), and the clone's own `.git/allod-change-branch` records that handoff the same way a linked worktree's did.

Because objects are copied rather than hardlinked or shared through an alternates file, and the branch lives only inside the clone's own `.git`, a sandboxed worker given nothing but its own clone directory can commit freely without ever writing to the parent; nothing in the parent changes until `record` pushes.

A worktree `begin -d` made before this change keeps working in `list`, `record`, and `cleanup` until it drains; `begin` makes no new ones.

## The ledger

The parent repository records every clone it issues under its own `.git`, where a sandboxed worker cannot reach it: `.git/allod-changes/<description>`, one file per clone, two lines — the clone's absolute path, then its branch (always `agent/<description>`). The file is keyed by the description, never by the clone directory's basename, and `begin -d` creates it with an `O_EXCL` open as the one atomic step in the whole command: two concurrent `begin -d` calls for the same description both pass the advisory "already claimed" check first, but only one of them can win that create, so the loser refuses instead of both ever writing an entry for the same branch. A failure at any later step — the clone itself, the checkout of the new branch, the handoff-file write — removes both the claim and the half-made clone directory.

Only `begin` writes an entry; only `cleanup` removes one. `list`, `record`, `submit`, and `cleanup` resolve a clone back to its parent by scanning the ledgers of every repository under `$WORK_DIR` the way `list`'s no-argument form already walks the workspace — never through the clone's own `.git/config`, which a worker can edit freely. This is also why `begin -d` and `--resume` refuse a repository that is not under `$WORK_DIR`: a clone issued from anywhere else would get a ledger entry none of these commands could ever find again.

That resolution is a path match, nothing more: the candidate path (an explicit one, or the working directory, walked upward until something matches) is resolved to its physical, absolute form and compared against every ledger entry's own physical path, in pure Go. No git command runs against the candidate to decide this — not even `git rev-parse --show-toplevel` — so a hostile clone never gets the chance to run anything before `record` or `submit` has even decided whether to refuse it outright. See "Inside and outside the sandbox" below for what each command does once that match is made.

## Inside and outside the sandbox

No command outside the sandbox ever runs git in a worker's clone — not even `run`'s own pre-flight, which confirms the clone is on its ledgered `agent/*` branch by reading `.git/HEAD` directly rather than running `git branch --show-current`. Outside, `record` and `submit` refuse a clone this repo's ledger lists, with exit code 11, before any git command and before any lock: the path match above is the whole check, so a hostile clone never gets the chance to run anything. The refusal names the worker's own command: `allod change run <clone> -- allod change record ...` (or `submit ...`).

Inside the sandbox, `run` sets `ALLOD_CHANGE_SANDBOX=1` in the environment it builds (`--setenv`, next to `TMPDIR`); the variable is never on the pass-through allowlist (docs/allod-change-run.md), so only `run` itself ever sets it. With it set, `record` and `submit` resolve the repository with git exactly as they would for any ordinary repository — the parent's own ledger lives under the parent's `.git`, which is not mounted inside the sandbox at all, so it is simply invisible — and treat the clone the way they already treat a legacy linked worktree:

- No misplaced-checkout refusal (exit 8): a clone never sits at its parent's own registered path, since it lives under `~/changes`, so that check is skipped rather than refuse every sandboxed commit outright.
- No shared-checkout sweep refusal: `record` with no `--files` stages every tracked change, safe because the clone belongs to one worker alone, the same reasoning that already exempts a legacy worktree.
- The branch is pinned by the clone's own handoff file, `.git/allod-change-branch`, exactly as a legacy worktree's is: a mismatch refuses with exit code 3, checked before anything is staged, before the branch is read for a PR, or before `submit` resolves `--base`.
- Branch protection by origin still applies: a protected branch refuses with exit code 2, the ordinary rule, unchanged by sandboxing.
- `submit` resolves an unset `--base` against the clone's own `origin/HEAD` — inside the sandbox the clone's refs are the worker's own affair, so there is no parent to defer to.
- The push is additive (`git push -u origin HEAD`), and `forge pr create` runs with the forge token `run` bound into the sandbox (docs/allod-change-run.md).
- The global git config and hook dispatcher run exactly as they do for any other repository: no isolated git environment, no `GIT_NO_REPLACE_OBJECTS`. The tracked `commit-msg` hook that rejects a Claude attribution trailer applies to a worker's own commit the same as to anyone else's.

`cleanup` and `list` never run git in a clone either, from outside or from anywhere else: a clone's removability, and its row in `list`, both come from the forge, covered next.

## list and cleanup, from the forge

`cleanup <path>` resolves the ledger entry and takes the exclusive, non-blocking `flock` on the clone's own ledger file first — the same two-line file under the parent's `.git/allod-changes` that names the clone and its branch — before deciding anything: a running worker still holding it refuses with exit code 13, naming the clone, rather than wait. A `missing` clone (its directory gone; see `list` below) drops the ledger entry and says so, with no question asked of the forge. Otherwise `cleanup` asks: `forge pr find-by-head -s all <branch>`, run with the parent repository as `forge`'s own working directory, so the repository `forge` infers comes from the parent's origin, never the clone's own, worker-writable one. No pull request for the branch: `cleanup` refuses, naming the branch and the worker's own submit command, or says to remove the directory with standard tools and run `cleanup` again to drop the entry. Open: `cleanup` refuses. Merged or closed: `cleanup` runs `os.RemoveAll` on the clone directory and drops the ledger entry. This remains true after a rebase merge or patch-equivalent relay lands the work under a different hash and the forge deletes the source branch: `-s all` finds the pull request by its recorded head, and its merged or closed state is primary rather than a branch hash that no longer exists. The forge not answering at all — `forge` missing from `PATH`, a non-zero exit, output this cannot parse as `<number>\t<state>` — is a refusal naming the error, never read as "no pull request" and let through.

No git command runs in the clone at any point in this: not to check it out, not to check whether it is dirty, not to ask whether anything is unpushed. The tool stopped reasoning about a clone's own disk the moment the overseer stopped running git in it at all; the branch's pull request, wherever it stands, is the one fact that matters.

`list` asks the same question for every ledgered clone it reports, after the same per-clone lock (held through the query, so a concurrent `cleanup` never races it): `missing`, `running` (the ledger file is currently flocked exclusively by something else, almost always a `run` still sandboxing a command inside it — no forge query runs either), `unsubmitted` (no pull request for the branch yet), `open`, `merged`, `closed`, or `unknown` (the forge did not answer). A legacy linked worktree keeps its own, unrelated states — `missing`, `prunable`, `locked`, `detached`, `submodule`, `dirty`, `unpushed`, `unknown`, or `clean` — judged from its own disk and the real remote exactly as before. On that legacy path, a branch deleted after a rebase-style or patch-equivalent landing is compared to the default branch by patch (`git cherry` after rejecting merge commits), not by commit hash, so #297's real contract survives without weakening the rule that host-side Git never runs in a worker clone.

`list` and `cleanup` both use the `forge` binary on `PATH`, the way `submit` already does.

## Why a worker's clone is safe to sandbox

The clone's branch lives only inside its own `.git`; the only write `begin` ever makes to the parent is the ledger entry it claims, atomically, before cloning. So a failure at any later step — the clone itself, the checkout of the new branch, the handoff-file write — undoes that claim along with `rm -rf` on the half-made clone, and there is no branch left behind in the parent to clean up, unlike the linked-worktree model this replaces.

Once a worker holds the VM's own forge SSH key and token (`docs/allod-change-run.md`), it can push commits onto a sibling's `agent/*` branch, create branches, open PRs, and comment under the agent account — all additive, visible, and reversible, and caught by the overseer's review of the branch once it reaches the forge. It cannot reach a protected default branch or a private repository, because the forge decides that server-side, not the VM: `protected-branches` and `protected-refs-policy` are backstops for the account that holds them, and the forge's own branch protection is the wall that does not depend on this tool at all.
