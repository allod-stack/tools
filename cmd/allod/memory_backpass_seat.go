package main

// A seat is "<argv...> <prompt-file>", no shell: the deployment's own
// wrapper script handles anything fancier. The seat's exit status is never
// the gate (delegate-impl, "gated on its content, never on its exit
// status") — only runSeat's timedOut flag can turn a call into a failure
// before the content is even looked at.

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

// runSeat runs seat in dir (the empty string means the caller's own cwd)
// against promptFile, stdin /dev/null, stdout captured as the answer, and
// stderr forwarded line by line to our stderr prefixed "seat <seat>: ".
//
// The call runs in its own process group (Setpgid) so a timeout can kill
// every descendant with syscall.Kill(-pgid, SIGKILL); shell.md's "pkill -f
// matches the shell that runs it" is why this never shells out to pkill.
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
