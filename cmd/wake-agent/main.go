// Command wake-agent detaches one event-triggered Pi continuation.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const usage = `Usage:
  wake-agent [options] <probe-cmd> <brief-file>

Options:
  --interval <duration>  Delay between pending probes (default 5m)
  --grace <duration>     Delay before confirming a successful probe (default 30s)
  --cap <duration>       Maximum total watcher lifetime (default 24h)
  --timeout <duration>   Maximum lifetime of each Pi attempt (default 90m)
  --retry                Retry Pi once after a failure or timeout

The probe runs through /bin/sh -c. Exit 0 means the event was observed, 1
means keep waiting, 2 means the target is gone, and any other exit means the
probe failed. An observed event must remain observed after --grace.

Durable status and logs use $WAKE_AGENT_STATE_DIR when set, otherwise
$XDG_STATE_HOME/wake-agent or $HOME/.local/state/wake-agent. The live sentinel
uses $WAKE_AGENT_RUNTIME_DIR or $XDG_RUNTIME_DIR/wake-agent when available.
`

const (
	maxBriefBytes   = 128*1024 - 1
	maxProbeOutput  = 4096
	maxStatusDetail = 8192
	unitName        = "wake-agent.service"
)

type config struct {
	probe      string
	brief      string
	interval   time.Duration
	grace      time.Duration
	cap        time.Duration
	timeout    time.Duration
	retry      bool
	worker     bool
	help       bool
	stateDir   string
	runtimeDir string
	piPath     string
	scriptPath string
}

type stateWriter struct {
	dir      string
	runtime  string
	status   string
	sentinel string
	logPath  string
}

var (
	lookPath   = exec.LookPath
	executable = os.Executable
	command    = exec.Command
	now        = time.Now
	unitActive = systemdUnitActive
)

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	cfg, code := parseArgs(args, stderr)
	if code != 0 {
		return code
	}
	if cfg.help {
		return 0
	}

	state, err := newStateWriter(cfg.stateDir, cfg.runtimeDir)
	if err != nil {
		fmt.Fprintf(stderr, "wake-agent: %v\n", err)
		return 1
	}
	cfg.stateDir = state.dir
	cfg.runtimeDir = state.runtime

	if cfg.worker {
		return runWorker(cfg, state, stderr)
	}
	brief, err := readBrief(cfg.brief)
	if err != nil {
		_ = state.write("brief-failed", err.Error(), 0)
		fmt.Fprintf(stderr, "wake-agent: brief: %v\n", err)
		return 2
	}
	piPath, err := resolveExecutable("pi")
	if err != nil {
		_ = state.write("spawn-failed", "pi not found", 0)
		fmt.Fprintln(stderr, "wake-agent: pi not found")
		return 1
	}
	scriptPath, err := resolveExecutable("script")
	if err != nil {
		_ = state.write("spawn-failed", "script not found", 0)
		fmt.Fprintln(stderr, "wake-agent: script not found")
		return 1
	}
	cfg.piPath = piPath
	cfg.scriptPath = scriptPath
	if err := validatePiCommand(cfg.piPath, brief); err != nil {
		_ = state.write("brief-failed", err.Error(), 0)
		fmt.Fprintf(stderr, "wake-agent: brief: %v\n", err)
		return 2
	}
	return launch(cfg, state, stdout, stderr)
}

func resolveExecutable(name string) (string, error) {
	path, err := lookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

func parseArgs(args []string, stderr io.Writer) (config, int) {
	cfg := config{}
	flags := flag.NewFlagSet("wake-agent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usage) }
	flags.DurationVar(&cfg.interval, "interval", 5*time.Minute, "delay between probes")
	flags.DurationVar(&cfg.grace, "grace", 30*time.Second, "successful-probe confirmation delay")
	flags.DurationVar(&cfg.cap, "cap", 24*time.Hour, "total watcher lifetime")
	flags.DurationVar(&cfg.timeout, "timeout", 90*time.Minute, "Pi attempt lifetime")
	flags.BoolVar(&cfg.retry, "retry", false, "retry Pi once")
	flags.BoolVar(&cfg.worker, "worker", false, "run the watcher in the foreground")
	flags.StringVar(&cfg.stateDir, "state-dir", "", "state directory")
	flags.StringVar(&cfg.runtimeDir, "runtime-dir", "", "runtime directory")
	flags.StringVar(&cfg.piPath, "pi-path", "", "Pi executable")
	flags.StringVar(&cfg.scriptPath, "script-path", "", "script executable")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return config{help: true}, 0
	} else if err != nil {
		return config{}, 2
	}
	if flags.NArg() != 2 {
		fmt.Fprint(stderr, usage)
		return config{}, 2
	}
	if cfg.interval <= 0 || cfg.grace <= 0 || cfg.cap <= 0 || cfg.timeout <= 0 {
		fmt.Fprintln(stderr, "wake-agent: durations must be positive")
		return config{}, 2
	}
	cfg.probe, cfg.brief = flags.Arg(0), flags.Arg(1)
	brief, err := filepath.Abs(cfg.brief)
	if err != nil {
		fmt.Fprintf(stderr, "wake-agent: brief: %v\n", err)
		return config{}, 2
	}
	cfg.brief = brief
	return cfg, 0
}

func defaultStateDir() (string, error) {
	if dir := os.Getenv("WAKE_AGENT_STATE_DIR"); dir != "" {
		return filepath.Abs(dir)
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Abs(filepath.Join(dir, "wake-agent"))
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("cannot resolve state directory: HOME is unavailable")
	}
	return filepath.Join(home, ".local", "state", "wake-agent"), nil
}

func newStateWriter(override, runtimeOverride string) (stateWriter, error) {
	dir := override
	var err error
	if dir == "" {
		dir, err = defaultStateDir()
	} else {
		dir, err = filepath.Abs(dir)
	}
	if err != nil {
		return stateWriter{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return stateWriter{}, fmt.Errorf("create state directory: %w", err)
	}
	runtimeDir := runtimeOverride
	if runtimeDir == "" {
		if envDir := os.Getenv("WAKE_AGENT_RUNTIME_DIR"); envDir != "" {
			runtimeDir = envDir
		} else if xdgDir := os.Getenv("XDG_RUNTIME_DIR"); xdgDir != "" {
			runtimeDir = filepath.Join(xdgDir, "wake-agent")
		} else {
			runtimeDir = dir
		}
	}
	runtimeDir, err = filepath.Abs(runtimeDir)
	if err != nil {
		return stateWriter{}, fmt.Errorf("resolve runtime directory: %w", err)
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return stateWriter{}, fmt.Errorf("create runtime directory: %w", err)
	}
	return stateWriter{
		dir:      dir,
		runtime:  runtimeDir,
		status:   filepath.Join(dir, "status"),
		sentinel: filepath.Join(runtimeDir, "sentinel"),
		logPath:  filepath.Join(dir, "wake-agent.log"),
	}, nil
}

func (s stateWriter) write(stage, detail string, attempt int) error {
	detail = strings.NewReplacer("\n", " ", "\r", " ").Replace(detail)
	if len(detail) > maxStatusDetail {
		detail = detail[:maxStatusDetail] + " [truncated]"
	}
	var body strings.Builder
	fmt.Fprintf(&body, "stage=%s\nlog=%s\nupdated=%s\n", stage, s.logPath, now().UTC().Format(time.RFC3339))
	if attempt > 0 {
		fmt.Fprintf(&body, "attempt=%d\n", attempt)
	}
	if detail != "" {
		fmt.Fprintf(&body, "detail=%s\n", detail)
	}
	if err := atomicWrite(s.status, []byte(body.String())); err != nil {
		return err
	}
	return atomicWrite(s.sentinel, []byte(body.String()))
}

func atomicWrite(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wake-agent-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func launch(cfg config, state stateWriter, stdout, stderr io.Writer) int {
	systemdRun, err := lookPath("systemd-run")
	if err != nil {
		_ = state.write("detach-failed", "systemd-run not found", 0)
		fmt.Fprintln(stderr, "wake-agent: systemd-run not found")
		return 1
	}
	self, err := executable()
	if err != nil {
		_ = state.write("detach-failed", err.Error(), 0)
		fmt.Fprintf(stderr, "wake-agent: locate executable: %v\n", err)
		return 1
	}
	workerArgs := []string{
		"--user", "--collect", "--unit=wake-agent", "--property=Type=exec",
		"--same-dir",
		"--description=Wait for one event and continue one Pi agent",
		self, "--worker", "--state-dir", cfg.stateDir, "--runtime-dir", cfg.runtimeDir,
		"--pi-path", cfg.piPath, "--script-path", cfg.scriptPath,
		"--interval", cfg.interval.String(), "--grace", cfg.grace.String(),
		"--cap", cfg.cap.String(), "--timeout", cfg.timeout.String(),
	}
	if cfg.retry {
		workerArgs = append(workerArgs, "--retry")
	}
	workerArgs = append(workerArgs, "--", cfg.probe, cfg.brief)
	cmd := command(systemdRun, workerArgs...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if !sentinelIsActive(state.sentinel) || !unitActive() {
			_ = state.write("detach-failed", err.Error(), 0)
		}
		fmt.Fprintf(stderr, "wake-agent: could not start user unit: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "status: %s\nlog: %s\n", state.status, state.logPath)
	return 0
}

func sentinelIsActive(path string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "stage=") {
			switch strings.TrimPrefix(line, "stage=") {
			case "waiting", "confirming", "running", "retrying":
				return true
			default:
				return false
			}
		}
	}
	return false
}

func systemdUnitActive() bool {
	cmd := exec.Command("systemctl", "--user", "is-active", "--quiet", unitName)
	return cmd.Run() == nil
}

func runWorker(cfg config, state stateWriter, stderr io.Writer) (code int) {
	logFile, err := os.OpenFile(state.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = state.write("log-failed", err.Error(), 0)
		fmt.Fprintf(stderr, "wake-agent: open log: %v\n", err)
		return 1
	}
	defer logFile.Close()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, cfg.cap)
	defer cancel()

	terminalStage, detail, attempt := "canceled", "watcher stopped", 0
	defer func() {
		writeLogStage(logFile, terminalStage, detail)
		if err := state.write(terminalStage, detail, attempt); err != nil {
			fmt.Fprintf(stderr, "wake-agent: write terminal state: %v\n", err)
			code = 1
		}
	}()

	writeStage := func(stage, stageDetail string, stageAttempt int) error {
		writeLogStage(logFile, stage, stageDetail)
		return state.write(stage, stageDetail, stageAttempt)
	}

	if err := writeStage("waiting", "", 0); err != nil {
		terminalStage, detail = "state-failed", err.Error()
		return 1
	}

	for {
		probeCode, output := runProbe(ctx, cfg.probe)
		switch probeCode {
		case 0:
			if err := writeStage("confirming", "", 0); err != nil {
				terminalStage, detail = "state-failed", err.Error()
				return 1
			}
			if err := sleepContext(ctx, cfg.grace); err != nil {
				terminalStage, detail = stoppedStage(ctx), "watcher stopped during confirmation"
				return 1
			}
			probeCode, output = runProbe(ctx, cfg.probe)
			if probeCode == 0 {
				goto spawn
			}
			if probeCode == 1 {
				if err := writeStage("waiting", "event did not survive confirmation", 0); err != nil {
					terminalStage, detail = "state-failed", err.Error()
					return 1
				}
				continue
			}
		case 1:
			if err := sleepContext(ctx, cfg.interval); err != nil {
				terminalStage, detail = stoppedStage(ctx), "watcher stopped while waiting"
				return 1
			}
			continue
		}

		if probeCode == 2 {
			terminalStage, detail = "dead-target", output
			return 2
		}
		if ctx.Err() != nil {
			terminalStage, detail = stoppedStage(ctx), "watcher stopped during probe"
			return 1
		}
		terminalStage, detail = "probe-failed", fmt.Sprintf("exit %d: %s", probeCode, output)
		return 1
	}

spawn:
	brief, err := readBrief(cfg.brief)
	if err != nil {
		terminalStage, detail = "brief-failed", err.Error()
		return 1
	}
	piPath := cfg.piPath
	if piPath == "" {
		piPath, err = resolveExecutable("pi")
		if err != nil {
			terminalStage, detail = "spawn-failed", "pi not found"
			return 1
		}
	}
	scriptPath := cfg.scriptPath
	if scriptPath == "" {
		scriptPath, err = resolveExecutable("script")
		if err != nil {
			terminalStage, detail = "spawn-failed", "script not found"
			return 1
		}
	}
	if err := validatePiCommand(piPath, brief); err != nil {
		terminalStage, detail = "brief-failed", err.Error()
		return 1
	}
	attempts := 1
	if cfg.retry {
		attempts = 2
	}
	for attempt = 1; attempt <= attempts; attempt++ {
		if err := writeStage("running", "", attempt); err != nil {
			terminalStage, detail = "state-failed", err.Error()
			return 1
		}
		result, runErr := runPi(ctx, cfg.timeout, scriptPath, piPath, string(brief), logFile)
		if runErr == nil {
			terminalStage, detail = "complete", ""
			return 0
		}
		if ctx.Err() != nil {
			terminalStage, detail = result, runErr.Error()
			return 1
		}
		if attempt < attempts {
			if err := writeStage("retrying", result, attempt); err != nil {
				terminalStage, detail = "state-failed", err.Error()
				return 1
			}
			continue
		}
		terminalStage, detail = result, runErr.Error()
		return 1
	}
	return 1
}

func writeLogStage(log io.Writer, stage, detail string) {
	fmt.Fprintf(log, "%s stage=%s", now().UTC().Format(time.RFC3339), stage)
	if detail != "" {
		fmt.Fprintf(log, " detail=%s", strings.NewReplacer("\n", " ", "\r", " ").Replace(detail))
	}
	fmt.Fprintln(log)
}

func readBrief(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > maxBriefBytes {
		return nil, fmt.Errorf("too large: %d bytes (maximum %d)", info.Size(), maxBriefBytes)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.IndexByte(string(body), 0) >= 0 {
		return nil, errors.New("contains a NUL byte")
	}
	if len(body) > maxBriefBytes {
		return nil, fmt.Errorf("too large: %d bytes (maximum %d)", len(body), maxBriefBytes)
	}
	return body, nil
}

func validatePiCommand(piPath string, brief []byte) error {
	length := len(shellCommand([]string{piPath, "--no-session", "-p", string(brief)}))
	if length > maxBriefBytes {
		return fmt.Errorf("quoted Pi command is too large: %d bytes (maximum %d)", length, maxBriefBytes)
	}
	return nil
}

func stoppedStage(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "capped"
	}
	return "canceled"
}

func runProbe(ctx context.Context, probe string) (int, string) {
	output, err := os.CreateTemp("", "wake-agent-probe-*")
	if err != nil {
		return 125, err.Error()
	}
	defer os.Remove(output.Name())
	defer output.Close()
	err = runProcess(ctx, []string{"/bin/sh", "-c", probe}, output, output)
	text := readProbeOutput(output)
	if err == nil {
		return 0, text
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), text
	}
	return 125, strings.TrimSpace(err.Error() + " " + text)
}

func readProbeOutput(file *os.File) string {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "read probe output: " + err.Error()
	}
	body, err := io.ReadAll(io.LimitReader(file, maxProbeOutput+1))
	if err != nil {
		return "read probe output: " + err.Error()
	}
	truncated := len(body) > maxProbeOutput
	if truncated {
		body = body[:maxProbeOutput]
	}
	text := strings.TrimSpace(string(body))
	if truncated {
		text += " [truncated]"
	}
	return text
}

func runPi(parent context.Context, timeout time.Duration, scriptPath, piPath, brief string, log io.Writer) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	inner := shellCommand([]string{piPath, "--no-session", "-p", brief})
	err := runProcess(ctx, []string{scriptPath, "-qec", inner, os.DevNull}, log, log)
	if err == nil {
		return "", nil
	}
	if parent.Err() != nil {
		return stoppedStage(parent), parent.Err()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "agent-timeout", context.DeadlineExceeded
	}
	return "agent-failed", err
}

func shellCommand(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'"'"'`) + "'"
	}
	return strings.Join(quoted, " ")
}

func runProcess(ctx context.Context, argv []string, stdout, stderr io.Writer) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return err
	case <-ctx.Done():
		select {
		case err := <-done:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return err
		default:
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		waitForProcessGroup(cmd.Process.Pid, done)
		return ctx.Err()
	}
}

func waitForProcessGroup(pgid int, done <-chan error) {
	grace := time.NewTimer(2 * time.Second)
	defer grace.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	waited := false
	for {
		if waited && !processGroupExists(pgid) {
			return
		}
		select {
		case <-done:
			waited = true
		case <-poll.C:
		case <-grace.C:
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			if !waited {
				<-done
			}
			return
		}
	}
}

func processGroupExists(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
