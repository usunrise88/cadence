#!/usr/bin/env bash
# E1 step 2: every parity run of the brief, one container each (docs/spikes/e1/run.sh), results in /cadence/spikes/e1/results.
#   docs/spikes/e1/parity_all.sh [runs...]   runs: 80b8 80b1 160b8 1120b8 80fp16b8 (default: the first four)
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
M=/art/hf/hub/models--nvidia--nemotron-3.5-asr-streaming-0.6b/snapshots/ea30d66debe3740a08b573244286791d423d6b3e/nemotron-3.5-asr-streaming-0.6b.nemo
runs=("$@")
[[ ${#runs[@]} -eq 0 ]] && runs=(80b8 80b1 160b8 1120b8)
for r in "${runs[@]}"; do
  case $r in
    80b8) args=(/work/onnx/base-80 /work/results/parity-80-b8.json --batch 8 --ort-batch 8) ;;
    80b1) args=(/work/onnx/base-80 /work/results/parity-80-b1.json --batch 1 --ort-batch 1) ;;
    160b8) args=(/work/onnx/base-160 /work/results/parity-160-b8.json --batch 8 --ort-batch 8) ;;
    160b1) args=(/work/onnx/base-160 /work/results/parity-160-b1.json --batch 1 --ort-batch 1) ;;
    1120b8) args=(/work/onnx/base-1120 /work/results/parity-1120-b8.json --batch 8 --ort-batch 8 --provider cpu) ;;
    80fp16b8) args=(/work/onnx/serve-80-fp16 /work/results/parity-80-fp16-b8.json --batch 8 --ort-batch 8) ;;
    80b8tf0) args=(/work/onnx/base-80 /work/results/parity-80-b8-tf32off.json --batch 8 --ort-batch 8 --tf32 0) ;;
    160b8tf0) args=(/work/onnx/base-160 /work/results/parity-160-b8-tf32off.json --batch 8 --ort-batch 8 --tf32 0) ;;
    80b8cpu) args=(/work/onnx/base-80 /work/results/parity-80-b8-cpu.json --batch 8 --ort-batch 8 --provider cpu) ;;
    *) echo "unknown run $r"; exit 2 ;;
  esac
  echo "== $r $(date -Is)"
  "$HERE/run.sh" --gpu python /repo/docs/spikes/e1/parity.py "$M" "${args[@]}" 2>&1 | grep -E '^\{|Error|error:' | grep -v 'Fallback'
done
