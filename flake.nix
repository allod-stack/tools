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

      # buildGoModule's check phase only tests the packages named in
      # subPackages, so the internal ones need a check of their own. This also
      # covers formatting and vet, which nothing else would.
      goChecks = pkgs.runCommand "forge-go-checks"
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

        touch "$out"
      '';
    in
    {
      packages.${system} = {
        inherit forge;
        default = forge;
      };

      checks.${system} = {
        inherit forge;
        go-checks = goChecks;
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = [ pkgs.go pkgs.gopls pkgs.jq ];
      };
    };
}
