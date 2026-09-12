package main

// Codex shards sessions by date: ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-
// <uuid>.jsonl (relocated by CODEX_HOME). Line 1 is always a session_meta
// record carrying cwd, session id, timestamp, and the originator/source
// pair that decides interactive vs non-interactive; every other line is
// walked for response_item records, since that is where user/assistant
// messages and tool calls live. A file whose first line is not a
// session_meta record does not look like a Codex rollout at all.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func codexStoreRoot() (string, bool) {
	var root string
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		root = filepath.Join(dir, "sessions")
	} else {
		root = filepath.Join(homeDir(), ".codex", "sessions")
	}
	info, err := os.Stat(root)
	return root, err == nil && info.IsDir()
}

var codexNumericDirName = regexp.MustCompile(`^\d+$`)

func codexNumericDirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() && codexNumericDirName.MatchString(entry.Name()) {
			out = append(out, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// codexCandidates walks the YYYY/MM/DD shard tree and lists every
// rollout-*.jsonl file under it.
func codexCandidates(root string) []string {
	var out []string
	for _, year := range codexNumericDirs(root) {
		for _, month := range codexNumericDirs(year) {
			for _, day := range codexNumericDirs(month) {
				entries, err := os.ReadDir(day)
				if err != nil {
					continue
				}
				for _, entry := range entries {
					name := entry.Name()
					if entry.IsDir() || !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
						continue
					}
					out = append(out, filepath.Join(day, name))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

type codexEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type codexSessionMeta struct {
	SessionID  string `json:"session_id"`
	ID         string `json:"id"`
	Cwd        string `json:"cwd"`
	Timestamp  string `json:"timestamp"`
	Originator string `json:"originator"`
	Source     string `json:"source"`
}

type codexResponseItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	ID        string          `json:"id"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
}

// codexToolInput reads whichever of 'arguments' (the OpenAI function-call
// convention, a JSON-encoded string) or 'input' (custom tool calls, seen on
// this machine as a bare string of tool-specific syntax) is present. A
// value that decodes to a JSON string is parsed once more, in case it is
// itself a JSON object serialized as text; a value that still is not one
// keeps the plain string, which traceSummarizeInput already knows how to
// present.
func codexToolInput(argumentsRaw, inputRaw json.RawMessage) any {
	raw := argumentsRaw
	if len(raw) == 0 {
		raw = inputRaw
	}
	v := traceDecodeAny(raw)
	if s, ok := v.(string); ok {
		var nested any
		if json.Unmarshal([]byte(s), &nested) == nil {
			return nested
		}
		return s
	}
	return v
}

// codexFlattenOutput joins a tool output block array (each block a string
// or a {..., text} object, the same convention message content uses) down
// to one string; any other shape passes through unchanged for
// traceResultText to handle.
func codexFlattenOutput(output any) any {
	arr, ok := output.([]any)
	if !ok {
		return output
	}
	var parts []string
	for _, item := range arr {
		if s, ok := item.(string); ok {
			parts = append(parts, s)
			continue
		}
		if m, ok := item.(map[string]any); ok {
			if t, ok := m["text"].(string); ok {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func codexParseSession(path string) (traceSession, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return traceSession{}, fmt.Errorf("cannot read file: %w", err)
	}
	lines := strings.Split(string(data), "\n")

	var meta codexSessionMeta
	metaSeen := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var env codexEnvelope
		if err := json.Unmarshal([]byte(line), &env); err == nil && env.Type == "session_meta" {
			if json.Unmarshal(env.Payload, &meta) == nil {
				metaSeen = true
			}
		}
		break
	}
	if !metaSeen {
		return traceSession{}, fmt.Errorf("first line is not a recognisable session_meta record")
	}

	var raw []traceRawEvent
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var env codexEnvelope
		if err := json.Unmarshal([]byte(line), &env); err != nil || env.Type != "response_item" {
			continue
		}
		var item codexResponseItem
		if err := json.Unmarshal(env.Payload, &item); err != nil {
			continue
		}
		switch item.Type {
		case "message":
			// developer messages are harness scaffolding, never user intent.
			if item.Role != "user" && item.Role != "assistant" {
				continue
			}
			raw = append(raw, traceContentEvents(item.Role, traceDecodeAny(item.Content))...)
		case "function_call", "custom_tool_call":
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			raw = append(raw, traceRawEvent{kind: "toolCall", name: item.Name, id: id, input: codexToolInput(item.Arguments, item.Input)})
		case "function_call_output", "custom_tool_call_output":
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			raw = append(raw, traceRawEvent{kind: "toolResult", id: id, result: codexFlattenOutput(traceDecodeAny(item.Output))})
		}
	}

	sessionID := meta.SessionID
	if sessionID == "" {
		sessionID = meta.ID
	}
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}

	startedAt, err := time.Parse(time.RFC3339, meta.Timestamp)
	if err != nil {
		if info, statErr := os.Stat(path); statErr == nil {
			startedAt = info.ModTime()
		}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	events := tracePairTools(raw)
	userMessages, selfMarked := traceSessionCounts(events)

	nonInteractive := meta.Originator == "codex_exec"
	if meta.Source != "" && meta.Source != "cli" {
		nonInteractive = true
	}

	return traceSession{
		id:               sessionID,
		cwd:              meta.Cwd,
		startedAt:        startedAt,
		interactive:      !nonInteractive,
		rawPath:          absPath,
		events:           events,
		userMessageCount: userMessages,
		selfMarked:       selfMarked,
	}, nil
}
