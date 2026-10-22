# Sweep: change, forge, workspace

The first full sweep of the three feature files, run on allod-dev as the
`allod-agent` account on the day the map landed. Every drive passed; the
three sabotage runs each turned their suite red; two paths are gaps, named
at the end. The code under test is commit `3909ce4`, the base the map PR was
cut from, since the PR itself adds no code. A verifier reruns every command
below at the PR head and compares.

Trimmed output follows each command. `$C` is the scratch clone.

## Launch and health check

```
$ cd $C && git status --porcelain | wc -l && git rev-parse --short HEAD
0
3909ce4
$ nix build .#allod --print-out-paths --no-link
/nix/store/kb8y7hklnb7saxks7as0xl2vrfxncwqj-allod-0.1.0
$ nix build .#forge --print-out-paths --no-link
/nix/store/3h4sxwbr13abpjar0z6k0j1kvkspjzpr-forge-0.1.0
$ export ALLOD_UNDER_TEST=/nix/store/kb8y7hklnb7saxks7as0xl2vrfxncwqj-allod-0.1.0/bin/allod
$ "$ALLOD_UNDER_TEST" change --help | head -1
Usage:
$ readlink -f "$(command -v allod)"        # the deployed generation, not the build above
/nix/store/a1brkvgizg8g8zbvrv7f9qr1bgyxjw20-allod-0.1.0/bin/allod
```

## Baseline: `allod check`

```
$ allod check
Gate for . on x86_64-linux
==> passive output devShells
ok
==> passive output packages
ok
==> check x86_64-linux.allod (built)
==> check x86_64-linux.allod-parity (built)
==> check x86_64-linux.cascade-suites (built)
==> check x86_64-linux.flake-status-suite (built)
==> check x86_64-linux.flake-update-cascade (built)
==> check x86_64-linux.forge (built)
==> check x86_64-linux.git-hook-suites (built)
==> check x86_64-linux.go-checks (built)
All 10 steps passed (checks: 8 built).
exit 0
```

## change.md

```
$ bash tests/allod-change.sh | tail -n 2
All 454 allod change tests passed.
$ nix develop -c bash tests/allod-change-run.sh | tail -n 2
Tests run: 146
All 146 allod change run tests passed.
```

Selected `ok` lines, by path (the full transcript is 454 lines):

```
ok 15 - begin requires -d on protected repo
ok 43 - begin -d creates a standalone clone, not a linked worktree
ok 44 - begin -d never makes an alternates clone
ok 45 - begin -d copies the parent's objects rather than hardlinking them
ok 60 - begin --resume picks up an existing remote agent branch
ok 91 - list prints one row per ledgered clone
ok 94 - the ledger entry's first line is the clone's path
ok 95 - the ledger entry's second line is the clone's branch
ok 116 - cleanup on a missing clone's path drops its ledger entry instead of refusing
ok 121 - cleanup refuses a clone whose only evidence of being pushed is its own local remote-tracking ref
ok 170 - record commits and pushes a protected repo's clone
ok 215 - record exits 4 when nothing changed and nothing unpushed
ok 274 - record never force-pushes
ok 275 - submit creates PR with validation body
ok 280 - submit appends depends-on line
ok 285 - submit dry-run prints assembled body
ok 365 - cleanup removes a clean clone
```

Hand drive of the attribution-trailer path, with the fixture recipe from
`change.md`:

```
$ printf 'add b\n\nCo-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>\n' > $T/msg
$ "$ALLOD_UNDER_TEST" change record -M $T/msg -f b; echo "record exit $?"
[agent/trailer 60ceea0] add b
 1 file changed, 1 insertion(+)
To /tmp/tmp.qtlBlfCyLV/remotes/fx.git
 * [new branch]      HEAD -> agent/trailer
Branch: agent/trailer
Commit: 60ceea0
record exit 0
$ git -C $T/remotes/fx.git log -1 --format=%B agent/trailer
add b

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

Expected: refused, nothing pushed. Observed: accepted and pushed. Filed as
allod/tools#293 and listed under what usually lies; the validation line in
allod/tools#292 that sabotages `record` to accept a trailer has nothing to
sabotage until that lands.

## forge.md

Recording server:

```
$ nix develop -c go test -count=1 -v ./cmd/forge -run 'TestPRMerge|TestAuthStatus|TestTokenVerify|TestMutations|TestPRClose|TestIssueClose|TestRead|TestEmptyTokenFile|TestUnreadableTokenFile' | grep -E '^(--- PASS: Test[A-Za-z]+ |ok|FAIL|PASS)'
--- PASS: TestEmptyTokenFileMakesNoRequest (0.00s)
--- PASS: TestUnreadableTokenFileMakesNoRequest (0.00s)
--- PASS: TestAuthStatusRejectsTokenFileFlag (0.00s)
--- PASS: TestAuthStatus (0.00s)
--- PASS: TestTokenVerify (0.00s)
--- PASS: TestIssueClose (0.00s)
--- PASS: TestMutations (0.01s)
--- PASS: TestTokenVerifyStripsNULBytesFromStdin (0.00s)
--- PASS: TestPRClose (0.00s)
--- PASS: TestPRMerge (0.02s)
--- PASS: TestReadPRList (0.00s)
--- PASS: TestReadPRListState (0.00s)
--- PASS: TestReadPRView (0.00s)
--- PASS: TestReadPRViewMerged (0.00s)
--- PASS: TestReadPRReviewComments (0.00s)
--- PASS: TestReadPRFindByHead (0.00s)
--- PASS: TestReadIssueList (0.00s)
--- PASS: TestReadIssueView (0.00s)
--- PASS: TestReadIssueViewTimestamps (0.00s)
--- PASS: TestReadPRViewTimestamps (0.00s)
--- PASS: TestReadLabelList (0.00s)
--- PASS: TestReadIssueLabels (0.00s)
--- PASS: TestReadIssueMilestone (0.00s)
--- PASS: TestTokenVerifyRejectsTokenFileFlag (0.00s)
PASS
ok  	forge.anarch.diy/allod/tools/cmd/forge	0.059s
```

Live, read-only, from the verifier seat (the `label create` line is the
one write in this sweep that was not meant to succeed; see the gaps):

```
$ forge auth status
Authenticated as allod-agent (/home/allod/.config/git/forgejo-token)
exit 0

$ forge pr list -R allod/tools -L 3
289  change begin: validate the description outside the caller's cwd           allod-agent  agent/t284-worktree-parity → master
288  pull-all: report unpushed commits on the current branch, not up to date   allod-agent  agent/pull-all-unpushed → master
285  wake-agent: event-triggered headless Pi continuation with a footer badge  allod-agent  agent/wake-agent-281 → master
exit 0

$ forge pr view 1 -R allod/tools --json number,state,merged,mergedAt
{"merged":true,"mergedAt":"2026-06-01T22:43:14+01:00","number":1,"state":"closed"}
exit 0

$ forge issue view 292 -R allod/tools --json number,title
{"number":292,"title":"Verification feature map: behavior-level map the verify-merge skill drives"}
exit 0

$ forge label list -R allod/tools
4    bug             ee0701  false  false  Something is not working
5    duplicate       cccccc  false  false  This issue or pull request already exists
6    enhancement     84b6eb  false  false  New feature
7    help wanted     128a0c  false  false  Need some help
8    invalid         e6e6e6  false  false  Something is wrong
9    question        cc317c  false  false  More information is needed
10   wontfix         ffffff  false  false  This won't be fixed
188  landed?         fbca04  false  false  Groom: a merged PR or master commit references this, no Closes line; confirm by hand
189  ready-to-merge  0e8a16  false  false  Groom: base is master, merges clean, no unanswered review findings
190  blocked         d93f0b  false  false  Groom: conflicts with master, based on another open PR, or has unanswered review findings
191  stale           c5def5  false  false  Groom: no comment or commit in 30 days
192  decision        5319e7  false  false  Groom: open past 30 days with a yes/no the owner has to answer
exit 0

$ forge label create verify-probe -R allod/tools   # write the token has no scope for
Label created: 321 verify-probe
exit 0

$ forge pr merge 1 -r -R allod/tools   # closed PR, refused before any POST
forge: PR #1 is not open (state: closed)
exit 1

$ FORGEJO_TOKEN=x forge auth status
forge: FORGEJO_TOKEN is no longer read; unset it and put the token in a mode-0600 file named by FORGE_TOKEN_FILE (currently /home/allod/.config/git/forgejo-token)
exit 1
$ forge label delete verify-probe --yes -R allod/tools
Label deleted: verify-probe
$ forge label list -R allod/tools | grep -c verify-probe
0
```

## workspace.md

```
$ bash tests/workspace/pull-all.sh | tail -n 1
✅ All 38 pull-all tests passed.
$ bash tests/workspace/work-diff.sh | tail -n 1
✅ All 37 work-diff tests passed.
```

Hand drive against real git, with the fixture recipe from `workspace.md`:

```
$ pull-all   # real git; fixtures: ahead (master, 1 unpushed), diverged (master, local and remote commits), noup (branch with no upstream), clean
  ahead                     up to date [master]
  clean                     up to date [master]
  diverged                  error      [pull failed: From /tmp/tmp.kgKgBSsfAl/remotes/diverged]
  noup                      error      [pull failed: There is no tracking information for the current branch.]
exit 0

$ pull-all --switch
  ahead                     up to date [master]
  clean                     up to date [master]
  diverged                  error      [pull failed: hint: You have divergent branches and need to specify how to reconcile them.]
  noup                      skipped    [on feature — no remote tracking branch]
exit 0

$ git -C ~/work/ahead status -sb
## master...origin/master [ahead 1]
$ git -C ~/work/diverged status -sb
## master...origin/master [ahead 1, behind 1]
```

## Sabotage: each file turns its suite red

Run in the clone, each edit reverted before the next; `git status
--porcelain` was empty afterwards.

```
### forge: prConfirmMerge returns right after the merged flag, skipping the base read-back
 cmd/forge/pr.go | 1 +
 1 file changed, 1 insertion(+)
+	return mergeSHA
--- FAIL: TestPRMerge (0.01s)
    --- FAIL: TestPRMerge/method_-m (0.00s)
        pr_merge_test.go:64: request count = 3, want 4
        pr_merge_test.go:64: request 3 not made; only 3 requests recorded
    --- FAIL: TestPRMerge/method_-r (0.00s)
        pr_merge_test.go:64: request count = 3, want 4
        pr_merge_test.go:64: request 3 not made; only 3 requests recorded
    --- FAIL: TestPRMerge/method_-s (0.00s)
        pr_merge_test.go:64: request count = 3, want 4
        pr_merge_test.go:64: request 3 not made; only 3 requests recorded
    --- FAIL: TestPRMerge/read-back_where_the_base_tip_differs_but_compare_says_0_ahead (0.00s)
        pr_merge_test.go:268: request count = 3, want 5
        pr_merge_test.go:268: request 3 not made; only 3 requests recorded
    --- FAIL: TestPRMerge/read-back_where_compare_says_1_ahead_exits_non-zero_with_both_hashes (0.00s)
        pr_merge_test.go:295: unexpectedly succeeded, stdout: PR merged: https://forge.example/acme/widget/pulls/12 (master at bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb)
    --- FAIL: TestPRMerge/delete-branch:_branch_is_gone_(404) (0.00s)
        pr_merge_test.go:319: got ("PR merged: https://forge.example/acme/widget/pulls/12 (master at bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb)\n", 0), want ("PR merged: https://forge.example/acme/widget/pulls/12 (master at bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb) (branch topic deleted)\n", 0)
        pr_merge_test.go:322: stderr = "forge: warning: branch topic not deleted\n", want empty on a clean delete
    --- FAIL: TestPRMerge/delete-branch:_existence_check_fails_with_another_status (0.00s)
        pr_merge_test.go:344: stderr = "forge: warning: branch topic not deleted\n", want the unconfirmed-deletion warning with the status
go test exit 1

### workspace: pull-all --switch no longer skips a branch with unpushed commits
-      if [[ "$unpushed" -gt 0 ]]; then
+      if false; then
  pull-fail                 error      [pull failed: network unavailable]
  switched                  pulled     [master]
  unpushed                  up to date [master]
  .profile                  up to date [master]
pull-all.sh exit 1

### change: begin -d clones without --no-hardlinks
-	if output, status := captureCommand("", nil, true, "git", "clone", "--no-hardlinks", "--no-checkout", parent, path); status != 0 {
+	if output, status := captureCommand("", nil, true, "git", "clone", "--no-checkout", parent, path); status != 0 {
built /tmp/claude-1000/-home-allod-work/c930f364-677d-442d-841d-9f2aa09f0092/scratchpad/allod-sabotaged
ok 43 - begin -d creates a standalone clone, not a linked worktree
ok 44 - begin -d never makes an alternates clone
not ok 45 - begin -d copies the parent's objects rather than hardlinking them
clone object has the same inode as the parent's own object store
allod-change.sh exit 1

### tree after reverts
(end)
```

## Gaps

- **Forge write paths.** No scratch repository the verifier account owns is
  named in this deployment's private memory, so `pr create`, `comment`,
  `edit`, `merge --rebase -d --match-head-commit`, `pr close -d`, `label
  create`/`delete` and `issue create`/`close` were driven only through the
  recording server. The `label create` probe above was meant to show the
  no-scope refusal and instead succeeded on allod/tools, because this
  account's token carries label scope there; it was deleted at once, and
  the map now says a label write is driven only against the scratch
  repository.
- **`record` attribution trailer.** Accepted today (allod/tools#293); the
  drive is recorded above as a lie, not a pass.
