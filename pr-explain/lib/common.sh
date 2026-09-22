# shellcheck shell=bash
# Primitives shared across every other pr-explain lib file: HTML escaping,
# the validation-error/warning accumulators, regex counting over a file or a
# string, UTF-8 safety, and the runner display name. It depends on nothing
# else under pr-explain/lib/ and is sourced first so every later file can use
# it.

# The one honest display name per runner, shared by the consent screen, the
# provenance validator, and every operator-facing message. The subscription
# CLIs spend a subscription; pi spends metered API credits from its own
# credential store, and calling it a subscription would misstate what the
# operator consented to.
pr_explain_runner_display() {
  case "$1" in
    pi) printf 'pi API CLI' ;;
    *) printf '%s subscription CLI' "$1" ;;
  esac
}

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

# Count case-insensitive matches of an extended regex in a string, mirroring
# pr_explain_count_regex for material already extracted from the report.
pr_explain_count_stream_regex() {
  local stream="$1" regex="$2" count
  count=$(grep -Eio "$regex" <<<"$stream" 2>/dev/null | wc -l) || true
  printf '%s\n' "${count//[[:space:]]/}"
}
