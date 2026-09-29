package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Replacing the process leaves no child for allod to watch: ssh owns the
// forward's whole lifetime and Ctrl-C reaches ssh itself.
var sitePreviewExec = func(argv []string) {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		die(1, "'%s' not found on PATH", argv[0])
	}
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		die(1, "could not run %s: %s", argv[0], err)
	}
}

func siteView(args []string) {
	port, vm, site, _ := siteFlags("view", args)
	if site == "" {
		siteListMissingID("view", "site view needs a site id", true)
	}
	entry, found := registryEntries()[site]
	if !found {
		siteListUnknown(site, true)
	}
	if port == 0 {
		if port = previewPort(site, entry); port == 0 {
			die(1, "no preview port for %s: give its entry in %s a preview_port, or pass --port", site, registryPath())
		}
	}
	if vm == "" {
		machines := vmsWithRepo(site)
		if len(machines) != 1 {
			die(1, "%d machines in %s list %s (%s); name one with --vm",
				len(machines), vmSpecsPath(), site, strings.Join(machines, " "))
		}
		vm = machines[0]
	}
	// The remote start prints the VM's own loopback address, which means nothing
	// here until the forward is up, so only its diagnostics are shown.
	if status := sitePreviewRemote(vm, site, port, false, io.Discard); status != 0 {
		exit(status)
	}
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port)
	fmt.Fprintf(stdout, "http://127.0.0.1:%d\n", port)
	sitePreviewExec([]string{"ssh", "-N", "-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3",
		"-L", forward, "--", vm})
}

const siteViewDetail = `'view' opens a site being served in a VM in this machine's browser. It makes sure
the server is started there, prints the address, and then becomes the ssh that
forwards the VM's port to the same port here, so the browser reaches
http://127.0.0.1:<port>. The VM must run a build of allod that has 'site serve'.

The port and the VM both come from the inventory beside this machine: the site's
'preview_port', which '--port <n>' overrides, and the one machine in
vm-specs.json whose repository list holds the site, which '--vm <name>' overrides
and which must be given when none or several do.

Ctrl-C, or closing the terminal, ends the forward and nothing else: the port here
closes and the server in the VM keeps running. Stop that server with
'allod site serve --stop --vm <name> <site>'.
`

func init() {
	siteCommands = append(siteCommands, siteCommand{
		name:    "view",
		summary: "Forward a site served in a VM to this machine's browser",
		usage:   []string{"allod site view [--vm <name>] [--port <n>] <site>"},
		detail:  siteViewDetail,
		run:     siteView,
	})
}
