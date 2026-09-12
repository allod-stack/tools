# allod trace

Dev VMs are reprovisioned, and a reprovision discards every local agent session log with it. `allod trace distill` turns each local Claude Code, Codex, and Pi session on the machine it runs on into one small, redacted markdown trace, so a separate hourly timer can commit those traces to a private repository before the originals are gone, and a weekly skill can later read them back against the memory index. The command only reads session stores and writes markdown files; it never touches git, and it never mutates a session store.

## Command

```
allod trace distill --out <dir> [--harness <name>[,<name>...]] [--quiet]
```

- `--out <dir>` (required) — the root traces are written under.
- `--harness` — which stores to read: `claude`, `codex`, `pi`, comma-separated and/or repeatable (`--harness claude,codex` and `--harness claude --harness codex` name the same set, per `cli-design.md`). Default: all three. An unrecognised name is a usage error naming the valid ones.
- `--quiet` — suppress the summary line on stdout.

Exit 0 on success, 1 on a usage error or an `--out` directory that cannot be created or written to. A missing session store, an unreadable file, or a file that does not look like a session log of the harness it was found under is skipped with one line on stderr naming it; none of those three is a failure.

### Session stores

| Harness | Store root | Override | Layout |
|---|---|---|---|
| Claude Code | `$HOME/.claude/projects` | `$CLAUDE_CONFIG_DIR` (relocates the whole config dir) | `<root>/<project-dir>/<session>.jsonl` |
| Codex | `$HOME/.codex/sessions` | `$CODEX_HOME` (relocates the whole config dir) | `<root>/YYYY/MM/DD/rollout-*.jsonl` |
| Pi | `$HOME/.pi/agent/sessions` | `$PI_CODING_AGENT_SESSION_DIR` | default: `<root>/<project-dir>/<session>.jsonl`; override: `<dir>/<session>.jsonl` directly, one level down, matching Pi's own convention that this variable names the per-project session directory rather than the sessions root above it |

### Output

Each distilled session is written to `<out>/<machine>/<harness>/<YYYY-MM-DD>-<session-id>.md`, where `<machine>` is `os.Hostname()` and the date is the session's start time in UTC. A file is written only when its distilled content differs from what is already on disk, so a finished session already committed on an earlier run is left untouched (mtime included) and a still-running session is simply rewritten each time the timer fires.

The summary line, suppressed by `--quiet`:

```
distilled <n> sessions into <out> (<m> unchanged, <k> skipped)
```

`<n>` counts every session that produced a trace (written or unchanged this run), `<m>` is how many of those were already up to date, and `<k>` is sessions and files skipped for any reason — an unreadable or unrecognised file, too few user messages, or the weekly skill's self-marker (below).

A session is skipped, not distilled, when it has fewer than two user messages, or when its first user message's first line is exactly `<!-- memory-backpass:self -->` — the line the weekly `memory-backpass` skill marks its own analysis calls with, so the skill never distills and later re-reads its own output as if it were a human session.

## Trace shape

```
# <harness> session <id>
- started: <RFC3339 UTC>
- cwd: <cwd>
- interactive: yes|no
- raw: <absolute path of the source file>

## user

<message text, verbatim>

## assistant

<message text, verbatim>

tool: <name> <input summary> -> <output summary> (<bytes> bytes)
```

`interactive` is `no` for Codex when the session's `originator` is `codex_exec` or its `source` is present and is not `cli`, for Claude when `entrypoint` is present and is not `cli`, and `yes` otherwise — including for every Pi session, since Pi records no interactive signal of its own and the interface's default for a harness with no signal is interactive.

Turns are `## user` and `## assistant` headings with the message text verbatim, capped at 6000 characters by keeping the first 4800 and the last 1000 with `[... N chars elided ...]` between. `<system-reminder>…</system-reminder>` spans are stripped from user text before it is written. `thinking` and `reasoning` parts are dropped entirely, as are Codex `developer` messages and any Claude record whose type is not `user` or `assistant`.

A tool call renders as one line: `tool: <name> <input summary> -> <output summary> (<bytes> bytes)`. The input summary is the first 160 characters of the value of the first present key among `command`, `cmd`, `file_path`, `path`, `pattern`, `query`, `url`, `description`; failing that, the input's JSON form. The output summary is the first 200 characters of the tool result, whitespace-folded; `<bytes>` is the full result's byte length, not the summary's. Tool calls and results are paired by id (Claude's `tool_use_id`, Codex's `call_id`, Pi's tool call id); a result whose id names no call in the same session renders on its own as `tool-result: <summary>`.

## Redaction

Redaction runs once over the whole rendered trace, before it is written, replacing every match of every rule below with `[redacted:<kind>]`. It is a coarse net for the token shapes that actually show up in these logs, not a guarantee — a secret shaped like ordinary prose survives it.

| Kind | Matches |
|---|---|
| `bearer` | `Bearer ` followed by a token |
| `openai` | `sk-` followed by 20 or more word characters |
| `github` | `gh[pousr]_` followed by 30 or more word characters |
| `slack` | `xox[abpr]-` followed by a token |
| `aws` | `AKIA` followed by 16 uppercase-alphanumeric characters |
| `age` | `AGE-SECRET-KEY-1` followed by base32 |
| `pem` | `-----BEGIN … PRIVATE KEY-----` through the matching `-----END … PRIVATE KEY-----` line |
| `netrc` | `password ` followed by a non-space run, or `password=…` |
| `authorization` | An `Authorization:` header and the rest of its line |
| `url-credential` | `://user:pass@` inside a URL |

A 40-hex git commit ID or a 64-hex sha256 digest — both common in these logs — is never redacted: none of the patterns above match a bare hex run.

## Fixtures and tests

`cmd/allod/testdata/trace/<harness>/session.jsonl` are tiny synthetic fixtures shaped like each harness's real JSONL, with a matching `golden.md` (its `raw:` line carries a `{{RAW_PATH}}` placeholder the tests fill in with the fixture's actual path). They hold no content from a real session — every value is invented for the test.

## Private material

A distilled trace still holds cwd paths, tool arguments, and message text from real work — it is redacted for secrets, not for the ordinary sensitivity of "what an agent was asked to do." Traces belong only in a private repository, never in `allod/*` or any other public one.
