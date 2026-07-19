# allod pm — PM board tools

`allod pm` manages the Allod PM board: a `pm.json` overlay rendered to a static,
self-contained `pm.html` and committed to a separate state checkout. It is
dispatched by the `allod` CLI, which resolves the files in this directory from
the Nix store via `ALLOD_TOOLS_DIR`.

These files are the **tool** and carry no board data. The board **state**
(`pm.json`, `pm.html`, `triage-policy.md`) lives in its own repo — default
`$WORK_DIR/pm-state`, overridable with `ALLOD_PM_DIR`.

## Commands

| Command | What |
|---|---|
| `allod pm refresh` | Re-render `pm.html` from `pm.json` + live forge state and commit. No LLM; freshens issue titles and open/closed state for issues already on the board, but does not add or drop issues. |
| `allod pm groom (--claude\|--codex) [--dry-run]` | Rebuild `pm.json` from current forge state (add new issues, drop closed, re-prioritize) via the `pm-groom` subagent, then re-render and commit. `--dry-run` leaves the rebuild uncommitted for review (`allod pm commit` / `allod pm discard`). Saves its per-issue findings to `pm-review.md` for `allod pm review`. |
| `allod pm review (--claude\|--codex)` | Promote the last groom's findings to an INTERACTIVE action session (the `pm-issue-review` skill): rewrite/refile/close flagged issues with per-write approval. Reads `pm-review.md`; attended only; defaults to `--claude`. |
| `allod pm commit` | Commit pending working-tree board changes — an approved dry-run groom, or a hand-edit of `pm.json`. |
| `allod pm discard` | Revert working-tree changes to `pm.json` + `pm.html`. |

`--no-push` on any committing command commits without pushing.

## Files

| Path | What |
|---|---|
| `pm` | Entry point behind `allod pm` (the subcommands above). |
| `render` | `(pm.json + forge issue data) -> pm.html`. Self-contained, inline `<style>`, no `<script>`, no external URLs (the forge serves committed `.html` in a sandboxed, JS-free iframe). Live mode fetches each referenced issue's title + state from the forge with the `forge` token, HTML-escaping all of it; `--issue-snapshot <file>` reads from a fixed JSON snapshot instead for byte-deterministic runs. Exits non-zero on any dangling ref. |
| `integrity-check` | Validates `pm.json` against the schema (`check-jsonschema`), resolves every `owner/repo#num` ref on the forge (non-zero on a dangling ref), asserts overlay purity, warns on orphans. |
| `pm.schema.json` | JSON Schema for `pm.json`. Enforces overlay purity structurally: string-only issue refs, `additionalProperties: false`, no cached issue-fact fields. |
| `skills/pm-groom/SKILL.md` | The steady-state, read-only groom prompt. Applies `triage-policy.md`, writes only `pm.json`, and reports per-issue hygiene recommendations (relevance/completion/correct-repo/quality) as the run's primary output. Hard-stops if `triage-policy.md` is absent. |
| `skills/pm-issue-review/SKILL.md` | Supervised, mutating per-issue review prompt — the executor for the groom's recommendations. Rewrites/refiles/closes issues with per-action human approval. Interactive/attended only; never wired into the unattended `groom` path. |
| `fixtures/` | Hermetic render + validation fixtures (one hostile title proves HTML escaping). |

## Overlay shape

`pm.json` is an overlay, **not a mirror**: it references issues by `owner/repo#num`
strings and never caches their title, body, state, status, or assignee. The forge
stays authoritative; `render` joins the two at render time.

```json
{
  "schema": 1,
  "updated": "2026-07-15T00:00:00Z",
  "policy": "triage-policy.md",
  "repos": ["owner/repo"],
  "plans":    [ { "id": "...", "title": "...", "status": "active",
                  "phases": [ { "id": "p1", "gate": "...", "issues": ["owner/repo#1"] } ] } ],
  "priority": [ { "issue": "owner/repo#1", "tier": "P0", "note": "..." } ],
  "gates":    [ { "blocked": "owner/repo#2", "by": ["owner/repo#1"], "reason": "..." } ],
  "decisions_pending": [ { "id": "...", "q": "...", "issue": "owner/repo#3" } ]
}
```

`priority` is ordered — array position is the rank.

## Runtime dependencies

`git`, `jq`, and `check-jsonschema`. `groom` additionally needs `yolo` on `PATH`
for the subagent run. On dev VMs these ride on the `allod` wrapper's `PATH` via
the profiles packaging.
