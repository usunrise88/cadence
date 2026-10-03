#!/usr/bin/env bash
# Nightly GPU checks on the staging host (owner decision 2026-10-03, docs/spec/00-overview.md decision log): the NeMo
# pack's GPU tests and its conformance suite, run from `main` in the nemo-speech worker image, reported to Telegram.
# It replaces the self-hosted GitHub runner the public repository cannot safely have: it checks out `main` only and
# opens nothing to the outside.
#
#   scripts/nightly-gpu.sh            # what cron runs (see scripts/README.md for the crontab line)
#   NIGHTLY_SKIP_CONFORMANCE=1 …      # the GPU tests only
#
# State lives in $NIGHTLY_HOME (default /cadence/nightly, the data volume: a conformance run writes checkpoints and
# training states, several GB, which the root disk has no room for): repo/ (a clone of origin's main, never the working tree),
# logs/ (30 days), work/ (the conformance scratch). Telegram is optional: $NIGHTLY_HOME/telegram.env (mode 600) with
# TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID; without it the summary only goes to the log.
#
# The card is shared (vLLM and other services, and the stand's own training jobs). The GPU tests need about 8 GB; the
# conformance trains under the staging cap (22 GB) and runs only when that much is free, otherwise it is skipped
# and the report says so. The model comes from the stand's Hugging Face cache, read-only and offline.
set -uo pipefail

NIGHTLY_HOME="${NIGHTLY_HOME:-/cadence/nightly}"
REPO_URL="${NIGHTLY_REPO_URL:-https://github.com/usunrise88/cadence.git}"
HF_VOLUME="${NIGHTLY_HF_VOLUME:-cadence-test_artifacts}"   # the stand worker's data volume; its hf/ is the cache
CAP_MB="${NIGHTLY_CAP_MB:-22528}"
NEED_TESTS_MB=8192
NEED_CONFORMANCE_MB=$((CAP_MB + 1536))
IMAGE=cadence/worker:nightly

mkdir -p "$NIGHTLY_HOME"/{logs,work}
exec 9>"$NIGHTLY_HOME/.lock"
flock -n 9 || { echo "another nightly run holds $NIGHTLY_HOME/.lock"; exit 0; }

day=$(date -u +%F)
log="$NIGHTLY_HOME/logs/$day.log"
exec > >(tee -a "$log") 2>&1
started=$(date +%s)
echo "== nightly GPU checks $(date -u +%FT%TZ)"
find "$NIGHTLY_HOME/logs" -name '*.log' -mtime +30 -delete 2>/dev/null || true

summary=()
status=ok
note() { summary+=("$1"); echo "-- $1"; }
fail() { status=fail; note "$1"; }

free_mb() { nvidia-smi --query-gpu=memory.free --format=csv,noheader,nounits | sort -n | tail -1 | tr -d ' '; }

report() {
  local mins=$(( ($(date +%s) - started) / 60 ))
  local icon="✅"; [ "$status" = fail ] && icon="❌"
  local text
  text=$(printf '%s Cadence nightly GPU checks — %s\nmain %s · %d min\n%s\nlog: %s' "$icon" "$day" "${commit:-?}" "$mins" \
    "$(printf '• %s\n' "${summary[@]}")" "$log")
  echo "$text"
  local env="$NIGHTLY_HOME/telegram.env"
  if [ -f "$env" ]; then
    # shellcheck disable=SC1090
    . "$env"
    if [ -n "${TELEGRAM_BOT_TOKEN:-}" ] && [ -n "${TELEGRAM_CHAT_ID:-}" ]; then
      # The URL (it holds the token) goes to curl on stdin, never on its command line where ps would show it.
      printf 'url = "https://api.telegram.org/bot%s/sendMessage"\n' "$TELEGRAM_BOT_TOKEN" |
        curl -fsS -m 20 -o /dev/null -K - --data-urlencode "chat_id=${TELEGRAM_CHAT_ID}" --data-urlencode "text=${text}" \
        || echo "telegram: the report could not be sent"
    fi
  fi
}
trap report EXIT

# 1. main, in a clone of its own.
if [ ! -d "$NIGHTLY_HOME/repo/.git" ]; then
  git clone -q --branch main "$REPO_URL" "$NIGHTLY_HOME/repo" || { fail "git clone failed"; exit 1; }
fi
git -C "$NIGHTLY_HOME/repo" fetch -q origin main && git -C "$NIGHTLY_HOME/repo" reset -q --hard origin/main \
  || { fail "git fetch of main failed"; exit 1; }
commit=$(git -C "$NIGHTLY_HOME/repo" rev-parse --short HEAD)
echo "main at $commit"

# 2. The worker image of that commit.
if ! docker build -q -f "$NIGHTLY_HOME/repo/worker/Dockerfile" -t "$IMAGE" "$NIGHTLY_HOME/repo" >/dev/null; then
  fail "the worker image did not build"; exit 1
fi

run_gpu() { # run_gpu <extra docker args…> -- <command>
  docker run --rm --init --gpus all --ipc=host --ulimit memlock=-1 --ulimit stack=67108864 \
    -e HF_HOME=/stand/hf -e HF_HUB_OFFLINE=1 -e TRANSFORMERS_OFFLINE=1 \
    -v "$HF_VOLUME":/stand:ro -v "$NIGHTLY_HOME/repo":/repo:ro "$@"
}

# 3. The NeMo pack's GPU tests (pytest -m gpu): the live decoder equals the eval decode, boosting, mixed batches.
free=$(free_mb)
if [ "${free:-0}" -lt "$NEED_TESTS_MB" ]; then
  fail "GPU tests skipped: ${free} MB free on the card, ${NEED_TESTS_MB} MB needed"
else
  out=$(run_gpu -e HOME=/tmp -w /tmp "$IMAGE" sh -c \
    'pip install -q pytest >/dev/null 2>&1; cp -r /repo/worker /tmp/worker && cd /tmp/worker && \
     python -m pytest -q -m gpu packs/nemo/tests -p no:cacheprovider 2>&1 | tail -15')
  echo "$out"
  line=$(echo "$out" | grep -E '[0-9]+ (passed|failed|error)' | tail -1)
  if echo "$line" | grep -qE 'failed|error'; then fail "GPU tests: ${line:-no summary}"
  elif [ -n "$line" ]; then note "GPU tests: $line"
  else fail "GPU tests: no pytest summary (see the log)"; fi
fi

# 4. The conformance suite on the card (calibrate, train, stop, resume, average, materialize, transcribe at every
# profile, score), as .github/workflows/nightly.yml describes it.
if [ -n "${NIGHTLY_SKIP_CONFORMANCE:-}" ]; then
  note "conformance skipped (NIGHTLY_SKIP_CONFORMANCE)"
else
  free=$(free_mb)
  if [ "${free:-0}" -lt "$NEED_CONFORMANCE_MB" ]; then
    note "conformance skipped: ${free} MB free on the card, ${NEED_CONFORMANCE_MB} MB needed (a training job or another service holds it)"
  else
    rm -rf "$NIGHTLY_HOME/work/conformance" && mkdir -p "$NIGHTLY_HOME/work/conformance"
    if run_gpu --user "$(id -u):$(id -g)" -e HOME=/work -v "$NIGHTLY_HOME/work/conformance":/work "$IMAGE" \
        python -m cadence_worker.conformance --runtime nemo-speech --memory-cap-mb "$CAP_MB" \
          --help-dir /repo/docs/help --work /work --report /work/conformance-nemo.json; then
      note "conformance: passed ($(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(len(d.get("stages", d.get("checks", []))), "stages")' "$NIGHTLY_HOME/work/conformance/conformance-nemo.json" 2>/dev/null || echo report written))"
    else
      fail "conformance: failed (report: $NIGHTLY_HOME/work/conformance/conformance-nemo.json)"
    fi
  fi
fi

[ "$status" = ok ] || exit 1
