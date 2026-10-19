package main

// Step 1 of skills/memory-backpass/SKILL.md.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type memBullet struct {
	id   string
	text string
}

// Ids are run-local references and are never written back to a memory file.
func indexBullets(dir, prefix string) []memBullet {
	data, err := os.ReadFile(filepath.Join(dir, "memory.md"))
	if err != nil {
		return nil
	}
	var bullets []memBullet
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		n++
		bullets = append(bullets, memBullet{
			id:   fmt.Sprintf("%s-%03d", prefix, n),
			text: strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")),
		})
	}
	return bullets
}

func renderIndex(bullets []memBullet) string {
	var b strings.Builder
	for _, bullet := range bullets {
		fmt.Fprintf(&b, "[%s] %s\n", bullet.id, bullet.text)
	}
	return strings.TrimRight(b.String(), "\n")
}

func bulletText(bullets []memBullet, id string) string {
	for _, b := range bullets {
		if b.id == id {
			return b.text
		}
	}
	return ""
}

func topicFileIndex(dir, prefix string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "memory.md"))
	if err != nil {
		return nil
	}
	var out []string
	inSection := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			inSection = strings.Contains(trimmed, "Topic Files")
			continue
		}
		if inSection && strings.HasPrefix(trimmed, "- ") {
			out = append(out, prefix+": "+trimmed)
		}
	}
	return out
}

type ledgerLine struct {
	id   string // "L-xxxxxxxx"
	text string // the line without its leading "- "
	kind string // the word before the first ':' in text
}

var ledgerKindWord = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// The header's format line starts with the literal <kind>, which is not an entry.
func ledgerRuleKind(body string) (kind string, ok bool) {
	colon := strings.IndexByte(body, ':')
	if colon < 0 {
		return "", false
	}
	kind = body[:colon]
	return kind, ledgerKindWord.MatchString(kind)
}

// The id hashes the line exactly as it appears in the file, without the
// newline, so a hand-run sha256sum reproduces it.
func parseLedgerLines(dir string) []ledgerLine {
	data, err := os.ReadFile(filepath.Join(dir, "ledger.md"))
	if err != nil {
		return nil
	}
	var lines []ledgerLine
	for _, raw := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(raw, "- ") {
			continue
		}
		body := strings.TrimPrefix(raw, "- ")
		if strings.Count(body, " | ") < 2 {
			continue
		}
		kind, ok := ledgerRuleKind(body)
		if !ok {
			continue
		}
		sum := sha256.Sum256([]byte(raw))
		lines = append(lines, ledgerLine{
			id:   "L-" + fmt.Sprintf("%x", sum[:4]),
			text: body,
			kind: kind,
		})
	}
	return lines
}

func renderLedgerEntries(public, private []ledgerLine) string {
	var b strings.Builder
	for _, l := range public {
		fmt.Fprintf(&b, "[%s] %s\n", l.id, l.text)
	}
	for _, l := range private {
		fmt.Fprintf(&b, "[%s] %s\n", l.id, l.text)
	}
	return strings.TrimRight(b.String(), "\n")
}

func resolveLedgerMatch(id string, publicLedger, privateLedger []ledgerLine) (line ledgerLine, isPrivate, found bool) {
	for _, l := range privateLedger {
		if l.id == id {
			return l, true, true
		}
	}
	for _, l := range publicLedger {
		if l.id == id {
			return l, false, true
		}
	}
	return ledgerLine{}, false, false
}

func ledgerRuleText(body string) string {
	rest := body
	if colon := strings.IndexByte(body, ':'); colon >= 0 {
		rest = strings.TrimSpace(body[colon+1:])
	}
	if idx := strings.Index(rest, " | "); idx >= 0 {
		rest = rest[:idx]
	}
	return rest
}

func forgeOnPath() bool {
	_, err := exec.LookPath("forge")
	return err == nil
}

type backpassPR struct {
	number int
	title  string
	body   string
	merged bool
}

var prListNumberRe = regexp.MustCompile(`^\s*(\d+)`)
var prListStatusRe = regexp.MustCompile(`\b(open|closed|merged\s+\d{4}-\d{2}-\d{2})\s*$`)

// Defensive on purpose: a title may hold digits and spaces, so only the
// leading number and the trailing status token are trusted.
func parsePRListRow(line string) (number int, merged bool, ok bool) {
	numMatch := prListNumberRe.FindStringSubmatch(line)
	if numMatch == nil {
		return 0, false, false
	}
	n, err := strconv.Atoi(numMatch[1])
	if err != nil {
		return 0, false, false
	}
	statusMatch := prListStatusRe.FindStringSubmatch(strings.TrimRight(line, " \t\r"))
	if statusMatch == nil {
		return 0, false, false
	}
	return n, strings.HasPrefix(statusMatch[1], "merged"), true
}

func closedBackpassPRs(dir string) ([]backpassPR, error) {
	output, status := captureCommand(dir, nil, true, "forge", "pr", "list", "-s", "closed")
	if status != 0 {
		return nil, fmt.Errorf("forge pr list -s closed: exit %d: %s", status, output)
	}
	var prs []backpassPR
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "memory-backpass:") {
			continue
		}
		number, merged, ok := parsePRListRow(line)
		if !ok {
			continue
		}
		viewOut, viewStatus := captureCommand(dir, nil, true, "forge", "pr", "view", strconv.Itoa(number), "--json", "title,body")
		if viewStatus != 0 {
			continue
		}
		var parsed struct {
			Title string
			Body  string
		}
		if json.Unmarshal([]byte(viewOut), &parsed) != nil {
			continue
		}
		prs = append(prs, backpassPR{number: number, title: parsed.Title, body: parsed.Body, merged: merged})
	}
	return prs, nil
}

var toRelayHeadingRe = regexp.MustCompile(`(?m)^#+.*$`)
var uncheckedRelayRe = regexp.MustCompile(`(?m)^\s*-\s\[\s\]`)

func toRelaySection(body string) string {
	lines := strings.Split(body, "\n")
	start := -1
	for i, l := range lines {
		if toRelayHeadingRe.MatchString(l) && strings.Contains(l, "To relay") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if toRelayHeadingRe.MatchString(lines[i]) {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

func unrelayedPublicEdits(prs []backpassPR) []string {
	var out []string
	for _, pr := range prs {
		if pr.merged && uncheckedRelayRe.MatchString(toRelaySection(pr.body)) {
			out = append(out, fmt.Sprintf("#%d %s", pr.number, pr.title))
		}
	}
	return out
}

func rejectedEditTitles(prs []backpassPR) []string {
	var out []string
	for _, pr := range prs {
		if !pr.merged {
			out = append(out, pr.title)
		}
	}
	return out
}
