# allod memory

A memory backpass reads recent agent session traces against the public and
private memory indexes, keeps the sightings that survive a quote check, and
opens one pull request of evidence-backed memory edits for a human to
review. `allod memory backpass` is that pass as code: a nightly timer can
run it and spend a model only on the two calls the pass actually needs — one
bulk-analysis call per trace and one synthesis call — instead of asking a
headless session to follow `skills/memory-backpass/SKILL.md`'s ten steps by
hand with a shell. The skill text stays the spec; this command cites its
step numbers and implements them.

## Command

```
allod memory backpass [--traces <dir>...] [--public <dir>] [--private <dir>]
    [--bulk-seat <cmd>] [--fallback-seat <cmd>] [--synthesis-seat <cmd>]
    [--timeout <seconds>] [--jobs <n>] [--report-only]
```

A bare `allod memory backpass`, no options at all, works on any machine
whose checkouts follow the registry convention: all three directories
resolve through the same ordered steps — the option, then an
`ALLOD_BACKPASS_*` environment variable, then a registry marker (or the
existing single-private-entry rule), then a fixed-name convention path —
and the run report's `## Inputs` section names what each one resolved to
and which step found it (`option`, `env`, `registry <id>`, or
`convention`).

- `--traces <dir>` — a git checkout of distilled traces laid out
  `<machine>/<harness>/<YYYY-MM-DD>-<session-id>.md`, the shape `allod
  trace distill` writes. Space-separated and repeatable, the same as
  `allod change record --files` (`cli-design.md`): a following option ends
  the list, and `--` ends option parsing so a value starting with `-`
  stays reachable. Resolution order: `--traces`; else
  `ALLOD_BACKPASS_TRACES`, a colon-separated path list; else every
  repository-registry entry marked `"traces": true` whose checkout exists
  under `workDir`, in sorted id order — the way an inventory declares a
  traces checkout without a flag; else `<workDir>/traces-<hostname>` if it
  exists; else `<workDir>/traces` if it exists. Finding nothing by any of
  these is a refusal listing every path and marker tried. A directory that
  does not exist or is not a git repository is a refusal before anything
  else runs.
- `--public <dir>` — resolution order: `--public`; else
  `ALLOD_BACKPASS_PUBLIC`; else `<workDir>/<registry checkout of
  allod/memory>`, falling back to the literal checkout name `allod/memory`
  when the registry has no entry for it. This step always finds something.
- `--private <dir>` — resolution order: `--private`; else
  `ALLOD_BACKPASS_PRIVATE`; else the one repository-registry entry marked
  `"memory": true` other than `allod/memory` (zero or more than one such
  entry is a refusal naming what was found; pass `--private` explicitly);
  else `<workDir>/agent-memory` if it exists. Finding nothing by any of
  these is a refusal listing what was tried.
  Both `--public` and `--private` must be git repositories.
- `--bulk-seat <cmd>` (env `ALLOD_BACKPASS_BULK_SEAT`) and `--synthesis-seat
  <cmd>` (env `ALLOD_BACKPASS_SYNTHESIS_SEAT`) — when neither the option nor
  the environment variable is set, each defaults to the built-in `pi` seat
  (below).
- `--fallback-seat <cmd>` (env `ALLOD_BACKPASS_FALLBACK_SEAT`) — optional,
  with no default of its own; without it the fallback attempt is skipped
  and the report says "no fallback seat". Pass `pi` to use the built-in
  there too.
- `--timeout <seconds>` (default 600) — bounds every seat call.
- `--jobs <n>` (default 1) — analysis calls in flight at once. The report's
  ordering never depends on completion order: traces are always processed
  and reported in sorted `TRACE_PATH` order.
- `--report-only` — runs steps 1 to 6 and 10 only: prints what the pass
  would do and writes nothing to either memory repository. It still commits
  `analyzed.txt` in each traces checkout that changed.

An option given on the command line always wins over its environment
variable.

## Seats

Model choice is the deployment's own: `allod/memory`'s `agent-roster` skill
names which model fills the bulk, fallback, and synthesis seats for this
pass, and this command only ever runs the seat it was given — it never
substitutes another model silently.

A seat value is a command and its arguments, split on whitespace, with no
shell involved: anything fancier (a `script -qec` wrapper, an `@path`
argument, redirecting stdin from `/dev/null` some other way) is the
deployment's own wrapper script, named as the seat. The command actually run
is `<argv...> <prompt-file>`: the absolute path of the filled prompt file is
appended as the seat's last argument. Stdin is `/dev/null`, stdout is read
back as the model's answer, and stderr is forwarded to this command's own
stderr, one line at a time, each prefixed `seat <name>: ` where `<name>` is
the seat value exactly as given.

### The built-in `pi` seat

A seat value of exactly `pi` — the default for the bulk and synthesis seats
when neither the option nor its environment variable is set, or an explicit
choice for any seat including the fallback — runs `pi` itself, with its own
configured default provider and model, instead of a deployment's wrapper
script. It is implemented as `script -qec "pi -p '@<prompt file>'"
/dev/null`: `pi` prints nothing without a terminal, so `script` gives it
one, and it takes the prompt as an `@path` argument (both measured on this
VM, not documented `pi` behavior). A prompt file path containing a single
quote is refused before any call, since it sits inside that inner shell's
single quotes — scratch directories never produce one. `pi -p` runs with
its tools enabled by default, which the synthesis call into a worktree
needs; this command never passes `--no-tools`.

The run report names the built-in `pi (<defaultProvider>/<defaultModel>)`,
the two values read from `$HOME/.pi/agent/settings.json` (keys
`defaultProvider`, `defaultModel`); when the file or either key is missing,
it is named `pi (configured default)` instead. Selecting the built-in for
any seat is a refusal up front, before any call, when `pi` or `script` is
not on `PATH` — the message names which.

The seat's exit status is never the gate; only its stdout content is. A
call under `--timeout` runs in its own process group, and on expiry the
whole group is killed with `SIGKILL` sent to the negative of its process
group id — never `pkill`, whose pattern can match the string on the calling
shell's own command line and kill the run itself (`shell.md`). A timed-out
call is a failed attempt, exactly like an unparseable answer.

## Prompts

The two prompt templates are read at run time from `<public
checkout>/skills/memory-backpass/analysis-prompt.md` and
`synthesis-prompt.md`, never embedded in the binary: the skill stays the
spec. Both templates' first line must be exactly `<!-- memory-backpass:self
-->` — the marker `allod trace distill` looks for to drop a session that is
one of this command's own model calls, so the pass never analyzes its own
output. A template naming a `{{NAME}}` placeholder this command's filler
does not know is a refusal, before any model call. Filling itself is a
single left-to-right pass: a value substituted for a placeholder is copied
into the output and never re-scanned, so a trace whose text happens to
contain `{{TRACE}}` or `&` reaches the model byte for byte.

## Step map

`skills/memory-backpass/SKILL.md`'s step numbers, as this command implements
them:

| Step | What the command does |
|---|---|
| 1 | Number every `memory.md` bullet `PUB-nnn`/`PRV-nnn`; id every open `ledger.md` line `L-<first 8 hex of its SHA-256>`; list merged `memory-backpass:` PRs whose "To relay" checklist is still unchecked (the relay check; skipped with one line when `forge` is not on `PATH` in `--report-only`, a refusal otherwise). |
| 2 | Walk each `--traces` checkout for pending traces (below); fill `analysis-prompt.md`; call the bulk seat, retry once, then the fallback seat once if given; gate on parsed `positive`/`negative`/`gaps` JSON arrays, never on exit status. |
| 3 | Whitespace-fold every returned quote and the trace; keep an item only when its folded quote is a non-empty substring of the folded trace. |
| 4 | One private `ledger.md` line per surviving rule and session: `gap`, `ignored` (non-compliance), or `harm`; corroborate an existing line instead of duplicating it; never ledger an `irrelevant` item. |
| 5 | Fold the evidence: per-instruction counts and relevance, per-ledger-rule session counts, and every surviving quote grouped by rule, for the synthesis prompt. |
| 6 | Prune pre-run private `ledger.md` lines that are over 90 days old (every cited session's trace found and that old) or a `gap` line already covered by a topic file. A public line that qualifies is never edited — it is only listed under "To relay" as `prune: <line>` for a human to apply. |
| 7 | (write mode) Open a private worktree, write the ledger changes, run the synthesis seat there, and apply any relayed public edits as plain commits in a second, unpushed worktree. |
| 8 | Run `.hooks/memory-budget --worktree` in the private worktree; on failure, one fix call to the synthesis seat, then one more check; still over cap drops every edit this run proposed, caps never raised. |
| 9 | `allod change record` then `allod change submit` from the private worktree; label the PR `relay` when anything was proposed for relay. |
| 10 | (report-only) Everything above except 7 to 9: print the would-be ledger changes and prunes, write nothing to either memory checkout. |

## State file

`<traces>/.memory-backpass/analyzed.txt`, one line per trace already
analyzed: `<sha256 hex of the file>  <path relative to the checkout>`, two
spaces, the format `sha256sum -c --quiet analyzed.txt` checks directly in
that checkout. A trace is pending when it has no line here or its line's
hash no longer matches — including a trace that kept growing after an
earlier read, which is exactly the case this format exists to catch, since
a path-only state file cannot tell "already read" from "grew since".

In both modes, every `--traces` checkout whose pending set changed this run
gets its `analyzed.txt` rewritten (a line is updated for a just-analyzed
trace, dropped for a trace file that no longer exists, and otherwise kept)
and committed with message `memory-backpass: <n> traces analyzed`. An
unchanged file is not committed, and this command never pushes that commit
— the hourly traces timer does.

## What each mode writes

`--report-only` writes nothing to either memory checkout: the ledger
changes, prunes, and public-edit proposals are printed, not applied. It
still writes and commits `analyzed.txt`, so a second report-only run against
the same traces analyzes nothing new.

Write mode creates a private worktree (`allod change begin -d
memory-backpass-<date>`) and writes the ledger changes and the synthesis
seat's edits there; if the synthesis seat proposes anything to relay, a
second worktree off the public checkout gets one commit per proposal that
applies cleanly, left unpushed, with the handover line `allod patch receive
<hostname>:<worktree path> allod/memory` in the PR body. The private
worktree's branch is pushed by `allod change submit`, the same way any other
PR is submitted; nothing is ever pushed to a default branch, and the
unpushed public worktree is the one branch this command deliberately leaves
for a human (or `allod patch receive`) to move.

## Exit status

- `0` — every step completed and every trace analyzed.
- `1` — a refusal or a fatal setup error, before or during the run, or
  `allod change record`/`submit` failing from the private worktree. In the
  latter case `analyzed.txt` is written and committed nowhere: every trace
  this run analyzed stays pending, since the next run's fresh worktree
  carries the same ledger lines.
- `3` — the run completed, but at least one trace failed analysis (bulk
  seat, retry, and fallback all failed or timed out). The rest of the run
  still happens; a failed trace stays pending for the next run.

The run report is printed to stdout in every mode and every exit status.

## Report

Plain text, `## ` headings, no colour, in this order: inputs (each resolved
directory and how it was found); unrelayed public edits; seats (which seat
analyzed which trace); the funnel (traces
analyzed, items returned, dropped for a missing quote, malformed, ledger
entries corroborated, ledger lines added, prunes, edits proposed); failures,
when any; then the mode-specific sections — in `--report-only`, the fold
(the same text the synthesis prompt would have received) followed by
would-add/would-corroborate/would-prune; in write mode, the PR URL, any
claimed-but-absent or edited-but-unclaimed paths, and the relay worktree's
outcome.

The funnel is the alarm this command is built around: a drop rate for
missing quotes far above a few percent means the input (a trace, a prompt
fill) is corrupted, not that the model is having a bad day.

## Pointing a deployment at its models

`agent-roster` (in the deployment's private memory, or wherever a fork
keeps it) owns which model fills which seat. Leaving a seat unset uses the
built-in `pi` seat above, which is enough when `pi`'s own configured
default provider and model is the roster's choice. Otherwise a deployment's
timer module passes `--bulk-seat`, `--fallback-seat`, and `--synthesis-seat`
as one wrapper script per model — the thing that knows how to invoke that
provider's CLI non-interactively, with its input from `/dev/null` and its
answer on stdout — never a bare model name: this command treats a seat as
an opaque command, and the wrapper is where a deployment's own invocation
details (a `script -qec` dance, an `@path` argument, a particular model
flag) live.
