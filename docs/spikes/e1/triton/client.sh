#!/usr/bin/env bash
# E1: run the benchmark client in a plain python:3.12-slim container (host network; tritonclient[grpc] and numpy in
# /cadence/spikes/e1/pyclient, installed on first use).   docs/spikes/e1/triton/client.sh python /repo/.../client.py ...
set -euo pipefail
WORK=${E1_WORK:-/cadence/spikes/e1}
REPO=$(cd "$(dirname "$0")/../../../.." && pwd)
exec docker run --rm --init --name "e1-client-$$" --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -e PYTHONPATH=/work/pyclient -e PYTHONUNBUFFERED=1 -v "$WORK:/work" -v "$REPO:/repo:ro" -w /work python:3.12-slim \
  bash -c 'test -d /work/pyclient/tritonclient || pip install -q --target /work/pyclient "tritonclient[grpc]" numpy; "$@"' _ "$@"
