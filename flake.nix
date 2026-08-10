{
  description = "Two-pane status dashboard for flakes under ~/src";

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
          flake-status = pkgs.buildGoModule {
            pname = "flake-status";
            version = "0.1.0";
            src = self;
            vendorHash = "sha256-TIAN4GVJC8SnnYGezzKTuaRCU7TeChUfrE1iJ+zLS+g=";
            env.CGO_ENABLED = "0";
            meta = {
              description = "Status dashboard for flake inputs and VCS under ~/src (read-only TUI)";
              mainProgram = "flake-status";
              license = pkgs.lib.licenses.mit;
            };
          };
          wrapped = pkgs.writeShellApplication {
            name = "flake-status";
            runtimeInputs = with pkgs; [
              flake-status
              nix
              jujutsu
              git
            ];
            text = ''
              exec ${pkgs.lib.getExe flake-status} "$@"
            '';
          };
        in
        {
          default = wrapped;
          flake-status-bin = flake-status;
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
