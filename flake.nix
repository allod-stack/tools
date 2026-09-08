{
  description = "allod/tools — development flake for the Go programs in this repo";

  # This flake exists for development only: `nix build .#forge`,
  # `nix flake check`, `nix develop`. Every consumer imports this repository
  # with `flake = false` and defines its production packages from the source
  # tree, so nothing here changes what they build.

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
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

        # The site namespace is behind the 'site' build tag, so an untagged run
        # compiles neither the command nor the half of its tests that asserts
        # the namespace exists. Both directions are checked, or the tagged code
        # would rot unnoticed on every machine that does not opt in.
        go vet -tags site ./...
        go test -tags site ./...

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

      # The cascade suites run three times: against the Bash oracle, against
      # the Go program, and in parity mode, where every invocation runs both
      # on one fixture and must produce identical output, status, command
      # trace, and tree. The nixConfig suite is left out: it drives the real
      # nix under a pty, which the sandbox cannot host; run it by hand.
      cascadeParity = pkgs.runCommand "flake-update-cascade-parity-tests"
        {
          nativeBuildInputs = [
            pkgs.bash
            pkgs.coreutils
            pkgs.diffutils
            pkgs.findutils
            pkgs.gnugrep
            pkgs.gnused
            pkgs.gnutar
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

        suites="tests/flake/flake-update-cascade/dry-run.sh
          tests/flake/flake-update-cascade/external-remote.sh
          tests/flake/flake-update-cascade/failures.sh
          tests/flake/flake-update-cascade/lock-contention.sh
          tests/flake/flake-update-cascade/preflight.sh
          tests/flake/flake-update-cascade/pr-mode.sh
          tests/flake/flake-update-cascade/validation.sh
          tests/flake/flake-update-cascade-multiple-inputs.sh
          tests/flake/flake-update-cascade-follows.sh"

        for suite in $suites; do
          echo "== oracle: $suite"
          bash "$suite"
        done
        export CASCADE_UNDER_TEST=${flakeUpdateCascade}/bin/flake-update-cascade
        for suite in $suites; do
          echo "== under test: $suite"
          bash "$suite"
        done
        export CASCADE_PARITY=1
        for suite in $suites; do
          echo "== parity: $suite"
          bash "$suite"
        done

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
        cascade-parity = cascadeParity;
        go-checks = goChecks;
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = [ pkgs.go pkgs.gopls pkgs.jq ];
      };
    };
}
