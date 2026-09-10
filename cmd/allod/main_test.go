package main

// Tests for dispatch itself, and the harness the rest of the package's tests
// are written against, in the shape cmd/forge/harness_test.go established: the
// CLI is run in process with the stdout/stderr seams swapped for buffers.
//
//	out, errText, code := runAllod(t, "site", "deploy", "--dry-run")
//
// This file carries no build tag, so the harness is available to the tagged
// and untagged builds alike — which is what lets both of them assert on the
// namespaces they do and do not have.
//
// The package-level seams these helpers swap are shared mutable state, so no
// test here calls t.Parallel.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Harness ---

func swapStreams(out, errOut io.Writer) func() {
	previousOut, previousErr := stdout, stderr
	stdout, stderr = out, errOut
	return func() { stdout, stderr = previousOut, previousErr }
}

// runAllod runs the CLI with the given arguments and returns everything it
// printed plus its exit status. Nothing reaches the test process's own streams.
func runAllod(t *testing.T, args ...string) (stdoutText, stderrText string, code int) {
	t.Helper()
	var outBuffer, errBuffer bytes.Buffer
	restore := swapStreams(&outBuffer, &errBuffer)
	code = run(args)
	restore()
	return outBuffer.String(), errBuffer.String(), code
}

// siteCommandUsageBlock builds the part of a site command's argument-error
// output that follows its one-line message: the "Usage:\n" block for name
// and the "Run 'allod site <name> --help' for details.\n" pointer, exactly
// as siteCommandUsageError (site_common.go) appends them. It reads the same
// siteCommands table that function does, so a test pins the usage lines
// without retyping them — and without drifting from that table's declared
// order or contents whichever build carries it.
func siteCommandUsageBlock(t *testing.T, name string) string {
	t.Helper()
	entry, ok := siteCommandEntry(name)
	if !ok {
		t.Fatalf("siteCommandUsageBlock: unregistered site command: %s", name)
	}
	var block strings.Builder
	block.WriteString("Usage:\n")
	for _, line := range entry.usage {
		fmt.Fprintf(&block, "  %s\n", line)
	}
	fmt.Fprintf(&block, "\nRun 'allod site %s --help' for details.\n", name)
	return block.String()
}

// stubTools puts unusable executables of the given names on an otherwise empty
// PATH, so the LookPath preflights pass and anything that actually ran one of
// them would fail loudly. It returns the directory PATH was set to.
func stubTools(t *testing.T, names ...string) string {
	t.Helper()
	binDir := t.TempDir()
	for _, name := range names {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
			t.Fatalf("could not write stub %s: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir)
	return binDir
}

// useSiteRepo creates a site repository with the given site.toml and makes it
// the current directory. It lives here, not in site_test.go, because
// site_preview_test.go needs it in an untagged build too.
func useSiteRepo(t *testing.T, config string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, siteConfigName), []byte(config), 0644); err != nil {
		t.Fatalf("could not write %s: %v", siteConfigName, err)
	}
	t.Chdir(root)
	// t.TempDir can hand back a path through a symlink (/tmp -> /private/tmp
	// and the like); the command reports the resolved one.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("could not resolve %s: %v", root, err)
	}
	return resolved
}

// --- Dispatch ---

// TestUsageListsEveryRegisteredNamespace ties the usage text to the dispatch
// table: a build advertises the namespaces it carries, and only those.
func TestUsageListsEveryRegisteredNamespace(t *testing.T) {
	out, errText, code := runAllod(t)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if errText != "" {
		t.Errorf("stderr = %q, want empty", errText)
	}
	if !strings.HasPrefix(out, "Usage: allod <namespace> <command> [options]\n") {
		t.Errorf("stdout does not start with the usage line\ngot: %q", out)
	}
	for _, entry := range namespaces {
		if !strings.Contains(out, entry.name) || !strings.Contains(out, entry.summary) {
			t.Errorf("usage does not list the %s namespace\ngot: %q", entry.name, out)
		}
	}
	// One line of usage per namespace, plus the usage line, the blank line,
	// and the 'Namespaces:' heading.
	if got, want := strings.Count(out, "\n"), len(namespaces)+3; got != want {
		t.Errorf("usage has %d lines, want %d\ngot: %q", got, want, out)
	}
}

func TestCoreNamespacesAreAlwaysRegistered(t *testing.T) {
	for _, name := range []string{"change", "patch", "pr"} {
		if _, ok := lookupNamespace(name); !ok {
			t.Errorf("the %s namespace is not registered", name)
		}
	}
}

func TestHelpFlagPrintsUsage(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		out, errText, code := runAllod(t, flag)
		if code != 0 {
			t.Errorf("%s: exit code = %d, want 0", flag, code)
		}
		if errText != "" {
			t.Errorf("%s: stderr = %q, want empty", flag, errText)
		}
		if !strings.Contains(out, "Usage: allod <namespace>") {
			t.Errorf("%s: stdout does not contain the usage text\ngot: %q", flag, out)
		}
	}
}

func TestUnknownNamespace(t *testing.T) {
	_, errText, code := runAllod(t, "frobnicate")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "unknown command namespace: frobnicate"; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
	}
}

// TestRegisterNamespaceRejectsDuplicates covers the guard that keeps dispatch
// from depending on which file the compiler saw first. It panics rather than
// dies because it reports a mistake in the program, not in its input.
func TestRegisterNamespaceRejectsDuplicates(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("registering a duplicate namespace did not panic")
		}
		if got, ok := recovered.(string); !ok || !strings.Contains(got, "change") {
			t.Errorf("panic = %v, want one naming the duplicate", recovered)
		}
	}()
	registerNamespace(namespace{name: "change", summary: "a second claim on the word", main: changeMain})
}
