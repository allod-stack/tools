package main

// exitActiveRunScope is unused by every other refusal, so a caller can tell
// "something is still running inside this clone" apart from the rest.
const exitActiveRunScope = 13

// Outside commands hold this lock until exit; an active worker makes them refuse.
func requireExclusiveLedgerLock(parent string, entry changeLedgerEntry) *changeLedgerLock {
	path, ok := ledgerFilePath(parent, entry)
	if !ok {
		die(1, "could not resolve the ledger file for '%s' to lock it", entry.path)
	}
	lock, ok := lockLedgerExclusive(path)
	if !ok {
		die(exitActiveRunScope,
			"clone '%s' is locked, almost certainly by a 'run' still sandboxing a command inside it; outside commands act on a clone only when nothing is running inside it, so this refuses until it stops",
			entry.path)
	}
	return lock
}
