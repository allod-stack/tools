# shellcheck shell=bash
# Fragment validation: the outline schema check and the per-pass tripwires
# (front matter, section, quiz) that stop a misdirected generation pass
# before the next provider call spends money continuing from it. Calls no
# other file under pr-explain/lib. pr_explain_check_front_fragment,
# pr_explain_check_section_fragment, and pr_explain_check_quiz_fragment call
# die, which no file under pr-explain/lib defines; it is caller-provided by
# explain. This file checks only fragment shape and placement; the full
# grammar is enforced later by lib/report-validators.sh over the assembled
# report.

# The section plan is provider output, so it is held to a fixed schema before
# any section pass spends a provider call continuing from it: exact keys,
# kebab-case ids outside the reserved shell names, monotonic layers with at
# least one concept section, and objective/concept references that resolve
# into the (already schema-validated) triage judgment, which the plan must
# jointly cover. One tolerance: a triage forced past a T0 decline carries no
# objective or concept vocabulary at all, so when the triage list is empty the
# membership checks stand down and the ids are held to shape only — the
# assembled-body validator still enforces the report's internal bookkeeping.
# There is no outline repair pass; an invalid plan fails the run closed.
pr_explain_validate_outline() {
  local outline="$1" triage="$2"
  jq -e --slurpfile triage "$triage" '
    ($triage[0].budget.max_sections) as $max
    | ($triage[0].objectives | map(.id)) as $objective_ids
    | ($triage[0].concepts | map(.slug)) as $concept_slugs
    | (type == "object")
    and (((keys | sort) == ["sections"]) or ((keys | sort) == ["notes","sections"]))
    and ((.notes // "") | type == "string")
    and (.sections | type == "array" and length >= 1 and length <= $max)
    and (.sections | all(.[];
        type == "object" and ((keys | sort) == ["concepts","gist","id","layer","objectives","title"])
        and (.id | type == "string" and test("^[a-z0-9]+(-[a-z0-9]+)*$"))
        and ((.id | IN("objectives","quiz")) | not)
        and ((.id | startswith("rx-")) | not)
        and (.title | type == "string" and length > 0)
        and (.layer | type == "string" and IN("concept","mechanism","receipts"))
        and (.gist | type == "string" and length > 0)
        and (.objectives | type == "array" and length >= 1
          and all(.[]; type == "string" and test("^obj-[0-9]+$")
            and (($objective_ids | length == 0) or IN($objective_ids[]))))
        and (.concepts | type == "array"
          and all(.[]; type == "string"
            and (($concept_slugs | length == 0) or IN($concept_slugs[]))))))
    and ((.sections | map(.id) | length) == (.sections | map(.id) | unique | length))
    and ((.sections | map(if .layer == "concept" then 0 elif .layer == "mechanism" then 1 else 2 end)) as $ranks
      | ($ranks | sort) == $ranks)
    and (.sections | map(.layer) | any(. == "concept"))
    and (($objective_ids - (.sections | map(.objectives[]) | unique)) == [])
  ' "$outline" >/dev/null 2>&1
}

pr_explain_layer_rank() {
  case "$1" in
    concept) printf '0\n' ;;
    mechanism) printf '1\n' ;;
    receipts) printf '2\n' ;;
    *) printf '3\n' ;;
  esac
}

# First and last non-blank line of a fragment, with surrounding whitespace
# trimmed: the placement checks care about which tag a fragment starts and
# ends on, not how the author indented it.
pr_explain_fragment_first_line() {
  grep -m 1 -v '^[[:space:]]*$' "$1" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' || true
}

pr_explain_fragment_last_line() {
  grep -v '^[[:space:]]*$' "$1" | tail -n 1 | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' || true
}

# Report the first forbidden element found in a fragment. Complete tags stay
# on one physical line in this grammar, so a plain start-tag scan is exact
# enough for a tripwire; the assembled body still faces the full validator.
pr_explain_fragment_forbidden_tag() {
  local fragment="$1" tag
  shift
  if grep -qi '<!doctype' "$fragment"; then
    printf '!doctype\n'
    return 0
  fi
  for tag in "$@"; do
    if grep -Eiq "<${tag}([[:space:]/>])" "$fragment"; then
      printf '%s\n' "$tag"
      return 0
    fi
  done
  return 1
}

# Mechanical shape checks for the per-pass fragments. Each is a cheap tripwire
# that stops a misdirected pass before the next provider call spends money
# continuing from it. They check placement and identity — the planned opening
# tag, the plan-matching table of contents, the absence of another pass's
# markup — and leave the full grammar to the assembled-body validator.
pr_explain_check_front_fragment() {
  local subject="$1" fragment="$2" outline="$3"
  local forbidden toc_expected toc_actual

  if forbidden=$(pr_explain_fragment_forbidden_tag "$fragment" html head body title style script footer); then
    die 1 "$subject front matter contains a forbidden <$forbidden> element"
  fi
  grep -q 'class="rx-skip"' "$fragment" ||
    die 1 "$subject front matter is missing the skip link"
  grep -q 'class="rx-masthead"' "$fragment" ||
    die 1 "$subject front matter is missing the masthead"
  grep -q 'class="rx-summary"' "$fragment" ||
    die 1 "$subject front matter is missing the operator summary"
  grep -qF '<main id="rx-main">' "$fragment" ||
    die 1 "$subject front matter does not open main at rx-main"
  ! grep -q '</main>' "$fragment" ||
    die 1 "$subject front matter closes main, which the quiz pass owns"
  grep -qF '<section id="objectives"' "$fragment" ||
    die 1 "$subject front matter is missing the objectives block"
  [[ "$(pr_explain_fragment_last_line "$fragment")" == '</section>' ]] ||
    die 1 "$subject front matter does not end at the objectives block"

  toc_expected=$(jq -r '(["objectives"] + (.sections | map(.id)) + ["quiz"]) | join("\n")' "$outline") ||
    die 1 "could not derive the planned table of contents"
  # Bound the href scan to the nav element itself. A line-address range would
  # run to end-of-fragment when the nav opens and closes on one physical line
  # (the closing address never matches a later line), scooping legitimate
  # anchors that follow the table of contents — a termref in the objectives
  # block, for example — into the comparison.
  toc_actual=$(awk '
    !in_nav && /<nav class="rx-toc"/ {
      in_nav = 1
      sub(/^.*<nav class="rx-toc"/, "<nav class=\"rx-toc\"")
    }
    in_nav {
      if (index($0, "</nav>")) {
        sub(/<\/nav>.*$/, "</nav>")
        print
        exit
      }
      print
    }
  ' "$fragment" | grep -o 'href="#[^"]*"' | sed 's/^href="#//; s/"$//') || true
  [[ "$toc_actual" == "$toc_expected" ]] ||
    die 1 "$subject table of contents does not match the section plan"
}

pr_explain_check_section_fragment() {
  local subject="$1" fragment="$2" outline="$3" index="$4"
  local id layer objectives expected forbidden

  id=$(jq -r ".sections[$index].id" "$outline") ||
    die 1 "could not read the planned section id"
  layer=$(jq -r ".sections[$index].layer" "$outline") ||
    die 1 "could not read the planned section layer"
  objectives=$(jq -r ".sections[$index].objectives | join(\" \")" "$outline") ||
    die 1 "could not read the planned section objectives"

  if forbidden=$(pr_explain_fragment_forbidden_tag "$fragment" html head body title style script main footer header nav); then
    die 1 "$subject wrote a forbidden <$forbidden> element into its section"
  fi
  expected="<section id=\"$id\" aria-labelledby=\"$id-h\" data-layer=\"$layer\" data-objective=\"$objectives\">"
  [[ "$(pr_explain_fragment_first_line "$fragment")" == "$expected" ]] ||
    die 1 "$subject fragment does not open with the planned section tag for '$id'"
  [[ "$(pr_explain_fragment_last_line "$fragment")" == '</section>' ]] ||
    die 1 "$subject fragment does not end at its own closing section tag"
}

pr_explain_check_quiz_fragment() {
  local subject="$1" fragment="$2" outline="$3"
  local forbidden first_line quiz_layer last_layer

  if forbidden=$(pr_explain_fragment_forbidden_tag "$fragment" html head body title style script main header nav); then
    die 1 "$subject wrote a forbidden <$forbidden> element into the closing fragment"
  fi
  first_line=$(pr_explain_fragment_first_line "$fragment")
  [[ "$first_line" =~ ^\<section\ id=\"quiz\"\ aria-labelledby=\"quiz-h\"\ data-layer=\"(concept|mechanism|receipts)\"\ data-objective=\"[^\"]+\"\>$ ]] ||
    die 1 "$subject fragment does not open with the quiz section tag"
  quiz_layer="${BASH_REMATCH[1]}"
  last_layer=$(jq -r '.sections[-1].layer' "$outline") ||
    die 1 "could not read the final planned section layer"
  [[ "$(pr_explain_layer_rank "$quiz_layer")" -ge "$(pr_explain_layer_rank "$last_layer")" ]] ||
    die 1 "$subject quiz section steps backward from the '$last_layer' layer"
  grep -q 'class="rx-quiz-item"' "$fragment" ||
    die 1 "$subject fragment contains no quiz items"
  [[ "$(grep -cF '</main>' "$fragment")" -eq 1 ]] ||
    die 1 "$subject fragment must close main exactly once"
  grep -qF '<footer class="rx-footer" id="rx-provenance">' "$fragment" ||
    die 1 "$subject fragment is missing the provenance footer"
  [[ "$(pr_explain_fragment_last_line "$fragment")" == '</footer>' ]] ||
    die 1 "$subject fragment does not end at the provenance footer"
}
