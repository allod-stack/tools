## Tightening rules

This is a deletion-only pass over a finished report body, not a rewrite and not a review. Make the surviving report shorter and plainer without changing what it claims.

- Read the current body at that path first.
- Permitted operations, and no others: deleting sentences, clauses, list items, and whole elements whose removal keeps the document valid; tightening wording so the same claim takes fewer, plainer words.
- Forbidden operations: adding new sentences, sections, figures, quiz items, or claims; changing quoted code, provenance values, quiz correctness data (`data-correct`, `data-misconception`), objective ids, or any factual content.
- Delete these words and phrases wherever they appear in visible prose, rewording the sentence around the cut: `delve`, `delves`, `delving`, `tapestry`, `testament`, `seamless`, `seamlessly`, `utilize`, `utilizes`, `utilizing`, `leverages`, `leveraging`, `worth noting`, `it is important to note`, `in today's`, `plays a vital role`, `rich landscape`, `crucial role`. Break up any three-word sentence opening repeated four or more times, and cut hedge words (`may`, `might`, `could`, `perhaps`, `arguably`, `likely`) that guard no real uncertainty.
- Target: cut at least 15 percent of the visible prose words, unless the body is already within its triage budget and free of the vocabulary above. Say nothing, in the report or anywhere else, about having tightened it.
- The result must still satisfy the complete body grammar, including objective coverage: every objective id keeps at least one section and at least one quiz item claiming it. When deleting an element would orphan an objective, tighten that element instead of removing it.
- Keep the body-fragment grammar. No doctype, `html`, `head`, `body`, `title`, `style`, or `script` element; every complete tag on one physical line; lowercase element and attribute names; double-quoted attribute values; no comments; no self-closing slash syntax; only the `&amp;`, `&lt;`, `&gt;`, `&quot;`, and `&apos;` character references; no `hidden`, `aria-hidden`, or `inert`.
- You may re-read the read-only job files named by `ALLOD_PR_EXPLAIN_JOB_DIR`: `triage.json` for the tier, budget, and objective ids, and `component-gallery.html` for canonical markup.
- Change nothing else. Do not modify `snapshot.json` or any other job file, do not touch the checkout, do not use the network, and do not write outside the body path.
- Leave a non-empty regular file at the body path, written in place or atomically replaced at that same path. Do not print the report to standard output.
