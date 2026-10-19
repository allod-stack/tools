package main

// The spec is skills/memory-backpass/SKILL.md in allod/memory; the step
// numbers in names and comments here are its.

import (
	"fmt"
	"os"
	"path/filepath"
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
  allod memory backpass --traces <dir>... [--public <dir>] [--private <dir>]
      [--bulk-seat <cmd>] [--fallback-seat <cmd>] [--synthesis-seat <cmd>]
      [--timeout <seconds>] [--jobs <n>] [--report-only]

'--traces <dir>' is required, space-separated and repeatable: each is a git
checkout of distilled traces laid out <machine>/<harness>/<date>-<id>.md.

'--public <dir>' default: <workDir>/<registry checkout of allod/memory>.
'--private <dir>' default: the one registry entry marked "memory": true
other than allod/memory; zero or more than one is a refusal.

'--bulk-seat <cmd>' (env ALLOD_BACKPASS_BULK_SEAT) and '--synthesis-seat
<cmd>' (env ALLOD_BACKPASS_SYNTHESIS_SEAT) default, when neither is given,
to the built-in 'pi' seat: pi with its own configured default provider and
model, run under 'script' for a terminal. '--fallback-seat <cmd>' (env
ALLOD_BACKPASS_FALLBACK_SEAT) has no default; pass 'pi' to use the
built-in there too. An option always wins over its environment variable.

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

	if len(tracesDirs) == 0 {
		die(1, "memory backpass requires --traces <dir>...")
	}
	for i, dir := range tracesDirs {
		tracesDirs[i] = resolveGitRepo(dir)
	}

	if !publicSet {
		publicDir = defaultPublicMemoryDir()
	}
	publicDir = resolveGitRepo(publicDir)

	if !privateSet {
		resolved, err := defaultPrivateMemoryDir()
		if err != nil {
			die(1, "%v", err)
		}
		privateDir = resolved
	}
	privateDir = resolveGitRepo(privateDir)

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
	})
}

func defaultPublicMemoryDir() string {
	checkout, ok := registryCheckout("allod/memory")
	if !ok {
		checkout = "allod/memory"
	}
	return filepath.Join(workDir(), checkout)
}

func defaultPrivateMemoryDir() (string, error) {
	var ids []string
	for _, id := range memoryRegistryIds() {
		if id != "allod/memory" {
			ids = append(ids, id)
		}
	}
	switch len(ids) {
	case 1:
		checkout, ok := registryCheckout(ids[0])
		if !ok {
			return "", fmt.Errorf("registry entry %s has no checkout path", ids[0])
		}
		return filepath.Join(workDir(), checkout), nil
	case 0:
		return "", fmt.Errorf(`no private memory registry entry found ("memory": true, other than allod/memory); pass --private`)
	default:
		return "", fmt.Errorf(`multiple private memory registry entries found (%s); pass --private`, strings.Join(ids, ", "))
	}
}
