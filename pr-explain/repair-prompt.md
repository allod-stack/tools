## Repair rules

This is a mechanical correction pass, not a rewrite, and it is the only one: the tool validates once more and then stops.

- Read the current body at that path first. Change only what the diagnostics above require.
- Preserve the semantic content. Every claim, worked example, figure, code walk, callout, quiz item, and provenance value that is already there stays, with the same meaning. Do not add new analysis, delete sections, or reword prose that no diagnostic names.
- Make the minimum structural correction that clears each diagnostic. Prefer wrapping, re-nesting, retitling a heading level, or fixing an attribute over removing content.
- Keep the body-fragment grammar. No doctype, `html`, `head`, `body`, `title`, `style`, or `script` element; every complete tag on one physical line; lowercase element and attribute names; double-quoted attribute values; no comments; no self-closing slash syntax; only the `&amp;`, `&lt;`, `&gt;`, `&quot;`, and `&apos;` character references; no `hidden`, `aria-hidden`, or `inert`.
- The contracts that fail most often:
  - every `rx-flow`, `rx-branch`, `rx-lanes`, `rx-codewalk`, `rx-timeline`, and `rx-compare` sits inside a `figure.rx-figure` whose last direct child is exactly one `figcaption.rx-caption`, and `rx-sequence` is its own `section`, never a figure;
  - heading levels never skip in document order: the fragment opens at `h1`, and no heading is more than one level deeper than the heading before it anywhere in the fragment;
  - provenance `data-runner` is the bare runner id `codex` or `claude`, while the visible runner field reads `codex subscription CLI` or `claude subscription CLI`.
- You may re-read the read-only job files named by `ALLOD_PR_EXPLAIN_JOB_DIR`: `report-contract.json` for exact provenance values, and `component-gallery.html` for canonical markup.
- Change nothing else. Do not modify `snapshot.json` or any other job file, do not touch the checkout, do not use the network, and do not write outside the body path.
- Leave a non-empty regular file at the body path, written in place or atomically replaced at that same path. Do not print the report to standard output.
