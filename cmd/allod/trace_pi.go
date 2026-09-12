package main

// Pi writes one JSONL file per session under
// ~/.pi/agent/sessions/<escaped-cwd>/<ISO-ts>_<uuid>.jsonl by default, one
// subdirectory per working directory. PI_CODING_AGENT_SESSION_DIR, when
// set, names that per-project directory directly rather than the sessions
// root above it, so files sit one level down from it instead of two; the
// two candidate walks below match that difference. Line 1 is always
// {type:"session", cwd, id}. Pi records no interactive/non-interactive
// signal of its own, so every Pi session reads as interactive, matching the
// interface's default for a harness that carries no such signal.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func piStoreRoot() (string, bool) {
	var root string
	if dir := os.Getenv("PI_CODING_AGENT_SESSION_DIR"); dir != "" {
		root = dir
	} else {
		root = filepath.Join(homeDir(), ".pi", "agent", "sessions")
	}
	info, err := os.Stat(root)
	return root, err == nil && info.IsDir()
}

func piCandidates(root string) []string {
	if os.Getenv("PI_CODING_AGENT_SESSION_DIR") != "" {
		return piDirectCandidates(root)
	}
	return piNestedCandidates(root)
}

// piDirectCandidates lists every *.jsonl file directly inside root, for the
// PI_CODING_AGENT_SESSION_DIR override — one level down from root.
func piDirectCandidates(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		out = append(out, filepath.Join(root, entry.Name()))
	}
	sort.Strings(out)
	return out
}

// piNestedCandidates lists every *.jsonl file two path segments below root:
// root/<escaped-cwd>/<session>.jsonl, the default store's layout.
func piNestedCandidates(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sub := filepath.Join(root, entry.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") {
				continue
			}
			out = append(out, filepath.Join(sub, file.Name()))
		}
	}
	sort.Strings(out)
	return out
}

type piSessionLine struct {
	Type      string `json:"type"`
	Cwd       string `json:"cwd"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
}

type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId"`
}

type piEnvelope struct {
	Type    string     `json:"type"`
	Message *piMessage `json:"message"`
}

func piParseSession(path string) (traceSession, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return traceSession{}, fmt.Errorf("cannot read file: %w", err)
	}
	lines := strings.Split(string(data), "\n")

	var meta piSessionLine
	metaSeen := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if json.Unmarshal([]byte(line), &meta) == nil && meta.Type == "session" && meta.Cwd != "" {
			metaSeen = true
		}
		break
	}
	if !metaSeen {
		return traceSession{}, fmt.Errorf("first line is not a recognisable session record")
	}

	var raw []traceRawEvent
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var env piEnvelope
		if err := json.Unmarshal([]byte(line), &env); err != nil || env.Type != "message" || env.Message == nil {
			continue
		}
		msg := env.Message
		switch msg.Role {
		case "toolResult":
			raw = append(raw, traceRawEvent{kind: "toolResult", id: msg.ToolCallID, result: traceDecodeAny(msg.Content)})
		case "user", "assistant":
			raw = append(raw, traceContentEvents(msg.Role, traceDecodeAny(msg.Content))...)
		}
	}

	sessionID := meta.ID
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

	return traceSession{
		id:               sessionID,
		cwd:              meta.Cwd,
		startedAt:        startedAt,
		interactive:      true,
		rawPath:          absPath,
		events:           events,
		userMessageCount: userMessages,
		selfMarked:       selfMarked,
	}, nil
}
