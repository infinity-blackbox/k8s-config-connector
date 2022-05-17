{
  description = "GCP Config Connector, a Kubernetes add-on for managing GCP resources";

  inputs.nixpkgs.url = "github:nixos/nixpkgs";

  outputs = { self, nixpkgs }: {
    devShell = nixpkgs.lib.genAttrs nixpkgs.lib.platforms.unix (system:
      import ./shell.nix {
        pkgs = import nixpkgs { inherit system; };
      }
    );
  };
}
