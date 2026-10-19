package main

// A seat is <argv...> <prompt-file>, no shell; the deployment wraps anything
// fancier. Its exit status is never the gate, only the parsed content is.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type seatOutcome struct {
	answer   string
	timedOut bool
	err      error // the process could not even be started
}

// backpassBuiltinSeat is the seat value that selects the built-in: the
// default for bulk and synthesis when neither flag nor env names a seat,
// and usable as an explicit value for any seat role, including fallback.
const backpassBuiltinSeat = "pi"

func runSeat(seat, promptFile, dir string, timeout time.Duration) seatOutcome {
	if seat == backpassBuiltinSeat {
		return runBuiltinPiSeat(promptFile, dir, timeout)
	}
	argv := strings.Fields(seat)
	if len(argv) == 0 {
		return seatOutcome{err: fmt.Errorf("empty seat command")}
	}
	return runSeatArgv(seat, append(argv, promptFile), dir, timeout)
}

// runBuiltinPiSeat is the "pi" built-in: pi prints nothing without a
// terminal, so script -qec gives it one, and it takes the prompt as an
// @path argument (both measured on this VM, not documented behavior).
// The prompt path sits inside the inner shell's single quotes, so one in
// the path would break the quoting; scratch dirs never contain one, but
// this refuses rather than silently mis-invoking pi.
func runBuiltinPiSeat(promptFile, dir string, timeout time.Duration) seatOutcome {
	if strings.Contains(promptFile, "'") {
		return seatOutcome{err: fmt.Errorf("prompt file path contains a single quote: %s", promptFile)}
	}
	inner := fmt.Sprintf("pi -p '@%s'", promptFile)
	return runSeatArgv(backpassBuiltinSeat, []string{"script", "-qec", inner, "/dev/null"}, dir, timeout)
}

// checkBuiltinSeatPath is the up-front refusal for the built-in: called once
// a resolved seat value turns out to need it.
func checkBuiltinSeatPath() {
	var missing []string
	for _, name := range []string{"pi", "script"} {
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		die(1, "the built-in '%s' seat needs %s on PATH", backpassBuiltinSeat, strings.Join(missing, " and "))
	}
}

// backpassSeatName is how a seat is named in the run report: the built-in
// names the provider and model it actually defaults to, read from pi's own
// settings so the report never has to guess or go stale against it.
func backpassSeatName(seat string) string {
	if seat != backpassBuiltinSeat {
		return seat
	}
	provider, model, ok := readPiDefaultSettings()
	if !ok {
		return "pi (configured default)"
	}
	return fmt.Sprintf("pi (%s/%s)", provider, model)
}

func readPiDefaultSettings() (provider, model string, ok bool) {
	data, err := os.ReadFile(filepath.Join(homeDir(), ".pi", "agent", "settings.json"))
	if err != nil {
		return "", "", false
	}
	var settings struct {
		DefaultProvider string `json:"defaultProvider"`
		DefaultModel    string `json:"defaultModel"`
	}
	if json.Unmarshal(data, &settings) != nil || settings.DefaultProvider == "" || settings.DefaultModel == "" {
		return "", "", false
	}
	return settings.DefaultProvider, settings.DefaultModel, true
}

// runSeatArgv is every seat's shared machinery: its own process group so a
// timeout kills every descendant by group id (shell.md records why pkill -f
// is never used here), stdin /dev/null, stdout captured, stderr forwarded
// per line prefixed with the seat's label.
func runSeatArgv(label string, argv []string, dir string, timeout time.Duration) seatOutcome {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return seatOutcome{err: err}
	}
	defer devnull.Close()
	cmd.Stdin = devnull
	var out bytes.Buffer
	cmd.Stdout = &out
	stderrReader, stderrWriter := io.Pipe()
	cmd.Stderr = stderrWriter
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	copyDone := make(chan struct{})
	go func() {
		prefixLines(stderrReader, stderr, "seat "+label+": ")
		close(copyDone)
	}()

	if err := cmd.Start(); err != nil {
		stderrWriter.Close()
		<-copyDone
		return seatOutcome{err: err}
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		stderrWriter.Close()
		<-copyDone
		_ = err // exit status is never the gate; only the parsed content is
		return seatOutcome{answer: out.String()}
	case <-time.After(timeout):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waitDone
		stderrWriter.Close()
		<-copyDone
		return seatOutcome{answer: out.String(), timedOut: true}
	}
}

func prefixLines(r io.Reader, w io.Writer, prefix string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		fmt.Fprintf(w, "%s%s\n", prefix, scanner.Text())
	}
}
