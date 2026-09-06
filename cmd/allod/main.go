// Command allod manages workspace changes, patch transfer, site deploys, PR
// explanations, and the PM board. The Bash implementation at the repository
// root remains the compatibility oracle until the Phase 4 cleanup in
// allod/tools#98.
package main

import (
	"fmt"
	"io"
	"os"
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
		fmt.Fprint(stdout, usageText)
		return 1
	}

	switch args[0] {
	case "change":
		changeMain(args[1:])
	case "patch":
		patchMain(args[1:])
	case "site":
		siteMain(args[1:])
	case "pr":
		delegatePR(args[1:])
	case "pm":
		delegatePM(args[1:])
	case "-h", "--help":
		fmt.Fprint(stdout, usageText)
	default:
		die(1, "unknown command namespace: %s", args[0])
	}
	return 0
}

const usageText = `Usage: allod <namespace> <command> [options]

Namespaces:
  change   Manage code changes (begin, list, record, submit, cleanup)
  patch    Transfer patches between environments (fetch, apply, receive)
  site     Deploy a static site to shared hosting (deploy)
  pr       Work with pull requests (explain)
  pm       Manage the PM board overlay in the private state repo
`

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
`

const siteUsageText = `Usage: allod site deploy [--dry-run]

Commands:
  deploy   Build the site repo and sync the result to its shared-hosting docroot

'deploy' walks up from the current directory to the site.toml that marks the
site repository root, builds that repo with 'nix build --no-link
--print-out-paths', and syncs the resulting store path to
shared:domains/<domain>/public_html. 'shared' is an rclone remote configured
once per machine; this command never handles a credential.

The docroot is derived from the 'domain' key in site.toml and from nothing
else. No flag, argument, or environment variable can point a deploy at another
site, because one hosting account owns every docroot on the server and a
redirected sync would delete a sibling site.

The exclusion filter belongs to this command rather than to the site repo. It
is written to a temporary file per run and passed as --filter-from, so every
site gets the same list and no repo can quietly lose the /.well-known/** entry
that certificate renewal depends on. Replaced and deleted files are moved to
shared:deploy-trash/<domain> rather than destroyed.

site.toml today has exactly one key:

  domain = "example.com"

Unknown keys are ignored, so a file written for a later version still deploys.

'--dry-run' passes --dry-run to rclone: the build still runs and rclone reports
the changes it would make, but the docroot is untouched and the HTTPS check is
skipped. Without it, deploy checks that https://<domain>/ answers 200 and exits
7 if it does not, which distinguishes a site that did not deploy from one that
deployed and is not serving.
`
