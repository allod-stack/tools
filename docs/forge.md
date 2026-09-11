# forge

Forgejo CLI -- `gh` but for a self-hosted Forgejo instance.

```
forge [-R|--repo owner/repo] <resource> <command> [args]
```

## Config

| Variable | Meaning |
|---|---|
| `FORGE_URL` | Forgejo base URL; defaults to `https://forge.anarch.diy` |
| `FORGE_TOKEN_FILE` | Token file path; defaults to `~/.config/git/forgejo-token` |

`forge` reads the token only from `FORGE_TOKEN_FILE`, which must be a mode-0600
file: a token file with any group or other permission bit is refused with a
`chmod 600` hint before it is read (symlinks are judged by their target).
`FORGEJO_TOKEN` is no longer read: when it is set and non-empty, every command
except help output exits 1 before making any request or starting any child
process, and says so, so a stale export cannot be mistaken for a working
credential. An environment variable is inherited by every child process, while
a file is read only by code that opens it.
Repo is inferred from `git remote get-url origin` when `-R`/`--repo` is omitted,
and `-R`/`--repo` may appear before the resource or after the command.

## Errors

Any response that is not 2xx writes one line to stderr and exits 22:

```
forge: POST /repos/owner/repo/labels failed: HTTP 403: user should have a permission to write to a repo
```

Read the status to tell a permission problem (403) from a missing resource (404) or
a rejected payload (422). Redirects are not followed and count as failures, so a 3xx
body is never mistaken for the requested resource. An `http://` `FORGE_URL` against
an https-only forge reports `HTTP 308` rather than looking like an empty result.

The trailing text is the API's `message` when the response carries one. Otherwise it
is the start of the raw body, which for a proxy error is HTML rather than anything
the forge said; either way it is truncated to 200 characters, flattened to a single
line, and stripped of control bytes. A response with no body leaves just the status.

A transport failure -- the request never completing -- instead reports the
compatibility message `curl exit <n>` and returns the corresponding curl-style code.
The Go client preserves that established CLI contract even though requests are made
in-process; code 22 specifically means the forge answered and the answer was not a
success.

## PR commands

```bash
forge pr list [--state open|closed|all] [--limit <number>]
forge pr view <number> [--json <fields>] [--jq <expression>]
forge pr snapshot <number>                 # stable immutable commit metadata as JSON
forge pr create --title <title> [--head <branch>] [--base <branch>] \
  [--body <text> | --body-file <file>]
forge pr comment <number> [--body <text> | --body-file <file>]
forge pr edit <number> [--title <title>] [--body <text> | --body-file <file>]
forge pr close {<number> | <url> | <branch>} [--comment <text>] [--delete-branch]
forge pr review-comments <number>          # list inline comments with IDs
forge pr reply <number> <comment-id> [--body <text> | --body-file <file>]
forge pr find-by-head <branch>             # print PR number if open PR exists for branch
```

`pr create` defaults `--head` to the current branch and `--base` to the
repository's default branch. The `gh` short aliases are also supported:
`-t`, `-b`, `-F`, `-H`, and `-B`.

`pr close` accepts a PR number, full URL, or head branch name as its target.
Use `-c`/`--comment` to leave a closing comment and `-d`/`--delete-branch` to
delete the remote head branch after closing.

`pr list` defaults to open pull requests, the same as before. `-s`/`--state`
also takes `closed` or `all`, and with either of those a status column is
appended to each row: `open`, `closed`, or `merged YYYY-MM-DD` (the merge
date). `pr view` still prints `State: closed` for a merged and an abandoned
pull request alike; the status column, the `Merged:` line, and the `merged`/
`mergedAt` JSON fields below are what tell them apart. `pr list -s closed` is
the form for finding the pull request that actually settled an issue:

```bash
forge pr list -s closed
```

`pr view --json <fields>` prints only the requested fields as one JSON object,
instead of the rendered summary and comments: no header, no comments, no
reviews, and only one API request. Fields are comma-separated and the flag may
be repeated (`gh`'s multi-value shape): `--json number,title` and
`--json number --json title` name the same set. Valid fields: `number`,
`title`, `body`, `state`, `author`, `labels`, `milestone`, `url`, `createdAt`,
`updatedAt`, `closedAt`, `headRefName`, `baseRefName`, `merged`, `mergedAt`.
`merged` is the raw boolean and `mergedAt` the raw merge timestamp or `null`,
both straight from the API. Outside `--json`, a closed pull request's `pr
view` prints a `Closed:` line and then `Merged:   yes YYYY-MM-DD` or
`Merged:   no`; an open pull request prints neither. The JSON object form
(`--json body`) carries the body exactly as the API returned it, with a null
body rendered as the empty string. This lets an edit start from the body as it
currently is on the forge instead of a retyped rendering of it:

```bash
forge pr view 12 --json body --jq .body > body.md
# edit body.md
forge pr edit 12 --body-file body.md
```

`--jq <expression>` filters the printed object down to one value and requires
`--json`. Only a simple dotted field path is supported, such as `.body` or
`.author.login`; anything else is rejected. A string prints raw plus one
trailing newline (`jq -r`'s and `gh --jq`'s convention, not byte-for-byte) --
so `--jq .body` adds a newline the JSON object form does not have, and the
round trip above leaves the body with one more trailing newline than before
unless the edit trims it back off. Anything other than a string prints as
JSON.

`pr list`, `pr find-by-head`, and `pr close`'s branch-name lookup page
through every open pull request rather than stopping at the first page, so a
match past the first 50 is still found.

`pr snapshot` is the machine-readable interface for tools that need a pull
request's exact commits. It emits a deliberately small, versioned schema rather
than the Forgejo API response, so unrelated server fields can change without
breaking consumers:

```json
{
  "schema_version": 1,
  "pull_request": {
    "number": 12,
    "url": "https://forge.example/acme/widget/pulls/12",
    "title": "Improve widget",
    "body": "Why this change is useful",
    "state": "closed",
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-02T00:00:00Z",
    "closed_at": "2026-01-03T00:00:00Z",
    "merged": true,
    "merged_at": "2026-01-03T00:00:00Z"
  },
  "base": {
    "repository": {
      "owner": "acme",
      "name": "widget",
      "full_name": "acme/widget",
      "clone_url": "https://forge.example/acme/widget.git"
    },
    "ref": "master",
    "sha": "1111111111111111111111111111111111111111"
  },
  "head": {
    "repository": {
      "owner": "contributor",
      "name": "widget",
      "full_name": "contributor/widget",
      "clone_url": "https://forge.example/contributor/widget.git"
    },
    "ref": "topic",
    "sha": "2222222222222222222222222222222222222222"
  }
}
```

All projected fields are present; a missing PR body becomes the empty string.
`pull_request.closed_at` and `pull_request.merged_at` are `null` until the pull
request is closed or merged; every other field is always non-null.
`created_at`, `updated_at`, `closed_at`, and `merged_at` carry the API's full
RFC 3339 timestamps verbatim. The command fails instead of emitting partial or
contradictory JSON when the API omits repository identity, an HTTPS clone URL
without userinfo, query, or fragment, a ref, a 40/64-character hexadecimal Git
object ID, a valid `state` (`open` or `closed`), a parseable timestamp, or when
the merge fields disagree with each other (`merged: true` with a null
`merged_at`, or the reverse) or `closed_at` is set on a PR the API still calls
open. Both sides must use the same object format, and terminal-facing identity
fields cannot contain control characters. `base.repository` and
`head.repository` are independent, which is what lets the same contract
represent both same-repository and fork heads.

`base.ref` and `head.ref` must each be an ordinary branch name, with one
exception: `head.ref` may instead be the exact AGit pull-request ref
`refs/pull/<n>/head` (where `<n>` is this pull request's number), which
Forgejo reports for a pull request pushed with `git push -o agit` instead of
a pushed branch. Any other explicit `refs/*` ref, or a ref git itself would
reject (a leading `-`, whitespace, `:`, `^`, `~`, `*`, path traversal, and
so on), fails snapshot validation. The snapshot accepts the AGit head shape
because it is a read with no side effects and such pull requests exist on
forges; `allod pr explain` separately refuses to act on them, because the
allod workflow does not accept AGit submissions (see docs/pr-explain.md).

## Auth commands

```bash
forge auth status       # verify configured credentials
```

`auth status` checks the token in the configured token file (`FORGE_TOKEN_FILE`)
and names that file as the source, without exposing token material in output or
process arguments.

## Token commands

```bash
extract-token-safely | forge token verify
```

`token verify` reads a candidate token from stdin and checks it against the
Forgejo API. It does not use the configured token -- stdin is the only input.
If stdin is a TTY (no pipe), it exits immediately with a usage hint.

Safe calling patterns -- avoid putting the token in shell history or argv:

```bash
# Read interactively without echo, then verify
read -rs tok; printf '%s' "$tok" | forge token verify

# From a password manager or secret store
pass show forgejo/token | forge token verify
```

**Departures from `gh`:** `gh` has no equivalent of `token verify`. Token
rotation with `gh` requires either blindly replacing the stored credential
(`gh auth login --with-token`) or manually verifying with `curl` after
extracting the raw token via `gh auth token`. forge separates verification
from installation so you can validate a candidate token before committing to
it, without exposing the live credential. forge also has no `auth token`
(print the raw token) or `auth login` commands -- token material is never
printed to stdout or accepted as a command-line argument.

## Issue commands

```bash
forge issue list [--state open|closed|all] [--label <label>] \
  [--milestone <milestone>] [--limit <number>] [--search <query>]
forge issue view <number> [--json <fields>] [--jq <expression>]
forge issue create --title <title> [--body <text> | --body-file <file>] \
  [--label <label>] [--milestone <milestone>]
forge issue edit <number> [--title <title>] [--body <text> | --body-file <file>] \
  [--milestone <milestone> | --remove-milestone] \
  [--add-label <label>] [--remove-label <label>]
forge issue labels <number> [--add-label <label>] [--remove-label <label>] \
  [--set <label>] [--clear]
forge issue milestone <number> [<milestone> | --clear]
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

`issue create` accepts repeated `--label` values by label name or ID and
`--milestone` by title or ID. `issue edit` follows `gh` label and milestone
mutation names (`--add-label`, `--remove-label`, `--milestone`, and
`--remove-milestone`). `issue labels` and `issue milestone` are Forge-specific
helpers for focused label/milestone operations; they list current values when
called without changes.

`issue view --json <fields>` prints only the requested fields as one JSON
object, instead of the rendered summary and comments -- the same shape as
`pr view --json` (see PR commands above), minus `headRefName`/`baseRefName`.
Valid fields: `number`, `title`, `body`, `state`, `author`, `labels`,
`milestone`, `url`, `createdAt`, `updatedAt`, `closedAt`. `body` behaves the
same way as on `pr view --json`: the JSON object form carries it exactly as
the API returned it (a null body renders as the empty string), while
`--jq .body` prints it plus one trailing newline, not byte-for-byte. This lets
an edit start from the body as it currently is on the forge instead of a
retyped rendering of it (`--body-file` still replaces the whole body, so the
edit is not a merge -- just built from the real text instead of a guess at
it):

```bash
forge issue view 12 --json body --jq .body > body.md
# edit body.md
forge issue edit 12 --body-file body.md
```

`issue list` pages through as many results as `--limit` needs (the server
caps a single page at 50) rather than trusting one request to hold the whole
limit.

## Label commands

```bash
forge label list [--limit <number>] [--search <query>] \
  [--sort created|name] [--order asc|desc]
forge label create <name> [--color <hex>] [--description <text>] \
  [--force] [--exclusive] [--archived]
forge label edit <id-or-name> [--name <name>] [--color <hex>] \
  [--description <text>] [--exclusive | --no-exclusive] \
  [--archived | --no-archived]
forge label delete <name> [--yes]
```

`label create`, `label edit`, `label list`, and `label delete` follow the
corresponding `gh label` command shapes where Forgejo supports the same data.
Colors are six-digit hex values with or without a leading `#`; `label create`
uses a random color when none is supplied, matching `gh`. The
`--exclusive`/`--archived` label fields are Forgejo-specific extensions.

`label list` pages through as many results as `--limit` needs, and every
lookup of a label by name (`label create --force`, `label edit`, `label
delete`) pages through the whole repository label set so a match past the
first page still resolves.

## Milestone commands

```bash
forge milestone list [--state open|closed|all]
forge milestone view <id-or-title>
forge milestone create --title <title> [--description <text>] \
  [--due YYYY-MM-DD] [--state open|closed]
forge milestone edit <id-or-title> [--title <title>] \
  [--description <text>] [--due YYYY-MM-DD] [--state open|closed]
forge milestone delete <id-or-title>
```

Milestone lookups accept IDs or exact titles. `--due YYYY-MM-DD` is sent to the
API as midnight UTC for that date.

`milestone list` and every lookup of a milestone by title (`milestone view`,
`milestone edit`, `milestone delete`, `issue create --milestone`, `issue edit
--milestone`, `issue milestone`) page through every milestone in the
repository, so a match past the first page still resolves.

## Project commands

```bash
forge project <command>
```

Project commands intentionally report unavailable. The Forgejo API served by
`forge.anarch.diy` exposes labels and milestones, but not repository project
endpoints. Use the web UI for project boards, or organize issues through labels
and milestones from the CLI.
