{
  description = "Limen dev environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    devenv.url = "github:cachix/devenv";
    devenv.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      imports = [ inputs.devenv.flakeModule ];

      # All platforms your team uses.
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
        "x86_64-darwin"
      ];

      perSystem = { pkgs, ... }: {
        devenv.shells.default = {
          # Languages
          languages.go = {
            enable = true;
            package = pkgs.go_1_26;
            # Required for Delve on Linux with Nix hardening.
            enableHardeningWorkaround = true;
          };

          languages.javascript = {
            enable = true;
            package = pkgs.nodejs-slim_24;
            corepack.enable = true;
          };

          # Extra tooling
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

            # Custom pre-commit hook to run go fix ./...
            gofix = {
              enable = true;
              name = "go fix";
              entry = "go fix ./...";
              files = "\\.go$";
              pass_filenames = false;
            };

            # Custom pre-commit hook to run go vet ./...
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
