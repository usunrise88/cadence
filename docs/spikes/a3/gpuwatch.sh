#!/usr/bin/env bash
# Sample per-process GPU memory every 2 s and the vLLM health endpoint every 30 s, to prove the resident service is
# untouched while an A3 experiment runs.   docs/spikes/a3/gpuwatch.sh <out.csv> &   (kill it when done)
# Summarise:  awk -F, '$3!~/VLLM/ {if($4>m)m=$4} END{print "peak MiB (A3 process):",m}' out.csv
out=${1:?out.csv}
echo "ts,pid,name,used_mib,vllm_http" > "$out"
i=0
while true; do
  code=""
  if (( i % 15 == 0 )); then code=$(curl -s -o /dev/null -w '%{http_code}' -m 5 localhost:16080/v1/models || echo ERR); fi
  ts=$(date +%s)
  nvidia-smi --query-compute-apps=pid,process_name,used_memory --format=csv,noheader,nounits 2>/dev/null |
    while IFS=, read -r pid name mem; do echo "$ts,${pid// /},${name// /},${mem// /},$code"; done >> "$out"
  i=$((i + 1))
  sleep 2
done
