# forge

Forgejo CLI -- `gh` but for a self-hosted Forgejo instance.

```
forge [-R|--repo owner/repo] <resource> <command> [args]
```

## Config

| Variable | Meaning |
|---|---|
| `FORGE_URL` | Forgejo base URL; defaults to `https://forge.anarch.diy` |
| `FORGEJO_TOKEN` | Token value; overrides the token file when set |
| `FORGE_TOKEN_FILE` | Token file path; defaults to `~/.config/git/forgejo-token` |

When `FORGEJO_TOKEN` is unset, `forge` reads the token from `FORGE_TOKEN_FILE`.
Repo is inferred from `git remote get-url origin` when `-R`/`--repo` is omitted,
and `-R`/`--repo` may appear before the resource or after the command.

## PR commands

```bash
forge pr list
forge pr view <number>
forge pr create --title <title> [--head <branch>] [--base <branch>] \
  [--body <text> | --body-file <file>]
forge pr comment <number> [--body <text> | --body-file <file>]
forge pr edit <number> [--title <title>] [--body <text> | --body-file <file>]
forge pr review-comments <number>          # list inline comments with IDs
forge pr reply <number> <comment-id> [--body <text> | --body-file <file>]
forge pr find-by-head <branch>             # print PR number if open PR exists for branch
```

`pr create` defaults `--head` to the current branch and `--base` to the
repository's default branch. The `gh` short aliases are also supported:
`-t`, `-b`, `-F`, `-H`, and `-B`.

## Auth commands

```bash
forge auth status       # verify configured credentials
```

`auth status` checks the configured token source (`FORGEJO_TOKEN` env var or
token file) without exposing token material in output or process arguments.

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
forge issue list
forge issue view <number>
forge issue create --title <title> [--body <text> | --body-file <file>]
forge issue edit <number> [--title <title>] [--body <text> | --body-file <file>]
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
