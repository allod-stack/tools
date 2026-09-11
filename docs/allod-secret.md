# allod secret

A credential has a non-secret half and a secret half, with different authors. `allod secret declare <name>` writes the non-secret half — three entries across `credentials.nix`, `secrets.nix`, and `forgejo-token-groups.json` — so an agent generates that PR instead of transcribing it by hand from the secrets template's README. `allod secret create <name>` then lands the secret half: it encrypts the value and flips the entry to `active`. `allod secret rekey <name>` re-encrypts an existing credential after its recipient list changes. `create` and `rekey` run only on the machine that holds the age identity, because the program is built with the `secret` tag only there; everywhere else those two commands are unknown, and `declare` alone is present. The plaintext `create` and `rekey` handle is read from stdin or one hidden terminal line, goes to `age` over a pipe, and is never written to disk, printed, or placed on a command line; `declare` writes no ciphertext and reads no identity at all.

## declare

`allod secret declare <name> --kind <kind> --owner <owner> --to <machine>[,<machine>...] --format <format> --deployed-path <path> --verify <probe> [--service none|forgejo] [--account <account>] [--ui-token-name <name>] [--strategy overlap|in-place]` appends the `credentials.nix` entry in `rotation_state = "pending"` (the `agent-pr-token` layout), the `secrets.nix` recipient line (`[ hostKey ] ++ vmKeys "<vm>"` per target machine, or `[ hostKey ]` alone when the one target is `nexus`), and a `forgejo-token-groups.json` rotation registry group whose `service` is `none` or `forgejo`. Every insertion is one contiguous block, so the diff an agent runs is a small, reviewable one.

An agent runs `declare`, reviews the diff it leaves, and opens the PR; the human then checks out that branch and runs `create` to land the secret half, exactly as described below. `declare` never commits.

`--kind` is the credential's own kind (`user`, `machine-host`, `forge-git`, `agent`, or `service` — the enum `credential-inventory` enforces), not a target machine's kind. Target kind is derived per `--to` machine rather than typed: `nexus` is the host and gets `nixos-host`; every other name is looked up in the inventory's VM registry (`~/work/allod/inventory/scripts/vm-specs.json` by default, or `$INVENTORY` when set) and refused if absent there. That registry's guest entries carry no `type` field — it is validated inside the inventory flake but filtered out of the generated JSON — so `declare` derives `dev-vm` from a non-empty `repos` list and `privacy-vm` from an empty one, the one distinguishing field the committed public template still carries. A private fork whose machines no longer follow that convention needs a real fix upstream; this is the best signal available in `vm-specs.json` today.

`declare` checks all three files for the name before writing anything: an existing `credentials.nix` entry, a `secrets.nix` line for `secrets/<name>.age`, or a registry group keyed `<name>` or naming `<name>` among its credentials refuses, listing every match found, and nothing is written. After building all three edits it re-reads each as its own format — JSON parses, the `.nix` files have balanced braces outside strings and comments — and refuses, restoring whatever it already wrote, if any of them would not. On success it prints the three paths it changed, one per line, and nothing else.

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

All three commands resolve the secrets checkout the same way: `~/work/<checkout>` where `<checkout>` is the `allod/secrets` entry of the inventory's repository registry, or `allod/secrets` when the registry does not name one. An optional trailing argument names a checkout or worktree instead. `declare` additionally resolves the inventory checkout to read `scripts/vm-specs.json`: `~/work/allod/inventory`, or `$INVENTORY` when set. The identity `create` and `rekey` use is `$AGE_IDENTITY`, or `~/.ssh/host`; its `.pub` must be among the recipients. `declare` reads no identity.

## Build

```nix
buildGoModule {
  subPackages = [ "cmd/allod" ];
  tags = [ "site" "secret" ];
}
```

`declare` needs no `secret` tag and is present in every build; `create` and `rekey` need it, so it is set wherever the age identity lives. The command runs `nix`, `age`, and `git` from the operator's PATH; `declare` needs none of the three.
