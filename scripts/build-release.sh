#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
release_dir="${1:-dist}"
mkdir -p "$release_dir"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  release_os="${target%/*}"
  release_arch="${target#*/}"
  release_name="miner-fleet-${release_os}-${release_arch}"
  [[ "$release_os" != windows ]] || release_name+=".exe"
  CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" go build -trimpath -ldflags='-s -w' -o "$release_dir/$release_name" .
done
