{
  description = "super-argus local dev environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};

        # Go's own GOTOOLCHAIN=auto (default since Go 1.21) fetches the exact
        # toolchain each go.mod asks for if this one is older, so we don't pin
        # nixpkgs' go version to match go.mod/testapp/go.mod exactly.
        commonPackages = with pkgs; [ go gopls jq curl sqlite ];
      in
      {
        devShells.default = pkgs.mkShell {
          packages = commonPackages ++ (with pkgs; [ golangci-lint delve process-compose nodejs_22 ]);
          shellHook = ''
            echo "super-argus dev shell ($(go version), node $(node --version))"
            echo "  (cd app && npm ci && npm run build:web)  # build the Argus client into internal/webui/dist"
            echo "  go run ./cmd/argus       # start the server (reads ./.env)"
            echo "  nix run .#dev-env        # build the client, then start argus + testapp wired together"
          '';
        };

        devShells.testapp = pkgs.mkShell {
          packages = commonPackages;
          shellHook = ''
            echo "testapp dev shell ($(go version))"
            echo "  go run .                 # needs an Argus server reachable at ARGUS_URL"
            echo "  from repo root: nix run .#dev-env brings both up together"
          '';
        };

        apps.dev-env = {
          type = "app";
          program = "${pkgs.writeShellApplication {
            name = "argus-dev-env";
            runtimeInputs = [ pkgs.process-compose pkgs.nodejs_22 ];
            text = ''
              if [ ! -f process-compose.yaml ]; then
                echo "run this from the super-argus repo root" >&2
                exit 1
              fi

              # cmd/argus embeds internal/webui/dist at build time, so the
              # Argus client (app/) must be built into it before `go run`
              # serves anything useful at /. Skip if already built; force
              # a rebuild with ARGUS_REBUILD_CLIENT=1.
              if [ ! -f internal/webui/dist/index.html ] || [ "''${ARGUS_REBUILD_CLIENT:-}" = "1" ]; then
                echo "argus-dev-env: building the Argus client (app/)..." >&2
                ( cd app && npm ci && npm run build:web )
              fi

              # --no-server: process-compose's own control API defaults to
              # :8080, which collides with Argus's default listen address.
              exec process-compose up -f process-compose.yaml --no-server "$@"
            '';
          }}/bin/argus-dev-env";
        };
      });
}
