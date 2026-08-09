{
  description = "fzf UI to check/update flake inputs under ~/src";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          flake-up = pkgs.buildGoModule {
            pname = "flake-up";
            version = "0.1.0";
            src = self;
            # No third-party Go modules.
            vendorHash = null;
            meta = {
              description = "Survey ~/src flakes and update locks via fzf";
              mainProgram = "flake-up";
            };
          };
          # Wrap so nix/fzf/jj are available when run from a minimal PATH.
          wrapped = pkgs.writeShellApplication {
            name = "flake-up";
            runtimeInputs = with pkgs; [
              flake-up
              nix
              fzf
              jujutsu
              git
            ];
            text = ''
              exec ${pkgs.lib.getExe flake-up} "$@"
            '';
          };
        in
        {
          default = wrapped;
          # Unwrapped binary (for debugging).
          flake-up-bin = flake-up;
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShellNoCC {
            packages = with pkgs; [
              go
              gopls
              nix
              fzf
              jujutsu
              git
            ];
          };
        }
      );
    };
}
