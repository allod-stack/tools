# shellcheck shell=bash
# Asset resolution and emission: locating the pr-explain lib directory,
# resolving the forge executable, and the pr_explain_emit_* family that
# streams the packaged CSS, JS, prompts, and gallery out to callers. Depends
# on PR_EXPLAIN_LIB_DIR (set by lib.sh before sourcing). pr_explain_resolve_forge
# and pr_explain_emit_asset also call die, which no file under pr-explain/lib
# defines; it is caller-provided by explain.

pr_explain_asset_dir() {
  printf '%s\n' "$PR_EXPLAIN_LIB_DIR"
}

# Resolve the `forge` executable to use for this run. An explicit
# ALLOD_PR_EXPLAIN_FORGE override supports tests and development; installed
# use resolves the separately packaged Go binary from PATH.
pr_explain_resolve_forge() {
  local override="${ALLOD_PR_EXPLAIN_FORGE:-}" override_dir

  if [[ -n "$override" ]]; then
    [[ -f "$override" && -x "$override" ]] ||
      die 1 "ALLOD_PR_EXPLAIN_FORGE must name an executable file: $override"
    override_dir=$(cd -- "$(dirname -- "$override")" && pwd) ||
      die 1 "ALLOD_PR_EXPLAIN_FORGE names an unresolvable directory: $override"
    printf '%s/%s\n' "$override_dir" "$(basename -- "$override")"
    return 0
  fi

  command -v forge 2>/dev/null || die 1 "forge not found on PATH"
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
pr_explain_emit_contract() { pr_explain_emit_asset contract.md; }
pr_explain_emit_outline_prompt() { pr_explain_emit_asset outline-prompt.md; }
pr_explain_emit_section_prompt() { pr_explain_emit_asset section-prompt.md; }
pr_explain_emit_quiz_prompt() { pr_explain_emit_asset quiz-prompt.md; }
pr_explain_emit_triage_prompt() { pr_explain_emit_asset triage-prompt.md; }
pr_explain_emit_slop_prompt() { pr_explain_emit_asset slop-prompt.md; }
pr_explain_emit_repair_prompt() { pr_explain_emit_asset repair-prompt.md; }
pr_explain_emit_gallery() { pr_explain_emit_asset component-gallery.html; }
