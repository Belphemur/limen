{
  description = "Limen dev environment";

  inputs = {
    nixpkgs.url = "github:cachix/devenv-nixpkgs/rolling";
    nixpkgs-upstream.url = "github:NixOS/nixpkgs/422c7ae3878366e2f011a40cc3e31b45b51c560c";
    flake-parts.url = "github:hercules-ci/flake-parts";
    devenv.url = "github:cachix/devenv";
    devenv.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      imports = [ inputs.devenv.flakeModule ];

      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
        "x86_64-darwin"
      ];

      perSystem = { pkgs, inputs', ... }: {
        devenv.shells.default = {
          overlays = [
            (final: prev: {
              nodejs-slim_24 = inputs'.nixpkgs-upstream.legacyPackages.nodejs-slim_24;
              go_1_26 = inputs'.nixpkgs-upstream.legacyPackages.go_1_26;
            })
          ];

          languages.go = {
            enable = true;
            package = pkgs.go_1_26;
            enableHardeningWorkaround = true;
          };

          languages.javascript = {
            enable = true;
            package = pkgs.nodejs-slim_24;
            corepack.enable = true;
          };

          packages = with pkgs; [
            buf
            protobuf
            golangci-lint
            gotools
            air
            docker
            docker-compose
            git
            jq
            yq-go
            ripgrep
            just
            gnumake
          ];

          env = {
            GO111MODULE = "on";
          };

          processes = {
            backend.exec = "make hot-dev-run";
            portal.exec = "make dev-portal";
            admin.exec = "make dev-admin";
          };

          tasks = {
            "proto:gen".exec = "make proto";
            "proto:lint".exec = "buf lint";
            "dev:hot".exec = "air -c .air.toml";
            "dev:up".exec = "make dev";
            "dev:down".exec = "make dev-down";
          };

          git-hooks.hooks = {
            gofmt.enable = true;
            golangci-lint.enable = true;

            gofix = {
              enable = true;
              name = "go fix";
              entry = "go fix ./...";
              files = "\\.go$";
              pass_filenames = false;
            };

            govet = {
              enable = true;
              name = "go vet";
              entry = "go vet ./...";
              files = "\\.go$";
              pass_filenames = false;
            };
          };

          enterShell = ''
            export PATH="$PWD/bin:$PWD/web/portal/node_modules/.bin:$PATH"

            echo ""
            echo "  Limen devshell"
            echo "  Go      $(go version | awk '{print $3}')"
            echo "  Node    $(node --version)"
            echo "  pnpm    $(pnpm --version 2>/dev/null || echo 'run: corepack install')"
            echo "  Buf     $(buf --version)"
            echo "  Air     $(air -v 2>&1 | head -1)"
            echo ""
            echo "  make dev          -> start full stack"
            echo "  make hot-dev      -> start with hot-reload"
            echo "  devenv up         -> run hot-reload backend + portal Vite + admin Vite in parallel"
            echo "  make nix-setup    -> first-time Nix setup for this repo"
            echo ""
          '';
        };
      };
    };
}
