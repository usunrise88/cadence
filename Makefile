.PHONY: up down gen check-gen lint test test-integration ui-e2e evals conformance spikes-measure contrast e2e spikes web help-sync

up:            ## build all images with one version and start postgres, control plane, agent host
	CADENCE_VERSION=$${CADENCE_VERSION:-$$(git describe --tags --always --dirty)} docker compose up -d --build

down:
	docker compose down

gen:           ## regenerate Go server stubs, MCP tool manifest, CLI table, TS client and bundled help from api/ and docs/help
	cd control-plane && go tool oapi-codegen -config oapi-codegen.yaml ../api/openapi.yaml
	cd control-plane && go run ./cmd/mcpgen
	cd web && npx openapi-ts
	cd agent-host && npx openapi-ts
	cd worker && uv run python scripts/gen_protocol.py && uv run ruff format -q cadence_worker/protocol_gen.py
	cd control-plane && go run ./cmd/helpsync

check-gen: gen ## CI: fail when generated files are not committed
	git diff --exit-code -- control-plane/internal/api control-plane/internal/mcp control-plane/internal/cli control-plane/internal/help/content web/src/api agent-host/src/api worker/cadence_worker/protocol_gen.py \
	  || (echo "generated files are stale: run make gen and commit" && exit 1)

lint:          ## golangci-lint, eslint (panel, Dockview and Base UI rules), tsc, ruff, mypy --strict
	@! git grep -nE '^(<<<<<<<|>>>>>>>)( |$$)' -- ':!*.gen.*' || { echo 'merge conflict markers left in the files above'; exit 1; }
	cd control-plane && golangci-lint run ./...
	cd web && npx tsc -b && npx eslint . --max-warnings 0
	cd agent-host && npm run typecheck
	cd worker && uv run ruff check . && uv run ruff format --check . && uv run mypy --strict cadence_worker tests scripts packs/toy/cadence_toy packs/toy/tests packs/nemo/cadence_nemo packs/nemo/tests packs/nemo/scripts packs/services/cadence_services packs/services/tests packs/services/scripts

test:          ## unit + contract (no Docker): Go, Vitest (jsdom + headless Chromium), pytest, agent host
	cd control-plane && go test ./...
	cd web && npx vitest run && node scripts/contrast.mjs > /dev/null
	cd worker && uv run pytest
	cd agent-host && npm test

test-integration: ## control plane against Postgres in Docker (testcontainers): commands, outbox → SSE, workspaces
	cd control-plane && go test -tags integration ./...

ui-e2e:        ## Playwright on sign-in and the shell against the real control plane (Postgres in Docker)
	cd web && npx playwright test e2e/auth.spec.ts e2e/panels.spec.ts e2e/shell.spec.ts e2e/search.spec.ts e2e/mix.spec.ts e2e/chat.spec.ts e2e/agents.spec.ts

conformance:   ## framework-pack conformance suite for the CPU toy pack (R45); the NeMo pack runs it nightly in its image
	cd worker && uv run python -m cadence_worker.conformance --runtime toy --report test-results/conformance-toy.json > /dev/null

evals:         ## agent evals on fresh fixture projects (Postgres in Docker): scripted agent offline; CADENCE_LIVE_AGENTS=1 for real drivers
	cd agent-host && npm run evals

spikes-measure: ## S1, S3, S4 and A4 measurements (weekly performance job); results in web/test-results/spikes
	cd web && npx playwright test e2e/spikes.spec.ts e2e/a4-live-events.spec.ts

contrast:      ## contrast of every Theming pairing, light and dark
	cd web && node scripts/contrast.mjs

e2e:           ## smoke project on the staging card — arrives with the data block (phase 4, R34)
	@echo "make e2e: the smoke project arrives in phase 4 (docs/spec/08-resolutions.md R34)"; exit 2

web:           ## build the SPA and copy it into the control plane's embed directory (replaces the placeholder page)
	cd web && npm ci && npm run build
	rm -rf control-plane/internal/webui/dist && mkdir -p control-plane/internal/webui/dist
	cp -R web/dist/. control-plane/internal/webui/dist/

help-sync:     ## mirror docs/help into control-plane/internal/help/content (embedded in the binary)
	cd control-plane && go run ./cmd/helpsync

spikes:        ## list spike briefs and their status
	@grep -H '^Status:' docs/spikes/*.md
