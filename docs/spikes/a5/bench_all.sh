#!/usr/bin/env bash
# A5: the bench sessions against a running worker job (one session after another). TAG names the worker setup.
#   TAG=t160-1120 docs/spikes/a5/bench_all.sh
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
OUT=${A5_WORK:-$HOME/cadence-spikes/a5}/out
TAG=${TAG:?worker setup tag}
b() { name=$1; shift; "$HERE/run.sh" python /a5/bench.py --out "$TAG-$name" "$@" > "$OUT/$TAG-$name.log" 2>&1; echo "$name rc=$?"; }
for s in ${SESSIONS:-ru-paced-16k he-paced-16k ru-fast-16k ru-paced-48k ru-eou-48k ru-tel-48k}; do
  case $s in
    ru-paced-16k) b "$s" --mode paced --tags ru --rate 16000 ;;
    he-paced-16k) b "$s" --mode paced --tags he --rate 16000 ;;
    ru-fast-16k) b "$s" --mode fast --tags ru --rate 16000 ;;
    ru-paced-48k) b "$s" --mode paced --tags ru --rate 48000 ;;
    ru-paced-48k-f20) b "$s" --mode paced --tags ru --rate 48000 --frame-ms 20 ;;
    he-paced-48k) b "$s" --mode paced --tags he --rate 48000 ;;
    ru-eou-48k) b "$s" --mode eou --tags ru --rate 48000 --tail 2.0 ;;
    ru-tel-48k) b "$s" --mode paced --tags ru --rate 48000 --telephony ;;
  esac
done
