# verify-session

`verify-session` runs the cold verifier seat for one pull request as a
single enforced command: it checks the seat rules, cuts a scratch clone of
the pull request's head, writes the verifier brief, and runs the agent
headless with its output captured. The seat refusals are the priority —
each must stop the run before any clone is cut or any agent starts — and
the exit code is the contract a caller reads: `1` argument and credential
errors, `2` a refused seat, `3` a seat check that could not be answered,
`4` the pull request could not be read, `5` the scratch clone failed, `6`
the verifier timed out or exited non-zero.

## What exists

- `verify-session [--token-file <path>] [--timeout <duration>]
  <owner/repo#number>`; `-h`/`--help` prints the usage block and exits 0.
  The pull request argument must match `owner/repo#number`, and exactly one
  is required.
- Token resolution: `--token-file` overrides `FORGE_TOKEN_FILE`, the file
  must exist, and it is exported as `FORGE_TOKEN_FILE` for every `forge`
  call and for the verifier. The script names no credential path of its own.
- Seat check, author credential: reads `forge auth status`, parses the
  account, reads the pull request's author and head ref with `forge pr view
  --json author,headRefName`, and refuses with exit 2 when the account is
  the author.
- Seat check, live clone: reads `allod change list`, matches a tab-separated
  row on repo and head branch, and refuses with exit 2 when this
  environment already holds a clone of the branch.
- Fail-closed seat checks: a non-zero `forge auth status` or `allod change
  list`, or an account the output does not name, refuses with exit 3 and
  starts no agent. An unreadable pull request or a missing author/head ref
  refuses with exit 4.
- Scratch clone: `forge pr snapshot` supplies `head.sha` and
  `base.repository.clone_url`; the script clones that URL, fetches
  `refs/pull/<n>/head`, and checks out the head commit detached by hash
  (exit 5 on any failure). It never clones the author's checkout.
- Brief: generated from the repo, number, clone path and head sha, naming
  `verify-merge`, the seat, the ledger lookup, the score-first rule, the
  no-map `Validation` fallback, and the `PROOF_COMMENT=` contract.
- Headless run: `timeout -- <duration> pi --no-session -p "<brief>"`, run
  from the scratch clone with the transcript captured; exit 124 is reported
  as a timeout (exit 6) and any other non-zero exit as a failed verifier
  (exit 6). `--timeout` is passed after `--`, so an option-shaped value
  reaches `timeout` as its duration and is rejected as an invalid time
  interval, never read as a `timeout` flag.
- Proof extraction: the first `PROOF_COMMENT=<value>` line in the transcript
  is printed as `proof-comment: <value>`; no line, or an exact `none`,
  prints `proof-comment: none reported` (a `none` carrying a reason is
  printed verbatim).

## How a user reaches it

- `verify-session` on PATH, from the dev VM and nexus home profiles
  (`allod/archetypes#153`), against an `allod-tools` pin that carries
  `verify-session/verify-session`; during development it runs from the
  checkout as `verify-session/verify-session`.
- A verifier credential file, mode 0600, named by `--token-file` or
  `FORGE_TOKEN_FILE`, with `FORGEJO_TOKEN` unset.
- `forge`, `git`, `jq`, `timeout`, `pi`, `allod change list`, and the text
  tools the script calls (`sed`, `awk`, `grep`, `cut`) on PATH; `pi` must
  reach a provider credential.
- A session that is not the pull request's author and does not hold a live
  clone of its branch.

## How to drive it

The suite is the driver, and it stands a fake `forge`, `git`, `allod` and
`pi` on PATH, so no process reaches the real forge, a real repository, or a
real agent:

```
bash tests/verify-session.sh
```

Expected: the closing `All 44 verify-session tests passed.` The refusal
paths are driven first — that is what the ordering in the suite enforces —
and each seat refusal is asserted to leave the fake `pi` log absent (the
author and live-clone refusals also leave the fake `git` log absent). The
timeout refusal cuts the scratch clone before it fails, so only its `pi`
log is asserted absent.

| Entry point and path | Expected `ok` line |
| --- | --- |
| help | `help exits zero`; `help prints usage`; `help names the token flag`; `help names the pull request form` |
| argument and token refusals | `a missing pull request is refused`; `the refusal names the missing argument`; `a missing token is refused`; `the refusal names the missing token` |
| seat refusal, author credential | `the author's credential is refused`; `the refusal names the author seat`; `no verifier runs on a refused seat`; `no scratch clone is cut on a refused seat` |
| seat refusal, live clone | `a live clone of the branch is refused`; `the refusal names the live clone`; `no verifier runs while a clone holds the branch` |
| seat refusal, fail closed | `an unreadable clone list is refused`; `the refusal names the failed list`; `no verifier runs when the clone list cannot be read`; `a failed auth check is refused`; `the refusal names the failed auth check`; `no verifier runs when the seat cannot be confirmed` |
| the seat runs | `the verifier seat runs`; `the verifier's output is the script's output`; `the proof comment link is reported`; `the verifier process is invoked`; `a scratch clone is cut` |
| scratch clone | `the pull request ref is fetched`; `the head is checked out by hash`; `the forge remote is cloned` |
| verifier invocation | `the verifier runs headless without a session`; `the verifier runs in print mode`; `the argument token file overrides the ambient one for the verifier`; `the verifier runs inside the scratch clone, not the caller's directory` |
| brief content | `the brief names the skill`; `the brief names the pull request`; `the brief names the ledger lookup`; `the brief names the no-map validation fallback`; `the brief names the score-first rule`; `the brief names the proof-comment contract` |
| timeout refusal | `an option-shaped timeout is refused, not run`; `the refusal reports the failed timeout`; `no verifier runs with an invalid timeout` |
| verifier failure | `a failed verifier is reported`; `the refusal names the failed verifier` |

The one path the suite does not reach is a real clone and a real agent. To
drive the author-seat refusal against the live forge without starting an
agent, list the open pull requests in a repository the calling account can
read, take a number whose author is that account, then run the wrapper with
that account's own token:

```
forge pr list -R allod/tools -s open
verify-session --token-file "$HOME/.config/git/forgejo-token" allod/tools#<n>
```

Expected: exit 2, a refusal naming the author seat, and no `pi` process.

## What usually lies

- **The fake `git` creates a directory and fetches nothing.** The suite
  proves the clone, fetch and checkout commands were issued, not that a real
  clone of `refs/pull/<n>/head` succeeds; a live run is the only proof of the
  remote's answer.
- **The fake `pi` never starts an agent.** The suite proves the wrapper
  invoked `pi` with the right arguments and brief, not that a verifier ran or
  posted a proof comment; the transcript and the comment URL are the live
  witnesses.
- **"No agent started" is the absence of the fake `pi` log.** On a live
  machine an agent started by a different path — or already running — is
  invisible to that assertion.
- **The fake `forge` answers by branch name alone.** Live, a branch the forge
  has stopped naming in `head.ref` (after a merge deletes it) is not the same
  seat; the live `allod change list` and `forge pr view` are the witness.
- **A seat check that passes silently is the failure that matters.** An
  unreadable credential or clone list is refused with exit 3 by design; a
  test that reads the run as a pass when `forge` was never consulted proves
  nothing.
- **An exit status read through a pipe is the last command's.** Run the
  suite's commands directly and read `$?` or `PIPESTATUS[0]` (`shell.md` in
  allod/memory); `verify-session ... | tee log` reports `tee`'s 0 on a
  refusal.
- **`--timeout` with an option-shaped value is not rejected up front.** It
  reaches `timeout` as the duration — `--` ends option parsing — and
  `timeout` rejects it as an invalid time interval (exit 125); the suite's
  `an option-shaped timeout is refused, not run` asserts that failure, not a
  pre-flight check.
