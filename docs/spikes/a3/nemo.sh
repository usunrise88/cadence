#!/usr/bin/env bash
# Run a command inside the pinned NeMo Speech container, the way the NeMo pack's runtime image will.
#   docs/spikes/a3/nemo.sh [--gpu] <cmd...>
# Mounts:
#   /work     A3 scratch (data, checkpoints, NeMo v3.0.0 sources (identical to the image's /workspace/nemo) for examples/ and scripts/) - never a repo
#   /spike    these scripts (read-only)
#   /hfro     the host's Hugging Face cache, read-only; only the token file is used (HF_TOKEN_PATH), downloads go
#             to /work/hf so the shared cache is never written.
# The image's Python lives under /root (mode 0700), so the container must run as root; outputs are chowned
# back to the calling user afterwards so nothing root-owned is left in /work.
set -euo pipefail
IMAGE=${NEMO_IMAGE:-nvcr.io/nvidia/nemo-speech:26.07}   # sha256:b8b1c094f1bb...
WORK=${A3_WORK:-$HOME/cadence-spikes/a3}
HERE=$(cd "$(dirname "$0")" && pwd)
GPU=()
if [[ ${1:-} == --gpu ]]; then GPU=(--gpus all); shift; fi
mkdir -p "$WORK/home"
exec docker run --rm --init --entrypoint "" "${GPU[@]}" --ipc=host --ulimit memlock=-1 --ulimit stack=67108864 \
  -e HOME=/work/home -e HOST_UID="$(id -u):$(id -g)" \
  -v "$HOME/.cache/huggingface:/hfro:ro" -e HF_TOKEN_PATH=/hfro/token -e HF_HOME=/work/hf \
  -v "$WORK:/work" -v "$HERE:/spike:ro" -w /work \
  -e PYTHONUNBUFFERED=1 \
  ${DOCKER_EXTRA:-} "$IMAGE" bash -c '"$@"; rc=$?; chown -R "$HOST_UID" /work; exit $rc' _ "$@"
