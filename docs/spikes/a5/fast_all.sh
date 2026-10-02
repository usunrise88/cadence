#!/usr/bin/env bash
# A5: the fast decodes per profile (pipeline.run + live code path, then the pack's loop in its own process).
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
OUT=${A5_WORK:-$HOME/cadence-spikes/a5}/out
for p in ${PROFILES:-80ms 160ms 1120ms}; do
  for w in ab c; do
    "$HERE/run.sh" --gpu python /a5/fast_decode.py "$p" "$w" > "$OUT/fast-$p-$w.log" 2>&1
    echo "$p $w rc=$?"
  done
done
