package main

// 'allod trace distill' walks each harness's local session store, turns
// every session log into a small markdown trace, and writes it under --out.
// The pipeline is the same for every harness: parse the raw JSONL into an
// ordered list of message and tool events (traceRawEvent), fold tool
// results back into the call they answer (tracePairTools), render the
// result as markdown (traceRender), then redact the whole rendered text
// (traceRedact) before it ever touches disk. Each harness file
// (trace_claude.go, trace_codex.go, trace_pi.go) owns only the part that
// differs: where its store lives, which files in it are candidates, and how
// its particular JSONL shape becomes traceRawEvents.
//
// A session is skipped, not failed, when it has fewer than two user
// messages or when its first user message is the weekly memory-backpass
// skill's own self-marker line — the tool tallies both into the "skipped"
// count in the summary line rather than treating either as an error.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// --- Harness dispatch table ---

// traceHarnessAdapter is what a harness contributes to the shared pipeline:
// where to look for its store, which files under that store are candidate
// session logs, and how to turn one candidate file into a traceSession.
// parse returns an error only for a file that cannot be read or does not
// look like a session log of this harness; every other judgment call
// (too few user messages, the self-marker) is applied uniformly by
// traceDistill once parsing succeeds.
type traceHarnessAdapter struct {
	storeRoot  func() (root string, present bool)
	candidates func(root string) []string
	parse      func(path string) (traceSession, error)
}

// traceHarnessOrder is the fixed order harnesses are processed and listed
// in, independent of the order --harness named them.
var traceHarnessOrder = []string{"claude", "codex", "pi"}

var traceHarnesses = map[string]traceHarnessAdapter{
	"claude": {storeRoot: claudeStoreRoot, candidates: claudeCandidates, parse: claudeParseSession},
	"codex":  {storeRoot: codexStoreRoot, candidates: codexCandidates, parse: codexParseSession},
	"pi":     {storeRoot: piStoreRoot, candidates: piCandidates, parse: piParseSession},
}

func validTraceHarness(name string) bool {
	_, ok := traceHarnesses[name]
	return ok
}

// --- Shared session and event shapes ---

// traceSession is one harness-neutral session, ready to render: the header
// facts and the ordered, tool-paired event stream.
type traceSession struct {
	id               string
	cwd              string
	startedAt        time.Time
	interactive      bool
	rawPath          string
	events           []traceTurnEvent
	userMessageCount int
	selfMarked       bool
}

// traceRawEvent is what a harness's own JSONL walk produces, before tool
// calls and their results are folded together. A "message" is a user or
// assistant turn's text (thinking/reasoning already dropped by the
// harness reader); "toolCall" and "toolResult" carry the harness's own
// pairing id (Claude tool_use_id, Codex call_id, Pi's tool call id) so
// tracePairTools can fold them back into one event.
type traceRawEvent struct {
	kind   string // "message", "toolCall", "toolResult"
	role   string
	text   string
	id     string
	name   string
	input  any
	result any
}

// traceTurnEvent is the paired, render-ready form: a message, a tool call
// with its result attached if one arrived, or an orphaned tool result that
// never found its call (a truncated log, most often).
type traceTurnEvent struct {
	kind      string // "message", "tool", "orphanResult"
	role      string
	text      string
	name      string
	input     any
	result    any
	hasResult bool
}

// tracePairTools folds each toolResult back into the toolCall it answers,
// by id, in one left-to-right pass — the call always precedes its result in
// a session log, so a later result can always find and update the event
// already placed in the output slice. A result whose id names no known call
// becomes its own "orphanResult" event at the point it appeared.
func tracePairTools(raw []traceRawEvent) []traceTurnEvent {
	events := make([]traceTurnEvent, 0, len(raw))
	byID := map[string]int{}
	for _, r := range raw {
		switch r.kind {
		case "message":
			if strings.TrimSpace(r.text) == "" {
				continue
			}
			events = append(events, traceTurnEvent{kind: "message", role: r.role, text: r.text})
		case "toolCall":
			events = append(events, traceTurnEvent{kind: "tool", name: r.name, input: r.input})
			if r.id != "" {
				byID[r.id] = len(events) - 1
			}
		case "toolResult":
			if r.id != "" {
				if idx, ok := byID[r.id]; ok {
					events[idx].result = r.result
					events[idx].hasResult = true
					continue
				}
			}
			events = append(events, traceTurnEvent{kind: "orphanResult", result: r.result, hasResult: true})
		}
	}
	return events
}

// traceSessionCounts scans the paired events for the two facts traceDistill
// needs to decide whether a session is worth keeping: how many real user
// messages it has, and whether the first one carries the weekly skill's
// self-marker line.
func traceSessionCounts(events []traceTurnEvent) (userMessages int, selfMarked bool) {
	seenFirst := false
	for _, ev := range events {
		if ev.kind != "message" || ev.role != "user" {
			continue
		}
		userMessages++
		if seenFirst {
			continue
		}
		seenFirst = true
		firstLine := ev.text
		if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
			firstLine = firstLine[:idx]
		}
		if strings.TrimSpace(firstLine) == "<!-- memory-backpass:self -->" {
			selfMarked = true
		}
	}
	return userMessages, selfMarked
}

// --- Shared content-block decoding ---

// traceDecodeAny unmarshals a JSON value of unknown shape (string, array,
// object, or absent) into Go's generic any representation, the form
// traceContentEvents and the tool input/output summarizers all consume.
func traceDecodeAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

func traceFirstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := m[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// traceContentEvents decodes a message content value in the shape Claude,
// Pi, and Codex all use for it: a plain string, or an array of typed
// blocks. Every harness that settled on this "string, or array of typed
// blocks" convention shares this one reader; a harness whose tool calls
// arrive as separate top-level records instead of inline blocks (Codex)
// simply never produces the tool_use/tool_result cases here. thinking and
// reasoning blocks are dropped entirely; a block type this function does
// not recognise but that carries a string "text" field is still read as
// text, the same tolerance every harness's own renderer applies.
func traceContentEvents(role string, content any) []traceRawEvent {
	switch v := content.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []traceRawEvent{{kind: "message", role: role, text: v}}
	case []any:
		var out []traceRawEvent
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, traceRawEvent{kind: "message", role: role, text: s})
				}
				continue
			}
			blockType, _ := block["type"].(string)
			switch blockType {
			case "text", "input_text", "output_text":
				if t, ok := block["text"].(string); ok && strings.TrimSpace(t) != "" {
					out = append(out, traceRawEvent{kind: "message", role: role, text: t})
				}
			case "tool_use", "toolCall":
				name, _ := block["name"].(string)
				id := traceFirstString(block, "id", "call_id")
				input := block["input"]
				if input == nil {
					input = block["arguments"]
				}
				out = append(out, traceRawEvent{kind: "toolCall", name: name, id: id, input: input})
			case "tool_result", "toolResult":
				id := traceFirstString(block, "tool_use_id", "id", "toolCallId")
				result := block["content"]
				if result == nil {
					result = block["output"]
				}
				if result == nil {
					result = block["text"]
				}
				out = append(out, traceRawEvent{kind: "toolResult", id: id, result: result})
			case "thinking", "reasoning":
				// dropped
			default:
				if t, ok := block["text"].(string); ok && strings.TrimSpace(t) != "" {
					out = append(out, traceRawEvent{kind: "message", role: role, text: t})
				}
			}
		}
		return out
	default:
		return nil
	}
}

// --- Rendering ---

var traceSystemReminderSpan = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

func traceStripSystemReminders(text string) string {
	return traceSystemReminderSpan.ReplaceAllString(text, "")
}

// traceClampMessage keeps a message readable without dropping its shape:
// the first 4800 characters (the task as asked) and the last 1000 (how the
// turn ended), with a count of what sits between.
func traceClampMessage(text string) string {
	const headChars, tailChars, capChars = 4800, 1000, 6000
	runes := []rune(text)
	if len(runes) <= capChars {
		return text
	}
	head := string(runes[:headChars])
	tail := string(runes[len(runes)-tailChars:])
	elided := len(runes) - (headChars + tailChars)
	return fmt.Sprintf("%s\n\n[... %d chars elided ...]\n\n%s", head, elided, tail)
}

var traceWhitespaceRun = regexp.MustCompile(`\s+`)

// traceOneLine flattens whitespace and caps at limit runes, with no
// truncation marker: the summary is explicitly a prefix, not a claim about
// where the value ends.
func traceOneLine(text string, limit int) string {
	flat := strings.TrimSpace(traceWhitespaceRun.ReplaceAllString(text, " "))
	runes := []rune(flat)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return flat
}

var traceToolInputKeys = []string{"command", "cmd", "file_path", "path", "pattern", "query", "url", "description"}

// traceSummarizeInput picks the field a human would recognise first; a
// plain string input is used as-is, and anything else falls back to its
// JSON form.
func traceSummarizeInput(input any) string {
	if input == nil {
		return ""
	}
	if s, ok := input.(string); ok {
		return traceOneLine(s, 160)
	}
	if m, ok := input.(map[string]any); ok {
		for _, key := range traceToolInputKeys {
			if v, ok := m[key]; ok {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return traceOneLine(s, 160)
				}
			}
		}
	}
	data, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return traceOneLine(string(data), 160)
}

// traceResultText flattens a tool result of unknown shape (a plain string,
// a content-block array, or a single object) down to the text a human
// reading the trace would want, the way each harness itself renders a tool
// result in its own transcript view.
func traceResultText(result any) string {
	switch v := result.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			switch b := item.(type) {
			case string:
				parts = append(parts, b)
			case map[string]any:
				if t, ok := b["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if t, ok := v["text"].(string); ok {
			return t
		}
		data, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(data)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(data)
	}
}

func traceSummarizeOutput(result any) (summary string, byteCount int) {
	text := traceResultText(result)
	return traceOneLine(text, 200), len([]byte(text))
}

func traceFormatToolLine(ev traceTurnEvent) string {
	line := "tool: " + ev.name
	if summary := traceSummarizeInput(ev.input); summary != "" {
		line += " " + summary
	}
	if ev.hasResult {
		summary, byteCount := traceSummarizeOutput(ev.result)
		line += fmt.Sprintf(" -> %s (%d bytes)", summary, byteCount)
	}
	return line
}

func traceYesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

var traceMultiBlank = regexp.MustCompile(`\n{3,}`)

// traceRender builds the full markdown trace: the header block, then the
// turns in order. Redaction is not applied here — traceDistill runs it once
// over the whole rendered text before writing.
func traceRender(harness string, sess traceSession) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s session %s\n", harness, sess.id)
	fmt.Fprintf(&b, "- started: %s\n", sess.startedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- cwd: %s\n", sess.cwd)
	fmt.Fprintf(&b, "- interactive: %s\n", traceYesNo(sess.interactive))
	fmt.Fprintf(&b, "- raw: %s\n", sess.rawPath)
	b.WriteString("\n")

	for _, ev := range sess.events {
		switch ev.kind {
		case "message":
			text := ev.text
			if ev.role == "user" {
				text = traceStripSystemReminders(text)
			}
			text = traceClampMessage(strings.TrimSpace(text))
			fmt.Fprintf(&b, "## %s\n\n%s\n\n", ev.role, text)
		case "tool":
			b.WriteString(traceFormatToolLine(ev))
			b.WriteString("\n\n")
		case "orphanResult":
			summary, _ := traceSummarizeOutput(ev.result)
			fmt.Fprintf(&b, "tool-result: %s\n\n", summary)
		}
	}

	body := traceMultiBlank.ReplaceAllString(b.String(), "\n\n")
	return strings.TrimRight(body, "\n") + "\n"
}

// --- Command ---

func traceDistill(args []string) {
	out, outSet := "", false
	quiet := false
	selected := map[string]bool{}
	harnessGiven := false

	for len(args) > 0 {
		switch args[0] {
		case "--out":
			requireValue(args, args[0])
			out, outSet, args = args[1], true, args[2:]
		case "--harness":
			requireValue(args, args[0])
			for _, raw := range strings.Split(args[1], ",") {
				name := strings.TrimSpace(raw)
				if name == "" {
					die(1, "--harness value must not be empty")
				}
				if !validTraceHarness(name) {
					die(1, "unknown harness: %s (valid: claude, codex, pi)", name)
				}
				selected[name] = true
			}
			harnessGiven = true
			args = args[2:]
		case "--quiet":
			quiet = true
			args = args[1:]
		case "-h", "--help":
			fmt.Fprint(stdout, traceUsageText)
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for trace distill: %s", args[0])
			}
			die(1, "trace distill accepts no positional arguments")
		}
	}

	if !outSet || out == "" {
		die(1, "trace distill requires --out <dir>")
	}
	if !harnessGiven {
		for _, name := range traceHarnessOrder {
			selected[name] = true
		}
	}

	if err := os.MkdirAll(out, 0755); err != nil {
		die(1, "cannot create output directory: %s: %v", out, err)
	}
	probe, err := os.CreateTemp(out, ".allod-trace-probe-*")
	if err != nil {
		die(1, "output directory is not writable: %s: %v", out, err)
	}
	probeName := probe.Name()
	probe.Close()
	os.Remove(probeName)

	hostname, err := os.Hostname()
	if err != nil {
		die(1, "could not determine hostname: %v", err)
	}

	var distilled, unchanged, skipped int

	for _, harnessName := range traceHarnessOrder {
		if !selected[harnessName] {
			continue
		}
		harness := traceHarnesses[harnessName]
		root, present := harness.storeRoot()
		if !present {
			fmt.Fprintf(stderr, "allod: trace distill: %s store not found: %s\n", harnessName, root)
			continue
		}
		for _, path := range harness.candidates(root) {
			sess, err := harness.parse(path)
			if err != nil {
				fmt.Fprintf(stderr, "allod: trace distill: skipping %s: %v\n", path, err)
				skipped++
				continue
			}
			if sess.userMessageCount < 2 || sess.selfMarked {
				skipped++
				continue
			}

			body := traceRedact(traceRender(harnessName, sess))
			destDir := filepath.Join(out, hostname, harnessName)
			destPath := filepath.Join(destDir, fmt.Sprintf("%s-%s.md", sess.startedAt.UTC().Format("2006-01-02"), sess.id))

			if existing, readErr := os.ReadFile(destPath); readErr == nil && string(existing) == body {
				unchanged++
				distilled++
				continue
			}
			if err := os.MkdirAll(destDir, 0755); err != nil {
				die(1, "cannot create output directory: %s: %v", destDir, err)
			}
			if err := os.WriteFile(destPath, []byte(body), 0644); err != nil {
				die(1, "cannot write trace: %s: %v", destPath, err)
			}
			distilled++
		}
	}

	if !quiet {
		fmt.Fprintf(stdout, "distilled %d sessions into %s (%d unchanged, %d skipped)\n", distilled, out, unchanged, skipped)
	}
}
