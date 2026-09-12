# allod secret

A credential has a non-secret half and a secret half, with different authors. `allod secret declare <name>` writes the non-secret half — three entries across `credentials.nix`, `secrets.nix`, and `forgejo-token-groups.json` — so an agent generates that PR instead of transcribing it by hand from the secrets template's README. `allod secret create <name>` then lands the secret half: it encrypts the value and flips the entry to `active`. `allod secret rotate <name>` replaces that value later, across every credential its rotation group shares it with. `allod secret rekey <name>` re-encrypts an existing credential after its recipient list changes. `allod secret migrate <name>` is a one-shot bridge for a credential declared before this shape existed. All four run only on the machine that holds the age identity, because the program is built with the `secret` tag only there; everywhere else they are unknown commands, and `declare` alone is present. The plaintext they handle is read from stdin or one hidden terminal line, goes to `age` over a pipe, and is never written to disk, printed, or placed on a command line; `declare` writes no ciphertext and reads no identity at all.

## The value a credential stores

A credential's plaintext is a template with the secret substituted into it. The template is the non-secret half — a user name, a host, the lines around a password — and it lives in the rotation registry, where it is reviewed like any other text:

```json
{
  "credential": "forgejo-https-token-allod-dev",
  "secret_path": "secrets/forgejo-https-token-allod-dev.age",
  "value": { "template": "https://allod-agent:{secret}@forge.anarch.diy" },
  "targets": [
    { "system": "allod-dev", "kind": "dev-vm", "deployed_path": "/root/.git-credentials",
      "verify": "sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote https://forge.anarch.diy/allod/tools.git HEAD" }
  ]
}
```

Three rules govern it:

- **Omit `value` when the plaintext is the secret itself.** Most credentials are a bare token, and a bare token needs no declaration.
- **A `value.template` contains exactly one `{secret}`.** Rendering replaces that one placeholder and preserves every other byte, including whether the template ends in a newline. Zero placeholders or two is refused.
- **A template is UTF-8 text, and every byte of it is preserved.** It is stored as a JSON string, and JSON carries UTF-8 and nothing else, so a template that is not valid UTF-8 is refused rather than stored with the invalid bytes silently replaced. `declare` refuses it at the input, and `migrate` refuses a legacy plaintext whose non-secret half is not UTF-8.
- **`value.encode` names a transformation applied to the secret alone**, before substitution, and must be one the secrets checkout exports as `lib.credentialEncodings`. Today that list holds `rclone-obscure`, for the rclone remote stanza whose password is stored obscured. The list lives in the checkout, not in this command, so adding an encoding is a change to the secrets flake; an exported name this build cannot actually perform is refused rather than silently stored unencoded.

**The secret is used verbatim.** Nothing trims it, and a trailing newline on a piped value is part of the value. Use `printf '%s' "$value" | allod secret create <name>` when it should not be. This is the same rule `create` has always documented, now applied to `rotate` as well: the command that encrypts does not guess which byte was meant, and a consumer handed something it cannot use fails at its own boundary where the failure is visible.

Each target carries `verify`: one non-empty command line, printed verbatim by `rotate` for a target on the machine it names, and wrapped as `ssh <system> <command>` for any other. Whatever the command needs — a repository URL, a user, a path — belongs inside the string; there are no separate registry fields for it. The four commands `rotate-token` used to build from a probe type are just four such strings:

```
sudo env HOME=/root GIT_TERMINAL_PROMPT=0 git ls-remote <url> HEAD
sudo -u <user> forge token verify < <path>
allod site check
tailscale status --peers=false
```

## Coexistence with the old shape

A credential is wholly new (`value` absent or present, `verify` a string on every target) or wholly legacy (a `format` name plus a structured `verify` object on every target). One credential carrying both is refused by every command here, and by the secrets flake's own `credential-registry` check. A registry may hold some of each while the migration runs.

`rotate` and `create` refuse a legacy credential, and `rotate` refuses a group with a legacy member. What the refusal names depends on the format. A `credential-store-url` or an `rclone-remote-stanza` is a container with non-secret text around the secret, so it names `allod secret migrate <name>`. Any other legacy format stores the secret on its own, so there is no container to take apart: the refusal says the entry is a plain legacy value that becomes new-shape by a registry edit that drops its `format` and turns each target's `verify` object into its command string, with nothing decrypted. Until that edit lands, `rotate` refuses it outright; there is no fallback that still rotates it.

## declare

`allod secret declare <name> --kind <kind> --owner <owner> --to <machine>[,<machine>...] --deployed-path <path> --verify <command> [--value-template-file <path>|--value-template-stdin] [--value-encode <name>] [--service none|forgejo] [--account <account>] [--ui-token-name <name>] [--strategy overlap|in-place]` appends the `credentials.nix` entry in `rotation_state = "pending"` (the `agent-pr-token` layout), the `secrets.nix` recipient line (`[ hostKey ] ++ vmKeys "<vm>"` per target machine, or `[ hostKey ]` alone for a target whose derived kind is `nixos-host`), and a `forgejo-token-groups.json` rotation registry group whose `service` is `none` or `forgejo`. Every insertion is one contiguous block, so the diff an agent runs is a small, reviewable one.

An agent runs `declare`, reviews the diff it leaves, and opens the PR; the human then checks out that branch and runs `create` to land the secret half, exactly as described below. `declare` never commits.

`--kind` is the credential's own kind, not a target machine's kind, and must be one of `user`, `agent`, `service`. `credential-inventory`'s other two kinds, `forge-git` and `machine-host`, are refused: the shape `declare` writes (`public_key` null, one `agenix` consumer) is exactly what the inventory check rejects for them — a `forge-git` entry needs a real `public_key` and one `forge-key-secret` plus one `forgejo-ssh` consumer, and `machine-host` entries are generated by `hostEntries`, never hand-declared. Target kind is derived per `--to` machine rather than typed, by reading `machines.<name>.type` straight from the inventory flake (`nix eval --json <inventory-checkout>#machines.<name>.type`, archetypes' own consumption path) and mapping it: `dev` → `dev-vm`, `privacy` → `privacy-vm`, `hypervisor` → `nixos-host`, `service` → `service-vm`. There is no literal `nexus` special case — `nexus`'s own type is `hypervisor`, so it reaches `nixos-host` through the same lookup as every other machine. A machine the eval cannot find, a type outside those four, or a failed `nix` run is refused, naming the inventory checkout, the machine, and the accepted types.

`<name>` must match `^[a-z][a-z0-9-]*$` and must not be a nix keyword (`let`, `in`, `with`, `rec`, `assert`, `if`, `then`, `else`, `inherit`, `or`) — it becomes an unquoted nix attribute key, and none of those parse as one (`1token = { ... }` and `let = { ... }` are both syntax errors). `--owner`, every `--to` machine name, and, when given, `--kind`, `--account`, and `--ui-token-name` must match `^[A-Za-z0-9][A-Za-z0-9_.@-]*$`, and `--deployed-path` must be an absolute path with no whitespace, control character, quote, backslash, or `$`. These are the values that reach generated nix or JSON text; each is checked against its shape before anything is built, not interpolated raw, so a value crafted to break out of its quoting (`--owner 'foo"; kind = "bar'`, say) is refused as a usage error rather than landing in a file.

`--verify` is the one command line every target carries, described under "The value a credential stores" above. It must be non-empty and hold no line break; `declare` checks nothing else about it, because `rotate` prints it for a human to run rather than interpolating it into any text of its own.

The value template is optional and is given as bytes, not as a flag value: `--value-template-file <path>` reads it from a file and `--value-template-stdin` from standard input, and the two are mutually exclusive. The bytes are stored exactly as read, with no trimming, so whether the template ends in a newline is part of the declaration. It must contain exactly one `{secret}` and must be valid UTF-8, since it is stored as a JSON string. `--value-encode <name>` requires a template and must name an encoding the checkout exports as `lib.credentialEncodings`; `declare` evaluates that export rather than carrying a list of its own.

`declare` checks the name against four sources before writing anything: an existing `credentials.nix` entry, a `secrets.nix` line for `secrets/<name>.age`, a registry group keyed `<name>` or naming `<name>` among its credentials (all three matched across line breaks and nix line comments, so a declaration split across lines or separated by a comment from its `= {` cannot slip past the check), and the secrets checkout's own evaluated credential inventory (`<checkout>#lib.credentials`). The fourth exists because `credentials.nix` is `hostEntries // activeEntries // stagedEntries // forgeGitEntries // { ...literal entries...; }`: a name one of the generated attrsets already produces is invisible to every textual check, and the literal entry `declare` would append there would silently shadow it (the last `//` operand wins in nix). A failed evaluation refuses outright rather than relying on the textual checks alone. Every match found across all four is listed, and nothing is written.

All three edits are then built and validated in memory — JSON parses, the `.nix` files have balanced braces outside strings and comments — before anything is written. Writing itself replaces one file at a time, atomically (a temp file in the same directory, renamed over the original, so a failed write never leaves a truncated file): immediately before each replacement, `declare` re-reads that file and refuses, restoring every file already replaced, if its bytes no longer match what was read at the start — a concurrent edit is refused rather than silently discarded. On success it prints the three paths it changed, one per line, and nothing else.

## The landing, in two halves

`declare`'s PR is green on its own, because the inventory check accepts `pending` and requires the ciphertext to be absent in that state.

The operator then checks out that branch in the secrets checkout and runs one line at the host terminal:

```
producer-of-the-value | allod secret create <name>
```

or, from a terminal with nothing piped, `allod secret create <name>` and paste the value at the hidden prompt. The command verifies the three entries agree with each other and with the name it was given, encrypts the value to exactly the recipients `secrets.nix` declares, writes `secrets/<name>.age`, flips the entry to `active`, runs `nix flake check`, and commits and pushes the branch. Merging is a separate act. Every file any of these commands replaces is written to a temp file in the same directory and renamed over the original, so a file on disk is always its old bytes in full or its new bytes in full.

## What create refuses

Every refusal names the file that disagrees and what it holds. The command touches nothing until every check below passes, and after it starts writing, a failed check restores both files.

- The checkout is on its default branch, or has uncommitted or untracked changes. A leftover `.age` from a failed attempt counts as untracked and is named.
- No `credentials.nix` entry has that name, or the entry is not `pending`, or it does not have exactly one `agenix` consumer in this repository.
- `secrets.nix` does not declare the consumer's path, lists no recipients for it, or lists recipients that do not include this host's identity, which would make a ciphertext the host could never rekey or rotate.
- No rotation registry entry names the credential, the one that does names a different path, or the credential has more than one entry — two entries in one group, or one in each of two groups. A command acts on exactly one entry, so a second entry of the same name would be left behind.
- The ciphertext already exists.
- The value is empty or whitespace only.
- The registry entry still carries a legacy `format`, or carries both a `format` and a `value`. The first names whichever route suits that format (see "Coexistence with the old shape").
- The declared `value.encode` needs an encoder that is not on PATH. This is checked before the value is read, so a missing `rclone` costs a refusal rather than a pasted password. The encoder's own output is never quoted into an error either: a process handed a secret on stdin can echo it, so only the exit status is reported.
- The declared value is unusable: its template has no `{secret}` or more than one, or its `value.encode` is not exported by the checkout.
- `age` fails, or produces something that is not an age file.
- The repository's checks fail after the write. Both files are restored and the check's exit status is returned.

A push failure after the commit is reported with the commit kept; the landing happened and the publication did not.

## rekey

`allod secret rekey <name>` is for a recipient change: a machine added to or removed from a `secrets.nix` line. It decrypts the existing ciphertext with the host identity, re-encrypts to the recipients the file declares now, and always rewrites the file. `agenix -e` skips a re-encryption whose plaintext did not change, which is how a recipient-only edit used to exit 0 and change nothing; this command has no such comparison. It refuses a `pending` entry, a failed decryption, and a value that decrypts to nothing.

## rotate

`allod secret rotate <name>` replaces a value. The unit is the registry group that lists <name> among its credentials: every credential in that group is re-encrypted from the one value read on stdin, the way `rotate-token --group` used to rotate a shared Forgejo token. Each one is rendered from its own declared template, so a group can mix a plain token and a URL around the same secret; a group whose members disagree on `value.encode` is refused, because one value cannot be both a password to obscure and a token to store.

Nothing is decrypted. Every credential in the group must already be `active` with a ciphertext on disk, and each new ciphertext is built in memory, written by temp-file-and-rename so a file is always old or new bytes, and gated by one `nix flake check` before one commit and push. A failed check restores every ciphertext and creates no commit. A push failure keeps the commit, still prints every step, and exits non-zero.

`--dry-run` runs every gate — branch, clean tree, active state, ciphertext present, recipients, registry shape, unique group membership, declared value and verification — reads no value, decrypts nothing, encrypts nothing, runs no checks, and writes nothing. It still refuses a dirty tree. It prints the group, its targets, the deploy and verification steps, and the revocation gate, phrased as what a live run would do.

The printed steps come from the registry: the rebuild command per target kind, the verification command per target, the revocation gate with Forgejo wording only for a `forgejo` group and none at all for an `in-place` strategy, and — when the group carries `local_auth_refresh` entries — `refresh-local-auth --group <alias>` as the operator's next step. That stays a separate host script, nexus's own `refresh-local-auth`: it installs root-owned files under sudo, a privilege a git-repository command should not hold. A group with such an entry also requires `refresh-local-auth` to resolve on PATH, checked before the value is read and on `--dry-run` too, so a host whose nexus pin predates allod/nexus#52 is refused before a live run lands a rotation it cannot finish.

## migrate

`allod secret migrate <name>` is run once per credential that predates the value template, and only by the operator at the host. It rewrites that credential's registry entry: `format` becomes a `value.template`, each target's structured `verify` object becomes its command string, and the legacy `user` field disappears into that string — `user` was only ever consumed by a `forge-token-verify` probe, so a `user` on a target of any other type is refused rather than dropped. The ciphertext is not rewritten, so nothing a machine deploys changes; the commit holds `forgejo-token-groups.json` and nothing else, and every other byte of that file is left as it was.

It decrypts once, in memory, and only after every check that does not need the plaintext has passed. The decrypted bytes must match the exact byte shape `rotate-token` writes for that format; substituting the extracted secret into the proposed template must reproduce those bytes exactly; the extracted bytes must occur nowhere else in the template, so a token that happens to also appear in the host name is refused rather than published; and the template that comes out is validated the same way a hand-written one is, so a legacy host containing a literal `{secret}` is refused even though it round-trips perfectly. A failed check restores the registry bytes exactly and creates no commit; a failed push keeps the commit and says so.

Everything that can be settled from registry text alone is settled before the decrypt, the encoder export included: a checkout that no longer exports `rclone-obscure` refuses an rclone migration without opening the ciphertext. The structured `verify` object is decoded with unknown fields refused, and every field that reaches the command — `repo_url`, `user`, `deployed_path` — must be inside the grammar `declare` holds the same field to. The command becomes a line an operator runs in a shell, and the legacy shape never constrained those fields; a value outside the grammar is refused, naming the target and asking for that command to be written into the registry by hand.

For an rclone stanza the migrated declaration carries `"encode": "rclone-obscure"` even though the stored value is the already-obscured password: the encoder describes what the next `rotate` does, which is take a raw password and obscure it.

`migrate` refuses a credential that already has a `value`, one in no registry group, in two groups, or listed twice inside one group, and any legacy format other than the two containers. A plain legacy entry needs no decryption at all: edit the registry directly, dropping `format` and turning each target's `verify` object into its command string.

A credential that is the `source_credential` of a `local_auth_refresh` entry in any group is refused unless it is a `credential-store-url` container — an `rclone-remote-stanza` can never render a netrc line — and `refresh-local-auth` resolves on PATH. A host whose nexus pin predates allod/nexus#52 has no such script installed, and migrating there would leave the old `rotate-token` refresh refusing the migrated entry.

## Where things are found

All five commands resolve the secrets checkout the same way: `~/work/<checkout>` where `<checkout>` is the `allod/secrets` entry of the inventory's repository registry, or `allod/secrets` when the registry does not name one. An optional trailing argument names a checkout or worktree instead. `declare` additionally resolves the inventory checkout to evaluate machine types, the same way when no override is given: `~/work/<checkout>` where `<checkout>` is the `allod/inventory` entry of the repository registry, or `allod/inventory` when the registry does not name one; `$INVENTORY`, when set, always wins. The identity `create`, `rekey`, `rotate`, and `migrate` use is `$AGE_IDENTITY`, or `~/.ssh/host`; its `.pub` must be among the recipients. `declare` reads no identity.

## Build

```nix
buildGoModule {
  subPackages = [ "cmd/allod" ];
  tags = [ "site" "secret" ];
}
```

`declare` needs no `secret` tag and is present in every build; `create`, `rekey`, `rotate`, and `migrate` need it, so it is set wherever the age identity lives. The command runs `nix`, `age`, and `git` from the operator's PATH, plus `rclone` when a credential being rotated declares the `rclone-obscure` encoding; `declare` needs `nix` (to read machine types, the evaluated credential inventory, and the exported encodings) and `git` (to resolve the checkout), but never `age`.
