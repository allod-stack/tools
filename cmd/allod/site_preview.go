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

func sitePreview(args []string) {
	port, stop, site := 0, false, ""
	for len(args) > 0 {
		switch args[0] {
		case "--port":
			if len(args) < 2 || strings.HasPrefix(args[1], "-") {
				siteCommandUsageError("preview", "--port requires a value")
			}
			number, ok := previewPortValue(args[1])
			if !ok || number == 0 {
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
	root, name, portText := sitePreviewSite(site)
	registryPort, ok := previewPortValue(portText)
	if !ok {
		die(1, "preview_port %s for %s in %s is not a whole number from 1024 to 65535", portText, name, registryPath())
	}
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

// A site found from the current directory is looked back up in the registry, so
// both routes to it reach one unit name, which is what lets a '--stop' elsewhere
// name the unit this machine started.
func sitePreviewSite(site string) (root, name, port string) {
	entries := registryEntries()
	if site != "" {
		entry, found := entries[site]
		if !found || entry.Checkout == "" {
			die(1, "unknown site: %s has no entry in %s", site, registryPath())
		}
		return filepath.Join(workDir(), entry.Checkout), site, string(entry.PreviewPort)
	}
	root = resolveSiteRoot()
	for id, entry := range entries {
		if entry.Checkout != "" && filepath.Join(workDir(), entry.Checkout) == root {
			return root, id, string(entry.PreviewPort)
		}
	}
	return root, strings.TrimPrefix(root, homeDir()+"/"), ""
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
sits in; the port is that entry's 'preview_port', which '--port <n>' overrides,
and <slug> is that id, or the checkout path under $HOME, with every character
outside [A-Za-z0-9._-] replaced by '-'.

The server listens on 127.0.0.1 alone, and its output is in the journal:
'journalctl --user -u allod-preview-<slug>', with '-f' to follow it.

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
		usage:   []string{"allod site preview [--port <n>] [--stop] [<site>]"},
		detail:  sitePreviewDetail,
		run:     sitePreview,
	}}, siteCommands...)
}
