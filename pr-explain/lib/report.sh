# shellcheck shell=bash
# The report driver: extracting a named asset region from an assembled
# report, stripping quoted code before a structural scan, streaming every
# markup tag with its line number, and pr_explain_validate_report, which
# calls the lib/report-validators.sh predicates and lib/fragments.sh
# schema checks over the finished document. Depends on lib/common.sh,
# lib/fragments.sh, and lib/report-validators.sh, and is sourced last.

pr_explain_extract_asset() {
  local report="$1" opening="$2" closing="$3" output="$4"
  awk -v opening="$opening" -v closing="$closing" '
    $0 == opening { inside = 1; next }
    inside && $0 == closing { exit }
    inside { print }
  ' "$report" >"$output"
}

# Drop text content quoted inside <pre>/<code> (a code walk faithfully
# quoting a frontend diff, e.g. `style="..."`, `url(...)`, `transform:
# rotate(...)`) before scanning for live/active HTML, CSS, or JS structure.
# Tags themselves are always kept, so a real element nested inside pre/code
# (unescaped markup smuggled past the tag vocabulary check) still shows up.
# The canonical template style/script blocks are skipped verbatim — already
# byte-compared elsewhere — so stray '<'/'>' in real CSS/JS cannot desync the
# tag scan that follows them.
pr_explain_strip_quoted_code() {
  awk '
    $0 == "<style id=\"rx-template-css\">" { in_style = 1; next }
    in_style && $0 == "</style>" { in_style = 0; next }
    in_style { next }
    $0 == "<script id=\"rx-template-js\">" { in_script = 1; next }
    in_script && $0 == "</script>" { in_script = 0; next }
    in_script { next }
    {
      rest = $0
      out = ""
      while (match(rest, /<[^>]*>/)) {
        textpart = substr(rest, 1, RSTART - 1)
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        if (!(pre_depth > 0 || code_depth > 0)) out = out textpart
        out = out tag
        name = tag
        closing = (name ~ /^<\//)
        sub(/^<\/?/, "", name)
        sub(/[[:space:]>].*$/, "", name)
        name = tolower(name)
        if (name == "pre") {
          if (closing) { if (pre_depth > 0) pre_depth-- } else pre_depth++
        } else if (name == "code") {
          if (closing) { if (code_depth > 0) code_depth-- } else code_depth++
        }
      }
      if (!(pre_depth > 0 || code_depth > 0)) out = out rest
      print out
    }
  ' "$1"
}

# Emit every markup tag in the document as "line<TAB>tag", excluding all text
# content and the canonical template style/script payloads (their own open and
# close tags are kept). Identifier and reference accounting runs on this
# stream, so an attribute-shaped string in quoted code or prose (for example
# `PID=""` in a shell codewalk) never counts as markup.
pr_explain_report_tags() {
  awk '
    $0 == "<style id=\"rx-template-css\">" { printf "%d\t%s\n", NR, $0; in_style = 1; next }
    in_style && $0 == "</style>" { printf "%d\t%s\n", NR, $0; in_style = 0; next }
    in_style { next }
    $0 == "<script id=\"rx-template-js\">" { printf "%d\t%s\n", NR, $0; in_script = 1; next }
    in_script && $0 == "</script>" { printf "%d\t%s\n", NR, $0; in_script = 0; next }
    in_script { next }
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        printf "%d\t%s\n", NR, substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
      }
    }
  ' "$1"
}

pr_explain_validate_report() {
  local report="$1" snapshot_file="$2" runner="$3"
  local repository number pr_url pr_title base_sha head_sha
  local repository_html pr_url_html pr_title_html expected_css expected_js actual_css actual_js body_html main_html visible_text report_live
  local expected_gallery provenance_html gallery_report=false
  local count style_count script_count h1_count main_count figure_count caption_count
  local h2_count claim_count main_h2_count main_claim_count
  local quiz_count choice_count correct_count false_count misconception_count
  local code_count scroll_count callout_count callout_label_count timeline_count state_count
  local branch_count branch_test_count branch_arms_count branch_arm_count arm_label_count
  local lanes_count lane_count lane_label_count sequence_count seq_steps_count seq_step_count seq_title_count
  local codewalk_count notes_count note_count highlight_count role_count table_count compare_count
  local details_count disclosure_count summary_count stat_group_count stat_count card_count
  local tabindex_count preview_scroll_count
  local class_value class_name id ref value role state field report_tags line_no
  local generated_date slop_prose

  PR_EXPLAIN_VALIDATION_ERRORS=()
  PR_EXPLAIN_VALIDATION_WARNINGS=()

  [[ -f "$report" && -s "$report" ]] || {
    pr_explain_validation_error E00 "staged report is missing or empty"
    printf 'allod: validation error [E00]: staged report is missing or empty\n' >&2
    return 1
  }
  [[ -r "$snapshot_file" ]] || {
    pr_explain_validation_error E00 "snapshot is not readable"
    printf 'allod: validation error [E00]: snapshot is not readable\n' >&2
    return 1
  }
  pr_explain_is_safe_utf8 "$report" ||
    pr_explain_validation_error E2 "report must be valid UTF-8 without forbidden control bytes"

  if [[ $# -eq 9 ]]; then
    # Production callers retain these values in the parent shell, outside the
    # runner's writable staging directory. The three-argument form below keeps
    # the standalone regression validator convenient.
    repository="$4"
    number="$5"
    pr_url="$6"
    pr_title="$7"
    base_sha="$8"
    head_sha="$9"
  else
    [[ $# -eq 3 ]] || {
      pr_explain_validation_error E00 "validator received an invalid expected-provenance contract"
      repository="" number="" pr_url="" pr_title="" base_sha="" head_sha=""
    }
    repository=$(jq -er '.base.repository.full_name' "$snapshot_file" 2>/dev/null) ||
      pr_explain_validation_error E18 "snapshot repository is missing"
    number=$(jq -er '.pull_request.number' "$snapshot_file" 2>/dev/null) ||
      pr_explain_validation_error E18 "snapshot PR number is missing"
    pr_url=$(jq -er '.pull_request.url' "$snapshot_file" 2>/dev/null) ||
      pr_explain_validation_error E18 "snapshot PR URL is missing"
    pr_title=$(jq -er '.pull_request.title' "$snapshot_file" 2>/dev/null) ||
      pr_explain_validation_error E18 "snapshot PR title is missing"
    base_sha=$(jq -er '.base.sha' "$snapshot_file" 2>/dev/null) ||
      pr_explain_validation_error E18 "snapshot base SHA is missing"
    head_sha=$(jq -er '.head.sha' "$snapshot_file" 2>/dev/null) ||
      pr_explain_validation_error E18 "snapshot head SHA is missing"
  fi

  repository_html=$(printf '%s' "${repository:-}" | pr_explain_html_escape)
  pr_url_html=$(printf '%s' "${pr_url:-}" | pr_explain_html_escape)
  pr_title_html=$(printf '%s' "${pr_title:-}" | tr '\r\n' '  ' | pr_explain_html_escape)

  if [[ "$(wc -c <"$report")" -gt 1572864 ]]; then
    pr_explain_validation_error E20 "report exceeds the 1.5 MiB limit"
  elif [[ "$(wc -c <"$report")" -gt 256000 ]]; then
    pr_explain_validation_warning W12 "report exceeds 250 KB; check whether every part earns its space"
  fi

  make_temp_file expected_css
  make_temp_file expected_js
  make_temp_file actual_css
  make_temp_file actual_js
  make_temp_file body_html
  make_temp_file main_html
  make_temp_file visible_text
  make_temp_file report_live
  pr_explain_emit_css >"$expected_css"
  pr_explain_emit_js >"$expected_js"
  pr_explain_extract_asset "$report" '<style id="rx-template-css">' '</style>' "$actual_css"
  pr_explain_extract_asset "$report" '<script id="rx-template-js">' '</script>' "$actual_js"
  if ! awk '
    $0 == "<body>" { body = 1; next }
    body && $0 == "<script id=\"rx-template-js\">" { found = 1; exit }
    body { print }
    END { if (!found) exit 1 }
  ' "$report" >"$body_html"; then
    pr_explain_validation_error E4 "could not isolate the canonical report body"
  fi

  if awk '
    {
      rest = $0
      gsub(/<[^>]*>/, "", rest)
      if (index(rest, "<") || index(rest, ">")) bad = 1
    }
    END { exit bad ? 0 : 1 }
  ' "$body_html"; then
    pr_explain_validation_error E2 "HTML tags must be complete on one line and literal angle brackets must be escaped"
  fi
  pr_explain_body_tags_are_canonical "$body_html" ||
    pr_explain_validation_error E2 "body tags must use canonical lowercase names and double-quoted attribute syntax"
  pr_explain_body_entities_are_canonical "$body_html" ||
    pr_explain_validation_error E2 "body text and attributes may use only amp, lt, gt, quot, and apos character references"
  pr_explain_body_attributes_preserve_content "$body_html" ||
    pr_explain_validation_error E11 "authored hiding, inert, popover, editing, and autofocus attributes are forbidden"
  if grep -qE '<!(-{2}|\[)|-{2}>' "$body_html"; then
    pr_explain_validation_error E2 "comments and conditional declarations are forbidden in the report body"
  fi
  pr_explain_body_is_well_nested "$body_html" ||
    pr_explain_validation_error E5 "report body elements are not explicitly and correctly nested"
  pr_explain_component_tags_are_semantic "$body_html" ||
    pr_explain_validation_error E6 "component classes must use their canonical semantic elements"
  pr_explain_diagrams_are_in_figures "$body_html" ||
    pr_explain_validation_error E8 "every diagram component must be contained by an rx-figure"
  pr_explain_disclosures_have_safe_depth "$body_html" ||
    pr_explain_validation_error E16 "details disclosures may be nested at most two levels deep"
  pr_explain_headings_are_ordered "$body_html" ||
    pr_explain_validation_error E5 "heading levels must begin at h1 and cannot skip a level"
  pr_explain_figures_are_well_formed "$body_html" ||
    pr_explain_validation_error E8 "each figure must be an rx-figure with one final direct caption"

  if ! awk '
    $0 == "<main id=\"rx-main\">" { main = 1; next }
    main && $0 ~ /^[[:space:]]*<\/main>[[:space:]]*$/ { found = 1; exit }
    main { print }
    END { if (!found) exit 1 }
  ' "$body_html" >"$main_html"; then
    pr_explain_validation_error E5 "could not isolate the canonical main landmark"
  fi
  sed -E 's/<[^>]+>//g' "$body_html" | tr -d '\r\n' >"$visible_text"
  pr_explain_strip_quoted_code "$report" >"$report_live"

  while IFS= read -r value; do
    value="${value#<}"
    value="${value#/}"
    value="${value%%[[:space:]>]*}"
    value="${value,,}"
    case "$value" in
      a|abbr|article|aside|b|blockquote|br|caption|cite|code|dd|details|dfn|div|dl|dt|em|figcaption|figure|footer|h1|h2|h3|h4|h5|h6|header|hr|kbd|li|main|mark|nav|ol|p|pre|q|samp|section|span|strong|summary|table|tbody|td|th|thead|time|tr|ul) ;;
      *) pr_explain_validation_error E2 "element '$value' is outside the passive semantic vocabulary" ;;
    esac
  done < <(grep -Eio '<\/?[a-z][a-z0-9-]*([[:space:]>])' "$body_html" 2>/dev/null || true)

  style_count=$(pr_explain_count_regex "$report" '<style([[:space:]>])')
  script_count=$(pr_explain_count_regex "$report" '<script([[:space:]>])')
  [[ "$style_count" -eq 1 ]] ||
    pr_explain_validation_error E1 "report must contain exactly one canonical style block"
  [[ "$script_count" -eq 1 ]] ||
    pr_explain_validation_error E1 "report must contain exactly one canonical script block"
  count=$(pr_explain_count_regex "$report" '<meta([[:space:]>])')
  [[ "$count" -eq 2 ]] ||
    pr_explain_validation_error E4 "report must contain only the canonical charset and viewport meta elements"
  cmp -s "$expected_css" "$actual_css" ||
    pr_explain_validation_error E1 "inline stylesheet does not match template version 1"
  cmp -s "$expected_js" "$actual_js" ||
    pr_explain_validation_error E1 "inline script does not match template version 1"

  if grep -qiE '<(link|iframe|frame|img|object|embed|video|audio|source|track|form|base|svg|canvas|math|applet|marquee|blink)([[:space:]>])|<meta[^>]+http-equiv' "$report_live"; then
    pr_explain_validation_error E2 "active or resource-loading markup is forbidden"
  fi
  if grep -qiE "(^|[[:space:]])(src|srcset|poster|ping|action|formaction|manifest|background|xlink:href)[[:space:]]*=|href[[:space:]]*=[[:space:]]*[\"'](https?:|//|data:|javascript:)" "$report_live"; then
    pr_explain_validation_error E2 "network-capable URL attributes are forbidden"
  fi
  if grep -qiE '(^|[[:space:]])style[[:space:]]*=|(^|[[:space:]])on[a-z]+[[:space:]]*=' "$report_live"; then
    pr_explain_validation_error E1 "inline styles and event-handler attributes are forbidden"
  fi
  if grep -qE 'aria-(label|labelledby|describedby)=""' "$body_html"; then
    pr_explain_validation_error E5 "authored accessibility names and references cannot be empty"
  fi
  if grep -qiE '@import|url[[:space:]]*\(|@keyframes|(^|[;{[:space:]])animation[[:space:]]*:|transform[[:space:]]*:[^;}]*rotate|(^|[;{[:space:]])rotate[[:space:]]*:|writing-mode[[:space:]]*:' "$report_live"; then
    pr_explain_validation_error E2 "stylesheet contains loading, autoplay, or rotated connector behavior"
  fi
  if grep -qiE 'fetch[[:space:]]*\(|XMLHttpRequest|WebSocket|EventSource|sendBeacon|import[[:space:]]*\(|localStorage|sessionStorage|indexedDB|document\.cookie|eval[[:space:]]*\(|new[[:space:]]+Function|innerHTML|outerHTML|insertAdjacentHTML|document\.write|window\.open|postMessage|setInterval|navigator\.' "$actual_js"; then
    pr_explain_validation_error E2 "canonical script contains a forbidden network or dynamic-code API"
  fi
  if grep -qiE 'rx-(arrow|connector)' "$report_live"; then
    pr_explain_validation_error E9 "connectors must be node-owned pseudo-elements, never sibling elements"
  fi

  if [[ "$(sed -n '1p' "$report")" != '<!doctype html>' ]] ||
     [[ "$(sed -n '2p' "$report")" != '<html lang="en" data-rx-template="1">' ]]; then
    pr_explain_validation_error E4 "doctype and language-bearing template shell are required"
  fi
  if [[ "$(sed -n '4p' "$report")" != '<meta charset="utf-8">' ]] ||
     ! grep -Fq '<meta name="viewport" content="width=device-width, initial-scale=1">' "$report"; then
    pr_explain_validation_error E4 "UTF-8 charset must be the first meta and viewport is required"
  fi
  for value in html head body main title; do
    count=$(pr_explain_count_regex "$report" "<$value([[:space:]>])")
    [[ "$count" -eq 1 ]] || pr_explain_validation_error E5 "expected exactly one <$value> element"
    count=$(pr_explain_count_regex "$report" "</$value>")
    [[ "$count" -eq 1 ]] || pr_explain_validation_error E5 "expected exactly one </$value> closing tag"
  done
  h1_count=$(pr_explain_count_regex "$report" '<h1([[:space:]>])')
  main_count=$(pr_explain_count_regex "$report" '<main([[:space:]>])')
  [[ "$h1_count" -eq 1 ]] || pr_explain_validation_error E5 "report must contain exactly one h1"
  [[ "$main_count" -eq 1 ]] || pr_explain_validation_error E5 "report must contain exactly one main landmark"
  grep -Fq '<main id="rx-main">' "$report" || pr_explain_validation_error E5 "main landmark must use id rx-main"
  grep -Fq '<a class="rx-skip" href="#rx-main">Skip to content</a>' "$report" ||
    pr_explain_validation_error E5 "keyboard skip link to rx-main is required"
  count=$(pr_explain_count_regex "$body_html" '<nav class="rx-toc" aria-label="[^"]+">')
  [[ "$count" -eq 1 ]] || pr_explain_validation_error E5 "report requires exactly one named contents navigation landmark"
  if [[ -n "${repository:-}" && -n "${number:-}" ]]; then
    grep -Fq "<title>$repository PR #$number" "$report" ||
      pr_explain_validation_error E4 "title must name repository and PR number"
  fi

  make_temp_file expected_gallery
  pr_explain_emit_gallery >"$expected_gallery"
  cmp -s "$report" "$expected_gallery" && gallery_report=true

  declare -A allowed_classes=()
  for class_name in \
    rx-skip rx-masthead rx-eyebrow rx-lede rx-cost rx-summary rx-decision rx-summary-cards rx-card \
    rx-toc rx-claim rx-objectives rx-figure rx-caption rx-flow rx-branch rx-branch-test rx-branch-arms \
    rx-branch-arm rx-arm-label rx-lanes rx-lane rx-lane-label rx-sequence rx-seq-steps \
    rx-seq-step rx-seq-title rx-seq-controls rx-seq-status rx-codewalk rx-scroll rx-code \
    rx-hl rx-elide rx-notes rx-note rx-callout rx-callout-label rx-compare rx-mark \
    rx-timeline rx-state rx-more rx-predict rx-term rx-termref rx-quiz-item rx-choices \
    rx-choice rx-quiz-result rx-footer rx-provenance rx-stats rx-stat; do
    allowed_classes["$class_name"]=1
  done
  if [[ "$gallery_report" == true ]]; then
    for class_name in rx-preview rx-preview-grid rx-preview-label rx-preview-scroll; do
      allowed_classes["$class_name"]=1
    done
  fi
  if grep -qE "class[[:space:]]*=[[:space:]]*'" "$report"; then
    pr_explain_validation_error E6 "class attributes must use double quotes"
  fi
  while IFS= read -r class_value; do
    class_value="${class_value#class=\"}"
    class_value="${class_value%\"}"
    for class_name in $class_value; do
      if [[ -z "${allowed_classes[$class_name]:-}" ]]; then
        pr_explain_validation_error E6 "unknown class '$class_name'"
      fi
    done
  done < <(grep -Eo 'class="[^"]+"' "$report" 2>/dev/null || true)

  report_tags=$(pr_explain_report_tags "$report")
  declare -A seen_ids=()
  while IFS=: read -r line_no value; do
    id="${value#id=\"}"
    id="${id%\"}"
    if [[ -n "${seen_ids[$id]:-}" ]]; then
      pr_explain_validation_error E3 \
        "duplicate id '$id' (first used at line ${seen_ids[$id]}, again at line $line_no)"
    else
      seen_ids["$id"]="$line_no"
    fi
  done < <(awk '{
      idx = index($0, "\t")
      line = substr($0, 1, idx - 1)
      tag = substr($0, idx + 1)
      while (match(tag, /id="[A-Za-z][A-Za-z0-9_.:-]*"/)) {
        printf "%s:%s\n", line, substr(tag, RSTART, RLENGTH)
        tag = substr(tag, RSTART + RLENGTH)
      }
    }' <<<"$report_tags" 2>/dev/null || true)
  count=$(pr_explain_count_stream_regex "$report_tags" 'id="[^"]*"')
  [[ "$count" -eq "${#seen_ids[@]}" ]] ||
    pr_explain_validation_error E3 "every id must be non-empty and use the canonical identifier syntax"
  if grep -qE "(id|href|aria-labelledby|aria-describedby)[[:space:]]*=[[:space:]]*'" <<<"$report_tags"; then
    pr_explain_validation_error E3 "ID and reference attributes must use double quotes"
  fi
  while IFS= read -r value; do
    ref="${value#href=\"#}"
    ref="${ref%\"}"
    [[ -n "$ref" && -n "${seen_ids[$ref]:-}" ]] ||
      pr_explain_validation_error E3 "dangling fragment link '#$ref'"
  done < <(grep -Eo 'href="#[^"]*"' <<<"$report_tags" 2>/dev/null || true)
  count=$(pr_explain_count_stream_regex "$report_tags" '<a([[:space:]>])')
  ref=$(pr_explain_count_stream_regex "$report_tags" '<a[^>]+href="#[^"]+"')
  [[ "$count" -eq "$ref" ]] || pr_explain_validation_error E2 "anchors must use non-empty same-document fragments"
  while IFS= read -r value; do
    value="${value#*=\"}"
    value="${value%\"}"
    for ref in $value; do
      [[ -n "${seen_ids[$ref]:-}" ]] || pr_explain_validation_error E3 "dangling accessibility reference '$ref'"
    done
  done < <(grep -Eo '(aria-labelledby|aria-describedby|data-hl)="[^"]+"' <<<"$report_tags" 2>/dev/null || true)

  for value in what-changes-now what-exists-after evidence residual-risk how-to-reject; do
    count=$(pr_explain_count_regex "$report" "data-q=\"$value\"")
    [[ "$count" -eq 1 ]] || pr_explain_validation_error E17 "summary card '$value' must appear exactly once"
  done
  grep -Fq '<section class="rx-summary" aria-labelledby="rx-summary-h">' "$report" ||
    pr_explain_validation_error E17 "operator routing summary is required"
  grep -Fq '<p class="rx-decision">' "$report" ||
    pr_explain_validation_error E17 "operator decision sentence is required"
  card_count=$(pr_explain_count_regex "$body_html" '<li class="rx-card"')
  [[ "$card_count" -eq 5 ]] ||
    pr_explain_validation_error E17 "operator routing summary must contain exactly five cards"
  pr_explain_summary_is_well_formed "$body_html" ||
    pr_explain_validation_error E17 "operator summary must directly own one heading, decision, and five-card routing list"
  pr_explain_check_reading_cost "$body_html"

  h2_count=$(pr_explain_count_regex "$report" '<h2([[:space:]>])')
  claim_count=$(pr_explain_count_regex "$report" '<p class="rx-claim">')
  if [[ "$h2_count" -lt 2 || "$claim_count" -ne $((h2_count - 1)) ]]; then
    pr_explain_validation_error E7 "every main h2 section must open with one signaling paragraph"
  fi
  main_h2_count=$(pr_explain_count_regex "$main_html" '<h2([[:space:]>])')
  main_claim_count=$(pr_explain_count_regex "$main_html" '<p class="rx-claim">')
  if [[ "$main_h2_count" -lt 1 || "$main_claim_count" -ne "$main_h2_count" ]] ||
     ! awk '
       /<h2([[:space:]>])/ { need_claim = 1; next }
       need_claim && /^[[:space:]]*$/ { next }
       need_claim {
         if ($0 !~ /^[[:space:]]*<p class="rx-claim">/) bad = 1
         need_claim = 0
       }
       END { exit (bad || need_claim) ? 1 : 0 }
     ' "$main_html"; then
    pr_explain_validation_error E7 "each main h2 must be immediately followed by exactly one rx-claim"
  fi

  pr_explain_check_layers "$body_html"
  pr_explain_check_objectives_block "$body_html"
  pr_explain_check_objective_coverage "$body_html"

  figure_count=$(pr_explain_count_regex "$report" '<figure class="rx-figure')
  caption_count=$(pr_explain_count_regex "$report" '<figcaption class="rx-caption">')
  [[ "$figure_count" -eq "$caption_count" ]] ||
    pr_explain_validation_error E8 "every component figure needs one claim-bearing caption"
  if grep -qE '<figcaption class="rx-caption">[[:space:]]*</figcaption>' "$report"; then
    pr_explain_validation_error E8 "figure captions cannot be empty"
  fi
  pr_explain_captions_are_substantive "$body_html" ||
    pr_explain_validation_error E8 "every figure caption must contain at least five visible words"

  local flow_failures flow_seen flow_count
  flow_failures=$(awk '
    {
      rest = $0
      while (match(rest, /<[^>]+>/)) {
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<[[:space:]]*\//)
        name = tag
        sub(/^<[[:space:]]*\/?[[:space:]]*/, "", name)
        sub(/[[:space:]\/>].*$/, "", name)
        name = tolower(name)

        if (!closing) {
          if (flow && name == "li" && depth == flow_depth) actual++
          depth++
          if (tag ~ /^<ol class="rx-flow" data-steps="[0-9]+">$/) {
            if (flow) bad++
            text = tag
            sub(/^.*data-steps="/, "", text)
            sub(/".*/, "", text)
            expected = text + 0
            actual = 0
            flow = 1
            flow_depth = depth
            seen++
          }
        } else {
          if (flow && name == "ol" && depth == flow_depth) {
            if (actual != expected) bad++
            flow = 0
          }
          depth--
        }
      }
    }
    END { if (flow || depth != 0) bad++; print (bad + 0) ":" (seen + 0) }
  ' "$body_html")
  IFS=: read -r flow_failures flow_seen <<<"$flow_failures"
  flow_count=$(pr_explain_count_regex "$body_html" '<ol class="rx-flow"')
  [[ "$flow_failures" -eq 0 && "$flow_seen" -eq "$flow_count" ]] ||
    pr_explain_validation_error E9 "flow data-steps must match direct list-node count"
  if ! grep -Fq '.rx-flow > li + li::before {' "$expected_css" ||
     ! grep -Fq -- '--rx-flow-gap:' "$expected_css" ||
     ! grep -Fq -- '--rx-flow-glyph: "\2193";' "$expected_css" ||
     ! grep -Fq -- '--rx-flow-glyph: "\2192";' "$expected_css"; then
    pr_explain_validation_error E9 "template is missing canonical node-owned horizontal/vertical connectors"
  fi

  branch_count=$(pr_explain_count_regex "$body_html" '<div class="rx-branch"')
  branch_test_count=$(pr_explain_count_regex "$body_html" '<p class="rx-branch-test">')
  branch_arms_count=$(pr_explain_count_regex "$body_html" '<ul class="rx-branch-arms" data-arms="[0-9]+">')
  branch_arm_count=$(pr_explain_count_regex "$body_html" '<li class="rx-branch-arm"')
  arm_label_count=$(pr_explain_count_regex "$body_html" '<b class="rx-arm-label">')
  if [[ "$branch_test_count" -ne "$branch_count" || "$branch_arms_count" -ne "$branch_count" ||
        "$branch_arm_count" -lt $((branch_count * 2)) || "$arm_label_count" -ne "$branch_arm_count" ]]; then
    pr_explain_validation_error E15 "each branch needs one test and a declared set of at least two visibly labeled arms"
  fi
  value=$(awk '
    {
      rest = $0
      while (match(rest, /<[^>]+>/)) {
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<[[:space:]]*\//)
        name = tag
        sub(/^<[[:space:]]*\/?[[:space:]]*/, "", name)
        sub(/[[:space:]\/>].*$/, "", name)
        name = tolower(name)
        if (!closing) {
          parent_depth = depth
          if (arms && name == "li" && depth == arms_depth && tag ~ /^<li class="rx-branch-arm"/) {
            if (arm) bad++
            actual++
            arm = 1
            arm_depth = depth + 1
            labels = 0
          } else if (arm && parent_depth == arm_depth && tag == "<b class=\"rx-arm-label\">") {
            labels++
          }
          depth++
          if (tag ~ /^<ul class="rx-branch-arms" data-arms="[0-9]+">$/) {
            if (arms) bad++
            text = tag
            sub(/^.*data-arms="/, "", text)
            sub(/".*/, "", text)
            expected = text + 0
            actual = 0
            arms = 1
            arms_depth = depth
            seen++
          }
        } else {
          if (arm && name == "li" && depth == arm_depth) {
            if (labels != 1) bad++
            arm = 0
          }
          if (arms && name == "ul" && depth == arms_depth) {
            if (actual != expected || actual < 2 || arm) bad++
            arms = 0
          }
          depth--
        }
      }
    }
    END { if (arms) bad++; print (bad + 0) ":" (seen + 0) }
  ' "$body_html")
  IFS=: read -r count ref <<<"$value"
  [[ "$count" -eq 0 && "$ref" -eq "$branch_arms_count" ]] ||
    pr_explain_validation_error E15 "branch data-arms must match its direct labeled arm count"
  pr_explain_branches_are_well_formed "$body_html" ||
    pr_explain_validation_error E15 "each branch must directly own exactly one test and one arms list"

  lanes_count=$(pr_explain_count_regex "$body_html" '<div class="rx-lanes"')
  lane_count=$(pr_explain_count_regex "$body_html" '<section class="rx-lane"')
  lane_label_count=$(pr_explain_count_regex "$body_html" '<h4 class="rx-lane-label">')
  value=$(pr_explain_lanes_are_well_formed "$body_html")
  IFS=: read -r count ref <<<"$value"
  [[ "$count" -eq 0 && "$ref" -eq "$lane_count" && "$lane_label_count" -eq "$lane_count" &&
     "$lane_count" -ge $((lanes_count * 2)) ]] ||
    pr_explain_validation_error E15 "lanes need at least two tracks, one label and flow each, and equal aligned step counts"

  sequence_count=$(pr_explain_count_regex "$body_html" '<section class="rx-sequence"')
  seq_steps_count=$(pr_explain_count_regex "$body_html" '<ol class="rx-seq-steps"')
  seq_step_count=$(pr_explain_count_regex "$body_html" '<li class="rx-seq-step"')
  seq_title_count=$(pr_explain_count_regex "$body_html" '<h4 class="rx-seq-title">')
  value=$(pr_explain_sequences_are_well_formed "$body_html")
  IFS=: read -r count ref <<<"$value"
  if [[ "$count" -ne 0 || "$ref" -ne "$sequence_count" ||
        "$seq_steps_count" -ne "$sequence_count" || "$seq_title_count" -ne "$seq_step_count" ]]; then
    pr_explain_validation_error E15 "each sequence needs one heading and one complete ordered list of titled visible steps"
  fi
  if grep -qE 'class="rx-(seq-controls|seq-prev|seq-next|seq-all|seq-status|quiz-score)([ " ])' "$body_html"; then
    pr_explain_validation_error E15 "sequence and quiz enhancement controls must be created only by canonical JavaScript"
  fi

  callout_count=$(pr_explain_count_regex "$body_html" '<aside class="rx-callout"')
  callout_label_count=$(pr_explain_count_regex "$body_html" '<p class="rx-callout-label">')
  role_count=$(pr_explain_count_regex "$body_html" '<aside class="rx-callout" data-role="[^"]+">')
  [[ "$callout_count" -eq "$callout_label_count" && "$role_count" -eq "$callout_count" ]] ||
    pr_explain_validation_error E15 "every callout requires one closed role and a visible role label"
  pr_explain_callouts_are_well_formed "$body_html" ||
    pr_explain_validation_error E15 "each callout must contain exactly one non-empty direct role label"
  while IFS= read -r value; do
    role="${value#data-role=\"}"
    role="${role%\"}"
    case "$role" in
      definition|intuition|misconception|caution|boundary|evidence) ;;
      *) pr_explain_validation_error E15 "unknown callout role '$role'" ;;
    esac
  done < <(grep -Eo 'data-role="[^"]+"' "$report" 2>/dev/null || true)

  timeline_count=$(pr_explain_count_regex "$body_html" '<ol class="rx-timeline"')
  state_count=$(pr_explain_count_regex "$body_html" '<p class="rx-state">')
  count=$(pr_explain_count_regex "$body_html" '<li data-state="(done|now|blocked|planned|correction|dead-end)">')
  if [[ "$timeline_count" -gt 0 && ( "$state_count" -eq 0 || "$state_count" -ne "$count" ) ]]; then
    pr_explain_validation_error E15 "every timeline item needs an allowed state and matching visible state text"
  fi
  pr_explain_timelines_are_well_formed "$body_html" ||
    pr_explain_validation_error E15 "every timeline item needs exactly one direct non-empty visible state"
  while IFS= read -r value; do
    state="${value#data-state=\"}"
    state="${state%\"}"
    case "$state" in
      done|now|blocked|planned|correction|dead-end|yes|partial|no) ;;
      *) pr_explain_validation_error E15 "unknown visible state '$state'" ;;
    esac
  done < <(grep -Eo 'data-state="[^"]+"' "$report" 2>/dev/null || true)
  if [[ "$timeline_count" -gt 0 ]] &&
     ! grep -qE 'data-state="(correction|dead-end)"' "$report"; then
    pr_explain_validation_warning W10 "timeline has no correction or dead end"
  fi

  if grep -qiE '<th([[:space:]>])' "$report"; then
    while IFS= read -r value; do
      [[ "$value" == *' scope="col"'* || "$value" == *' scope="row"'* ]] ||
        pr_explain_validation_error E14 "every table header needs col or row scope"
    done < <(grep -Eio '<th([[:space:]][^>]*)?>' "$report" 2>/dev/null || true)
  fi
  table_count=$(pr_explain_count_regex "$body_html" '<table([[:space:]>])')
  compare_count=$(pr_explain_count_regex "$body_html" '<table class="rx-compare"')
  value=$(pr_explain_count_regex "$body_html" '<caption>')
  [[ "$table_count" -eq "$compare_count" && "$value" -eq "$compare_count" ]] ||
    pr_explain_validation_error E14 "every table must be an rx-compare with exactly one caption"
  pr_explain_comparisons_are_well_formed "$body_html" ||
    pr_explain_validation_error E14 "each comparison must begin with exactly one substantive direct caption"
  grep -qiE 'table[^{]*\{[^}]*display[[:space:]]*:[[:space:]]*block' "$expected_css" &&
    pr_explain_validation_error E14 "template must preserve table semantics on narrow screens"

  quiz_count=$(pr_explain_count_regex "$report" '<article class="rx-quiz-item"')
  choice_count=$(pr_explain_count_regex "$report" '<details class="rx-choice"')
  correct_count=$(pr_explain_count_regex "$report" '<details class="rx-choice"[^>]*data-correct="true"')
  false_count=$(pr_explain_count_regex "$report" '<details class="rx-choice"[^>]*data-correct="false"')
  misconception_count=$(pr_explain_count_regex "$report" '<details class="rx-choice"[^>]*data-misconception="[^"]+"')
  [[ "$quiz_count" -ge 3 && "$quiz_count" -le 7 ]] ||
    pr_explain_validation_error E10 "report must contain between three and seven quiz items (found $quiz_count)"
  [[ "$choice_count" -eq $((quiz_count * 4)) ]] ||
    pr_explain_validation_error E10 "each quiz item must contain four choices"
  [[ "$correct_count" -eq "$quiz_count" ]] ||
    pr_explain_validation_error E10 "each quiz item must contain exactly one correct choice"
  [[ "$false_count" -eq $((quiz_count * 3)) && "$misconception_count" -eq $((quiz_count * 3)) ]] ||
    pr_explain_validation_error E10 "every incorrect choice needs a misconception label"
  pr_explain_quizzes_are_well_formed "$body_html" ||
    pr_explain_validation_error E10 "each quiz item needs one heading, four grouped choices, one key, three misconception distractors, feedback, and one live result"
  pr_explain_check_quiz_v2 "$body_html"
  if grep -qiE '<(button|input|select)([[:space:]>])' "$report"; then
    pr_explain_validation_error E10 "authored controls are forbidden; no-JS details carry interaction"
  fi
  count=$(pr_explain_count_regex "$report" '<summary>')
  [[ "$count" -ge "$choice_count" ]] || pr_explain_validation_error E10 "quiz choices need visible summary text"
  details_count=$(pr_explain_count_regex "$body_html" '<details([[:space:]>])')
  disclosure_count=$(pr_explain_count_regex "$body_html" '<details class="rx-(more|predict)"')
  summary_count=$(pr_explain_count_regex "$body_html" '<summary>')
  [[ "$details_count" -eq $((choice_count + disclosure_count)) && "$summary_count" -eq "$details_count" ]] ||
    pr_explain_validation_error E16 "every disclosure must use an allowed class and contain exactly one visible summary"
  if grep -qE '<summary>[[:space:]]*</summary>' "$body_html"; then
    pr_explain_validation_error E16 "disclosure summaries cannot be empty"
  fi
  if grep -qiE '<summary>[[:space:]]*(more|details|click|expand|read more|…)[[:space:]]*</summary>' "$report"; then
    pr_explain_validation_error E16 "disclosure summaries must say what they reveal"
  fi

  stat_group_count=$(pr_explain_count_regex "$body_html" '<ul class="rx-stats"')
  stat_count=$(pr_explain_count_regex "$body_html" '<li class="rx-stat"')
  [[ "$stat_count" -le $((stat_group_count * 4)) ]] ||
    pr_explain_validation_error E15 "each stats group may contain at most four defined values"

  code_count=$(pr_explain_count_regex "$report" '<pre class="rx-code"')
  scroll_count=$(pr_explain_count_regex "$report" '<div class="rx-scroll" role="region" tabindex="0" aria-label="[^"]+">')
  codewalk_count=$(pr_explain_count_regex "$body_html" '<figure class="rx-figure rx-codewalk"')
  notes_count=$(pr_explain_count_regex "$body_html" '<ol class="rx-notes"')
  note_count=$(pr_explain_count_regex "$body_html" '<li class="rx-note" data-hl="[^"]+">')
  highlight_count=$(pr_explain_count_regex "$body_html" '<mark class="rx-hl" id="[^"]+">')
  [[ "$scroll_count" -eq $((code_count + compare_count)) &&
     "$codewalk_count" -eq "$code_count" && "$notes_count" -eq "$codewalk_count" &&
     "$note_count" -eq "$highlight_count" ]] ||
    pr_explain_validation_error E12 "codewalks and comparisons need their exact named scroll, code, highlight, and note structure"
  pr_explain_scroll_regions_are_well_formed "$body_html" ||
    pr_explain_validation_error E12 "each code block or comparison needs its own direct non-empty named scroll region"
  preview_scroll_count=0
  if [[ "$gallery_report" == true ]]; then
    preview_scroll_count=$(pr_explain_count_regex "$body_html" '<div class="rx-preview-scroll" role="region" tabindex="0" aria-label="[^"]+">')
  fi
  role_count=$(pr_explain_count_regex "$body_html" ' role="[^"]+"')
  tabindex_count=$(pr_explain_count_regex "$body_html" ' tabindex="[^"]+"')
  if [[ "$role_count" -ne $((scroll_count + preview_scroll_count)) ||
        "$tabindex_count" -ne $((scroll_count + preview_scroll_count)) ]]; then
    pr_explain_validation_error E12 "role and tabindex are reserved for canonical named scroll regions"
  fi
  while IFS= read -r value; do
    ref="${value#data-hl=\"}"
    ref="${ref%\"}"
    grep -Fq "<mark class=\"rx-hl\" id=\"$ref\">" "$body_html" ||
      pr_explain_validation_error E12 "code note '$ref' does not target an rx-hl mark"
  done < <(grep -Eo 'data-hl="[^"]+"' "$body_html" 2>/dev/null || true)
  if grep -E '<pre class="rx-code"' "$report" | grep -vq 'data-lang="[^"]\+"'; then
    pr_explain_validation_error E12 "every code block needs data-lang"
  fi
  grep -Fq '.rx-code > code {' "$expected_css" ||
    pr_explain_validation_error E12 "template is missing the exact-code rule"
  grep -Fq 'white-space: pre;' "$expected_css" ||
    pr_explain_validation_error E12 "exact code must preserve whitespace with white-space: pre"
  if grep -qiE 'rx-code[^}]*white-space[[:space:]]*:[[:space:]]*pre-wrap' "$expected_css"; then
    pr_explain_validation_error E12 "exact code cannot use pre-wrap"
  fi

  if ! grep -Fq '@media (prefers-color-scheme: dark)' "$expected_css"; then
    pr_explain_validation_error E1 "template lacks dark-scheme tokens"
  fi
  if ! grep -Fq '@media (prefers-reduced-motion: reduce)' "$expected_css" ||
     ! grep -Fq 'transition-duration: .01ms !important;' "$expected_css"; then
    pr_explain_validation_error E1 "template lacks reduced-motion behavior"
  fi
  if ! grep -Fq '@media print' "$expected_css"; then
    pr_explain_validation_error E1 "template lacks print behavior"
  fi
  if ! grep -Fq '@container rx-figure' "$expected_css"; then
    pr_explain_validation_error E9 "flow layout must respond to its component container"
  fi
  if ! grep -Fq ':focus-visible' "$expected_css"; then
    pr_explain_validation_error E1 "template lacks a visible keyboard focus treatment"
  fi

  make_temp_file provenance_html
  : >"$provenance_html"
  if [[ -n "${repository:-}" && -n "${number:-}" && -n "${pr_url:-}" && -n "${base_sha:-}" && -n "${head_sha:-}" ]]; then
    value="<dl class=\"rx-provenance\" data-repository=\"$repository_html\" data-pr=\"$number\" data-pr-url=\"$pr_url_html\" data-base-sha=\"$base_sha\" data-head-sha=\"$head_sha\" data-runner=\"$runner\">"
    if ! awk -v opening="$value" '
      index($0, opening) { inside = 1 }
      inside { print }
      inside && /<\/dl>/ { found = 1; exit }
      END { if (!found) exit 1 }
    ' "$body_html" >"$provenance_html"; then
      pr_explain_validation_error E18 "provenance attributes do not match the resolved snapshot and runner"
    fi
    grep -Fq "<dt data-field=\"repo\">Repository</dt><dd>$repository_html</dd>" "$provenance_html" ||
      pr_explain_validation_error E18 "visible repository provenance is missing or misplaced"
    grep -Fq "<dt data-field=\"pr\">Pull request</dt><dd>#$number</dd>" "$provenance_html" ||
      pr_explain_validation_error E18 "visible PR provenance is missing or misplaced"
    grep -Fq "<dt data-field=\"url\">Pull request URL</dt><dd>$pr_url_html</dd>" "$provenance_html" ||
      pr_explain_validation_error E18 "visible PR URL does not match snapshot"
    grep -Fq "<dt data-field=\"title\">Pull request title</dt><dd>$pr_title_html</dd>" "$provenance_html" ||
      pr_explain_validation_error E18 "visible PR title does not match snapshot"
    grep -Fq "<dt data-field=\"base\">Base commit</dt><dd><code>$base_sha</code></dd>" "$provenance_html" ||
      pr_explain_validation_error E18 "visible base SHA does not match snapshot"
    grep -Fq "<dt data-field=\"head\">Head commit</dt><dd><code>$head_sha</code></dd>" "$provenance_html" ||
      pr_explain_validation_error E18 "visible head SHA does not match snapshot"
    grep -Fq "<dt data-field=\"runner\">Runner</dt><dd>$(pr_explain_runner_display "$runner")</dd>" "$provenance_html" ||
      pr_explain_validation_error E18 "visible runner provenance is missing"
  fi
  count=$(pr_explain_count_regex "$body_html" '<dl class="rx-provenance"')
  [[ "$count" -eq 1 ]] || pr_explain_validation_error E18 "report must contain exactly one provenance list"
  for field in repo pr url title base head diffstat runner generator generated sources limits; do
    count=$(pr_explain_count_regex "$body_html" "data-field=\"$field\"")
    ref=$(pr_explain_count_regex "$provenance_html" "data-field=\"$field\"")
    [[ "$count" -eq 1 && "$ref" -eq 1 ]] ||
      pr_explain_validation_error E18 "provenance field '$field' must appear exactly once inside provenance"
  done
  generated_date=$(grep -Eo '<time datetime="[0-9]{4}-[0-9]{2}-[0-9]{2}">[0-9]{4}-[0-9]{2}-[0-9]{2}</time>' "$provenance_html" 2>/dev/null | head -n1 || true)
  if [[ -z "$generated_date" ]]; then
    pr_explain_validation_error E18 "generated provenance needs matching ISO date text and datetime"
  else
    value="${generated_date#*datetime=\"}"
    value="${value%%\"*}"
    field="${generated_date#*>}"
    field="${field%</time>}"
    if [[ "$value" != "$field" ]] ||
       [[ "$(date -u -d "$value" +%F 2>/dev/null || true)" != "$value" ]]; then
      pr_explain_validation_error E18 "generated provenance date is not a real matching ISO date"
    fi
  fi
  count=$(pr_explain_count_regex "$body_html" '<time([[:space:]>])')
  ref=$(pr_explain_count_regex "$provenance_html" '<time([[:space:]>])')
  [[ "$count" -eq 1 && "$ref" -eq 1 ]] ||
    pr_explain_validation_error E18 "provenance must contain exactly one generated time element"
  count=$(awk '
    /<dt data-field="limits">/ { limits = 1 }
    limits && /<li([ >])/ { items++ }
    limits && /<\/dl>/ { limits = 0 }
    END { print items + 0 }
  ' "$provenance_html")
  [[ "$count" -ge 1 ]] || pr_explain_validation_error E18 "provenance limits must name at least one unverified claim"

  # Defense-in-depth, not a completeness guarantee: closed-form patterns for
  # well-known credential shapes plus a generic key-name/delimiter heuristic,
  # tuned to leave ordinary prose (a key name not immediately glued to a
  # contiguous value) alone. See docs/pr-explain.md.
  if grep -qiE -- "-----BEGIN|BEGIN OPENSSH PRIVATE KEY|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{20,}|(^|[^A-Za-z0-9])sk-[A-Za-z0-9_-]{20,}|(^|[^A-Za-z0-9])(sk|rk|pk)_(live|test)_[A-Za-z0-9]{16,}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}|(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|auth[_-]?token|client[_-]?secret|private[_-]?key)[[:space:]]*[=:][[:space:]]*[\"']?[^<\"'[:space:]]{12,}|[A-Za-z0-9+/]{200,}={0,2}" "$report" "$visible_text"; then
    pr_explain_validation_error E19 "report contains secret-looking material"
  fi

  make_temp_file slop_prose
  pr_explain_visible_prose "$body_html" >"$slop_prose"
  pr_explain_check_slop "$slop_prose"

  [[ "$figure_count" -ge 1 ]] ||
    pr_explain_validation_warning W1 "report contains no dual-coded figure; confirm prose alone is clearest"
  [[ "$h2_count" -ge 4 ]] ||
    pr_explain_validation_warning W12 "report has fewer than four navigable teaching sections"
  grep -Fq 'class="rx-more"' "$report" ||
    pr_explain_validation_warning W5 "report offers no skippable deep-background section"

  local warning_message error_message
  for warning_message in "${PR_EXPLAIN_VALIDATION_WARNINGS[@]}"; do
    printf 'allod: validation warning [%s]\n' "$warning_message" >&2
  done
  if [[ "${#PR_EXPLAIN_VALIDATION_ERRORS[@]}" -gt 0 ]]; then
    for error_message in "${PR_EXPLAIN_VALIDATION_ERRORS[@]}"; do
      printf 'allod: validation error [%s]\n' "$error_message" >&2
    done
    return 1
  fi
  return 0
}
