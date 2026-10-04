#!/usr/bin/env bash
# Fetch one FLEURS locale onto the corpora mount, as the source's own files (Cadence indexes them in place):
#   scripts/corpora/fleurs.sh sr_rs [splits…]     # default: train dev test
# Layout: $CORPORA/fleurs-<lang>/<revision>/{<split>/*.wav, <split>.tsv, SOURCE.yaml}. Idempotent: a split already
# extracted is skipped. FLEURS is CC BY 4.0 (google/fleurs dataset card).
set -euo pipefail
LOCALE="${1:?usage: fleurs.sh <locale e.g. sr_rs> [splits…]}"; shift || true
if [ $# -gt 0 ]; then SPLITS=("$@"); else SPLITS=(train dev test); fi
TAG="${LOCALE%%_*}-${LOCALE##*_}"; TAG="${TAG%-*}-$(echo "${TAG##*-}" | tr a-z A-Z)"   # sr_rs → sr-RS
CORPORA="${CORPORA:-/cadence/corpora}"
API=https://huggingface.co/api/datasets/google/fleurs
REV=$(curl -fsS -m 30 "$API" | python3 -c 'import json,sys; print(json.load(sys.stdin)["sha"])')
DIR="$CORPORA/fleurs-${LOCALE%%_*}/${REV:0:12}"
mkdir -p "$DIR"
base="https://huggingface.co/datasets/google/fleurs/resolve/$REV/data/$LOCALE"
for s in "${SPLITS[@]}"; do
  if [ -f "$DIR/$s/.done" ]; then echo "$s: already there"; continue; fi
  echo "$s: fetching"
  curl -fsSL -m 3600 -o "$DIR/$s.tsv" "$base/$s.tsv"
  mkdir -p "$DIR/$s"
  curl -fsSL -m 7200 "$base/audio/$s.tar.gz" | tar -xz -C "$DIR/$s" --strip-components=1
  chmod -R a+rX "$DIR/$s" "$DIR/$s.tsv"   # workers read the mount as their own user (uid 65532), not the owner
  touch "$DIR/$s/.done"
done
cat > "$DIR/SOURCE.yaml" <<YAML
name: fleurs-${LOCALE%%_*}
licence: CC-BY-4.0
kind: public
url: https://huggingface.co/datasets/google/fleurs
revision: $REV
languages: [$TAG]
note: >
  FLEURS $LOCALE as published: <split>.tsv (id, file, raw transcription, normalized transcription, phonemes, samples,
  gender) and <split>/<file>.wav (16 kHz mono). The test split is the golden set's source: never ingest it for training.
YAML
echo "done: $DIR"
