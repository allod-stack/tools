## Your pass: write the quiz and close the report

You are the last authoring pass, and the only one that sees the report whole: the front matter and every body section are quoted below, already accepted. Your fragment closes the document — the final quiz section, then the closing `</main>`, then the provenance footer:

```html
<section id="quiz" aria-labelledby="quiz-h" data-layer="..." data-objective="...">
  <h2 id="quiz-h">A subject-naming heading for the retrieval section</h2>
  <p class="rx-claim">...</p>
  ...three to seven quiz items...
</section>
</main>

<footer class="rx-footer" id="rx-provenance">
  <dl class="rx-provenance" data-repository="owner/repo" data-pr="N" data-pr-url="exact snapshot URL" data-base-sha="40 lowercase hex" data-head-sha="40 lowercase hex" data-runner="codex-claude-or-pi">
    ...required provenance fields...
  </dl>
</footer>
```

The quiz section's `data-layer` is the last body section's layer or later — document order never goes backward through `concept`, `mechanism`, `receipts` — and its `data-objective` names every objective its items test, space-separated.

## Write the quiz against the report, not the diff

The quiz is the report's active retrieval: answering strengthens only material the report taught, so the quiz tests only what the report taught. Draft items from the quoted sections: before keeping an item, point to the body paragraph that gives a first-time reader enough to answer it. If the knowledge lives only in a stem or a feedback body, the body is missing a paragraph — you cannot add one from this pass, so replace the item with one the report supports. A reader's first contact with a fact must never happen under assessment pressure.

Write **three to seven** `article.rx-quiz-item` elements as direct children of the quiz section, with exactly four choices per item; at a `T1` tier stay at the minimum of three tight items. Each item carries `data-concept` with one concept slug from `triage.json` and `data-objective` with exactly one objective id, and every objective is tested by at least one item. Most items must require application, prediction, diagnosis, or comparison; at most two may ask for direct recall; and at least one item must test design reasoning — why this design rather than a plausible alternative — with the tempting alternatives as distractors whose feedback names what each one would break.

For every item:

1. Choose one learning objective from the objectives block: predict behavior, trace data or control flow, select a design under a new scenario, diagnose a plausible failure, or distinguish an implemented guarantee from a tempting overclaim.
2. Write the stem, defensibly best answer, and instructional rationale before writing distractors. The stem must be answerable without seeing the options.
3. Write plausible distractors from specific misconceptions — the false beliefs the body sections corrected. Avoid trivia, obscure wording, tricks, double negatives, `all of the above`, and `none of the above`.
4. Keep options parallel in grammar, specificity, technical register, and approximate length. Do not signal the key by making it consistently longer, more qualified, or more polished.
5. Make every feedback body explain the governing concept and why that selected answer is right or wrong, not merely reveal the key. Write each body knowing the reader sees all four after answering: together they must correct a wrong answer completely — the misconception named, the governing rule stated.

Use this no-script answer mechanism:

```html
<article class="rx-quiz-item" id="q1" data-concept="concept-slug" data-objective="obj-1">
  <h3>Application question?</h3>
  <ul class="rx-choices">
    <li><details class="rx-choice" name="q1" data-correct="false" data-misconception="specific false belief"><summary>A. Parallel option</summary><p>Instructional feedback of at least eight words.</p></details></li>
    <li><details class="rx-choice" name="q1" data-correct="true"><summary>B. Parallel option</summary><p>Instructional feedback of at least eight words.</p></details></li>
    ...two more choices...
  </ul>
  <p class="rx-quiz-result" aria-live="polite"></p>
</article>
```

Every item shares one unique `name="qN"`, contains exactly one `data-correct="true"`, and gives every incorrect choice a non-empty `data-misconception`. Across all items, no answer letter may be correct more than twice. Choose each correct position before writing the choices, vary the positions rather than defaulting to one letter, and store correctness on each choice; never infer it from a fixed index.

Then audit the choices without looking at the keys. Compare option word counts, detail, grammar, tone, and repeated vocabulary. Revise any item a test-wise reader could answer without understanding the change. Do not put authoring notes in the report.

## The provenance footer

The provenance list contains each field exactly once: `repo`, `pr`, `url`, `title`, `base`, `head`, `diffstat`, `runner`, `generator`, `generated`, `sources`, and `limits`. Copy repository, pull request, URL, title, SHAs, runner, and date exactly from `report-contract.json`. Show the runner as `codex subscription CLI`, `claude subscription CLI`, or `pi API CLI`. Put the generated date in `<time datetime="YYYY-MM-DD">YYYY-MM-DD</time>`. The `sources` value names what was actually read across the passes — the outline pass's evidence notes in `outline.json` record it. The `limits` value contains a list with at least one concrete unverified claim, in the contract's plainest register: what this report asserts that no pass executed or observed.

## Finish mechanically

Re-read the finished fragment and check:

1. **Objective bookkeeping closes.** Every objective in the objectives block is tested by at least one quiz item; every item's `data-objective` id and `data-concept` slug exist in `triage.json`; the quiz section's `data-objective` lists exactly the ids its items test.
2. **Answer letters balance.** No letter is correct more than twice across all items, and the misconception labels are complete.
3. **Provenance carries the id and the label.** `data-runner` is the bare `codex`, `claude`, or `pi`; the visible runner field reads `codex subscription CLI`, `claude subscription CLI`, or `pi API CLI`; every other field matches `report-contract.json` exactly.
4. **The fragment closes the document and nothing else.** It starts at the quiz section's opening tag, the `</main>` line sits between the quiz section and the footer, the footer is last, every internal reference resolves against the quoted report, and the disclosure-register rule holds inside every choice explanation.
