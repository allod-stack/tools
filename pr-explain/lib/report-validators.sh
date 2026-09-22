# shellcheck shell=bash
# The report validators: about thirty self-contained predicates over the
# assembled report body (the *_are_well_formed, *_are_canonical, and
# *_are_semantic checks, plus check_reading_cost, check_layers,
# check_objectives_block, check_objective_coverage, check_quiz_v2,
# visible_prose, and check_slop) and their private helpers. Each is called
# by lib/report.sh over the assembled document. Depends on lib/common.sh
# (count_regex, validation_error, validation_warning). pr_explain_visible_prose
# also calls pr_explain_strip_quoted_code, which lives in lib/report.sh,
# sourced after this file — sourcing order still resolves it because the
# call happens later, when pr_explain_validate_report runs, not when this
# file is sourced.

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
      expected["rx-cost"] = "p"
      expected["rx-summary"] = "section"
      expected["rx-decision"] = "p"
      expected["rx-summary-cards"] = "ul"
      expected["rx-card"] = "li"
      expected["rx-toc"] = "nav"
      expected["rx-claim"] = "p"
      expected["rx-objectives"] = "ol"
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
    END { if (item || seen < 3 || seen > 7) bad = 1; exit bad ? 1 : 0 }
  ' "$1"
}

pr_explain_check_reading_cost() {
  local body="$1" value total in_masthead after_lede text_ok
  value=$(awk '
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
        if (cost_open) content = content " " text
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<\//)
        name = tag
        sub(/^<\/?/, "", name)
        sub(/[[:space:]>].*$/, "", name)
        if (!closing) {
          parent_depth = depth
          depth++
          if (name == "header" && has_class(tag, "rx-masthead")) {
            masthead = 1
            masthead_depth = depth
            lede_seen = 0
          } else if (masthead && parent_depth == masthead_depth && has_class(tag, "rx-lede")) {
            lede_seen = 1
          }
          if (has_class(tag, "rx-cost")) {
            total++
            if (masthead && parent_depth == masthead_depth) {
              in_masthead++
              if (lede_seen) after_lede++
              cost_open = 1
              cost_depth = depth
              content = ""
            }
          }
        } else {
          if (cost_open && name == "p" && depth == cost_depth) {
            stripped = content
            gsub(/[[:space:]]+/, "", stripped)
            if (stripped != "" && index(tolower(content), "minute")) text_ok++
            cost_open = 0
          }
          if (masthead && name == "header" && depth == masthead_depth) masthead = 0
          depth--
        }
      }
      if (cost_open) content = content " " rest
    }
    END { print (total + 0) ":" (in_masthead + 0) ":" (after_lede + 0) ":" (text_ok + 0) }
  ' "$body")
  IFS=: read -r total in_masthead after_lede text_ok <<<"$value"
  if [[ "$in_masthead" -ne 1 ]]; then
    pr_explain_validation_error E21 "masthead must directly contain exactly one p.rx-cost reading-cost line"
  else
    [[ "$after_lede" -eq 1 ]] ||
      pr_explain_validation_error E21 "p.rx-cost must come after p.rx-lede inside the masthead"
    [[ "$text_ok" -eq 1 ]] ||
      pr_explain_validation_error E21 "p.rx-cost must state the reading cost in non-empty text containing the word 'minute'"
  fi
  [[ "$total" -eq "$in_masthead" ]] ||
    pr_explain_validation_error E21 "p.rx-cost is allowed only inside the masthead"
}

pr_explain_check_layers() {
  local body="$1" kind label value
  while IFS=$'\t' read -r kind label value; do
    case "$kind" in
      missing)
        pr_explain_validation_error E22 "main section '$label' is missing its data-layer attribute" ;;
      badvalue)
        pr_explain_validation_error E22 "main section '$label' has unknown data-layer '$value'; use concept, mechanism, or receipts" ;;
      order)
        pr_explain_validation_error E22 "main section '$label' ($value) breaks the non-decreasing concept, mechanism, receipts layer order" ;;
      misplaced)
        pr_explain_validation_error E22 "data-layer on '$label' is allowed only on direct section children of main" ;;
      summary)
        [[ "$value" -ge 1 ]] ||
          pr_explain_validation_error E22 "main must contain at least one data-layer=\"concept\" section" ;;
    esac
  done < <(awk '
    function attr(tag, key,    text) {
      text = tag
      if (text !~ (" " key "=\"")) return ""
      sub("^.* " key "=\"", "", text)
      sub(/".*$/, "", text)
      return text
    }
    BEGIN { rank["concept"] = 1; rank["mechanism"] = 2; rank["receipts"] = 3 }
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
          if (name == "main") {
            main = 1
            main_depth = depth
          } else if (main && name == "section" && parent_depth == main_depth) {
            sections++
            id = attr(tag, "id")
            label = (id == "" ? "main section " sections : "#" id)
            if (tag !~ / data-layer="/) {
              print "missing\t" label
            } else {
              value = attr(tag, "data-layer")
              if (!(value in rank)) {
                print "badvalue\t" label "\t" value
              } else {
                if (rank[value] < prev) print "order\t" label "\t" value
                else prev = rank[value]
                if (value == "concept") concepts++
              }
            }
          } else if (tag ~ / data-layer="/) {
            print "misplaced\t" name
          }
        } else {
          if (main && name == "main" && depth == main_depth) main = 0
          depth--
        }
      }
    }
    END { print "summary\t" (sections + 0) "\t" (concepts + 0) }
  ' "$body")
}

pr_explain_check_objectives_block() {
  local body="$1" kind a b
  local first="" layer="" inside=0 outside=0
  local -a ids=() texts=()
  while IFS=$'\t' read -r kind a b; do
    case "$kind" in
      first) first="$a" ;;
      layer) layer="$a" ;;
      lists) inside="$a" outside="$b" ;;
      item) ids+=("$a") texts+=("$b") ;;
    esac
  done < <(awk '
    function has_class(tag, wanted,    value) {
      value = tag
      if (value !~ / class="[^"]*"/) return 0
      sub(/^.* class="/, "", value)
      sub(/".*$/, "", value)
      return index(" " value " ", " " wanted " ") != 0
    }
    function attr(tag, key,    text) {
      text = tag
      if (text !~ (" " key "=\"")) return ""
      sub("^.* " key "=\"", "", text)
      sub(/".*$/, "", text)
      return text
    }
    {
      rest = $0
      while (match(rest, /<[^>]*>/)) {
        text = substr(rest, 1, RSTART - 1)
        if (li_open) content = content " " text
        tag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        closing = (tag ~ /^<\//)
        name = tag
        sub(/^<\/?/, "", name)
        sub(/[[:space:]>].*$/, "", name)
        if (!closing) {
          parent_depth = depth
          depth++
          if (name == "main") {
            main = 1
            main_depth = depth
          } else if (main && name == "section" && parent_depth == main_depth) {
            sections++
            id = attr(tag, "id")
            if (sections == 1) {
              print "first\t" (id == "" ? "(unnamed)" : id)
              print "layer\t" attr(tag, "data-layer")
            }
            if (id == "objectives" && !objsec_depth) {
              objsec = 1
              objsec_depth = depth
            }
          }
          if (has_class(tag, "rx-objectives")) {
            if (objsec) {
              inside++
              if (inside == 1) {
                list = 1
                list_depth = depth
              }
            } else outside++
          } else if (list && name == "li" && parent_depth == list_depth) {
            li_open = 1
            li_depth = depth
            li_id = attr(tag, "id")
            content = ""
          }
        } else {
          if (li_open && name == "li" && depth == li_depth) {
            gsub(/[[:space:]]+/, "", content)
            print "item\t" (li_id == "" ? "(missing)" : li_id) "\t" (content == "" ? 0 : 1)
            li_open = 0
          }
          if (list && name == "ol" && depth == list_depth) list = 0
          if (objsec && name == "section" && depth == objsec_depth) objsec = 0
          if (main && name == "main" && depth == main_depth) main = 0
          depth--
        }
      }
      if (li_open) content = content " " rest
    }
    END {
      if (sections == 0) print "first\tnone"
      print "lists\t" (inside + 0) "\t" (outside + 0)
    }
  ' "$body")
  if [[ -z "$first" || "$first" == none ]]; then
    pr_explain_validation_error E23 "main must open with a section id=\"objectives\""
  elif [[ "$first" != objectives ]]; then
    pr_explain_validation_error E23 "first main section must be '#objectives' (found '#$first')"
  else
    [[ "$layer" == concept ]] ||
      pr_explain_validation_error E23 "'#objectives' must declare data-layer=\"concept\""
    [[ "$inside" -eq 1 ]] ||
      pr_explain_validation_error E23 "'#objectives' must contain exactly one ol.rx-objectives"
  fi
  [[ "$outside" -eq 0 ]] ||
    pr_explain_validation_error E23 "ol.rx-objectives is allowed only inside '#objectives'"
  local count="${#ids[@]}" i id previous_id="" previous_number=0 number
  if [[ "$inside" -eq 1 ]] && [[ "$count" -lt 1 || "$count" -gt 7 ]]; then
    pr_explain_validation_error E23 "objectives list must contain between one and seven objectives (found $count)"
  fi
  for ((i = 0; i < count; i++)); do
    id="${ids[$i]}"
    if [[ ! "$id" =~ ^obj-[0-9]+$ ]]; then
      pr_explain_validation_error E23 "objective id '$id' must match obj-N"
    else
      number="${id#obj-}"
      if [[ -n "$previous_id" && "$number" -le "$previous_number" ]]; then
        pr_explain_validation_error E23 "objective ids must be unique and ascending ('$id' follows '$previous_id')"
      fi
      previous_id="$id"
      previous_number="$number"
    fi
    [[ "${texts[$i]}" -eq 1 ]] ||
      pr_explain_validation_error E23 "objective '$id' must contain visible text"
  done
}

pr_explain_check_objective_coverage() {
  local body="$1" line kind owner label value ref_id
  local -a lines=() objective_order=()
  declare -A known=() claimed_by_section=() claimed_by_quiz=()
  while IFS= read -r line; do
    lines+=("$line")
  done < <(awk '
    function has_class(tag, wanted,    value) {
      value = tag
      if (value !~ / class="[^"]*"/) return 0
      sub(/^.* class="/, "", value)
      sub(/".*$/, "", value)
      return index(" " value " ", " " wanted " ") != 0
    }
    function attr(tag, key,    text) {
      text = tag
      if (text !~ (" " key "=\"")) return ""
      sub("^.* " key "=\"", "", text)
      sub(/".*$/, "", text)
      return text
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
          if (name == "main") {
            main = 1
            main_depth = depth
          } else if (main && name == "section" && parent_depth == main_depth) {
            sections++
            id = attr(tag, "id")
            label = (id == "" ? "main section " sections : "#" id)
            if (id == "objectives" && !objsec_depth) {
              objsec = 1
              objsec_depth = depth
            }
            if (tag ~ / data-objective="/) print "ref\tsection\t" label "\t" attr(tag, "data-objective")
            else if (id != "objectives") print "untagged\tsection\t" label
          } else if (name == "figure" && has_class(tag, "rx-figure")) {
            figures++
            id = attr(tag, "id")
            label = (id == "" ? "rx-figure " figures : "#" id)
            if (tag ~ / data-objective="/) print "ref\tfigure\t" label "\t" attr(tag, "data-objective")
            else print "untagged\tfigure\t" label
          } else if (has_class(tag, "rx-quiz-item")) {
            quizzes++
            id = attr(tag, "id")
            label = (id == "" ? "quiz item " quizzes : "#" id)
            if (tag ~ / data-objective="/) print "ref\tquiz\t" label "\t" attr(tag, "data-objective")
          } else if (tag ~ / data-objective="/) {
            print "misplaced\t" name
          }
          if (objsec && has_class(tag, "rx-objectives") && !list_done) {
            list = 1
            list_depth = depth
          } else if (list && name == "li" && parent_depth == list_depth) {
            id = attr(tag, "id")
            if (id != "") print "objective\t" id
          }
        } else {
          if (list && name == "ol" && depth == list_depth) {
            list = 0
            list_done = 1
          }
          if (objsec && name == "section" && depth == objsec_depth) objsec = 0
          if (main && name == "main" && depth == main_depth) main = 0
          depth--
        }
      }
    }
  ' "$body")
  for line in "${lines[@]}"; do
    IFS=$'\t' read -r kind owner label value <<<"$line"
    if [[ "$kind" == objective ]]; then
      known["$owner"]=1
      objective_order+=("$owner")
    fi
  done
  for line in "${lines[@]}"; do
    IFS=$'\t' read -r kind owner label value <<<"$line"
    case "$kind" in
      misplaced)
        pr_explain_validation_error E24 "data-objective on '$owner' is allowed only on main sections, rx-figure figures, and quiz items" ;;
      untagged)
        pr_explain_validation_error E24 "$owner '$label' must declare the objectives it serves with data-objective" ;;
      ref)
        if [[ -z "${value//[[:space:]]/}" ]]; then
          pr_explain_validation_error E24 "data-objective on $owner '$label' must name at least one objective id"
          continue
        fi
        for ref_id in $value; do
          if [[ -n "${known[$ref_id]:-}" ]]; then
            case "$owner" in
              section) claimed_by_section["$ref_id"]=1 ;;
              quiz) claimed_by_quiz["$ref_id"]=1 ;;
            esac
          else
            pr_explain_validation_error E24 "data-objective on $owner '$label' names unknown objective '$ref_id'"
          fi
        done
        ;;
    esac
  done
  for ref_id in "${objective_order[@]}"; do
    [[ -n "${claimed_by_section[$ref_id]:-}" ]] ||
      pr_explain_validation_error E24 "objective '$ref_id' is not claimed by any main section"
    [[ -n "${claimed_by_quiz[$ref_id]:-}" ]] ||
      pr_explain_validation_error E24 "objective '$ref_id' is not tested by any quiz item"
  done
}

pr_explain_check_quiz_v2() {
  local body="$1" kind qid concept objective position letter
  declare -A correct_positions=()
  while IFS=$'\t' read -r kind qid concept objective position; do
    case "$kind" in
      misplaced)
        pr_explain_validation_error E10 "data-concept on '$qid' is allowed only on article.rx-quiz-item" ;;
      item)
        [[ "$concept" == present ]] ||
          pr_explain_validation_error E10 "quiz item '$qid' must declare a non-empty data-concept"
        if [[ "$objective" == "-" || "$objective" != "${objective//[[:space:]]/}" || -z "${objective//[[:space:]]/}" ]]; then
          pr_explain_validation_error E10 "quiz item '$qid' must declare data-objective naming exactly one objective id"
        fi
        case "$position" in
          1) letter=A ;;
          2) letter=B ;;
          3) letter=C ;;
          4) letter=D ;;
          *) letter="" ;;
        esac
        [[ -z "$letter" ]] || correct_positions["$letter"]=$(( ${correct_positions[$letter]:-0} + 1 ))
        ;;
    esac
  done < <(awk '
    function has_class(tag, wanted,    value) {
      value = tag
      if (value !~ / class="[^"]*"/) return 0
      sub(/^.* class="/, "", value)
      sub(/".*$/, "", value)
      return index(" " value " ", " " wanted " ") != 0
    }
    function attr(tag, key,    text) {
      text = tag
      if (text !~ (" " key "=\"")) return ""
      sub("^.* " key "=\"", "", text)
      sub(/".*$/, "", text)
      return text
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
          depth++
          quiz_item = has_class(tag, "rx-quiz-item")
          if (tag ~ / data-concept="/ && !quiz_item) print "misplaced\t" name
          if (quiz_item) {
            item = 1
            item_depth = depth
            items++
            id = attr(tag, "id")
            qid = (id == "" ? "quiz item " items : "#" id)
            concept = attr(tag, "data-concept")
            has_objective = (tag ~ / data-objective="/)
            objective = attr(tag, "data-objective")
            choice = 0
            correct = 0
          } else if (item && has_class(tag, "rx-choice")) {
            choice++
            if (tag ~ / data-correct="true"/ && !correct) correct = choice
          }
        } else {
          if (item && name == "article" && depth == item_depth) {
            print "item\t" qid "\t" (concept == "" ? "-" : "present") "\t" \
              (has_objective ? (objective == "" ? "" : objective) : "-") "\t" correct
            item = 0
          }
          depth--
        }
      }
    }
  ' "$body")
  for letter in A B C D; do
    [[ "${correct_positions[$letter]:-0}" -le 2 ]] ||
      pr_explain_validation_error E10 "answer position $letter is correct more than twice across the quiz items"
  done
}

# Visible prose for the slop linter: the body with quoted code dropped, every
# tag (and so every attribute value) removed, and the five allowed entities
# plus typographic apostrophes decoded, so word-boundary matches see the same
# words a reader sees and code identifiers cannot false-positive.
pr_explain_visible_prose() {
  pr_explain_strip_quoted_code "$1" | awk '
    {
      rest = $0
      out = ""
      while (match(rest, /<[^>]*>/)) {
        out = out substr(rest, 1, RSTART - 1) " "
        rest = substr(rest, RSTART + RLENGTH)
      }
      out = out rest
      gsub(/&lt;/, "<", out)
      gsub(/&gt;/, ">", out)
      gsub(/&quot;/, "\"", out)
      gsub(/&apos;/, "\047", out)
      gsub("\342\200\231", "\047", out)
      gsub(/&amp;/, "\\&", out)
      print out
    }
  '
}

pr_explain_check_slop() {
  local prose="$1" term count words hedges trigram
  local -a banned=(
    delve delves delving tapestry testament seamless seamlessly
    utilize utilizes utilizing leverages leveraging
    "worth noting" "it is important to note" "in today's" "plays a vital role"
    "rich landscape" "crucial role"
  )
  for term in "${banned[@]}"; do
    count=$(pr_explain_count_regex "$prose" "\\b$term\\b")
    [[ "$count" -eq 0 ]] ||
      pr_explain_validation_error E25 "visible prose uses banned vocabulary '$term' ($count occurrence(s))"
  done
  while IFS=$'\t' read -r count trigram; do
    [[ -n "$trigram" ]] || continue
    pr_explain_validation_error E25 "the sentence opening '$trigram' repeats $count times; vary the claim structure"
  done < <(awk '
    { text = text " " $0 }
    END {
      n = split(text, sentences, /[.!?]+/)
      for (i = 1; i <= n; i++) {
        s = tolower(sentences[i])
        gsub("[^a-z0-9\047-]", " ", s)
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", s)
        k = split(s, w, /[[:space:]]+/)
        if (k >= 3) counts[w[1] " " w[2] " " w[3]]++
      }
      for (key in counts) if (counts[key] >= 4) print counts[key] "\t" key
    }
  ' "$prose")
  hedges=$(pr_explain_count_regex "$prose" '\b(may|might|could|perhaps|arguably|likely)\b')
  words=$(wc -w <"$prose")
  words="${words//[[:space:]]/}"
  if [[ "$words" -gt 0 ]] && (( hedges * 500 > words * 4 )); then
    pr_explain_validation_warning W13 \
      "visible prose hedges $hedges times in $words words (over 4 per 500); commit to what the evidence supports"
  fi
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

