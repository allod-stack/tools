package main

import (
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"
)

// The test seam is the exec boundary, never the argument lists it is handed.
var sitePreviewRun = func(name string, args []string, out io.Writer) int {
	return runCommand("", nil, out, stderr, name, args...)
}

var sitePreviewTimeout = 60 * time.Second
var sitePreviewPoll = 250 * time.Millisecond

// The 'or {}' fallbacks answer for a flake with no apps rather than failing.
const sitePreviewAppExpr = `let flake = builtins.getFlake "%s"; system = builtins.currentSystem; ` +
	`in if ((flake.apps or {}).${system} or {}) ? preview then "" else "apps.${system}.preview"`

// A checkout path reaches nix quoted in a Nix string and again in a flake
// reference, where '"', '${', '#' and '?' all mean something.
const sitePreviewPlain = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"

// siteFlags parses the options 'preview' and 'view' share, refusing a value that
// begins with '-' wherever one is expected, and prints command's own help for
// '-h'. '--stop' is preview's alone; view refuses it by name.
func siteFlags(command string, args []string) (port int, vm, site string, stop bool) {
	for len(args) > 0 {
		switch option := args[0]; option {
		case "--port", "--vm":
			if len(args) < 2 || strings.HasPrefix(args[1], "-") {
				siteCommandUsageError(command, "%s requires a value", option)
			}
			if option == "--vm" {
				vm = args[1]
			} else if number, ok := previewPortValue(args[1]); ok && number != 0 {
				port = number
			} else {
				siteCommandUsageError(command, "--port must be a whole number from 1024 to 65535, not %s", args[1])
			}
			args = args[2:]
		case "--stop":
			if command != "preview" {
				siteCommandUsageError(command, "unknown option for site %s: --stop", command)
			}
			stop, args = true, args[1:]
		case "-h", "--help":
			fmt.Fprint(stdout, siteCommandHelp(command))
			exit(0)
		default:
			if strings.HasPrefix(option, "-") {
				siteCommandUsageError(command, "unknown option for site %s: %s", command, option)
			}
			if site != "" {
				siteCommandUsageError(command, "unexpected argument for site %s: %s", command, option)
			}
			site, args = option, args[1:]
		}
	}
	return port, vm, site, stop
}

// shellQuote wraps one value for the shell in the VM: ssh joins the words after
// the host with spaces and hands the result to that shell, so a site id with a
// space or a quote in it arrives as one argument only if it is quoted here.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// sitePreviewRemote runs this same command in vm over a connection of its own: a
// forward on a shared connection outlives the command that asked for it.
func sitePreviewRemote(vm, site string, port int, stop bool, out io.Writer) int {
	remote := []string{"-o", "ControlMaster=no", "-o", "ControlPath=none", "--", vm, "allod", "site", "preview"}
	if port != 0 {
		remote = append(remote, "--port", shellQuote(fmt.Sprint(port)))
	}
	if stop {
		remote = append(remote, "--stop")
	}
	return sitePreviewRun("ssh", append(remote, shellQuote(site)), out)
}

func sitePreview(args []string) {
	port, vm, site, stop := siteFlags("preview", args)
	if vm != "" {
		if site == "" {
			siteCommandUsageError("preview", "--vm needs a site id: nothing is resolved on this machine")
		}
		exit(sitePreviewRemote(vm, site, port, stop, stdout))
	}
	root, name, entry := sitePreviewSite(site)
	registryPort := previewPort(name, entry)
	for _, character := range root {
		if !strings.ContainsRune(sitePreviewPlain+"/+", character) {
			die(1, "the checkout path %s cannot be given to nix: it contains %q", root, character)
		}
	}
	unit := "allod-preview-" + sitePreviewSlug(name)
	if stop {
		// A stop alone cannot report this: it exits 5 on a unit never loaded.
		if !sitePreviewActive(unit) {
			fmt.Fprintf(stdout, "%s is not running\n", unit)
		} else if sitePreviewRun("systemctl", []string{"--user", "stop", unit}, stderr) != 0 {
			die(1, "could not stop %s", unit)
		}
		return
	}
	if port == 0 && registryPort == 0 {
		die(1, "no preview port for %s: give its entry in %s a preview_port, or pass --port", name, registryPath())
	} else if port == 0 {
		port = registryPort
	}
	// The app is asked about only when a start is about to happen: a preview
	// serving a worktree answers to the same id as a checkout without the app.
	if !sitePreviewActive(unit) {
		var absent strings.Builder
		if sitePreviewRun("nix", []string{"eval", "--impure", "--raw", "--expr", fmt.Sprintf(sitePreviewAppExpr, root)}, &absent) != 0 {
			die(1, "could not evaluate the flake at %s", root)
		}
		if missing := strings.TrimSpace(absent.String()); missing != "" {
			die(1, "the flake at %s has no %s; docs/allod-site-preview.md in allod/tools has worked examples", root, missing)
		}
		start := []string{"--user", "--collect", "--quiet", "--unit", unit, "--working-directory", root, "-E",
			fmt.Sprintf("ALLOD_PREVIEW_PORT=%d", port), "-E", "ALLOD_PREVIEW_INTERFACE=127.0.0.1", "--", "nix", "run", root + "#preview"}
		if sitePreviewRun("systemd-run", start, stderr) != 0 {
			die(1, "could not start %s", unit)
		}
	}
	sitePreviewWait(unit, port)
}

// sitePreviewSite resolves the site to its checkout root, the name it is refused,
// reported and named after, and the registry entry that states its port. Every
// route to one site reaches one name, and so one unit name, which is what lets a
// '--stop' elsewhere name the unit this machine started: a directory is looked
// back up in the registry, and a git worktree, which no registry names, through
// the repository it belongs to, while the root stays the worktree being edited.
func sitePreviewSite(site string) (root, name string, entry registryEntry) {
	entries := registryEntries()
	if site != "" {
		entry, found := entries[site]
		if !found || entry.Checkout == "" {
			die(1, "unknown site: %s has no entry in %s", site, registryPath())
		}
		return filepath.Join(workDir(), entry.Checkout), site, entry
	}
	root = resolveSiteRoot()
	if id, found := siteWithCheckout(entries, root); found {
		return root, id, entries[id]
	}
	var common strings.Builder
	if sitePreviewRun("git", []string{"-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir"}, &common) == 0 {
		repository := filepath.Dir(strings.TrimSpace(common.String()))
		if id, found := siteWithCheckout(entries, repository); found {
			return root, id, entries[id]
		}
	}
	return root, strings.TrimPrefix(root, homeDir()+"/"), registryEntry{}
}

func siteWithCheckout(entries map[string]registryEntry, directory string) (id string, found bool) {
	for candidate, entry := range entries {
		if entry.Checkout != "" && filepath.Join(workDir(), entry.Checkout) == directory {
			return candidate, true
		}
	}
	return "", false
}

func sitePreviewSlug(name string) string {
	return strings.Map(func(character rune) rune {
		if strings.ContainsRune(sitePreviewPlain, character) {
			return character
		}
		return '-'
	}, name)
}

func sitePreviewActive(unit string) bool {
	return sitePreviewRun("systemctl", []string{"--user", "is-active", "--quiet", unit}, io.Discard) == 0
}

// The connection comes before the question about the unit, so a server that
// answered in the instant its unit ended still counts as serving.
func sitePreviewWait(unit string, port int) {
	address := fmt.Sprintf("127.0.0.1:%d", port)
	for deadline := time.Now().Add(sitePreviewTimeout); ; time.Sleep(sitePreviewPoll) {
		if connection, err := net.DialTimeout("tcp", address, time.Second); err == nil {
			connection.Close()
			fmt.Fprintf(stdout, "http://%s\n", address)
			return
		}
		if !sitePreviewActive(unit) {
			sitePreviewRun("journalctl", []string{"--user", "-u", unit, "-n", "20", "--no-pager"}, stderr)
			die(1, "%s is no longer running; its last log lines are above", unit)
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(stderr, "allod: %s is still starting; follow it with: journalctl --user -f -u %s\n", unit, unit)
			exit(3)
		}
	}
}

const sitePreviewDetail = `'preview' hands the site's own 'preview' flake app to the user systemd manager
as the unit 'allod-preview-<slug>', waits up to 60 seconds for its port to answer,
then exits; the server keeps running under systemd until '--stop'. A flake with no
'apps.<system>.preview', or a checkout path nix could not be given as written, is
refused: nothing here knows which tool builds a site. <site> is an id in the
repository registry, and without it the site is the checkout the current directory
sits in, or, in a git worktree of one, that site under its own id; the port is that
entry's 'preview_port', which '--port <n>' overrides, and <slug> is that id, or the
checkout path under $HOME, with every character outside [A-Za-z0-9._-] replaced
by '-'.

The server listens on 127.0.0.1 alone, and its output is in the journal:
'journalctl --user -u allod-preview-<slug>', with '-f' to follow it. '--vm <name>'
resolves nothing here and runs this same command in that VM over a connection of
its own, which is how the owner stops a preview from the hypervisor.

Exit 0 prints the address, whether this run started the server or found one up;
exit 1 means the unit has stopped, and its last log lines are on standard error;
exit 3 means the unit is up with the port still silent, as a first build looks.
`

// This file carries no build tag, so preview is in every build; siteCommands in
// site_common.go says why the entry is added from an init().
func init() {
	registerNamespace(namespace{name: "site", summary: "Preview a static site; deploy, check, config are present in site-tagged builds", main: siteMain})
	siteCommands = append([]siteCommand{{
		name:    "preview",
		summary: "Serve a site locally from the 'preview' app in its own flake",
		usage:   []string{"allod site preview [--port <n>] [--stop] [--vm <name>] [<site>]"},
		detail:  sitePreviewDetail,
		run:     sitePreview,
	}}, siteCommands...)
}
