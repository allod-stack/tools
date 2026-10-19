package main

// Steps 7 to 9 of skills/memory-backpass/SKILL.md.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type evidenceRef struct {
	Source string `json:"source"`
	Quote  string `json:"quote"`
}

type editProposal struct {
	Title     string        `json:"title"`
	Paths     []string      `json:"paths"`
	Rationale string        `json:"rationale"`
	Evidence  []evidenceRef `json:"evidence"`
}

type relayProposal struct {
	Title     string        `json:"title"`
	Diff      string        `json:"diff"`
	Rationale string        `json:"rationale"`
	Evidence  []evidenceRef `json:"evidence"`
}

type synthesisAnswer struct {
	Edits   []editProposal  `json:"edits"`
	ToRelay []relayProposal `json:"toRelay"`
}

func parseSynthesisAnswer(raw string) (synthesisAnswer, error) {
	text := stripFence(strings.TrimSpace(raw))
	var generic map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &generic); err != nil {
		return synthesisAnswer{}, err
	}
	for _, key := range []string{"edits", "toRelay"} {
		v, ok := generic[key]
		if !ok {
			return synthesisAnswer{}, fmt.Errorf("missing %q array", key)
		}
		if !strings.HasPrefix(strings.TrimSpace(string(v)), "[") {
			return synthesisAnswer{}, fmt.Errorf("%q is not an array", key)
		}
	}
	var answer synthesisAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return synthesisAnswer{}, err
	}
	return answer, nil
}

func callChangeBegin(desc, repoDir string) (path string, code int) {
	var buf bytes.Buffer
	prevOut := stdout
	stdout = &buf
	code = run([]string{"change", "begin", "-d", desc, repoDir})
	stdout = prevOut
	return strings.TrimSpace(buf.String()), code
}

// A branch-exists refusal (exit 5) is retried once with the time of day appended.
func beginChangeWorktree(desc, repoDir string) (path string, ok bool) {
	path, code := callChangeBegin(desc, repoDir)
	if code == 5 {
		path, code = callChangeBegin(desc+"-"+time.Now().UTC().Format("150405"), repoDir)
	}
	return path, code == 0
}

var budgetAssignRe = regexp.MustCompile(`(INDEX_MAX_BYTES|TOPIC_MAX_BYTES)=(\d+)`)

func readMemoryBudgetCaps(dir string) (indexMax, topicMax int, ok bool) {
	text, err := readFileOrEmpty(dir + "/.hooks/memory-budget")
	if err != nil {
		return 0, 0, false
	}
	found := map[string]int{}
	for _, m := range budgetAssignRe.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[2])
		found[m[1]] = n
	}
	indexMax, hasIndex := found["INDEX_MAX_BYTES"]
	topicMax, hasTopic := found["TOPIC_MAX_BYTES"]
	return indexMax, topicMax, hasIndex && hasTopic
}

// The hook runs from mainCheckout, never the worktree copy the seat can
// rewrite, through bash because its shebang has no env in the nix sandbox.
func runMemoryBudgetWorktree(mainCheckout, worktree string) (ok bool, output string) {
	out, status := captureCommand(worktree, nil, true, "bash", mainCheckout+"/.hooks/memory-budget", "--worktree")
	return status == 0, out
}

func renderIndexesWithBudget(publicBullets, privateBullets []memBullet, publicDir, privateDir string, indexMax int) string {
	var b strings.Builder
	b.WriteString("Public:\n")
	b.WriteString(renderIndex(publicBullets))
	fmt.Fprintf(&b, "\nmemory.md: %d bytes of %d\n\n", fileSize(publicDir+"/memory.md"), indexMax)
	b.WriteString("Private:\n")
	b.WriteString(renderIndex(privateBullets))
	fmt.Fprintf(&b, "\nmemory.md: %d bytes of %d\n", fileSize(privateDir+"/memory.md"), indexMax)
	return b.String()
}

func renderTopicFileIndex(publicDir, privateDir string) string {
	lines := append(topicFileIndex(publicDir, "public"), topicFileIndex(privateDir, "private")...)
	return strings.Join(lines, "\n")
}

func renderRejectedTitles(prs []backpassPR) string {
	titles := rejectedEditTitles(prs)
	if len(titles) == 0 {
		return "none"
	}
	return strings.Join(titles, "\n")
}

func changedPaths(dir string) map[string]bool {
	out, _ := gitOutput(dir, "status", "--porcelain")
	changed := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if arrow := strings.Index(path, " -> "); arrow >= 0 {
			path = path[arrow+4:]
		}
		changed[path] = true
	}
	return changed
}

func tryApplyDiff(worktree, diff string) bool {
	return runCommand(worktree, strings.NewReader(diff), io.Discard, io.Discard, "git", "apply") == 0
}

func commitAppliedEdit(worktree, message string) bool {
	if runCommand(worktree, nil, io.Discard, io.Discard, "git", "add", "-A") != 0 {
		return false
	}
	file, err := os.CreateTemp("", "allod-relay-msg-")
	if err != nil {
		return false
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(message); err != nil {
		file.Close()
		return false
	}
	file.Close()
	return runCommand(worktree, nil, io.Discard, io.Discard, "git", "commit", "-F", file.Name()) == 0
}

func evidenceLines(evidence []evidenceRef) string {
	lines := make([]string, 0, len(evidence))
	for _, e := range evidence {
		lines = append(lines, fmt.Sprintf("%s: %s", e.Source, quoteWrap(e.Quote)))
	}
	return strings.Join(lines, "\n")
}

func runChangeInWorktree(worktree string, args []string) (output string, code int) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", 1
	}
	defer os.Chdir(cwd)
	if err := os.Chdir(worktree); err != nil {
		return "", 1
	}
	var buf bytes.Buffer
	prevOut := stdout
	stdout = &buf
	code = run(append([]string{"change"}, args...))
	stdout = prevOut
	return buf.String(), code
}

// Committed before the synthesis seat runs, so a budget-failure drop can
// revert everything the seat did without touching the ledger lines.
func commitLedgerChanges(worktree string) {
	runChangeInWorktree(worktree, []string{"record", "-m", "memory-backpass: ledger"})
}

// When every edit was dropped the ledger pre-commit is the whole branch, so
// record's nothing-to-commit refusal is not a failure.
const changeRecordNothingToCommit = 4

func inProcessRecordAndSubmit(worktreePath, title, bodyFile string) (submitOut, failedStep string, ok bool) {
	if _, code := runChangeInWorktree(worktreePath, []string{"record", "-m", title}); code != 0 && code != changeRecordNothingToCommit {
		return "", "change record", false
	}
	out, code := runChangeInWorktree(worktreePath, []string{"submit", "-t", title, "-F", bodyFile})
	if code != 0 {
		return out, "change submit", false
	}
	return out, "", true
}

var prNumberRe = regexp.MustCompile(`/(\d+)\s*$`)

func extractPRNumber(prURL string) string {
	m := prNumberRe.FindStringSubmatch(strings.TrimSpace(prURL))
	if m == nil {
		return ""
	}
	return m[1]
}

func addRelayLabel(worktreePath, prURL string) (ok bool, output string) {
	number := extractPRNumber(prURL)
	if number == "" {
		return false, "could not parse a PR number from: " + prURL
	}
	out, status := captureCommand(worktreePath, nil, true, "forge", "issue", "edit", number, "--add-label", "relay")
	return status == 0, out
}

type writeResult struct {
	privateWorktree, publicWorktree string
	keptEdits                       []editProposal
	claimedAbsent, editedUnreported []string
	synthesisUnparseable            bool
	budgetFailed                    bool
	allEditsDropped                 bool
	appliedRelay, failedRelay       []string
	publicBudgetFailed              bool
	prURL                           string
	submitFailed                    bool
	failedStep                      string
	labelFailed                     bool
	labelOutput                     string
}

func composeFixPrompt(hookOutput string) string {
	return backpassSelfMarker +
		"\nThe .hooks/memory-budget check failed in this worktree (your current " +
		"directory). Fix the failure described below by editing the files in " +
		"this worktree, then return {} and nothing else, with no Markdown fence.\n\n" +
		hookOutput
}

func writeBackpass(cfg backpassConfig, date string, ledger *ledgerBuilder, publicBullets, privateBullets []memBullet, prs []backpassPR, fold string, scratchDir string, rep reportData) writeResult {
	var result writeResult
	desc := "memory-backpass-" + date

	privatePath, ok := beginChangeWorktree(desc, cfg.privateDir)
	if !ok {
		die(1, "could not create the private memory-backpass worktree")
	}
	result.privateWorktree = privatePath

	if err := os.WriteFile(privatePath+"/ledger.md", []byte(ledger.renderFile()), 0644); err != nil {
		die(1, "could not write ledger.md in %s: %v", privatePath, err)
	}
	commitLedgerChanges(privatePath)

	indexMax, _, ok := readMemoryBudgetCaps(privatePath)
	if !ok {
		die(1, "%s/.hooks/memory-budget does not define INDEX_MAX_BYTES and TOPIC_MAX_BYTES", privatePath)
	}

	synthesisTemplate := readFileOrDie(cfg.publicDir + "/skills/memory-backpass/synthesis-prompt.md")
	values := map[string]string{
		"PRIVATE_WORKTREE":                     privatePath,
		"INSTRUCTION_INDEXES_WITH_BYTE_COUNTS": renderIndexesWithBudget(publicBullets, privateBullets, cfg.publicDir, cfg.privateDir, indexMax),
		"TOPIC_FILE_INDEX":                     renderTopicFileIndex(cfg.publicDir, cfg.privateDir),
		"FOLD":                                 fold,
		"REJECTED_EDIT_TITLES":                 renderRejectedTitles(prs),
	}
	runSynthesis := func() (synthesisAnswer, error) {
		filled, err := fillTemplate(synthesisTemplate, values)
		if err != nil {
			return synthesisAnswer{}, err
		}
		promptPath := scratchDir + "/synthesis-" + date + ".prompt.md"
		if err := os.WriteFile(promptPath, []byte(filled), 0600); err != nil {
			return synthesisAnswer{}, err
		}
		out := runSeat(cfg.synthesisSeat, promptPath, privatePath, cfg.timeout)
		if out.timedOut {
			return synthesisAnswer{}, fmt.Errorf("synthesis seat timed out")
		}
		return parseSynthesisAnswer(out.answer)
	}

	answer, err := runSynthesis()
	if err != nil {
		answer, err = runSynthesis()
	}
	if err != nil {
		result.synthesisUnparseable = true
		answer = synthesisAnswer{}
	}

	// Claimed only once the whole edit is kept, so a half-valid edit cannot
	// hide a changed path from the unreported sweep.
	changed := changedPaths(privatePath)
	claimed := map[string]bool{}
	for _, e := range answer.Edits {
		ok := true
		for _, p := range e.Paths {
			if !changed[p] {
				result.claimedAbsent = append(result.claimedAbsent, fmt.Sprintf("%s (%s)", e.Title, p))
				ok = false
			}
		}
		if ok {
			result.keptEdits = append(result.keptEdits, e)
			for _, p := range e.Paths {
				claimed[p] = true
			}
		}
	}
	for p := range changed {
		if p == "ledger.md" || claimed[p] {
			continue
		}
		result.editedUnreported = append(result.editedUnreported, p)
	}

	budgetOK, budgetOut := runMemoryBudgetWorktree(cfg.privateDir, privatePath)
	if !budgetOK {
		fixPrompt := composeFixPrompt(budgetOut)
		fixPath := scratchDir + "/synthesis-fix-" + date + ".prompt.md"
		if err := checkTemplateHeader("fix prompt", fixPrompt); err == nil {
			_ = os.WriteFile(fixPath, []byte(fixPrompt), 0600)
			runSeat(cfg.synthesisSeat, fixPath, privatePath, cfg.timeout)
		}
		budgetOK, _ = runMemoryBudgetWorktree(cfg.privateDir, privatePath)
		if !budgetOK {
			// The ledger commit is in HEAD; this reverts exactly what the seat did.
			result.budgetFailed = true
			result.allEditsDropped = true
			runCommand(privatePath, nil, io.Discard, io.Discard, "git", "checkout", "--", ".")
			runCommand(privatePath, nil, io.Discard, io.Discard, "git", "clean", "-fd")
			result.keptEdits = nil
			result.claimedAbsent = nil
			result.editedUnreported = nil
		}
	}

	if len(answer.ToRelay) > 0 {
		publicPath, ok := beginChangeWorktree(desc, cfg.publicDir)
		if ok {
			result.publicWorktree = publicPath
			for _, p := range answer.ToRelay {
				message := p.Title + "\n\n" + p.Rationale
				if ev := evidenceLines(p.Evidence); ev != "" {
					message += "\n\n" + ev
				}
				if tryApplyDiff(publicPath, p.Diff) && commitAppliedEdit(publicPath, message) {
					result.appliedRelay = append(result.appliedRelay, p.Title)
				} else {
					result.failedRelay = append(result.failedRelay, p.Title)
				}
			}
			if ok, _ := runMemoryBudgetWorktree(cfg.publicDir, publicPath); !ok {
				result.publicBudgetFailed = true
			}
		}
	}

	body := renderPRBody(date, cfg, rep, result, fold)
	bodyFile, err := os.CreateTemp("", "allod-backpass-body-")
	if err != nil {
		die(1, "could not create PR body temp file: %v", err)
	}
	defer os.Remove(bodyFile.Name())
	bodyFile.WriteString(body)
	bodyFile.Close()

	title := "memory-backpass: " + date
	prOut, failedStep, ok := inProcessRecordAndSubmit(privatePath, title, bodyFile.Name())
	if !ok {
		result.submitFailed = true
		result.failedStep = failedStep
		return result
	}
	result.prURL = strings.TrimSpace(prOut)
	if len(answer.ToRelay) > 0 {
		labelOK, labelOut := addRelayLabel(privatePath, result.prURL)
		result.labelFailed = !labelOK
		result.labelOutput = labelOut
	}
	return result
}
