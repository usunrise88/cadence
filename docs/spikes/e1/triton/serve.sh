#!/usr/bin/env bash
# E1 step 3c: start tritonserver on a model repository (container e1-triton, host network, private ports 18400 http /
# 18401 grpc / 18402 metrics, loopback only).   docs/spikes/e1/triton/serve.sh /cadence/spikes/e1/triton/repo-fp16
# The repository holds hard links into /cadence/spikes/e1/onnx, so the whole scratch tree is mounted read-only.
# The CUDA memory pool holds the implicit state of every live stream (6.3 MB in + 6.3 MB out per stream at fp32):
# 256 MB (A3's value) runs out at about 20 streams and the rest falls back to host memory; 2 GB by default here.
set -euo pipefail
REPO=${1:?model repository}
IMAGE=${TRITON_IMAGE:-nvcr.io/nvidia/tritonserver:26.08-py3}
exec docker run --rm --name e1-triton --gpus all --network host --shm-size 1g \
  -v /cadence/spikes/e1:/cadence/spikes/e1:ro "$IMAGE" \
  tritonserver --model-repository="$REPO" --http-address=127.0.0.1 --grpc-address=127.0.0.1 \
  --metrics-address=127.0.0.1 --http-port=18400 --grpc-port=18401 --metrics-port=18402 \
  --pinned-memory-pool-byte-size="${PINNED_POOL_BYTES:-268435456}" \
  --cuda-memory-pool-byte-size=0:"${CUDA_POOL_BYTES:-2147483648}" --log-verbose=0 \
  --disable-auto-complete-config
