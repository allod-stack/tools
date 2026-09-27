# allod check

`allod check` is what you run in place of `nix flake check` on a repository that
holds NixOS machines. It does the same job in small pieces: one `nix` process per
machine and per check, one after another, so a flake that would otherwise exhaust
an 8 GiB dev VM's memory in a single evaluation finishes inside it. It also does
more: every check it can build, it builds, it runs every step before reporting so
one failure does not hide the next, and it refuses to call a run green when it
evaluated checks and built none of them. It reads a flake and builds its checks;
it writes nothing, not even the checkout's `flake.lock`.

## Command

```
allod check [--override-input <input> <flake-ref>]... [<flake-ref>]
```

The reference defaults to `.`, so in a checkout the whole command is `allod
check`.

Exit 0 when every step passed and nothing was refused; a flake with machines and
no checks passes on its machines alone. Exit 1 on a usage error, on a failing
step, on a flake with no machine and no check in it, on a run that evaluated
checks and built none of them, and on a flake output the command neither runs nor
is allowed to leave alone.

### What it runs

Four kinds of step, in this order, each announced with `==> <label>`:

| Step | What it does |
|---|---|
| `passive output <name>` | Forces the output's value, which is all stock `nix flake check` does with an output it does not understand. |
| `nixos module <name>` | Forces the exported module value. Composing a module onto a machine is a check's job, not this command's. |
| `machine <name>` | Requires `config.system.build.toplevel` to be a derivation and instantiates it — what the stock check does, and the instantiation on top. |
| `check <system>.<name>` | Requires the check to be a derivation, instantiates it, and builds it when `<system>` is this host's. A check for another system can only be instantiated, since its derivation cannot be built here. |

Every step reaches the flake the same way, one `nix eval '<ref>#.'` with the name
embedded in the expression, so a name that cannot be spelled on a command line
needs no special handling.

A failing step prints `!!! FAILED: <label>` and the run continues. At the end,
either `All 7 steps passed (checks: 4 built).` or a `Failed steps (2 of 7):`
heading listing every step that failed.

## Proving an unmerged branch

`--override-input` takes an input name and a flake reference, as `nix` does, and
may be repeated. Every `nix` invocation that reads the flake gets all of them, so
the whole gate — the enumeration included — runs against the overridden inputs.
That matters when the set of checks itself comes from an input: the run then shows
the checks the branch defines, not the ones the lock file names. To run a
framework repository's whole suite against an inventory branch before it merges:

```bash
cd ~/work/<framework-checkout>
allod check --override-input inventory 'git+https://<forge>/<owner>/inventory.git?ref=add-machine'
```

For a local checkout, use `path:` with an absolute path
(`--override-input inventory "path:$(pwd)/../inventory"`): a bare directory inside
a git repository is fetched through the git fetcher, which cannot see a file that
has not been committed.

## allod-check.toml

Some repositories expose a top-level output that is not a machine, a check or a
module — a source tree another flake consumes, say. Two such names are built in,
`lib` and `vmFacts`. Any other one is the repository's own setting, and lives in
`allod-check.toml` at the root of the flake being checked:

```toml
passive-outputs = ["profilesSource", "secretsSource"]
```

**An allowed output is forced and nothing more.** Forcing is what stock `nix flake
check` does with an output it does not understand and it catches a name that
throws, but it does not check what the output contains: listing `packages` here
would stop the refusal without checking that its entries are derivations, which
stock `nix flake check` does do. A surface that needs checking needs a step of its
own in the command.

A repository without the file adds nothing, which is the usual case. A name the
flake does not expose is not an error, so one file can serve a repository and its
forks; a name listed twice is the same as once. The file is read from the source
`nix flake metadata` reports, so an uncommitted `allod-check.toml` has exactly as
much effect as an uncommitted `flake.nix` would: none.

Nix decodes the file, with `builtins.fromTOML`. An unknown key, a
`passive-outputs` that is not a list of strings, a name the command runs itself
(`checks`, `nixosConfigurations`, `nixosModules`), or TOML Nix cannot read each
stop the run with a message naming the file.

## What a refusal means

**`<flake> exposes flake output(s) allod check does not run:`** — a top-level
output is neither run nor allowed, and the run stops before any step. Nearly
always a typo: `chekcs.x86_64-linux.foo` is not a check, and nothing else would
have told you. Otherwise allow it in `allod-check.toml` if forcing it is enough,
or teach the command a step for it.

**`found no machine and no check to run in <flake>`** — the flake exposed neither,
so there was nothing to gate. A run of no steps is not a pass.

**`Witnessed nothing: evaluated N of N checks and built none of them.`** — every
check is keyed on a system this host is not. Instantiation catches a mistake in a
check's Nix expression, where most of them live, but not one written into the
builder's shell, which only a build runs. The refusal names the system to run on.

**`could not enumerate what to run in <flake>`** — the one cheap evaluation that
reads the output names failed, and the message from `nix` is above it. A
`checks.<name>` that is not a system type, such as `checks.typo`, lands here.

## The self-test

`tests/allod-check-selftest.sh` proves the command can fail, one fixture per way
it can, and compares it against stock `nix flake check` over small fixtures. It
drives a real `nix`, which the flake's own sandboxed checks cannot host, so it is
not one of this repository's checks and is run by hand — after a Nix upgrade, and
before a change to the command lands:

```bash
ALLOD_UNDER_TEST=$(nix build --no-link --print-out-paths .#allod)/bin/allod \
  bash tests/allod-check-selftest.sh
```

Its second half is the reason for the first: it prints a table of every fixture
with stock Nix's verdict beside the command's, and fails when Nix rejects a
fixture the command accepts. That is the standing witness for how deeply Nix
checks the kinds this command handles; no list of that is written down, because a
list read off one Nix release goes quietly stale and a differential does not. A
fixture the command rejects and Nix accepts is expected and printed as such.
