package main

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

	if !strings.Contains(out, "#101") {
		t.Errorf("unrelayed public edits should list the merged, unchecked PR #101\n%s", out)
	}
	if strings.Contains(out, "#102") {
		t.Errorf("#102 is closed but unmerged; it must not be listed as unrelayed\n%s", out)
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
	state := readAnalyzedState(fx.traces)
	if len(state) != 0 {
		t.Errorf("analyzed.txt should list none of the traces after every analysis failed, got: %v", state)
	}
}

// One trace fails every attempt while the other two analyze; the funnel and state file must tell them apart.
func TestMemoryBackpassPartialFailure(t *testing.T) {
	fx := backpassSetup(t)
	backpassSetupForge(t)
	binDir := t.TempDir()
	bulkSeat := backpassSeat(t, binDir, backpassResponsesDir(t))
	fallbackSeat := backpassWriteScript(t, binDir, "fallback-seat.sh", backpassFakeSeatScript) + " " + backpassResponsesDir(t)

	t.Setenv("FAKE_SEAT_GARBAGE_N", "3")
	t.Setenv("FAKE_SEAT_GARBAGE_BASENAME", "2026-09-01-session-a.md")
	t.Setenv("FAKE_SEAT_COUNTER_DIR", t.TempDir())

	out, errText, code := runAllod(t, "memory", "backpass", "--traces", fx.traces,
		"--public", fx.public, "--private", fx.private,
		"--bulk-seat", bulkSeat, "--fallback-seat", fallbackSeat, "--report-only")
	if code != 3 {
		t.Fatalf("exit code = %d, want 3, stderr = %q", code, errText)
	}
	if !strings.Contains(out, "traces analyzed: 2") {
		t.Errorf("the other two traces should still analyze\n%s", out)
	}
	if !strings.Contains(out, "devbox/claude/2026-09-01-session-a.md") {
		t.Errorf("the failed trace should be named under Failures\n%s", out)
	}

	state := readAnalyzedState(fx.traces)
	if _, ok := state["devbox/claude/2026-09-01-session-a.md"]; ok {
		t.Error("the failed trace must not be marked analyzed")
	}
	for _, path := range []string{"devbox/claude/2026-09-02-session-b.md", "devbox/pi/2026-09-03-session-c.md"} {
		if _, ok := state[path]; !ok {
			t.Errorf("%s should be marked analyzed", path)
		}
	}
}

func TestMemoryBackpassWriteMode(t *testing.T) {
	fx := backpassSetup(t)
	forgeLog := backpassSetupForge(t)
	binDir := t.TempDir()
	bulkSeat := backpassSeat(t, binDir, backpassResponsesDir(t))
	promptCopy := filepath.Join(t.TempDir(), "synthesis-prompt-received.md")
	t.Setenv("FAKE_PROMPT_COPY", promptCopy)
	synthesisSeat := backpassWriteScript(t, binDir, "fake-synthesis.sh", `#!/bin/sh
if [ -n "$FAKE_PROMPT_COPY" ]; then cp "$1" "$FAKE_PROMPT_COPY"; fi
echo "- Fixture synthesis edit." >> memory.md
cat <<'EOF'
{"edits":[{"title":"Fixture synthesis edit","paths":["memory.md"],"rationale":"evidence supports it","evidence":[{"source":"claude a","quote":"q1"}]}],"toRelay":[{"title":"Fixture relay edit","diff":"--- a/memory.md\n+++ b/memory.md\n@@ -7,2 +7,3 @@\n - Write a test before merging a change to the fixture tool.\n - Keep fixture bullets short so the index stays easy to read.\n+- Fixture relay edit landed.\n","rationale":"evidence supports relaying it","evidence":[{"source":"claude a","quote":"q1"}]}]}
EOF
`)

	publicOriginBefore, _ := gitOutput(fx.public, "ls-remote", "--heads", "origin")
	privateMainBefore, _ := gitOutput(fx.private, "rev-parse", "origin/main")

	out, errText, code := runAllod(t, "memory", "backpass", "--traces", fx.traces,
		"--public", fx.public, "--private", fx.private,
		"--bulk-seat", bulkSeat, "--synthesis-seat", synthesisSeat)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stderr = %q", code, errText)
	}
	if !strings.Contains(out, "PR: http://forge.example") {
		t.Errorf("report should carry the PR URL\n%s", out)
	}
	if !strings.Contains(out, "#101") {
		t.Errorf("write mode should also run the relay check\n%s", out)
	}

	promptReceived, err := os.ReadFile(promptCopy)
	if err != nil {
		t.Fatalf("synthesis seat never received a prompt file: %v", err)
	}
	if !strings.Contains(string(promptReceived), "memory-backpass: 2026-08-01 (rejected)") {
		t.Error("synthesis prompt should list the closed, unmerged PR under rejected titles")
	}
	if strings.Contains(string(promptReceived), "memory-backpass: 2026-09-01") {
		t.Error("synthesis prompt must not list the merged PR as a rejected title")
	}

	branch := backpassFindAgentBranch(t, fx.private)
	diff, _ := gitOutput(fx.private, "diff", "main", branch, "--", "ledger.md")
	if diff == "" {
		t.Error("the private branch should carry a ledger.md change")
	}
	// Proves the budget hook actually ran against this worktree (in the
	// sandbox, with the old #!/usr/bin/env bash shebang, it silently did
	// not): the kept edit only lands on the branch once the hook passes it.
	memoryDiff, _ := gitOutput(fx.private, "diff", "main", branch, "--", "memory.md")
	if !strings.Contains(memoryDiff, "Fixture synthesis edit") {
		t.Errorf("the kept edit should be on the branch's memory.md, diff = %q", memoryDiff)
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
	if !strings.Contains(log, "pr create") {
		t.Errorf("forge log missing a 'pr create' call\n%s", log)
	}
	if !strings.Contains(log, "issue edit") || !strings.Contains(log, "--add-label relay") {
		t.Errorf("forge log missing 'issue edit ... --add-label relay'\n%s", log)
	}

	// Only a push to a default branch is forbidden; submit pushes the agent branch.
	privateOriginAfter, _ := gitOutput(fx.private, "ls-remote", "--heads", "origin", branch)
	if privateOriginAfter == "" {
		t.Error("the private agent branch should have been pushed by 'change submit'")
	}
	privateMainAfter, _ := gitOutput(fx.private, "rev-parse", "origin/main")
	if privateMainBefore != privateMainAfter {
		t.Errorf("private origin's default branch moved: before %q, after %q", privateMainBefore, privateMainAfter)
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

	branch := backpassFindAgentBranch(t, fx.private)
	memoryDiff, _ := gitOutput(fx.private, "diff", "main", branch, "--", "memory.md")
	if memoryDiff != "" {
		t.Errorf("the dropped edit must not reach the branch's memory.md, diff = %q", memoryDiff)
	}
	worktree := backpassWorktreePathForBranch(t, fx.private, branch)
	if status, _ := gitOutput(worktree, "status", "--porcelain"); status != "" {
		t.Errorf("the worktree should be clean of the seat's files after the drop, status = %q", status)
	}
}

// TestMemoryBackpassBuiltinPiSeatDefault proves the default (no seat flags,
// no ALLOD_BACKPASS_* env) runs the built-in 'pi' seat through 'script -qec'
// and names it from pi's own settings.json.
func TestMemoryBackpassBuiltinPiSeatDefault(t *testing.T) {
	fx := backpassSetup(t)
	backpassSetupForge(t)
	binDir := t.TempDir()
	backpassWriteScript(t, binDir, "script", `#!/bin/sh
cmd="$2"
exec sh -c "$cmd"
`)
	backpassWriteScript(t, binDir, "pi", `#!/bin/sh
for last in "$@"; do :; done
promptfile=$(echo "$last" | sed 's/^@//')
tracepath=$(grep '^Path: ' "$promptfile" | head -1 | sed 's/^Path: //')
base=$(basename "$tracepath")
cat "$FAKE_PI_RESPONSES_DIR/$base.json"
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_PI_RESPONSES_DIR", backpassResponsesDir(t))

	settingsDir := filepath.Join(fx.root, "home", ".pi", "agent")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"),
		[]byte(`{"defaultProvider":"fixture","defaultModel":"model-x"}`), 0644); err != nil {
		t.Fatal(err)
	}

	out, errText, code := runAllod(t, "memory", "backpass", "--traces", fx.traces,
		"--public", fx.public, "--private", fx.private, "--report-only")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stderr = %q", code, errText)
	}
	if !strings.Contains(out, "pi (fixture/model-x)") {
		t.Errorf("seats section should name the built-in by its configured defaults\n%s", out)
	}
	if !strings.Contains(out, "traces analyzed: 3") {
		t.Errorf("the built-in seat should have analyzed every trace\n%s", out)
	}
}

func TestMemoryBackpassBuiltinSeatMissingToolsRefuses(t *testing.T) {
	fx := backpassSetup(t)
	gitPath := backpassLookPath(t, "git")
	dir := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(dir, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	_, errText, code := runAllod(t, "memory", "backpass", "--traces", fx.traces,
		"--public", fx.public, "--private", fx.private, "--report-only")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errText, "pi") || !strings.Contains(errText, "script") {
		t.Errorf("stderr = %q, want it to name both missing tools", errText)
	}
}
