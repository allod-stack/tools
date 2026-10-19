package main

import (
	"strings"
	"testing"
	"time"
)

func TestFillTemplateNeverRescansEmittedValues(t *testing.T) {
	template := "A {{X}} B {{Y}} C"
	out, err := fillTemplate(template, map[string]string{
		"X": "contains {{Y}} literally",
		"Y": "and an & here too",
	})
	if err != nil {
		t.Fatalf("fillTemplate: %v", err)
	}
	want := "A contains {{Y}} literally B and an & here too C"
	if out != want {
		t.Errorf("fillTemplate = %q, want %q", out, want)
	}
}

func TestFillTemplateUnknownPlaceholderRefuses(t *testing.T) {
	_, err := fillTemplate("{{KNOWN}} {{MYSTERY}}", map[string]string{"KNOWN": "x"})
	if err == nil {
		t.Fatal("fillTemplate with an unsupplied placeholder did not error")
	}
	if !strings.Contains(err.Error(), "MYSTERY") {
		t.Errorf("error = %v, want it to name MYSTERY", err)
	}
}

func TestCheckTemplatePlaceholdersRefusesUnknownName(t *testing.T) {
	known := map[string]bool{"TRACE": true}
	if err := checkTemplatePlaceholders("t", "{{TRACE}}", known); err != nil {
		t.Errorf("known placeholder refused: %v", err)
	}
	err := checkTemplatePlaceholders("t", "{{TRACE}} {{BOGUS}}", known)
	if err == nil || !strings.Contains(err.Error(), "BOGUS") {
		t.Errorf("checkTemplatePlaceholders = %v, want an error naming BOGUS", err)
	}
}

func TestCheckTemplateHeaderRequiresExactFirstLine(t *testing.T) {
	if err := checkTemplateHeader("t", backpassSelfMarker+"\nrest"); err != nil {
		t.Errorf("valid header refused: %v", err)
	}
	if err := checkTemplateHeader("t", "not the marker\nrest"); err == nil {
		t.Error("missing self marker was not refused")
	}
	if err := checkTemplateHeader("t", backpassSelfMarker+" trailing\nrest"); err == nil {
		t.Error("marker with trailing text was not refused")
	}
}

func TestFoldWhitespaceCollapsesRuns(t *testing.T) {
	if got := foldWhitespace("  a\n\tb   c  "); got != "a b c" {
		t.Errorf("foldWhitespace = %q, want %q", got, "a b c")
	}
}

func TestQuoteSurvivesWhitespaceButNotParaphrase(t *testing.T) {
	trace := "the quick  brown\nfox jumps"
	if !quoteSurvives("the quick brown fox", trace) {
		t.Error("a quote differing only in whitespace should survive")
	}
	if quoteSurvives("the slow brown fox", trace) {
		t.Error("a paraphrase should not survive")
	}
	if quoteSurvives("   ", trace) {
		t.Error("an empty-after-fold quote should not survive")
	}
}

func TestGateItemsDropsMalformed(t *testing.T) {
	validIDs := map[string]bool{"PUB-001": true}
	answer := analysisAnswer{
		Positive: []analysisItem{
			{ID: "PUB-001", Quote: "ok"},
			{ID: "PUB-999", Quote: "unknown id"},
			{ID: "PUB-001", Quote: ""},
		},
		Negative: []analysisItem{
			{ID: "PUB-001", Class: "harm", Quote: "ok"},
			{ID: "PUB-001", Class: "bogus", Quote: "bad class"},
		},
		Gaps: []analysisItem{
			{ProposedInstruction: "do x", Quote: "ok"},
			{ProposedInstruction: "do y", Quote: ""},
		},
	}
	items, malformed := gateItems(answer, validIDs)
	if malformed != 4 {
		t.Errorf("malformed = %d, want 4", malformed)
	}
	if len(items) != 3 {
		t.Errorf("surviving items = %d, want 3", len(items))
	}
}

func TestLedgerBuilderGapDedupeWithinSessionAndCorroborationAcrossSessions(t *testing.T) {
	b := newLedgerBuilder(t.TempDir(), nil, nil)
	sessionA := []evidenceItem{
		{kind: "gap", proposedInstruction: "Always write a test.", quote: "q1", harness: "claude", sessionID: "a"},
		{kind: "gap", proposedInstruction: "Always write a test.", quote: "q1b", harness: "claude", sessionID: "a"},
	}
	outcomeA := traceOutcome{analyzed: true, items: sessionA}
	outcomeB := traceOutcome{analyzed: true, items: []evidenceItem{
		{kind: "gap", proposedInstruction: "Always  write a test.", quote: "q2", harness: "pi", sessionID: "b"},
	}}
	corroborations := buildLedger(b, []traceOutcome{outcomeA, outcomeB}, map[string]string{})
	added := b.addedLines()
	if len(added) != 1 {
		t.Fatalf("addedLines = %v, want exactly one line", added)
	}
	if corroborations != 1 {
		t.Errorf("corroborations = %d, want 1", corroborations)
	}
	if !strings.Contains(added[0], "claude a") || !strings.Contains(added[0], "pi b") {
		t.Errorf("line %q should cite both sessions", added[0])
	}
	if strings.Count(added[0], "q1b") != 0 {
		t.Errorf("line %q should not carry the in-session duplicate's quote", added[0])
	}

	// A third item from the already-cited session adds nothing further.
	outcomeA2 := traceOutcome{analyzed: true, items: []evidenceItem{
		{kind: "gap", proposedInstruction: "Always write a test.", quote: "q3", harness: "pi", sessionID: "b"},
	}}
	buildLedger(b, []traceOutcome{outcomeA2}, map[string]string{})
	if got := b.addedLines()[0]; strings.Count(got, "pi b") != 1 {
		t.Errorf("re-citing session pi b duplicated: %q", got)
	}
}

func TestLedgerBuilderNegativePublicCandidateSuffix(t *testing.T) {
	b := newLedgerBuilder(t.TempDir(), nil, nil)
	bullets := map[string]string{"PUB-001": "Always write a test before merging a change to the fixture tool."}
	outcome := traceOutcome{analyzed: true, items: []evidenceItem{
		{kind: "negative", id: "PUB-001", class: "non-compliance", effect: "skipped it", quote: "q", harness: "claude", sessionID: "a"},
	}}
	buildLedger(b, []traceOutcome{outcome}, bullets)
	added := b.addedLines()
	if len(added) != 1 {
		t.Fatalf("addedLines = %v, want one line", added)
	}
	if !strings.HasPrefix(added[0], "- ignored: ") {
		t.Errorf("line %q should start with '- ignored: '", added[0])
	}
	if !strings.HasSuffix(added[0], "Public memory.md candidate") {
		t.Errorf("line %q should end with the public-candidate suffix", added[0])
	}
}

func TestPruneByAgeRequiresEveryCitedTraceOldAndFound(t *testing.T) {
	today := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	dates := map[string]string{"claude old-session": "2026-01-01"}
	oldLine := `gap: x | claude old-session | "q"`
	if !pruneByAge(oldLine, dates, today) {
		t.Error("a line citing only an old, found session should prune")
	}
	recentLine := `gap: x | claude recent-session | "q"`
	if pruneByAge(recentLine, dates, today) {
		t.Error("a line citing a session with no trace found should never prune")
	}
	mixedLine := `gap: x | claude old-session | "q" | claude recent-session | "q2"`
	if pruneByAge(mixedLine, dates, today) {
		t.Error("a line with any not-old-enough citation should not prune")
	}
}

func TestPruneByCoverageMatchesFoldedSubstring(t *testing.T) {
	dir := t.TempDir()
	backpassWriteFile(t, dir+"/memory.md", "# Memory\n\n- the rule text is here\n")
	if !pruneByCoverage("the   rule\ntext is here", dir) {
		t.Error("coverage check should fold whitespace before comparing")
	}
	if pruneByCoverage("a rule nowhere present", dir) {
		t.Error("coverage check should not match unrelated text")
	}
}

func TestParsePRListRowDefensiveParsing(t *testing.T) {
	cases := []struct {
		line       string
		wantNumber int
		wantMerged bool
		wantOK     bool
	}{
		{"101  memory-backpass: 2026-09-01  claude  merged 2026-09-05", 101, true, true},
		{"102  memory-backpass: 2026-08-01  claude  closed", 102, false, true},
		{"not a row at all", 0, false, false},
	}
	for _, c := range cases {
		number, merged, ok := parsePRListRow(c.line)
		if number != c.wantNumber || merged != c.wantMerged || ok != c.wantOK {
			t.Errorf("parsePRListRow(%q) = (%d, %v, %v), want (%d, %v, %v)",
				c.line, number, merged, ok, c.wantNumber, c.wantMerged, c.wantOK)
		}
	}
}

func TestToRelaySectionAndUncheckedDetection(t *testing.T) {
	body := "Paragraph.\n\n## To relay\n\n- [ ] do the thing\n\n## Funnel\n\nstuff\n"
	section := toRelaySection(body)
	if !strings.Contains(section, "do the thing") {
		t.Errorf("toRelaySection = %q, want it to contain the relay line", section)
	}
	if strings.Contains(section, "Funnel") {
		t.Errorf("toRelaySection = %q, should stop before the next heading", section)
	}
	if !uncheckedRelayRe.MatchString(section) {
		t.Error("an unchecked '- [ ]' line should be detected")
	}
	checked := "## To relay\n\n- [x] done already\n"
	if uncheckedRelayRe.MatchString(toRelaySection(checked)) {
		t.Error("a checked '- [x]' line should not be detected as unchecked")
	}
}

func TestComputeAnalyzedStateMergeRules(t *testing.T) {
	checkoutDir := t.TempDir()
	backpassWriteFile(t, checkoutDir+"/.memory-backpass/analyzed.txt",
		"aaaaaaaa  kept/unanalyzed.md\nbbbbbbbb  deleted/gone.md\n")
	all := []pendingTrace{
		{relPath: "kept/unanalyzed.md", hash: "aaaaaaaa"},
		{relPath: "new/analyzed.md", hash: "cccccccc"},
	}
	outcomesByRel := map[string]traceOutcome{
		"new/analyzed.md": {analyzed: true, trace: all[1]},
	}
	changed, newState := computeAnalyzedState(checkoutDir, all, outcomesByRel)
	if !changed {
		t.Fatal("state should have changed")
	}
	if newState["kept/unanalyzed.md"] != "aaaaaaaa" {
		t.Errorf("kept/unanalyzed.md = %q, want it preserved", newState["kept/unanalyzed.md"])
	}
	if newState["new/analyzed.md"] != "cccccccc" {
		t.Errorf("new/analyzed.md = %q, want the newly analyzed hash", newState["new/analyzed.md"])
	}
	if _, ok := newState["deleted/gone.md"]; ok {
		t.Error("deleted/gone.md should be dropped: its file no longer exists")
	}
}
