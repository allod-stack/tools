package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// An unresolvable group id must be an error, never "no groups": requireUntrustedNixUser
// refuses on it. A gid LookupGroupId cannot resolve exercises exactly that path.
func TestCurrentUserGroupsUnresolvableGid(t *testing.T) {
	fake := &user.User{Uid: "0", Gid: "999999999", Username: "allod-test-nonexistent"}
	if _, err := currentUserGroups(fake); err == nil {
		t.Errorf("currentUserGroups with an unresolvable gid should return an error, got nil")
	}
}

func TestSSHConfigCopierRewritesIncludeChain(t *testing.T) {
	temp := t.TempDir()
	home := filepath.Join(temp, "home")
	sshDir := filepath.Join(home, ".ssh")
	includeDir := filepath.Join(sshDir, "conf.d")
	bundle := filepath.Join(temp, "copies")
	for _, dir := range []string{includeDir, bundle} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mainConfig := filepath.Join(sshDir, "config")
	files := map[string]string{
		mainConfig:                             "# kept exactly\nHost forge.example\n  Include conf.d/*.conf missing.conf\n  User git\n",
		filepath.Join(includeDir, "10-a.conf"): "Include nested.conf\n  Port 22\n",
		filepath.Join(includeDir, "20-b.conf"): "  IdentityFile ~/.ssh/id_test\n",
		filepath.Join(sshDir, "nested.conf"):   "Host nested\n  User nested-user\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	copier := sshConfigCopier{dir: bundle, home: home, copies: make(map[string]string)}
	copyPath, err := copier.copyConfig(mainConfig, sshDir)
	if err != nil {
		t.Fatal(err)
	}
	mainCopy := readTestFile(t, copyPath)
	if !strings.HasPrefix(mainCopy, "# kept exactly\nHost forge.example\n") || !strings.HasSuffix(mainCopy, "  User git\n") {
		t.Fatalf("non-Include lines changed in main copy:\n%s", mainCopy)
	}
	if strings.Contains(mainCopy, "missing.conf") {
		t.Fatalf("missing Include target was retained:\n%s", mainCopy)
	}
	mainIncludes := testIncludePaths(mainCopy)
	if len(mainIncludes) != 2 {
		t.Fatalf("glob produced %d Include copies, want 2: %q", len(mainIncludes), mainCopy)
	}

	var nestedParent string
	for _, path := range mainIncludes {
		assertPrivateSSHCopy(t, path, bundle)
		content := readTestFile(t, path)
		if strings.Contains(content, "Port 22") {
			nestedParent = content
		}
	}
	if nestedParent == "" {
		t.Fatalf("glob copies did not contain the first included file: %q", mainIncludes)
	}
	nestedIncludes := testIncludePaths(nestedParent)
	if len(nestedIncludes) != 1 {
		t.Fatalf("nested Include produced %d copies, want 1: %q", len(nestedIncludes), nestedParent)
	}
	assertPrivateSSHCopy(t, nestedIncludes[0], bundle)
	if got := readTestFile(t, nestedIncludes[0]); got != files[filepath.Join(sshDir, "nested.conf")] {
		t.Fatalf("nested copy changed non-Include lines: got %q", got)
	}
}

func TestSSHIdentityFilesEqualsForms(t *testing.T) {
	temp := t.TempDir()
	home := filepath.Join(temp, "home")
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		line string
		key  string
	}{
		{"equals in keyword token", "IdentityFile=~/.ssh/attached", "attached"},
		{"standalone equals token", "IdentityFile = %d/.ssh/separated", "separated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			keyPath := filepath.Join(sshDir, test.key)
			if err := os.WriteFile(keyPath, []byte("key\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(temp, test.key+".config")
			if err := os.WriteFile(configPath, []byte(test.line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got := sshIdentityFiles(home, configPath)
			if len(got) != 1 || got[0] != keyPath {
				t.Fatalf("sshIdentityFiles() = %q, want [%q]", got, keyPath)
			}
		})
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func testIncludePaths(config string) []string {
	var paths []string
	for _, line := range strings.Split(config, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && strings.EqualFold(fields[0], "include") {
			paths = append(paths, fields[1:]...)
		}
	}
	return paths
}

func assertPrivateSSHCopy(t *testing.T, path, bundle string) {
	t.Helper()
	if filepath.Dir(path) != bundle {
		t.Fatalf("Include copy %q is outside private bundle %q", path, bundle)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("Include copy %q mode is %o, want 600", path, info.Mode().Perm())
	}
}
