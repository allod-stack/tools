//go:build unix && !linux

package main

import "syscall"

// See termios_linux.go.
const (
	ioctlReadTermios  = syscall.TIOCGETA
	ioctlWriteTermios = syscall.TIOCSETA
)
