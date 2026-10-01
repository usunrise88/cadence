#!/usr/bin/env bash
# A3 step 5: assemble a Triton model repository from an export dir and start tritonserver (host network, private
# ports 18300 http / 18301 grpc / 18302 metrics).   docs/spikes/a3/triton/serve.sh /home/.../a3/onnx/ft500
set -euo pipefail
ONNX=${1:?onnx export dir}
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=${A3_WORK:-$HOME/cadence-spikes/a3}/triton/repo
rm -rf "$REPO"; mkdir -p "$REPO"
cp -r "$HERE/streaming_asr" "$HERE/encoder_prompt" "$HERE/decoder_joint" "$REPO/"
mkdir -p "$REPO/encoder_prompt/1" "$REPO/decoder_joint/1"
# hard links: no second copy of the 2.4 GB weights; the graph references its data file by name
ln "$ONNX/encoder_prompt.onnx" "$REPO/encoder_prompt/1/model.onnx"
ln "$ONNX/encoder_prompt.onnx.data" "$REPO/encoder_prompt/1/encoder_prompt.onnx.data"
ln "$ONNX/decoder_joint.onnx" "$REPO/decoder_joint/1/model.onnx"
exec docker run --rm --name a3-triton --gpus all --network host --shm-size 1g \
  -v "$REPO:/models:ro" nvcr.io/nvidia/tritonserver:26.07-py3 \
  tritonserver --model-repository=/models --http-address=127.0.0.1 --grpc-address=127.0.0.1 --metrics-address=127.0.0.1 --http-port=18300 --grpc-port=18301 --metrics-port=18302 \
  --pinned-memory-pool-byte-size=268435456 --cuda-memory-pool-byte-size=0:268435456 --log-verbose=0 --disable-auto-complete-config
