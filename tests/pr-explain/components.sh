#!/usr/bin/env bash
# shellcheck source=/dev/null
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_case components

emit_and_compare() {
  local kind="$1" source="$2" description="$3"
  local emitted="$CASE_DIR/emitted-$kind"
  set +e
  "$ALLOD" pr _emit-asset "$kind" > "$emitted" 2> "$CASE_DIR/emitted-$kind.err"
  local status=$?
  set -e
  if [[ "$status" -ne 0 ]]; then
    fail "$description" "asset emission failed:" "$(cat "$CASE_DIR/emitted-$kind.err")"
  elif cmp -s "$source" "$emitted"; then
    pass "$description"
  else
    fail "$description" "emitted asset differs from: $source"
  fi
}

CSS="$ROOT/pr-explain/report.css"
JS="$ROOT/pr-explain/report.js"
PROMPT="$ROOT/pr-explain/prompt.md"
GALLERY="$ROOT/pr-explain/component-gallery.html"

emit_and_compare css "$CSS" "the installed CLI embeds the checked stylesheet byte-for-byte"
emit_and_compare js "$JS" "the installed CLI embeds the checked enhancement script byte-for-byte"
emit_and_compare prompt "$PROMPT" "the installed CLI embeds the checked common prompt byte-for-byte"
emit_and_compare gallery "$GALLERY" "the installed CLI embeds the checked component gallery byte-for-byte"

assert_contains "$(cat "$PROMPT")" "information transfer" \
  "the prompt treats explanation as information transfer"
assert_contains "$(cat "$PROMPT")" "human brains" \
  "the prompt explicitly asks the runner to think about human brains"
for learning_term in "progressive disclosure" signaling chunking "concrete examples" \
  "dual coding" "active retrieval" misconception; do
  if grep -Fqi "$learning_term" "$PROMPT"; then
    pass "the prompt requires $learning_term"
  else
    fail "the prompt requires $learning_term" "missing from: $PROMPT"
  fi
done
assert_contains "$(cat "$PROMPT")" "complete diff" \
  "the prompt requires reading the complete diff"
assert_contains "$(cat "$PROMPT")" "surrounding" \
  "the prompt requires investigating surrounding code"
assert_contains "$(cat "$PROMPT")" "review" \
  "the prompt requires investigating pull request reviews"
assert_contains "$(cat "$PROMPT")" "Facts" \
  "the prompt separates facts from interpretation"
assert_contains "$(cat "$PROMPT")" "not decoration" \
  "the prompt allows pictures and motion only when they reduce cognitive work"

assert_contains "$(cat "$CSS")" '.rx-flow > li + li::before' \
  "flow connectors are pseudo-elements owned by the following node"
assert_contains "$(cat "$CSS")" '--rx-flow-axis: column' \
  "the narrow flow default is vertical"
assert_contains "$(cat "$CSS")" '--rx-flow-glyph: "\2193"' \
  "the narrow flow authors a vertical down-arrow glyph"
assert_contains "$(cat "$CSS")" '--rx-flow-glyph: "\2192"' \
  "the wide flow authors a horizontal right-arrow glyph"
assert_contains "$(cat "$CSS")" '@container rx-figure' \
  "component width rather than viewport width controls flow layout"
assert_contains "$(cat "$CSS")" 'block-size: var(--rx-flow-gap)' \
  "one flow-gap variable sizes both connector and node spacing"

if grep -Eiq '(transform[[:space:]]*:[^;]*(rotate|matrix)|(^|[;{[:space:]])rotate[[:space:]]*:|writing-mode[[:space:]]*:)' "$CSS"; then
  fail "the component sheet contains no rotated connector geometry" \
    "$(grep -Ein '(transform[[:space:]]*:[^;]*(rotate|matrix)|rotate[[:space:]]*:|writing-mode[[:space:]]*:)' "$CSS")"
else
  pass "the component sheet contains no rotated connector geometry"
fi
if grep -Eq 'rx-(arrow|connector)' "$CSS" "$GALLERY"; then
  fail "the vocabulary contains no sibling connector boxes" \
    "$(grep -En 'rx-(arrow|connector)' "$CSS" "$GALLERY")"
else
  pass "the vocabulary contains no sibling connector boxes"
fi

assert_contains "$(cat "$CSS")" '.rx-code > code' \
  "the stylesheet targets exact code blocks explicitly"
assert_contains "$(cat "$CSS")" 'white-space: pre;' \
  "exact code preserves whitespace"
assert_contains "$(cat "$CSS")" '@media (prefers-reduced-motion: reduce)' \
  "the stylesheet includes a reduced-motion fallback"
assert_contains "$(cat "$CSS")" '@media print' \
  "the component vocabulary includes print behavior"
assert_contains "$(cat "$CSS")" '@media (prefers-color-scheme: dark)' \
  "the component vocabulary includes a dark color scheme"
assert_contains "$(cat "$CSS")" ':focus-visible' \
  "the component vocabulary gives keyboard focus a visible treatment"

if grep -Eiq '@keyframes|(^|[;{[:space:]])animation(-[a-z]+)?[[:space:]]*:' "$CSS"; then
  fail "the stylesheet contains no autoplay or decorative animation" \
    "$(grep -Ein '@keyframes|animation(-[a-z]+)?[[:space:]]*:' "$CSS")"
else
  pass "the stylesheet contains no autoplay or decorative animation"
fi

for forbidden_api in fetch XMLHttpRequest WebSocket EventSource sendBeacon \
  setInterval localStorage sessionStorage indexedDB document.cookie 'eval(' 'new Function'; do
  if grep -Fq "$forbidden_api" "$JS"; then
    fail "the enhancement script avoids $forbidden_api" "found in: $JS"
  else
    pass "the enhancement script avoids $forbidden_api"
  fi
done
assert_contains "$(cat "$JS")" 'prefers-reduced-motion: reduce' \
  "JavaScript enhancements honor reduced-motion preference"

for component in rx-summary rx-flow rx-branch rx-lanes rx-sequence rx-codewalk \
  rx-callout rx-timeline rx-compare rx-quiz-item rx-provenance; do
  if grep -Fq "class=\"$component" "$GALLERY" || grep -Fq " $component" "$GALLERY"; then
    pass "the gallery demonstrates $component"
  else
    fail "the gallery demonstrates $component" "missing from: $GALLERY"
  fi
done
for width in 320 375 768 1280; do
  assert_contains "$(cat "$GALLERY")" "data-width=\"$width\"" \
    "the gallery offers a ${width}px flow-inspection frame"
done
assert_contains "$(cat "$GALLERY")" 'data-steps="7"' \
  "the gallery demonstrates the always-stacked long-flow case"
assert_contains "$(cat "$GALLERY")" 'dir="rtl"' \
  "the gallery demonstrates right-to-left connector glyph behavior"
assert_contains "$(cat "$GALLERY")" 'data-align="columns"' \
  "the gallery demonstrates aligned parallel lanes"
if grep -Eiq '<(button|input|select)([[:space:]>])' "$GALLERY"; then
  fail "the no-JavaScript gallery authors no dead controls" \
    "$(grep -Ein '<(button|input|select)([[:space:]>])' "$GALLERY")"
else
  pass "the no-JavaScript gallery authors no dead controls"
fi
assert_contains "$(cat "$GALLERY")" '<details class="rx-choice"' \
  "quiz answers remain usable without JavaScript"
assert_contains "$(cat "$GALLERY")" '<ol class="rx-seq-steps"' \
  "sequence information remains ordered and visible without JavaScript"

gallery_snapshot="$CASE_DIR/gallery-snapshot.json"
jq -n '{
  schema_version: 1,
  pull_request: {
    number: 42,
    url: "https://forge.example/example/widget/pulls/42",
    title: "Component gallery",
    body: "Visual regression fixture"
  },
  base: {
    repository: {owner: "example", name: "widget", full_name: "example/widget", clone_url: "https://forge.example/example/widget.git"},
    ref: "master", sha: "1111111111111111111111111111111111111111"
  },
  head: {
    repository: {owner: "example", name: "widget", full_name: "example/widget", clone_url: "https://forge.example/example/widget.git"},
    ref: "gallery", sha: "2222222222222222222222222222222222222222"
  }
}' > "$gallery_snapshot"
capture "$ALLOD" pr _validate-report "$GALLERY" "$gallery_snapshot" codex
assert_success "the complete component gallery passes the production validator"

finish_tests "PR explanation components"
