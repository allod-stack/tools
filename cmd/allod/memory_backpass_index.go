package main

// Step 1 of skills/memory-backpass/SKILL.md: number memory.md's bullets,
// id every open ledger line, and list merged memory-backpass PRs whose
// "To relay" checklist is still unchecked.

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

// indexBullets numbers every memory.md bullet in dir by current position:
// PUB-001.. for prefix "PUB", PRV-001.. for "PRV". Ids are run-local and are
// never written back to a memory file.
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

// topicFileIndex reads memory.md's "Topic Files" section and renders its
// bullets prefixed "public:"/"private:" for the synthesis prompt.
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

// ledgerRuleKind reports whether a ledger.md line is a real entry (its kind
// token is a plain word, not the header template's literal "<kind>") and,
// if so, that kind.
func ledgerRuleKind(body string) (kind string, ok bool) {
	colon := strings.IndexByte(body, ':')
	if colon < 0 {
		return "", false
	}
	kind = body[:colon]
	return kind, ledgerKindWord.MatchString(kind)
}

// parseLedgerLines reads ledger.md in dir and ids every open entry: a line
// starting "- " with at least two " | " separators and a plain-word kind.
// The id is "L-" plus the first 8 lowercase hex characters of the SHA-256
// of the line exactly as it appears in the file, no trailing newline.
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

// renderLedgerEntries renders OPEN_LEDGER_ENTRIES: public lines first, then
// private, each "[L-xxxxxxxx] <line without the leading \"- \">".
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

// resolveLedgerMatch finds the ledger line a matchesLedger id names, in
// either ledger, so a gap item's match can be told apart as private (direct
// corroboration) or public (a new private line names it instead).
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

// ledgerRuleText extracts a ledger line's rule text: everything between its
// "<kind>: " prefix and the first " | " separator.
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

// backpassPR is one closed memory-backpass PR, with its title and body read
// back by 'forge pr view' so the relay check and REJECTED_EDIT_TITLES can
// read its "To relay" section and whether it landed.
type backpassPR struct {
	number int
	title  string
	body   string
	merged bool
}

var prListNumberRe = regexp.MustCompile(`^\s*(\d+)`)
var prListStatusRe = regexp.MustCompile(`\b(open|closed|merged\s+\d{4}-\d{2}-\d{2})\s*$`)

// parsePRListRow parses one 'forge pr list' row defensively: the number is
// its leading digits, the status its trailing open|closed|merged date token.
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

// closedBackpassPRs runs 'forge pr list -s closed' with dir as the working
// directory and reads title/body for every row naming a memory-backpass PR.
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

// toRelaySection returns a PR body's "To relay" section: from the heading
// line containing "To relay" to the next heading or the end of the body.
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

// unrelayedPublicEdits lists merged memory-backpass PRs whose "To relay"
// checklist still has an unchecked line: the public half has not landed.
func unrelayedPublicEdits(prs []backpassPR) []string {
	var out []string
	for _, pr := range prs {
		if pr.merged && uncheckedRelayRe.MatchString(toRelaySection(pr.body)) {
			out = append(out, fmt.Sprintf("#%d %s", pr.number, pr.title))
		}
	}
	return out
}

// rejectedEditTitles lists titles of closed, unmerged memory-backpass PRs,
// for REJECTED_EDIT_TITLES: the synthesis model should not repeat one
// without materially new evidence.
func rejectedEditTitles(prs []backpassPR) []string {
	var out []string
	for _, pr := range prs {
		if !pr.merged {
			out = append(out, pr.title)
		}
	}
	return out
}
