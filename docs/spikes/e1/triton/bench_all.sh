#!/usr/bin/env bash
# E1 step 3: for each repository variant start Triton, run the stream levels, record the card, stop Triton.
#   docs/spikes/e1/triton/bench_all.sh <variant>[:levels] ...   e.g. fp16 trt:1,8,16,32,64,96,128,160
# variant = a repository under /cadence/spikes/e1/triton/repo-<variant>; results in /cadence/spikes/e1/results.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
OUT=/cadence/spikes/e1/results
SECONDS_PER_LEVEL=${LEVEL_SECONDS:-45}
mkdir -p "$OUT"
for spec in "$@"; do
  v=${spec%%:*}
  levels=${spec#*:}
  [[ $levels == "$spec" ]] && levels=1,8,16,32,48,64,96,128
  docker rm -f e1-triton >/dev/null 2>&1
  "$HERE/serve.sh" "/cadence/spikes/e1/triton/repo-$v" > "$OUT/triton-$v.serve.log" 2>&1 &
  ok=0
  for _ in $(seq 1 150); do
    if curl -sf http://127.0.0.1:18400/v2/models/nemotron_step/ready >/dev/null; then ok=1; break; fi
    sleep 2
  done
  if [[ $ok != 1 ]]; then echo "== $v did not become ready"; tail -20 "$OUT/triton-$v.serve.log"; continue; fi
  # the card while the levels run: total use, utilisation and Triton's own memory (the process named exactly
  # tritonserver: the stand's other Triton renames itself), every second
  (while true; do
     t=$(date +%s); g=$(nvidia-smi --query-gpu=memory.used,utilization.gpu --format=csv,noheader,nounits)
     m=$(nvidia-smi --query-compute-apps=process_name,used_memory --format=csv,noheader,nounits | awk -F', ' '$1=="tritonserver"{print $2}')
     echo "$t,$g,${m:-0}"; sleep 1
   done) > "$OUT/triton-$v.gpu.csv" &
  mon=$!
  echo "== $v $levels $(date -Is)"
  "$HERE/client.sh" python /repo/docs/spikes/e1/triton/client.py /work/bench/feats-80.npz --streams "$levels" \
    --seconds "$SECONDS_PER_LEVEL" --procs "${PROCS:-4}" --label "$v" --out "/work/results/triton-$v.json" 2>&1 | grep -E '^\{|error'
  kill $mon
  echo "triton peak MiB $(cut -d, -f4 "$OUT/triton-$v.gpu.csv" | sort -n | tail -1), card util p50 $(cut -d, -f3 "$OUT/triton-$v.gpu.csv" | sort -n | awk '{a[NR]=$1} END{print a[int(NR/2)+1]}')%"
  docker stop e1-triton >/dev/null
  wait
done
