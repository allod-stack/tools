// Command allod manages workspace changes, patch transfer, site deploys, and
// PR explanations. The Bash implementation at the repository root remains the
// compatibility oracle until the Phase 4 cleanup in allod/tools#98.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

var (
	stdin  io.Reader = os.Stdin
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

type cliExit struct{ code int }

func exit(code int) { panic(cliExit{code: code}) }

func die(code int, format string, args ...any) {
	fmt.Fprintf(stderr, "allod: "+format+"\n", args...)
	exit(code)
}

// A namespace is one top-level command group: the word that selects it, the
// line that describes it in the usage text, and the function that runs it.
type namespace struct {
	name    string
	summary string
	main    func(args []string)
}

// namespaces is the dispatch table, and it is the whole of it: a namespace
// that is not in this slice does not exist. The core three are here; an
// optional namespace appends itself from the init() of a file its build tag
// selects, so a build that does not opt in carries neither the code nor the
// word. That is the point of the arrangement. 'allod site' on a machine that
// deploys nothing fails the way 'allod frobnicate' does — the capability is
// absent — rather than dispatching into a command that was never going to
// work and failing several minutes later for a reason the operator has to
// decode.
//
// Package-level variables are initialised before any init() runs, so the core
// namespaces are always registered first and an optional one always lists
// last, whatever the compiler's file order happens to be.
var namespaces = []namespace{
	{"change", "Manage code changes (begin, list, record, submit, cleanup)", changeMain},
	{"patch", "Transfer patches between environments (fetch, apply, receive)", patchMain},
	{"pr", "Work with pull requests (explain)", delegatePR},
}

// registerNamespace adds one namespace to the dispatch table. It panics on a
// duplicate because that is a mistake in the program, not in its input: two
// files claiming one word would otherwise make dispatch depend on file order.
func registerNamespace(entry namespace) {
	if _, exists := lookupNamespace(entry.name); exists {
		panic("duplicate command namespace: " + entry.name)
	}
	namespaces = append(namespaces, entry)
}

func lookupNamespace(name string) (namespace, bool) {
	for _, entry := range namespaces {
		if entry.name == name {
			return entry, true
		}
	}
	return namespace{}, false
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) (code int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if e, ok := recovered.(cliExit); ok {
				code = e.code
				return
			}
			panic(recovered)
		}
	}()

	if len(args) == 0 {
		fmt.Fprint(stdout, usageText())
		return 1
	}

	if entry, ok := lookupNamespace(args[0]); ok {
		entry.main(args[1:])
		return 0
	}

	switch args[0] {
	case "-h", "--help":
		fmt.Fprint(stdout, usageText())
	default:
		die(1, "unknown command namespace: %s", args[0])
	}
	return 0
}

// usageText renders the top-level usage from the dispatch table, so a build
// advertises exactly the namespaces it carries and no others.
func usageText() string {
	var text strings.Builder
	text.WriteString("Usage: allod <namespace> <command> [options]\n\nNamespaces:\n")
	for _, entry := range namespaces {
		fmt.Fprintf(&text, "  %-8s %s\n", entry.name, entry.summary)
	}
	return text.String()
}

const changeUsageText = `Usage:
  allod change begin [-d <description>] [<repo-path>]
  allod change list [<repo-path>]
  allod change record -m <message> [--files <file>...]
  allod change record -M <file|-> [--files <file>...]
  allod change submit -t <title> (-b <body> | -F <file|->) [--base <branch>] [--depends-on <text>] [--dry-run]
  allod change cleanup <worktree-path>

'-d' is the isolation switch, for every repo whether protected or not: it
creates a worktree under ~/changes on a new agent/<description> branch.
Without it, begin prints the shared checkout path and creates nothing, which
is the in-place flow for committing to a repo's default branch.

'list' prints one tab-separated row per linked worktree — repo, path, branch,
state — where the state is the one thing standing between that worktree and
'cleanup': prunable, locked, detached, submodule, dirty, unpushed, unknown, or clean.

'--files' takes one or more paths and may be repeated, so '--files a b' and
'--files a --files b' both stage a and b. It stops at the next option, so it
can go anywhere in the line; pass a path that begins with '-' after '--',
which ends option parsing. With no path named, record stages every tracked
change — in a shared checkout, that includes another agent's.
`

const patchUsageText = `Usage: allod patch <command> [options]

Commands:
  fetch    Fetch patches from a remote source repo via SSH
  apply    Apply fetched patches to a local destination repo
  receive  Fetch and apply patches in one step

  allod patch fetch <ssh-host>:<source-repo> [--base <ref>] [--output <dir>]
  allod patch apply <artifact-dir> [--repo <destination-repo>]
  allod patch receive <ssh-host>:<source-repo> <destination-repo> [--base <ref>]

'--base <ref>' sets the base ref for the patch range (default: source branch
upstream, same-named origin branch, or origin default branch; if none
exists, export from root).
'--output <dir>' sets the local directory for a fetched artifact (default:
auto-generated in /tmp).
'--repo <destination-repo>' sets the apply destination (default: current
directory).

Repo arguments (<source-repo> and <destination-repo>): a repo argument that
is absolute or begins with '~', '.', or '..' is a path. Anything else is
looked up in the repository registry first and, when no entry matches,
treated as a relative path.
`
