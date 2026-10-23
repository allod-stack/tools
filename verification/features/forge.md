# forge

`forge` is the Forgejo command line every merge, comment and issue goes
through, and `forge pr merge` is the one write the merge ladder lets an
agent make on a default branch. The long form is `docs/forge.md`; this file
says what to drive and what the result must look like.

## What exists

- Credential: the token is read only from `FORGE_TOKEN_FILE` (default
  `~/.config/git/forgejo-token`), which must be mode 0600, judged by the
  symlink target. A set, non-empty `FORGEJO_TOKEN` makes every command but
  help exit 1 before any request. `auth status` prints the account and the
  file; `token verify` reads a candidate token from stdin and never from an
  argument, and strips trailing whitespace and NUL bytes.
- Repository: `-R owner/repo` before the resource or after the command,
  otherwise inferred from `git remote get-url origin`.
- Error contract: any non-2xx answer is one stderr line, `forge: <METHOD>
  <path> failed: HTTP <status>: <message>`, exit 22; redirects count as
  failures; a transport failure prints `curl exit <n>` and exits with that
  code. A client-side refusal (a closed PR, a missing flag, an unknown
  option) exits 1 with its own message and makes no request.
- Pull requests: `list` (open by default, `-s closed|all` adds a status
  column `open`, `closed` or `merged YYYY-MM-DD`, paged past 50), `view`
  (header, comments, reviews; `--json <fields>` and `--jq .path` print
  fields only; a closed PR prints `Merged: yes <date>` or `Merged: no`),
  `snapshot` (versioned JSON of the immutable commits, fails on
  contradictory data), `review-comments`, `reply`, `edit`, `comment`,
  `create` (head defaults to the current branch, base to the repository
  default), `close [-c] [-d]` by number, URL or branch, `find-by-head`
  (`-s open`, default, a bare number; `-s all` prints `<number>\t<state>`,
  state `open`, `closed`, or `merged`, for a caller that must find a head
  branch's pull request in any state).
- `pr merge <target> (-m|-r|-s) [-d] [--match-head-commit <sha>] [-t] [-b|-F]`:
  reads the PR; refuses when it is not open, already merged, not mergeable
  or has no `mergeable` field (exit 1, no POST); refuses when
  `--match-head-commit` differs from the head just read (exit 1, no POST);
  always sends `head_commit_id`, so a push in between is refused by the
  forge and surfaced verbatim (exit 22); after a 200 reads the PR back and
  then the base branch, and when the base tip is not the merge commit asks
  `compare` and exits non-zero with both hashes if the base does not
  contain it (allod/tools#246). With `-d` it confirms the head branch is
  gone (404) and prints `(branch <name> deleted)`, or warns when it is still
  there or the check failed. Not implemented: `--auto`, `--admin`,
  `--author-email`, fast-forward-only, manually-merged.
- Issues: `list` (`-s`, `-l`, `-m`, `-L`, `-S`), `view` (`--json`,
  `--jq`), `create` (labels and milestone resolved by name first), `edit`
  (title, body, milestone, add and remove labels), `labels` (add, remove,
  set, clear), `milestone` (show, set, clear), `comment`, `close` (`-r
  completed|not planned|duplicate`, `--duplicate-of`, a comment posted
  before the close).
- Labels: `list`, `create` (`-c` hex, `-d`, `-f` updates an existing one),
  `edit` by id or name, `delete <name> --yes`. Milestones: `list`, `view`,
  `create`, `edit`, `delete`. `project`: refused, the API has no endpoint.
- Bodies: `-b` text or `-F file` (`-` for stdin) on every content command;
  NUL bytes are stripped, trailing newlines kept; `--body` never interprets
  `\n`.

## How a user reaches it

- `forge [-R owner/repo] <resource> <command> [args]` on any machine with a
  token file; `FORGE_URL` names the forge (default
  `https://forge.anarch.diy`).
- `allod change submit` calls `forge pr create`; the `verify-merge` skill
  calls `pr view`, `pr comment -F` and `pr merge --rebase -d
  --match-head-commit`; `allod pr explain` calls `pr snapshot`.
- Writes land only where the account's token carries scope. Which
  repositories that is differs by account and by namespace, and a 403 is
  the forge's answer, not the tool's (allod/tools#100).

## How to drive it

Two drivers. The first is the recording server behind the HTTP boundary,
where the forge would be, and it is the proof for every path, including the
ones that must never be run against a real repository. The second is the
live forge from the verifier seat: read-only paths against any repository,
write paths only against the scratch repository the verifier account owns,
`$SCRATCH`, named in the deployment's private memory. With no `$SCRATCH`,
every write path is a gap with that as the blocker.

Recording server, in the clone at `$HEAD`:

```
nix develop -c go test -count=1 -v ./cmd/forge \
  -run 'TestPRMerge|TestAuthStatus|TestTokenVerify|TestMutations|TestPRClose|TestIssueClose|TestRead|TestEmptyTokenFile|TestUnreadableTokenFile'
```

Expected: `PASS` and `ok  forge.anarch.diy/allod/tools/cmd/forge` with a
duration, not `(cached)`. The path each subtest proves:

| Entry point and path | Expected subtest |
| --- | --- |
| `auth status` success, 401, 403, 500, carriage return, rejects arguments | `TestAuthStatus/valid_token`, `/status_401`, `/status_403`, `/status_500`, `/carriage_return_is_stripped`, `/rejects_options_and_arguments` |
| token file empty or unreadable makes no request | `TestEmptyTokenFileMakesNoRequest`, `TestUnreadableTokenFileMakesNoRequest` |
| `token verify` success, invalid, empty stdin, newline, quote, backslash, no configured credential | `TestTokenVerify/valid_token_from_stdin`, `/invalid_token`, `/empty_stdin`, `/rejects_newline`, `/rejects_quote`, `/rejects_backslash`, `/works_without_a_configured_credential` |
| `pr merge` success per method | `TestPRMerge/method_-m`, `/method_-r`, `/method_-s` (four requests: read, POST, read-back, base branch) |
| `pr merge` client refusals | `TestPRMerge/refuses_a_closed_PR`, `/refuses_an_already-merged_PR`, `/refuses_an_unmergeable_PR`, `/refuses_a_PR_whose_mergeable_field_is_missing`, `/flag_validation/*` |
| `pr merge --match-head-commit` moved head | `TestPRMerge/match-head-commit_mismatch_refuses_before_any_POST`; `/match-head-commit_match_proceeds` |
| `pr merge` forge refusal surfaced verbatim | `TestPRMerge/POST_failure_surfaces_the_forge's_message` |
| `pr merge` read-back (allod/tools#246) | `TestPRMerge/read-back_where_the_PR_still_says_merged_false`; `/read-back_where_the_base_tip_differs_but_compare_says_0_ahead`; `/read-back_where_compare_says_1_ahead_exits_non-zero_with_both_hashes`; `/read-back:_base_branch_response_without_a_commit_id_fails` |
| `pr merge -d` | `TestPRMerge/delete-branch:_branch_is_gone_(404)`, `/delete-branch:_branch_still_present`, `/delete-branch:_existence_check_fails_with_another_status` |
| `pr merge` message fields | `TestPRMerge/-t_sets_MergeTitleField`, `/-b_sets_MergeMessageField`, `/-F_file_sets_MergeMessageField`, `/-F_-_reads_the_message_from_stdin` |
| `pr create`, `edit`, `comment`, `reply` | `TestMutations/pr_create_infers_default_branch_and_posts_multiline_body`, `/pr_create_with_short_flags_avoids_metadata_lookup`, `/pr_edit_preserves_an_explicitly_empty_body`, `/pr_comment_preserves_multiline_content`, `/pr_reply_looks_up_the_original_inline_comment_first` |
| `pr close`, `-c`, `-d`, by URL, by branch, no match | `TestPRClose/plain_close_makes_one_API_request`, `/close_with_a_comment_posts_it_before_closing`, `/delete-branch_fetches_PR_details_then_deletes_after_closing`, `/accepts_a_PR_URL_as_the_close_target`, `/branch_target_looks_up_the_PR_first`, `/rejects_a_branch_with_no_matching_PR`, `/skips_branch_deletion_when_head_equals_base` |
| `pr list`, `view`, `view --json`, merged rendering, `review-comments`, `find-by-head`, `find-by-head -s all` | `TestReadPRList`, `TestReadPRListState`, `TestReadPRView`, `TestReadPRViewMerged`, `TestReadPRViewTimestamps`, `TestReadPRReviewComments`, `TestReadPRFindByHead`, `TestReadPRFindByHeadStateAll` |
| `issue create`, `edit`, `labels`, `milestone`, `comment` | `TestMutations/issue_*` (sixteen subtests, label and milestone resolution by name first) |
| `issue close` plain, with comment, duplicate, not planned | `TestIssueClose/*` |
| `issue list`, `view`, `labels`, `milestone` reads | `TestReadIssueList`, `TestReadIssueView`, `TestReadIssueViewTimestamps`, `TestReadIssueLabels`, `TestReadIssueMilestone` |
| `label create`, `--force`, `edit`, `delete`; `milestone create`, `edit`, `delete` | `TestMutations/label_*`, `TestMutations/milestone_*`; `TestReadLabelList`, `TestReadMilestoneList`, `TestReadMilestoneView` |

Live, from the verifier seat; `forge` below is `$FORGE_UNDER_TEST`. The public
`service.forge` profile in allod/profiles would be a forge of its own, but it
composes only through the service builder in allod/archetypes and is not
reachable from this repository, so the live forge is the deployment's. Read-only:

| Command | Expected |
| --- | --- |
| `forge auth status` | `Authenticated as <verifier account> (<token file path>)`, exit 0 |
| `FORGEJO_TOKEN=x forge auth status` | one line naming `FORGEJO_TOKEN` and the file to use, exit 1, no request |
| `forge pr list -R allod/tools -L 3` | three rows `number  title  author  head → base`, exit 0 |
| `forge pr view 1 -R allod/tools --json number,state,merged,mergedAt` | one JSON object with `"merged":true` and a `mergedAt` timestamp |
| `forge issue view <n> -R allod/tools --json number,title` | one JSON object |
| `forge label list -R allod/tools` | the repository's label rows |
| `forge pr list -R <owner>/<no-such-repo>` | `forge: GET /repos/.../pulls?... failed: HTTP 404: The target couldn't be found.`, exit 22 |
| `forge pr merge 1 -r -R allod/tools` (a closed PR) | `forge: PR #1 is not open (state: closed)`, exit 1, no POST |

Live, writes, only against `$SCRATCH`, in a clone of it under a temporary
directory, one pass:

```
git switch -c agent/verify-probe && echo probe > probe && git add probe && git commit -qm probe
git push -u origin agent/verify-probe
forge pr create -R "$SCRATCH" -t "verify probe" -b "probe"           # prints the PR URL; note its number N
forge pr comment N -R "$SCRATCH" -b "probe comment"                   # prints the comment URL
forge pr edit N -R "$SCRATCH" -t "verify probe (edited)"
forge pr view N -R "$SCRATCH" --json headRefName,state                 # open, agent/verify-probe
head=$(git rev-parse HEAD)
forge pr merge N -R "$SCRATCH" --rebase -d --match-head-commit "$head"  # PR merged: <url> (<base> at <sha>) (branch agent/verify-probe deleted)
git ls-remote origin refs/heads/<base>                                 # the sha the merge line printed
git ls-remote origin refs/heads/agent/verify-probe                     # empty
forge pr merge N -R "$SCRATCH" --rebase                                # forge: PR #N is not open ..., exit 1
forge label create verify-probe -R "$SCRATCH" -c '#ff0000' && forge label delete verify-probe --yes -R "$SCRATCH"
forge issue create -R "$SCRATCH" -t "verify probe" -b "probe" && forge issue close <m> -R "$SCRATCH" -r "not planned"
```

Expected: every line exits 0 except the second merge, which exits 1 before
any POST; the base tip read with `git ls-remote` equals the sha in the
`PR merged:` line, which is the proof of the merge; `forge pr list -R
"$SCRATCH"` is empty afterwards and `forge label list -R "$SCRATCH"` has no
`verify-probe`.

## What usually lies

- **The merged flag.** The forge once reported a PR merged while the base
  branch had not moved (allod/tools#246). `pr merge` reads the base back and
  exits non-zero when it does not contain the merge commit; the verifier's
  own proof of a merge is `git ls-remote origin refs/heads/<base>` showing
  that commit, never `merged: true`, never the `PR merged:` line alone.
- **`git push --dry-run` proves nothing about push access.** It prints
  `[new branch]` for a ref the pre-receive hook will refuse, because a dry
  run never reaches the hook. Push access is proved by a real push to
  `$SCRATCH`, and a refused push to a protected branch is the wall working.
- **A rejection with no text.** The bash `forge` ran `curl -sf`, which
  discarded the status and body and left a bare exit 22 (allod/tools#100).
  The Go one prints the status and message, but the status still vanishes
  through a pipe: `forge ... | tee` reports `tee`'s 0. Read `$?` directly.
- **A write that succeeds on a real repository.** Token scope differs by
  account and by namespace: the same `label create` is a 403 under one
  account and a success under another, and the verifier account on allod-dev
  created one on allod/tools during the first sweep and had to delete it.
  A label or issue write is driven only against `$SCRATCH`; a success
  elsewhere is pollution to undo, not proof.
- **`State: closed` for merged and abandoned alike.** `pr view` prints the
  same state for both; the `Merged:` line, the `-s closed` status column
  and the `merged`/`mergedAt` JSON fields are what tell them apart, and
  `pr list` with no `-s` hides closed PRs entirely.
- **`FORGEJO_TOKEN` in the shell.** A stale export makes every command exit
  1 with the same message, which reads like a broken credential when the
  file is fine; the message names the variable.
- **A cached `go test`.** `ok ... (cached)` is the previous run; `-count=1`
  is the one that ran now.
- **Exit 1 and exit 22 are different refusers.** Exit 1 is `forge` itself
  before any request (a closed PR, a moved head under `--match-head-commit`,
  a bad flag); exit 22 is the forge answering. A proof row names which, and
  a merge "refused" with exit 1 made no POST, which the recording server
  shows as a request count.
- **`--match-head-commit` compares against the head read a moment ago.**
  The race after that read is closed by `head_commit_id` in the POST, which
  the forge refuses; the proof that it was sent is the request body in
  `TestPRMerge/method_*`, not the flag's own check.
