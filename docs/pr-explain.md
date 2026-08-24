# PR explanation reports

`allod pr explain` turns one immutable pull-request snapshot into a validated,
self-contained HTML teaching report. Use it when a normal diff is not enough to
understand a change: choose the provider explicitly, review the
identity and commit summary printed by the command, then inspect the report. A
triage pass first judges how much explanation the change deserves, so a trivial
change is declined rather than padded. The command only reads the PR and
repositories; it never edits the PR, commits, pushes, or deploys anything.

## Generate a report

From a checkout of the pull request's base repository:

```sh
allod pr explain 42 --codex --output ./pr-42-explanation.html
```

Or name both the Forge repository and a checkout explicitly:

```sh
allod pr explain 42 --claude \
  -R acme/widget \
  --checkout /path/to/widget \
  --output ./pr-42-explanation.html
```

The complete interface is:

```text
allod pr explain <number> (--codex | --claude | --pi)
  [-R|--repo <owner/repo>] [--checkout <path>]
  --output <report.html> [--replace] [--dry-run]
  [--model <model>] [--effort <low|medium|high|xhigh|max>]
  [--force-tier <T1|T2|T3>] [--no-slop] [--no-repair]
```

The pull-request number, exactly one provider, and `--output` are required,
including for a dry run.

| Option | Meaning |
|---|---|
| `--codex` | Consent to send the report inputs to the installed Codex subscription CLI through `codex exec`. |
| `--claude` | Consent to send the report inputs to the installed Claude subscription CLI through `claude -p`. |
| `--pi` | Consent to send the report inputs to the installed pi coding agent through `pi -p`. Pi meters API credits rather than a subscription and authenticates from its own credential store; like every runner, it receives an environment with all API keys and provider credentials stripped, and it runs with extensions, skills, prompt templates, project context files, and project-local trust disabled. |
| `-R`, `--repo` | Forge repository in `owner/repo` form. Without it, `forge` infers the repository from the checkout. |
| `--checkout` | Local checkout of the PR's base repository. Without it, the current checkout is used. |
| `--output` | Required destination for the completed HTML report. Its parent directory must already exist. |
| `--replace` | Permit an existing report to be replaced, after the new report passes validation. |
| `--dry-run` | Resolve and verify the snapshot and print the disclosure summary, but do not run a provider or write the output. |
| `--model` | Override the selected CLI's built-in model default for this run. |
| `--effort` | Override explanation effort. The default is `high`. |
| `--force-tier` | Generate at the named tier even when triage judged the change differently, including overriding a `T0` decline. |
| `--no-slop` | Skip the deletion-only tightening pass, saving one provider call. |
| `--no-repair` | Fail on the first validation failure instead of spending one repair pass, trimming the run's maximum provider calls by one. |

Leaving out `--model` follows the selected CLI's built-in default, so the
command does not freeze a model name that will become stale. User and project
runner configuration is disabled for this isolated run; use `--model` for a
deliberate one-run override. The default `high` effort favors careful repository
investigation; lower it for a quick iteration or raise it only when the
installed CLI supports the requested level.

## Triage and the pass pipeline

Generation is a pipeline of provider passes, all against the same consented
subscription CLI. The report is authored one piece per pass — the reader
contract is re-anchored at the top of every authoring prompt, so the last
section is written under the same instruction pressure as the first — and the
tool assembles the pieces:

1. **Triage** (`pr-explain/triage-prompt.md`) reads the job files and the
   checkout and writes `triage.json` into the private job directory: a tier, a
   decision-risk level, a reading budget, the concepts the change touches, the
   developer questions it raises, and the learning objectives a report must
   serve.
2. **T0 short-circuit** — when triage answers tier `T0` and no `--force-tier`
   was given, the command prints the tier judgment and its reason, generates
   nothing, and exits 0.
3. **Outline** (`pr-explain/outline-prompt.md`) does the deep investigation,
   writes `outline.json` — the section plan: id, title, layer, objectives,
   concepts, and a one-sentence gist per section, plus free-form evidence
   notes for later passes — and writes the report's front matter (masthead,
   operator summary, table of contents, objectives block) as a fragment. The
   tool validates the plan mechanically before any section pass runs: section
   count within the triage budget, unique non-reserved kebab-case ids, layer
   order never backward with at least one concept section, every triage
   objective claimed, and a table of contents that matches the plan exactly.
   An invalid plan fails the run; there is no outline repair.
4. **Section passes**, one provider call per planned section, strictly in
   document order (`pr-explain/section-prompt.md`). Each call receives the
   shared contract, the section's plan entry, and every previously accepted
   fragment quoted verbatim, and writes exactly one `<section>` fragment.
   The tool checks each fragment's opening tag against the plan before the
   next call runs.
5. **Quiz** (`pr-explain/quiz-prompt.md`) sees the whole assembled-so-far
   report and writes the final quiz section, the closing `main` tag, and the
   provenance footer.
6. **Assembly** — the tool, not a provider, concatenates the fragments into
   the staged report body.
7. **Slop** (`pr-explain/slop-prompt.md`) tightens the assembled body by
   deletion only: it may cut sentences, list items, and whole elements and
   tighten wording, but it cannot add content or change code, provenance,
   quiz correctness data, or objective ids. Skip it with `--no-slop`.
8. **Validation and one bounded repair pass**, unchanged: the same validator,
   with the same errors blocking publication.

Every authoring prompt opens with one shared block (`pr-explain/contract.md`):
the characterization anchor, the reader contract, evidence discipline, voice,
and the fragment grammar. Per-pass rules live only in their pass's prompt.

Tiers grade how much explanation the change deserves:

| Tier | Meaning |
|---|---|
| `T0` | The PR body's own summary suffices; the tool declines to generate. |
| `T1` | One-screen brief: one concept section, a minimal quiz, a full read of at most 4 minutes. |
| `T2` | Full explainer. |
| `T3` | Full explainer plus background sections for the prerequisite gaps triage found. |

`decision_risk` is independent of size: a 5-line auth change can be `T1` with
`high` risk, and `high` risk forbids a `T0` decline. `--force-tier <T1|T2|T3>`
overrides the triage tier, including a `T0` decline you disagree with.

### triage.json

The triage verdict is written to the private job directory (the path is handed
to the provider in `ALLOD_PR_EXPLAIN_TRIAGE`) and has exactly these keys:

```json
{
  "tier": "T0" | "T1" | "T2" | "T3",
  "tier_reason": "one plain sentence",
  "decision_risk": "low" | "medium" | "high",
  "budget": { "reading_minutes": <int 1..60>, "max_sections": <int 1..12> },
  "concepts": [ { "slug": "kebab-case", "name": "...", "status": "new"|"modified"|"background", "gap": true|false } ],
  "questions": [ "developer questions this change raises" ],
  "objectives": [ { "id": "obj-1", "verb": "predict"|"decide"|"diagnose"|"explain"|"trace", "statement": "After reading, the reader can ..." } ]
}
```

Objectives are verb-first capability claims tied to the merge decision — 3–7
for `T2` and `T3`, 1–3 for `T1`. The report's objectives block must carry the
same ids in the same order, and every objective must be claimed by at least one
section and tested by at least one quiz item; the vocabulary that enforces this
is described in [Report components](components.md).

`allod pr explain` is a thin dispatcher: it resolves the `pr-explain/` tool
directory — from `ALLOD_TOOLS_DIR`, its own script directory, or a
`$WORK_DIR/allod/tools` checkout, in that order — and hands off to
`pr-explain/explain`, which embeds the shared prompts, report template,
progressive-enhancement script, and gallery from that same directory.
Generation does not depend on an `allod/tools` source checkout beyond that
resolution; `--checkout` always names the repository being explained.

`forge` is a separately packaged Go binary and is resolved from `PATH`.
`ALLOD_PR_EXPLAIN_FORGE` overrides that resolution with an explicit executable
path for tests and local development; it must name an existing, executable
file or the command fails clearly rather than silently falling back.

### Standalone validator

`pr-explain/validate-report` is the same validator `allod pr explain` runs
before publishing, packaged as a standalone tool:

```sh
pr-explain/validate-report <report.html> <snapshot.json> <runner>
```

Use it to check a report file you already have — for example, one you are
hand-editing during template development — without regenerating it. It prints
one diagnostic per validation error or warning and exits non-zero on any hard
error. `allod pr _validate-report` (used by this repository's own tests)
delegates to the same script.

## What is disclosed

The provider flag is the consent boundary. Before running a provider, the
command prints the repository and PR number, immutable base and head commits,
selected runner, and destination. Choosing `--codex` or `--claude` explicitly
means the PR title, body, review context, complete diff, and relevant source can
be sent to that provider's installed subscription CLI, in at most 17 calls per
run — triage, outline, one per planned section up to the twelve-section budget
cap, quiz, slop, and repair — and the printed consent text states that
maximum. There is no API-key, Pi, nullsink, or direct provider-API mode.

The provider runs inside the development VM cage. Provider API credentials and
routing overrides, cloud-provider credentials and routing switches, and Forge
or Git-hosting tokens are removed from its environment by prefix as well as by
known name. Only the selected CLI's subscription state remains available for
authentication; the other provider's state is removed. Codex user config,
project instructions, and execution rules are disabled, and Claude runs in safe
mode so repository settings, hooks, plugins, skills, MCP servers, and
`CLAUDE.md` cannot reconfigure the job. If the selected login is missing or
expired, the run fails and the diagnostics are preserved; `allod` does not fall
back to another credential or provider.

## Snapshot and checkout safety

The command reads [`forge pr snapshot`](forge.md#pr-commands), the stable JSON
interface for PR identity and immutable commit metadata. It does not parse the
human-readable output of `forge pr view`.

The named checkout must identify the snapshot's base repository. The snapshot
describes base and head repositories independently, so a same-repository branch
and a contributor's fork are resolved through the same interface. For a fork,
the head commit is fetched from the head repository recorded in the snapshot;
you do not need a separate checkout of the fork.

Every ref the snapshot reports is normalized into an exact remote ref before
any git command runs. An ordinary branch name becomes `refs/heads/<name>`;
nothing else is accepted. An AGit-created pull request (for example, one
pushed with `git push -o agit`) has no pushed branch — Forgejo reports its
own pull namespace, `refs/pull/<n>/head`, as the head — and the allod
workflow does not accept AGit submissions, so the command refuses such a
pull request at snapshot validation, before any fetch or provider
disclosure, with a diagnosis that names AGit. (`forge pr snapshot` still
describes AGit-created pull requests faithfully; the refusal is this
command's policy, not the snapshot reader's.) Any other explicit `refs/*`
ref, and anything git's own ref-name rules would reject —
a leading `-`, whitespace or control bytes, `:`, `^`, `~`, `*`, path
traversal — is rejected before it ever reaches git.

Before disclosure, `allod` verifies that fetched object IDs equal the snapshot's
base and head SHAs and refuses a moved ref, a wrong checkout, or an empty diff.
The provider works in a detached job checkout at the verified head, not in your
working checkout. It therefore cannot switch your branch or turn generated
report content into a repository change. A dirty checkout is allowed because it
is used only to establish repository identity and discover its origin; verified
commits are fetched into the private job checkout, while uncommitted work remains
untouched and outside the report input.

## Output and failure behavior

The destination is deliberately explicit. If it already exists, the command
refuses before invoking the provider unless `--replace` is present. The provider
writes only a staging report inside a private, mode-`0700` job directory created
under a `077` umask. `allod` validates that file and then moves it atomically to
the destination with mode `0600`. A failed regeneration leaves the previous
report byte-for-byte untouched.

Hard validation errors block the output. They include active or
network-capable markup, external resources, modified template CSS or JavaScript,
secret-looking content, provenance that does not match the snapshot, malformed
or dangling anchors, an incomplete document shell, and broken accessibility,
mobile-flow, reduced-motion, quiz, diagram, table, or code-whitespace contracts.
The v2 shape adds its own hard errors: a missing reading-cost line, a missing or
out-of-order layer or objectives block, an objective no section or quiz item
claims, an out-of-range quiz count or unbalanced answer letters, banned AI-slop
vocabulary, and a sentence-opening pattern repeated four or more times. Content
heuristics — hedge density among them — can be reported separately as advisory
warnings; they are prompts for human review, not substitutes for the mechanical
safety checks.

The secret-looking-content check matches a closed set of well-known credential
shapes (cloud and forge tokens, private-key headers, JWTs, long base64 blobs)
plus a generic key-name-next-to-a-value heuristic. It is defense-in-depth, not
a secret scanner: it will not catch every credential shape, and it deliberately
leaves plain prose that merely *names* a field (`password:` followed by an
ordinary sentence, not a pasted value) alone rather than risk failing a
faithful code walk. Treat a pass as "nothing obviously credential-shaped
leaked," not as a guarantee the report is secret-free.

### The bounded repair pass

A report is assembled and validated before it can be published. When that
validation fails, `allod` prints every diagnostic, prints `validation failed;
requesting one repair pass`, and hands the diagnostics back to the *same*
provider for exactly one more call. The repair prompt carries only the
canonical body path, the validator's diagnostics as quoted data, and the
immutable authoring rules; it asks for the minimum structural correction that
clears each diagnostic while preserving the report's meaning. The repair pass
runs with the same hardened arguments and sanitized environment as the first
call — no credentials are resent, and the provider cannot be switched — and
every cage postcondition is re-checked afterward: the staged body is captured
safely, the immutable snapshot must be unchanged, and the detached job checkout
must still be clean at the snapshotted head.

The loop is bounded, not adaptive. With `N` planned sections (1 to 12, capped
by the triage `max_sections` budget), one run makes **at most `N + 5`
provider calls** — 17 at the twelve-section worst case, and the consent text
states that ceiling before triage runs:

| Run | Provider calls |
|---|---|
| `--dry-run` | 0 |
| Triage answers `T0`, no `--force-tier` | 1 |
| Outline fails the schema gate | 2, then the run fails |
| Body validates | N + 4 (N + 3 with `--no-slop`) |
| Body fails, repair validates | N + 5 (N + 4 with `--no-slop`) |
| Body fails, repaired body fails | N + 5, then the run fails |

A fragment pass that breaks its mechanical contract — a section that does not
match its plan entry, a missing section plan, a fragment carrying another
pass's markup — fails the run at that pass, before the next provider call
spends anything continuing from it.

A second validation failure is final. Both passes' diagnostics, prompts, runner
logs, and captured bodies stay in the preserved job directory, and any previous
report is left byte-for-byte untouched. Since the provider flag already consents
to this PR and this provider, the repair pass discloses nothing new — but it does
cost one more call against your subscription. Pass `--no-repair` when you want
strict cost control; the run then fails on the first validation failure
and still preserves the diagnostics.

Repair does not weaken any contract. The repaired report is assembled and
validated by exactly the same validator, with the same errors blocking
publication. The repair pass only removes the need for one-shot perfection from
a long component gallery on an on-demand run.

On success, the private job directory and detached checkout are removed. On a
runner failure, missing output, or validation failure, the command prints and
preserves the `.allod-pr-explain.*` job directory beside the destination so you
can inspect the prompts, runner logs, snapshot, discussion, complete diff,
triage verdict, and rejected staging file alongside the validator diagnostics.
Each pass keeps its own artifacts there: the triage verdict in `triage.json`;
the section plan in `outline.json` and the per-pass prompts
(`outline-prompt.md`, one `section-prompt.NN-<id>.md` per section,
`quiz-prompt.md`); the captured fragments under `fragments/`; per-pass runner
logs (`runner.outline.*`, `runner.section-NN.*`, `runner.quiz.*`); the
assembled body in `report-body.captured.html`; the tightened body captured
after the slop pass; and `repair-prompt.md`,
`runner.repair.stdout`/`runner.repair.stderr`, and
`report-body.repair.captured.html` for the repair pass, with the
diagnostics that triggered each in `validation.diagnostics.txt` and
`validation.repair.diagnostics.txt`. That directory may contain the PR's
source and review context; treat it as private diagnostic material and remove
it when the investigation is complete.

`--dry-run` performs no provider run and no report write. It is the quickest way
to check repository inference, fork resolution, immutable commits, runner
selection, and overwrite policy before any source is disclosed:

```sh
allod pr explain 42 --codex \
  -R acme/widget \
  --output ./pr-42-explanation.html \
  --dry-run
```

## Read the report in both modes

Reports use semantic HTML first. Forge renders committed HTML in a sandboxed
iframe that does not execute JavaScript, so all prose, figures, sequence steps,
code walks, comparison tables, quiz choices, feedback, and provenance remain
complete without it. When the same file is opened locally, the bundled script
may add user-controlled sequence, quiz, and highlighting conveniences. It never
autoplays motion, and reduced-motion preferences keep the complete static view.

The shared component vocabulary is documented in [Report components](components.md).
To inspect every component and its narrow, dark, reduced-motion, print, and
no-JavaScript fallbacks, open
[`pr-explain/component-gallery.html`](../pr-explain/component-gallery.html) in a
local browser. In particular, resize the flow examples and confirm that nodes
own their connector pseudo-elements: wide layouts use an authored horizontal
glyph, narrow layouts use a vertical glyph, and no connector is a rotated
sibling box.

The automated checks enforce those structural geometry contracts, but the test
environment has no browser-based pixel comparison. The gallery is the manual
visual regression surface; responsive source invariants narrow, but do not
eliminate, what a human rendering check can catch.

## Critique and regenerate

A report is an explanation to review, not an autonomous approval decision.
After the first run:

1. Check the routing summary first: can a reader decide whether to stop, skim,
   or read fully without being misled?
2. Check facts against the diff and surrounding code. Flag interpretations that
   are presented as facts, missing review context, unsupported intent, and
   residual limits that the report fails to name.
3. Check information transfer: useful chunking and signaling, concrete examples
   before abstractions, diagrams that reduce cognitive work, and quiz distractors
   based on real misconceptions rather than trivia.
4. Improve the common prompt or component vocabulary through its own reviewed
   code change when the critique is reusable. The report file is not the place
   to fork the CSS or JavaScript.
5. Regenerate deliberately with the same command plus `--replace`, then compare
   the new report with the critique:

   ```sh
   allod pr explain 42 --codex \
     --output ./pr-42-explanation.html \
     --replace
   ```

`--replace` authorizes replacement of that local artifact only. It does not
commit or push the report, update the source branch, post a comment, or edit the
pull request. Publication remains a separate, explicit human workflow.
