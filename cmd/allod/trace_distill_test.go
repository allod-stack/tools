package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTraceDistillRequiresOut(t *testing.T) {
	_, stderrText, code := runAllod(t, "trace", "distill")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderrText, "requires --out") {
		t.Errorf("stderr = %q, want it to mention --out", stderrText)
	}
}

// TestTraceDistillHarnessFlagParsing covers --harness's comma-separated and
// repeated forms (cli-design.md: they name the same set) and the unknown-
// name error, using each store's "not found" line as the observable proof
// of which harnesses were actually selected.
func TestTraceDistillHarnessFlagParsing(t *testing.T) {
	runSelecting := func(t *testing.T, args ...string) string {
		t.Helper()
		work := traceTestWorkDir(t)
		t.Setenv("HOME", filepath.Join(work, "home"))
		out := filepath.Join(work, "out")
		fullArgs := append([]string{"trace", "distill", "--out", out}, args...)
		_, stderrText, code := runAllod(t, fullArgs...)
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderrText)
		}
		return stderrText
	}

	t.Run("comma-separated", func(t *testing.T) {
		stderrText := runSelecting(t, "--harness", "claude,codex")
		if !strings.Contains(stderrText, "claude store not found") || !strings.Contains(stderrText, "codex store not found") {
			t.Errorf("stderr missing expected store lines: %q", stderrText)
		}
		if strings.Contains(stderrText, "pi store not found") {
			t.Errorf("stderr unexpectedly mentions pi: %q", stderrText)
		}
	})

	t.Run("repeated-flag", func(t *testing.T) {
		stderrText := runSelecting(t, "--harness", "claude", "--harness", "codex")
		if !strings.Contains(stderrText, "claude store not found") || !strings.Contains(stderrText, "codex store not found") {
			t.Errorf("stderr missing expected store lines: %q", stderrText)
		}
		if strings.Contains(stderrText, "pi store not found") {
			t.Errorf("stderr unexpectedly mentions pi: %q", stderrText)
		}
	})

	t.Run("default-is-all-three", func(t *testing.T) {
		stderrText := runSelecting(t)
		for _, name := range []string{"claude", "codex", "pi"} {
			if !strings.Contains(stderrText, name+" store not found") {
				t.Errorf("stderr missing %s store line: %q", name, stderrText)
			}
		}
	})

	t.Run("unknown-name", func(t *testing.T) {
		work := traceTestWorkDir(t)
		out := filepath.Join(work, "out")
		_, stderrText, code := runAllod(t, "trace", "distill", "--out", out, "--harness", "bogus")
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		for _, want := range []string{"bogus", "claude", "codex", "pi"} {
			if !strings.Contains(stderrText, want) {
				t.Errorf("stderr = %q, want it to name %q", stderrText, want)
			}
		}
	})

	t.Run("empty-value", func(t *testing.T) {
		work := traceTestWorkDir(t)
		out := filepath.Join(work, "out")
		_, stderrText, code := runAllod(t, "trace", "distill", "--out", out, "--harness", "")
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderrText, "--harness value must not be empty") {
			t.Errorf("stderr = %q, want the empty-value message", stderrText)
		}
	})
}

// TestTraceDistillUnchangedFileRule proves a session already distilled is
// left alone on a later run: the write counter in the summary line moves
// from written to unchanged, and the file's mtime does not move at all,
// since a genuinely unchanged run never opens it for writing.
func TestTraceDistillUnchangedFileRule(t *testing.T) {
	work := traceTestWorkDir(t)
	configDir := filepath.Join(work, "claudeconfig")
	rawPath := filepath.Join(configDir, "projects", "proj1", "session.jsonl")
	traceCopyFixture(t, "testdata/trace/claude/session.jsonl", rawPath)
	t.Setenv("HOME", filepath.Join(work, "home"))
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)

	out := filepath.Join(work, "out")
	if _, stderrText, code := runAllod(t, "trace", "distill", "--out", out, "--harness", "claude"); code != 0 {
		t.Fatalf("first run: exit code = %d, stderr = %q", code, stderrText)
	}
	destPath := filepath.Join(out, traceHostname(t), "claude", "2026-01-02-claude-fixture-1.md")
	before, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("stat after first run: %v", err)
	}

	stdoutText, stderrText, code := runAllod(t, "trace", "distill", "--out", out, "--harness", "claude")
	if code != 0 {
		t.Fatalf("second run: exit code = %d, stderr = %q", code, stderrText)
	}
	if want := "distilled 1 sessions into " + out + " (1 unchanged, 0 skipped)\n"; stdoutText != want {
		t.Errorf("stdout = %q, want %q", stdoutText, want)
	}
	after, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("stat after second run: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("mtime changed on an unchanged rerun: before=%v after=%v", before.ModTime(), after.ModTime())
	}
}

// TestTraceDistillSkipRules covers both skip rules in one store: a session
// with fewer than two user messages, and a session whose first user
// message carries the weekly skill's self-marker line, alongside one
// ordinary session that must still be distilled.
func TestTraceDistillSkipRules(t *testing.T) {
	work := traceTestWorkDir(t)
	configDir := filepath.Join(work, "claudeconfig")
	projectDir := filepath.Join(configDir, "projects", "proj1")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	traceCopyFixture(t, "testdata/trace/claude/session.jsonl", filepath.Join(projectDir, "valid.jsonl"))

	fewMessages := `{"type":"user","sessionId":"skip-fewmsgs","cwd":"/repo","entrypoint":"cli","timestamp":"2026-02-01T00:00:00Z","message":{"role":"user","content":"Only one message here."}}
{"type":"assistant","sessionId":"skip-fewmsgs","cwd":"/repo","timestamp":"2026-02-01T00:00:01Z","message":{"role":"assistant","content":"OK."}}
`
	if err := os.WriteFile(filepath.Join(projectDir, "fewmsgs.jsonl"), []byte(fewMessages), 0644); err != nil {
		t.Fatalf("write fewmsgs fixture: %v", err)
	}

	selfMarked := `{"type":"user","sessionId":"skip-selfmarked","cwd":"/repo","entrypoint":"cli","timestamp":"2026-02-01T00:01:00Z","message":{"role":"user","content":"<!-- memory-backpass:self -->\nAnalysis prompt body."}}
{"type":"assistant","sessionId":"skip-selfmarked","cwd":"/repo","timestamp":"2026-02-01T00:01:01Z","message":{"role":"assistant","content":"Done."}}
{"type":"user","sessionId":"skip-selfmarked","cwd":"/repo","entrypoint":"cli","timestamp":"2026-02-01T00:01:02Z","message":{"role":"user","content":"Second message."}}
`
	if err := os.WriteFile(filepath.Join(projectDir, "selfmarked.jsonl"), []byte(selfMarked), 0644); err != nil {
		t.Fatalf("write selfmarked fixture: %v", err)
	}

	t.Setenv("HOME", filepath.Join(work, "home"))
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)

	out := filepath.Join(work, "out")
	stdoutText, stderrText, code := runAllod(t, "trace", "distill", "--out", out, "--harness", "claude")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderrText)
	}
	if want := "distilled 1 sessions into " + out + " (0 unchanged, 2 skipped)\n"; stdoutText != want {
		t.Errorf("stdout = %q, want %q", stdoutText, want)
	}
	for _, name := range []string{"2026-02-01-skip-fewmsgs.md", "2026-02-01-skip-selfmarked.md"} {
		if _, err := os.Stat(filepath.Join(out, traceHostname(t), "claude", name)); !os.IsNotExist(err) {
			t.Errorf("%s should not have been written, stat err = %v", name, err)
		}
	}
}
