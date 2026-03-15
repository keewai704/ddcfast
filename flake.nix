{
  description = "Fast DDC/CI monitor control daemon and CLI";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        version =
          if self ? shortRev
          then "0.0.0+git.${self.shortRev}"
          else "0.0.0";
        package = pkgs.callPackage ./nix/package.nix { inherit version; };
      in {
        packages.default = package;
        packages.ddcfast = package;
        checks.default = package;
        formatter = pkgs.nixpkgs-fmt;
      });
}
