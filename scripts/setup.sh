#!/usr/bin/env bash
# Sets up a Linux dev machine for logi: go-task, golangci-lint, a C toolchain
# (DuckDB uses cgo) and the Go modules. Idempotent: every step checks before it
# acts, so re-running is safe and fast. The cloud environment's setup script
# calls this file, so this is the single source of truth.
#
# Tools come from Nix (nixpkgs), which carries golangci-lint v2 matching CI.
# Go itself fetches the toolchain named in go.mod (GOTOOLCHAIN=auto).
set -euo pipefail

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TOOLS=/opt/logi-tools # GC roots for the nix builds, so they survive nix GC
BIN=/usr/local/bin
SUDO=
[ "$(id -u)" -eq 0 ] || SUDO=sudo

log() { printf '==> %s\n' "$*" >&2; }
nix_() { nix --extra-experimental-features 'nix-command flakes' "$@"; }

command -v nix >/dev/null || {
  echo "nix is not installed; install it first (https://nixos.org/download)" >&2
  exit 1
}

if ! command -v gcc >/dev/null || ! command -v g++ >/dev/null; then
  log "installing build-essential"
  $SUDO apt-get update -qq
  $SUDO apt-get install -y -qq build-essential
fi

# nix_install <command> <nixpkgs attr>: build, keep a GC root, link into PATH.
nix_install() {
  local cmd=$1 attr=$2 out
  $SUDO mkdir -p "$TOOLS"
  $SUDO "$(command -v nix)" --extra-experimental-features 'nix-command flakes' \
    build --out-link "$TOOLS/$attr" "nixpkgs#$attr"
  out=$TOOLS/$attr/bin/$cmd
  $SUDO ln -sf "$out" "$BIN/$cmd"
  log "$cmd -> $(readlink -f "$out")"
}

command -v go >/dev/null || nix_install go go
nix_install task go-task
nix_install golangci-lint golangci-lint

# Project dependencies; also fetches the go.mod toolchain.
log "downloading Go modules in $REPO"
(cd "$REPO" && task download)

log "done: $(go version), task $(task --version), $(golangci-lint version 2>&1 | head -1)"
