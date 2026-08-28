## Your pass: write one body section

You write exactly one `<section>` of the report — the one specified at the end of this prompt, from the plan in `outline.json`. The front matter and every earlier section are quoted below your section's specification: they are the report so far, already accepted. Continue them; do not rewrite, restate, or contradict them, and do not write any other section. The reader contract above binds this section as strictly as it bound the first one — by this point in the report the temptation is compressed clause-chains and insider shorthand, and giving in to it is how reports die in their back half.

Your fragment is the section element and nothing else:

- The first line is the opening tag, built from your section's plan entry exactly: `<section id="<plan id>" aria-labelledby="<plan id>-h" data-layer="<plan layer>" data-objective="<plan objectives, space-separated, in plan order>">`.
- The `h2` carries `id="<plan id>-h"` and the planned title (tightened wording is fine; a vaguer or subject-hiding title is not), followed immediately by one `p.rx-claim` that states the point before the evidence.
- The last line is `</section>`. No content before the opening tag or after the closing one, and never a `main`, `footer`, masthead, or another section's markup.

Investigate before writing: read the code your section explains in the checkout, not just the plan's gist. The outline pass left evidence notes in `outline.json` under `notes` — use them, and verify any claim you take from them before teaching it. Quiz items are written by a later pass that sees the whole report; do not write `article.rx-quiz-item` elements here. Prediction and reasoning disclosures (`details.rx-predict`) belong to this pass and are encouraged where the section teaches something worth committing to first.

## Design for learning

Use a coherent narrative for this section that serves the plan's `gist` and objectives. Apply these learning mechanisms deliberately:

- **Objectives bound content:** the section teaches toward the objectives its plan entry claims, and it tells you what to cut. Anything that serves no objective spends the reader's attention on work the merge decision never asked for.
- **Prediction before reveal:** in concept and mechanism sections, put `details.rx-predict` immediately before each important reveal. A reader who commits to a guess learns from the answer; one who only reads it confirms nothing. A prediction question uses only terms the report has already established — asking the reader to predict something about a word they have not been taught measures nothing — and its reveal answers in the same plain sentences as body text.
- **Reasoning before explanation:** the strongest questions ask *why*, not *what*. Wherever the section teaches a deliberate design decision — a fact owned by one repository and not another, a default overridden, a check deliberately not written, a gate placed here and not there — pose the why-question in a `details.rx-predict` before explaining the decision: "why must this list come from the guest rather than the host?", "what would break if both repositories stated this rule?". The reveal gives the reasoned answer in plain body sentences and names what the rejected alternative would break.
- **Progressive disclosure:** give beginner-friendly context first and put skippable depth in informative `details.rx-more` blocks. The surrounding explanation must remain complete when they stay closed.
- **Signaling:** the `p.rx-claim` after the `h2` states the point before the evidence, and every figure caption carries a claim, not a label.
- **Chunking:** group this section's material by concept, behavior, or execution path. Keep diagrams small enough to scan and reuse the same small visual family the earlier sections used instead of changing visual grammar.
- **Concrete examples:** show a worked trace with representative values before stating the general rule. Connect implementation details to user-visible behavior and tests.
- **Dual coding:** a figure earns its place when it makes explicit something the prose leaves implicit — when the reader sees an answer instead of reading labels and assembling them in working memory. The caption states the claim the figure supports rather than repeating the adjacent paragraph.
- **Misconception seeding:** where the section corrects a tempting false belief, teach the correction in the body — the quiz pass will build its distractors from what the body taught.

## Choosing a figure

Answer these three questions before writing any figure markup. They decide both whether to draw and what to draw.

1. **What question does the reader have at this point?** Name it in one sentence: "in what order do these run?", "which path does a request take, and on what test?", "what is true of this path that is not true of that one?", "where in this file does the check happen?"
2. **What would they otherwise have to work out for themselves?** Prose is read in sequence; a figure is read by location, so it can put everything one inference needs in a single place and let the reader perceive the answer instead of computing it. If the honest answer is "nothing, the prose states it directly," there is no figure here. Writing the paragraph instead is a correct outcome, and a section with no figure is a correct section — a figure that has to be decoded costs the reader more than the paragraph it replaced.
3. **Which relationship is it?** The relationship chooses the component, through the promises listed below. If it matches none of them, this is prose or a comparison table.

Because language is itself sequential, a sequence is the relationship prose already handles well — which makes a flow the least valuable figure you can draw and the one that comes to mind first. Figures pay for themselves on the relationships prose holds poorly: **containment** (these three units all sit behind one gate), **correspondence** (what happens here at this stage happens there at the same stage), **divergence** (two paths are identical until exactly this test), and **anchoring** (this claim lives on that line of code).

A figure has no room to teach a word, so it may not be the first place a term appears. Every label uses terms the surrounding prose has already used, and states meaning rather than a code — "only the owner can read it", not a bare permission number. A figure that makes the reader import knowledge from outside it adds work instead of removing it.

## Component vocabulary

The gallery is the markup reference. Use only components that remove cognitive work; omitting an irrelevant component is correct.

- Wrap every diagram in `figure.rx-figure` and put a non-empty, claim-bearing `figcaption.rx-caption` last. Every `figure.rx-figure` carries `data-objective` naming one or more of this section's objective ids; put `data-objective` nowhere else inside the section — not on nested sections such as `rx-sequence` or `rx-lane`.
- A linear flow is `ol.rx-flow[data-steps]` with direct `li` nodes. Each node contains a short `b` label and optional `span` explanation. `data-steps` equals the node count. Never author arrows or connectors; the following node owns its connector through canonical CSS. On wide screens a flow lays its nodes out as side-by-side columns, so every node's entire text must survive being one narrow column: keep each `span` to a phrase, and never put a literal path, command line, or full sentence in a flow node. A step that needs a long literal or a sentence of explanation disqualifies the flow — use a top-down `section.rx-sequence`, whose steps have room for prose, or a codewalk when the literal is the point.
- A branch uses `div.rx-branch`, `p.rx-branch-test`, and `ul.rx-branch-arms[data-arms]` containing `li.rx-branch-arm` with a textual `b.rx-arm-label`.
- Parallel lanes use `div.rx-lanes`, sections named `rx-lane`, an `h4.rx-lane-label`, and one `rx-flow` per lane. With `data-align="columns"`, every lane has the same step count.
- A sequence uses `section.rx-sequence`, one `h3`, and `ol.rx-seq-steps` containing at least two visible `li.rx-seq-step` elements with `h4.rx-seq-title`. Author no controls, hide no steps, and do not add `hidden`, `aria-hidden`, or `inert`.
- A codewalk is `figure.rx-figure.rx-codewalk`. Put `pre.rx-code[data-lang] > code` inside `div.rx-scroll[role="region"][tabindex="0"][aria-label]`. Exact code uses preserved whitespace. Within `code`, use text plus only `mark.rx-hl[id]` and `span.rx-elide`. Link `li.rx-note[data-hl]` annotations to the highlight IDs.
- A callout is `aside.rx-callout[data-role]` with a visible `p.rx-callout-label`. Roles are `definition`, `intuition`, `misconception`, `caution`, `boundary`, and `evidence`.
- A timeline is `ol.rx-timeline`; every item uses one of `done`, `now`, `blocked`, `planned`, `correction`, or `dead-end` and includes visible `p.rx-state` text.
- A comparison is `table.rx-compare` inside a named keyboard-scroll region. Include a claim-bearing `caption`, and put `scope="col"` or `scope="row"` on every `th`. State markers contain words, not glyphs or color alone.
- Use `details.rx-more`, `details.rx-predict`, `dfn.rx-term[id]`, and `a.rx-termref[href="#id"]` for optional depth, prediction, and term recall. Disclosure summaries say what opening them reveals.
- Optional `rx-stats` contain at most four `rx-stat` items, and every description says what the number counts or excludes.
- Give ids to elements a later section or the quiz may need to reference; keep every id unique across the report — the fragments quoted below show which ids are taken.

Each component makes one spatial promise. Check that yours is true of your content, and choose a different component or prose when it is not.

| Component | Promises | Not this component when |
| --- | --- | --- |
| `rx-flow`, `rx-sequence` | the steps are ordered | reordering them would not make the figure false |
| `rx-lanes` | row N of every lane is the same stage | the lanes are independent lists set side by side |
| `rx-branch` | the arms are exclusive outcomes of one test | there is no single test, or the arms overlap |
| `rx-compare` | one grid: the same attributes for every subject | subjects carry different attributes, or cells stand empty |
| `rx-timeline` | the order is chronological | the order is anything other than time |
| `rx-codewalk` | every note is anchored to a specific line | the code illustrates rather than being the subject |

Row order is not a channel for `rx-compare`: sorting a table changes nothing about what it says, so the reordering check belongs to flows and sequences alone.

Use no classes outside the gallery vocabulary. Use no inline `style`, event-handler attributes, external resources, external links, images, SVG, canvas, forms, media, embedded documents, or network-capable markup. Use same-document `#fragment` anchors only, and only to ids that exist in the report so far or in this section. No information needed to understand the change may depend on JavaScript.

## Finish mechanically

Re-read the finished section and check these before returning:

0. **The section obeys the reader contract as strictly as the report's opening did.** Re-read every sentence against "Who you are writing for" — the plain-English rules, the real-name rule, the disclosure-register rule — and rewrite any sentence that would not have survived in the report's first section. This check exists because register decays with position, and your section is the position where it decays.
1. **Every diagram is in a figure.** Each `rx-flow`, `rx-branch`, `rx-lanes`, `rx-codewalk`, `rx-timeline`, and `rx-compare` — including one used as a short aside mid-section — is inside a `figure.rx-figure` whose last direct child is exactly one `figcaption.rx-caption`. Only `rx-sequence` is exempt: it is its own `section`, never a figure.
2. **Every figure keeps its promise.** For each figure: the reader question it answers is real, the component's promise above is true of its content, no label introduces a term the prose has not already used in plain English, and the adjacent paragraph does not already carry the same content. Content that fails any of these belongs in prose, and moving it there is the fix.
3. **Heading levels never skip in document order.** Your section's `h2` is followed by headings each at most one level deeper than the heading before it: a `h4` may follow only a `h3` or deeper. The report so far ends at heading level two, so your section starts from its own `h2`.
4. **The opening tag matches the plan entry exactly** — id, `aria-labelledby`, `data-layer`, `data-objective` — and the fragment holds one section, whole: starts at its opening tag, ends at `</section>`, every internal reference resolves, every sequence has at least two visible steps, code whitespace is exact, and nothing uses `hidden`, `aria-hidden`, or `inert`.
