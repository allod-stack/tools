# verify-session

`verify-session` runs the cold verifier seat for one pull request as a single
enforced command: it refuses the pull request author's credential and any
clone this environment already holds on the branch, cuts a scratch clone at
`refs/pull/<n>/head` by hash, writes the brief, and runs the verifier headless
with its output captured.

The seat rules are the script's, not the operator's. A session that wrote the
pull request cannot verify it, and a session holding a live clone of its
branch cannot either; both checks fail closed, so an unreadable credential or
clone list stops the run before any verifier starts. The script names no
credential path of its own — pass `--token-file`, or let it default to
`FORGE_TOKEN_FILE`, which a deployment's private memory points at the verifier
token.

```console
$ verify-session --token-file "$VERIFIER_TOKEN" allod/tools#305
```

The scratch clone is fresh, of the forge remote's `refs/pull/<n>/head`, never
the author's checkout under the work tree or `~/changes`. The generated brief
names `verify-merge`, the pull request, the ledger-issue lookup, and the
no-feature-map fallback — the repository's checks plus a rerun at the head of
the body's `## Validation` commands — and tells the verifier to score the diff
itself before reading the body's score and to stop rather than merge past a
stop. The verifier's captured stdout is the script's output, followed by the
proof comment link it printed as `PROOF_COMMENT=<url>`; the scratch tree and
transcript path are reported on stderr.

The invocation is `tests/verify-session.sh`, a hermetic fake-forge, fake-git,
fake-allod and fake-pi harness that proves the refusal paths, the token
override, the scratch checkout, and the brief's contents.
