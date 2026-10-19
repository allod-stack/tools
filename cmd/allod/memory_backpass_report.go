package main

// The run report, step 10, the state-file commit, and runBackpass, the driver.

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

type funnelStats struct {
	tracesAnalyzed int
	itemsReturned  int
	droppedQuote   int
	malformed      int
	corroborations int
	ledgerAdded    int
	prunes         int
	editsProposed  int
}

type failureRecord struct {
	path, reason string
	attempts     []string
}

type reportData struct {
	unrelayed    []string
	funnel       funnelStats
	failures     []failureRecord
	publicPrunes []string
	foldQuotes   map[string]bool
}

// Only a quote the fold actually offered counts as an edit's evidence.
func foldedQuoteSet(outcomes []traceOutcome) map[string]bool {
	set := map[string]bool{}
	for _, oc := range outcomes {
		if !oc.analyzed {
			continue
		}
		for _, item := range oc.items {
			set[foldWhitespace(item.quote)] = true
		}
	}
	return set
}

func computeFunnel(outcomes []traceOutcome, corroborations, ledgerAdded, prunes int) funnelStats {
	f := funnelStats{corroborations: corroborations, ledgerAdded: ledgerAdded, prunes: prunes}
	for _, oc := range outcomes {
		if !oc.analyzed {
			continue
		}
		f.tracesAnalyzed++
		f.itemsReturned += len(oc.items) + oc.quoteDrops
		f.droppedQuote += oc.quoteDrops
		f.malformed += oc.malformed
	}
	return f
}

func computeFailures(outcomes []traceOutcome) []failureRecord {
	var out []failureRecord
	for _, oc := range outcomes {
		if oc.analyzed {
			continue
		}
		out = append(out, failureRecord{path: oc.trace.tracePath(), attempts: oc.attempts, reason: oc.failReason})
	}
	return out
}

func seatDescription(seat string) string {
	if seat == "" {
		return "no fallback seat"
	}
	return seat
}

func perTraceSeats(outcomes []traceOutcome) string {
	var lines []string
	for _, oc := range outcomes {
		if !oc.analyzed {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", oc.trace.tracePath(), oc.seat))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func renderFunnel(f funnelStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "traces analyzed: %d\n", f.tracesAnalyzed)
	fmt.Fprintf(&b, "items returned: %d\n", f.itemsReturned)
	fmt.Fprintf(&b, "dropped for a missing quote: %d\n", f.droppedQuote)
	fmt.Fprintf(&b, "malformed: %d\n", f.malformed)
	fmt.Fprintf(&b, "ledger entries corroborated: %d\n", f.corroborations)
	fmt.Fprintf(&b, "ledger lines added: %d\n", f.ledgerAdded)
	fmt.Fprintf(&b, "prunes: %d\n", f.prunes)
	fmt.Fprintf(&b, "edits proposed: %d\n", f.editsProposed)
	return strings.TrimRight(b.String(), "\n")
}

func renderFailures(failures []failureRecord) string {
	if len(failures) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range failures {
		fmt.Fprintf(&b, "%s: attempts=%s reason=%s\n", f.path, strings.Join(f.attempts, ","), f.reason)
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderReport(cfg backpassConfig, rep reportData, outcomes []traceOutcome, ledger *ledgerBuilder, result writeResult, scratchKept, fold string) string {
	var b strings.Builder
	b.WriteString("## Unrelayed public edits\n\n")
	if len(rep.unrelayed) == 0 {
		b.WriteString("none\n")
	} else {
		for _, u := range rep.unrelayed {
			fmt.Fprintf(&b, "%s\n", u)
		}
	}

	b.WriteString("\n## Seats\n\n")
	fmt.Fprintf(&b, "bulk: %s\n", cfg.bulkSeat)
	fmt.Fprintf(&b, "fallback: %s\n", seatDescription(cfg.fallbackSeat))
	if cfg.synthesisSeat != "" {
		fmt.Fprintf(&b, "synthesis: %s\n", cfg.synthesisSeat)
	}
	if perTrace := perTraceSeats(outcomes); perTrace != "" {
		b.WriteString(perTrace)
		b.WriteString("\n")
	}

	b.WriteString("\n## Funnel\n\n")
	b.WriteString(renderFunnel(rep.funnel))
	b.WriteString("\n")

	if failures := renderFailures(rep.failures); failures != "" {
		b.WriteString("\n## Failures\n\n")
		b.WriteString(failures)
		b.WriteString("\n")
	}

	if scratchKept != "" {
		fmt.Fprintf(&b, "\nkept scratch dir for failed calls: %s\n", scratchKept)
	}

	if cfg.reportOnly {
		b.WriteString("\n## Fold\n\n")
		b.WriteString(fold)
		b.WriteString("\n")

		b.WriteString("\n## Would add to private ledger\n\n")
		added := ledger.addedLines()
		if len(added) == 0 {
			b.WriteString("none\n")
		} else {
			for _, l := range added {
				fmt.Fprintf(&b, "%s\n", l)
			}
		}
		b.WriteString("\n## Would corroborate\n\n")
		corroborated := ledger.corroboratedLines()
		if len(corroborated) == 0 {
			b.WriteString("none\n")
		} else {
			for _, l := range corroborated {
				fmt.Fprintf(&b, "%s\n", l)
			}
		}
		b.WriteString("\n## Would prune\n\n")
		var prunedLines []string
		for _, e := range ledger.entries {
			if e.prunedThisRun {
				prunedLines = append(prunedLines, e.raw)
			}
		}
		if len(prunedLines) == 0 && len(rep.publicPrunes) == 0 {
			b.WriteString("none\n")
		} else {
			for _, l := range prunedLines {
				fmt.Fprintf(&b, "private: %s\n", l)
			}
			for _, l := range rep.publicPrunes {
				fmt.Fprintf(&b, "public: %s\n", l)
			}
		}
		return b.String()
	}

	b.WriteString("\n## Write mode\n\n")
	if result.submitFailed {
		fmt.Fprintf(&b, "%s failed in %s\n", result.failedStep, result.privateWorktree)
		return b.String()
	}
	fmt.Fprintf(&b, "PR: %s\n", result.prURL)
	for _, p := range result.claimedAbsent {
		fmt.Fprintf(&b, "claimed, absent: %s\n", p)
	}
	for _, p := range result.editedUnreported {
		fmt.Fprintf(&b, "edited, unreported: %s\n", p)
	}
	if result.synthesisUnparseable {
		b.WriteString("synthesis answer unparseable; edits unreported\n")
	}
	if result.allEditsDropped {
		b.WriteString("every edit dropped: .hooks/memory-budget still failing after the fix call\n")
	}
	if result.publicWorktree != "" {
		fmt.Fprintf(&b, "public relay worktree: %s\n", result.publicWorktree)
		for _, t := range result.appliedRelay {
			fmt.Fprintf(&b, "relayed: %s\n", t)
		}
		for _, t := range result.failedRelay {
			fmt.Fprintf(&b, "could not apply: %s\n", t)
		}
		if result.publicBudgetFailed {
			b.WriteString("public .hooks/memory-budget failed; branch left as-is for review\n")
		}
	}
	if result.labelFailed {
		fmt.Fprintf(&b, "could not add relay label: %s\n", result.labelOutput)
	}
	return b.String()
}

// The synthesis gates as a reviewer-facing count: the human reading the PR
// is the gate, so nothing here reverts a hunk.
func editEvidenceStats(e editProposal, foldQuotes map[string]bool) (sessions, quotes int) {
	sources := map[string]bool{}
	for _, ev := range e.Evidence {
		if foldQuotes[foldWhitespace(ev.Quote)] {
			quotes++
			sources[ev.Source] = true
		}
	}
	return len(sources), quotes
}

func renderPRBody(date string, cfg backpassConfig, rep reportData, result writeResult, fold string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sessions read: %d. Findings kept: %d, dropped for a missing quote: %d. Edits proposed: %d.",
		rep.funnel.tracesAnalyzed, rep.funnel.itemsReturned-rep.funnel.droppedQuote, rep.funnel.droppedQuote, len(result.keptEdits))
	if len(result.keptEdits) > 5 {
		b.WriteString(" over the five-edit limit.")
	}
	b.WriteString("\n")

	for _, e := range result.keptEdits {
		fmt.Fprintf(&b, "\n## %s\n\n", e.Title)
		fmt.Fprintf(&b, "Paths: %s\n\n", strings.Join(e.Paths, ", "))
		fmt.Fprintf(&b, "%s\n\n", e.Rationale)
		sessions, quotes := editEvidenceStats(e, rep.foldQuotes)
		fmt.Fprintf(&b, "evidence: %d sessions, %d quotes\n", sessions, quotes)
		if sessions < 2 {
			b.WriteString("one-session exception: needs the safety reason in the rationale\n")
		}
		if quotes == 0 {
			b.WriteString("no quoted evidence from this run\n")
		}
		for _, ev := range e.Evidence {
			fmt.Fprintf(&b, "%s: %s\n", ev.Source, quoteWrap(ev.Quote))
		}
		if diff, ok := gitOutput(result.privateWorktree, append([]string{"diff", "--"}, e.Paths...)...); ok && diff != "" {
			fmt.Fprintf(&b, "\n```diff\n%s\n```\n", diff)
		}
	}

	b.WriteString("\n## To relay\n\n")
	if result.publicWorktree == "" {
		b.WriteString("nothing to relay\n")
	} else {
		receiveLine := receiveCommandLine(cfg, result.publicWorktree)
		if len(result.appliedRelay) > 0 {
			fmt.Fprintf(&b, "- [ ] %s (%s)\n", receiveLine, strings.Join(result.appliedRelay, ", "))
		}
		for _, t := range result.failedRelay {
			fmt.Fprintf(&b, "- [ ] could not apply: %s\n", t)
		}
	}
	for _, u := range rep.publicPrunes {
		fmt.Fprintf(&b, "- [ ] prune: %s\n", u)
	}

	b.WriteString("\n## Funnel\n\n")
	b.WriteString(renderFunnel(rep.funnel))
	b.WriteString("\n")

	if failures := renderFailures(rep.failures); failures != "" {
		b.WriteString("\n## Failures\n\n")
		b.WriteString(failures)
		b.WriteString("\n")
	}
	return b.String()
}

// The hostname is the agent's own, as git-workflow.md requires of a receive source.
func receiveCommandLine(cfg backpassConfig, publicWorktree string) string {
	host, err := os.Hostname()
	if err != nil {
		host = "localhost"
	}
	return fmt.Sprintf("allod patch receive %s:%s allod/memory", host, publicWorktree)
}

func computeAnalyzedState(checkoutDir string, all []pendingTrace, outcomesByRel map[string]traceOutcome) (changed bool, newState map[string]string) {
	old := readAnalyzedState(checkoutDir)
	existing := map[string]bool{}
	for _, t := range all {
		existing[t.relPath] = true
	}
	newState = map[string]string{}
	for path, hash := range old {
		if existing[path] {
			newState[path] = hash
		}
	}
	for _, t := range all {
		if oc, ok := outcomesByRel[t.relPath]; ok && oc.analyzed {
			newState[t.relPath] = t.hash
		}
	}
	if len(newState) != len(old) {
		return true, newState
	}
	for path, hash := range newState {
		if old[path] != hash {
			return true, newState
		}
	}
	return false, newState
}

func renderAnalyzedState(state map[string]string) string {
	paths := make([]string, 0, len(state))
	for p := range state {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "%s  %s\n", state[p], p)
	}
	return b.String()
}

// Reports whether the content changed: an unchanged file is not committed.
func writeAnalyzedState(checkoutDir string, newState map[string]string) bool {
	content := renderAnalyzedState(newState)
	stateDir := checkoutDir + "/.memory-backpass"
	stateFile := stateDir + "/analyzed.txt"
	old, _ := readFileOrEmpty(stateFile)
	if old == content {
		return false
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		fmt.Fprintf(stderr, "allod: could not create %s: %v\n", stateDir, err)
		return false
	}
	if err := os.WriteFile(stateFile, []byte(content), 0644); err != nil {
		fmt.Fprintf(stderr, "allod: could not write %s: %v\n", stateFile, err)
		return false
	}
	return true
}

func commitAnalyzedState(checkoutDir string, count int) {
	runCommand(checkoutDir, nil, io.Discard, io.Discard, "git", "add", ".memory-backpass/analyzed.txt")
	message := fmt.Sprintf("memory-backpass: %d traces analyzed", count)
	if status := runCommand(checkoutDir, nil, io.Discard, io.Discard, "git", "commit", "-m", message); status != 0 {
		fmt.Fprintf(stderr, "allod: could not commit the analyzed.txt update in %s\n", checkoutDir)
	}
}

var analysisKnownPlaceholders = map[string]bool{
	"INSTRUCTION_INDEX": true, "OPEN_LEDGER_ENTRIES": true, "TRACE_PATH": true, "TRACE": true,
}
var synthesisKnownPlaceholders = map[string]bool{
	"PRIVATE_WORKTREE": true, "INSTRUCTION_INDEXES_WITH_BYTE_COUNTS": true,
	"TOPIC_FILE_INDEX": true, "FOLD": true, "REJECTED_EDIT_TITLES": true,
}

func runBackpass(cfg backpassConfig) {
	forgeAvailable := forgeOnPath()
	if !forgeAvailable && !cfg.reportOnly {
		die(1, "'forge' not found on PATH; memory backpass needs it for the relay check and, in write mode, for submit")
	}

	// Step 1: index memory.md bullets and open ledger lines, both repos.
	publicBullets := indexBullets(cfg.publicDir, "PUB")
	privateBullets := indexBullets(cfg.privateDir, "PRV")
	bulletsByID := map[string]string{}
	validIDs := map[string]bool{}
	for _, b := range publicBullets {
		bulletsByID[b.id], validIDs[b.id] = b.text, true
	}
	for _, b := range privateBullets {
		bulletsByID[b.id], validIDs[b.id] = b.text, true
	}
	publicLedgerLines := parseLedgerLines(cfg.publicDir)
	privateLedgerLines := parseLedgerLines(cfg.privateDir)

	var unrelayed []string
	var prs []backpassPR
	if forgeAvailable {
		var err error
		prs, err = closedBackpassPRs(cfg.privateDir)
		if err != nil {
			if !cfg.reportOnly {
				die(1, "relay check: %v", err)
			}
			fmt.Fprintf(stderr, "allod: memory backpass: relay check: %v\n", err)
		}
		unrelayed = unrelayedPublicEdits(prs)
	} else {
		fmt.Fprintln(stdout, "relay check skipped: forge not on PATH")
	}

	// Prompts come from the public checkout at run time, never embedded.
	analysisTemplate := readFileOrDie(cfg.publicDir + "/skills/memory-backpass/analysis-prompt.md")
	synthesisTemplate := readFileOrDie(cfg.publicDir + "/skills/memory-backpass/synthesis-prompt.md")
	if err := checkTemplateHeader("analysis-prompt.md", analysisTemplate); err != nil {
		die(1, "%v", err)
	}
	if err := checkTemplateHeader("synthesis-prompt.md", synthesisTemplate); err != nil {
		die(1, "%v", err)
	}
	if err := checkTemplatePlaceholders("analysis-prompt.md", analysisTemplate, analysisKnownPlaceholders); err != nil {
		die(1, "%v", err)
	}
	if err := checkTemplatePlaceholders("synthesis-prompt.md", synthesisTemplate, synthesisKnownPlaceholders); err != nil {
		die(1, "%v", err)
	}

	indexText := renderIndex(publicBullets) + "\n\n" + renderIndex(privateBullets)
	ledgerText := renderLedgerEntries(publicLedgerLines, privateLedgerLines)

	// Step 2: every pending trace across every --traces checkout.
	var allTraces, pending []pendingTrace
	var unreadableOutcomes []traceOutcome
	tracesByCheckout := map[string][]pendingTrace{}
	for i, dir := range cfg.tracesDirs {
		all, pend, badOutcomes, err := pendingTracesIn(dir)
		if err != nil {
			die(1, "cannot walk traces checkout %s: %v", dir, err)
		}
		for j := range all {
			all[j].checkoutIndex = i
		}
		for j := range pend {
			pend[j].checkoutIndex = i
		}
		for j := range badOutcomes {
			badOutcomes[j].trace.checkoutIndex = i
		}
		tracesByCheckout[dir] = all
		allTraces = append(allTraces, all...)
		pending = append(pending, pend...)
		unreadableOutcomes = append(unreadableOutcomes, badOutcomes...)
	}
	sortTracesByPath(pending)

	scratchDir, err := os.MkdirTemp("", "allod-backpass-")
	if err != nil {
		die(1, "could not create a scratch directory: %v", err)
	}

	// Steps 2 and 3: analyze and quote-check every pending trace.
	outcomes := analyzeAllTraces(cfg, pending, analysisTemplate, indexText, ledgerText, validIDs, scratchDir)
	outcomes = append(outcomes, unreadableOutcomes...)
	sort.Slice(outcomes, func(i, j int) bool { return outcomes[i].trace.tracePath() < outcomes[j].trace.tracePath() })

	anyFailed := false
	for _, oc := range outcomes {
		if !oc.analyzed {
			anyFailed = true
		}
	}

	// Step 4: ledger lines, into the private checkout's ledger.md text.
	builder := newLedgerBuilder(cfg.privateDir, publicLedgerLines, privateLedgerLines)
	corroborations := buildLedger(builder, outcomes, bulletsByID)

	// Step 6: prunes over pre-run lines only, before anything is written.
	today := time.Now().UTC()
	traceDates := traceDatesByCite(allTraces)
	prunedCount := builder.applyPrunes(traceDates, today, cfg.privateDir, cfg.publicDir)
	publicPruneCandidates := publicPrunes(publicLedgerLines, traceDates, today, cfg.publicDir)

	// Step 5: the evidence fold, for the synthesis prompt.
	analyzedCount := 0
	for _, oc := range outcomes {
		if oc.analyzed {
			analyzedCount++
		}
	}
	fold := "## Instruction relevance\n\n" + buildFoldTable(outcomes, analyzedCount) +
		"\n\n## Ledger rules touched this run\n\n" + buildFoldLedgerSection(builder) +
		"\n\n## Evidence quotes\n\n" + buildFoldQuotes(outcomes, bulletsByID)

	funnel := computeFunnel(outcomes, corroborations, len(builder.addedLines()), prunedCount)
	failures := computeFailures(outcomes)
	rep := reportData{
		unrelayed: unrelayed, funnel: funnel, failures: failures,
		publicPrunes: publicPruneCandidates, foldQuotes: foldedQuoteSet(outcomes),
	}

	var result writeResult
	if !cfg.reportOnly {
		date := today.Format("2006-01-02")
		result = writeBackpass(cfg, date, builder, publicBullets, privateBullets, prs, fold, scratchDir, rep)
		rep.funnel.editsProposed = len(result.keptEdits)
		if result.submitFailed {
			rep.failures = append(rep.failures, failureRecord{
				path: result.privateWorktree, reason: result.failedStep + " failed",
			})
		}
	}

	// The scratch dir also holds the synthesis prompts, so cleanup waits.
	scratchKept := ""
	if anyFailed {
		scratchKept = scratchDir
	} else {
		os.RemoveAll(scratchDir)
	}

	fmt.Fprint(stdout, renderReport(cfg, rep, outcomes, builder, result, scratchKept, fold))

	// Step 10, both modes. Skipped when record or submit failed: the traces
	// must stay pending for the next run.
	if !result.submitFailed {
		for _, dir := range cfg.tracesDirs {
			outcomesByRel := map[string]traceOutcome{}
			analyzedHere := 0
			for _, oc := range outcomes {
				if oc.trace.checkoutDir == dir {
					outcomesByRel[oc.trace.relPath] = oc
					if oc.analyzed {
						analyzedHere++
					}
				}
			}
			changedState, newState := computeAnalyzedState(dir, tracesByCheckout[dir], outcomesByRel)
			if changedState && writeAnalyzedState(dir, newState) {
				commitAnalyzedState(dir, analyzedHere)
			}
		}
	}

	if result.submitFailed {
		exit(1)
	}
	if anyFailed {
		exit(3)
	}
}
