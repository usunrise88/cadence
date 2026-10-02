#!/usr/bin/env bash
# Sample per-process GPU memory every second (nvidia-smi, host pids) and the host pids of our containers, so a live
# job's memory and the other residents' memory can be read off afterwards.
#   docs/spikes/a5/gpuwatch.sh <out.csv> [container...] &      (kill it when done)
out=${1:?out.csv}
shift
echo "ts,pid,name,used_mib,tag" > "$out"
while true; do
  ts=$(date +%s)
  declare -A tag=()
  for c in "$@"; do
    for p in $(docker top "$c" -eo pid 2>/dev/null | tail -n +2); do tag[$p]=$c; done
  done
  nvidia-smi --query-compute-apps=pid,process_name,used_memory --format=csv,noheader,nounits 2>/dev/null |
    while IFS=, read -r pid name mem; do
      pid=${pid// /}
      echo "$ts,$pid,${name// /},${mem// /},${tag[$pid]:-}"
    done >> "$out"
  unset tag
  sleep 1
done
