#!/bin/sh
# Spike S5 end to end (throwaway): fixtures -> tiles -> Playwright measurements (headless Chromium) -> librosa compare.
# Usage: sh docs/spikes/s5/run.sh [fixtures|tiles|measure|compare|all]   (from the repository root; needs Docker)
# FLEURS clips are read read-only from the stand's import when present; nothing is written there.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
S5=web/src/spikes/s5
FX=$S5/public/fixtures
OUT=web/test-results/s5
FLEURS_DIR=${FLEURS_DIR:-/cadence/stand/artifacts/imports/fleurs-sr-latn}
PY="docker run --rm --user $(id -u):$(id -g) -e HOME=/tmp -e UV_CACHE_DIR=/uvcache -v cadence-uvcache:/uvcache -v $REPO:/src -w /src"
[ -d "$FLEURS_DIR" ] && PY="$PY -v $FLEURS_DIR:/fleurs:ro"
UV="ghcr.io/astral-sh/uv:python3.12-bookworm-slim uv run --no-project --python 3.12 --with numpy --with scipy --with soundfile --with librosa==0.11.0 --with av --with matplotlib"
step=${1:-all}

if [ "$step" = fixtures ] || [ "$step" = all ]; then
  mkdir -p "$REPO/$FX"
  $PY $UV python docs/spikes/s5/fixtures.py "$FX"
fi
if [ "$step" = tiles ] || [ "$step" = all ]; then
  rm -rf "${REPO:?}/$FX/tiles-call" "${REPO:?}/$FX/tiles-clip"
  $PY $UV python docs/spikes/s5/tiles.py "$FX/call.wav" "$FX/tiles-call"
  $PY $UV python docs/spikes/s5/tiles.py "$FX/clip.wav" "$FX/tiles-clip"
fi
if [ "$step" = measure ] || [ "$step" = all ]; then
  mkdir -p "$REPO/$OUT"
  docker run --rm --init --ipc=host --user "$(id -u):$(id -g)" -e HOME=/tmp -e S5_MEM="${S5_MEM:-}" -v "$REPO":/src -w /src/$S5 \
    mcr.microsoft.com/playwright:v1.63.0-noble sh -c "npm ci --no-audit --no-fund --silent && ../../../node_modules/.bin/playwright test -c playwright.config.ts ${PW_ARGS:-}"
fi
if [ "$step" = compare ] || [ "$step" = all ]; then
  $PY $UV python docs/spikes/s5/compare.py "$FX" "$OUT" "$FX/tiles-call"
fi
