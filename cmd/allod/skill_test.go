package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill writes a SKILL.md with the given frontmatter body under
// <skillsDir>/<name>/ and returns the skills directory's path.
func writeSkill(t *testing.T, skillsDir, name, frontmatter string) string {
	t.Helper()
	dir := filepath.Join(skillsDir, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body := "---\n" + frontmatter + "---\n\n# " + name + "\n\nBody text.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return skillsDir
}

func TestSkillBareListsNamesAndDescriptionsSorted(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "zeta", "name: zeta\ndescription: Last alphabetically.\n")
	writeSkill(t, dir, "alpha", "name: alpha\ndescription: First alphabetically.\n")

	out, errText, code := runAllod(t, "skill", "--from", dir)

	if code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, errText)
	}
	want := "alpha: First alphabetically.\nzeta: Last alphabetically.\n"
	if out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
}

func TestSkillListBriefTruncation(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "long", "name: long\ndescription: "+strings.Repeat("word ", 30)+"\n")

	out, _, code := runAllod(t, "skill", "--from", dir)

	if code != 0 {
		t.Fatal(code)
	}
	line := strings.TrimSuffix(out, "\n")
	if !strings.HasSuffix(line, "...") || len(line) > 110 {
		t.Errorf("stdout %q, want a description truncated near the limit with an ellipsis", out)
	}
	if strings.Contains(line, "word  word") {
		t.Errorf("stdout %q, want single-spaced folded text", out)
	}
}

func TestSkillNamePrintsWholeFile(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "whole", "name: whole\ndescription: Printed in full.\n")
	wantBytes, err := os.ReadFile(filepath.Join(dir, "whole", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}

	out, errText, code := runAllod(t, "skill", "whole", "--from", dir)

	if code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, errText)
	}
	if out != string(wantBytes) {
		t.Errorf("stdout %q, want the SKILL.md verbatim", out)
	}
}

func TestSkillTwoNamesSeparatedByBlankLine(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "one", "name: one\ndescription: First.\n")
	writeSkill(t, dir, "two", "name: two\ndescription: Second.\n")

	out, _, code := runAllod(t, "skill", "one", "two", "--from", dir)

	if code != 0 {
		t.Fatal(code)
	}
	if out != "---\nname: one\ndescription: First.\n---\n\n# one\n\nBody text.\n\n---\nname: two\ndescription: Second.\n---\n\n# two\n\nBody text.\n" {
		t.Errorf("stdout %q", out)
	}
}

func TestSkillUnknownNameListsAvailable(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "real", "name: real\ndescription: Present.\n")

	_, errText, code := runAllod(t, "skill", "nothere", "--from", dir)

	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if !strings.Contains(errText, "unknown skill: nothere") || !strings.Contains(errText, "real") {
		t.Errorf("stderr %q, want the refusal to name the available skill", errText)
	}
}

func TestSkillFoldsBlockScalarDescription(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "folded", "name: folded\ndescription: >-\n  First part\n  second part.\n")

	out, _, code := runAllod(t, "skill", "--from", dir)

	if code != 0 {
		t.Fatal(code)
	}
	if out != "folded: First part second part.\n" {
		t.Errorf("stdout %q", out)
	}
}

func TestSkillNameFallsBackToDirectory(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "dirname", "description: No name key.\n")

	out, _, code := runAllod(t, "skill", "--from", dir)

	if code != 0 || out != "dirname: No name key.\n" {
		t.Errorf("code %d, stdout %q", code, out)
	}
}

func TestSkillSkipsSubdirectoryWithoutSkillFile(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "real", "name: real\ndescription: Present.\n")
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0755); err != nil {
		t.Fatal(err)
	}

	out, errText, code := runAllod(t, "skill", "--from", dir)

	if code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, errText)
	}
	if out != "real: Present.\n" {
		t.Errorf("stdout %q", out)
	}
	if !strings.Contains(errText, "empty") || !strings.Contains(errText, "SKILL.md") {
		t.Errorf("stderr %q, want a skip note naming the empty skill directory", errText)
	}
}

func TestSkillNoFrontmatterYieldsNoSkills(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plain"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain", "SKILL.md"), []byte("# no frontmatter\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, errText, code := runAllod(t, "skill", "--from", dir)

	if code != 1 {
		t.Errorf("exit code %d, want 1 for a directory that yields no skills", code)
	}
	if !strings.Contains(errText, "no skills found") {
		t.Errorf("stderr %q, want the no-skills refusal", errText)
	}
}

func TestSkillResolvesRegistryId(t *testing.T) {
	inventory := t.TempDir()
	base := t.TempDir()
	writeSkill(t, filepath.Join(base, "memrepo", "skills"), "registered", "name: registered\ndescription: Via registry id.\n")
	writeRegistryFixture(t, inventory, map[string]string{"allod/memory": "memrepo"})
	t.Setenv("INVENTORY", inventory)
	t.Setenv("WORK_DIR", base)

	out, errText, code := runAllod(t, "skill", "--from", "allod/memory")

	if code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, errText)
	}
	if out != "registered: Via registry id.\n" {
		t.Errorf("stdout %q", out)
	}
}

func TestSkillDefaultsToAllodMemory(t *testing.T) {
	inventory := t.TempDir()
	base := t.TempDir()
	writeSkill(t, filepath.Join(base, "memrepo", "skills"), "defaulted", "name: defaulted\ndescription: Default target.\n")
	writeRegistryFixture(t, inventory, map[string]string{"allod/memory": "memrepo"})
	t.Setenv("INVENTORY", inventory)
	t.Setenv("WORK_DIR", base)

	out, errText, code := runAllod(t, "skill")

	if code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, errText)
	}
	if out != "defaulted: Default target.\n" {
		t.Errorf("stdout %q", out)
	}
}

func TestSkillDefaultRefusesWhenRegistryLacksMemoryRepo(t *testing.T) {
	writeRegistryFixture(t, t.TempDir(), nil)
	t.Setenv("INVENTORY", t.TempDir())

	_, errText, code := runAllod(t, "skill")

	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if !strings.Contains(errText, "allod/memory") {
		t.Errorf("stderr %q, want the default target named", errText)
	}
}

func TestSkillFromFlagAccumulatesAndSplits(t *testing.T) {
	first := writeSkill(t, t.TempDir(), "one", "name: one\ndescription: From the first dir.\n")
	second := writeSkill(t, t.TempDir(), "two", "name: two\ndescription: From the second dir.\n")

	out, _, code := runAllod(t, "skill", "--from", first+","+second)

	if code != 0 {
		t.Fatal(code)
	}
	want := "one: From the first dir.\ntwo: From the second dir.\n"
	if out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
}

func TestSkillUnknownTarget(t *testing.T) {
	dir := t.TempDir()
	writeRegistryFixture(t, dir, nil)
	t.Setenv("INVENTORY", dir)

	_, errText, code := runAllod(t, "skill", "--from", "allod/nowhere")

	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if !strings.Contains(errText, "allod/nowhere") || !strings.Contains(errText, "not a registry id") {
		t.Errorf("stderr %q, want the unknown-target refusal", errText)
	}
}

func TestSkillOptionHandling(t *testing.T) {
	if _, errText, code := runAllod(t, "skill", "--from"); code != 1 || !strings.Contains(errText, "--from requires") {
		t.Errorf("missing --from value: code %d, stderr %q", code, errText)
	}
	if _, errText, code := runAllod(t, "skill", "--frobnicate"); code != 1 || !strings.Contains(errText, "unknown option for skill") {
		t.Errorf("unknown option: code %d, stderr %q", code, errText)
	}
	if out, _, code := runAllod(t, "skill", "--help"); code != 0 || !strings.Contains(out, "usage: allod skill") {
		t.Errorf("skill --help: code %d, stdout %q", code, out)
	}
}
