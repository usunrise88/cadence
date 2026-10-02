#!/usr/bin/env bash
# Run a command in the stand's NeMo runtime image (cadence/worker:635702e), in our own container — never the stand's.
#   docs/spikes/a5/run.sh [--gpu] [--name N] <cmd...>
# Mounts:
#   /a5      these scripts (read-only)
#   /work    ~/cadence-spikes/a5 (clips, outputs, temp; never a repo)
#   /hfmodel the stand's HF cache entry of nvidia/nemotron-3.5-asr-streaming-0.6b (read-only; the pinned revision)
# Host networking, so the relay on 127.0.0.1 is reachable.
set -euo pipefail
IMAGE=${A5_IMAGE:-cadence/worker:635702e}
WORK=${A5_WORK:-$HOME/cadence-spikes/a5}
HERE=$(cd "$(dirname "$0")" && pwd)
SNAP=/cadence/stand/artifacts/hf/hub/models--nvidia--nemotron-3.5-asr-streaming-0.6b
GPU=()
NAME=()
while [[ ${1:-} == --* ]]; do
  case $1 in
    --gpu) GPU=(--gpus all); shift ;;
    --name) NAME=(--name "$2"); shift 2 ;;
    *) break ;;
  esac
done
mkdir -p "$WORK/home" "$WORK/tmp" "$WORK/out" && chmod 777 "$WORK/home" "$WORK/tmp" "$WORK/out" 2>/dev/null || true
exec docker run --rm --init "${GPU[@]}" "${NAME[@]}" --network host --ipc=host --user 65532:65532 --entrypoint "" \
  --tmpfs /scratch:rw,size=8g,mode=1777 -e HOME=/work/home -e TMPDIR=/scratch -e PYTHONUNBUFFERED=1 -e HF_HUB_OFFLINE=1 -e NUMBA_CACHE_DIR=/work/tmp/numba \
  -e PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True -e A5_BATCH -e A5_SUFFIX -e A5_CAP_GIB -e A5_NO_PATCH \
  -e A5_MODEL=/hfmodel/snapshots/ea30d66debe3740a08b573244286791d423d6b3e/nemotron-3.5-asr-streaming-0.6b.nemo \
  -v "$HERE:/a5:ro" -v "$WORK:/work" -v "$SNAP:/hfmodel:ro" \
  ${DOCKER_EXTRA:-} "$IMAGE" "$@"
