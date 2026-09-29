# allod site serve and allod site view

An agent working in a dev VM can serve the site it is editing, and the owner can look at those pages in a browser on the hypervisor with one command. The site's own flake says how to serve it: `allod` runs that and knows nothing about zola, Vite or any other tool. In the VM, `allod site serve` starts the server as a systemd unit named after the site, listening on 127.0.0.1 only, which keeps running until it is stopped. On the hypervisor, `allod site view <site>` makes sure that server is up and forwards its one port until Ctrl-C.

## The contract with a site repository

A previewable site's flake has an app named `preview` for the machine's system, `apps.<system>.preview`. The app:

- listens on the TCP port in `ALLOD_PREVIEW_PORT`, at the address in `ALLOD_PREVIEW_INTERFACE`, which is always the literal `127.0.0.1` — on these machines `localhost` resolves to `::1` alone, and a server bound there cannot be reached;
- fails when that port is taken, and never moves to another one. `allod` only ever waits on the port it handed over, so a server that quietly picked a different one is reported as still starting while the site is in fact being served where no forward reaches it;
- serves everything the browser needs on that one port, reload channel included;
- runs in the foreground and exits when sent `SIGTERM`;
- reloads the browser when files change, by whatever means the tool has.

A flake without that app is refused by name, and nothing is started.

A site repository should commit its `flake.lock`. `nix run` writes one into a checkout that has none, and inside the unit that appears as a new file in the working tree.

## Three worked examples

Each is the whole `flake.nix` of a site repository. In full, for a zola site:

```nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/6774f7bc253789b113a4f39285dc0fa100abeacc";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
    in
    {
      apps.${system}.preview = {
        type = "app";
        program = toString (pkgs.writeShellScript "preview" ''
          exec ${pkgs.zola}/bin/zola serve \
            --interface "$ALLOD_PREVIEW_INTERFACE" --port "$ALLOD_PREVIEW_PORT"
        '');
      };
    };
}
```

The other two are that flake with a different `program`. A directory of plain HTML under `public/`:

```nix
        program = toString (pkgs.writeShellScript "preview" ''
          exec ${pkgs.live-server}/bin/live-server \
            --host "$ALLOD_PREVIEW_INTERFACE" --port "$ALLOD_PREVIEW_PORT" public
        '');
```

A Vite site:

```nix
        program = toString (pkgs.writeShellScript "preview" ''
          export PATH=${pkgs.nodejs}/bin:$PATH
          npm install --no-audit --no-fund
          exec node_modules/.bin/vite \
            --host "$ALLOD_PREVIEW_INTERFACE" --port "$ALLOD_PREVIEW_PORT" --strictPort
        '');
```

What each asks of the site's owner:

- **zola** — the site's own nixpkgs pin must carry zola 0.23 or later. An older zola serves its reload channel on a second port, 1024, and the one port a preview offers does not reach it, so pages would load and never reload. The pin above carries 0.23.6.
- **Vite** — nixpkgs has `nodejs` but not Vite, so the app installs Vite from npm at every start. That writes `node_modules/` into the checkout, about 21 MB for a bare site, so the repository needs `node_modules/` in `.gitignore` and a committed `package-lock.json` to keep the install reproducible. A start with a cold npm cache needs the network. `--strictPort` is load-bearing: without it Vite moves to the next free port when the one it was given is taken.
- **a directory of plain HTML** — nothing to add.

## Using it in the VM

```
allod site serve [--port <n>] [--stop] [--vm <name>] [<site>]
```

`<site>` is a repository id in the registry. Without it, the site is the checkout the current directory sits in, found by walking up to its `site.toml`. The port is that repository's `preview_port` in the inventory's `scripts/repositories.json`, which `--port <n>` overrides; a site with neither is refused by name, as is a `preview_port` that is not a whole number from 1024 to 65535.

A git worktree of a site is served as that site: the worktree's own directory is in no registry, so the repository it belongs to supplies the id, the port and the unit name, while the server's working directory and the flake it runs stay the worktree's. So a site being edited on a branch is served on its usual port, and `--stop <site>` from anywhere stops it.

The unit is `allod-preview-<slug>`, where `<slug>` is the registry id, or the checkout path relative to `$HOME` when the registry does not list the site, with every character outside `[A-Za-z0-9._-]` replaced by `-`. Both ways of naming a site reach the same slug, so a preview started from inside a checkout can be stopped by id.

The server's own output is in the journal:

```
journalctl --user -u allod-preview-<slug>
```

Add `-f` to follow it. `--stop` stops the unit and every process it started; a preview that is not running is reported as such and is not an error.

A start exits 0 and prints `http://127.0.0.1:<port>` once the port accepts a connection and the unit is still running, whether this run started the server or found one already up. It exits 3 when 60 seconds pass with the unit up and the port still silent, which is what a first build inside the unit looks like; that message names the `journalctl` line to watch. Exit 1 is everything else — a refusal, a command that failed, systemd that could not be asked, or a unit that has stopped, in which case the last 20 lines of its log are on standard error. `allod site serve --help` says the same in brief.

Before it starts a unit, `serve` connects to the port once. If anything answers, the port is taken: it exits 1 naming the port and starts nothing, so another program's server is not printed as the site's. A preview that is already running is not checked this way. The check cannot see a port taken in the seconds between it and the app's bind; the app then fails as its contract requires, and the journal says so.

## From the hypervisor

```
allod site view [--vm <name>] [--port <n>] <site>
```

`view` makes sure the preview is running in the VM, prints `http://127.0.0.1:<port>`, and then becomes the `ssh` that forwards that port from the VM to the same port here, so a browser on this machine opens the page at that address. The port comes from this machine's registry, and the VM from `vm-specs.json` beside it: the one machine whose repository list holds the site, or `--vm <name>` when none or several do. The VM must run a build of `allod` that has `site serve`; an older one there fails as an unknown command and `view` stops with its exit code.

`allod site serve --stop --vm <name> <site>` stops that server from here. `--vm` resolves nothing locally — it runs the same command in the VM over a connection of its own, with every value quoted for the shell there — so `<site>` is required with it.

Ctrl-C on `view` ends the forward and nothing else, deliberately: the agent in the VM uses that same server to fetch pages and check its own work, and the owner closing a browser window must not break a task in progress. What a site owner can therefore expect:

| Event | Result |
| --- | --- |
| Ctrl-C on `view`, or its terminal closes | `ssh` exits and the port here closes. The server is unaffected |
| `ssh` is killed outright | The same; the kernel closes the port |
| `--stop`, from either machine | systemd stops the unit and every process in its control group |
| The server crashes | The unit goes inactive, the forward stays up, and the browser shows a connection error until the preview is started again |
| The VM shuts down | The unit goes with it, and `ssh` here exits once its keepalives go unanswered (`ServerAliveInterval=5`, `ServerAliveCountMax=3`) |

What becomes of the unit when the last login session in the VM ends is not measured. A preview started from the hypervisor while no agent is logged in may not outlive that command; if it matters, start it from a session in the VM.

The connection is never a shared one: a forward added to a shared connection outlives the command that asked for it, and releasing it closes every other session riding that connection. `view` never starts a VM and never checks whether the local port is free — `ssh` refuses a taken port or an unreachable VM in its own words.
