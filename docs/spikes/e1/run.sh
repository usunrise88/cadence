#!/usr/bin/env bash
# E1: run a command in the NeMo runtime image (the stand's cadence/worker build of nvcr.io/nvidia/nemo-speech:26.07),
# in a container of our own (name e1-*), never the stand's.   docs/spikes/e1/run.sh [--gpu] <cmd...>
# Mounts:
#   /work     E1 scratch on the host (/cadence/spikes/e1): exports, results, pylib (onnxruntime-gpu, --no-deps)
#   /repo     this worktree, read-only (the spike scripts and the current cadence_nemo pipeline decoder)
#   /art      the stand's artifacts volume, read-only (hf/ = the offline Hugging Face cache with the base .nemo)
#   /corpora  /cadence/corpora, read-only (FLEURS sr)
# Root because the image's Python lives under /root; outputs are chowned back to the calling user.
set -euo pipefail
IMAGE=${E1_IMAGE:-cadence/worker:2e6aead}
WORK=${E1_WORK:-/cadence/spikes/e1}
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
GPU=()
if [[ ${1:-} == --gpu ]]; then GPU=(--gpus all); shift; fi
mkdir -p "$WORK/home" "$WORK/tmp"
exec docker run --rm --init --name "e1-$$" --entrypoint "" -u 0 "${GPU[@]}" --ipc=host --network host \
  --ulimit memlock=-1 --ulimit stack=67108864 \
  -e HOME=/work/home -e TMPDIR=/work/tmp -e HOST_UID="$(id -u):$(id -g)" \
  -e HF_HOME=/art/hf -e HF_HUB_OFFLINE=1 -e PYTHONUNBUFFERED=1 \
  -e PYTHONPATH=/repo/docs/spikes/e1:/repo/worker/packs/nemo:/repo/worker:/work/pylib \
  -v "$WORK:/work" -v "$REPO:/repo:ro" -v cadence-test_artifacts:/art:ro -v /cadence/corpora:/corpora:ro -w /work \
  ${DOCKER_EXTRA:-} "$IMAGE" bash -c '"$@"; rc=$?; chown -R "$HOST_UID" /work; exit $rc' _ "$@"
