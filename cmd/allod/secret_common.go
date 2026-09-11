package main

// The 'secret' namespace's shared plumbing: the command table type and
// dispatcher that select one of its subcommands, plus the checkout
// resolution 'declare' and every secret-tagged command share. None of this
// carries the 'secret' build tag.
//
// This mirrors site_common.go's split. secret_declare.go's init() registers
// the 'secret' namespace itself and adds the 'declare' entry to
// secretCommands; secret.go, which does carry the 'secret' tag, extends the
// same namespace with 'create' and 'rekey' rather than registering a second
// one — registerNamespace panics on a duplicate word, so only one file may
// call it, and secret_declare.go is that file.
//
// 'declare' writes a credential's non-secret half — the credentials.nix
// entry, the secrets.nix recipient line, and the rotation registry group —
// as plain text edits against the secrets checkout. It needs no identity,
// encrypts nothing, and runs wherever agents run, which is why it carries
// no tag while 'create' and 'rekey' do.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// secretCommand is one command inside the 'secret' namespace: the word that
// selects it from 'allod secret <command>', its one-line summary for the
// Commands: list, the Usage: lines it contributes, its detailed prose, and
// the function that runs it.
type secretCommand struct {
	name    string
	summary string
	usage   []string
	detail  string
	run     func(args []string)
}

// secretCommands is the secret namespace's own dispatch table, parallel to
// namespaces in main.go: a build carries exactly the entries this slice
// holds once every init() function has run. It is declared with no
// initializer, and every entry is added from an init() rather than a var
// literal, for the same compile-time initialization-cycle reason
// siteCommands documents in site_common.go: a secretCommand's run field
// holds a function whose body calls secretCommandHelp, which reads
// secretCommands back.
var secretCommands []secretCommand

// secretMain dispatches 'allod secret <command>' to whichever entry in
// secretCommands matches, so a build advertises and runs exactly the
// commands it carries. On an untagged build that is 'declare' alone:
// 'create' and 'rekey' fall through to the same "unknown secret command" a
// typo would, because in that build they are exactly as absent as a typo.
func secretMain(args []string) {
	if len(args) == 0 {
		fmt.Fprint(stderr, secretShortUsage())
		exit(1)
	}
	command, rest := args[0], args[1:]
	switch command {
	case "-h", "--help":
		fmt.Fprint(stdout, secretUsageText())
		return
	}
	for _, entry := range secretCommands {
		if entry.name == command {
			entry.run(rest)
			return
		}
	}
	fmt.Fprintf(stderr, "allod: unknown secret command: %s\n", command)
	fmt.Fprint(stderr, secretShortUsage())
	exit(1)
}

// secretUsageHeader renders the Usage: and Commands: blocks from
// secretCommands that the short and long usage forms share, so a build
// advertises exactly the commands it carries and no others.
func secretUsageHeader() string {
	var text strings.Builder
	text.WriteString("Usage:\n")
	for _, entry := range secretCommands {
		for _, line := range entry.usage {
			fmt.Fprintf(&text, "  %s\n", line)
		}
	}
	text.WriteString("\nCommands:\n")
	for _, entry := range secretCommands {
		fmt.Fprintf(&text, "  %-8s %s\n", entry.name, entry.summary)
	}
	return text.String()
}

// secretShortUsage is what a bare 'allod secret' and an unknown secret
// command print: the Usage: and Commands: blocks, with no command's detail
// prose, and a pointer to where that prose lives.
func secretShortUsage() string {
	return secretUsageHeader() + "\nRun 'allod secret --help' for details.\n"
}

// secretUsageText renders the long form of 'allod secret' usage: the same
// Usage: and Commands: blocks as secretShortUsage, followed by every
// registered command's own detail prose. Only 'allod secret --help' and
// 'allod secret -h' print this; a single command's own '-h'/'--help' prints
// secretCommandHelp for that command alone, not this.
func secretUsageText() string {
	var text strings.Builder
	text.WriteString(secretUsageHeader())
	for _, entry := range secretCommands {
		text.WriteString("\n")
		text.WriteString(entry.detail)
	}
	return text.String()
}

// secretCommandEntry looks up one command by name and reports whether it is
// registered.
func secretCommandEntry(name string) (secretCommand, bool) {
	for _, entry := range secretCommands {
		if entry.name == name {
			return entry, true
		}
	}
	return secretCommand{}, false
}

// secretCommandHelp renders one registered command's own usage lines
// followed by its own detail prose, and nothing about any other command.
// Every command's '-h'/'--help' case calls this with its own name rather
// than printing secretUsageText, so this is implemented once instead of
// once per command.
//
// name must be a name already registered in secretCommands — every caller
// passes its own secretCommand.name — so an unmatched name is a mistake in
// this program, not in its input.
func secretCommandHelp(name string) string {
	entry, ok := secretCommandEntry(name)
	if !ok {
		panic("secretCommandHelp: unregistered secret command: " + name)
	}
	var text strings.Builder
	text.WriteString("Usage:\n")
	for _, line := range entry.usage {
		fmt.Fprintf(&text, "  %s\n", line)
	}
	text.WriteString("\n")
	text.WriteString(entry.detail)
	return text.String()
}

// secretCommandUsageError reports one argument-parsing error inside a
// secret command the way 'gh' does: the one-line message, then that
// command's own Usage: lines, then a pointer to its own '--help', then
// exit 1.
//
// name must be a name already registered in secretCommands, for the same
// reason secretCommandHelp requires it.
func secretCommandUsageError(name string, format string, args ...any) {
	entry, ok := secretCommandEntry(name)
	if !ok {
		panic("secretCommandUsageError: unregistered secret command: " + name)
	}
	fmt.Fprintf(stderr, "allod: "+format+"\n", args...)
	fmt.Fprint(stderr, "Usage:\n")
	for _, line := range entry.usage {
		fmt.Fprintf(stderr, "  %s\n", line)
	}
	fmt.Fprintf(stderr, "\nRun 'allod secret %s --help' for details.\n", name)
	exit(1)
}

// --- The checkout ---

// resolveSecretsCheckout finds the secrets repository the way rotate-token
// does: the repository registry's checkout for it under ~/work, or the
// conventional allod/secrets when the registry does not name one. An
// explicit path — a worktree, typically — wins.
func resolveSecretsCheckout(arg string) string {
	path := arg
	if path == "" {
		path = filepath.Join(workDir(), secretsCheckoutRelative())
	}
	checkout := resolveGitRepo(path)
	for _, file := range []string{"flake.nix", "secrets.nix", "credentials.nix"} {
		if info, err := os.Stat(filepath.Join(checkout, file)); err != nil || !info.Mode().IsRegular() {
			die(1, "%s is not a secrets checkout: no %s", checkout, file)
		}
	}
	return checkout
}

func secretsCheckoutRelative() string {
	const fallback = "allod/secrets"
	for _, alias := range []string{"secrets", "allod/secrets"} {
		if checkout, ok := registryCheckout(alias); ok {
			return checkout
		}
	}
	return fallback
}

// inventoryCheckout resolves the inventory checkout the way
// resolveSecretsCheckout resolves the secrets one when no override is
// given: the repository registry's checkout for it under ~/work, or the
// conventional allod/inventory when the registry does not name one. The
// INVENTORY environment variable, mirroring nexus's rotate-token script,
// always wins.
func inventoryCheckout() string {
	if value := os.Getenv("INVENTORY"); value != "" {
		// Trimming trailing slashes turns the root into the empty string,
		// and the nix command would then run in the process working
		// directory instead of /, so the root is special-cased back.
		// Not filepath.Clean: it resolves '..' lexically, so a path
		// through a symlink ('/base/link/../inventory', link -> /srv/x)
		// would name a different directory than the one the OS reaches.
		trimmed := strings.TrimRight(value, "/")
		if trimmed == "" {
			return "/"
		}
		return trimmed
	}
	return filepath.Join(workDir(), inventoryCheckoutRelative())
}

func inventoryCheckoutRelative() string {
	const fallback = "allod/inventory"
	for _, alias := range []string{"inventory", "allod/inventory"} {
		if checkout, ok := registryCheckout(alias); ok {
			return checkout
		}
	}
	return fallback
}

// --- nix eval ---

// nixEvalJSON runs the checkout as its working directory and names the
// attribute as 'path:.#<attribute>', so the path itself never has to
// survive flake-reference or Nix-expression quoting; a checkout under a
// directory with a space in its name evaluates the same as any other. It
// lives here, untagged, because 'declare' needs it (to read a machine's
// type from the inventory flake) as much as 'create' and 'rekey' do (to
// read the secrets flake's credential inventory and rotation registry).
func nixEvalJSON(checkout, attribute string) ([]byte, error) {
	var out, errOut bytes.Buffer
	cmd := exec.Command("nix", "eval", "--json", "path:.#"+attribute)
	cmd.Dir = checkout
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(errOut.String()))
	}
	return out.Bytes(), nil
}
