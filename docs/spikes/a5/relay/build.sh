#!/usr/bin/env bash
# Build the A5 relay (static binary) into ~/cadence-spikes/a5/bin/relay with the pinned Go toolchain image.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${A5_WORK:-$HOME/cadence-spikes/a5}
mkdir -p "$WORK/bin" "$WORK/gocache"
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod \
  -e CGO_ENABLED=0 -v "$HERE:/src" -v "$WORK/gocache:/cache" -v "$WORK/bin:/out" -w /src golang:1.27 \
  sh -c 'go mod tidy && go vet ./... && go build -o /out/relay .'
