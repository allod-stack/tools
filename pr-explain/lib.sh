# shellcheck shell=bash
# Shared library for the pr-explain tools (explain, validate-report).
# Sourced with `set -euo pipefail` already in effect in the caller.

PR_EXPLAIN_LIB_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# The allod tools root is always this tool directory's parent: whichever
# candidate `resolve_pr_explain_dir` in the `allod` dispatcher picked — an
# ALLOD_TOOLS_DIR package, a source checkout's own `pr-explain/`, or a
# `$WORK_DIR/allod/tools/pr-explain` checkout — that parent is exactly the
# root `forge` ships beside in every one of those layouts.
PR_EXPLAIN_TOOLS_ROOT="$(cd -- "$PR_EXPLAIN_LIB_DIR/.." && pwd)"

pr_explain_asset_dir() {
  printf '%s\n' "$PR_EXPLAIN_LIB_DIR"
}

# Resolve the `forge` executable to use for this run. Checked in order:
#  1. ALLOD_PR_EXPLAIN_FORGE — an explicit override for tests and development,
#     validated as an executable file so it cannot silently no-op.
#  2. The `forge` shipped beside the resolved allod tools root, so a source
#     checkout's own companion `forge` is preferred over anything older that
#     happens to sit earlier on PATH.
#  3. `forge` on PATH, for packaged installs where `forge` ships as its own
#     package rather than beside `pr-explain/`.
pr_explain_resolve_forge() {
  local override="${ALLOD_PR_EXPLAIN_FORGE:-}" candidate override_dir

  if [[ -n "$override" ]]; then
    [[ -f "$override" && -x "$override" ]] ||
      die 1 "ALLOD_PR_EXPLAIN_FORGE must name an executable file: $override"
    override_dir=$(cd -- "$(dirname -- "$override")" && pwd) ||
      die 1 "ALLOD_PR_EXPLAIN_FORGE names an unresolvable directory: $override"
    printf '%s/%s\n' "$override_dir" "$(basename -- "$override")"
    return 0
  fi

  candidate="$PR_EXPLAIN_TOOLS_ROOT/forge"
  if [[ -f "$candidate" && -x "$candidate" ]]; then
    printf '%s\n' "$candidate"
    return 0
  fi

  command -v forge 2>/dev/null ||
    die 1 "forge companion not found beside allod tools root '$PR_EXPLAIN_TOOLS_ROOT' or on PATH"
}

pr_explain_emit_asset() {
  local name="$1" asset_dir
  asset_dir=$(pr_explain_asset_dir)
  [[ -f "$asset_dir/$name" ]] ||
    die 1 "PR explanation asset not found: $asset_dir/$name"
  cat -- "$asset_dir/$name"
}

pr_explain_emit_css() { pr_explain_emit_asset report.css; }
pr_explain_emit_js() { pr_explain_emit_asset report.js; }
pr_explain_emit_prompt() { pr_explain_emit_asset prompt.md; }
pr_explain_emit_repair_prompt() { pr_explain_emit_asset repair-prompt.md; }
pr_explain_emit_gallery() { pr_explain_emit_asset component-gallery.html; }

pr_explain_html_escape() {
  jq -sRr @html
}

declare -a PR_EXPLAIN_VALIDATION_ERRORS=()
declare -a PR_EXPLAIN_VALIDATION_WARNINGS=()

pr_explain_validation_error() {
  PR_EXPLAIN_VALIDATION_ERRORS+=("$1: $2")
}

pr_explain_validation_warning() {
  PR_EXPLAIN_VALIDATION_WARNINGS+=("$1: $2")
}

pr_explain_count_regex() {
  local file="$1" regex="$2" count
  count=$(grep -Eio "$regex" "$file" 2>/dev/null | wc -l) || true
  printf '%s\n' "${count//[[:space:]]/}"
}

pr_explain_is_safe_utf8() {
  od -An -v -tu1 -- "$1" | awk '
    BEGIN { need = 0; bad = 0; lower = 128; upper = 191; b1 = 0; b2 = 0 }
    {
      for (i = 1; i <= NF; i++) {
        byte = $i + 0
        if (need > 0) {
          if (byte < lower || byte > upper) bad = 1
          if (need == 2) b2 = byte
          need--
          lower = 128
          upper = 191
          if (need == 0) {
            # C1 controls U+0080-U+009F (2-byte: C2 80..9F).
            if (b1 == 194 && byte >= 128 && byte <= 159) bad = 1
            # Byte-order mark U+FEFF (3-byte: EF BB BF).
            if (b1 == 239 && b2 == 187 && byte == 191) bad = 1
            if (b1 == 226 && b2 == 128) {
              # Line/paragraph separator U+2028/U+2029.
              if (byte == 168 || byte == 169) bad = 1
              # Zero-width space/joiner/non-joiner, LRM/RLM U+200B-U+200F.
              if (byte >= 139 && byte <= 143) bad = 1
              # Bidi embedding/override controls U+202A-U+202E.
              if (byte >= 170 && byte <= 174) bad = 1
            }
            # Bidi isolate controls U+2066-U+2069.
            if (b1 == 226 && b2 == 129 && byte >= 166 && byte <= 169) bad = 1
          }
          continue
        }
        b1 = byte
        if (byte <= 127) {
          if (byte != 9 && byte != 10 && byte != 13 && byte < 32) bad = 1
        } else if (byte >= 194 && byte <= 223) {
          need = 1
        } else if (byte == 224) {
          need = 2
          lower = 160
        } else if ((byte >= 225 && byte <= 236) || (byte >= 238 && byte <= 239)) {
          need = 2
        } else if (byte == 237) {
          need = 2
          upper = 159
        } else if (byte == 240) {
          need = 3
          lower = 144
        } else if (byte >= 241 && byte <= 243) {
          need = 3
        } else if (byte == 244) {
          need = 3
          upper = 143
        } else {
          bad = 1
        }
      }
    }
    END { exit (bad || need != 0) ? 1 : 0 }
  '
}

pr_explain_body_tags_are_canonical() {
  awk '
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        if (tag ~ /^<\/[a-z][a-z0-9]*>$/) continue
        if (tag !~ /^<[a-z][a-z0-9]*([[:space:]]+[a-z][a-z0-9_.:-]*(="[^"]*")?)*>$/) {
          bad = 1
          continue
        }

        attrs = tag
        sub(/^<[a-z][a-z0-9]*/, "", attrs)
        sub(/>$/, "", attrs)
        tag_number++
        while (length(attrs)) {
          if (attrs !~ /^[[:space:]]+/) {
            bad = 1
            break
          }
          sub(/^[[:space:]]+/, "", attrs)
          name = attrs
          sub(/[=[:space:]].*$/, "", name)
          if (name == "" || seen[tag_number, name]) {
            bad = 1
            break
          }
          seen[tag_number, name] = 1
          attrs = substr(attrs, length(name) + 1)
          if (substr(attrs, 1, 2) == "=\"") {
            attrs = substr(attrs, 3)
            quote = index(attrs, "\"")
            if (!quote) {
              bad = 1
              break
            }
            attrs = substr(attrs, quote + 1)
          } else if (length(attrs) && attrs !~ /^[[:space:]]/) {
            bad = 1
            break
          }
        }
      }
    }
    END { exit bad ? 1 : 0 }
  ' "$1"
}

pr_explain_body_entities_are_canonical() {
  awk '
    {
      text = $0
      gsub(/&(amp|lt|gt|quot|apos);/, "", text)
      if (index(text, "&")) bad = 1
    }
    END { exit bad ? 1 : 0 }
  ' "$1"
}

pr_explain_body_attributes_preserve_content() {
  awk '
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        if (tag ~ /^<\//) continue
        attrs = tag
        sub(/^<[a-z][a-z0-9]*/, "", attrs)
        sub(/>$/, "", attrs)
        while (length(attrs)) {
          sub(/^[[:space:]]+/, "", attrs)
          name = attrs
          sub(/[=[:space:]].*$/, "", name)
          if (name == "") {
            bad = 1
            break
          }
          if (name ~ /^(hidden|inert|popover|contenteditable|autofocus|aria-hidden)$/) bad = 1
          attrs = substr(attrs, length(name) + 1)
          if (substr(attrs, 1, 2) == "=\"") {
            attrs = substr(attrs, 3)
            quote = index(attrs, "\"")
            if (!quote) {
              bad = 1
              break
            }
            attrs = substr(attrs, quote + 1)
          } else if (length(attrs) && attrs !~ /^[[:space:]]/) {
            bad = 1
            break
          }
        }
      }
    }
    END { exit bad ? 1 : 0 }
  ' "$1"
}

pr_explain_component_tags_are_semantic() {
  awk '
    function has_class(tag, wanted,    value) {
      value = tag
      if (value !~ / class="[^"]*"/) return 0
      sub(/^.* class="/, "", value)
      sub(/".*$/, "", value)
      return index(" " value " ", " " wanted " ") != 0
    }
    function require_tag(tag, name, class_name) {
      if (has_class(tag, class_name) && name != expected[class_name]) bad = 1
    }
    BEGIN {
      expected["rx-skip"] = "a"
      expected["rx-masthead"] = "header"
      expected["rx-eyebrow"] = "p"
      expected["rx-lede"] = "p"
      expected["rx-summary"] = "section"
      expected["rx-decision"] = "p"
      expected["rx-summary-cards"] = "ul"
      expected["rx-card"] = "li"
      expected["rx-toc"] = "nav"
      expected["rx-claim"] = "p"
      expected["rx-figure"] = "figure"
      expected["rx-caption"] = "figcaption"
      expected["rx-flow"] = "ol"
      expected["rx-branch"] = "div"
      expected["rx-branch-test"] = "p"
      expected["rx-branch-arms"] = "ul"
      expected["rx-branch-arm"] = "li"
      expected["rx-arm-label"] = "b"
      expected["rx-lanes"] = "div"
      expected["rx-lane"] = "section"
      expected["rx-lane-label"] = "h4"
      expected["rx-sequence"] = "section"
      expected["rx-seq-steps"] = "ol"
      expected["rx-seq-step"] = "li"
      expected["rx-seq-title"] = "h4"
      expected["rx-codewalk"] = "figure"
      expected["rx-scroll"] = "div"
      expected["rx-code"] = "pre"
      expected["rx-hl"] = "mark"
      expected["rx-elide"] = "span"
      expected["rx-notes"] = "ol"
      expected["rx-note"] = "li"
      expected["rx-callout"] = "aside"
      expected["rx-callout-label"] = "p"
      expected["rx-compare"] = "table"
      expected["rx-mark"] = "span"
      expected["rx-timeline"] = "ol"
      expected["rx-more"] = "details"
      expected["rx-predict"] = "details"
      expected["rx-term"] = "dfn"
      expected["rx-termref"] = "a"
      expected["rx-quiz-item"] = "article"
      expected["rx-choices"] = "ul"
      expected["rx-choice"] = "details"
      expected["rx-quiz-result"] = "p"
      expected["rx-footer"] = "footer"
      expected["rx-provenance"] = "dl"
      expected["rx-stats"] = "ul"
      expected["rx-stat"] = "li"
      expected["rx-preview"] = "figure"
      expected["rx-preview-grid"] = "div"
      expected["rx-preview-label"] = "p"
      expected["rx-preview-scroll"] = "div"
    }
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        if (tag ~ /^<\//) continue
        name = tag
        sub(/^</, "", name)
        sub(/[[:space:]>].*$/, "", name)
        for (class_name in expected) require_tag(tag, name, class_name)
      }
    }
    END { exit bad ? 1 : 0 }
  ' "$1"
}

pr_explain_diagrams_are_in_figures() {
  awk '
    function has_class(tag, wanted,    value) {
      value = tag
      if (value !~ / class="[^"]*"/) return 0
      sub(/^.* class="/, "", value)
      sub(/".*$/, "", value)
      return index(" " value " ", " " wanted " ") != 0
    }
    function is_diagram(tag) {
      return has_class(tag, "rx-flow") || has_class(tag, "rx-branch") ||
        has_class(tag, "rx-lanes") || has_class(tag, "rx-codewalk") ||
        has_class(tag, "rx-timeline") || has_class(tag, "rx-compare")
    }
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<\//)
        name = tag
        sub(/^<\/?/, "", name)
        sub(/[[:space:]>].*$/, "", name)
        if (!closing) {
          if (name == "figure" && has_class(tag, "rx-figure")) figures++
          if (is_diagram(tag) && !figures) bad = 1
        } else if (name == "figure" && figures) {
          figures--
        }
      }
    }
    END { exit (bad || figures) ? 1 : 0 }
  ' "$1"
}

pr_explain_disclosures_have_safe_depth() {
  awk '
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<\//)
        name = tag
        sub(/^<\/?/, "", name)
        sub(/[[:space:]>].*$/, "", name)
        if (!closing) {
          if (details && depth == detail_depth[details]) {
            if (first_child[details] == "") first_child[details] = name
            if (name == "summary") summaries[details]++
          }
          depth++
          if (name == "details") {
            details++
            detail_depth[details] = depth
            first_child[details] = ""
            summaries[details] = 0
            if (details > 2) bad = 1
          }
        } else {
          if (name == "details") {
            if (!details || depth != detail_depth[details] || summaries[details] != 1 ||
                first_child[details] != "summary") bad = 1
            delete detail_depth[details]
            delete first_child[details]
            delete summaries[details]
            details--
          }
          depth--
        }
      }
    }
    END { exit (bad || details || depth) ? 1 : 0 }
  ' "$1"
}

pr_explain_scroll_regions_are_well_formed() {
  awk '
    function has_class(tag, wanted,    value) {
      value = tag
      if (value !~ / class="[^"]*"/) return 0
      sub(/^.* class="/, "", value)
      sub(/".*$/, "", value)
      return index(" " value " ", " " wanted " ") != 0
    }
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<\//)
        name = tag
        sub(/^<\/?/, "", name)
        sub(/[[:space:]>].*$/, "", name)
        if (!closing) {
          parent_depth = depth
          depth++
          if (has_class(tag, "rx-scroll")) {
            if (scroll || tag !~ /^<div class="rx-scroll" role="region" tabindex="0" aria-label="[^"]+">$/) bad = 1
            scroll = 1
            scroll_depth = depth
            contents = 0
          }
          if (has_class(tag, "rx-code") || has_class(tag, "rx-compare")) {
            seen_contents++
            if (!scroll || parent_depth != scroll_depth) bad = 1
            else contents++
          }
        } else {
          if (scroll && name == "div" && depth == scroll_depth) {
            if (contents != 1) bad = 1
            scroll = 0
            seen_scrolls++
          }
          depth--
        }
      }
    }
    END { exit (bad || scroll || seen_scrolls != seen_contents) ? 1 : 0 }
  ' "$1"
}

pr_explain_body_is_well_nested() {
  awk '
    BEGIN {
      depth = 0
      bad = 0
    }
    {
      rest = $0
      while (match(rest, /<[^>]+>/)) {
        token = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (token ~ /^<[[:space:]]*\//)
        name = token
        sub(/^<[[:space:]]*\/?[[:space:]]*/, "", name)
        sub(/[[:space:]\/>].*$/, "", name)
        name = tolower(name)
        if (name == "br") continue
        if (name == "hr") {
          if (paragraph) bad = 1
          continue
        }
        if (closing) {
          if (depth == 0 || stack[depth] != name) bad = 1
          else depth--
        } else {
          if (paragraph && name ~ /^(article|aside|blockquote|details|div|dl|figure|footer|h[1-6]|header|hr|main|nav|ol|p|pre|section|table|ul)$/) bad = 1
          if ((name == "li" && stack[depth] == "li") ||
              (name ~ /^(dt|dd)$/ && stack[depth] ~ /^(dt|dd)$/) ||
              (name == "tr" && stack[depth] == "tr") ||
              (name ~ /^(th|td)$/ && stack[depth] ~ /^(th|td)$/)) bad = 1
          depth++
          stack[depth] = name
          if (name == "p") paragraph = depth
        }
        if (closing && name == "p" && paragraph == depth + 1) {
          paragraph = 0
        }
      }
    }
    END { exit (bad || depth != 0) ? 1 : 0 }
  ' "$1"
}

pr_explain_figures_are_well_formed() {
  awk '
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
          if (figure && depth == figure_depth) {
            last_child = name
            if (name == "figcaption") {
              captions++
              if (tag != "<figcaption class=\"rx-caption\">") bad = 1
            }
          }
          depth++
          if (name == "figure") {
            if (figure) bad = 1
            figure = 1
            figure_depth = depth
            captions = 0
            last_child = ""
            if (tag !~ /class="rx-figure( |")/) bad = 1
          }
        } else {
          if (figure && name == "figure" && depth == figure_depth) {
            if (captions != 1 || last_child != "figcaption") bad = 1
            figure = 0
          }
          depth--
        }
      }
    }
    END { exit (bad || figure) ? 1 : 0 }
  ' "$1"
}

pr_explain_captions_are_substantive() {
  awk '
    function words(text,    parts, count) {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", text)
      if (text == "") return 0
      return split(text, parts, /[[:space:]]+/)
    }
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        text = substr(rest, 1, RSTART - 1)
        if (caption) content = content " " text
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        if (tag == "<figcaption class=\"rx-caption\">") {
          if (caption) bad = 1
          caption = 1
          content = ""
          seen++
        } else if (tag == "</figcaption>") {
          if (!caption || words(content) < 5) bad = 1
          caption = 0
        }
      }
      if (caption) content = content " " rest
    }
    END { exit (bad || caption) ? 1 : 0 }
  ' "$1"
}

pr_explain_callouts_are_well_formed() {
  awk '
    function words(text,    parts) {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", text)
      if (text == "") return 0
      return split(text, parts, /[[:space:]]+/)
    }
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        text = substr(rest, 1, RSTART - 1)
        if (label) content = content " " text
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<\//)
        name = tag
        sub(/^<\/?/, "", name)
        sub(/[[:space:]>].*$/, "", name)
        if (!closing) {
          parent_depth = depth
          depth++
          if (tag ~ /^<aside class="rx-callout"([[:space:]>])/) {
            if (callout) bad = 1
            callout = 1
            callout_depth = depth
            labels = 0
            seen++
          } else if (callout && parent_depth == callout_depth && tag == "<p class=\"rx-callout-label\">") {
            labels++
            label = 1
            label_depth = depth
            content = ""
          }
        } else {
          if (label && name == "p" && depth == label_depth) {
            if (words(content) < 1) bad = 1
            label = 0
          }
          if (callout && name == "aside" && depth == callout_depth) {
            if (labels != 1 || label) bad = 1
            callout = 0
          }
          depth--
        }
      }
      if (label) content = content " " rest
    }
    END { exit (bad || callout || label) ? 1 : 0 }
  ' "$1"
}

pr_explain_summary_is_well_formed() {
  awk '
    function has_class(tag, wanted,    value) {
      value = tag
      if (value !~ / class="[^"]*"/) return 0
      sub(/^.* class="/, "", value)
      sub(/".*$/, "", value)
      return index(" " value " ", " " wanted " ") != 0
    }
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<\//)
        name = tag
        sub(/^<\/?/, "", name)
        sub(/[[:space:]>].*$/, "", name)
        if (!closing) {
          parent_depth = depth
          depth++
          if (has_class(tag, "rx-summary")) {
            if (summary) bad = 1
            summary = 1
            summary_depth = depth
            headings = decisions = lists = 0
            seen++
          } else if (summary && parent_depth == summary_depth && name == "h2") {
            headings++
          } else if (summary && parent_depth == summary_depth && has_class(tag, "rx-decision")) {
            decisions++
          } else if (summary && parent_depth == summary_depth && has_class(tag, "rx-summary-cards")) {
            lists++
            cards_depth = depth
            cards = 0
          } else if (cards_depth && parent_depth == cards_depth && has_class(tag, "rx-card")) {
            cards++
          }
        } else {
          if (cards_depth && name == "ul" && depth == cards_depth) {
            if (cards != 5) bad = 1
            cards_depth = 0
          }
          if (summary && name == "section" && depth == summary_depth) {
            if (headings != 1 || decisions != 1 || lists != 1 || cards_depth) bad = 1
            summary = 0
          }
          depth--
        }
      }
    }
    END { exit (bad || summary || cards_depth || seen != 1) ? 1 : 0 }
  ' "$1"
}

pr_explain_branches_are_well_formed() {
  awk '
    function has_class(tag, wanted,    value) {
      value = tag
      if (value !~ / class="[^"]*"/) return 0
      sub(/^.* class="/, "", value)
      sub(/".*$/, "", value)
      return index(" " value " ", " " wanted " ") != 0
    }
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<\//)
        name = tag
        sub(/^<\/?/, "", name)
        sub(/[[:space:]>].*$/, "", name)
        if (!closing) {
          parent_depth = depth
          depth++
          if (has_class(tag, "rx-branch")) {
            if (branch) bad = 1
            branch = 1
            branch_depth = depth
            tests = arms = 0
            seen++
          } else if (branch && parent_depth == branch_depth && has_class(tag, "rx-branch-test")) {
            tests++
          } else if (branch && parent_depth == branch_depth && has_class(tag, "rx-branch-arms")) {
            arms++
          }
        } else {
          if (branch && name == "div" && depth == branch_depth) {
            if (tests != 1 || arms != 1) bad = 1
            branch = 0
          }
          depth--
        }
      }
    }
    END { exit (bad || branch) ? 1 : 0 }
  ' "$1"
}

pr_explain_timelines_are_well_formed() {
  awk '
    function has_class(tag, wanted,    value) {
      value = tag
      if (value !~ / class="[^"]*"/) return 0
      sub(/^.* class="/, "", value)
      sub(/".*$/, "", value)
      return index(" " value " ", " " wanted " ") != 0
    }
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        text = substr(rest, 1, RSTART - 1)
        if (state_text) content = content " " text
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<\//)
        name = tag
        sub(/^<\/?/, "", name)
        sub(/[[:space:]>].*$/, "", name)
        if (!closing) {
          parent_depth = depth
          depth++
          if (has_class(tag, "rx-timeline")) {
            if (timeline) bad = 1
            timeline = 1
            timeline_depth = depth
            items = 0
          } else if (timeline && parent_depth == timeline_depth && name == "li") {
            if (item || tag !~ /^<li data-state="(done|now|blocked|planned|correction|dead-end)">$/) bad = 1
            item = 1
            item_depth = depth
            states = 0
            items++
          } else if (item && parent_depth == item_depth && has_class(tag, "rx-state")) {
            states++
            state_text = 1
            state_depth = depth
            content = ""
          }
        } else {
          if (state_text && name == "p" && depth == state_depth) {
            gsub(/[[:space:]]+/, "", content)
            if (content == "") bad = 1
            state_text = 0
          }
          if (item && name == "li" && depth == item_depth) {
            if (states != 1 || state_text) bad = 1
            item = 0
          }
          if (timeline && name == "ol" && depth == timeline_depth) {
            if (items < 1 || item) bad = 1
            timeline = 0
          }
          depth--
        }
      }
      if (state_text) content = content " " rest
    }
    END { exit (bad || timeline || item || state_text) ? 1 : 0 }
  ' "$1"
}

pr_explain_comparisons_are_well_formed() {
  awk '
    function has_class(tag, wanted,    value) {
      value = tag
      if (value !~ / class="[^"]*"/) return 0
      sub(/^.* class="/, "", value)
      sub(/".*$/, "", value)
      return index(" " value " ", " " wanted " ") != 0
    }
    function words(text,    parts) {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", text)
      if (text == "") return 0
      return split(text, parts, /[[:space:]]+/)
    }
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        text = substr(rest, 1, RSTART - 1)
        if (caption) content = content " " text
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<\//)
        name = tag
        sub(/^<\/?/, "", name)
        sub(/[[:space:]>].*$/, "", name)
        if (!closing) {
          parent_depth = depth
          depth++
          if (has_class(tag, "rx-compare")) {
            if (table) bad = 1
            table = 1
            table_depth = depth
            captions = 0
            first_child = ""
          } else if (table && parent_depth == table_depth) {
            if (first_child == "") first_child = name
            if (name == "caption") {
              captions++
              caption = 1
              caption_depth = depth
              content = ""
            }
          }
        } else {
          if (caption && name == "caption" && depth == caption_depth) {
            if (words(content) < 5) bad = 1
            caption = 0
          }
          if (table && name == "table" && depth == table_depth) {
            if (captions != 1 || first_child != "caption" || caption) bad = 1
            table = 0
          }
          depth--
        }
      }
      if (caption) content = content " " rest
    }
    END { exit (bad || table || caption) ? 1 : 0 }
  ' "$1"
}

pr_explain_headings_are_ordered() {
  awk '
    {
      rest = $0
      while (match(rest, /<h[1-6]([[:space:]>])/)) {
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        level = substr(tag, 3, 1) + 0
        if (!seen && level != 1) bad = 1
        if (seen && level > previous + 1) bad = 1
        previous = level
        seen = 1
      }
    }
    END { exit (bad || !seen) ? 1 : 0 }
  ' "$1"
}

pr_explain_quizzes_are_well_formed() {
  awk '
    function attr(tag, key,    text) {
      text = tag
      if (text !~ (key "=\"[^\"]+\"")) return ""
      sub("^.*" key "=\"", "", text)
      sub("\".*$", "", text)
      return text
    }
    function words(text,    parts) {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", text)
      if (text == "") return 0
      return split(text, parts, /[[:space:]]+/)
    }
    function finish_item() {
      if (choices != 4 || correct != 1 || incorrect != 3 || misconceptions != 3 ||
          summaries != 4 || feedback != 4 || headings != 1 || results != 1 ||
          group == "" || group_bad) bad = 1
      if (used[group]) bad = 1
      used[group] = 1
    }
    {
      rest = $0
      while (match(rest, /<[^>]+>/)) {
        text = substr(rest, 1, RSTART - 1)
        if (feedback_open) feedback_text = feedback_text " " text
        if (summary_open) summary_text = summary_text " " text
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<[[:space:]]*\//)
        name = tag
        sub(/^<[[:space:]]*\/?[[:space:]]*/, "", name)
        sub(/[[:space:]\/>].*$/, "", name)
        name = tolower(name)

        if (!closing) {
          parent_depth = depth
          depth++
          if (tag ~ /^<article class="rx-quiz-item"/) {
            if (item) bad = 1
            item = 1
            item_depth = depth
            choices = correct = incorrect = misconceptions = summaries = feedback = headings = results = 0
            group = ""
            group_bad = 0
            seen++
          } else if (item && tag ~ /^<details class="rx-choice"/) {
            choices++
            choice_depth = depth
            choice_feedback = choice_summaries = 0
            choice_group = attr(tag, "name")
            if (choice_group == "") group_bad = 1
            if (group == "") group = choice_group
            else if (group != choice_group) group_bad = 1
            if (tag ~ /data-correct="true"/) correct++
            else if (tag ~ /data-correct="false"/) incorrect++
            else group_bad = 1
            if (tag ~ /data-correct="false"/ && attr(tag, "data-misconception") != "") misconceptions++
          } else if (item && choice_depth && parent_depth == choice_depth && name == "summary") {
            summaries++
            choice_summaries++
            summary_open = 1
            summary_depth = depth
            summary_text = ""
          } else if (item && choice_depth && parent_depth == choice_depth && name == "p") {
            feedback++
            choice_feedback++
            feedback_open = 1
            feedback_depth = depth
            feedback_text = ""
          } else if (item && depth == item_depth + 1 && name == "h3") {
            headings++
          } else if (item && depth == item_depth + 1 && tag ~ /^<p class="rx-quiz-result" aria-live="polite">/) {
            results++
          }
        } else {
          if (feedback_open && name == "p" && depth == feedback_depth) {
            if (words(feedback_text) < 8) group_bad = 1
            feedback_open = 0
          }
          if (summary_open && name == "summary" && depth == summary_depth) {
            if (words(summary_text) < 1) group_bad = 1
            summary_open = 0
          }
          if (item && name == "details" && depth == choice_depth) {
            if (choice_feedback != 1 || choice_summaries != 1 || feedback_open || summary_open) group_bad = 1
            choice_depth = 0
          }
          if (item && name == "article" && depth == item_depth) {
            finish_item()
            item = 0
          }
          depth--
        }
      }
      if (feedback_open) feedback_text = feedback_text " " rest
      if (summary_open) summary_text = summary_text " " rest
    }
    END { if (item || seen != 5) bad = 1; exit bad ? 1 : 0 }
  ' "$1"
}

pr_explain_lanes_are_well_formed() {
  awk '
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
          depth++
          if (tag ~ /^<div class="rx-lanes"/) {
            if (lanes) bad = 1
            lanes = 1
            lanes_depth = depth
            lane_total = 0
            aligned = (tag ~ /data-align="columns"/)
            aligned_steps = -1
          } else if (tag ~ /^<section class="rx-lane"/) {
            if (!lanes || lane || parent_depth != lanes_depth) bad = 1
            lane = 1
            lane_depth = depth
            labels = flows = lane_steps = 0
            lane_total++
            seen_lanes++
          } else if (lane && tag ~ /^<h4 class="rx-lane-label"/) {
            labels++
          } else if (lane && tag ~ /^<ol class="rx-flow" data-steps="[0-9]+">$/) {
            flows++
            text = tag
            sub(/^.*data-steps="/, "", text)
            sub(/".*/, "", text)
            lane_steps = text + 0
          }
        } else {
          if (lane && name == "section" && depth == lane_depth) {
            if (labels != 1 || flows != 1) bad = 1
            if (aligned) {
              if (aligned_steps < 0) aligned_steps = lane_steps
              else if (aligned_steps != lane_steps) bad = 1
            }
            lane = 0
          }
          if (lanes && name == "div" && depth == lanes_depth) {
            if (lane || lane_total < 2) bad = 1
            lanes = 0
          }
          depth--
        }
      }
    }
    END { if (lanes || lane) bad = 1; print (bad + 0) ":" (seen_lanes + 0) }
  ' "$1"
}

pr_explain_sequences_are_well_formed() {
  awk '
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
          depth++
          if (tag ~ /^<section class="rx-sequence"/) {
            if (sequence) bad = 1
            sequence = 1
            sequence_depth = depth
            headings = lists = steps = titles = 0
            seen++
          } else if (sequence && parent_depth == sequence_depth && name == "h3") {
            headings++
          } else if (sequence && parent_depth == sequence_depth && tag ~ /^<ol class="rx-seq-steps"/) {
            lists++
            list_depth = depth
          } else if (sequence && list_depth && parent_depth == list_depth && tag ~ /^<li class="rx-seq-step"/) {
            if (step_depth) bad = 1
            steps++
            step_depth = depth
            step_titles = 0
          } else if (sequence && step_depth && parent_depth == step_depth && tag ~ /^<h4 class="rx-seq-title"/) {
            titles++
            step_titles++
          }
        } else {
          if (sequence && name == "li" && depth == step_depth) {
            if (step_titles != 1) bad = 1
            step_depth = 0
          }
          if (sequence && name == "ol" && depth == list_depth) list_depth = 0
          if (sequence && name == "section" && depth == sequence_depth) {
            if (headings != 1 || lists != 1 || steps < 2 || titles != steps) bad = 1
            sequence = 0
          }
          depth--
        }
      }
    }
    END { if (sequence) bad = 1; print (bad + 0) ":" (seen + 0) }
  ' "$1"
}

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
  local class_value class_name id ref value role state field
  local generated_date

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
    rx-skip rx-masthead rx-eyebrow rx-lede rx-summary rx-decision rx-summary-cards rx-card \
    rx-toc rx-claim rx-figure rx-caption rx-flow rx-branch rx-branch-test rx-branch-arms \
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
  done < <(grep -noE 'id="[A-Za-z][A-Za-z0-9_.:-]*"' "$report" 2>/dev/null || true)
  count=$(pr_explain_count_regex "$report" 'id="[^"]*"')
  [[ "$count" -eq "${#seen_ids[@]}" ]] ||
    pr_explain_validation_error E3 "every id must be non-empty and use the canonical identifier syntax"
  if grep -qE "(id|href|aria-labelledby|aria-describedby)[[:space:]]*=[[:space:]]*'" "$report"; then
    pr_explain_validation_error E3 "ID and reference attributes must use double quotes"
  fi
  while IFS= read -r value; do
    ref="${value#href=\"#}"
    ref="${ref%\"}"
    [[ -n "$ref" && -n "${seen_ids[$ref]:-}" ]] ||
      pr_explain_validation_error E3 "dangling fragment link '#$ref'"
  done < <(grep -Eo 'href="#[^"]*"' "$report" 2>/dev/null || true)
  count=$(pr_explain_count_regex "$report" '<a([[:space:]>])')
  ref=$(pr_explain_count_regex "$report" '<a[^>]+href="#[^"]+"')
  [[ "$count" -eq "$ref" ]] || pr_explain_validation_error E2 "anchors must use non-empty same-document fragments"
  while IFS= read -r value; do
    value="${value#*=\"}"
    value="${value%\"}"
    for ref in $value; do
      [[ -n "${seen_ids[$ref]:-}" ]] || pr_explain_validation_error E3 "dangling accessibility reference '$ref'"
    done
  done < <(grep -Eo '(aria-labelledby|aria-describedby|data-hl)="[^"]+"' "$report" 2>/dev/null || true)

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
  [[ "$quiz_count" -eq 5 ]] || pr_explain_validation_error E10 "quiz must contain exactly five items"
  [[ "$choice_count" -eq 20 ]] || pr_explain_validation_error E10 "each quiz item must contain four choices"
  [[ "$correct_count" -eq 5 ]] || pr_explain_validation_error E10 "each quiz item must contain exactly one correct choice"
  [[ "$false_count" -eq 15 && "$misconception_count" -eq 15 ]] ||
    pr_explain_validation_error E10 "every incorrect choice needs a misconception label"
  pr_explain_quizzes_are_well_formed "$body_html" ||
    pr_explain_validation_error E10 "each quiz item needs one heading, four grouped choices, one key, three misconception distractors, feedback, and one live result"
  if grep -qiE '<(button|input|select)([[:space:]>])' "$report"; then
    pr_explain_validation_error E10 "authored controls are forbidden; no-JS details carry interaction"
  fi
  count=$(pr_explain_count_regex "$report" '<summary>')
  [[ "$count" -ge 20 ]] || pr_explain_validation_error E10 "quiz choices need visible summary text"
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
    grep -Fq "<dt data-field=\"runner\">Runner</dt><dd>$runner subscription CLI</dd>" "$provenance_html" ||
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
