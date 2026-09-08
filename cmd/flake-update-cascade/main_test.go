package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseArgsSinglePass(t *testing.T) {
	opts, code, done := parseArgs([]string{"nixpkgs", "--pr", "vm", "--dry-run"})
	if done || code != 0 {
		t.Fatalf("parse ended the run: code %d", code)
	}
	want := options{dryRun: true, prMode: true, names: []string{"nixpkgs", "vm"}}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("options = %+v, want %+v", opts, want)
	}

	// The first unknown option ends the run, even after valid names.
	if _, code, done := parseArgs([]string{"demo", "--bad"}); !done || code != 1 {
		t.Fatalf("unknown option: done %v code %d", done, code)
	}
	// Help wins wherever it appears, but only if no unknown option precedes it.
	if _, code, done := parseArgs([]string{"demo", "-h"}); !done || code != 0 {
		t.Fatalf("help: done %v code %d", done, code)
	}
	if _, code, done := parseArgs([]string{"--bad", "-h"}); !done || code != 1 {
		t.Fatalf("unknown option before help: done %v code %d", done, code)
	}
}

func TestInputNamePattern(t *testing.T) {
	for _, name := range []string{"vm", "allod-tools", "nixpkgs_unstable", "A1"} {
		if !inputNamePattern.MatchString(name) {
			t.Errorf("%q rejected", name)
		}
	}
	for _, name := range []string{"bad/input", "", "a b", "vm."} {
		if inputNamePattern.MatchString(name) {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestAtoiReadsGitCounts(t *testing.T) {
	cases := map[string]int{"0": 0, "1": 1, "12\n": 12, "": 0, "x": 0}
	for text, want := range cases {
		if got := atoi(text); got != want {
			t.Errorf("atoi(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestRemoteIsAllowed(t *testing.T) {
	dir := t.TempDir()
	c := &cascade{allowedFile: filepath.Join(dir, "allowed-external-remotes")}
	if !c.remoteIsAllowed("ssh://git@forge.anarch.diy:2222/acme/app.git") {
		t.Error("the forge needs no allowlist entry")
	}
	if c.remoteIsAllowed("https://github.com/acme/app.git") {
		t.Error("an external origin passed without an allowlist")
	}
	if err := os.WriteFile(c.allowedFile, []byte("# comment\n\ngithub.com/acme/allowed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !c.remoteIsAllowed("https://github.com/acme/allowed.git") {
		t.Error("an allowlisted origin was refused")
	}
	if c.remoteIsAllowed("https://github.com/acme/other.git") {
		t.Error("a comment or blank line admitted an origin")
	}
}

func TestForgeNamePattern(t *testing.T) {
	cases := map[string]string{
		"ssh://git@forge.example:2222/acme/app": "acme/app",
		"git@forge.example:acme/app":            "acme/app",
		"https://forge.example/acme/app":        "acme/app",
		"nonsense":                              "",
	}
	for url, want := range cases {
		if got := forgeNamePattern.FindString(url); got != want {
			t.Errorf("FindString(%q) = %q, want %q", url, got, want)
		}
	}
}

// collectRepos must return repositories in the directory walk's glob order: plain
// names first, dotted names after, each in byte order, with a dotted directory
// entered only when it is itself a repository, and a directory that is not a
// repository descended into.
func TestCollectReposFollowsTheGlobOrder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	work := t.TempDir()
	initRepo := func(rel string) {
		dir := filepath.Join(work, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "-C", dir, "init", "-q")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v\n%s", rel, err, out)
		}
	}
	for _, rel := range []string{"zeta", "allod/vm", "allod/archetypes", "allod/.profile", ".dotrepo", "Beta"} {
		initRepo(rel)
	}
	// A dotted directory that is not a repository is not entered, so the
	// repository below it is never found; a plain one is.
	initRepo(".hidden/inner")
	initRepo("plain/inner")
	if err := os.MkdirAll(filepath.Join(work, "allod", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := collectRepos(work + "/")
	want := []string{"Beta", "allod/archetypes", "allod/vm", "allod/.profile", "plain/inner", "zeta", ".dotrepo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectRepos = %v, want %v", got, want)
	}
}
