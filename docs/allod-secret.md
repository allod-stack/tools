# allod secret

A credential has a non-secret half and a secret half, with different authors. `allod secret declare <name>` writes the non-secret half — three entries across `credentials.nix`, `secrets.nix`, and `forgejo-token-groups.json` — so an agent generates that PR instead of transcribing it by hand from the secrets template's README. `allod secret create <name>` then lands the secret half: it encrypts the value and flips the entry to `active`. `allod secret rekey <name>` re-encrypts an existing credential after its recipient list changes. `create` and `rekey` run only on the machine that holds the age identity, because the program is built with the `secret` tag only there; everywhere else those two commands are unknown, and `declare` alone is present. The plaintext `create` and `rekey` handle is read from stdin or one hidden terminal line, goes to `age` over a pipe, and is never written to disk, printed, or placed on a command line; `declare` writes no ciphertext and reads no identity at all.

## declare

`allod secret declare <name> --kind <kind> --owner <owner> --to <machine>[,<machine>...] --format <format> --deployed-path <path> --verify <probe> [--service none|forgejo] [--account <account>] [--ui-token-name <name>] [--strategy overlap|in-place]` appends the `credentials.nix` entry in `rotation_state = "pending"` (the `agent-pr-token` layout), the `secrets.nix` recipient line (`[ hostKey ] ++ vmKeys "<vm>"` per target machine, or `[ hostKey ]` alone for a target whose derived kind is `nixos-host`), and a `forgejo-token-groups.json` rotation registry group whose `service` is `none` or `forgejo`. Every insertion is one contiguous block, so the diff an agent runs is a small, reviewable one.

An agent runs `declare`, reviews the diff it leaves, and opens the PR; the human then checks out that branch and runs `create` to land the secret half, exactly as described below. `declare` never commits.

`--kind` is the credential's own kind (`user`, `machine-host`, `forge-git`, `agent`, or `service` — the enum `credential-inventory` enforces), not a target machine's kind. Target kind is derived per `--to` machine rather than typed, by reading `machines.<name>.type` straight from the inventory flake (`nix eval --json <inventory-checkout>#machines.<name>.type`, archetypes' own consumption path) and mapping it: `dev` → `dev-vm`, `privacy` → `privacy-vm`, `hypervisor` → `nixos-host`, `service` → `service-vm`. There is no literal `nexus` special case — `nexus`'s own type is `hypervisor`, so it reaches `nixos-host` through the same lookup as every other machine. A machine the eval cannot find, a type outside those four, or a failed `nix` run is refused, naming the inventory checkout, the machine, and the accepted types.

`<name>` must match `^[a-z][a-z0-9-]*$` — a leading digit is refused, since the name becomes an unquoted nix attribute key and `1token = { ... }` does not parse. `--owner` and, when given, `--kind`, `--account`, and `--ui-token-name` must match `^[A-Za-z0-9][A-Za-z0-9_.@-]*$`, and `--deployed-path` must be an absolute path with no whitespace, control character, quote, backslash, or `$`. These are the values that reach generated nix or JSON text; each is checked against its shape before anything is built, not interpolated raw, so a value crafted to break out of its quoting (`--owner 'foo"; kind = "bar'`, say) is refused as a usage error rather than landing in a file.

`declare` checks all three files for the name before writing anything: an existing `credentials.nix` entry, a `secrets.nix` line for `secrets/<name>.age`, or a registry group keyed `<name>` or naming `<name>` among its credentials refuses (matching across line breaks, so a declaration split across lines cannot slip past the check), listing every match found, and nothing is written. All three edits are then built and validated in memory — JSON parses, the `.nix` files have balanced braces outside strings and comments — before anything is written. Writing itself replaces one file at a time, atomically (a temp file in the same directory, renamed over the original, so a failed write never leaves a truncated file): immediately before each replacement, `declare` re-reads that file and refuses, restoring every file already replaced, if its bytes no longer match what was read at the start — a concurrent edit is refused rather than silently discarded. On success it prints the three paths it changed, one per line, and nothing else.

## The landing, in two halves

`declare`'s PR is green on its own, because the inventory check accepts `pending` and requires the ciphertext to be absent in that state.

The operator then checks out that branch in the secrets checkout and runs one line at the host terminal:

```
producer-of-the-value | allod secret create <name>
```

or, from a terminal with nothing piped, `allod secret create <name>` and paste the value at the hidden prompt. The command verifies the three entries agree with each other and with the name it was given, encrypts the value to exactly the recipients `secrets.nix` declares, writes `secrets/<name>.age`, flips the entry to `active`, runs `nix flake check`, and commits and pushes the branch. Merging is a separate act.

## What create refuses

Every refusal names the file that disagrees and what it holds. The command touches nothing until every check below passes, and after it starts writing, a failed check restores both files.

- The checkout is on its default branch, or has uncommitted or untracked changes. A leftover `.age` from a failed attempt counts as untracked and is named.
- No `credentials.nix` entry has that name, or the entry is not `pending`, or it does not have exactly one `agenix` consumer in this repository.
- `secrets.nix` does not declare the consumer's path, lists no recipients for it, or lists recipients that do not include this host's identity, which would make a ciphertext the host could never rekey or rotate.
- No rotation registry entry names the credential, or the one that does names a different path.
- The ciphertext already exists.
- The value is empty or whitespace only.
- `age` fails, or produces something that is not an age file.
- The repository's checks fail after the write. Both files are restored and the check's exit status is returned.

A push failure after the commit is reported with the commit kept; the landing happened and the publication did not.

## rekey

`allod secret rekey <name>` is for a recipient change: a machine added to or removed from a `secrets.nix` line. It decrypts the existing ciphertext with the host identity, re-encrypts to the recipients the file declares now, and always rewrites the file. `agenix -e` skips a re-encryption whose plaintext did not change, which is how a recipient-only edit used to exit 0 and change nothing; this command has no such comparison. It refuses a `pending` entry, a failed decryption, and a value that decrypts to nothing.

Rotating to a new value is `rotate-token`'s job and is not part of this namespace.

## Where things are found

All three commands resolve the secrets checkout the same way: `~/work/<checkout>` where `<checkout>` is the `allod/secrets` entry of the inventory's repository registry, or `allod/secrets` when the registry does not name one. An optional trailing argument names a checkout or worktree instead. `declare` additionally resolves the inventory checkout to evaluate machine types, the same way when no override is given: `~/work/<checkout>` where `<checkout>` is the `allod/inventory` entry of the repository registry, or `allod/inventory` when the registry does not name one; `$INVENTORY`, when set, always wins. The identity `create` and `rekey` use is `$AGE_IDENTITY`, or `~/.ssh/host`; its `.pub` must be among the recipients. `declare` reads no identity.

## Build

```nix
buildGoModule {
  subPackages = [ "cmd/allod" ];
  tags = [ "site" "secret" ];
}
```

`declare` needs no `secret` tag and is present in every build; `create` and `rekey` need it, so it is set wherever the age identity lives. The command runs `nix`, `age`, and `git` from the operator's PATH; `declare` needs `nix` (to read machine types) and `git` (to resolve the checkout), but never `age`.
