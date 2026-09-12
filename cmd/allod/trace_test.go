package main

// Shared fixture plumbing for the trace namespace's tests: copying a
// testdata JSONL fixture into a fake store, and reading a golden .md with
// its {{RAW_PATH}} placeholder resolved to the fixture's actual (symlink-
// resolved) absolute path, since that path is test-specific and cannot be
// baked into the golden file itself.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// traceTestWorkDir returns a fresh temp directory with symlinks resolved,
// the same precaution useSiteRepo takes in main_test.go: t.TempDir() can
// hand back a path through a symlink, and the command under test reports
// the resolved one.
func traceTestWorkDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("could not resolve %s: %v", dir, err)
	}
	return resolved
}

// traceCopyFixture copies a testdata fixture to dst, creating dst's parent
// directories as needed.
func traceCopyFixture(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		t.Fatalf("write fixture %s: %v", dst, err)
	}
}

// traceReadGolden reads a golden .md and substitutes the fixture's raw
// source path for the {{RAW_PATH}} placeholder.
func traceReadGolden(t *testing.T, path, rawPath string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	return strings.ReplaceAll(string(data), "{{RAW_PATH}}", rawPath)
}

// traceHostname is os.Hostname with a test-friendly failure.
func traceHostname(t *testing.T) string {
	t.Helper()
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	return hostname
}

func TestTraceSafeFileName(t *testing.T) {
	cases := map[string]string{
		"01a09035-d058-7083-9686-033b3572b8f4": "01a09035-d058-7083-9686-033b3572b8f4",
		"../../etc/passwd":                     ".._.._etc_passwd",
		"bad\nname":                            "bad_name",
		"":                                     "session",
	}
	for in, want := range cases {
		if got := traceSafeFileName(in); got != want {
			t.Errorf("traceSafeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
