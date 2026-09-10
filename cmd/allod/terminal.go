package main

// Terminal plumbing shared by every command that asks the operator for a
// value it must not echo: 'allod site config' (behind the 'site' tag) and
// 'allod secret' (behind the 'secret' tag). It carries no tag of its own so
// either build can compile it without the other, and so neither tagged file
// owns code the other needs.
//
// The standard library has no terminal package, so echo control is the
// TCGETS/TCSETS ioctl pair directly — the same call cmd/forge makes to answer
// isatty.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"unsafe"
)

// isTerminal reports whether f is a terminal, by asking for its line
// settings: only a terminal has any.
func isTerminal(f *os.File) bool {
	var settings syscall.Termios
	return getTermios(f, &settings) == nil
}

// askSecretOnTerminal opens the controlling terminal, asks one question with
// echo off, and returns the answer without its line ending.
func askSecretOnTerminal(prompt string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("no terminal to ask on: %w", err)
	}
	defer tty.Close()
	return askSecret(tty, bufio.NewReader(tty), prompt)
}

func askLine(tty *os.File, reader *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(tty, prompt)
	line, err := reader.ReadString('\n')
	// A terminal closed mid-answer still returns what was typed before it
	// went; only an EOF with nothing before it is a failure to answer.
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func askSecret(tty *os.File, reader *bufio.Reader, prompt string) (string, error) {
	restore, err := disableEcho(tty)
	if err != nil {
		return "", err
	}
	defer restore()
	secret, err := askLine(tty, reader, prompt)
	// The Return that ended the answer was not echoed either, so the next
	// thing printed would land on the prompt line.
	fmt.Fprintln(tty)
	return secret, err
}

// disableEcho turns the terminal's echo off for the duration of one prompt,
// the way 'stty -echo' does, and returns the function that puts it back. The
// standard library has no terminal package, so this is the TCGETS/TCSETS
// ioctl pair directly — the same call cmd/forge makes to answer isatty.
//
// The restore also runs on SIGINT. Ctrl-C at a password prompt is an ordinary
// thing to do, and the default action would kill the process with echo still
// off, leaving the operator typing blind into their own shell afterwards.
func disableEcho(tty *os.File) (func(), error) {
	var original syscall.Termios
	if err := getTermios(tty, &original); err != nil {
		return nil, fmt.Errorf("could not read the terminal settings: %w", err)
	}
	quiet := original
	quiet.Lflag &^= syscall.ECHO
	if err := setTermios(tty, &quiet); err != nil {
		return nil, fmt.Errorf("could not turn off terminal echo: %w", err)
	}

	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-interrupted:
			_ = setTermios(tty, &original)
			// 128 + SIGINT: what a shell reports for a command its user
			// interrupted, which is what happened.
			os.Exit(130)
		case <-done:
		}
	}()

	return func() {
		signal.Stop(interrupted)
		close(done)
		_ = setTermios(tty, &original)
	}, nil
}

func getTermios(tty *os.File, out *syscall.Termios) error {
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, tty.Fd(),
		uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(out)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func setTermios(tty *os.File, in *syscall.Termios) error {
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, tty.Fd(),
		uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(in)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
