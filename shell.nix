{ pkgs ? import <nixpkgs> { } }:

pkgs.mkShell {
  buildInputs = [
    pkgs.jq
    pkgs.go
    pkgs.git
    pkgs.gnumake
    pkgs.kustomize
    pkgs.kubebuilder
  ];
}
