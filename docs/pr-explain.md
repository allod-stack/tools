# PR explanation reports

`allod pr explain` turns one immutable pull-request snapshot into a validated,
self-contained HTML teaching report. Use it when a normal diff is not enough to
understand a change: choose the subscription provider explicitly, review the
identity and commit summary printed by the command, then inspect the report. The
command only reads the PR and repositories; it never edits the PR, commits,
pushes, or deploys anything.

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
allod pr explain <number> (--codex | --claude)
  [-R|--repo <owner/repo>] [--checkout <path>]
  --output <report.html> [--replace] [--dry-run]
  [--model <model>] [--effort <low|medium|high|xhigh|max>]
```

The pull-request number, exactly one provider, and `--output` are required,
including for a dry run.

| Option | Meaning |
|---|---|
| `--codex` | Consent to send the report inputs to the installed Codex subscription CLI through `codex exec`. |
| `--claude` | Consent to send the report inputs to the installed Claude subscription CLI through `claude -p`. |
| `-R`, `--repo` | Forge repository in `owner/repo` form. Without it, `forge` infers the repository from the checkout. |
| `--checkout` | Local checkout of the PR's base repository. Without it, the current checkout is used. |
| `--output` | Required destination for the completed HTML report. Its parent directory must already exist. |
| `--replace` | Permit an existing report to be replaced, after the new report passes validation. |
| `--dry-run` | Resolve and verify the snapshot and print the disclosure summary, but do not run a provider or write the output. |
| `--model` | Override the selected CLI's built-in model default for this run. |
| `--effort` | Override explanation effort. The default is `high`. |

Leaving out `--model` follows the selected CLI's built-in default, so the
command does not freeze a model name that will become stale. User and project
runner configuration is disabled for this isolated run; use `--model` for a
deliberate one-run override. The default `high` effort favors careful repository
investigation; lower it for a quick iteration or raise it only when the
installed CLI supports the requested level.

`allod pr explain` is a thin dispatcher: it resolves the `pr-explain/` tool
directory — from `ALLOD_TOOLS_DIR`, its own script directory, or a
`$WORK_DIR/allod/tools` checkout, in that order — and hands off to
`pr-explain/explain`, which embeds the shared prompt, report template,
progressive-enhancement script, and gallery from that same directory.
Generation does not depend on an `allod/tools` source checkout beyond that
resolution; `--checkout` always names the repository being explained.

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
be sent to that provider's installed subscription CLI. There is no API-key,
Pi, nullsink, or direct provider-API mode.

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
Content heuristics can be reported separately as advisory warnings; they are
prompts for human review, not substitutes for the mechanical safety checks.

On success, the private job directory and detached checkout are removed. On a
runner failure, missing output, or validation failure, the command prints and
preserves the `.allod-pr-explain.*` job directory beside the destination so you
can inspect the prompt, runner logs, snapshot, discussion, complete diff, and
rejected staging file alongside the validator diagnostics. That directory may
contain the PR's source and review context; treat it as private diagnostic
material and remove it when the investigation is complete.

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
