package main

// The 'trace' namespace turns local agent session logs (Claude Code, Codex,
// Pi) into small redacted markdown traces, so an hourly timer elsewhere can
// commit them to a private repository before a dev VM reprovision discards
// the originals, and a weekly skill can read them back against the memory
// index. It reads only; it never mutates a session store and never touches
// git itself.
//
// This file carries no build tag: every machine that runs agent sessions
// can distill them, unlike 'secret's landing commands or 'site's deploy
// path, which need a credential or a hosting account only some machines
// hold.

import "fmt"

func init() {
	registerNamespace(namespace{
		name:    "trace",
		summary: "Distill local Claude, Codex, and Pi session logs into redacted markdown traces",
		main:    traceMain,
	})
}

const traceUsageText = `Usage:
  allod trace distill --out <dir> [--harness <name>[,<name>...]] [--quiet]

'--out <dir>' is required: the root traces are written under, as
<out>/<machine>/<harness>/<YYYY-MM-DD>-<session-id>.md.

'--harness' selects which stores to read: 'claude', 'codex', 'pi', comma-
separated and/or repeatable ('--harness claude,codex' and '--harness claude
--harness codex' name the same set). Default: all three.

'--quiet' suppresses the summary line on stdout.

A missing store, an unreadable file, or a file this command does not
recognise is skipped with one line on stderr; none of those is a failure.
A file is rewritten only when its distilled content changed, so a finished
session is left alone on later runs.
`

func traceMain(args []string) {
	if len(args) == 0 {
		fmt.Fprint(stderr, traceUsageText)
		exit(1)
	}
	command, args := args[0], args[1:]
	switch command {
	case "distill":
		traceDistill(args)
	case "-h", "--help":
		fmt.Fprint(stdout, traceUsageText)
	default:
		die(1, "unknown trace command: %s", command)
	}
}
