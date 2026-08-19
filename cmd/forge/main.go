// Command forge is a Forgejo CLI: gh, but for a self-hosted Forgejo instance.
//
// It is a transliteration of the bash `forge` script at the repository root,
// which remains the specification. Stdout, stderr and exit codes are
// byte-for-byte identical, bugs included; comments reference the bash line
// numbers rather than restating the behaviour.
package main

import (
	"fmt"
	"io"
	"os"

	"forge.anarch.diy/allod/tools/internal/gitremote"
)

// Test seams. Everything that talks to the outside world goes through one of
// these four variables so tests can swap them without a subprocess. They are
// package-level and mutable, which is why cmd/forge tests never call
// t.Parallel().
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
	stdin  io.Reader = os.Stdin

	inferRepo = gitremote.InferRepo
)

// Mutable state mirroring the bash globals. See the header of helpers.go for
// the mapping; resetState() re-initialises all of it at the start of a run so
// repeated in-process runs in tests are independent.
var (
	forgeURL       string
	forgeTokenFile string
	token          string
	tokenLoaded    bool
	repoOpt        string
	positionalArgs []string
	bodyOpt        string
	bodySet        bool
	bodySource     string
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is the whole CLI. It recovers the cliExit panic that exit() and die()
// raise, which is how the bash `exit` statements sprinkled through every
// helper survive the port.
func run(argv []string) (code int) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if e, ok := r.(cliExit); ok {
			code = e.code
			return
		}
		panic(r)
	}()

	resetState()
	resource, command, args := parseGlobalArgs(argv)
	dispatch(resource, command, args)
	return 0
}

// resetState re-reads the environment and clears the bash globals. bash sets
// FORGE_URL and FORGE_TOKEN_FILE once at startup (forge lines 54-55); doing it
// here keeps that timing while letting a test process run the CLI many times.
func resetState() {
	forgeURL = envOr("FORGE_URL", "https://forge.anarch.diy")
	forgeTokenFile = envOr("FORGE_TOKEN_FILE", os.Getenv("HOME")+"/.config/git/forgejo-token")
	token = ""
	tokenLoaded = false
	repoOpt = ""
	positionalArgs = nil
	resetBodyOption()
}

// envOr mirrors ${VAR:-default}: an empty value counts as unset.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// parseGlobalArgs transliterates the pre-dispatch loop (forge lines 116-136):
// -R/--repo may precede the resource, and the resource and command are shifted
// off whatever is left.
func parseGlobalArgs(argv []string) (resource, command string, args []string) {
	repoOpt = ""
	args = argv

	for len(args) > 0 {
		if args[0] == "-R" || args[0] == "--repo" {
			if len(args) < 2 {
				fmt.Fprintf(stderr, "forge: %s requires a value\n", args[0])
				exit(1)
			}
			repoOpt = args[1]
			args = args[2:]
			continue
		}
		if args[0] == "-h" || args[0] == "--help" {
			// bash assigns RESOURCE=help here and then overwrites it below
			// with ${1:-help}, so the assignment never survives. Kept for a
			// literal reading of the loop.
			resource = "help"
			args = args[1:]
			continue
		}
		break
	}

	resource = "help"
	if len(args) > 0 {
		resource = args[0]
		args = args[1:]
	}
	command = ""
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	return resource, command, args
}

// dispatch transliterates the final case statement (forge lines 2400-2537).
// The stream and exit-code asymmetries are deliberate: an unknown command
// prints usage on stdout and exits 1, an unknown resource prints its complaint
// and usage on stderr and exits 1, and a help request prints usage on stdout
// and exits 0.
func dispatch(resource, command string, args []string) {
	switch resource {
	case "pr":
		switch command {
		case "list":
			prList(args)
		case "view":
			prView(args)
		case "review-comments":
			prReviewComments(args)
		case "reply":
			prReply(args)
		case "edit":
			prEdit(args)
		case "comment":
			prComment(args)
		case "create":
			prCreate(args)
		case "find-by-head":
			prFindByHead(args)
		case "close":
			prClose(args)
		case "-h", "--help":
			usage()
		default:
			usage()
			exit(1)
		}
	case "issue":
		switch command {
		case "list":
			issueList(args)
		case "view":
			issueView(args)
		case "create":
			issueCreate(args)
		case "edit":
			issueEdit(args)
		case "labels":
			issueLabels(args)
		case "milestone":
			issueMilestone(args)
		case "comment":
			issueComment(args)
		case "close":
			issueClose(args)
		case "-h", "--help":
			usage()
		default:
			usage()
			exit(1)
		}
	case "label":
		switch command {
		case "list":
			labelList(args)
		case "create":
			labelCreate(args)
		case "edit":
			labelEdit(args)
		case "delete":
			labelDelete(args)
		case "-h", "--help":
			usage()
		default:
			usage()
			exit(1)
		}
	case "milestone":
		switch command {
		case "list":
			milestoneList(args)
		case "view":
			milestoneView(args)
		case "create":
			milestoneCreate(args)
		case "edit":
			milestoneEdit(args)
		case "delete":
			milestoneDelete(args)
		case "-h", "--help":
			usage()
		default:
			usage()
			exit(1)
		}
	case "project":
		switch command {
		case "-h", "--help":
			commandUsage("project")
		default:
			// bash passes "$COMMAND" "$@": the command word is re-prepended
			// so a --help anywhere still reaches contains_help_flag.
			projectUnavailable(append([]string{command}, args...))
		}
	case "token":
		switch command {
		case "verify":
			tokenVerify(args)
		case "-h", "--help":
			usage()
		default:
			usage()
			exit(1)
		}
	case "auth":
		switch command {
		case "status":
			authStatus(args)
		case "token":
			die("auth token is not a command; use 'forge auth status' to check credentials")
		case "-h", "--help":
			usage()
		default:
			usage()
			exit(1)
		}
	case "help", "--help", "-h", "":
		usage()
	default:
		fmt.Fprintf(stderr, "forge: unknown resource '%s'\n", resource)
		usageTo(stderr)
		exit(1)
	}
}

// --- Usage printing ---
//
// The texts live in usage.go, generated byte-exactly from the bash heredocs
// and never edited by hand. They already end in a newline.

func usage() { usageTo(stdout) }

func usageTo(w io.Writer) { fmt.Fprint(w, usageText) }

// commandUsage mirrors command_usage (forge line 2019). An unknown key prints
// nothing, exactly like the bash case statement falling through.
func commandUsage(key string) {
	fmt.Fprint(stdout, commandUsageTexts[key])
}
