{
  description = "nixtui - TUI to search nixpkgs and add, edit and apply packages in a NixOS flake";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      appVersion = nixpkgs.lib.removeSuffix "\n" (builtins.readFile ./VERSION);
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (pkgs: {
        default = pkgs.buildGoModule {
          pname = "nixtui";
          version = appVersion;
          src = ./.;
          vendorHash = "sha256-t7BjNtbpSMlmiJvQ1LbYPjqGjKnChjwn72QHdbRue4w=";
          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" "-X main.version=${appVersion}" ];
          nativeBuildInputs = [ pkgs.makeWrapper ];
          # Bundle the formatter; --suffix lets a nixfmt already on PATH win.
          postInstall = ''
            wrapProgram $out/bin/nixtui --suffix PATH : ${pkgs.lib.makeBinPath [ pkgs.nixfmt ]}
          '';
          meta = {
            description = "TUI to search nixpkgs and add, edit and apply packages in a NixOS flake";
            mainProgram = "nixtui";
          };
        };
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell { packages = [ pkgs.go pkgs.gopls ]; };
      });
    };
}
