package main

// End-to-end coverage for 'allod memory backpass', against the fixtures in
// testdata/backpass and memory_backpass_fixture_test.go's helpers. Assertions
// are on observable effects — stdout sections, git state, the forge stub's
// log — never on package internals, which the unit tests already pin.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryBackpassRequiresTraces(t *testing.T) {
	stubTools(t, "git", "forge")
	_, errText, code := runAllod(t, "memory", "backpass")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errText, "requires --traces") {
		t.Errorf("stderr = %q, want it to mention --traces", errText)
	}
}

func TestMemoryBackpassRefusesMultiplePrivateMemoryEntries(t *testing.T) {
	fx := backpassSetup(t)
	backpassSetupForge(t)
	writeRegistryFixtureRaw(t, filepath.Join(fx.root, "work", "allod", "inventory"), map[string]string{
		"allod/memory": `{"checkout": "public", "memory": true}`,
		"private-a":    `{"checkout": "private", "memory": true}`,
		"private-b":    `{"checkout": "private", "memory": true}`,
	})

	_, errText, code := runAllod(t, "memory", "backpass", "--traces", fx.traces, "--public", fx.public,
		"--bulk-seat", "bogus")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errText, "private-a") || !strings.Contains(errText, "private-b") {
		t.Errorf("stderr = %q, want it to name both registry entries", errText)
	}
}

func TestMemoryBackpassRefusesBadPromptTemplates(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"missing-marker", "not the marker\n{{INSTRUCTION_INDEX}}{{OPEN_LEDGER_ENTRIES}}{{TRACE_PATH}}{{TRACE}}", "first line"},
		{"unknown-placeholder", backpassSelfMarker + "\n{{INSTRUCTION_INDEX}}{{OPEN_LEDGER_ENTRIES}}{{TRACE_PATH}}{{TRACE}}{{BOGUS}}", "BOGUS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := backpassSetup(t)
			backpassSetupForge(t)
			if err := os.WriteFile(filepath.Join(fx.public, "skills", "memory-backpass", "analysis-prompt.md"), []byte(c.content), 0644); err != nil {
				t.Fatal(err)
			}
			_, errText, code := runAllod(t, "memory", "backpass", "--traces", fx.traces,
				"--public", fx.public, "--private", fx.private, "--bulk-seat", "bogus", "--report-only")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(errText, c.want) {
				t.Errorf("stderr = %q, want it to contain %q", errText, c.want)
			}
		})
	}
}

func TestMemoryBackpassPromptPlaceholdersAllKnown(t *testing.T) {
	analysis, err := os.ReadFile("testdata/backpass/public/skills/memory-backpass/analysis-prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkTemplatePlaceholders("analysis-prompt.md", string(analysis), analysisKnownPlaceholders); err != nil {
		t.Errorf("analysis-prompt.md fixture has a placeholder the filler does not know: %v", err)
	}
	synthesis, err := os.ReadFile("testdata/backpass/public/skills/memory-backpass/synthesis-prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkTemplatePlaceholders("synthesis-prompt.md", string(synthesis), synthesisKnownPlaceholders); err != nil {
		t.Errorf("synthesis-prompt.md fixture has a placeholder the filler does not know: %v", err)
	}
}

func TestMemoryBackpassForgeMissingRefusesInWriteMode(t *testing.T) {
	fx := backpassSetup(t)
	backpassGitWithoutForge(t)
	_, errText, code := runAllod(t, "memory", "backpass", "--traces", fx.traces,
		"--public", fx.public, "--private", fx.private,
		"--bulk-seat", "bogus", "--synthesis-seat", "bogus")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errText, "forge") {
		t.Errorf("stderr = %q, want it to mention forge", errText)
	}
}

// backpassRunReportOnly runs the fixture set in report-only mode with the
// fake bulk seat answering from testdata/backpass/responses, and returns
// stdout, stderr, and the exit code.
func backpassRunReportOnly(t *testing.T, fx backpassFixtures, seat string) (string, string, int) {
	t.Helper()
	return runAllod(t, "memory", "backpass", "--traces", fx.traces,
		"--public", fx.public, "--private", fx.private,
		"--bulk-seat", seat, "--report-only")
}

func TestMemoryBackpassReportOnlyFunnelAndLedger(t *testing.T) {
	fx := backpassSetup(t)
	backpassSetupForge(t)
	binDir := t.TempDir()
	seat := backpassSeat(t, binDir, backpassResponsesDir(t))

	publicStatusBefore, _ := gitOutput(fx.public, "status", "--porcelain")
	privateStatusBefore, _ := gitOutput(fx.private, "status", "--porcelain")

	out, errText, code := backpassRunReportOnly(t, fx, seat)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stderr = %q", code, errText)
	}

	for _, want := range []string{
		"traces analyzed: 3",
		"items returned: 5",
		"dropped for a missing quote: 0",
		"malformed: 2",
		"ledger entries corroborated: 1",
		"ledger lines added: 3",
		"prunes: 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n--- report ---\n%s", want, out)
		}
	}

	if !strings.Contains(out, "Would add to private ledger") {
		t.Error("report-only should list would-be private ledger additions")
	}
	if !strings.Contains(out, "Preserve literal placeholder text such as {{TRACE}}") {
		t.Errorf("report should carry the gap whose quote spans '&' and '{{TRACE}}'\n%s", out)
	}
	if strings.Contains(out, "Unquotable gap.") {
		t.Error("an item with an empty quote must be dropped as malformed, not ledgered")
	}

	publicStatusAfter, _ := gitOutput(fx.public, "status", "--porcelain")
	privateStatusAfter, _ := gitOutput(fx.private, "status", "--porcelain")
	if publicStatusBefore != publicStatusAfter {
		t.Errorf("public checkout changed in report-only mode: %q -> %q", publicStatusBefore, publicStatusAfter)
	}
	if privateStatusBefore != privateStatusAfter {
		t.Errorf("private checkout changed in report-only mode: %q -> %q", privateStatusBefore, privateStatusAfter)
	}

	log, _ := gitOutput(fx.traces, "log", "--oneline", "-1")
	if !strings.Contains(log, "memory-backpass:") {
		t.Errorf("traces checkout should have a 'memory-backpass:' commit, log = %q", log)
	}
	if status, _ := gitOutput(fx.traces, "status", "--porcelain"); status != "" {
		t.Errorf("traces checkout should be clean after its analyzed.txt commit, status = %q", status)
	}
}

func TestMemoryBackpassHashMarkerIdempotency(t *testing.T) {
	fx := backpassSetup(t)
	backpassSetupForge(t)
	binDir := t.TempDir()
	seat := backpassSeat(t, binDir, backpassResponsesDir(t))

	if _, errText, code := backpassRunReportOnly(t, fx, seat); code != 0 {
		t.Fatalf("first run: exit code = %d, stderr = %q", code, errText)
	}

	out, errText, code := backpassRunReportOnly(t, fx, seat)
	if code != 0 {
		t.Fatalf("second run: exit code = %d, stderr = %q", code, errText)
	}
	if !strings.Contains(out, "traces analyzed: 0") {
		t.Errorf("second run should analyze zero traces\n%s", out)
	}

	growing := filepath.Join(fx.traces, "devbox", "claude", "2026-09-02-session-b.md")
	data, err := os.ReadFile(growing)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(growing, append(data, []byte("\nMore text appended after analysis.\n")...), 0644); err != nil {
		t.Fatal(err)
	}
	workspaceTestGit(t, fx.traces, "add", "-A")
	workspaceTestGit(t, fx.traces, "commit", "-q", "-m", "fixture: grow session-b")

	out, errText, code = backpassRunReportOnly(t, fx, seat)
	if code != 0 {
		t.Fatalf("third run: exit code = %d, stderr = %q", code, errText)
	}
	if !strings.Contains(out, "traces analyzed: 1") {
		t.Errorf("a grown trace should become pending again\n%s", out)
	}

	if err := os.Remove(filepath.Join(fx.traces, "devbox", "pi", "2026-09-03-session-c.md")); err != nil {
		t.Fatal(err)
	}
	workspaceTestGit(t, fx.traces, "add", "-A")
	workspaceTestGit(t, fx.traces, "commit", "-q", "-m", "fixture: delete session-c")
	if _, errText, code := backpassRunReportOnly(t, fx, seat); code != 0 {
		t.Fatalf("fourth run: exit code = %d, stderr = %q", code, errText)
	}
	state := readAnalyzedState(fx.traces)
	if _, ok := state["devbox/pi/2026-09-03-session-c.md"]; ok {
		t.Error("a deleted trace's analyzed.txt line should be dropped")
	}
}

func TestMemoryBackpassRetryThenFallback(t *testing.T) {
	fx := backpassSetup(t)
	backpassSetupForge(t)
	binDir := t.TempDir()
	bulkSeat := backpassSeat(t, binDir, backpassResponsesDir(t))
	fallbackSeat := backpassWriteScript(t, binDir, "fallback-seat.sh", backpassFakeSeatScript) + " " + backpassResponsesDir(t)

	t.Setenv("FAKE_SEAT_GARBAGE_N", "2")
	t.Setenv("FAKE_SEAT_COUNTER_DIR", t.TempDir())

	out, errText, code := runAllod(t, "memory", "backpass", "--traces", fx.traces,
		"--public", fx.public, "--private", fx.private,
		"--bulk-seat", bulkSeat, "--fallback-seat", fallbackSeat, "--report-only")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stderr = %q", code, errText)
	}
	if !strings.Contains(out, fallbackSeat) {
		t.Errorf("report should name the fallback seat that analyzed the first trace\n%s", out)
	}
}

func TestMemoryBackpassTimeout(t *testing.T) {
	fx := backpassSetup(t)
	backpassSetupForge(t)
	binDir := t.TempDir()
	seat := backpassSeat(t, binDir, backpassResponsesDir(t))
	t.Setenv("FAKE_SEAT_SLEEP", "30")

	start := time.Now()
	out, _, code := runAllod(t, "memory", "backpass", "--traces", fx.traces,
		"--public", fx.public, "--private", fx.private,
		"--bulk-seat", seat, "--timeout", "1", "--report-only")
	elapsed := time.Since(start)

	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if elapsed > 10*time.Second {
		t.Errorf("a 1-second timeout took %s to come back, want well under 30s", elapsed)
	}
	if !strings.Contains(out, "traces analyzed: 0") {
		t.Errorf("every trace should have failed to analyze\n%s", out)
	}
}

func TestMemoryBackpassWriteMode(t *testing.T) {
	fx := backpassSetup(t)
	forgeLog := backpassSetupForge(t)
	binDir := t.TempDir()
	bulkSeat := backpassSeat(t, binDir, backpassResponsesDir(t))
	synthesisSeat := backpassWriteScript(t, binDir, "fake-synthesis.sh", `#!/bin/sh
echo "- Fixture synthesis edit." >> memory.md
cat <<'EOF'
{"edits":[{"title":"Fixture synthesis edit","paths":["memory.md"],"rationale":"evidence supports it","evidence":[{"source":"claude a","quote":"q1"}]}],"toRelay":[{"title":"Fixture relay edit","diff":"--- a/memory.md\n+++ b/memory.md\n@@ -7,2 +7,3 @@\n - Write a test before merging a change to the fixture tool.\n - Keep fixture bullets short so the index stays easy to read.\n+- Fixture relay edit landed.\n","rationale":"evidence supports relaying it","evidence":[{"source":"claude a","quote":"q1"}]}]}
EOF
`)

	publicOriginBefore, _ := gitOutput(fx.public, "ls-remote", "--heads", "origin")

	out, errText, code := runAllod(t, "memory", "backpass", "--traces", fx.traces,
		"--public", fx.public, "--private", fx.private,
		"--bulk-seat", bulkSeat, "--synthesis-seat", synthesisSeat)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stderr = %q", code, errText)
	}
	if !strings.Contains(out, "PR: http://forge.example") {
		t.Errorf("report should carry the PR URL\n%s", out)
	}

	today := time.Now().UTC().Format("2006-01-02")
	branch := "agent/memory-backpass-" + today
	if !localRefExists(fx.private, branch) {
		t.Errorf("branch %s should exist in the private fixture repo", branch)
	}
	diff, _ := gitOutput(fx.private, "diff", "main", branch, "--", "ledger.md")
	if diff == "" {
		t.Error("the private branch should carry a ledger.md change")
	}
	if !localRefExists(fx.public, branch) {
		t.Errorf("branch %s should exist in the public fixture repo (the relay worktree)", branch)
	}
	publicCommits, _ := gitOutput(fx.public, "log", "--oneline", "main.."+branch)
	publicCommitLines := strings.Split(strings.TrimSpace(publicCommits), "\n")
	if len(publicCommitLines) != 1 || publicCommitLines[0] == "" {
		t.Errorf("public relay branch should carry exactly one commit, got: %q", publicCommits)
	}

	logData, err := os.ReadFile(forgeLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logData)
	if !strings.Contains(log, "pr create") || !strings.Contains(log, "memory-backpass: "+today) {
		t.Errorf("forge log missing a 'pr create' with the run's title\n%s", log)
	}
	if !strings.Contains(log, "issue edit") || !strings.Contains(log, "--add-label relay") {
		t.Errorf("forge log missing 'issue edit ... --add-label relay'\n%s", log)
	}

	// 'change submit' pushes the private agent branch itself (how else would
	// the PR exist to review) — only a push to a default branch is forbidden,
	// and only the public relay worktree's branch must stay unpushed.
	privateOriginAfter, _ := gitOutput(fx.private, "ls-remote", "--heads", "origin", branch)
	if privateOriginAfter == "" {
		t.Error("the private agent branch should have been pushed by 'change submit'")
	}
	publicOriginAfter, _ := gitOutput(fx.public, "ls-remote", "--heads", "origin")
	if publicOriginBefore != publicOriginAfter {
		t.Errorf("public origin's branch list changed: nothing should be pushed\nbefore: %q\nafter: %q", publicOriginBefore, publicOriginAfter)
	}
}

func TestMemoryBackpassBudgetFailureDropsEdit(t *testing.T) {
	fx := backpassSetup(t)
	backpassSetupForge(t)
	binDir := t.TempDir()
	bulkSeat := backpassSeat(t, binDir, backpassResponsesDir(t))
	synthesisSeat := backpassWriteScript(t, binDir, "fake-synthesis-budget.sh", `#!/bin/sh
head -c 20000 /dev/zero | tr '\0' 'x' >> memory.md
cat <<'EOF'
{"edits":[{"title":"Oversized edit","paths":["memory.md"],"rationale":"too big","evidence":[{"source":"claude a","quote":"q1"}]}],"toRelay":[]}
EOF
`)

	out, errText, code := runAllod(t, "memory", "backpass", "--traces", fx.traces,
		"--public", fx.public, "--private", fx.private,
		"--bulk-seat", bulkSeat, "--synthesis-seat", synthesisSeat)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stderr = %q", code, errText)
	}
	if !strings.Contains(out, "every edit dropped") {
		t.Errorf("report should say every edit was dropped after the budget stayed over cap\n%s", out)
	}
}
