package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func skillMain(args []string) {
	var names, targets []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Fprint(stdout, skillUsageText)
			return
		case "--from":
			i++
			if i >= len(args) {
				die(1, "--from requires a registry id or directory")
			}
			targets = append(targets, strings.Split(args[i], ",")...)
		default:
			if strings.HasPrefix(args[i], "-") {
				die(1, "unknown option for skill: %s", args[i])
			}
			names = append(names, args[i])
		}
	}
	if len(names) == 0 {
		skillList(targets)
		return
	}
	skillShow(names, targets)
}

const skillUsageText = `usage: allod skill [<name> ...] [--from <target>]...

With no names, list the available skills: one line each with the skill's name
and a brief description. With one or more names, print each skill's SKILL.md
in full.

A --from target is a registry id such as allod/memory (its skills/ directory
is used) or a path to a skills directory; repeat or comma-separate targets to
search several. Without --from, skills come from allod/memory.
`

func skillList(targets []string) {
	dirs := resolveSkillDirs(targets)
	printed := 0
	for _, dir := range dirs {
		printed += printSkillLines(dir)
	}
	if printed == 0 {
		die(1, "no skills found in %s", strings.Join(dirs, ", "))
	}
}

// Every name resolves before anything prints, so one bad name fails the
// command instead of leaving output that looks complete.
func skillShow(names, targets []string) {
	dirs := resolveSkillDirs(targets)
	index := map[string]string{}
	var available []string
	for _, dir := range dirs {
		for _, skill := range readSkills(dir) {
			if _, seen := index[skill.name]; !seen {
				index[skill.name] = filepath.Join(dir, skill.dirName, "SKILL.md")
			}
			available = append(available, skill.name)
		}
	}
	slices.Sort(available)
	available = slices.Compact(available)
	for _, name := range names {
		if _, ok := index[name]; !ok {
			die(1, "unknown skill: %s (available: %s)", name, strings.Join(available, ", "))
		}
	}
	for i, name := range names {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		data, err := os.ReadFile(index[name])
		if err != nil {
			die(1, "cannot read %s: %v", index[name], err)
		}
		fmt.Fprint(stdout, string(data))
	}
}

func resolveSkillDirs(targets []string) []string {
	if len(targets) == 0 {
		targets = []string{"allod/memory"}
	}
	dirs := make([]string, 0, len(targets))
	for _, target := range targets {
		if info, err := os.Stat(target); err == nil && info.IsDir() {
			dirs = append(dirs, target)
			continue
		}
		checkout, ok := registryCheckout(target)
		if !ok {
			die(1, "unknown skills target: %s (not a directory, not a registry id)", target)
		}
		dirs = append(dirs, filepath.Join(workDir(), checkout, "skills"))
	}
	return dirs
}

// printSkillLines prints one line per skill under dir and returns how many.
func printSkillLines(dir string) int {
	skills := readSkills(dir)
	for _, skill := range skills {
		fmt.Fprintf(stdout, "%s: %s\n", skill.name, briefDescription(skill.description))
	}
	return len(skills)
}

type skillEntry struct {
	name        string
	description string
	dirName     string
}

// A subdirectory without a readable SKILL.md is skipped with a stderr note:
// a stray file must not hide the skills around it.
func readSkills(dir string) []skillEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		die(1, "cannot read skills directory %s: %v", dir, err)
	}
	var skills []skillEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skill, ok := parseSkillFile(filepath.Join(dir, entry.Name(), "SKILL.md"), entry.Name())
		if !ok {
			fmt.Fprintf(stderr, "allod: %s has no readable SKILL.md; skipped\n", filepath.Join(dir, entry.Name()))
			continue
		}
		skills = append(skills, skill)
	}
	slices.SortFunc(skills, func(a, b skillEntry) int { return strings.Compare(a.name, b.name) })
	return skills
}

// briefLimit caps a listed description; the full text is one `allod skill
// <name>` away, so the list stays skimmable.
const briefLimit = 100 // the full text is one `allod skill <name>` away; the list stays skimmable

func briefDescription(text string) string {
	if len(text) <= briefLimit {
		return text
	}
	cut := text[:briefLimit]
	if i := strings.LastIndexByte(cut, ' '); i > briefLimit/2 {
		cut = cut[:i]
	}
	return cut + "..."
}

// The Agent Skills format is a YAML subset: keys at the left margin between
// --- lines, with the description allowed as a block scalar continued on
// indented lines.
func parseSkillFile(path, fallbackName string) (skill skillEntry, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return skillEntry{}, false
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 || lines[0] != "---" {
		return skillEntry{}, false
	}
	skill.name = fallbackName
	skill.dirName = fallbackName
	description, lines, ok := frontmatterValue(lines[1:], "description")
	if ok {
		skill.description = description
	}
	if name, _, ok := frontmatterValue(lines, "name"); ok {
		skill.name = name
	}
	return skill, true
}

// rest has the key and any block-scalar continuation removed, so a second
// key can be read after it.
func frontmatterValue(lines []string, key string) (value string, rest []string, ok bool) {
	for i, line := range lines {
		if !strings.HasPrefix(line, key+":") {
			continue
		}
		rest = append(rest, lines[:i]...)
		text := strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		if isBlockScalar(text) {
			continuation := lines[i+1:]
			end := 0
			for end < len(continuation) && (continuation[end] == "" || continuation[end][0] == ' ' || continuation[end][0] == '\t') {
				end++
			}
			folded := make([]string, 0, end)
			for _, part := range continuation[:end] {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					folded = append(folded, trimmed)
				}
			}
			rest = append(rest, continuation[end:]...)
			return strings.Join(folded, " "), rest, true
		}
		rest = append(rest, lines[i+1:]...)
		return text, rest, true
	}
	return "", lines, false
}

// | or > alone or with - + digits after: the YAML block scalar indicators.
func isBlockScalar(text string) bool {
	if text == "" || (text[0] != '|' && text[0] != '>') {
		return false
	}
	for _, r := range text[1:] {
		if r != '-' && r != '+' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
