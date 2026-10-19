package main

// The spec is skills/memory-backpass/SKILL.md in allod/memory; the step
// numbers in names and comments here are its.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func init() {
	registerNamespace(namespace{
		name:    "memory",
		summary: "Run the memory backward pass over session traces (backpass)",
		main:    memoryMain,
	})
}

const memoryUsageText = `Usage:
  allod memory backpass [--traces <dir>...] [--public <dir>] [--private <dir>]
      [--bulk-seat <cmd>] [--fallback-seat <cmd>] [--synthesis-seat <cmd>]
      [--timeout <seconds>] [--jobs <n>] [--report-only]

All three directories resolve in the same order: the option, then an
ALLOD_BACKPASS_* environment variable, then a registry marker or
convention path, so a bare invocation works on any machine whose checkouts
follow the registry convention. The run report's "Inputs" section names
what each one resolved to and which of those four found it.

'--traces <dir>' is space-separated and repeatable. Order: --traces; else
ALLOD_BACKPASS_TRACES, a colon-separated path list; else every registry
entry marked "traces": true whose checkout exists, in sorted id order;
else <workDir>/traces-<hostname> if it exists; else <workDir>/traces if it
exists. Each is a git checkout of distilled traces laid out
<machine>/<harness>/<date>-<id>.md. Nothing found is a refusal listing
every path and marker tried.

'--public <dir>'. Order: --public; else ALLOD_BACKPASS_PUBLIC; else
<workDir>/<registry checkout of allod/memory> (falling back to the literal
checkout name).

'--private <dir>'. Order: --private; else ALLOD_BACKPASS_PRIVATE; else the
one registry entry marked "memory": true other than allod/memory (zero or
more than one is a refusal naming what was found); else <workDir>/agent-
memory if it exists. Nothing found is a refusal listing what was tried.

'--bulk-seat <cmd>' (env ALLOD_BACKPASS_BULK_SEAT) and '--synthesis-seat
<cmd>' (env ALLOD_BACKPASS_SYNTHESIS_SEAT) default, when neither is given,
to the built-in 'pi' seat: pi with its own configured default provider and
model, run under 'script' for a terminal. '--fallback-seat <cmd>' (env
ALLOD_BACKPASS_FALLBACK_SEAT) has no default; pass 'pi' to use the
built-in there too. An option always wins over its environment variable.
The built-in runs the bulk and fallback calls with 'pi -p --no-tools', so
analysis cannot wander into reading memory files instead of answering the
prompt; the synthesis call keeps tools on, since it edits the worktree.

'--timeout <seconds>' bounds every seat call (default 600).
'--jobs <n>' is analysis calls in flight at once (default 1).
'--report-only' runs steps 1 to 6 and 10 only and writes nothing to either
memory repository; it still commits analyzed.txt in each traces checkout.

See docs/allod-memory.md for the full contract: the seat contract, the
state file, what each mode writes, exit codes, and the report shape.
`

func memoryMain(args []string) {
	if len(args) == 0 {
		fmt.Fprint(stderr, memoryUsageText)
		exit(1)
	}
	command, args := args[0], args[1:]
	switch command {
	case "backpass":
		memoryBackpass(args)
	case "-h", "--help":
		fmt.Fprint(stdout, memoryUsageText)
	default:
		die(1, "unknown memory command: %s", command)
	}
}

const memoryBackpassUsageText = memoryUsageText

// Contract: every directory is an existing git repository and every seat the
// chosen mode needs is non-empty before runBackpass sees this.
type backpassConfig struct {
	tracesDirs    []string
	publicDir     string
	privateDir    string
	bulkSeat      string
	fallbackSeat  string
	synthesisSeat string
	timeout       time.Duration
	jobs          int
	reportOnly    bool
	inputs        []resolvedInput // for the report's "## Inputs" section
}

func memoryBackpass(args []string) {
	var tracesDirs []string
	publicDir, publicSet := "", false
	privateDir, privateSet := "", false
	bulkSeat, bulkSet := "", false
	fallbackSeat, fallbackSet := "", false
	synthesisSeat, synthesisSet := "", false
	timeoutSeconds := 600
	jobs := 1
	reportOnly := false

	for len(args) > 0 {
		switch args[0] {
		case "--traces":
			requireValue(args, args[0])
			if strings.HasPrefix(args[1], "-") {
				die(1, "%s requires a value", args[0])
			}
			args = args[1:]
			for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
				tracesDirs = append(tracesDirs, args[0])
				args = args[1:]
			}
		case "--":
			tracesDirs = append(tracesDirs, args[1:]...)
			args = nil
		case "--public":
			requireValue(args, args[0])
			publicDir, publicSet, args = args[1], true, args[2:]
		case "--private":
			requireValue(args, args[0])
			privateDir, privateSet, args = args[1], true, args[2:]
		case "--bulk-seat":
			requireValue(args, args[0])
			bulkSeat, bulkSet, args = args[1], true, args[2:]
		case "--fallback-seat":
			requireValue(args, args[0])
			fallbackSeat, fallbackSet, args = args[1], true, args[2:]
		case "--synthesis-seat":
			requireValue(args, args[0])
			synthesisSeat, synthesisSet, args = args[1], true, args[2:]
		case "--timeout":
			requireValue(args, args[0])
			n, err := strconv.Atoi(args[1])
			if err != nil || n <= 0 {
				die(1, "--timeout requires a positive whole number of seconds")
			}
			timeoutSeconds, args = n, args[2:]
		case "--jobs":
			requireValue(args, args[0])
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 1 {
				die(1, "--jobs requires a positive whole number")
			}
			jobs, args = n, args[2:]
		case "--report-only":
			reportOnly, args = true, args[1:]
		case "-h", "--help":
			fmt.Fprint(stdout, memoryBackpassUsageText)
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for memory backpass: %s", args[0])
			}
			die(1, "memory backpass accepts no positional arguments")
		}
	}

	tracesResults, tracesTried, err := resolveFirst(tracesResolveSteps(tracesDirs)...)
	if err != nil {
		die(1, "%v", err)
	}
	if len(tracesResults) == 0 {
		die(1, "could not resolve --traces; tried: %s", strings.Join(tracesTried, "; "))
	}

	publicResults, _, _ := resolveFirst(publicResolveSteps(publicDir, publicSet)...)

	privateResults, privateTried, err := resolveFirst(privateResolveSteps(privateDir, privateSet)...)
	if err != nil {
		die(1, "%v", err)
	}
	if len(privateResults) == 0 {
		die(1, "could not resolve --private; tried: %s", strings.Join(privateTried, "; "))
	}

	for i := range tracesResults {
		tracesResults[i].path = resolveGitRepo(tracesResults[i].path)
	}
	publicResults[0].path = resolveGitRepo(publicResults[0].path)
	privateResults[0].path = resolveGitRepo(privateResults[0].path)

	tracesDirs = make([]string, len(tracesResults))
	for i, r := range tracesResults {
		tracesDirs[i] = r.path
	}
	publicDir = publicResults[0].path
	privateDir = privateResults[0].path

	inputs := append(append([]resolvedInput{}, tracesResults...), publicResults[0], privateResults[0])

	if !bulkSet {
		bulkSeat = os.Getenv("ALLOD_BACKPASS_BULK_SEAT")
	}
	if bulkSeat == "" {
		bulkSeat = backpassBuiltinSeat
	}
	if !fallbackSet {
		fallbackSeat = os.Getenv("ALLOD_BACKPASS_FALLBACK_SEAT")
	}
	if !synthesisSet {
		synthesisSeat = os.Getenv("ALLOD_BACKPASS_SYNTHESIS_SEAT")
	}
	if synthesisSeat == "" && !reportOnly {
		synthesisSeat = backpassBuiltinSeat
	}
	if bulkSeat == backpassBuiltinSeat || fallbackSeat == backpassBuiltinSeat || synthesisSeat == backpassBuiltinSeat {
		checkBuiltinSeatPath()
	}

	runBackpass(backpassConfig{
		tracesDirs:    tracesDirs,
		publicDir:     publicDir,
		privateDir:    privateDir,
		bulkSeat:      bulkSeat,
		fallbackSeat:  fallbackSeat,
		synthesisSeat: synthesisSeat,
		timeout:       time.Duration(timeoutSeconds) * time.Second,
		jobs:          jobs,
		reportOnly:    reportOnly,
		inputs:        inputs,
	})
}
