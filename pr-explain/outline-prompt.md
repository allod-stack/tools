## Your pass: outline the report and write its front matter

You are the first authoring pass. Later passes write one body section each and then the quiz, seeing only this pass's plan and the fragments before their own, so this pass carries the whole-report thinking: what the sections are, what each one teaches, and in what order. Do the deep investigation now — this is the pass that reads the complete diff, the surrounding code, and the review discussion end to end — and leave what you learned where later passes can use it.

This pass writes exactly two files:

1. the section plan, as JSON, to the exact path in `ALLOD_PR_EXPLAIN_OUTLINE`;
2. the report's front matter, as an HTML fragment, to the exact path in `ALLOD_PR_EXPLAIN_REPORT_BODY`.

## Honor the triage

Triage already decided how much this change deserves. Do not relitigate it.

- Plan at the triage `tier`. `T1` is a one-screen brief: one concept section. `T2` is a full explainer. `T3` adds a background section for each concept marked `"gap": true`, before the sections that depend on it.
- Stay within `budget`: at most `max_sections` planned sections, and a full read that fits `reading_minutes` for the reader described in the contract.
- Every triage objective must be claimed by at least one planned section, and every objective and concept a section claims must exist in `triage.json`.
- Plan where the triage `questions` get answered: each belongs to the section whose narrative reaches it, recorded in that section's `gist` or your notes.

## The section plan

Write `outline.json` with exactly this shape:

```json
{
  "sections": [
    {
      "id": "kebab-case-id",
      "title": "The heading, obeying the contract's heading rule",
      "layer": "concept",
      "objectives": ["obj-1"],
      "concepts": ["concept-slug"],
      "gist": "One sentence saying what this section teaches and with what evidence."
    }
  ],
  "notes": "Optional free-form evidence notes for later passes."
}
```

Rules the tool enforces mechanically before any section pass runs:

- 1 to `max_sections` sections, in reading order.
- Every `id` is unique kebab-case, and none is a reserved shell id: not `objectives`, not `quiz`, and never starting with `rx-`.
- `layer` is `concept`, `mechanism`, or `receipts`; the sequence never goes backward in that order, and at least one section is `concept`.
- Each section's `objectives` list one or more triage objective ids it serves; jointly the sections claim every triage objective.
- Each section's `concepts` list the triage concept slugs it teaches; a section may list none.

Use `notes` for what you learned that the plan cannot carry: file paths worth reading per section, representative values you traced, review corrections that changed the design, claims you verified and how. Later passes read it; the reader never sees it.

## The front matter fragment

The front-matter fragment runs from the skip link through the objectives block, filling every placeholder from `report-contract.json`, `triage.json`, and the evidence:

```html
<a class="rx-skip" href="#rx-main">Skip to content</a>
<header class="rx-masthead">
  <p class="rx-eyebrow">owner/repo · pull request #N</p>
  <h1>One claim-bearing sentence</h1>
  <p class="rx-lede">Two or three sentences: what changed, why it matters, and what accepting it costs.</p>
  <p class="rx-cost">Summary: 1 minute. Concepts: 4 minutes. Full mechanism: 12 minutes.</p>
</header>

<section class="rx-summary" aria-labelledby="rx-summary-h">
  <h2 id="rx-summary-h">If you read nothing else</h2>
  <p class="rx-decision">One sentence defining exactly what approval accepts and does not accept.</p>
  <ul class="rx-summary-cards">
    <li class="rx-card" data-q="what-changes-now"><h3>What merging changes now</h3><p>...</p></li>
    <li class="rx-card" data-q="what-exists-after"><h3>What exists afterward</h3><p>...</p></li>
    <li class="rx-card" data-q="evidence"><h3>What the tests actually prove</h3><p>...</p></li>
    <li class="rx-card" data-q="residual-risk"><h3>What is still unproven</h3><p>...</p></li>
    <li class="rx-card" data-q="how-to-reject"><h3>How to reject or roll back</h3><p>...</p></li>
  </ul>
</section>

<nav class="rx-toc" aria-label="Contents"><ol>...same-document links...</ol></nav>
<main id="rx-main">
  <section id="objectives" aria-labelledby="objectives-h" data-layer="concept">
    <h2 id="objectives-h">What you can do after reading</h2>
    <p class="rx-claim">...</p>
    <ol class="rx-objectives">
      <li id="obj-1">After reading you can predict ...</li>
      ...one item per triage objective, same ids, same order, varied openings...
    </ol>
  </section>
</main>
```

Write everything in that skeleton except the closing `</main>`, which the quiz pass owns: your fragment's last line is the objectives section's `</section>`. The operator summary has exactly the five shown `data-q` cards; keep it plain and routing-oriented — present state, evidence, limits, and exit path. `p.rx-cost` follows `p.rx-lede`, is non-empty, contains the word `minute`, and prices the stopping points in plain words for the contract's reader, who lacks the prerequisite concepts triage lists. The `h1` and every card obey the contract's voice rules.

The table of contents is the plan made navigable, and the tool checks it against `outline.json` exactly: its links are, in order, `#objectives`, then each planned section id in plan order, then `#quiz`. Link text is the section's planned title (shortened is fine; never vaguer).

The objectives block restates the triage `objectives` as reader-facing plain English: `ol.rx-objectives` with one `li` per triage objective, ids matching the triage objective ids exactly, in the same order. Vary the statements' opening words — four list items that all start with the same three words trip the repeated-opener check. One `p.rx-claim` follows the `h2`, as in every section.

Before finishing, re-read the plan as a table of contents alone: a reader who sees only the titles must come away knowing what the change is. Then confirm both output files exist, the JSON parses, and the fragment obeys the contract's grammar.
