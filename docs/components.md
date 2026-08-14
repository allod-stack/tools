# PR explanation report components

The PR explanation template is a small semantic vocabulary for teaching a code change. It gives reports a stable shell, responsive diagrams, script-free learning tools, and explicit provenance without forcing every change into the same narrative.

The checked assets are:

- [`pr-explain/report.css`](../pr-explain/report.css) — layout, color, print, focus, and responsive behavior;
- [`pr-explain/report.js`](../pr-explain/report.js) — optional sequence, quiz, codewalk, and print enhancements;
- [`pr-explain/prompt.md`](../pr-explain/prompt.md) — investigation and authoring contract;
- [`pr-explain/component-gallery.html`](../pr-explain/component-gallery.html) — self-contained visual reference and regression fixture.

The runner writes a body fragment. `allod` supplies the document shell and inserts the checked CSS and JavaScript verbatim. A report cannot override either asset: extra style blocks, scripts, inline styles, and event-handler attributes fail validation. This is what turns a component fix into one reviewed template change instead of another per-report fork.

The fragment uses a deliberately small authoring grammar so the shell can validate it without interpreting browser error recovery. Keep every complete tag on one physical line, use lowercase element and attribute names, and double-quote every attribute value. Do not write HTML comments or self-closing slash syntax. Use only the named entities `&amp;`, `&lt;`, `&gt;`, `&quot;`, and `&apos;`, never numeric or other named entities. Do not author `hidden`, `aria-hidden`, or `inert`: report content must be present and perceivable before enhancement.

## Start with the reader's route

Every report opens with a masthead and an operator summary. The summary answers five fixed questions: what merging changes now, what exists afterward, what the evidence proves, what remains unproven, and how to reject or roll back. It is followed by an authored table of contents and one long `main` landmark.

Each main section starts with a heading and `p.rx-claim`. That paragraph states the point before the section asks the reader to inspect evidence. Narrative paragraphs need no class and stay at a readable measure; figures can use the full report column.

```html
<section id="behavior" aria-labelledby="behavior-h">
  <h2 id="behavior-h">The new gate runs before work begins</h2>
  <p class="rx-claim">A failed precondition now ends the request before any durable state exists.</p>
  <p>Begin with a concrete trace, then generalize from it.</p>
</section>
```

The template is a vocabulary, not a diagram checklist. Use a component only when deleting it would remove information or force the reader to hold more state in working memory. Reusing one diagram family several times is usually easier to learn than showing every available component once.

## Figures and captions

Every diagram lives in `figure.rx-figure`, and `figcaption.rx-caption` is its final child. The figure establishes its own inline-size container, so an embedded diagram responds to the space it actually receives rather than to the browser viewport.

```html
<figure class="rx-figure" id="fig-gate">
  <ol class="rx-flow" data-steps="2"><li><b>Check</b><span>compare immutable identities</span></li><li><b>Continue</b><span>enter the detached checkout</span></li></ol>
  <figcaption class="rx-caption">The failed branch exits before the first write can occur.</figcaption>
</figure>
```

A caption carries a claim. It should expose a relationship that nearby prose does not already repeat. If the same sentence works unchanged after deleting the figure, the visual is probably not earning its place.

## Linear flow

Use an ordered list for a sequence. `data-steps` must equal the number of direct list items.

```html
<figure class="rx-figure">
  <ol class="rx-flow" data-steps="3">
    <li><b>Resolve</b><span>read the fixed snapshot</span></li>
    <li><b>Inspect</b><span>trace code and tests</span></li>
    <li><b>Validate</b><span>accept only the canonical shell</span></li>
  </ol>
  <figcaption class="rx-caption">Validation receives evidence from a fixed target rather than a moving branch.</figcaption>
</figure>
```

The connector is never an element. The node after a gap owns the connector through `.rx-flow > li + li::before`. One variable, `--rx-flow-gap`, controls both flex spacing and the connector box. The default is a stacked flow with an authored down glyph. Container queries promote short flows to a row with an authored right glyph; a wide right-to-left flow uses an authored left glyph. Logical inset properties place the connector on the preceding visual side in either writing direction.

This is the geometry invariant:

- no sibling arrow or connector boxes;
- no transformed direction glyphs;
- no separate node alignment rule;
- one gap authority for spacing and connector extent;
- flows with six or more nodes stay stacked.

Long labels use overflow containment rather than widening the whole page. A one-node flow is valid and receives no connector.

## Branches and lanes

A branch is one test followed by unordered alternatives. The visible arm label carries meaning; its pseudo-element is only a visual guide.

```html
<figure class="rx-figure">
  <div class="rx-branch">
    <p class="rx-branch-test">Do the fetched and snapshotted commits match?</p>
    <ul class="rx-branch-arms" data-arms="2">
      <li class="rx-branch-arm"><b class="rx-arm-label">yes</b>Continue in the detached checkout.</li>
      <li class="rx-branch-arm"><b class="rx-arm-label">no</b>Stop before provider invocation.</li>
    </ul>
  </div>
  <figcaption class="rx-caption">Movement is a refusal condition rather than a warning attached to stale evidence.</figcaption>
</figure>
```

A lane is a label plus the ordinary flow component. This keeps one step geometry throughout the template.

```html
<figure class="rx-figure">
  <div class="rx-lanes" data-align="columns">
    <section class="rx-lane">
      <h4 class="rx-lane-label">Old path</h4>
      <ol class="rx-flow" data-steps="2">...</ol>
    </section>
    <section class="rx-lane">
      <h4 class="rx-lane-label">New path</h4>
      <ol class="rx-flow" data-steps="2">...</ol>
    </section>
  </div>
  <figcaption class="rx-caption">Both paths change at the second stage, where the new guard runs.</figcaption>
</figure>
```

`data-align="columns"` means every lane has the same step count. Omit it when the tracks are independent rather than stage-by-stage comparisons.

## User-controlled sequence

Author a sequence as a complete ordered list with at least two visible steps. Do not write controls, hide steps, or add `hidden`, `aria-hidden`, or `inert`.

```html
<section class="rx-sequence" id="seq-request" aria-labelledby="seq-request-h">
  <h3 id="seq-request-h">Following one request</h3>
  <ol class="rx-seq-steps">
    <li class="rx-seq-step" id="seq-request-1">
      <h4 class="rx-seq-title">1. Resolve the object</h4>
      <p>The snapshot fixes the expected identity.</p>
    </li>
    <li class="rx-seq-step" id="seq-request-2">
      <h4 class="rx-seq-title">2. Compare the fetched identity</h4>
      <p>A mismatch stops the job.</p>
    </li>
  </ol>
</section>
```

In a script-free viewer, both steps remain visible. When JavaScript runs, it creates Previous, Next, and Show all controls and an announced status line. State changes occur only after a reader gesture; there is no autoplay. Reduced-motion mode defaults to Show all and avoids smooth scrolling. Print always shows every step.

## Codewalk

Exact code goes in a keyboard-focusable horizontal scroll region. `.rx-code > code` uses `white-space: pre`; wrapping exact code would change its apparent line structure.

```html
<figure class="rx-figure rx-codewalk" id="cw-check">
  <div class="rx-scroll" role="region" tabindex="0" aria-label="example.sh, lines 8 through 10">
    <pre class="rx-code" data-lang="sh"><code><mark class="rx-hl" id="cw-check-h1">expected="$1"</mark>
actual="$(resolve)"
<span class="rx-elide">⋯ one line omitted ⋯</span></code></pre>
  </div>
  <ol class="rx-notes">
    <li class="rx-note" data-hl="cw-check-h1"><a href="#cw-check-h1">Expected identity</a> — the comparison begins with the immutable value.</li>
  </ol>
  <figcaption class="rx-caption">The immutable value enters the comparison before any fetched result can influence it.</figcaption>
</figure>
```

Inside the code element, only escaped text, `mark.rx-hl`, and `span.rx-elide` are allowed. Ordinary anchors make note-to-code navigation work without scripts. Enhancement adds linked focus and hover highlighting and respects reduced-motion preference when scrolling.

## Callouts and disclosure

Callouts have a closed role vocabulary: `definition`, `intuition`, `misconception`, `caution`, `boundary`, and `evidence`. The label is required because color and border treatment never carry the role alone.

```html
<aside class="rx-callout" data-role="boundary">
  <p class="rx-callout-label">Evidence boundary</p>
  <p>The unit test exercises parsing but does not start the service.</p>
</aside>
```

Use callouts sparingly. If everything is emphasized, the reader receives no signal about priority.

`details.rx-more` holds skippable depth. `details.rx-predict` asks the reader to commit to a prediction before revealing the result. Every summary must describe what opening it reveals. A `dfn.rx-term[id]` can define a term once; later `a.rx-termref` links return to it.

All native disclosure remains usable without JavaScript. Print CSS and a print enhancement expand closed details so hidden screen state does not become missing paper content.

## Timeline and comparison

An `ol.rx-timeline` records history. Each item uses one state—`done`, `now`, `blocked`, `planned`, `correction`, or `dead-end`—and includes that state as visible `p.rx-state` text. The rail and dot are decorative pseudo-elements owned by the list item. Corrections and dead ends are useful evidence: a flawless-looking history often hides the learning that most affects the reader's confidence.

A `table.rx-compare` keeps native table layout at every width. Put it in `div.rx-scroll[role="region"][tabindex="0"][aria-label]`; do not turn table rows into layout blocks. Include a claim-bearing `caption` and `scope="col"` or `scope="row"` on every header cell. State marks contain a word as well as a glyph or color.

## Quiz

The final quiz contains exactly five items with four choices each. Each choice is a native `details.rx-choice`, so selecting it can reveal instructional feedback when scripts are disabled.

```html
<article class="rx-quiz-item" id="q1">
  <h3>What happens when the fetched commit differs from the snapshot?</h3>
  <ul class="rx-choices">
    <li><details class="rx-choice" name="q1" data-correct="false" data-misconception="treats movement as a warning">
      <summary>A. The job continues and adds a warning</summary>
      <p>Not quite. Continuing would explain a different change from the one the operator approved for disclosure.</p>
    </details></li>
    <li><details class="rx-choice" name="q1" data-correct="true">
      <summary>B. The job stops before provider invocation</summary>
      <p>Correct. The immutable snapshot is a hard evidence boundary, so movement fails closed.</p>
    </details></li>
    <li><details class="rx-choice" name="q1" data-correct="false" data-misconception="assumes movement can be repaired automatically">
      <summary>C. The job silently updates the stored snapshot</summary>
      <p>Not quite. Updating the snapshot would discard the exact disclosure decision the operator confirmed.</p>
    </details></li>
    <li><details class="rx-choice" name="q1" data-correct="false" data-misconception="defers identity checks until after generation">
      <summary>D. The job generates the report and rejects it later</summary>
      <p>Not quite. Identity is verified before provider invocation so stale evidence never reaches generation.</p>
    </details></li>
  </ul>
  <p class="rx-quiz-result" aria-live="polite"></p>
</article>
```

Every item has one correct choice. Every incorrect choice names the specific misconception behind it. The feedback teaches the governing rule instead of merely revealing the key. JavaScript optionally locks the first selection, marks its result, and maintains a live score; the answer and feedback mechanism remains native HTML.

After the first enhanced selection, the chosen disclosure remains operable so its feedback can be collapsed and reopened. Unselected choices receive disabled semantics and leave the tab order, while the polite result region announces both the result and that the answer is locked. With scripts disabled, all four native disclosures and every explanation remain available.

The prompt owns the question-writing discipline: quiz last, application before recall, answer and rationale before distractors, balanced option shape, A–D once across the first four keys, and a runtime-randomized fifth key.

## Provenance

The footer is fail-closed. Its `dl.rx-provenance` opening tag carries machine-readable repository, PR, URL, base/head SHAs, and runner attributes that must match the resolved snapshot. Visible fields record repository, PR, URL, title, base, head, diffstat, runner, generator, generation date, sources read, and at least one verification limit.

Provenance says what was examined, not what the report wishes had been examined. A missing review source or unavailable witness belongs in `limits`, not in an implied claim of completeness.

## Accessibility and viewing paths

The primary viewing path is script-free HTML in Forge's sandbox. The page therefore remains complete before enhancement. The same file may be opened locally to gain sequence stepping, quiz scoring, linked code highlighting, and print expansion.

The template provides:

- light and dark token sets through `prefers-color-scheme`;
- visible `:focus-visible` outlines;
- a skip link and semantic landmarks;
- logical properties and right-to-left flow behavior;
- keyboard-reachable overflow regions;
- redundant text for every state and callout role;
- reduced-motion behavior with no keyframes or autoplay;
- print colors, expanded details, visible sequence steps, and break protection for figures and callouts;
- a phone layout that keeps prose, provenance, summaries, diagrams, and controls inside the viewport.

The light-theme interactive-border token is `#7a8992`. Its WCAG relative-luminance contrast is 3.61:1 against white, 3.52:1 against the `#fdfcfa` page surface, and 3.32:1 against the `#f3f6f7` soft surface; the weakest supported pairing therefore remains above 3:1.

JavaScript is progressive enhancement only. Each enhancer is isolated and idempotent, constructs controls with DOM methods, and uses no network, storage, dynamic-code, or HTML-reparsing APIs.

## Inspecting the gallery

Open `pr-explain/component-gallery.html` in a local browser. For example, when Firefox is available:

```sh
firefox pr-explain/component-gallery.html
```

Check the fixed-width flow previews at 320, 375, 768, and 1280 pixels. At narrow widths, nodes and down glyphs share one column. At wide widths, short flows use right glyphs; the 1280-pixel five-node case exercises its separate horizontal threshold, and the fixed 768-pixel right-to-left case uses left glyphs. Also inspect dark mode, keyboard traversal, reduced-motion mode, printed output, horizontal code/table scrolling, sequence controls, and quiz feedback. Disable JavaScript and confirm that every sequence step and answer explanation remains available and that no dead control remains in the authored source.

CI does not require a headless browser, so it does not claim pixel equivalence. Narrow-screen geometry is guarded mechanically by source invariants: node-owned pseudo-elements, logical positioning, authored direction glyphs, one shared gap variable, matching `data-steps`, container queries, and absence of sibling connector classes. The four-width gallery remains the documented manual visual check. This is a real residual limitation, not a substitute claim that source checks prove rendered pixels.

Whenever `report.css` or `report.js` changes, update the gallery's inline asset from that same file and run the component tests. The gallery must contain byte-identical canonical assets and pass the production validator before the template is accepted.
