package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTraceDistillCodexFixture(t *testing.T) {
	work := traceTestWorkDir(t)
	codexHome := filepath.Join(work, "codexhome")
	rawPath := filepath.Join(codexHome, "sessions", "2026", "01", "02", "rollout-2026-01-02T03-05-00-fixture.jsonl")
	traceCopyFixture(t, "testdata/trace/codex/session.jsonl", rawPath)

	t.Setenv("HOME", filepath.Join(work, "home"))
	t.Setenv("CODEX_HOME", codexHome)

	out := filepath.Join(work, "out")
	stdoutText, stderrText, code := runAllod(t, "trace", "distill", "--out", out, "--harness", "codex")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderrText)
	}
	if stderrText != "" {
		t.Errorf("stderr = %q, want empty", stderrText)
	}
	if want := "distilled 1 sessions into " + out + " (0 unchanged, 0 skipped)\n"; stdoutText != want {
		t.Errorf("stdout = %q, want %q", stdoutText, want)
	}

	destPath := filepath.Join(out, traceHostname(t), "codex", "2026-01-02-codex-fixture-1.md")
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read distilled trace: %v", err)
	}
	want := traceReadGolden(t, "testdata/trace/codex/golden.md", rawPath)
	if string(got) != want {
		t.Errorf("distilled trace mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}
