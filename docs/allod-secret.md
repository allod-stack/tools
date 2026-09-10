# allod secret

`allod secret create <name>` lands an encrypted credential in the secrets repository, and `allod secret rekey <name>` re-encrypts one after its recipient list changes. Both run only on the machine that holds the age identity, because the program is built with the `secret` tag only there; everywhere else the namespace does not exist. The plaintext is read from stdin or one hidden terminal line, goes to `age` over a pipe, and is never written to disk, printed, or placed on a command line.

## The landing, in two halves

A credential has a non-secret half and a secret half, with different authors.

An agent writes the non-secret half as one reviewable PR against the secrets repository: the `credentials.nix` entry with `rotation_state = "pending"`, the `secrets.nix` recipient line, the rotation registry entry in `forgejo-token-groups.json`, and whatever consumer wiring the credential needs. That PR is green on its own, because the inventory check accepts `pending` and requires the ciphertext to be absent in that state.

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

The secrets checkout is `~/work/<checkout>` where `<checkout>` is the `allod/secrets` entry of the inventory's repository registry, or `allod/secrets` when the registry does not name one. An optional second argument names a checkout or worktree instead. The identity is `$AGE_IDENTITY`, or `~/.ssh/host`; its `.pub` must be among the recipients.

## Build

```nix
buildGoModule {
  subPackages = [ "cmd/allod" ];
  tags = [ "site" "secret" ];
}
```

The command runs `nix`, `age`, and `git` from the operator's PATH.
