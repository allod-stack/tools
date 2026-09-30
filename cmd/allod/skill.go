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
search several. Without --from, skills come from every registry entry marked
"memory": true, in sorted registry-id order — allod/memory and any private
memory fork the deployment registers. A name reachable from several sources
resolves to the first source in that order.
`

// A name reachable from several sources prints once, from the first directory
// in resolved order.
func skillList(targets []string) {
	dirs := resolveSkillDirs(targets)
	seen := map[string]bool{}
	printed := 0
	for _, dir := range dirs {
		for _, skill := range readSkills(dir) {
			if seen[skill.name] {
				continue
			}
			seen[skill.name] = true
			fmt.Fprintf(stdout, "%s: %s\n", skill.name, briefDescription(skill.description))
			printed++
		}
	}
	if printed == 0 {
		if len(dirs) == 0 {
			die(1, "no skills found")
		}
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

// The registry resolves a target before a same-named relative directory. With
// no targets the sources come from the registry: see defaultSkillDirs.
func resolveSkillDirs(targets []string) []string {
	if len(targets) == 0 {
		return defaultSkillDirs()
	}
	dirs := make([]string, 0, len(targets))
	for _, target := range targets {
		if checkout, ok := registryCheckout(target); ok {
			dirs = append(dirs, filepath.Join(workDir(), checkout, "skills"))
			continue
		}
		if info, err := os.Stat(target); err == nil && info.IsDir() {
			dirs = append(dirs, target)
			continue
		}
		die(1, "unknown skills target: %s (not a directory, not a registry id)", target)
	}
	return dirs
}

// defaultSkillDirs lists the skills directories of every registry entry marked
// "memory": true, in sorted registry-id order — allod/memory's public skills
// and any private memory fork a deployment registers. A checkout not on disk is
// skipped silently: the deployment's vm-specs repos lists govern which machines
// clone which repos, and the sweep must not fail because one is missing here.
// When the registry marks no memory repos — no inventory checkout, standalone
// use — the single allod/memory default remains: the id maps to its own checkout
// unless the registry says otherwise.
func defaultSkillDirs() []string {
	ids := memoryRegistryIds()
	if len(ids) == 0 {
		checkout, ok := registryCheckout("allod/memory")
		if !ok {
			checkout = "allod/memory"
		}
		dir := filepath.Join(workDir(), checkout, "skills")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return []string{dir}
		}
		return nil
	}
	dirs := make([]string, 0, len(ids))
	for _, id := range ids {
		checkout, ok := registryCheckout(id)
		if !ok {
			continue
		}
		dir := filepath.Join(workDir(), checkout, "skills")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	return dirs
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
