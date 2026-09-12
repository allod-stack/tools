package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTraceDistillPiFixture(t *testing.T) {
	work := traceTestWorkDir(t)
	sessionDir := filepath.Join(work, "pisessions")
	rawPath := filepath.Join(sessionDir, "session.jsonl")
	traceCopyFixture(t, "testdata/trace/pi/session.jsonl", rawPath)

	t.Setenv("HOME", filepath.Join(work, "home"))
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", sessionDir)

	out := filepath.Join(work, "out")
	stdoutText, stderrText, code := runAllod(t, "trace", "distill", "--out", out, "--harness", "pi")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderrText)
	}
	if stderrText != "" {
		t.Errorf("stderr = %q, want empty", stderrText)
	}
	if want := "distilled 1 sessions into " + out + " (0 unchanged, 0 skipped)\n"; stdoutText != want {
		t.Errorf("stdout = %q, want %q", stdoutText, want)
	}

	destPath := filepath.Join(out, traceHostname(t), "pi", "2026-01-02-pi-fixture-1.md")
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read distilled trace: %v", err)
	}
	want := traceReadGolden(t, "testdata/trace/pi/golden.md", rawPath)
	if string(got) != want {
		t.Errorf("distilled trace mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}
