# allod site preview

An agent working in a dev VM can serve the site it is editing and look at the pages while it changes them. The site's own flake says how to serve it: `allod` runs that and knows nothing about zola, Vite or any other tool. The server listens on 127.0.0.1 only, under a systemd unit named after the site, and keeps running until it is stopped.

## The contract with a site repository

A previewable site's flake has an app named `preview` for the machine's system, `apps.<system>.preview`. The app:

- listens on the TCP port in `ALLOD_PREVIEW_PORT`, at the address in `ALLOD_PREVIEW_INTERFACE`, which is always the literal `127.0.0.1` — on these machines `localhost` resolves to `::1` alone, and a server bound there cannot be reached;
- fails when that port is taken, and never moves to another one. `allod` waits for the port it handed over to accept a connection, so a server that quietly picked a different port would be reported as serving while nothing reachable is;
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
allod site preview [--port <n>] [--stop] [<site>]
```

`<site>` is a repository id in the registry. Without it, the site is the checkout the current directory sits in, found by walking up to its `site.toml`. The port is that repository's `preview_port` in the inventory's `scripts/repositories.json`, which `--port <n>` overrides; a site with neither is refused by name, as is a `preview_port` that is not a whole number from 1024 to 65535.

The unit is `allod-preview-<slug>`, where `<slug>` is the registry id, or the checkout path relative to `$HOME` when the registry does not list the site, with every character outside `[A-Za-z0-9._-]` replaced by `-`. Both ways of naming a site reach the same slug, so a preview started from inside a checkout can be stopped by id.

The server's own output is in the journal:

```
journalctl --user -u allod-preview-<slug>
```

Add `-f` to follow it. `--stop` stops the unit and every process it started; a preview that is not running is reported as such and is not an error.

A start exits 0 and prints `http://127.0.0.1:<port>` once the port accepts a connection, whether this run started the server or found one already up. It exits 1 when the unit is no longer running, with the last 20 lines of its log on standard error. It exits 3 when 60 seconds pass with the unit up and the port still silent, which is what a first build inside the unit looks like; that message names the `journalctl` line to watch. `allod site preview --help` says the same in brief.
