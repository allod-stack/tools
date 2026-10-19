package main

// A seat is <argv...> <prompt-file>, no shell; the deployment wraps anything
// fancier. Its exit status is never the gate, only the parsed content is.

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type seatOutcome struct {
	answer   string
	timedOut bool
	err      error // the process could not even be started
}

// The call runs in its own process group so a timeout kills every
// descendant by group id; shell.md records why pkill -f is never used here.
func runSeat(seat, promptFile, dir string, timeout time.Duration) seatOutcome {
	argv := strings.Fields(seat)
	if len(argv) == 0 {
		return seatOutcome{err: fmt.Errorf("empty seat command")}
	}
	cmd := exec.Command(argv[0], append(argv[1:], promptFile)...)
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
		prefixLines(stderrReader, stderr, "seat "+seat+": ")
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
