#!/usr/bin/env bash
# Starts a throwaway Postgres (Docker) and the control plane for Playwright. Runs in the foreground; Playwright
# stops it after the run and the trap removes the database container.
set -euo pipefail
PG_PORT="${E2E_PG_PORT:-55433}"
API_PORT="${E2E_API_PORT:-18081}"
NAME="cadence-e2e-pg-$$"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DATA="$(mktemp -d)"
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$DATA"; }
trap cleanup EXIT INT TERM
# A previous run on this port killed hard leaves its database behind; remove it so the port is free. Runs on other
# ports (parallel worktrees) keep theirs.
docker ps -aq --filter "label=cadence-e2e=${PG_PORT}" | xargs -r docker rm -f >/dev/null
docker run -d --rm --name "$NAME" --label "cadence-e2e=${PG_PORT}" -e POSTGRES_USER=cadence -e POSTGRES_PASSWORD=e2e -e POSTGRES_DB=cadence \
  -p "127.0.0.1:${PG_PORT}:5432" postgres:17 >/dev/null
for _ in $(seq 1 60); do docker exec "$NAME" pg_isready -h 127.0.0.1 -U cadence >/dev/null 2>&1 && break; sleep 0.5; done
(cd "$ROOT/control-plane" && go build -o "$DATA/cadence" ./cmd/cadence)
DATABASE_URL="postgres://cadence:e2e@127.0.0.1:${PG_PORT}/cadence?sslmode=disable" \
CADENCE_ADDR="127.0.0.1:${API_PORT}" CADENCE_DATA_DIR="$DATA" CADENCE_LOG_LEVEL=warn \
  "$DATA/cadence" serve
