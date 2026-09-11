//go:build linux

package main

import "syscall"

// The ioctl pair terminal.go uses to read and write line settings. Linux
// names them TCGETS/TCSETS; the BSD family, macOS included, names the same
// operations TIOCGETA/TIOCSETA (termios_other.go).
const (
	ioctlReadTermios  = syscall.TCGETS
	ioctlWriteTermios = syscall.TCSETS
)
