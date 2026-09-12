package main

// Claude Code writes one JSONL file per session under
// ~/.claude/projects/<munged-cwd>/<session-uuid>.jsonl (relocated by
// CLAUDE_CONFIG_DIR when it is set). Every user/assistant line carries its
// own cwd, sessionId, and timestamp, so any line supplies the header facts;
// the many other record types this store holds (ai-title, file-history
// snapshots, mode changes, and the rest) are not user/assistant turns and
// are skipped entirely, per the interface this file implements.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func claudeStoreRoot() (string, bool) {
	var root string
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		root = filepath.Join(dir, "projects")
	} else {
		root = filepath.Join(homeDir(), ".claude", "projects")
	}
	info, err := os.Stat(root)
	return root, err == nil && info.IsDir()
}

// claudeCandidates lists every *.jsonl file two path segments below root:
// root/<project-dir>/<session>.jsonl.
func claudeCandidates(root string) []string {
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

type claudeLine struct {
	Type       string         `json:"type"`
	Message    *claudeMessage `json:"message"`
	Cwd        string         `json:"cwd"`
	Entrypoint string         `json:"entrypoint"`
	SessionID  string         `json:"sessionId"`
	Timestamp  string         `json:"timestamp"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func claudeParseSession(path string) (traceSession, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return traceSession{}, fmt.Errorf("cannot read file: %w", err)
	}

	var raw []traceRawEvent
	var sessionID, cwd, entrypoint string
	var startedAt time.Time
	parsedLines := 0

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry claudeLine
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		parsedLines++
		if sessionID == "" && entry.SessionID != "" {
			sessionID = entry.SessionID
		}
		if cwd == "" && entry.Cwd != "" {
			cwd = entry.Cwd
		}
		if entrypoint == "" && entry.Entrypoint != "" {
			entrypoint = entry.Entrypoint
		}
		if startedAt.IsZero() && entry.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
				startedAt = t
			}
		}
		if entry.Type != "user" && entry.Type != "assistant" || entry.Message == nil {
			continue
		}
		raw = append(raw, traceContentEvents(entry.Message.Role, traceDecodeAny(entry.Message.Content))...)
	}

	if parsedLines == 0 {
		return traceSession{}, fmt.Errorf("no recognisable JSON lines")
	}
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	if startedAt.IsZero() {
		if info, err := os.Stat(path); err == nil {
			startedAt = info.ModTime()
		}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	events := tracePairTools(raw)
	userMessages, selfMarked := traceSessionCounts(events)

	// entrypoint is "no signal" (interactive) when absent, and non-
	// interactive whenever present and not exactly "cli" (e.g. "sdk-cli").
	return traceSession{
		id:               sessionID,
		cwd:              cwd,
		startedAt:        startedAt,
		interactive:      entrypoint == "" || entrypoint == "cli",
		rawPath:          absPath,
		events:           events,
		userMessageCount: userMessages,
		selfMarked:       selfMarked,
	}, nil
}
