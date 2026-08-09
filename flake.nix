{
  description = "Two-pane TUI to check/update flake inputs under ~/src";

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
            vendorHash = "sha256-TIAN4GVJC8SnnYGezzKTuaRCU7TeChUfrE1iJ+zLS+g=";
            env.CGO_ENABLED = "0";
            meta = {
              description = "Survey ~/src flakes and update locks (two-pane TUI)";
              mainProgram = "flake-up";
            };
          };
          wrapped = pkgs.writeShellApplication {
            name = "flake-up";
            runtimeInputs = with pkgs; [
              flake-up
              nix
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
              jujutsu
              git
            ];
          };
        }
      );
    };
}
