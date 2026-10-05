#!/usr/bin/env bash
# Starts a throwaway Postgres (Docker) and the control plane for Playwright. Runs in the foreground; Playwright
# stops it after the run and the trap removes the database container.
set -euo pipefail
PG_PORT="${E2E_PG_PORT:-55433}"
API_PORT="${E2E_API_PORT:-18081}"
# The port is in the name: stacks started in fresh containers share the Docker daemon but get the same low PIDs.
NAME="cadence-e2e-pg-${PG_PORT}-$$"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DATA="$(mktemp -d)"
# Test tooling (the host token, mint-agent-token) goes to web/.e2e for the Playwright specs; the agent evals
# (agent-host/evals) pass their own directory so both can run from one checkout.
TOOLS="${E2E_TOOLS_DIR:-$ROOT/web/.e2e}"
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$DATA" "$TOOLS"; }
trap cleanup EXIT INT TERM
# A previous run killed hard leaves its database behind; remove it so the port is free. Only this port's: runs on
# other ports (parallel worktrees, E2E_PG_PORT) keep theirs.
docker ps -aq --filter "label=cadence-e2e=${PG_PORT}" | xargs -r docker rm -f >/dev/null 2>&1 || true
docker run -d --rm --name "$NAME" --label "cadence-e2e=${PG_PORT}" -e POSTGRES_USER=cadence -e POSTGRES_PASSWORD=e2e -e POSTGRES_DB=cadence \
  -p "127.0.0.1:${PG_PORT}:5432" postgres:17 >/dev/null
for _ in $(seq 1 60); do docker exec "$NAME" pg_isready -h 127.0.0.1 -U cadence >/dev/null 2>&1 && break; sleep 0.5; done
(cd "$ROOT/control-plane" && go build -o "$DATA/cadence" ./cmd/cadence && go build -o "$DATA/mintagent" ./internal/e2etools/mintagent && go build -o "$DATA/seedtraining" ./internal/e2etools/seedtraining && go build -o "$DATA/seedannotation" ./internal/e2etools/seedannotation)
DSN="postgres://cadence:e2e@127.0.0.1:${PG_PORT}/cadence?sslmode=disable"
# Specs that act as an agent session (MCP with a cst_ token) mint their token through this wrapper (spike A4); it
# lives only as long as this stack.
mkdir -p "$TOOLS"
printf '#!/usr/bin/env bash\nDATABASE_URL=%q exec %q "$@"\n' "$DSN" "$DATA/mintagent" > "$TOOLS/mint-agent-token"
chmod +x "$TOOLS/mint-agent-token"
# Training fixtures for the Run / Metrics / Checkpoints specs (e2e/training.ts): base model, dataset, calibration.
printf '#!/usr/bin/env bash\nDATABASE_URL=%q CADENCE_CAS_DIR=%q exec %q "$@"\n' "$DSN" "$DATA/cas" "$DATA/seedtraining" > "$TOOLS/seed-training"
chmod +x "$TOOLS/seed-training"
# The annotation spec's frame (e2e/annotation.spec.ts): a call on a local mount under $DATA/mounts, its source and a
# segments artifact recorded in the project the spec names.
printf '#!/usr/bin/env bash\nDATABASE_URL=%q CADENCE_CAS_DIR=%q CADENCE_SEED_DIR=%q exec %q "$@"\n' "$DSN" "$DATA/cas" "$DATA/mounts" "$DATA/seedannotation" > "$TOOLS/seed-annotation"
chmod +x "$TOOLS/seed-annotation"
# The agent-host credential (cah_…) for specs that play a scripted agent host (e2e/host.ts).
# The worker credential (cwk_…) for specs that play a scripted worker (e2e/worker.ts): queue, leases, logs.
DATABASE_URL="$DSN" CADENCE_HOST_TOKEN_FILE="$TOOLS/host-token" CADENCE_WORKER_TOKEN_FILE="$TOOLS/worker-token" \
CADENCE_ADDR="127.0.0.1:${API_PORT}" CADENCE_DATA_DIR="$DATA" CADENCE_LOG_LEVEL=warn \
  "$DATA/cadence" serve
