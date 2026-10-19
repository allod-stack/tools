package main

// Steps 7 to 9 of skills/memory-backpass/SKILL.md: the private worktree and
// its synthesis call, the budget gate, the public relay worktree, and the
// pull request itself.

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

// callChangeBegin runs 'allod change begin -d <desc> <repoDir>' in process,
// swapping stdout for a buffer to capture the printed worktree path.
func callChangeBegin(desc, repoDir string) (path string, code int) {
	var buf bytes.Buffer
	prevOut := stdout
	stdout = &buf
	code = run([]string{"change", "begin", "-d", desc, repoDir})
	stdout = prevOut
	return strings.TrimSpace(buf.String()), code
}

// beginChangeWorktree is step 7's worktree creation: a branch-exists
// refusal (exit 5) is retried once with the time of day appended.
func beginChangeWorktree(desc, repoDir string) (path string, ok bool) {
	path, code := callChangeBegin(desc, repoDir)
	if code == 5 {
		path, code = callChangeBegin(desc+"-"+time.Now().UTC().Format("150405"), repoDir)
	}
	return path, code == 0
}

var budgetAssignRe = regexp.MustCompile(`(INDEX_MAX_BYTES|TOPIC_MAX_BYTES)=(\d+)`)

// readMemoryBudgetCaps reads .hooks/memory-budget's two constants; ok is
// false when either is missing, which the caller turns into a refusal.
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

func runMemoryBudgetWorktree(dir string) (ok bool, output string) {
	out, status := captureCommand(dir, nil, true, dir+"/.hooks/memory-budget", "--worktree")
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

// changedPaths reads 'git status --porcelain' into the set of paths with
// any change, following a rename to its destination path.
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

// inProcessRecordAndSubmit is step 9: 'change record' then 'change submit',
// both in process from the private worktree, stdout captured so the PR URL
// in submit's output can be read back and printed.
func inProcessRecordAndSubmit(worktreePath, title, bodyFile string) (submitOut string, ok bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	defer os.Chdir(cwd)
	if err := os.Chdir(worktreePath); err != nil {
		return "", false
	}
	var buf bytes.Buffer
	prevOut := stdout
	stdout = &buf
	code := run([]string{"change", "record", "-m", title})
	stdout = prevOut
	if code != 0 {
		return buf.String(), false
	}
	buf.Reset()
	stdout = &buf
	code = run([]string{"change", "submit", "-t", title, "-F", bodyFile})
	stdout = prevOut
	return buf.String(), code == 0
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

// writeResult carries every observation step 7 to 9 makes, for the run
// report: the brief requires these to be reported even when they are not
// fatal (a claimed-absent path, an unreported edit, a dropped edit).
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
	labelFailed                     bool
	labelOutput                     string
}

// composeFixPrompt is the step-8 fix call: a short self-contained
// instruction plus the hook's own stderr, so the synthesis seat can see
// exactly what it must correct.
func composeFixPrompt(hookOutput string) string {
	return backpassSelfMarker +
		"\nThe .hooks/memory-budget check failed in this worktree (your current " +
		"directory). Fix the failure described below by editing the files in " +
		"this worktree, then return {} and nothing else, with no Markdown fence.\n\n" +
		hookOutput
}

// writeBackpass runs steps 7 to 9 against an already-built private ledger
// (ledger.renderFile() is the new ledger.md). date is "YYYY-MM-DD". rep
// carries the funnel, failures, and unrelayed-edits data the PR body quotes
// alongside what this step itself observes.
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

	changed := changedPaths(privatePath)
	claimed := map[string]bool{}
	for _, e := range answer.Edits {
		ok := true
		for _, p := range e.Paths {
			if !changed[p] {
				result.claimedAbsent = append(result.claimedAbsent, fmt.Sprintf("%s (%s)", e.Title, p))
				ok = false
			} else {
				claimed[p] = true
			}
		}
		if ok {
			result.keptEdits = append(result.keptEdits, e)
		}
	}
	for p := range changed {
		if p == "ledger.md" || claimed[p] {
			continue
		}
		result.editedUnreported = append(result.editedUnreported, p)
	}

	budgetOK, budgetOut := runMemoryBudgetWorktree(privatePath)
	if !budgetOK {
		fixPrompt := composeFixPrompt(budgetOut)
		fixPath := scratchDir + "/synthesis-fix-" + date + ".prompt.md"
		if err := checkTemplateHeader("fix prompt", fixPrompt); err == nil {
			_ = os.WriteFile(fixPath, []byte(fixPrompt), 0600)
			runSeat(cfg.synthesisSeat, fixPath, privatePath, cfg.timeout)
		}
		budgetOK, _ = runMemoryBudgetWorktree(privatePath)
		if !budgetOK {
			result.budgetFailed = true
			result.allEditsDropped = true
			paths := []string{}
			for p := range claimed {
				if p != "ledger.md" {
					paths = append(paths, p)
				}
			}
			if len(paths) > 0 {
				runCommand(privatePath, nil, io.Discard, io.Discard, "git", append([]string{"checkout", "--"}, paths...)...)
			}
			result.keptEdits = nil
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
			if ok, _ := runMemoryBudgetWorktree(publicPath); !ok {
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
	prOut, ok := inProcessRecordAndSubmit(privatePath, title, bodyFile.Name())
	if !ok {
		result.submitFailed = true
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
