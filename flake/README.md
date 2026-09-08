# Flake Tools

Nix flake pin inspection, update automation, and fleet impact checking.

## `flake-status`

Shows which flake inputs each repo pins and at what revision.

```
flake-status                           # all inputs, all repos
flake-status <input-name>              # one input across all repos
flake-status <input-name> --upstream   # compare pins to upstream HEAD
```

**No args** -- full table per repo:
```
==> allod/nexus
  allod-tools        97b57e1  2026-06-03
  home-manager          3ee51fb  2026-05-23
  nixpkgs               b77b3de  2026-05-22
  ...
```

**Named input** -- consistency check across all repos:
```
$ flake-status allod-tools
allod-tools — all repos consistent at 97b57e1 (2026-06-03)

  allod/vm              (not an input)
  allod/nexus           97b57e1  2026-06-03
  allod/profiles        97b57e1  2026-06-03
```

If repos are out of sync, the header says `INCONSISTENT` and the stale rows are
marked `<- stale`.

**`--upstream`** makes a network call to compare local pins against the input's
remote HEAD. In all-inputs mode, stale pins are marked with `-> <rev>` and the
output suggests `flake-update-cascade` commands for outdated inputs.

---

## `flake-update-cascade`

Updates one or more named flake inputs across all repos that pin them directly,
running pre-flight checks before touching anything.

```
flake-update-cascade <input-name>... [--pr] [--dry-run]
```

The requested names are resolved through each repository's lock graph. All
reachable direct pins with those names are passed to one `nix flake update`
invocation, producing at most one commit and one PR per repository.

**Modes:**

| Flag | Behaviour |
|---|---|
| *(none)* | Commit directly to the default branch. Skips repos listed in `~/.config/git/protected-branches`. |
| `--pr` | Create/update a PR branch (`agent/flake-update-<input>`) for each repo. Works on protected repos. |
| `--dry-run` | Show what would change without modifying anything. |

**Pre-flight checks** (runs on all repos before any changes):
- Not on default branch -> error
- Dirty working tree -> error
- Unpushed commits -> error
- No `flake.lock` -> skip with notice
- Input not present / is a `follows` -> skip with notice
- Listed in `~/.config/git/active-pr-branches` -> skip with notice (GPG-signed commits required)
- `origin` outside the forge and not matched by `~/.config/git/allowed-external-remotes` -> skip with notice, in every mode; the repo is never pulled, updated, committed to or pushed
- No `origin` remote -> skip with notice

If any repo fails pre-flight, the cascade aborts before touching anything.

**`--pr` mode details:**

Each eligible repo gets a branch named `agent/flake-update-<input>`. On
re-runs, the branch is force-updated and the existing PR is noted rather than a
new one being created. Requires `forge` on PATH.

**Implementation.** The command is the Go program in `cmd/flake-update-cascade`.
The Bash program at `flake/flake-update-cascade` is its oracle until the
retirement step of allod/tools#159: every suite under `tests/flake` runs against
whichever `CASCADE_UNDER_TEST` names, the Bash program by default, and with
`CASCADE_PARITY=1` runs both on one fixture and requires identical output, exit
status, subprocess trace, and tree. `nix flake check` runs the mock-driven suites
all three ways; `tests/flake/flake-update-cascade-nixconfig.sh` drives the real
`nix` under a pty and is run by hand:

```bash
CASCADE_UNDER_TEST="$(nix build .#flake-update-cascade --print-out-paths)/bin/flake-update-cascade" \
  CASCADE_PARITY=1 bash tests/flake/flake-update-cascade-nixconfig.sh
```

Repositories are processed in directory order; dependency ordering and pin
propagation are allod/tools#171.

**Examples:**

```bash
# See what a nixpkgs update would do, without changing anything
flake-update-cascade nixpkgs --dry-run

# Update nixpkgs across all repos, committing directly (non-protected only)
flake-update-cascade nixpkgs

# Update multiple inputs together in each repository
flake-update-cascade nixpkgs home-manager

# Update allod-tools across all repos via PRs (works on protected branches)
flake-update-cascade allod-tools --pr
```

---

## `fleet-diff`

Answers whether an unmerged change alters any machine you actually run, and
fails when the answer differs from what the change claimed it would be.

```
fleet-diff [<deploy-checkout>] --override <input>=<rev> [...]
           [--expect <machine>,... | --expect-none]
```

For every machine in a composition-root flake it evaluates
`config.system.build.toplevel.drvPath` twice — once against the committed lock,
once with the given revisions substituted — and reports `unchanged` or
`CHANGES`. The checkout defaults to the current directory.

Each machine is evaluated on its own, so a run costs one machine's evaluation at
a time rather than the whole fleet's at once.

**Expectation.** The point of the tool is the assertion, not the report.
`--expect` names the machines the change is supposed to alter and `--expect-none`
says it should alter none — the land-inert case, and the one most runs use. A
mismatch fails in **both** directions, and the two sets are named separately:

```
$ fleet-diff ~/work/allod/deploy --override archetypes/vm=1a2b3c4 --expect allod-canary
fleet-diff: 3 machines in /home/allod/work/allod/deploy
  override:     archetypes/vm=1a2b3c4 → git+https://forge.anarch.diy/allod/vm.git?rev=1a2b3c4d…  (refs/heads/agent/guest-split)
  expectation:  allod-canary

  allod-canary           unchanged
  allod-dev              CHANGES
  allod-work             unchanged

1 of 3 machines change.
Scope: the fleet this checkout composes — another composition root has its own.

Expectation mismatch.
  changed, not expected:     allod-dev
  expected, did not change:  allod-canary
```

Giving neither expectation flag is report-only: the per-machine result prints,
nothing is asserted, and the exit status is 0 even when machines change. That
keeps the tool usable for exploration; a gate always names an expectation.

**Exit status** — distinct so it composes as a preflight inside another command:

| Code | Meaning |
|---|---|
| 0 | computed set matches the expectation, or none was declared |
| 1 | usage or precondition error |
| 2 | expectation mismatch |
| 3 | evaluation failed |

**Overrides.** `--override` is repeatable and takes one `input=rev` per
occurrence, split at the first `=`. Transitive inputs override by path
(`archetypes/vm`), direct ones by name.

A revision is all there is to type, because the rest is already written down:
the checkout's own `flake.lock` records which repository each input came from.
The line the tool prints shows what your revision became, so the pinned commit
is on the receipt rather than taken on trust.

Abbreviate to at least 7 characters — git's own floor for `core.abbrev=auto`,
which grows from there with a repository's object count. The floor is a sanity
check, not the guarantee: an abbreviation is expanded against the remote's refs,
and one naming no commit there, or more than one, is refused. So a collision
fails loudly instead of pinning the wrong commit.

Expanding is the only reason the remote is consulted, so it applies only to an
abbreviation, which has to be pushed and be the tip of a branch, tag, or pull
request for the remote to list it. A commit deeper in history takes its whole
40-character revision, which needs no expanding and is used as given.

Only the revision is pinned, never a ref. Nix reaches a commit without being
told which branch carries it — checked against a cold fetcher cache, on a commit
that is not on the default branch — so naming a ref would add a second thing
that has to stay true for no gain.

A source that `flake.lock` cannot turn back into a git remote takes a whole flake URL
in the same place:

```bash
fleet-diff --override 'vm=git+https://forge.example/vm.git?rev=<40-char-rev>'
```

Quote that form — an unquoted `&` backgrounds the command.

An override path that names no input in `flake.lock` is refused, and every path
is checked before any revision is resolved, so a typo costs no network round
trip. Nix itself answers a mistyped path with a warning and evaluates the
baseline anyway, exit 0, so an unchecked typo would report every machine
unchanged and pass `--expect-none` while proving nothing.

**Reading the output.** The verdict goes to stdout and Nix's own diagnostics to
stderr, so `fleet-diff … 2>/dev/null` gives a bare report. Keeping stderr is
usually worth it: the "not writing modified lock file" block names each
overridden input and its old and new revision, which is the receipt that the
override took effect, and a cold run's fetch progress appears there too.

**Preconditions.** The checkout needs both `flake.nix` and a committed
`flake.lock` — without a lock there is no baseline. An uncommitted `flake.nix`
or `flake.lock` warns, because the baseline is then the working tree rather than
the committed lock. Nothing is written: both evaluations pass
`--no-write-lock-file`.

**Examples:**

```bash
# Report which machines a branch would alter
fleet-diff ~/work/allod/deploy --override archetypes/vm=1a2b3c4

# Gate a land-inert merge: fail if any machine changes
fleet-diff --override inventory=1a2b3c4 --expect-none

# Gate an activation change: fail unless exactly these machines change
fleet-diff --override inventory=1a2b3c4 --expect allod-dev,allod-canary
```

A run over `allod/deploy` covers the public example fleet only. The
authoritative run needs the private fleet, so a green public result is not
fleet-wide proof.
