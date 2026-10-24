# allod/tools feature map

Behavior-level inventory of allod/tools, one file per area. A verifier uses
it to decide what to drive for a change and what evidence counts; a human
uses it as the regression checklist. The conventions are `testing.md`
(Feature Map) in allod/memory; this file carries what is specific to this
repository. The `verify-merge` skill reads this map and never edits it.

Everything here drives one of two things: a test script under `tests/`, or
the binary built from this checkout against a fixture. Nothing in the map
drives a protected branch of a real repository, and nothing writes to a real
repository at all; a forge write runs only against the scratch repository the
verifier account owns (`forge.md`).

## Driver

The commands a verifier runs, each one a real command in this repository.
`$HEAD` is the head commit pinned for the verification; every command runs in
a scratch clone checked out at that hash, never in a shared checkout.

| Step | Command | What it proves |
| --- | --- | --- |
| launch | `export ALLOD_UNDER_TEST="$(nix build .#allod --print-out-paths --no-link)/bin/allod"` and `export FORGE_UNDER_TEST="$(nix build .#forge --print-out-paths --no-link)/bin/forge"`, run in the clone | the Go binaries under test are built from this tree; the shell tools (`workspace/`, `flake/`, `git-hooks/`, `pr-explain/`, `verify-session/`) need no build, the suites run them from the tree |
| health check | `test -z "$(git status --porcelain)" && test "$(git rev-parse HEAD)" = "$HEAD" && case "$ALLOD_UNDER_TEST" in /nix/store/*) "$ALLOD_UNDER_TEST" change --help \| head -1 ;; *) echo "stale: ALLOD_UNDER_TEST unset, the suites would drive $(command -v allod)"; false ;; esac` | the tree is exactly the head commit and the variable names the build from it; `command -v allod` is the deployed generation, which a suite run without the variable drives silently. A stale build is reported, not driven |
| drive | `ALLOD_UNDER_TEST=... bash tests/<suite>.sh`, or `nix develop -c go test -count=1 -v ./cmd/forge -run '<Test>'`, or the fixture recipe a feature file gives, run against `$ALLOD_UNDER_TEST` | one entry point exercised end to end, with the expected line the feature file names |
| inspect | the suite's `ok N - <description>` lines and its closing `All N ... passed` line; for a fixture, `git -C <bare origin> log --format=%B <branch>`, the parent's `.git/allod-changes/<description>`, `"$ALLOD_UNDER_TEST" change list <repo>`, `git ls-remote` on the forge | the observable state after a path ran, read from the authoritative copy (the origin, the ledger, the forge), never from the clone a worker could have rewritten |
| clean up | every suite runs under a temporary `$HOME` it removes on exit; a fixture recipe ends with `rm -rf "$T"`; `"$ALLOD_UNDER_TEST" change list` in the real workspace shows no row from the run; `forge label list -R <scratch>` and `forge pr list -R <scratch> -s all` show only what the run meant to leave | nothing from this run leaks into the next |

`go test` caches a passing package; a cached rerun prints `(cached)` and proves
nothing about this run. `-count=1` forces execution.

## Baseline preconditions

- Checks that must pass before driving anything: `allod check` in the clone
  (the same eight derivations as `nix flake check`: the three packages,
  `go-checks` running `go test ./...` under the four tag combinations the
  repository README lists plus `gofmt` and `go vet`, `allod-parity`,
  `cascade-suites`, `flake-status-suite`, `git-hook-suites`). A failed check
  is a finding, never rerun to green.
- A dev VM with `nix`, `git`, `bash`, `bwrap` and `systemd-run --user`, and
  an invoking user that is not one of Nix's trusted users: `allod change run`
  refuses a trusted user with exit 12, so its suite cannot run as one.
- The verifier seat: `forge auth status` names the verifier account. The
  token file is mode 0600 and `FORGEJO_TOKEN` is unset in the shell, or every
  `forge` command exits 1 before making a request.
- A forge write needs a scratch repository the verifier account owns, named
  in the deployment's private memory as `<owner>/<repo>`. Without one, every
  mutating forge path is a gap with that as the blocker, and the recording
  server suite is the proof for those paths (`forge.md`).
- Manual or unreachable from a dev VM: `tests/site-preview-by-hand.sh` (a
  browser on the hypervisor), the `nixconfig` cascade suite (a pty), and the
  live `service.forge` profile in allod/profiles, which composes only through
  the service builder in allod/archetypes and is not reachable from this
  repository alone.

## Proof standard

For a change, the touched feature files define the coverage set. Each
entry point in them is driven through its success, cancel, error, empty and
persistence paths where the change can affect them, and every row of proof
names the expected observable result before the observed one. A path that
cannot be reached is reported as a gap with what blocks it, never as a pass.
A mock counts only behind the same boundary an unavailable external
dependency would use: the `cmd/forge` recording server stands exactly where
the forge's HTTP endpoint would, so it counts for the forge's answers; the
mock `git` in `tests/workspace/pull-all.sh` stands where git would, so it
counts for `pull-all`'s own state machine and not for git's behavior, which
the real-git fixture in `workspace.md` covers.

A suite stops at its first failure. One `not ok` line hides every case
after it, so a partial transcript is never a pass for the cases it did not
reach; the closing `All N ... passed` line is the pass.

## Sweep order

For a broad regression, walk the files top to bottom. An area without a
file yet is proved by the checks plus the PR's own Validation commands,
rerun by the verifier, until its file lands.

- [change](change.md): `allod change` begin, list, record, submit, cleanup
  and run; clone isolation, the ledger, the sandboxed record/submit flow,
  forge-derived cleanup and list.
- [forge](forge.md): `forge` auth and token, pull request, issue, label and
  milestone commands; `pr merge` with its read-back; the error contract.
- [workspace](workspace.md): `pull-all` and `work-diff` over `~/work`.
- [verify-session](verify-session.md): the seat refusals (author credential,
  live clone, unreadable credential or clone list), the scratch clone at
  `refs/pull/<n>/head`, the generated brief, the headless run and the
  proof-comment extraction; driver `tests/verify-session.sh`.
- patch: `allod patch` send and receive, the linear-history refusal, hash
  rewriting by `git am` (no file yet).
- git-hooks: `protected-refs-policy` cases from
  `tests/fixtures/protection-cases.tsv`, `setup-tracked-hooks` (no file yet).
- flake: `flake-status`, `fleet-diff`, `flake-update-cascade` (no file yet).
- secret: `allod secret` declare, create, rotate, rekey (no file yet).
- check: `allod check` (no file yet).
- site: `allod site` serve and view, and the tagged deploy, check and
  config (no file yet).
- memory-and-trace: `allod memory backpass`, `allod trace`, `allod skill`
  (no file yet).
- pr-explain: `allod pr explain` and `validate-report` (no file yet).

A recorded sweep lives beside the map under `../sweeps/`, one file per run,
commands plus observed output tied to the head commit it proved.

## Entry contract

Every feature file uses the same four headings, in this order:

1. `What exists` — the behaviors, one line each, with the variants that
   change them.
2. `How a user reaches it` — the entry points: command, endpoint, UI path,
   timer, with preconditions.
3. `How to drive it` — the driver invocation per entry point, and the
   expected observable result of each path.
4. `What usually lies` — the false passes: a cached result, a default that
   masks the error path, a log line that prints before the write commits.

Map drift is fixed in the same PR as the behavior change. A PR that removes
or narrows an entry here, or a check, is never merged below R2.
