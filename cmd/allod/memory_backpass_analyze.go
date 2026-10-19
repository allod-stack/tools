package main

// Steps 2 and 3 of skills/memory-backpass/SKILL.md.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
)

type pendingTrace struct {
	checkoutDir   string
	checkoutBase  string
	checkoutIndex int    // this trace's position in cfg.tracesDirs
	relPath       string // slash-separated, relative to checkoutDir
	absPath       string
	harness       string
	sessionID     string
	date          string // YYYY-MM-DD, from the filename
	hash          string // sha256 hex of the current file content
}

// TRACE_PATH: the checkout basename joined with the path inside it.
func (t pendingTrace) tracePath() string { return t.checkoutBase + "/" + t.relPath }

func discoverTraces(checkoutDir string) ([]pendingTrace, error) {
	var all []pendingTrace
	base := filepath.Base(checkoutDir)
	err := filepath.WalkDir(checkoutDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(checkoutDir, path)
		if relErr != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if rel != "." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || filepath.Ext(name) != ".md" {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			fmt.Fprintf(stderr, "allod: memory backpass: skipping symlink: %s\n", path)
			return nil
		}
		segs := strings.Split(filepath.ToSlash(rel), "/")
		if len(segs) != 3 {
			return nil
		}
		stem := strings.TrimSuffix(segs[2], ".md")
		if len(stem) < 12 || stem[10] != '-' {
			return nil
		}
		all = append(all, pendingTrace{
			checkoutDir: checkoutDir, checkoutBase: base,
			relPath: filepath.ToSlash(rel), absPath: path,
			harness: segs[1], date: stem[:10], sessionID: stem[11:],
		})
		return nil
	})
	return all, err
}

func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}

// sha256sum format, two spaces, so sha256sum -c works in the checkout.
func readAnalyzedState(checkoutDir string) map[string]string {
	state := map[string]string{}
	data, err := os.ReadFile(filepath.Join(checkoutDir, ".memory-backpass", "analyzed.txt"))
	if err != nil {
		return state
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			continue
		}
		state[parts[1]] = parts[0]
	}
	return state
}

// A trace whose hash cannot be read is a failed outcome, never a silent skip.
func pendingTracesIn(checkoutDir string) (all, pending []pendingTrace, unreadable []traceOutcome, err error) {
	all, err = discoverTraces(checkoutDir)
	if err != nil {
		return nil, nil, nil, err
	}
	state := readAnalyzedState(checkoutDir)
	for i := range all {
		hash, hashErr := sha256File(all[i].absPath)
		if hashErr != nil {
			unreadable = append(unreadable, traceOutcome{
				trace: all[i], failReason: fmt.Sprintf("could not read trace: %v", hashErr),
			})
			continue
		}
		all[i].hash = hash
		if state[all[i].relPath] != hash {
			pending = append(pending, all[i])
		}
	}
	return all, pending, unreadable, nil
}

type evidenceItem struct {
	kind                string // "positive", "negative", "gap"
	id                  string // PUB-/PRV- id: positive and negative only
	class               string // negative only: harm|non-compliance|irrelevant
	quote, effect       string
	mistake             string
	proposedInstruction string
	matchesLedger       string
	harness, sessionID  string
}

type analysisItem struct {
	ID                  string `json:"id"`
	Moment              string `json:"moment"`
	Effect              string `json:"effect"`
	Quote               string `json:"quote"`
	Class               string `json:"class"`
	Mistake             string `json:"mistake"`
	ProposedInstruction string `json:"proposedInstruction"`
	RecurrenceRisk      string `json:"recurrenceRisk"`
	MatchesLedger       string `json:"matchesLedger"`
}

type analysisAnswer struct {
	Positive []analysisItem `json:"positive"`
	Negative []analysisItem `json:"negative"`
	Gaps     []analysisItem `json:"gaps"`
}

func stripFence(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return text
	}
	first := strings.TrimSpace(lines[0])
	last := strings.TrimSpace(lines[len(lines)-1])
	if (first == "```" || first == "```json") && last == "```" {
		return strings.Join(lines[1:len(lines)-1], "\n")
	}
	return text
}

// The gate is the parsed content, never the seat's exit status.
func parseAnalysisAnswer(raw string) (analysisAnswer, error) {
	text := stripFence(strings.TrimSpace(raw))
	var generic map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &generic); err != nil {
		return analysisAnswer{}, err
	}
	for _, key := range []string{"positive", "negative", "gaps"} {
		v, ok := generic[key]
		if !ok {
			return analysisAnswer{}, fmt.Errorf("missing %q array", key)
		}
		if !strings.HasPrefix(strings.TrimSpace(string(v)), "[") {
			return analysisAnswer{}, fmt.Errorf("%q is not an array", key)
		}
	}
	var answer analysisAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return analysisAnswer{}, err
	}
	return answer, nil
}

var validNegativeClass = map[string]bool{"harm": true, "non-compliance": true, "irrelevant": true}

func gateItems(answer analysisAnswer, validIDs map[string]bool) (items []evidenceItem, malformed int) {
	add := func(kind string, raw []analysisItem, requireID bool) {
		for _, it := range raw {
			if strings.TrimSpace(it.Quote) == "" {
				malformed++
				continue
			}
			if kind == "negative" && !validNegativeClass[it.Class] {
				malformed++
				continue
			}
			if requireID && !validIDs[it.ID] {
				malformed++
				continue
			}
			items = append(items, evidenceItem{
				kind: kind, id: it.ID, class: it.Class, quote: it.Quote,
				effect: it.Effect, mistake: it.Mistake,
				proposedInstruction: it.ProposedInstruction, matchesLedger: it.MatchesLedger,
			})
		}
	}
	add("positive", answer.Positive, true)
	add("negative", answer.Negative, true)
	add("gap", answer.Gaps, false)
	return items, malformed
}

// Step 3's mandatory check runs on this fold; a model's stated confidence never substitutes for it.
func foldWhitespace(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func quoteSurvives(quote, traceText string) bool {
	folded := foldWhitespace(quote)
	return folded != "" && strings.Contains(foldWhitespace(traceText), folded)
}

type traceOutcome struct {
	trace      pendingTrace
	analyzed   bool
	seat       string
	attempts   []string
	failReason string
	items      []evidenceItem
	malformed  int
	quoteDrops int
}

func analyzeOneTrace(cfg backpassConfig, pt pendingTrace, analysisTemplate, indexText, ledgerText string, validIDs map[string]bool, scratchDir string) traceOutcome {
	traceBytes, err := os.ReadFile(pt.absPath)
	if err != nil {
		return traceOutcome{trace: pt, failReason: fmt.Sprintf("could not read trace: %v", err)}
	}
	traceText := string(traceBytes)
	filled, err := fillTemplate(analysisTemplate, map[string]string{
		"INSTRUCTION_INDEX":   indexText,
		"OPEN_LEDGER_ENTRIES": ledgerText,
		"TRACE_PATH":          pt.tracePath(),
		"TRACE":               traceText,
	})
	if err != nil {
		return traceOutcome{trace: pt, failReason: fmt.Sprintf("template fill: %v", err)}
	}
	// The checkout index keeps two --traces checkouts with one basename apart.
	promptPath := filepath.Join(scratchDir, fmt.Sprintf("t%d_%s.prompt.md", pt.checkoutIndex, strings.ReplaceAll(pt.tracePath(), "/", "_")))
	if err := os.WriteFile(promptPath, []byte(filled), 0600); err != nil {
		return traceOutcome{trace: pt, failReason: fmt.Sprintf("could not write prompt file: %v", err)}
	}

	var attempts []string
	reason := ""
	try := func(seat string) (analysisAnswer, bool) {
		attempts = append(attempts, seat)
		out := runSeat(seat, promptPath, "", cfg.timeout, seatRoleAnalysis)
		if out.timedOut {
			reason = "timeout"
			return analysisAnswer{}, false
		}
		if out.err != nil {
			reason = out.err.Error()
			return analysisAnswer{}, false
		}
		parsed, perr := parseAnalysisAnswer(out.answer)
		if perr != nil {
			reason = "unparseable answer: " + perr.Error()
			return analysisAnswer{}, false
		}
		return parsed, true
	}

	var answer analysisAnswer
	seatUsed, ok := "", false
	if a, success := try(cfg.bulkSeat); success {
		answer, seatUsed, ok = a, cfg.bulkSeat, true
	} else if a, success := try(cfg.bulkSeat); success {
		answer, seatUsed, ok = a, cfg.bulkSeat, true
	} else if cfg.fallbackSeat != "" {
		if a, success := try(cfg.fallbackSeat); success {
			answer, seatUsed, ok = a, cfg.fallbackSeat, true
		}
	}
	if !ok {
		return traceOutcome{trace: pt, attempts: attempts, failReason: reason}
	}

	items, malformed := gateItems(answer, validIDs)
	var survivors []evidenceItem
	quoteDrops := 0
	for _, it := range items {
		it.harness, it.sessionID = pt.harness, pt.sessionID
		if quoteSurvives(it.quote, traceText) {
			survivors = append(survivors, it)
		} else {
			quoteDrops++
		}
	}
	return traceOutcome{
		trace: pt, analyzed: true, seat: seatUsed, attempts: attempts,
		items: survivors, malformed: malformed, quoteDrops: quoteDrops,
	}
}

// Contract: traces are already sorted by tracePath; results share its index.
func analyzeAllTraces(cfg backpassConfig, traces []pendingTrace, analysisTemplate, indexText, ledgerText string, validIDs map[string]bool, scratchDir string) []traceOutcome {
	results := make([]traceOutcome, len(traces))
	sem := make(chan struct{}, cfg.jobs)
	var wg sync.WaitGroup
	for i, pt := range traces {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, pt pendingTrace) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = analyzeOneTrace(cfg, pt, analysisTemplate, indexText, ledgerText, validIDs, scratchDir)
		}(i, pt)
	}
	wg.Wait()
	return results
}

func sortTracesByPath(traces []pendingTrace) {
	sort.Slice(traces, func(i, j int) bool { return traces[i].tracePath() < traces[j].tracePath() })
}

func traceDatesByCite(all []pendingTrace) map[string]string {
	dates := map[string]string{}
	for _, t := range all {
		key := t.harness + " " + t.sessionID
		if _, exists := dates[key]; !exists {
			dates[key] = t.date
		}
	}
	return dates
}
