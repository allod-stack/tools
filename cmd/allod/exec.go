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

// A worker's replace refs must not rewrite ancestry in host-side Git queries.
const gitReplaceObjectsOff = "GIT_NO_REPLACE_OBJECTS=1"

var cloneGitEnv []string

// Once a ledgered clone is selected, Git must not load host hooks, filters,
// helpers, or executable settings while acting on worker-owned files.
func enableCloneGitEnv() {
	if cloneGitEnv != nil {
		return
	}
	name, _ := gitOutput("", "config", "--global", "user.name")
	email, _ := gitOutput("", "config", "--global", "user.email")
	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(key, "GIT_") && key != "SSH_ASKPASS" {
			env = append(env, kv)
		}
	}
	cloneGitEnv = append(env,
		gitReplaceObjectsOff, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_COUNT=4", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/dev/null",
		"GIT_CONFIG_KEY_1=user.name", "GIT_CONFIG_VALUE_1="+name,
		"GIT_CONFIG_KEY_2=user.email", "GIT_CONFIG_VALUE_2="+email,
		// A commit-graph a worker writes after begin cleared one is never consulted outside.
		"GIT_CONFIG_KEY_3=core.commitGraph", "GIT_CONFIG_VALUE_3=false")
}

func gitCommandEnv() []string {
	if cloneGitEnv != nil {
		return cloneGitEnv
	}
	return append(os.Environ(), gitReplaceObjectsOff)
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
	if name == "git" {
		cmd.Env = gitCommandEnv()
	}
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
	if name == "git" {
		cmd.Env = gitCommandEnv()
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

// Registry checkout paths are relative to this directory under $HOME.
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
