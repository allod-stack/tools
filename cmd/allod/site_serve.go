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

const sitePreviewPoll = 250 * time.Millisecond

// The 'or {}' fallbacks answer for a flake with no apps rather than failing.
const sitePreviewAppExpr = `let flake = builtins.getFlake "%s"; system = builtins.currentSystem; ` +
	`in if ((flake.apps or {}).${system} or {}) ? preview then "" else "apps.${system}.preview"`

// A checkout path reaches nix quoted in a Nix string and again in a flake
// reference, where '"', '${', '#' and '?' all mean something.
const sitePreviewPlain = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"

// '--stop' is serve's alone; view refuses it by name.
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
			if command != "serve" {
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

// ssh joins the words after the host with spaces and hands them to a shell there,
// so a value arrives as one argument only if it is quoted here.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// The connection is never a shared one: a forward on a shared connection outlives
// the command that asked for it.
func sitePreviewRemote(vm, site string, port int, stop bool, out io.Writer) int {
	remote := []string{"-o", "ControlMaster=no", "-o", "ControlPath=none", "--", vm, "allod", "site", "serve"}
	if port != 0 {
		remote = append(remote, "--port", shellQuote(fmt.Sprint(port)))
	}
	if stop {
		remote = append(remote, "--stop")
	}
	return sitePreviewRun("ssh", append(remote, shellQuote(site)), out)
}

func siteServe(args []string) {
	port, vm, site, stop := siteFlags("serve", args)
	if vm != "" {
		if site == "" {
			siteListMissingID("serve", "--vm needs a site id: nothing is resolved on this machine", true)
		}
		exit(sitePreviewRemote(vm, site, port, stop, stdout))
	}
	root, name, entry := sitePreviewSite(site)
	unit := "allod-preview-" + sitePreviewSlug(name)
	if stop {
		// A stop alone cannot report this: it exits 5 on a unit never loaded.
		if !sitePreviewRunning(unit) {
			fmt.Fprintf(stdout, "%s is not running\n", unit)
		} else if sitePreviewRun("systemctl", []string{"--user", "stop", unit}, stderr) != 0 {
			die(1, "could not stop %s", unit)
		}
		return
	}
	// The registry's port is read only where it is needed, so a malformed one
	// neither blocks a stop nor outlives the '--port' that replaces it.
	if port == 0 {
		if port = previewPort(name, entry); port == 0 {
			die(1, "no preview port for %s: give its entry in %s a preview_port, or pass --port", name, registryPath())
		}
	}
	// The app and the path are checked only when a start is about to happen: a
	// preview serving a worktree answers to the same id as a checkout without the
	// app, and nothing but a start hands the path to nix.
	if !sitePreviewRunning(unit) {
		for _, character := range root {
			if !strings.ContainsRune(sitePreviewPlain+"/+", character) {
				die(1, "the checkout path %s cannot be given to nix: it contains %q", root, character)
			}
		}
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

// Every route to one site must reach one name, and so one unit name, or a
// '--stop' elsewhere would not name the unit this machine started. A git worktree
// is therefore looked up through the repository it belongs to, while the root
// stays the worktree being edited.
func sitePreviewSite(site string) (root, name string, entry registryEntry) {
	entries := registryEntries()
	if site != "" {
		entry, found := entries[site]
		if !found || entry.Checkout == "" {
			siteListUnknown(site, true)
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

// sitePreviewRunning answers from the exit codes measured for 'systemctl --user
// is-active --quiet': 0 active, 3 loaded but not active (activating, or failed
// before it was collected), 4 inactive or no such unit. A 3 counts as running, so
// a unit that is still coming up is stopped rather than reported as absent, and
// any other code means systemd could not be asked, which is never "not running".
func sitePreviewRunning(unit string) bool {
	switch status := sitePreviewRun("systemctl", []string{"--user", "is-active", "--quiet", unit}, io.Discard); status {
	case 0, 3:
		return true
	case 4:
		return false
	default:
		die(1, "could not ask systemd about %s: 'systemctl --user is-active' exited %d", unit, status)
		return false
	}
}

// A connection is only good news while the unit is still up: an app that accepts
// one connection and exits, or another program holding the port, answers just as
// a working preview does.
func sitePreviewWait(unit string, port int) {
	address := fmt.Sprintf("127.0.0.1:%d", port)
	for deadline := time.Now().Add(sitePreviewTimeout); ; time.Sleep(sitePreviewPoll) {
		accepted := false
		if connection, err := net.DialTimeout("tcp", address, time.Second); err == nil {
			connection.Close()
			accepted = true
		}
		if !sitePreviewRunning(unit) {
			sitePreviewRun("journalctl", []string{"--user", "-u", unit, "-n", "20", "--no-pager"}, stderr)
			die(1, "%s is no longer running; its last log lines are above", unit)
		}
		if accepted {
			fmt.Fprintf(stdout, "http://%s\n", address)
			return
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(stderr, "allod: %s is still starting; follow it with: journalctl --user -f -u %s\n", unit, unit)
			exit(3)
		}
	}
}

const siteServeDetail = `'serve' hands the site's own 'preview' flake app to the user systemd manager as
the unit 'allod-preview-<slug>', waits up to 60 seconds for its port to answer,
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

Exit 0 prints the address, whether this run started the server or found one up.
Exit 3 means the unit is up with the port still silent, as a first build looks.
Exit 1 is everything else: a refusal, a command that failed, or a unit that has
stopped, in which case its last log lines are on standard error.
`

// siteCommands in site_common.go says why the entry is added from an init().
func init() {
	registerNamespace(namespace{name: "site", summary: "Serve and view a static site; deploy, check, config are present in site-tagged builds", main: siteMain})
	siteCommands = append([]siteCommand{{
		name:    "serve",
		summary: "Serve a site locally from the 'preview' app in its own flake",
		usage:   []string{"allod site serve [--port <n>] [--stop] [--vm <name>] [<site>]"},
		detail:  siteServeDetail,
		run:     siteServe,
	}}, siteCommands...)
}
