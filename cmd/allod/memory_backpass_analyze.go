package main

// Steps 2 and 3 of skills/memory-backpass/SKILL.md: find pending traces,
// analyze each with the bulk seat (one retry, then the fallback seat), and
// whitespace-fold every returned quote against the trace before trusting it.

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

// pendingTrace is one candidate session file under a --traces checkout.
type pendingTrace struct {
	checkoutDir  string
	checkoutBase string
	relPath      string // slash-separated, relative to checkoutDir
	absPath      string
	harness      string
	sessionID    string
	date         string // YYYY-MM-DD, from the filename
	hash         string // sha256 hex of the current file content
}

// tracePath is the TRACE_PATH value: the checkout's basename joined with
// the trace's path inside it, e.g. "traces-allod-dev/pi/2026-10-13-x.md".
func (t pendingTrace) tracePath() string { return t.checkoutBase + "/" + t.relPath }

// discoverTraces walks checkoutDir for "*/*/*.md", skipping dot-directories
// and any file whose name does not parse as "YYYY-MM-DD-<session-id>.md".
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

// readAnalyzedState reads <checkout>/.memory-backpass/analyzed.txt, in
// sha256sum format: "<hex>  <relative path>", two spaces.
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

// pendingTracesIn returns checkout's candidate traces (all) and, among
// them, the ones pending: no analyzed.txt line, or a hash that no longer
// matches (the file grew or changed since it was last read).
func pendingTracesIn(checkoutDir string) (all, pending []pendingTrace, err error) {
	all, err = discoverTraces(checkoutDir)
	if err != nil {
		return nil, nil, err
	}
	state := readAnalyzedState(checkoutDir)
	for i := range all {
		hash, hashErr := sha256File(all[i].absPath)
		if hashErr != nil {
			continue
		}
		all[i].hash = hash
		if state[all[i].relPath] != hash {
			pending = append(pending, all[i])
		}
	}
	return all, pending, nil
}

// evidenceItem is one surviving positive, negative, or gap item, with the
// session it came from attached for the ledger and fold steps.
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

// stripFence removes one surrounding Markdown fence: a first line of
// "```" or "```json" and a last line of "```".
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

// parseAnalysisAnswer gates the raw answer on content, never on the seat's
// exit status: it must parse as a JSON object whose positive, negative, and
// gaps members are all present arrays.
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

// gateItems drops a malformed item without failing the call: one missing a
// quote, a negative item with a class outside the three known ones, or a
// positive/negative item whose id is not in this run's instruction index.
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

// foldWhitespace collapses every run of Unicode whitespace to one space and
// trims the ends: the mandatory quote check (step 3) runs on this, never on
// raw text, and a model's confidence never substitutes for it.
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

// traceOutcome is one trace's full result: the gated, quote-checked
// evidence it contributed, or why it failed.
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

// analyzeOneTrace runs step 2 (fill, call, gate, retry, fallback) then step
// 3 (quote check) for one pending trace.
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
	promptPath := filepath.Join(scratchDir, strings.ReplaceAll(pt.tracePath(), "/", "_")+".prompt.md")
	if err := os.WriteFile(promptPath, []byte(filled), 0600); err != nil {
		return traceOutcome{trace: pt, failReason: fmt.Sprintf("could not write prompt file: %v", err)}
	}

	var attempts []string
	reason := ""
	try := func(seat string) (analysisAnswer, bool) {
		attempts = append(attempts, seat)
		out := runSeat(seat, promptPath, "", cfg.timeout)
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

// analyzeAllTraces runs cfg.jobs analyses at once; traces must already be
// sorted by tracePath so results, indexed the same way, need no re-sort.
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

// sortTracesByPath orders traces by TRACE_PATH so the report's ordering
// never depends on completion order or --traces argument order.
func sortTracesByPath(traces []pendingTrace) {
	sort.Slice(traces, func(i, j int) bool { return traces[i].tracePath() < traces[j].tracePath() })
}

// traceDatesByCite indexes every discovered trace (pending or not) by
// "<harness> <session id>" to its filename date, for step 6's age prune.
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
