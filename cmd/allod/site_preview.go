package main

// 'allod site preview' starts a live-reloading local server for the site
// found by walking up from the current directory, rendered by that site
// repository's own locked zola rather than whatever zola happens to be on
// PATH: 'nix shell --inputs-from <root> nixpkgs#zola --command zola --root
// <root> serve' plus whatever flags below were given. '--root' is zola's own
// global option and must precede 'serve' — 'zola serve --root <root>' is
// rejected ("unexpected argument '--root' found"); 'zola --root <root>
// serve' is accepted. That is the same nix expression 'allod site deploy'
// builds from, so a channel bump in the site repository's flake moves what
// preview shows and what deploy publishes together.
//
// The generator is hardcoded to zola because every site this command knows
// about builds with zola; the site derivation, not this command, owns that
// choice. The trigger to stop hardcoding it is a second generator appearing
// in a site repository — at that point a 'generator' key in site.toml or a
// 'preview' app the site flake exposes selects it, not a guess made here.
// site.toml ignores unknown keys today, so adding one later stays compatible
// with a binary that predates it.
//
// This command carries no build tag on purpose: sites are edited on dev
// machines, and dev machines hold no hosting credential and no rclone
// configuration. Preview never reads either, never runs the remote
// preflight 'deploy' and 'check' both depend on, and takes no '--config'.

import (
	"fmt"
	"os/exec"
	"strings"
)

// sitePreviewArgs builds the full argv for the 'nix' invocation that starts
// zola. It stays in production code and is never replaced by a test, unlike
// sitePreviewRun below: a test that swapped this out to build its own argv
// would be checking its own reimplementation, not this command's, and could
// not catch a mistake here — such as '--root' landing after 'serve', where
// zola rejects it.
func sitePreviewArgs(root string, zolaArgs []string) []string {
	args := []string{"shell", "--inputs-from", root, "nixpkgs#zola", "--command", "zola", "--root", root, "serve"}
	return append(args, zolaArgs...)
}

// sitePreviewRun is the test seam, placed at the exec boundary rather than
// around argv construction: it runs one command, given its full argv, and
// returns its exit code. Production wraps runCommand, with stdin, stdout,
// and stderr passed straight through and the child's own exit code
// propagated as-is; tests replace it to capture what sitePreviewArgs built,
// without a nix daemon or a network.
var sitePreviewRun = func(name string, args []string) int {
	return runCommand("", stdin, stdout, stderr, name, args...)
}

func sitePreview(args []string) {
	var zolaArgs []string
	for len(args) > 0 {
		switch args[0] {
		case "--port", "--interface", "--base-url":
			if len(args) < 2 {
				siteCommandUsageError("preview", "%s requires a value", args[0])
			}
			zolaArgs = append(zolaArgs, args[0], args[1])
			args = args[2:]
		case "--drafts", "--open":
			zolaArgs = append(zolaArgs, args[0])
			args = args[1:]
		case "--":
			zolaArgs = append(zolaArgs, args[1:]...)
			args = nil
		case "-h", "--help":
			fmt.Fprint(stdout, siteCommandHelp("preview"))
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				siteCommandUsageError("preview", "unknown option for site preview: %s", args[0])
			}
			siteCommandUsageError("preview", "unexpected argument for site preview: %s", args[0])
		}
	}

	root := resolveSiteRoot()
	if _, err := exec.LookPath("nix"); err != nil {
		die(1, "'nix' not found on PATH")
	}
	exit(sitePreviewRun("nix", sitePreviewArgs(root, zolaArgs)))
}

const sitePreviewSummary = "Serve a site locally with the generator its deploy build pins"

var sitePreviewUsage = []string{
	"allod site preview [--port <n>] [--interface <addr>] [--base-url <url>] [--drafts] [--open] [-- <zola args>...]",
}

const sitePreviewDetail = `'preview' walks up from the current directory to the site.toml that marks
the site repository root, the way every site command does, then starts that
repository's own locked zola: 'nix shell --inputs-from <root> nixpkgs#zola
--command zola --root <root> serve' plus the flags below. Standard input,
output, and error connect straight through to zola, and preview exits with
zola's own exit code.

'--port <n>', '--interface <addr>', '--base-url <url>', '--drafts', and
'--open' mirror zola's own flags of the same names. Only a flag actually
given is passed through, so an unset one keeps zola's own default rather than
one this command invents. Anything after '--' is passed to zola verbatim.

Without '--interface' and '--port', zola binds its own default, 127.0.0.1:1111 — loopback,
so nothing outside this machine can reach it, and this machine's firewall
does not open it either. View it from the host's browser through an SSH
tunnel: 'ssh -L 1111:127.0.0.1:1111 <vm>', then open http://127.0.0.1:1111
there.

'preview' never reads the rclone configuration, never checks the hosting
remote, and takes no '--config': nothing it does can reach shared hosting.
It needs no build tag, unlike every other site command, because this is
exactly the command that must run on a machine with no hosting credential to
protect.

The generator is zola. This command does not read a generator choice from
site.toml or ask the site flake which one it uses; a second generator, when
one appears in a site repository, is the trigger to make that a choice
rather than a constant.
`

// init registers the 'site' namespace and its 'preview' command. It runs in
// every build, tagged or not, which is what makes 'preview' available
// without the 'site' tag: this file carries none.
//
// siteCommands is declared with no initializer in site_common.go, and every
// entry is added here or in site.go's init() rather than in a var literal:
// a siteCommand's run field is a function (sitePreview here, siteDeploy and
// friends in site.go) whose body calls siteCommandHelp, which reads
// siteCommands back — a var initializer that built such a value directly
// would be a compile-time initialization cycle, since Go's dependency
// analysis follows a function reference into that function's own body. This
// prepends rather than appends, so 'preview' stays first in the table
// whether this init() or site.go's runs first; file init() order is decided
// by the compiler's file-name ordering, not by this program.
//
// The namespace summary states a fact about the build variant, not about
// this particular binary, which is what keeps it true in both shapes: it
// would read wrong on a tagged build if it said the other three commands
// "need" the tag, since on that build they are right there. A static line,
// chosen for exactly this reason, needs no coordination with site.go's
// init() to stay accurate either way.
func init() {
	registerNamespace(namespace{
		name:    "site",
		summary: "Preview a static site; deploy, check, config are present in site-tagged builds",
		main:    siteMain,
	})
	siteCommands = append([]siteCommand{{
		name:    "preview",
		summary: sitePreviewSummary,
		usage:   sitePreviewUsage,
		detail:  sitePreviewDetail,
		run:     sitePreview,
	}}, siteCommands...)
}
