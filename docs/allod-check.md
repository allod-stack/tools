# allod check

`allod check` is what you run in place of `nix flake check` on a repository that
holds NixOS machines. It does the same job, in small pieces: one `nix` process
per machine and per check, one after another, so a flake that would otherwise
exhaust an 8 GiB dev VM's memory in a single evaluation finishes inside it. It
also does more than the stock command: every check it can build, it builds, it
runs every step before reporting so one failure does not hide the next, and it
refuses to call a run green when it built nothing at all.

It reads a flake and builds its checks. It writes nothing — not even the
checkout's `flake.lock`.

## Command

```
allod check [--override-input <input> <flake-ref>]... [<flake-ref>]
```

The flake reference defaults to `.`, so in a checkout the whole command is:

```bash
allod check
```

Exit 0 when every step passed and at least one check was built. Exit 1 on a
usage error, on a failing step, on a flake with no machine and no check in it,
on a run that built no check, and on a flake output the command neither runs nor
is allowed to leave alone.

### What it runs

Four kinds of step, in this order, each announced with `==> <label>`:

| Step | What it does |
|---|---|
| `passive output <name>` | Forces the output's value, which is all stock `nix flake check` does with an output it does not understand. |
| `nixos module <name>` | Forces the exported module value. Composing a module onto a machine is a check's job, not this command's. |
| `machine <name>` | Forces `config.system.build.toplevel`, checks that it is a derivation, and instantiates it — everything the stock check does, and the instantiation on top. |
| `check <system>.<name>` | Builds the check when `<system>` is this host's, and only instantiates it otherwise, since a derivation for another system cannot be built here. |

A failing step prints `!!! FAILED: <label>` and the run continues. At the end,
either

```
All 7 steps passed (checks: 4 built).
```

or a list of every step that failed:

```
Failed steps (2 of 7):
  - machine green-machine
  - check x86_64-linux.green-local (built)
```

Machines and checks are what make a run meaningful; passive outputs and modules
are forced to match the stock command but do not count. A flake with machines
and no checks passes on its machines alone.

## Proving an unmerged branch

`--override-input` takes an input name and a flake reference, the way `nix` does,
and may be repeated. Every `nix` invocation that reads the flake gets all of
them, so the whole gate — the enumeration included — runs against the overridden
inputs. That matters when the set of checks itself comes from an input: with the
override in place, the run shows the checks the branch defines, not the ones the
lock file names.

Say a machine-inventory repository has a branch that adds a machine, and you
want the framework's whole suite run against it before it merges:

```bash
cd ~/work/<framework-checkout>
allod check --override-input inventory 'git+https://<forge>/<owner>/inventory.git?ref=add-machine'
```

or, against an unpushed local checkout:

```bash
allod check --override-input inventory "path:$(pwd)/../inventory"
```

Use `path:` with an absolute path for a local directory: a bare directory inside
a git repository is fetched through the git fetcher, which cannot see a file that
has not been committed.

With at least one override, `--no-write-lock-file` is passed to every
invocation, so a run against a branch cannot leave a modified `flake.lock`
behind in the checkout. The command never writes the lock either way.

## allod-check.toml

Some repositories expose a top-level output that is not a machine, a check or a
module — a source tree another flake consumes, say. Two such names are built in,
`lib` and `vmFacts`. Any other one is the repository's own setting, and lives in
`allod-check.toml` at the root of the flake being checked:

```toml
passive-outputs = ["profilesSource", "secretsSource"]
```

A repository without the file adds nothing, which is the usual case. A name
listed here that the flake does not expose is not an error, so one file can
serve a repository and its forks. The file is read from the directory when the
reference is a directory, and from the source `nix flake metadata` reports
otherwise, so it always comes from the revision being checked.

The file is deliberately strict, because its whole job is to widen what the
command tolerates: an unknown key, a `[table]` header, a value that is not a
list of quoted strings, a name listed twice, or a name the command already runs
itself (`checks`, `nixosConfigurations`, `nixosModules`) each stop the run with
a message naming the file.

## What a refusal means

**`<flake> exposes flake output(s) allod check does not run:`** — the flake has
a top-level output that is neither run nor allowed, and the run stops before any
step. Nearly always a typo: `chekcs.x86_64-linux.foo` is not a check, and
nothing else would have told you. If the output is deliberate and needs no step
of its own, add it to `passive-outputs` in `allod-check.toml`. If it needs a
step — a new `packages` output, say — the command has to learn to run it before
it can be trusted as the gate.

**`found no machine and no check to run in <flake>`** — the flake exposed
neither, so there was nothing to gate. A run of no steps is not a pass.

**`Witnessed nothing: evaluated N of N checks and built none of them.`** — every
check is keyed on a system this host is not, so each one was instantiated and
none was built. Instantiation catches a mistake written in a check's Nix
expression, where most of them live; it does not catch one written into the
builder's shell, which only a build runs. The refusal names the system to run
the command on.

**`could not enumerate what to run in <flake>`** — the one cheap evaluation that
reads the output names failed, and the message from `nix` is above it. A
`checks.<name>` whose name is not a system type, such as `checks.typo`, is
reported here.

## The self-test

`tests/allod-check-selftest.sh` in this repository proves the command can fail,
one fixture per way it can, and compares it against stock `nix flake check` over
a set of small fixtures. It needs a real `nix`, which the flake's own sandboxed
checks cannot provide, so it is not part of `nix flake check` here and is run by
hand:

```bash
ALLOD_UNDER_TEST=$(nix build --no-link --print-out-paths .#allod)/bin/allod \
  bash tests/allod-check-selftest.sh
```

Run it after a Nix upgrade, and before a change to the command lands. The second
half is the reason for the first: it prints a table of every fixture with stock
Nix's verdict beside the command's, and fails when Nix rejects a fixture the
command accepts. That is the standing witness for how deeply Nix checks the
outputs this command handles — there is no list of that written down anywhere,
by design, because a list read off one Nix release goes quietly stale and a
differential does not.

A fixture the command rejects and Nix accepts is expected and printed as such:
being stricter than the stock command is the point.

## Why not `nix flake check`

On a deployment fork with eight machines and 21 checks, measured on an 8 GiB dev
VM with no swap: `nix flake check --no-build` was killed by the kernel after 45
seconds having reached 6.6 GiB, because it evaluates every machine in one
process. The same suite through this command passed all 30 steps in 3 minutes 38
seconds, peaking at 5.0 GiB across the whole run, with the largest single
process well under a gigabyte. The ceiling to watch is the largest single check,
not the sum.
