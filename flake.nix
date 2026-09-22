{
  description = "allod/tools — development flake for the Go programs in this repo";

  # This flake exists for development only: `nix build .#forge`,
  # `nix flake check`, `nix develop`. Every consumer imports this repository
  # with `flake = false` and defines its production packages from the source
  # tree, so nothing here changes what they build.

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  # `flake = false`: the credential-store-url-parity check below only cmps
  # one file against this tree, so the source is all it needs, and this
  # keeps allod/secrets' own inputs (nixpkgs, inventory) out of this lock.
  # Like the rest of this flake, this input is development-only — every
  # consumer imports allod/tools with `flake = false`, so nothing downstream
  # pays for it.
  inputs.allod-secrets = {
    url = "git+https://forge.anarch.diy/allod/secrets.git";
    flake = false;
  };

  outputs = { self, nixpkgs, allod-secrets }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};

      forge = pkgs.buildGoModule {
        pname = "forge";
        version = "0.1.0";
        src = ./.;
        # Standard library only, by design: no module dependencies to vendor.
        vendorHash = null;
        subPackages = [ "cmd/forge" ];

        meta = {
          description = "Forgejo CLI: gh, but for a self-hosted Forgejo instance";
          mainProgram = "forge";
          platforms = pkgs.lib.platforms.unix;
        };
      };

      allod = pkgs.buildGoModule {
        pname = "allod";
        version = "0.1.0";
        src = ./.;
        # Standard library only, by design: no module dependencies to vendor.
        vendorHash = null;
        subPackages = [ "cmd/allod" ];

        meta = {
          description = "Allod workspace change and patch-transfer CLI";
          mainProgram = "allod";
          platforms = pkgs.lib.platforms.unix;
        };
      };

      flakeUpdateCascade = pkgs.buildGoModule {
        pname = "flake-update-cascade";
        version = "0.1.0";
        src = ./.;
        # Standard library only, by design: no module dependencies to vendor.
        vendorHash = null;
        subPackages = [ "cmd/flake-update-cascade" ];

        meta = {
          description = "Update named flake inputs across every workspace repository that pins them";
          mainProgram = "flake-update-cascade";
          platforms = pkgs.lib.platforms.unix;
        };
      };

      # buildGoModule's check phase only tests the packages named in
      # subPackages, so the internal ones need a check of their own. This also
      # covers formatting and vet, which nothing else would.
      goChecks = pkgs.runCommand "allod-tools-go-checks"
        {
          # git is a test dependency: internal/gitremote drives the real thing.
          nativeBuildInputs = [ pkgs.go pkgs.git ];
          src = ./.;
        } ''
        export HOME="$TMPDIR"
        export GOCACHE="$TMPDIR/go-cache"
        export GOPATH="$TMPDIR/go"
        export GOFLAGS=-mod=mod
        export GOPROXY=off
        export GOTOOLCHAIN=local
        # Pure Go, and this derivation has no C compiler.
        export CGO_ENABLED=0

        cp -r "$src" source
        chmod -R u+w source
        cd source

        unformatted=$(gofmt -l .)
        if [ -n "$unformatted" ]; then
          echo "gofmt would rewrite:" >&2
          echo "$unformatted" >&2
          exit 1
        fi

        go vet ./...
        go test ./...

        # 'allod site deploy', 'check', and 'config' are behind the 'site'
        # build tag ('preview' is not); an untagged run compiles neither
        # those commands nor the half of the tests that assert them absent.
        # Both directions are checked, or the tagged code would rot unnoticed
        # on every machine that does not opt in.
        go vet -tags site ./...
        go test -tags site ./...

        # 'allod secret create', 'rekey', and 'rotate' are behind
        # their own 'secret' tag for the same reason ('declare' is not: it
        # writes no ciphertext and reads no identity, so it runs untagged
        # everywhere), and the host sets both tags. Each is checked alone
        # (which also runs the other's absence tests) and then together, the
        # shape the host actually builds.
        go vet -tags secret ./...
        go test -tags secret ./...
        go vet -tags site,secret ./...
        go test -tags site,secret ./...

        touch "$out"
      '';

      allodParity = pkgs.runCommand "allod-parity-tests"
        {
          nativeBuildInputs = [
            pkgs.bash
            pkgs.coreutils
            pkgs.findutils
            pkgs.gawk
            pkgs.git
            pkgs.gnugrep
            pkgs.gnused
            pkgs.gnutar
            pkgs.gzip
            pkgs.jq
            pkgs.openssh
            pkgs.procps
            pkgs.util-linux
          ];
          src = ./.;
        } ''
        export HOME="$TMPDIR/home"
        cp -r "$src" source
        chmod -R u+w source
        cd source
        patchShebangs .

        export ALLOD_UNDER_TEST=${allod}/bin/allod
        bash tests/allod-change.sh
        bash tests/allod-patch.sh
        bash tests/pr-explain/components.sh
        bash tests/pr-explain/validation.sh
        bash tests/pr-explain/command.sh

        touch "$out"
      '';

      # allod/tools cannot evaluate Nix cheaply, so the Go tests read a
      # checked-in copy of the secrets flake's vector table and grammar
      # (cmd/allod/testdata/credential-store-url.json) instead of the export
      # itself. This check is what keeps that copy honest: nothing else
      # compares the two files, and a stale copy would test the Go
      # implementation against a table nobody else agrees with. It moved
      # here from allod/archetypes, where it could not hold on a fork whose
      # `secrets` input is its own data repo (allod/archetypes#110).
      #
      # `${./cmd/...}` is a path literal: it copies only that one file into
      # the store, so this derivation is stable across commits that touch
      # anything else (nix.md).
      credentialStoreUrlParity = pkgs.runCommand "credential-store-url-parity-check"
        { nativeBuildInputs = [ pkgs.diffutils ]; }
        ''
          # `if ! cmp`, not a bare `cmp`: an inverted command never aborts
          # under errexit, so the failure is the explicit `exit 1` and the
          # message gets printed. cmp also names the first differing byte.
          if ! cmp ${./cmd/allod/testdata/credential-store-url.json} ${allod-secrets}/credential-store-url.json; then
            echo "ERROR: the credential-store URL vector table differs between repositories" >&2
            echo "  allod/tools:   cmd/allod/testdata/credential-store-url.json" >&2
            echo "  allod/secrets: credential-store-url.json" >&2
            echo "allod/tools must carry a byte-for-byte copy of the allod/secrets definition" >&2
            exit 1
          fi
          echo "allod/tools and allod/secrets agree on credential-store-url.json"
          touch $out
        '';

      # The mock-driven cascade suites, run against the packaged program. The
      # nixConfig suite is left out: it drives the real nix under a pty, which
      # the sandbox cannot host; run it by hand.
      cascadeSuites = pkgs.runCommand "flake-update-cascade-suites"
        {
          nativeBuildInputs = [
            pkgs.bash
            pkgs.coreutils
            pkgs.gnugrep
            pkgs.jq
            pkgs.util-linux
          ];
          src = ./.;
        } ''
        export HOME="$TMPDIR/home"
        cp -r "$src" source
        chmod -R u+w source
        cd source
        patchShebangs .

        suites="tests/flake/flake-update-cascade/dependency-order.sh
          tests/flake/flake-update-cascade/dry-run.sh
          tests/flake/flake-update-cascade/external-remote.sh
          tests/flake/flake-update-cascade/failures.sh
          tests/flake/flake-update-cascade/lock-contention.sh
          tests/flake/flake-update-cascade/post-pull-lock.sh
          tests/flake/flake-update-cascade/preflight.sh
          tests/flake/flake-update-cascade/pr-mode.sh
          tests/flake/flake-update-cascade/validation.sh
          tests/flake/flake-update-cascade/resolve-heads.sh
          tests/flake/flake-update-cascade-multiple-inputs.sh
          tests/flake/flake-update-cascade-follows.sh"

        export CASCADE_UNDER_TEST=${flakeUpdateCascade}/bin/flake-update-cascade
        for suite in $suites; do
          echo "== $suite"
          bash "$suite"
        done

        touch "$out"
      '';

      # The flake-status suite drives the Bash program against fixture locks
      # and a mock git, so it needs no network and no real git.
      flakeStatusSuite = pkgs.runCommand "flake-status-suite"
        {
          nativeBuildInputs = [
            pkgs.bash
            pkgs.coreutils
            pkgs.gawk
            pkgs.gnugrep
            pkgs.gnused
            pkgs.jq
          ];
          src = ./.;
        } ''
        export HOME="$TMPDIR/home"
        cp -r "$src" source
        chmod -R u+w source
        cd source
        patchShebangs .

        bash tests/flake/flake-status.sh

        touch "$out"
      '';
    in
    {
      packages.${system} = {
        inherit allod forge;
        flake-update-cascade = flakeUpdateCascade;
        default = forge;
      };

      checks.${system} = {
        inherit allod forge;
        flake-update-cascade = flakeUpdateCascade;
        allod-parity = allodParity;
        credential-store-url-parity = credentialStoreUrlParity;
        cascade-suites = cascadeSuites;
        flake-status-suite = flakeStatusSuite;
        go-checks = goChecks;
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = [ pkgs.go pkgs.gopls pkgs.jq ];
      };
    };
}
