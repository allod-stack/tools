package main

// Steps 4 to 6 of skills/memory-backpass/SKILL.md.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// lineIndex -1 is a line created this run, appended at the end of the file.
type ledgerEntry struct {
	lineIndex      int
	raw            string // current text, "- ..." form
	ruleKey        string
	preRun         bool
	touchedThisRun bool
	prunedThisRun  bool
}

// Read once, rendered once: every line this run does not touch survives as is.
type ledgerBuilder struct {
	fileLines                   []string
	entries                     []*ledgerEntry
	publicLedger, privateLedger []ledgerLine
}

func newLedgerBuilder(dir string, publicLedger, privateLedger []ledgerLine) *ledgerBuilder {
	b := &ledgerBuilder{publicLedger: publicLedger, privateLedger: privateLedger}
	data, err := readFileOrEmpty(dir + "/ledger.md")
	if err != nil {
		return b
	}
	b.fileLines = strings.Split(data, "\n")
	for i, raw := range b.fileLines {
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
		b.entries = append(b.entries, &ledgerEntry{
			lineIndex: i, raw: raw, ruleKey: ruleKeyOf(kind, body), preRun: true,
		})
	}
	return b
}

var ignoredPhraseRe = regexp.MustCompile(`^(?:ignored|harm):\s*"([^"]*)"`)

// The dedup and corroboration key: the folded proposed instruction for a
// gap, the folded quoted phrase for an ignored or harm line.
func ruleKeyOf(kind, body string) string {
	switch kind {
	case "gap":
		return "gap:" + foldWhitespace(ledgerRuleText(body))
	case "ignored", "harm":
		if m := ignoredPhraseRe.FindStringSubmatch(body); m != nil {
			return kind + ":" + foldWhitespace(m[1])
		}
	}
	return ""
}

func (b *ledgerBuilder) findByRuleKey(key string) *ledgerEntry {
	if key == "" {
		return nil
	}
	for _, e := range b.entries {
		if e.ruleKey == key {
			return e
		}
	}
	return nil
}

func (b *ledgerBuilder) add(key, body string) *ledgerEntry {
	e := &ledgerEntry{lineIndex: -1, raw: "- " + body, ruleKey: key, touchedThisRun: true}
	b.entries = append(b.entries, e)
	return e
}

func (b *ledgerBuilder) corroborate(e *ledgerEntry, sessionTag, suffix string, corroborations *int) {
	if strings.Contains(e.raw, sessionTag) {
		return
	}
	e.raw += suffix
	e.touchedThisRun = true
	*corroborations++
}

func quoteWrap(s string) string { return "\"" + s + "\"" }

func (b *ledgerBuilder) addGap(item evidenceItem, corroborations *int) {
	sessionTag := item.harness + " " + item.sessionID
	suffix := fmt.Sprintf(" | %s | %s", sessionTag, quoteWrap(item.quote))

	if item.matchesLedger != "" {
		if match, isPrivate, found := resolveLedgerMatch(item.matchesLedger, b.publicLedger, b.privateLedger); found {
			if isPrivate {
				if e := b.findByRuleKey("gap:" + foldWhitespace(ledgerRuleText(match.text))); e != nil {
					b.corroborate(e, sessionTag, suffix, corroborations)
					return
				}
				// A pre-run line with no recoverable key falls through to the gap key.
			} else {
				ruleText := ledgerRuleText(match.text)
				key := "gap:" + foldWhitespace(ruleText)
				if e := b.findByRuleKey(key); e != nil {
					b.corroborate(e, sessionTag, suffix, corroborations)
					return
				}
				body := fmt.Sprintf("gap: %s | %s | %s Public ledger %s corroboration", ruleText, sessionTag, quoteWrap(item.quote), match.id)
				b.add(key, body)
				return
			}
		}
	}

	key := "gap:" + foldWhitespace(item.proposedInstruction)
	if e := b.findByRuleKey(key); e != nil {
		b.corroborate(e, sessionTag, suffix, corroborations)
		return
	}
	body := fmt.Sprintf("gap: %s | %s | %s", item.proposedInstruction, sessionTag, quoteWrap(item.quote))
	b.add(key, body)
}

func (b *ledgerBuilder) addNegative(item evidenceItem, instructionText string, corroborations *int) {
	sessionTag := item.harness + " " + item.sessionID
	words := strings.Fields(instructionText)
	if len(words) > 8 {
		words = words[:8]
	}
	phrase := strings.Join(words, " ")
	kind := "ignored"
	if item.class == "harm" {
		kind = "harm"
	}
	key := kind + ":" + foldWhitespace(phrase)
	suffix := fmt.Sprintf(" | %s | %s", sessionTag, quoteWrap(item.quote))
	if e := b.findByRuleKey(key); e != nil {
		b.corroborate(e, sessionTag, suffix, corroborations)
		return
	}
	body := fmt.Sprintf("%s: %s — %s | %s | %s", kind, quoteWrap(phrase), item.effect, sessionTag, quoteWrap(item.quote))
	if strings.HasPrefix(item.id, "PUB-") {
		body += " Public memory.md candidate"
	}
	b.add(key, body)
}

func buildLedger(b *ledgerBuilder, outcomes []traceOutcome, bullets map[string]string) (corroborations int) {
	for _, oc := range outcomes {
		if !oc.analyzed {
			continue
		}
		seenGap := map[string]bool{}
		seenNeg := map[string]bool{}
		for _, item := range oc.items {
			switch item.kind {
			case "gap":
				key := foldWhitespace(item.proposedInstruction)
				if seenGap[key] {
					continue
				}
				seenGap[key] = true
				b.addGap(item, &corroborations)
			case "negative":
				if item.class == "irrelevant" {
					continue
				}
				key := item.id + "|" + item.class
				if seenNeg[key] {
					continue
				}
				seenNeg[key] = true
				b.addNegative(item, bullets[item.id], &corroborations)
			}
		}
	}
	return corroborations
}

func (b *ledgerBuilder) addedLines() []string {
	var out []string
	for _, e := range b.entries {
		if e.lineIndex == -1 && !e.prunedThisRun {
			out = append(out, e.raw)
		}
	}
	return out
}

func (b *ledgerBuilder) corroboratedLines() []string {
	var out []string
	for _, e := range b.entries {
		if e.lineIndex >= 0 && e.touchedThisRun && !e.prunedThisRun {
			out = append(out, e.raw)
		}
	}
	return out
}

func (b *ledgerBuilder) renderFile() string {
	prunedIdx := map[int]bool{}
	replaceIdx := map[int]string{}
	var appended []string
	for _, e := range b.entries {
		if e.lineIndex < 0 {
			if !e.prunedThisRun {
				appended = append(appended, e.raw)
			}
			continue
		}
		if e.prunedThisRun {
			prunedIdx[e.lineIndex] = true
		} else {
			replaceIdx[e.lineIndex] = e.raw
		}
	}
	var out []string
	for i, line := range b.fileLines {
		if prunedIdx[i] {
			continue
		}
		if r, ok := replaceIdx[i]; ok {
			out = append(out, r)
			continue
		}
		out = append(out, line)
	}
	text := strings.Join(out, "\n")
	if len(appended) > 0 {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += strings.Join(appended, "\n") + "\n"
	}
	return text
}

var sessionCiteRe = regexp.MustCompile(`\| (\S+) (\S+) \| "`)

func sessionCitations(line string) [][2]string {
	matches := sessionCiteRe.FindAllStringSubmatch(line, -1)
	var out [][2]string
	for _, m := range matches {
		out = append(out, [2]string{m[1], m[2]})
	}
	return out
}

// A cited session with no trace found never prunes a line.
func pruneByAge(line string, traceDates map[string]string, today time.Time) bool {
	cites := sessionCitations(line)
	if len(cites) == 0 {
		return false
	}
	for _, c := range cites {
		dateStr, found := traceDates[c[0]+" "+c[1]]
		if !found {
			return false
		}
		d, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			return false
		}
		if today.Sub(d) <= 90*24*time.Hour {
			return false
		}
	}
	return true
}

// The hook's own exempt list: ledger.md trivially contains its own lines.
var pruneCoverageExempt = map[string]bool{"README.md": true, "LICENSE": true, "ledger.md": true, "retired.md": true}

// Gap lines only: an ignored or harm line quotes an instruction that is in
// the index by construction, so coverage would prune every one on sight.
func pruneByCoverage(ruleText, checkoutDir string) bool {
	folded := foldWhitespace(ruleText)
	if folded == "" {
		return false
	}
	entries, err := readDirNames(checkoutDir)
	if err != nil {
		return false
	}
	for _, name := range entries {
		if !strings.HasSuffix(name, ".md") || pruneCoverageExempt[name] {
			continue
		}
		if data, err := readFileOrEmpty(checkoutDir + "/" + name); err == nil && strings.Contains(foldWhitespace(data), folded) {
			return true
		}
	}
	return false
}

var publicCandidateSuffixRe = regexp.MustCompile(`Public \S+\.md candidate$`)

// Only pre-run lines this run has not touched are eligible for a prune.
func (b *ledgerBuilder) applyPrunes(traceDates map[string]string, today time.Time, privateDir, publicDir string) (pruned int) {
	for _, e := range b.entries {
		if !e.preRun || e.touchedThisRun {
			continue
		}
		body := strings.TrimPrefix(e.raw, "- ")
		kind, _ := ledgerRuleKind(body)
		owner := privateDir
		if publicCandidateSuffixRe.MatchString(body) {
			owner = publicDir
		}
		age := pruneByAge(body, traceDates, today)
		coverage := kind == "gap" && pruneByCoverage(ledgerRuleText(body), owner)
		if age || coverage {
			e.prunedThisRun = true
			pruned++
		}
	}
	return pruned
}

// A public line is never edited; a prune is listed under To relay for a human.
func publicPrunes(publicLedger []ledgerLine, traceDates map[string]string, today time.Time, publicDir string) []string {
	var out []string
	for _, l := range publicLedger {
		age := pruneByAge(l.text, traceDates, today)
		coverage := l.kind == "gap" && pruneByCoverage(ledgerRuleText(l.text), publicDir)
		if age || coverage {
			out = append(out, "- "+l.text)
		}
	}
	return out
}

type idStats struct {
	positive, nonCompliance, harm int
	sessions                      map[string]bool
}

func buildFoldTable(outcomes []traceOutcome, analyzedSessions int) string {
	stats := map[string]*idStats{}
	for _, oc := range outcomes {
		if !oc.analyzed {
			continue
		}
		for _, item := range oc.items {
			if item.kind == "gap" {
				continue
			}
			s := stats[item.id]
			if s == nil {
				s = &idStats{sessions: map[string]bool{}}
				stats[item.id] = s
			}
			s.sessions[item.harness+" "+item.sessionID] = true
			switch {
			case item.kind == "positive":
				s.positive++
			case item.class == "non-compliance":
				s.nonCompliance++
			case item.class == "harm":
				s.harm++
			}
		}
	}
	ids := make([]string, 0, len(stats))
	for id := range stats {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("| id | positive | non-compliance | harm | relevance |\n|---|---|---|---|---|\n")
	for _, id := range ids {
		s := stats[id]
		relevance := 0.0
		if analyzedSessions > 0 {
			relevance = float64(len(s.sessions)) / float64(analyzedSessions)
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %s |\n", id, s.positive, s.nonCompliance, s.harm, strconv.FormatFloat(relevance, 'f', 2, 64))
	}
	return strings.TrimRight(b.String(), "\n")
}

func buildFoldLedgerSection(b *ledgerBuilder) string {
	var out strings.Builder
	for _, e := range b.entries {
		if !e.touchedThisRun || e.prunedThisRun {
			continue
		}
		seen := map[string]bool{}
		for _, c := range sessionCitations(e.raw) {
			seen[c[0]+" "+c[1]] = true
		}
		fmt.Fprintf(&out, "%s (sessions: %d)\n", e.raw, len(seen))
	}
	return strings.TrimRight(out.String(), "\n")
}

// Every surviving quote must be here: the synthesis prompt allows only fold quotes.
func buildFoldQuotes(outcomes []traceOutcome, bullets map[string]string) string {
	groups := map[string][]evidenceItem{}
	var order []string
	for _, oc := range outcomes {
		if !oc.analyzed {
			continue
		}
		for _, item := range oc.items {
			rule := item.proposedInstruction
			if item.kind != "gap" {
				rule = bullets[item.id]
			}
			if _, ok := groups[rule]; !ok {
				order = append(order, rule)
			}
			groups[rule] = append(groups[rule], item)
		}
	}
	sort.Strings(order)
	var b strings.Builder
	for _, rule := range order {
		fmt.Fprintf(&b, "- %s\n", rule)
		for _, item := range groups[rule] {
			fmt.Fprintf(&b, "  - %s %s: %s\n", item.harness, item.sessionID, quoteWrap(item.quote))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
