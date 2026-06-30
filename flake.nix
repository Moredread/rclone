{
  description = "rclone fork dev environment (Go toolchain for the justfile)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    flake-utils.url = "github:numtide/flake-utils";
  };

  nixConfig = {
    experimental-features = ["nix-command" "flakes"];
  };

  outputs = {
    nixpkgs,
    flake-utils,
    ...
  }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {inherit system;};
    in {
      devShells.default = pkgs.mkShell {
        packages = [
          # keep-sorted start
          pkgs.git
          pkgs.go
          pkgs.just
          # keep-sorted end
        ];
      };
    });
}
