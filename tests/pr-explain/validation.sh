#!/usr/bin/env bash
# shellcheck source=/dev/null
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_case validation-base
write_valid_body codex
VALID_REPORT="$CASE_DIR/output/valid.html"
SNAPSHOT="$CASE_DIR/snapshot.json"
write_snapshot_file "$SNAPSHOT"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$VALID_REPORT"
assert_success "builds the known-good report used by sabotage checks"

capture "$ALLOD" pr _validate-report "$VALID_REPORT" "$SNAPSHOT" codex
assert_success "the validator accepts an unmodified generated report"
assert_contains "$CAPTURE_OUTPUT" "validation warning" \
  "advisory content warnings do not block an otherwise mechanical-valid report"

validation_failure() {
  local file="$1" expected_regex="$2" description="$3"
  capture "$ALLOD" pr _validate-report "$file" "$SNAPSHOT" codex
  if [[ "$CAPTURE_STATUS" -eq 0 ]]; then
    fail "$description" "validator unexpectedly accepted: $file"
  elif printf '%s\n' "$CAPTURE_OUTPUT" | grep -Eiq "$expected_regex"; then
    pass "$description"
  else
    fail "$description" "expected diagnostic matching: $expected_regex" \
      "actual output:" "$CAPTURE_OUTPUT"
  fi
}

sabotage_copy() {
  local name="$1"
  SABOTAGE="$CASE_DIR/$name.html"
  cp "$VALID_REPORT" "$SABOTAGE"
}

splice_before_main_close() {
  local source="$1" fragment="$2" destination="$3"
  awk -v fragfile="$fragment" '
    $0 == "</main>" { while ((getline line < fragfile) > 0) print line }
    { print }
  ' "$source" > "$destination"
}

sabotage_copy active-script
sed -i 's#</main>#<script>alert("active")</script></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'script|active|template' \
  "rejects an extra active script element"

sabotage_copy multiline-script
sed -i 's#</main>#<script\n>fetch("https://outside.example/");</script\n>\n</main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'script|active|fetch|network|template|HTML|tag|complete' \
  "rejects a network-capable script whose tags span lines"

sabotage_copy slash-delimited-script
sed -i 's#</main>#<script/x>alert("active")</script></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'script|active|canonical|HTML|tag' \
  "rejects a slash-delimited script tag accepted by the HTML tokenizer"

sabotage_copy malformed-attribute
sed -i 's#</main>#<p =>Malformed attribute syntax must terminate validation.</p></main>#' "$SABOTAGE"
capture timeout 3 "$ALLOD" pr _validate-report "$SABOTAGE" "$SNAPSHOT" codex
if [[ "$CAPTURE_STATUS" -eq 124 ]]; then
  fail "rejects malformed attributes without hanging" "validator exceeded the three-second guard"
elif [[ "$CAPTURE_STATUS" -ne 0 ]] && printf '%s\n' "$CAPTURE_OUTPUT" | grep -Eiq 'canonical|attribute|HTML|tag'; then
  pass "rejects malformed attributes without hanging"
else
  fail "rejects malformed attributes without hanging" "status: $CAPTURE_STATUS" "output:" "$CAPTURE_OUTPUT"
fi

sabotage_copy multiline-active
sed -i 's#</main>#<form\n>\n<p>Cross-line active markup</p>\n</form>\n</main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'form|active|forbidden|markup|HTML|tag|complete' \
  "rejects an active tag whose opening syntax spans lines"

sabotage_copy multiline-refresh
sed -i 's#</main>#<meta\n http-equiv="refresh"\n content="0;url=https://outside.example/"\n>\n</main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'meta|refresh|active|network|resource|HTML|tag|complete' \
  "rejects a refresh directive whose tag spans lines"

sabotage_copy external-image
sed -i 's#</main>#<img src="https://outside.example/image.png" alt="external"></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'img|external|resource|network' \
  "rejects network-capable external image markup"

sabotage_copy legacy-background
sed -i 's#</main>#<figure class="rx-figure"><div class="rx-scroll" role="region" tabindex="0" aria-label="External background probe"><table class="rx-compare" background="https://outside.example/pixel.png"><caption>The probe table must never load an external background resource.</caption><thead><tr><th scope="col">Input</th><th scope="col">Result</th></tr></thead><tbody><tr><th scope="row">Legacy attribute</th><td>Blocked</td></tr></tbody></table></div><figcaption class="rx-caption">Legacy presentation attributes can still initiate a browser request.</figcaption></figure></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'background|network|resource|attribute' \
  "rejects the legacy network-capable background attribute"

sabotage_copy entity-duplicate-href
sed -i 's|</main>|<p><a href="h\&#116;tps://outside.example/pixel" href="#rx-main">External resource</a></p></main>|' "$SABOTAGE"
validation_failure "$SABOTAGE" 'canonical|entity|attribute|network|href' \
  "rejects entity-obfuscated and duplicate URL attributes"

sabotage_copy external-css
sed -i '0,/<style id="rx-template-css">/s#<style id="rx-template-css">#<style id="rx-template-css">@import "https://outside.example/report.css";#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'style|css|import|template|external' \
  "rejects network-capable CSS and a modified pinned stylesheet"

sabotage_copy inline-style
sed -i 's#<main id="rx-main">#<main id="rx-main" style="display:block">#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'style|attribute|HTML|tag|complete' \
  "rejects report-authored inline styles"

sabotage_copy multiline-attribute
sed -i 's#</main>#<p\nstyle\n=\n"display:block"\n>Cross-line inline style</p>\n</main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'style|attribute|HTML|tag|complete' \
  "rejects a dangerous attribute whose name and value span lines"

sabotage_copy event-handler
sed -i 's#<main id="rx-main">#<main id="rx-main" onclick="alert(1)">#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'event|onclick|attribute|active' \
  "rejects report-authored event handlers"

sabotage_copy form
sed -i 's#</main>#<form action="https://outside.example"><input name="source"></form></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'form|input|active|forbidden' \
  "rejects form controls and submission-capable markup"

sabotage_copy secret
sed -i 's#</main>#<p>AKIAABCDEFGHIJKLMNOP</p></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'secret|credential|AKIA' \
  "rejects secret-looking report content"

sabotage_copy entity-secret
sed -i 's|</main>|<p>AKIA\&#65;BCDEFGHIJKLMNOP</p></main>|' "$SABOTAGE"
validation_failure "$SABOTAGE" 'entity|secret|character reference' \
  "rejects entity-obfuscated secret-looking content"

sabotage_copy colon-delimited-password
sed -i 's#</main>#<p>password: correcthorsebatterystaple</p></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'secret|credential' \
  "rejects a colon-delimited password value"

sabotage_copy colon-delimited-api-key
sed -i 's#</main>#<p>api_key: 4f8a9c2e7b1d6053a8f9c2e7b1d6</p></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'secret|credential' \
  "rejects a colon-delimited api_key value"

sabotage_copy stripe-style-key
sed -i 's#</main>#<p>sk_live_4eC39HqLyjWDarjtT1zdp7dc</p></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'secret|credential' \
  "rejects a short prefixed secret-key shape"

sabotage_copy jwt-shape
sed -i 's#</main>#<p>eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c</p></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'secret|credential' \
  "rejects a JWT-shaped bearer token"

# The secret scan runs over the raw report bytes, not a rendering of them, so
# quoting a credential-shaped value inside <code> is not an exemption. This is
# the documented defense-in-depth intent (docs/pr-explain.md): a passage that
# only *names* a field is allowed, but a pasted value stays blocked no matter
# what element carries it, including a code sample that would look like a
# natural, low-suspicion place to paste one.
sabotage_copy secret-in-code
sed -i 's#</main>#<p>Example key: <code>AKIAABCDEFGHIJKLMNOP</code></p></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'E19.*secret-looking' \
  "blocks a secret-shaped value quoted inside <code>; a code element is not an exemption"

SECRET_PROSE_FRAGMENT="$CASE_DIR/secret-prose-fragment.html"
cat > "$SECRET_PROSE_FRAGMENT" <<'HTML'
<section id="auth-notes" aria-labelledby="auth-notes-h" data-layer="receipts" data-objective="obj-2">
  <h2 id="auth-notes-h">Authentication prose does not read as a credential</h2>
  <p class="rx-claim">Naming a credential field is not the same as quoting its value.</p>
  <p>The form asks for a password: it must be at least twelve characters and one number.</p>
  <p>The client sends an auth token: it is short-lived and rotated automatically by the library.</p>
  <p>Configuration documents an api_key: the field is present but this report never received one.</p>
  <p>A bearer token grants access after the user authenticates through the identity provider.</p>
</section>
HTML
SECRET_PROSE_REPORT="$CASE_DIR/secret-prose-report.html"
splice_before_main_close "$VALID_REPORT" "$SECRET_PROSE_FRAGMENT" "$SECRET_PROSE_REPORT"
capture "$ALLOD" pr _validate-report "$SECRET_PROSE_REPORT" "$SNAPSHOT" codex
assert_success \
  "accepts ordinary prose that names password/token/api_key fields without quoting a contiguous value"

sabotage_copy provenance-sha
sed -i "0,/$SAME_HEAD_SHA/s//$BASE_SHA/" "$SABOTAGE"
validation_failure "$SABOTAGE" 'head|provenance|snapshot|commit' \
  "rejects provenance that disagrees with the immutable snapshot"

sabotage_copy provenance-runner
sed -i 's/data-runner="codex"/data-runner="claude"/' "$SABOTAGE"
validation_failure "$SABOTAGE" 'runner|provenance' \
  "rejects provenance that names a different runner"

sabotage_copy provenance-title
sed -i 's#<dt data-field="title">Pull request title</dt><dd>Teach the change</dd>#<dt data-field="title">Pull request title</dt><dd>Wrong visible title</dd>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'title|provenance|snapshot' \
  "rejects a visible pull request title that disagrees with the snapshot"

sabotage_copy provenance-decoy
sed -i 's#<dt data-field="repo">Repository</dt><dd>acme/widget</dd>#<dt data-field="repo">Repository</dt><dd>wrong/repository</dd>#' "$SABOTAGE"
sed -i 's#</main>#<dl><dt>Decoy repository</dt><dd>acme/widget</dd></dl></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'repository|provenance|misplaced' \
  "rejects expected provenance text supplied by a decoy outside the footer"

sabotage_copy impossible-date
sed -i 's#<time datetime="2026-08-14">2026-08-14</time>#<time datetime="2026-99-99">2026-99-99</time>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'date|ISO|generated|real' \
  "rejects a syntactically shaped but impossible generated date"

sabotage_copy dangling-anchor
sed -i 's|href="#background"|href="#missing-section"|' "$SABOTAGE"
validation_failure "$SABOTAGE" 'anchor|fragment|href|missing-section' \
  "rejects a dangling same-document anchor"

sabotage_copy duplicate-id
sed -i 's#<main id="rx-main">#<main id="background">#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'duplicate|id|background' \
  "rejects duplicate identifiers"

# Quoted code makes attribute-shaped text (`PID=""`, `pid="worker-7"`,
# `href="#local"`) look like identifiers to a whole-file scan, so identifier
# and reference accounting must read markup tags only. The first attended v2
# run failed exactly here: the report quoted the tool's own
# `PR_EXPLAIN_PROVIDER_PID=""` line back at the validator.
QUOTED_ATTR_FRAGMENT="$CASE_DIR/quoted-attr-fragment.html"
cat > "$QUOTED_ATTR_FRAGMENT" <<'HTML'
<section id="cage-quoting" aria-labelledby="cage-quoting-h" data-layer="receipts" data-objective="obj-2">
  <h2 id="cage-quoting-h">Quoted code is text, not markup</h2>
  <p class="rx-claim">Attribute-shaped strings inside quoted code never count as document identifiers.</p>
  <p>The provider cage clears <code>PR_EXPLAIN_PROVIDER_PID=""</code> before it arms the trap, and quoted lines such as <code>pid="worker-7"</code> or <code>href="#local"</code> stay plain text.</p>
</section>
HTML
QUOTED_ATTR_REPORT="$CASE_DIR/quoted-attr-report.html"
splice_before_main_close "$VALID_REPORT" "$QUOTED_ATTR_FRAGMENT" "$QUOTED_ATTR_REPORT"
capture "$ALLOD" pr _validate-report "$QUOTED_ATTR_REPORT" "$SNAPSHOT" codex
assert_success \
  "accepts attribute-shaped strings quoted inside code; id accounting reads tags only"

sabotage_copy malformed-id
sed -i '0,/<p class="rx-claim">/s//<p class="rx-claim" id="9lives">/' "$SABOTAGE"
validation_failure "$SABOTAGE" 'canonical identifier' \
  "still rejects a malformed id carried by a real markup tag"

sabotage_copy broken-aria
sed -i 's/aria-labelledby="background-h"/aria-labelledby="missing-heading"/' "$SABOTAGE"
validation_failure "$SABOTAGE" 'aria|missing-heading|reference' \
  "rejects an inaccessible ARIA reference"

sabotage_copy unnamed-toc
sed -i 's/<nav class="rx-toc" aria-label="Contents">/<nav class="rx-toc" aria-label="">/' "$SABOTAGE"
validation_failure "$SABOTAGE" 'navigation|landmark|accessibility|empty' \
  "rejects an unnamed table-of-contents landmark"

sabotage_copy missing-viewport
sed -i '/name="viewport"/d' "$SABOTAGE"
validation_failure "$SABOTAGE" 'viewport|head|shell' \
  "rejects a malformed required document shell"

sabotage_copy missing-summary-card
sed -i 's/data-q="what-changes-now"/data-q="not-a-summary-question"/' "$SABOTAGE"
validation_failure "$SABOTAGE" 'summary|what-changes-now|data-q' \
  "rejects a malformed operator-routing summary"

sabotage_copy detached-summary-cards
sed -i 's#  <ul class="rx-summary-cards">#</section><section>\n  <ul class="rx-summary-cards">#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'summary|directly|routing|decision' \
  "rejects routing cards detached from the operator summary"

sabotage_copy quiz-correctness
sed -i '0,/name="q1" data-correct="false"/s//name="q1" data-correct="true"/' "$SABOTAGE"
validation_failure "$SABOTAGE" 'quiz|correct|choice' \
  "rejects a quiz item with more than one correct choice"

sabotage_copy quiz-misconception
sed -i '0,/ data-misconception="[^"]*"/s///' "$SABOTAGE"
validation_failure "$SABOTAGE" 'quiz|misconception|choice' \
  "rejects a distractor with no misconception model"

sabotage_copy redistributed-sequence-titles
sed -i 's#</main>#<section class="rx-sequence" id="seq-sabotage" aria-labelledby="seq-sabotage-h"><h3 id="seq-sabotage-h">Two visible steps</h3><ol class="rx-seq-steps"><li class="rx-seq-step"><p>The first step has no title.</p></li><li class="rx-seq-step"><h4 class="rx-seq-title">First misplaced title</h4><h4 class="rx-seq-title">Second misplaced title</h4><p>The second step cannot title the first.</p></li></ol></section></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'sequence|title|step|ordered' \
  "rejects sequence titles redistributed between steps"

sabotage_copy malformed-flow
sed -i 's#</section>#<figure class="rx-figure"><ol class="rx-flow" data-steps="2"><li><b>Only node</b><span>one step</span></li></ol><figcaption class="rx-caption">One node cannot honestly claim two steps.</figcaption></figure></section>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'flow|data-steps|count' \
  "rejects malformed component cardinality"

sabotage_copy flow-outside-figure
sed -i 's#</main>#<ol class="rx-flow" data-steps="1"><li><b>Unwrapped node</b><span>No figure owns this diagram.</span></li></ol></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'figure|diagram|contain' \
  "rejects a diagram component outside an rx-figure"

sabotage_copy implicit-flow-li
sed -i 's#</main>#<figure class="rx-figure"><ol class="rx-flow" data-steps="1"><li><b>One</b><li><b>Two</b></li></li></ol><figcaption class="rx-caption">Browser recovery would create an undeclared second direct flow node.</figcaption></figure></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'nested|flow|element|browser|parse' \
  "rejects list markup whose HTML recovery changes flow geometry"

sabotage_copy hidden-content
sed -i 's#<section id="background"#<section hidden id="background"#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'hidden|hiding|perceivable|authored' \
  "rejects authored hiding that makes the no-JavaScript report incomplete"

sabotage_copy nested-disclosure
sed -i 's#</main>#<details class="rx-more"><summary>Outer evidence layer</summary><details class="rx-more"><summary>Middle evidence layer</summary><details class="rx-more"><summary>Inner evidence layer</summary><p>Content nested too deeply for an ergonomic disclosure path.</p></details></details></details></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'details|disclosure|nested|depth' \
  "rejects disclosures nested more than two levels deep"

sabotage_copy redistributed-disclosure-summaries
sed -i 's#</main>#<details class="rx-more"><p>The first disclosure has no summary and cannot be opened reliably.</p></details><details class="rx-predict"><summary>First summary</summary><summary>Second summary</summary><p>The second disclosure cannot donate a summary to the first.</p></details></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'disclosure|summary|first|details' \
  "rejects summaries redistributed between no-JavaScript disclosures"

sabotage_copy short-feedback
sed -i '0,/<p>Correct because the immutable input no longer names the fetched revision.<\/p>/s//<p>No.<\/p>/' "$SABOTAGE"
validation_failure "$SABOTAGE" 'quiz|feedback|choice|eight' \
  "rejects quiz feedback too short to teach the governing rule"

sabotage_copy relocated-callout-label
sed -i 's#</main>#<aside class="rx-callout" data-role="evidence"><p>The first callout has no visible role label.</p></aside><aside class="rx-callout" data-role="caution"><p class="rx-callout-label">Caution</p><p class="rx-callout-label">Second label</p><p>The second callout cannot supply the first callout label.</p></aside></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'callout|label|direct|role' \
  "rejects callout labels relocated between component instances"

sabotage_copy relocated-branch-label
sed -i 's#</main>#<figure class="rx-figure"><div class="rx-branch"><p class="rx-branch-test">Does the snapshot still match?</p><ul class="rx-branch-arms" data-arms="2"><li class="rx-branch-arm">The first arm has no label.</li><li class="rx-branch-arm"><b class="rx-arm-label">yes</b><b class="rx-arm-label">no</b>The second arm cannot label both outcomes.</li></ul></div><figcaption class="rx-caption">Each alternative needs its own visible label rather than borrowed color.</figcaption></figure></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'branch|label|arm|direct' \
  "rejects branch labels redistributed between arms"

sabotage_copy short-caption
sed -i 's#</main>#<figure class="rx-figure"><p>Supporting prose.</p><figcaption class="rx-caption">Diagram.</figcaption></figure></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'caption|five|words|figure' \
  "rejects a label-like one-word figure caption"

sabotage_copy presentational-table-role
sed -i 's#</main>#<figure class="rx-figure"><div class="rx-scroll" role="region" tabindex="0" aria-label="Role sabotage table"><table class="rx-compare" role="presentation"><caption>The semantic table must retain its header relationships for readers.</caption><thead><tr><th scope="col">Claim</th><th scope="col">Evidence</th></tr></thead><tbody><tr><th scope="row">Semantics</th><td>Required</td></tr></tbody></table></div><figcaption class="rx-caption">A presentation role would erase relationships that the comparison needs.</figcaption></figure></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'role|tabindex|semantic|scroll' \
  "rejects a presentational role that erases comparison-table semantics"

sabotage_copy decoy-scroll-region
sed -i 's#</main>#<figure class="rx-figure"><div class="rx-scroll" aria-label="Malformed actual wrapper"><table class="rx-compare"><caption>The actual comparison sits in a wrapper that keyboard users cannot reach.</caption><thead><tr><th scope="col">Claim</th><th scope="col">Evidence</th></tr></thead><tbody><tr><th scope="row">Reachable</th><td>No</td></tr></tbody></table></div><div class="rx-scroll" role="region" tabindex="0" aria-label="Empty decoy region"></div><figcaption class="rx-caption">An unrelated named region cannot make the actual comparison keyboard reachable.</figcaption></figure></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'scroll|direct|named|region' \
  "rejects a decoy scroll region detached from the comparison"

sabotage_copy redistributed-table-captions
sed -i 's#</main>#<figure class="rx-figure"><div class="rx-scroll" role="region" tabindex="0" aria-label="First comparison"><table class="rx-compare"><thead><tr><th scope="col">First claim</th><th scope="col">Evidence</th></tr></thead><tbody><tr><th scope="row">Caption</th><td>Missing</td></tr></tbody></table></div><div class="rx-scroll" role="region" tabindex="0" aria-label="Second comparison"><table class="rx-compare"><caption>The second table has one caption too many for its own evidence.</caption><caption>An extra caption cannot satisfy the first table contract.</caption><thead><tr><th scope="col">Second claim</th><th scope="col">Evidence</th></tr></thead><tbody><tr><th scope="row">Caption</th><td>Duplicated</td></tr></tbody></table></div><figcaption class="rx-caption">Every comparison needs its own direct caption before its tabular evidence.</figcaption></figure></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'comparison|caption|table|direct' \
  "rejects captions redistributed between comparison tables"

sabotage_copy redistributed-timeline-states
sed -i 's#</main>#<figure class="rx-figure"><ol class="rx-timeline"><li data-state="done"><h4>First event</h4><p>The first event has no visible state.</p></li><li data-state="now"><h4>Second event</h4><p class="rx-state">now</p><p class="rx-state">duplicate</p></li></ol><figcaption class="rx-caption">Every timeline event needs its own visible state independent of color.</figcaption></figure></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'timeline|state|direct|visible' \
  "rejects visible states redistributed between timeline entries"

sabotage_copy paragraph-recovery
sed -i 's#</main>#<p>Before<div>Block content forces browser paragraph recovery.</div>After</p></main>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'nested|parse|body element|paragraph|HTML' \
  "rejects paragraph markup whose browser DOM would differ from the source stack"

sabotage_copy rotated-connector
sed -i '0,/<\/style>/s#</style>#.rx-flow > li::before { transform: rotate(90deg); }</style>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'style|template|rotate|connector' \
  "rejects rotated connector geometry"

sabotage_copy missing-reduced-motion
sed -i 's/prefers-reduced-motion: reduce/prefers-reduced-motion: no-preference/' "$SABOTAGE"
validation_failure "$SABOTAGE" 'style|template|motion' \
  "rejects a stylesheet with the reduced-motion contract removed"

sabotage_copy wrapping-code
sed -i '0,/white-space: pre;/s//white-space: pre-wrap;/' "$SABOTAGE"
validation_failure "$SABOTAGE" 'style|template|white-space|code' \
  "rejects a stylesheet that wraps exact code"

sabotage_copy horizontal-mobile-flow
sed -i '0,/--rx-flow-axis: column;/s//--rx-flow-axis: row;/' "$SABOTAGE"
validation_failure "$SABOTAGE" 'style|template|flow|mobile' \
  "rejects a stylesheet that makes horizontal flow the narrow default"

sabotage_copy network-script-api
sed -i '0,/<\/script>/s#</script>#fetch("https://outside.example");</script>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'script|template|fetch|network' \
  "rejects network APIs in the inline enhancement script"

# A code walk must be able to faithfully quote the exact markup and CSS a
# frontend diff removed, including substrings ("style=", "onclick=",
# "url(...)", "@import", "transform: rotate(...)") that the active-markup and
# CSS-behavior heuristics would otherwise mistake for live report content.
CODEWALK_FRAGMENT="$CASE_DIR/codewalk-fragment.html"
cat > "$CODEWALK_FRAGMENT" <<'HTML'
<section id="frontend-diff" aria-labelledby="frontend-diff-h" data-layer="receipts" data-objective="obj-2 obj-3">
  <h2 id="frontend-diff-h">The frontend diff moved presentation into the stylesheet</h2>
  <p class="rx-claim">Quoting the removed markup and CSS verbatim shows exactly what the change deleted.</p>
  <figure class="rx-figure rx-codewalk" id="cw-diff" data-objective="obj-3">
    <div class="rx-scroll" role="region" tabindex="0" aria-label="Removed inline presentation">
      <pre class="rx-code" data-lang="html"><code>&lt;button style="color:red" onclick="handleClick()"&gt;Send&lt;/button&gt;
<mark class="rx-hl" id="cw-diff-h1">.rx-old { background: url(sprite.png); transform: rotate(45deg); }</mark>
@import "legacy.css";</code></pre>
    </div>
    <ol class="rx-notes">
      <li class="rx-note" data-hl="cw-diff-h1"><a href="#cw-diff-h1">Removed rule</a> — the deleted selector no longer loads a sprite or rotates its icon.</li>
    </ol>
    <figcaption class="rx-caption">The report quotes the exact deleted markup and CSS without executing or loading any of it.</figcaption>
  </figure>
</section>
HTML

ESCAPED_CODEWALK="$CASE_DIR/escaped-codewalk.html"
splice_before_main_close "$VALID_REPORT" "$CODEWALK_FRAGMENT" "$ESCAPED_CODEWALK"
capture "$ALLOD" pr _validate-report "$ESCAPED_CODEWALK" "$SNAPSHOT" codex
assert_success \
  "accepts a code walk that faithfully quotes style=, onclick=, url(), @import, and transform: rotate() as escaped code"

# The exclusion is scoped to <pre>/<code> content, not prose in general: the
# same dangerous substring outside a code element must still fail.
LIVE_FRAGMENT="$CASE_DIR/live-fragment.html"
cat > "$LIVE_FRAGMENT" <<'HTML'
<section id="prose-leak" aria-labelledby="prose-leak-h" data-layer="receipts" data-objective="obj-2">
  <h2 id="prose-leak-h">A prose leak is not a code quotation</h2>
  <p class="rx-claim">Prose describing behavior is not the same as quoting exact removed code.</p>
  <p>The removed rule used url(https://outside.example/track.gif) to load a tracking pixel.</p>
</section>
HTML
sabotage_copy prose-network-url
splice_before_main_close "$SABOTAGE" "$LIVE_FRAGMENT" "$CASE_DIR/prose-network-url-spliced.html"
mv "$CASE_DIR/prose-network-url-spliced.html" "$SABOTAGE"
validation_failure "$SABOTAGE" 'style|css|import|template|network|resource' \
  "rejects a network-capable CSS function named in prose outside any code element"

# A live attribute nested inside a code block is a real, rendered attribute
# on a real element — not quoted text — and must still fail.
NESTED_LIVE_FRAGMENT="$CASE_DIR/nested-live-fragment.html"
cat > "$NESTED_LIVE_FRAGMENT" <<'HTML'
<figure class="rx-figure rx-codewalk" id="cw-evil" data-objective="obj-1">
  <div class="rx-scroll" role="region" tabindex="0" aria-label="Evil nested style">
    <pre class="rx-code" data-lang="html"><code><mark class="rx-hl" id="cw-evil-h1" style="color:red">live text</mark></code></pre>
  </div>
  <ol class="rx-notes">
    <li class="rx-note" data-hl="cw-evil-h1"><a href="#cw-evil-h1">Note</a> — the highlighted line carries a live attribute, not a quoted example.</li>
  </ol>
  <figcaption class="rx-caption">A real attribute nested inside a code block must still fail even though it sits inside a pre element.</figcaption>
</figure>
HTML
sabotage_copy live-style-inside-code
splice_before_main_close "$SABOTAGE" "$NESTED_LIVE_FRAGMENT" "$CASE_DIR/live-style-inside-code-spliced.html"
mv "$CASE_DIR/live-style-inside-code-spliced.html" "$SABOTAGE"
validation_failure "$SABOTAGE" 'style|attribute|HTML|tag|complete' \
  "rejects a live style attribute on a real element nested inside a code block"

# UTF-8 safety: one sabotage per forbidden class, plus a normal-Unicode
# acceptance case so the check is not merely rejecting all non-ASCII text.
utf8_sabotage() {
  local name="$1" bytes="$2" description="$3"
  local fragment="$CASE_DIR/utf8-$name-fragment.html"
  printf '<p>Embedded control byte sequence: %b end.</p>\n' "$bytes" > "$fragment"
  sabotage_copy "utf8-$name"
  splice_before_main_close "$SABOTAGE" "$fragment" "$CASE_DIR/utf8-$name-spliced.html"
  mv "$CASE_DIR/utf8-$name-spliced.html" "$SABOTAGE"
  validation_failure "$SABOTAGE" 'utf-8|control|valid' "$description"
}

utf8_sabotage c1-control '\xc2\x80' \
  "rejects an embedded C1 control byte (U+0080)"
utf8_sabotage bom '\xef\xbb\xbf' \
  "rejects an embedded byte-order mark"
utf8_sabotage line-separator '\xe2\x80\xa8' \
  "rejects an embedded U+2028 line separator"
utf8_sabotage paragraph-separator '\xe2\x80\xa9' \
  "rejects an embedded U+2029 paragraph separator"
utf8_sabotage zero-width-space '\xe2\x80\x8b' \
  "rejects an embedded U+200B zero-width space"
utf8_sabotage right-to-left-mark '\xe2\x80\x8f' \
  "rejects an embedded U+200F right-to-left mark"
utf8_sabotage bidi-embedding '\xe2\x80\xaa' \
  "rejects an embedded U+202A left-to-right embedding control"
utf8_sabotage bidi-isolate '\xe2\x81\xa6' \
  "rejects an embedded U+2066 left-to-right isolate control"

UNICODE_FRAGMENT="$CASE_DIR/unicode-fragment.html"
printf '<p>Ordinary Unicode prose renders fine: caf\xc3\xa9, \xe6\x97\xa5\xe6\x9c\xac\xe8\xaa\x9e, an em dash\xe2\x80\x94like this, and an emoji \xf0\x9f\x8e\x89.</p>\n' \
  > "$UNICODE_FRAGMENT"
UNICODE_REPORT="$CASE_DIR/unicode-report.html"
splice_before_main_close "$VALID_REPORT" "$UNICODE_FRAGMENT" "$UNICODE_REPORT"
capture "$ALLOD" pr _validate-report "$UNICODE_REPORT" "$SNAPSHOT" codex
assert_success "accepts ordinary Unicode prose (accents, CJK, em dash, emoji)"

# v2 grammar: reading-cost line (E21), layers (E22), objectives block (E23),
# objective coverage (E24), quiz v2 additions (E10), and the slop linter
# (E25 errors, W13 warning). Each probe breaks exactly one new contract in an
# otherwise valid report.

# One structurally complete quiz item for count/balance sabotage: the given
# answer letter is correct, everything else satisfies the item grammar, and
# the prose is parametrized by item number so appended copies cannot trip the
# repeated-trigram detector on their own.
write_extra_quiz_item_fragment() {
  local fragment="$1" n="$2" correct_letter="$3"
  local letter attrs
  {
    printf '<article class="rx-quiz-item" id="q%s" data-concept="staged-report-boundary" data-objective="obj-1">\n' "$n"
    printf '<h3>%s. Which appended question number %s keeps the retrieval shape lawful?</h3>\n' "$n" "$n"
    printf '<ul class="rx-choices">\n'
    for letter in A B C D; do
      if [[ "$letter" == "$correct_letter" ]]; then
        attrs='data-correct="true"'
        printf '<li><details class="rx-choice" name="q%s" %s><summary>%s. Synthetic key %s for appended item %s</summary><p>Correct. Appended item %s keeps its explanation specific to this synthetic retrieval question.</p></details></li>\n' \
          "$n" "$attrs" "$letter" "$letter" "$n" "$n"
      else
        attrs="data-correct=\"false\" data-misconception=\"distractor $letter of item $n\""
        printf '<li><details class="rx-choice" name="q%s" %s><summary>%s. Synthetic distractor %s for appended item %s</summary><p>Not quite. Distractor %s of item %s exists only to keep the quiz shape lawful.</p></details></li>\n' \
          "$n" "$attrs" "$letter" "$letter" "$n" "$letter" "$n"
      fi
    done
    printf '</ul>\n'
    printf '<p class="rx-quiz-result" aria-live="polite"></p>\n'
    printf '</article>\n'
  } > "$fragment"
}

# Drop one quiz item (article open tag through its closing tag) from a report.
remove_quiz_item() {
  local file="$1" quiz_id="$2"
  local trimmed="$file.trimmed"
  awk -v id="id=\"$quiz_id\"" '
    index($0, "<article class=\"rx-quiz-item\"") && index($0, id) { skip = 1 }
    skip { if (index($0, "</article>")) skip = 0; next }
    { print }
  ' "$file" > "$trimmed"
  mv "$trimmed" "$file"
}

sabotage_copy cost-missing
sed -i '/<p class="rx-cost">/d' "$SABOTAGE"
validation_failure "$SABOTAGE" 'rx-cost|reading-cost' \
  "rejects a masthead with no reading-cost line (E21)"

sabotage_copy cost-before-lede
sed -i 's#<p class="rx-lede">#<p class="rx-cost">Summary: 1 minute.</p>\n<p class="rx-lede">#' "$SABOTAGE"
sed -i '/Full mechanism and quiz: 8 minutes/d' "$SABOTAGE"
validation_failure "$SABOTAGE" 'rx-cost.*after|after p.rx-lede' \
  "rejects a reading-cost line placed before the lede (E21)"

sabotage_copy cost-outside-masthead
sed -i 's#<p>The complete diff and surrounding code supply the evidence for this explanation.</p>#&\n    <p class="rx-cost">Concepts: 3 minutes.</p>#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'only inside the masthead' \
  "rejects a second reading-cost line outside the masthead (E21)"

sabotage_copy cost-without-minutes
sed -i 's#Summary: 1 minute. Concepts: 3 minutes. Full mechanism and quiz: 8 minutes.#Summary: fast. Concepts: quick. Full mechanism: brisk.#' "$SABOTAGE"
validation_failure "$SABOTAGE" "minute" \
  "rejects a reading-cost line that never states minutes (E21)"

sabotage_copy layer-missing
sed -i 's# data-layer="concept" data-objective="obj-1 obj-3"# data-objective="obj-1 obj-3"#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'missing its data-layer' \
  "rejects a main section with no data-layer (E22)"

sabotage_copy layer-unknown
sed -i 's#data-layer="concept" data-objective="obj-1 obj-3"#data-layer="overview" data-objective="obj-1 obj-3"#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'unknown data-layer' \
  "rejects an unknown data-layer value (E22)"

sabotage_copy layer-order
sed -i 's#data-layer="concept" data-objective="obj-1 obj-3"#data-layer="receipts" data-objective="obj-1 obj-3"#' "$SABOTAGE"
sed -i 's#data-layer="receipts" data-objective="obj-1 obj-2 obj-3"#data-layer="mechanism" data-objective="obj-1 obj-2 obj-3"#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'non-decreasing' \
  "rejects layers out of concept, mechanism, receipts order (E22)"

sabotage_copy layer-no-concept
sed -i 's#data-layer="concept"#data-layer="mechanism"#g' "$SABOTAGE"
validation_failure "$SABOTAGE" 'at least one|concept' \
  "rejects a main with no concept-layer section (E22)"

LAYER_MISPLACED_FRAGMENT="$CASE_DIR/layer-misplaced-fragment.html"
printf '<p data-layer="concept">A stray layered paragraph outside any section.</p>\n' \
  > "$LAYER_MISPLACED_FRAGMENT"
sabotage_copy layer-misplaced
splice_before_main_close "$SABOTAGE" "$LAYER_MISPLACED_FRAGMENT" "$CASE_DIR/layer-misplaced-spliced.html"
mv "$CASE_DIR/layer-misplaced-spliced.html" "$SABOTAGE"
validation_failure "$SABOTAGE" 'direct section children' \
  "rejects data-layer on anything but a direct main section (E22)"

sabotage_copy objectives-section-missing
sed -i 's#<section id="objectives" aria-labelledby="objectives-h" data-layer="concept">#<section id="goals" aria-labelledby="objectives-h" data-layer="concept">#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'objectives' \
  "rejects a main that does not open with the #objectives section (E23)"

sabotage_copy objective-bad-id
sed -i 's#<li id="obj-2">#<li id="objective-2">#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'obj-N' \
  "rejects an objective id outside the obj-N shape (E23)"

sabotage_copy objective-ids-out-of-order
sed -i 's#<li id="obj-1">#<li id="obj-9">#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'unique and ascending' \
  "rejects objective ids out of ascending order (E23)"

sabotage_copy objectives-duplicate-list
sed -i 's#<li id="obj-3">You can trace every provenance field back to the immutable snapshot.</li>#&</ol><ol class="rx-objectives">#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'exactly one ol.rx-objectives' \
  "rejects a second rx-objectives list inside #objectives (E23)"

STRAY_OBJECTIVES_FRAGMENT="$CASE_DIR/stray-objectives-fragment.html"
printf '<ol class="rx-objectives"><li id="obj-8">A stray objectives list entry outside the block.</li></ol>\n' \
  > "$STRAY_OBJECTIVES_FRAGMENT"
sabotage_copy objectives-list-outside-block
splice_before_main_close "$SABOTAGE" "$STRAY_OBJECTIVES_FRAGMENT" "$CASE_DIR/stray-objectives-spliced.html"
mv "$CASE_DIR/stray-objectives-spliced.html" "$SABOTAGE"
validation_failure "$SABOTAGE" "allowed only inside '#objectives'|rx-objectives" \
  "rejects an rx-objectives list outside #objectives (E23)"

sabotage_copy objective-ref-unknown
sed -i 's#data-objective="obj-1 obj-3"#data-objective="obj-1 obj-9"#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'unknown objective' \
  "rejects a data-objective naming an id the block never declares (E24)"

sabotage_copy section-without-objective
sed -i 's# data-objective="obj-1 obj-3"##' "$SABOTAGE"
validation_failure "$SABOTAGE" 'must declare the objectives it serves' \
  "rejects a main section with no data-objective (E24)"

UNTAGGED_FIGURE_FRAGMENT="$CASE_DIR/untagged-figure-fragment.html"
cat > "$UNTAGGED_FIGURE_FRAGMENT" <<'HTML'
<figure class="rx-figure"><p>A quiet supporting exhibit with no declared objective.</p><figcaption class="rx-caption">This figure omits the objective tag it owes the checker.</figcaption></figure>
HTML
sabotage_copy figure-without-objective
splice_before_main_close "$SABOTAGE" "$UNTAGGED_FIGURE_FRAGMENT" "$CASE_DIR/figure-without-objective-spliced.html"
mv "$CASE_DIR/figure-without-objective-spliced.html" "$SABOTAGE"
validation_failure "$SABOTAGE" 'figure.*data-objective|declare the objectives' \
  "rejects an rx-figure with no data-objective (E24)"

sabotage_copy objective-without-section-claim
sed -i 's#<li id="obj-3">You can trace every provenance field back to the immutable snapshot.</li>#&\n      <li id="obj-4">You can audit the coverage checker from its own reports.</li>#' "$SABOTAGE"
sed -i 's#id="q5" data-concept="staged-report-boundary" data-objective="obj-2"#id="q5" data-concept="staged-report-boundary" data-objective="obj-4"#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'not claimed by any main section' \
  "rejects an objective no section claims (E24)"

sabotage_copy objective-without-quiz-claim
sed -i 's#<li id="obj-3">You can trace every provenance field back to the immutable snapshot.</li>#&\n      <li id="obj-4">You can audit the coverage checker from its own reports.</li>#' "$SABOTAGE"
sed -i 's#data-objective="obj-1 obj-3"#data-objective="obj-1 obj-3 obj-4"#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'not tested by any quiz item' \
  "rejects an objective no quiz item tests (E24)"

sabotage_copy quiz-missing-concept
sed -i 's# id="q1" data-concept="snapshot-immutability"# id="q1"#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'data-concept' \
  "rejects a quiz item with no data-concept (E10)"

sabotage_copy quiz-multi-objective
sed -i 's#id="q1" data-concept="snapshot-immutability" data-objective="obj-1"#id="q1" data-concept="snapshot-immutability" data-objective="obj-1 obj-2"#' "$SABOTAGE"
validation_failure "$SABOTAGE" 'exactly one objective id' \
  "rejects a quiz item claiming more than one objective (E10)"

EXTRA_QUIZ_FRAGMENT="$CASE_DIR/extra-quiz-item-6.html"
write_extra_quiz_item_fragment "$EXTRA_QUIZ_FRAGMENT" 6 A
sabotage_copy quiz-letter-thrice
splice_before_main_close "$SABOTAGE" "$EXTRA_QUIZ_FRAGMENT" "$CASE_DIR/quiz-letter-thrice-spliced.html"
mv "$CASE_DIR/quiz-letter-thrice-spliced.html" "$SABOTAGE"
validation_failure "$SABOTAGE" 'answer position A.*more than twice' \
  "rejects one answer letter correct three times across the quiz (E10)"

sabotage_copy quiz-two-items
remove_quiz_item "$SABOTAGE" q3
remove_quiz_item "$SABOTAGE" q4
remove_quiz_item "$SABOTAGE" q5
validation_failure "$SABOTAGE" 'between three and seven' \
  "rejects a report with only two quiz items (E10)"

sabotage_copy quiz-eight-items
for extra_item_n in 6 7 8; do
  case "$extra_item_n" in
    6) extra_item_letter=B ;;
    7) extra_item_letter=C ;;
    8) extra_item_letter=D ;;
  esac
  write_extra_quiz_item_fragment "$CASE_DIR/extra-quiz-item-$extra_item_n.html" \
    "$extra_item_n" "$extra_item_letter"
  splice_before_main_close "$SABOTAGE" "$CASE_DIR/extra-quiz-item-$extra_item_n.html" \
    "$CASE_DIR/quiz-eight-items-spliced.html"
  mv "$CASE_DIR/quiz-eight-items-spliced.html" "$SABOTAGE"
done
validation_failure "$SABOTAGE" 'between three and seven' \
  "rejects a report with eight quiz items (E10)"

BANNED_PROSE_FRAGMENT="$CASE_DIR/banned-prose-fragment.html"
printf '<p>These helpers utilize the shared cache aggressively.</p>\n' > "$BANNED_PROSE_FRAGMENT"
sabotage_copy slop-banned-vocabulary
splice_before_main_close "$SABOTAGE" "$BANNED_PROSE_FRAGMENT" "$CASE_DIR/slop-banned-spliced.html"
mv "$CASE_DIR/slop-banned-spliced.html" "$SABOTAGE"
validation_failure "$SABOTAGE" 'banned vocabulary' \
  "rejects AI-slop vocabulary in visible prose (E25)"

# The same banned word quoted inside <code> is evidence, not prose, and the
# linter strips quoted code before matching — it must not fire.
BANNED_CODE_FRAGMENT="$CASE_DIR/banned-code-fragment.html"
printf '<p>The linter once flagged <code>utilize</code> inside quoted identifiers.</p>\n' \
  > "$BANNED_CODE_FRAGMENT"
BANNED_CODE_REPORT="$CASE_DIR/banned-code-report.html"
splice_before_main_close "$VALID_REPORT" "$BANNED_CODE_FRAGMENT" "$BANNED_CODE_REPORT"
capture "$ALLOD" pr _validate-report "$BANNED_CODE_REPORT" "$SNAPSHOT" codex
assert_success "accepts a banned word quoted inside a code element; the slop linter reads prose only"

TRIGRAM_FRAGMENT="$CASE_DIR/trigram-fragment.html"
printf '<p>The cache holds every entry. The cache holds one shard. The cache holds stale rows. The cache holds warm keys.</p>\n' \
  > "$TRIGRAM_FRAGMENT"
sabotage_copy slop-repeated-trigram
splice_before_main_close "$SABOTAGE" "$TRIGRAM_FRAGMENT" "$CASE_DIR/slop-trigram-spliced.html"
mv "$CASE_DIR/slop-trigram-spliced.html" "$SABOTAGE"
validation_failure "$SABOTAGE" 'sentence opening.*repeats' \
  "rejects the same sentence-opening trigram repeated four times (E25)"

# Hedge density is a warning, not an error: the report still validates, and
# W13 names the density in the diagnostic stream.
HEDGE_FRAGMENT="$CASE_DIR/hedge-fragment.html"
printf '<p>This paragraph hedges on purpose for the density check. The retry may stall. The cache might drift. The lock could starve. Perhaps the queue wraps. The clock is arguably wrong. The index will likely rot. The mirror may lag. The probe might misfire. The scan could skip. The write may block. The read might tear. The sync could stall.</p>\n' \
  > "$HEDGE_FRAGMENT"
HEDGE_REPORT="$CASE_DIR/hedge-report.html"
splice_before_main_close "$VALID_REPORT" "$HEDGE_FRAGMENT" "$HEDGE_REPORT"
capture "$ALLOD" pr _validate-report "$HEDGE_REPORT" "$SNAPSHOT" codex
assert_success "hedge-dense prose still validates; density is advisory"
assert_contains "$CAPTURE_OUTPUT" "W13" \
  "warns with W13 when hedges exceed four per five hundred words"
assert_contains "$CAPTURE_OUTPUT" "commit to what the evidence supports" \
  "the hedge warning tells the author what to do instead"

finish_tests "PR explanation report validator"
