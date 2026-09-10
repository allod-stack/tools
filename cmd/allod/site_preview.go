package main

// 'allod site preview' starts a live-reloading local server for the site
// found by walking up from the current directory, rendered by that site
// repository's own locked zola rather than whatever zola happens to be on
// PATH: 'nix shell --inputs-from <root> nixpkgs#zola --command zola serve
// --root <root>' plus whatever flags below were given. That is the same nix
// expression 'allod site deploy' builds from, so a channel bump in the site
// repository's flake moves what preview shows and what deploy publishes
// together.
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

// sitePreviewRun is the test seam: every effect this command has outside the
// process goes through it, so tests can capture the argv it builds without a
// nix daemon or a network. Production runs the real thing, with stdin,
// stdout, and stderr passed straight through and zola's own exit code
// propagated as-is.
var sitePreviewRun = runSitePreviewZola

func runSitePreviewZola(root string, zolaArgs []string) int {
	args := []string{"shell", "--inputs-from", root, "nixpkgs#zola", "--command", "zola", "serve", "--root", root}
	args = append(args, zolaArgs...)
	return runCommand("", stdin, stdout, stderr, "nix", args...)
}

func sitePreview(args []string) {
	var zolaArgs []string
	for len(args) > 0 {
		switch args[0] {
		case "--port", "--interface", "--base-url":
			requireValue(args, args[0])
			zolaArgs = append(zolaArgs, args[0], args[1])
			args = args[2:]
		case "--drafts", "--open":
			zolaArgs = append(zolaArgs, args[0])
			args = args[1:]
		case "--":
			zolaArgs = append(zolaArgs, args[1:]...)
			args = nil
		case "-h", "--help":
			fmt.Fprint(stdout, siteUsageText())
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for site preview: %s", args[0])
			}
			die(1, "unexpected argument for site preview: %s", args[0])
		}
	}

	root := resolveSiteRoot()
	if _, err := exec.LookPath("nix"); err != nil {
		die(1, "'nix' not found on PATH")
	}
	exit(sitePreviewRun(root, zolaArgs))
}

const sitePreviewSummary = "Serve a site locally with the generator its deploy build pins"

var sitePreviewUsage = []string{
	"allod site preview [--port <n>] [--interface <addr>] [--base-url <url>] [--drafts] [--open] [-- <zola args>...]",
}

const sitePreviewDetail = `'preview' walks up from the current directory to the site.toml that marks
the site repository root, the way every site command does, then starts that
repository's own locked zola: 'nix shell --inputs-from <root> nixpkgs#zola
--command zola serve --root <root>' plus the flags below. Standard input,
output, and error connect straight through to zola, and preview exits with
zola's own exit code.

'--port <n>', '--interface <addr>', '--base-url <url>', '--drafts', and
'--open' mirror zola's own flags of the same names. Only a flag actually
given is passed through, so an unset one keeps zola's own default rather than
one this command invents. Anything after '--' is passed to zola verbatim.

Without '--interface', zola binds its own default, 127.0.0.1:1111 — loopback,
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
// friends in site.go) whose body calls siteUsageText, which reads
// siteCommands back — a var initializer that built such a value directly
// would be a compile-time initialization cycle, since Go's dependency
// analysis follows a function reference into that function's own body. This
// prepends rather than appends, so 'preview' stays first in the table
// whether this init() or site.go's runs first; file init() order is decided
// by the compiler's file-name ordering, not by this program.
//
// The namespace summary names all four commands and states which need the
// tag, which is true whether or not this particular binary carries it: a
// static line, chosen for exactly this reason, needs no coordination with
// site.go's init() to stay accurate in both shapes.
func init() {
	registerNamespace(namespace{
		name:    "site",
		summary: "Preview a site locally; deploy, check, config need a site-tagged build",
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
