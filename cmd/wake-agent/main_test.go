package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeBrief(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "brief.md")
	if err := os.WriteFile(path, []byte("Do the one requested thing.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfig(dir, brief, probe string) config {
	return config{
		probe: probe, brief: brief, stateDir: dir,
		interval: time.Millisecond, grace: time.Millisecond,
		cap: time.Second, timeout: time.Second,
	}
}

func testScriptPath(t *testing.T) string {
	t.Helper()
	path, err := resolveExecutable("script")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func readStage(t *testing.T, dir string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "stage=") {
			return strings.TrimPrefix(line, "stage=")
		}
	}
	t.Fatalf("status has no stage: %s", body)
	return ""
}

func TestHelpExitsSuccessfullyWithoutCreatingState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("WAKE_AGENT_STATE_DIR", dir)
	var stdout, stderr bytes.Buffer

	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want usage", &stderr)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state directory exists after help: %v", err)
	}
}

func TestLaunchRejectsNonRegularAndOversizedBriefs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		brief  func(*testing.T, string) string
		detail string
	}{
		{
			name: "directory",
			brief: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "brief-dir")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				return path
			},
			detail: "not a regular file",
		},
		{
			name: "oversized",
			brief: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "large-brief")
				if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxBriefBytes+1), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			detail: "too large",
		},
		{
			name: "unreadable",
			brief: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "unreadable-brief")
				if err := os.WriteFile(path, []byte("brief"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
				return path
			},
			detail: "permission denied",
		},
		{
			name: "nul-byte",
			brief: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "nul-brief")
				if err := os.WriteFile(path, []byte("before\x00after"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			detail: "contains a NUL byte",
		},
		{
			name: "quoted-command-too-large",
			brief: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "quote-heavy-brief")
				if err := os.WriteFile(path, bytes.Repeat([]byte("'"), 30_000), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			detail: "quoted Pi command is too large",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(bin, "pi"), `exit 0`)
			writeExecutable(t, filepath.Join(bin, "script"), `exit 0`)
			t.Setenv("PATH", bin)
			stateDir := filepath.Join(dir, "state")
			runtimeDir := filepath.Join(dir, "runtime")
			brief := tc.brief(t, dir)
			var stdout, stderr bytes.Buffer
			code := run([]string{"--state-dir", stateDir, "--runtime-dir", runtimeDir, "exit 0", brief}, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("run code = %d, want 2", code)
			}
			status, err := os.ReadFile(filepath.Join(stateDir, "status"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(status, []byte("stage=brief-failed\n")) || !bytes.Contains(status, []byte(tc.detail)) {
				t.Fatalf("status lacks brief failure:\n%s", status)
			}
		})
	}
}

func TestSentinelActiveStageParsingIsExact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(path, []byte("stage=complete\ndetail=stage=waiting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sentinelIsActive(path) {
		t.Fatal("terminal sentinel was treated as active from detail text")
	}
	if err := os.WriteFile(path, []byte("stage=waiting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !sentinelIsActive(path) {
		t.Fatal("waiting sentinel was not active")
	}
}

func TestWorkerConfirmsEventAndRunsFreshPiSession(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "artifact")
	prompt := filepath.Join(dir, "prompt")
	pi := filepath.Join(bin, "pi")
	writeExecutable(t, pi, `
test "$1" = "--no-session"
test "$2" = "-p"
printf '%s' "$3" > "$TEST_PROMPT"
printf 'landed\n' > "$TEST_ARTIFACT"
`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("TEST_ARTIFACT", artifact)
	t.Setenv("TEST_PROMPT", prompt)
	brief := writeBrief(t, dir)
	state, err := newStateWriter(dir, dir)
	if err != nil {
		t.Fatal(err)
	}

	if code := runWorker(testConfig(dir, brief, "exit 0"), state, &bytes.Buffer{}); code != 0 {
		t.Fatalf("runWorker code = %d", code)
	}
	if got := readStage(t, dir); got != "complete" {
		t.Fatalf("stage = %q, want complete", got)
	}
	if body, err := os.ReadFile(artifact); err != nil || string(body) != "landed\n" {
		t.Fatalf("artifact = %q, %v", body, err)
	}
	if body, err := os.ReadFile(prompt); err != nil || string(body) != "Do the one requested thing.\n" {
		t.Fatalf("prompt = %q, %v", body, err)
	}
	if got := readSentinelStage(t, dir); got != "complete" {
		t.Fatalf("sentinel stage = %q, want complete", got)
	}
}

func TestWorkerRecordsDeadTargetWithoutStartingPi(t *testing.T) {
	dir := t.TempDir()
	brief := writeBrief(t, dir)
	state, err := newStateWriter(dir, dir)
	if err != nil {
		t.Fatal(err)
	}

	if code := runWorker(testConfig(dir, brief, "echo gone; exit 2"), state, &bytes.Buffer{}); code != 2 {
		t.Fatalf("runWorker code = %d, want 2", code)
	}
	if got := readStage(t, dir); got != "dead-target" {
		t.Fatalf("stage = %q, want dead-target", got)
	}
	body, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("detail=gone\n")) {
		t.Fatalf("status does not preserve probe detail: %s", body)
	}
}

func TestWorkerRetriesPiExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(dir, "attempts")
	writeExecutable(t, filepath.Join(bin, "pi"), `
attempt=0
test ! -e "$TEST_COUNTER" || attempt=$(cat "$TEST_COUNTER")
attempt=$((attempt + 1))
printf '%s\n' "$attempt" > "$TEST_COUNTER"
test "$attempt" -eq 2
`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("TEST_COUNTER", counter)
	brief := writeBrief(t, dir)
	state, err := newStateWriter(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(dir, brief, "exit 0")
	cfg.retry = true

	if code := runWorker(cfg, state, &bytes.Buffer{}); code != 0 {
		t.Fatalf("runWorker code = %d", code)
	}
	if body, err := os.ReadFile(counter); err != nil || string(body) != "2\n" {
		t.Fatalf("attempts = %q, %v", body, err)
	}
}

func TestWorkerCapWritesTerminalStage(t *testing.T) {
	dir := t.TempDir()
	brief := writeBrief(t, dir)
	state, err := newStateWriter(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(dir, brief, "exit 1")
	cfg.interval = time.Second
	cfg.cap = 10 * time.Millisecond

	if code := runWorker(cfg, state, &bytes.Buffer{}); code != 1 {
		t.Fatalf("runWorker code = %d, want 1", code)
	}
	if got := readStage(t, dir); got != "capped" {
		t.Fatalf("stage = %q, want capped", got)
	}
	if got := readSentinelStage(t, dir); got != "capped" {
		t.Fatalf("sentinel stage = %q, want capped", got)
	}
	log, err := os.ReadFile(filepath.Join(dir, "wake-agent.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(log, []byte("stage=waiting")) || !bytes.Contains(log, []byte("stage=capped")) {
		t.Fatalf("log does not contain pending and terminal stages:\n%s", log)
	}
}

func TestWorkerRetryStopsAfterOneAdditionalAttempt(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(dir, "attempts")
	writeExecutable(t, filepath.Join(bin, "pi"), `
attempt=0
test ! -e "$TEST_COUNTER" || attempt=$(cat "$TEST_COUNTER")
printf '%s\n' "$((attempt + 1))" > "$TEST_COUNTER"
exit 9
`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("TEST_COUNTER", counter)
	brief := writeBrief(t, dir)
	state, err := newStateWriter(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(dir, brief, "exit 0")
	cfg.retry = true

	if code := runWorker(cfg, state, &bytes.Buffer{}); code != 1 {
		t.Fatalf("runWorker code = %d, want 1", code)
	}
	if body, err := os.ReadFile(counter); err != nil || string(body) != "2\n" {
		t.Fatalf("attempts = %q, %v", body, err)
	}
	if got := readStage(t, dir); got != "agent-failed" {
		t.Fatalf("stage = %q, want agent-failed", got)
	}
}

func TestWorkerDoesNotRetryAfterCap(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(dir, "attempts")
	writeExecutable(t, filepath.Join(bin, "pi"), `
attempt=0
test ! -e "$TEST_COUNTER" || attempt=$(cat "$TEST_COUNTER")
printf '%s\n' "$((attempt + 1))" > "$TEST_COUNTER"
trap 'exit 9' TERM
while :; do sleep 1; done
`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("TEST_COUNTER", counter)
	brief := writeBrief(t, dir)
	state, err := newStateWriter(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(dir, brief, "exit 0")
	cfg.cap = 50 * time.Millisecond
	cfg.timeout = time.Second
	cfg.retry = true

	if code := runWorker(cfg, state, &bytes.Buffer{}); code != 1 {
		t.Fatalf("runWorker code = %d, want 1", code)
	}
	if body, err := os.ReadFile(counter); err != nil || string(body) != "1\n" {
		t.Fatalf("attempts = %q, %v; want exactly one", body, err)
	}
	if got := readStage(t, dir); got != "capped" {
		t.Fatalf("stage = %q, want capped", got)
	}
	status, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(status, []byte("attempt=1\n")) || bytes.Contains(status, []byte("attempt=2\n")) {
		t.Fatalf("status contains a phantom attempt:\n%s", status)
	}
	log, err := os.ReadFile(filepath.Join(dir, "wake-agent.log"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(log, []byte("stage=retrying")) || bytes.Count(log, []byte("stage=running")) != 1 {
		t.Fatalf("cap emitted a phantom retry:\n%s", log)
	}
}

func TestWorkerReturnsToWaitingAfterTransientConfirmation(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(dir, "probes")
	probe := filepath.Join(bin, "probe")
	writeExecutable(t, probe, `
attempt=0
test ! -e "$TEST_COUNTER" || attempt=$(cat "$TEST_COUNTER")
attempt=$((attempt + 1))
printf '%s\n' "$attempt" > "$TEST_COUNTER"
test "$attempt" -ne 2
`)
	writeExecutable(t, filepath.Join(bin, "pi"), `exit 0`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("TEST_COUNTER", counter)
	brief := writeBrief(t, dir)
	state, err := newStateWriter(dir, dir)
	if err != nil {
		t.Fatal(err)
	}

	if code := runWorker(testConfig(dir, brief, probe), state, &bytes.Buffer{}); code != 0 {
		t.Fatalf("runWorker code = %d", code)
	}
	if body, err := os.ReadFile(counter); err != nil || string(body) != "4\n" {
		t.Fatalf("probe count = %q, %v; want four", body, err)
	}
	log, err := os.ReadFile(filepath.Join(dir, "wake-agent.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(log, []byte("event did not survive confirmation")) {
		t.Fatalf("log lacks transient confirmation branch:\n%s", log)
	}
}

func TestWorkerRecordsBrokenProbe(t *testing.T) {
	dir := t.TempDir()
	brief := writeBrief(t, dir)
	state, err := newStateWriter(dir, dir)
	if err != nil {
		t.Fatal(err)
	}

	if code := runWorker(testConfig(dir, brief, "echo broken; exit 7"), state, &bytes.Buffer{}); code != 1 {
		t.Fatalf("runWorker code = %d, want 1", code)
	}
	status, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(status, []byte("stage=probe-failed\n")) || !bytes.Contains(status, []byte("exit 7: broken")) {
		t.Fatalf("status lacks broken probe result:\n%s", status)
	}
}

func TestProbeOutputIsBounded(t *testing.T) {
	code, output := runProbe(context.Background(), `i=0; while [ "$i" -lt 6000 ]; do printf x; i=$((i + 1)); done; exit 2`)
	if code != 2 {
		t.Fatalf("probe code = %d, want 2", code)
	}
	if len(output) > maxProbeOutput+len(" [truncated]") {
		t.Fatalf("probe output length = %d, want bounded", len(output))
	}
	if !strings.HasSuffix(output, " [truncated]") {
		t.Fatalf("probe output lacks truncation marker: %q", output)
	}
}

func TestProbeDoesNotWaitForBackgroundOutputHolder(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	probe := fmt.Sprintf("sleep 30 & echo $! > '%s'; exit 2", pidFile)
	started := time.Now()
	code, _ := runProbe(context.Background(), probe)
	if code != 2 {
		t.Fatalf("probe code = %d, want 2", code)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("probe waited %s for background stdout holder", elapsed)
	}
	pid := readPID(t, pidFile)
	defer syscall.Kill(pid, syscall.SIGKILL)
	deadline := time.Now().Add(time.Second)
	for processExists(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(pid) {
		t.Fatalf("background probe process %d survived", pid)
	}
}

func TestPiTimeoutKillsTermIgnoringDescendant(t *testing.T) {
	dir := t.TempDir()
	childPID := filepath.Join(dir, "child.pid")
	termMarker := filepath.Join(dir, "term-received")
	pi := filepath.Join(dir, "pi")
	writeExecutable(t, pi, `
trap 'printf received > "$TEST_TERM_MARKER"; exit 0' TERM
sh -c 'trap "" TERM; echo "$$" > "$TEST_CHILD_PID"; while :; do sleep 1; done' &
wait
`)
	t.Setenv("TEST_CHILD_PID", childPID)
	t.Setenv("TEST_TERM_MARKER", termMarker)

	started := time.Now()
	result, err := runPi(context.Background(), 100*time.Millisecond, testScriptPath(t), pi, "brief", ioDiscard{})
	if result != "agent-timeout" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runPi = %q, %v; want agent-timeout, deadline exceeded", result, err)
	}
	if elapsed := time.Since(started); elapsed < 2*time.Second {
		t.Fatalf("timeout returned after %s, before the two-second TERM grace", elapsed)
	}
	if body, err := os.ReadFile(termMarker); err != nil || string(body) != "received" {
		t.Fatalf("leader did not record SIGTERM: %q, %v", body, err)
	}
	pid := readPID(t, childPID)
	defer syscall.Kill(pid, syscall.SIGKILL)
	deadline := time.Now().Add(2 * time.Second)
	for processExists(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(pid) {
		t.Fatalf("TERM-ignoring descendant %d survived timeout", pid)
	}
}

func TestPiReportsWatcherCapBeforeAttemptTimeout(t *testing.T) {
	dir := t.TempDir()
	pi := filepath.Join(dir, "pi")
	writeExecutable(t, pi, `trap 'exit 0' TERM; while :; do sleep 1; done`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	result, err := runPi(ctx, time.Second, testScriptPath(t), pi, "brief", ioDiscard{})
	if result != "capped" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runPi = %q, %v; want capped, deadline exceeded", result, err)
	}
}

func TestPiSuccessWinsExpiredParentRace(t *testing.T) {
	dir := t.TempDir()
	pi := filepath.Join(dir, "pi")
	writeExecutable(t, pi, `exit 0`)

	result, err := runPi(expiredWithoutSignalContext{}, time.Second, testScriptPath(t), pi, "brief", ioDiscard{})
	if result != "" || err != nil {
		t.Fatalf("runPi = %q, %v; want success", result, err)
	}
}

func TestPiPTYWrapperQuotesArgvAndPropagatesExit(t *testing.T) {
	dir := t.TempDir()
	piDir := filepath.Join(dir, "dir with ' quote")
	if err := os.Mkdir(piDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pi := filepath.Join(piDir, "pi ' helper")
	promptFile := filepath.Join(dir, "prompt")
	injected := filepath.Join(dir, "injected")
	writeExecutable(t, pi, `
test "$#" -eq 3
test "$1" = "--no-session"
test "$2" = "-p"
printf '%s' "$3" > "$TEST_PROMPT"
exit "$TEST_EXIT"
`)
	t.Setenv("TEST_PROMPT", promptFile)
	brief := "line one\n' ; touch '" + injected + "' ; # $(touch " + injected + ")"
	t.Setenv("TEST_EXIT", "0")

	result, err := runPi(context.Background(), time.Second, testScriptPath(t), pi, brief, ioDiscard{})
	if result != "" || err != nil {
		t.Fatalf("runPi = %q, %v; want success", result, err)
	}
	if body, err := os.ReadFile(promptFile); err != nil || string(body) != brief {
		t.Fatalf("prompt = %q, %v", body, err)
	}
	if _, err := os.Stat(injected); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell metacharacters escaped the prompt argument: %v", err)
	}

	t.Setenv("TEST_EXIT", "23")
	result, err = runPi(context.Background(), time.Second, testScriptPath(t), pi, brief, ioDiscard{})
	var exitErr *exec.ExitError
	if result != "agent-failed" || !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("runPi = %q, %v; want propagated exit 23", result, err)
	}
}

func TestPiCancellationSendsSIGTERM(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	termMarker := filepath.Join(dir, "term-received")
	pi := filepath.Join(dir, "pi")
	writeExecutable(t, pi, `
printf started > "$TEST_STARTED"
trap 'printf received > "$TEST_TERM_MARKER"; exit 9' TERM
while :; do sleep 1; done
`)
	t.Setenv("TEST_STARTED", started)
	t.Setenv("TEST_TERM_MARKER", termMarker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if _, err := os.Stat(started); err == nil {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	result, err := runPi(ctx, time.Second, testScriptPath(t), pi, "brief", ioDiscard{})
	if result != "canceled" || !errors.Is(err, context.Canceled) {
		t.Fatalf("runPi = %q, %v; want canceled", result, err)
	}
	if body, err := os.ReadFile(termMarker); err != nil || string(body) != "received" {
		t.Fatalf("process did not record SIGTERM: %q, %v", body, err)
	}
}

func TestLauncherPassesContractToTransientUserUnit(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "systemd-run.args")
	writeExecutable(t, filepath.Join(bin, "systemd-run"), `printf '%s\n' "$@" > "$SYSTEMD_ARGS"`)
	piPath := filepath.Join(bin, "pi")
	writeExecutable(t, piPath, `exit 0`)
	scriptPath := filepath.Join(bin, "script")
	writeExecutable(t, scriptPath, `exit 0`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("SYSTEMD_ARGS", argsFile)
	brief := writeBrief(t, dir)
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldDir) })
	stateDir := filepath.Join(dir, "state")
	runtimeDir := filepath.Join(dir, "runtime")
	t.Setenv("WAKE_AGENT_RUNTIME_DIR", runtimeDir)
	var stdout, stderr bytes.Buffer

	code := run([]string{
		"--state-dir", stateDir, "--retry",
		"--interval", "3s", "--grace", "4s", "--cap", "5m",
		"--timeout", "6m", "echo ready", filepath.Base(brief),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run code = %d, stderr = %s", code, &stderr)
	}
	body, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--user", "--collect", "--unit=wake-agent", "--property=Type=exec", "--same-dir",
		"--description=Wait for one event and continue one Pi agent",
		self, "--worker", "--state-dir", stateDir, "--runtime-dir", runtimeDir,
		"--pi-path", piPath, "--script-path", scriptPath,
		"--interval", "3s", "--grace", "4s", "--cap", "5m0s", "--timeout", "6m0s",
		"--retry", "--", "echo ready", brief,
	}
	got := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("systemd-run args:\n got: %q\nwant: %q", got, want)
	}
	if !strings.Contains(stdout.String(), "status:") || !strings.Contains(stdout.String(), "log:") {
		t.Fatalf("stdout = %q", &stdout)
	}
}

func TestLauncherOverwritesStaleActiveSentinelOnDetachFailure(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "systemd-run"), `exit 1`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	state, err := newStateWriter(filepath.Join(dir, "state"), filepath.Join(dir, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.write("waiting", "", 0); err != nil {
		t.Fatal(err)
	}
	oldUnitActive := unitActive
	unitActive = func() bool { return false }
	t.Cleanup(func() { unitActive = oldUnitActive })
	cfg := testConfig(state.dir, writeBrief(t, dir), "exit 0")
	cfg.runtimeDir = state.runtime
	cfg.piPath = "/bin/true"
	cfg.scriptPath = "/bin/true"

	if code := launch(cfg, state, ioDiscard{}, ioDiscard{}); code != 1 {
		t.Fatalf("launch code = %d, want 1", code)
	}
	if got := readStage(t, state.dir); got != "detach-failed" {
		t.Fatalf("stage = %q, want detach-failed", got)
	}
	if err := state.write("waiting", "", 0); err != nil {
		t.Fatal(err)
	}
	unitActive = func() bool { return true }
	if code := launch(cfg, state, ioDiscard{}, ioDiscard{}); code != 1 {
		t.Fatalf("second launch code = %d, want 1", code)
	}
	if got := readStage(t, state.dir); got != "waiting" {
		t.Fatalf("live watcher stage = %q, want waiting", got)
	}
}

func TestStateSnapshotsMatchAndStayPrivate(t *testing.T) {
	dir := t.TempDir()
	state, err := newStateWriter(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.write("running", "line one\nline two", 2); err != nil {
		t.Fatal(err)
	}
	status, err := os.ReadFile(state.status)
	if err != nil {
		t.Fatal(err)
	}
	sentinel, err := os.ReadFile(state.sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(status, sentinel) {
		t.Fatalf("status and sentinel differ:\nstatus:\n%ssentinel:\n%s", status, sentinel)
	}
	for _, want := range []string{"stage=running\n", "log=" + state.logPath + "\n", "attempt=2\n", "detail=line one line two\n"} {
		if !bytes.Contains(status, []byte(want)) {
			t.Errorf("snapshot does not contain %q:\n%s", want, status)
		}
	}
	for _, path := range []string{state.status, state.sentinel} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", path, got)
		}
	}
	if err := state.write("probe-failed", strings.Repeat("x", maxStatusDetail+1000), 0); err != nil {
		t.Fatal(err)
	}
	bounded, err := os.ReadFile(state.status)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded) > maxStatusDetail+256 || !bytes.Contains(bounded, []byte(" [truncated]\n")) {
		t.Fatalf("status detail was not bounded: %d bytes", len(bounded))
	}
}

func TestSentinelUsesRuntimeDirectory(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	runtimeRoot := filepath.Join(dir, "runtime")
	t.Setenv("WAKE_AGENT_STATE_DIR", stateDir)
	t.Setenv("WAKE_AGENT_RUNTIME_DIR", "")
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	state, err := newStateWriter("", "")
	if err != nil {
		t.Fatal(err)
	}
	if state.status != filepath.Join(stateDir, "status") {
		t.Fatalf("status = %q", state.status)
	}
	if state.sentinel != filepath.Join(runtimeRoot, "wake-agent", "sentinel") {
		t.Fatalf("sentinel = %q", state.sentinel)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

type expiredWithoutSignalContext struct{}

func (expiredWithoutSignalContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (expiredWithoutSignalContext) Done() <-chan struct{}       { return nil }
func (expiredWithoutSignalContext) Err() error                  { return context.DeadlineExceeded }
func (expiredWithoutSignalContext) Value(any) any               { return nil }

func readPID(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func processExists(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	fields := strings.Fields(string(stat))
	return len(fields) < 3 || fields[2] != "Z"
}

func readSentinelStage(t *testing.T, dir string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, "sentinel"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "stage=") {
			return strings.TrimPrefix(line, "stage=")
		}
	}
	return ""
}
