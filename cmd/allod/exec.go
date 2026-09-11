package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const tempAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func makeTempDir(parent, prefix string, suffixLength int) (string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		random := make([]byte, suffixLength)
		if _, err := rand.Read(random); err != nil {
			return "", err
		}
		for i := range random {
			random[i] = tempAlphabet[int(random[i])%len(tempAlphabet)]
		}
		path := filepath.Join(parent, prefix+string(random))
		if err := os.Mkdir(path, 0700); err == nil {
			return path, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	return "", errors.New("could not allocate unique temporary directory")
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func runCommand(dir string, input io.Reader, out, errOut io.Writer, name string, args ...string) int {
	if name == "git" && dir != "" {
		args = append([]string{"-C", dir}, args...)
		dir = ""
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = input
	cmd.Stdout = out
	cmd.Stderr = errOut
	return commandExitCode(cmd.Run())
}

func captureCommand(dir string, input io.Reader, combined bool, name string, args ...string) (string, int) {
	var out bytes.Buffer
	if name == "git" && dir != "" {
		args = append([]string{"-C", dir}, args...)
		dir = ""
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = input
	cmd.Stdout = &out
	if combined {
		cmd.Stderr = &out
	}
	status := commandExitCode(cmd.Run())
	return strings.TrimRight(out.String(), "\n"), status
}

func gitOutput(dir string, args ...string) (string, bool) {
	out, status := captureCommand(dir, nil, false, "git", args...)
	return out, status == 0
}

func gitQuiet(dir string, args ...string) bool {
	return runCommand(dir, nil, io.Discard, io.Discard, "git", args...) == 0
}

func gitInherit(dir string, args ...string) int {
	return runCommand(dir, stdin, stdout, stderr, "git", args...)
}

func homeDir() string { return os.Getenv("HOME") }

func workDir() string {
	if value := os.Getenv("WORK_DIR"); value != "" {
		return strings.TrimRight(value, "/")
	}
	return filepath.Join(homeDir(), workRelative)
}

// workRelative is the path, relative to $HOME, that vm-provisioning.md
// requires every machine's checkouts to live under. The repository
// registry's "checkout" values are relative to this directory on every
// machine, including a remote host whose own WORK_DIR this process cannot
// see.
const workRelative = "work"

func readValue(source string) string {
	var data []byte
	var err error
	if source == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(source)
	}
	if err != nil {
		die(1, "cannot read file: %s", source)
	}
	return string(data)
}

func requireValue(args []string, option string) {
	if len(args) < 2 {
		die(1, "%s requires a value", option)
	}
}
