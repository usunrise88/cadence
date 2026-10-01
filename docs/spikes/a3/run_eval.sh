#!/usr/bin/env bash
# A3 step 3: real cache-aware streaming decode (NeMo's speech_to_text_cache_aware_streaming_infer.py, chunk by chunk
# with encoder caches, not offline decoding re-labelled) at 80 ms [56,0] and 160 ms [56,1], then WER (wer.py).
#   docs/spikes/a3/run_eval.sh <model.nemo> <tag> [manifest]
set -euo pipefail
MODEL=$1 TAG=$2 MAN=${3:-/work/data/manifests/test_fleurs.json}
HERE=$(cd "$(dirname "$0")" && pwd)
for ctx in "56,0" "56,1"; do
  out=/work/eval/$TAG/ctx${ctx/,/_}
  "$HERE/nemo.sh" --gpu python /spike/capped.py \
    /work/NeMo/examples/asr/asr_cache_aware_streaming/speech_to_text_cache_aware_streaming_infer.py \
    model_path="$MODEL" dataset_manifest="$MAN" output_path="$out" batch_size=32 cuda=0 \
    "att_context_size=[$ctx]" target_lang=he-IL strip_lang_tags=true decoder_type=rnnt \
    2>&1 | grep -a -E "WER% of streaming|whole streaming process took|a3.cap|Error|Traceback" || true
  "$HERE/nemo.sh" bash -c "python /spike/wer.py $out/*.json" | tail -1
done
