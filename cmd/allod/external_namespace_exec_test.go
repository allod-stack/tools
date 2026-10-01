package main

// The stub is a compiled binary, not a shell script: the kernel replaces a
// shebang script's argv[0] with the file's real path, which would hide the
// git-style rename this test checks. The helper branch runs the real exec.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const execStubSource = `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	fmt.Println(os.Args[0])
	fmt.Println(strings.Join(os.Args[1:], " "))
	fmt.Println(os.Getenv("ALLOD_SENTINEL"))
	os.Exit(3)
}
`

func TestExternalCommandIsExeced(t *testing.T) {
	if os.Getenv("ALLOD_TEST_EXEC_HELPER") == "1" {
		run([]string{"frobnicate", "--wax", "on"})
		return
	}

	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH: cannot build the allod-frobnicate stub")
	}

	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "stub.go")
	if err := os.WriteFile(srcFile, []byte(execStubSource), 0644); err != nil {
		t.Fatalf("could not write stub source: %v", err)
	}
	binDir := t.TempDir()
	stubPath := filepath.Join(binDir, "allod-frobnicate")
	if out, err := exec.Command(goTool, "build", "-o", stubPath, srcFile).CombinedOutput(); err != nil {
		t.Fatalf("could not build stub: %v\n%s", err, out)
	}

	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "PATH="+binDir, "ALLOD_TEST_EXEC_HELPER=1", "ALLOD_SENTINEL=present")

	cmd := exec.Command(os.Args[0], "-test.run=^TestExternalCommandIsExeced$")
	cmd.Env = env
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("helper process error: %v (output: %s)", err, out)
	}
	if exitErr.ExitCode() != 3 {
		t.Errorf("exit code = %d, want 3", exitErr.ExitCode())
	}
	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	want := []string{"allod-frobnicate", "--wax on", "present"}
	if len(got) != len(want) {
		t.Fatalf("helper output = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("helper output line %d = %q, want %q", i, got[i], want[i])
		}
	}
}
