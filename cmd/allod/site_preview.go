package main

import (
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// sitePreviewRun is the test seam, at the exec boundary rather than around
// argument construction: it is handed a program, the whole argument list
// production code built, and where that program's stdout goes. Its stderr
// passes through, so nix's and systemd's own diagnostics are not reworded.
var sitePreviewRun = func(name string, args []string, out io.Writer) int {
	return runCommand("", nil, out, stderr, name, args...)
}

// Variables so a test reaches every branch of the wait below without sleeping.
var (
	sitePreviewTimeout = 60 * time.Second
	sitePreviewPoll    = 250 * time.Millisecond
)

// sitePreviewAppExpr answers with the empty string when the flake at the root
// given exposes apps.<system>.preview and with the missing attribute path when
// it does not, so one evaluation both decides and names. currentSystem under
// '--impure' spares a map from Go's architecture spelling to Nix's; the two
// 'or {}' fallbacks make a flake with no apps an answer, not an error.
const sitePreviewAppExpr = `let flake = builtins.getFlake "%s"; system = builtins.currentSystem; ` +
	`in if ((flake.apps or {}).${system} or {}) ? preview then "" else "apps.${system}.preview"`

func sitePreview(args []string) {
	port, stop, site := 0, false, ""
	for len(args) > 0 {
		switch args[0] {
		case "--port":
			if len(args) < 2 || strings.HasPrefix(args[1], "-") {
				siteCommandUsageError("preview", "--port requires a value")
			}
			number, err := strconv.Atoi(args[1])
			if err != nil || number < 1024 || number > 65535 {
				siteCommandUsageError("preview", "--port must be a whole number from 1024 to 65535, not %s", args[1])
			}
			port, args = number, args[2:]
		case "--stop":
			stop, args = true, args[1:]
		case "-h", "--help":
			fmt.Fprint(stdout, siteCommandHelp("preview"))
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				siteCommandUsageError("preview", "unknown option for site preview: %s", args[0])
			}
			if site != "" {
				siteCommandUsageError("preview", "unexpected argument for site preview: %s", args[0])
			}
			site, args = args[0], args[1:]
		}
	}
	root, name, registryPort := sitePreviewSite(site)
	unit := "allod-preview-" + sitePreviewSlug(name)
	if stop {
		// 'systemctl --user stop' exits 5 on a unit that was never loaded, so
		// the stop alone cannot tell that from a stop that was refused.
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
	var absent strings.Builder
	if sitePreviewRun("nix", []string{"eval", "--impure", "--raw", "--expr", fmt.Sprintf(sitePreviewAppExpr, root)}, &absent) != 0 {
		die(1, "could not evaluate the flake at %s", root)
	}
	if missing := strings.TrimSpace(absent.String()); missing != "" {
		die(1, "the flake at %s has no %s; docs/allod-site-preview.md in allod/tools has worked examples", root, missing)
	}
	if !sitePreviewActive(unit) {
		start := []string{"--user", "--collect", "--quiet", "--unit", unit, "--working-directory", root, "-E",
			fmt.Sprintf("ALLOD_PREVIEW_PORT=%d", port), "-E", "ALLOD_PREVIEW_INTERFACE=127.0.0.1", "--", "nix", "run", root + "#preview"}
		if sitePreviewRun("systemd-run", start, stderr) != 0 {
			die(1, "could not start %s", unit)
		}
	}
	sitePreviewWait(unit, port)
}

// sitePreviewSite resolves the site: its checkout root, the name it is refused
// and reported by, and its registry preview_port, zero when it has none. A
// site found from the current directory is looked back up in the registry, so
// standing in it and naming it by id reach one name, and so one unit name,
// which is what lets a '--stop' issued elsewhere name this unit.
func sitePreviewSite(site string) (root, name string, port int) {
	entries := registryEntries()
	if site != "" {
		entry, found := entries[site]
		if !found || entry.Checkout == "" {
			die(1, "unknown site: %s has no entry in %s", site, registryPath())
		}
		return filepath.Join(workDir(), entry.Checkout), site, entry.PreviewPort
	}
	root = resolveSiteRoot()
	for id, entry := range entries {
		if entry.Checkout != "" && filepath.Join(workDir(), entry.Checkout) == root {
			return root, id, entry.PreviewPort
		}
	}
	return root, strings.TrimPrefix(root, homeDir()+"/"), 0
}

func sitePreviewSlug(name string) string {
	return strings.Map(func(c rune) rune {
		if c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || strings.ContainsRune("._-", c) {
			return c
		}
		return '-'
	}, name)
}

func sitePreviewActive(unit string) bool {
	return sitePreviewRun("systemctl", []string{"--user", "is-active", "--quiet", unit}, io.Discard) == 0
}

// sitePreviewWait reports which of three things happened. The connection is
// attempted before the unit is asked about, so a server that answered in the
// instant its unit ended is still reported as serving; a unit that is up with
// a silent port is still starting, whether this run started it or found it.
func sitePreviewWait(unit string, port int) {
	address := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(sitePreviewTimeout)
	for {
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
		time.Sleep(sitePreviewPoll)
	}
}

const sitePreviewDetail = `'preview' hands the site's own 'preview' flake app to the user systemd manager
as the unit 'allod-preview-<slug>', waits up to 60 seconds for its port to
answer, then exits; the server keeps running under systemd until '--stop'.
A flake with no 'apps.<system>.preview' is refused, and nothing here knows
which tool builds the site.

The site is what <site> names, an id in the repository registry, or else the
checkout the current directory sits in; its port is that entry's
'preview_port', which '--port <n>' overrides, and a site with neither is
refused by name. <slug> is that id, or the checkout path relative to $HOME when
the registry has no entry for it, with every character outside [A-Za-z0-9._-]
replaced by '-'.

The server listens on 127.0.0.1 alone, so nothing off this machine reaches it;
'journalctl --user -u allod-preview-<slug>' shows its output, '-f' follows it.
Exit 0 prints the address, whether this run started the server or found one
already up; exit 1 means the unit is no longer running and its last log lines
are on standard error; exit 3 means the unit is up with the port still silent,
which is what a first build inside it looks like. '--stop' reports a preview
that is not running as such, not as an error.
`

// init registers the 'site' namespace and 'preview' in every build: this file
// carries no build tag. siteCommands in site_common.go says why the entry is
// added from here rather than a var literal, and why it prepends.
func init() {
	registerNamespace(namespace{name: "site", summary: "Preview a static site; deploy, check, config are present in site-tagged builds", main: siteMain})
	siteCommands = append([]siteCommand{{
		name:    "preview",
		summary: "Serve a site locally from the 'preview' app in its own flake",
		usage:   []string{"allod site preview [--port <n>] [--stop] [<site>]"},
		detail:  sitePreviewDetail,
		run:     sitePreview,
	}}, siteCommands...)
}
