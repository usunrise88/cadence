#!/usr/bin/env bash
# E1: build a TensorRT engine from a step.onnx with trtexec in the Triton image (TensorRT 11.2), one optimisation
# profile B = 1..64 (opt 32), the 80 ms buffer (17 frames).
#   docs/spikes/e1/triton/trt_build.sh <step.onnx> <out.plan> [trtexec flags...]   e.g. --noTF32
set -euo pipefail
ONNX=${1:?step.onnx}
PLAN=${2:?out.plan}
shift 2
T=${FRAMES:-17}
shapes() {
  local b=$1
  echo "audio_signal:${b}x128x${T},length:${b}x1,start:${b}x1,prompt:${b}x1,kv_cache_in:${b}x24x56x1024,conv_cache_in:${b}x24x1024x8,cache_fill_in:${b}x1,lstm_h_in:${b}x2x640,lstm_c_in:${b}x2x640,prev_token_in:${b}x1"
}
exec docker run --rm --name e1-trtexec --gpus all -v /cadence/spikes/e1:/cadence/spikes/e1 -u "$(id -u):$(id -g)" \
  nvcr.io/nvidia/tritonserver:26.08-py3 trtexec --onnx="$ONNX" --saveEngine="$PLAN" \
  --minShapes="$(shapes 1)" --optShapes="$(shapes 32)" --maxShapes="$(shapes 64)" --memPoolSize=workspace:2048M "$@"
