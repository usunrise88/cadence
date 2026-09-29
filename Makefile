.PHONY: up down gen check-gen lint test test-integration ui-e2e spikes-measure contrast e2e spikes web help-sync

up:            ## build all images with one version and start postgres, control plane, agent host
	CADENCE_VERSION=$${CADENCE_VERSION:-$$(git describe --tags --always --dirty)} docker compose up -d --build

down:
	docker compose down

gen:           ## regenerate Go server stubs, MCP tool manifest, TS client and bundled help from api/ and docs/help
	cd control-plane && go tool oapi-codegen -config oapi-codegen.yaml ../api/openapi.yaml
	cd control-plane && go run ./cmd/mcpgen
	cd web && npx openapi-ts
	cd control-plane && go run ./cmd/helpsync

check-gen: gen ## CI: fail when generated files are not committed
	git diff --exit-code -- control-plane/internal/api control-plane/internal/mcp control-plane/internal/help/content web/src/api \
	  || (echo "generated files are stale: run make gen and commit" && exit 1)

lint:          ## golangci-lint, eslint (panel, Dockview and Base UI rules), tsc, ruff, mypy --strict
	cd control-plane && golangci-lint run ./...
	cd web && npx tsc -b && npx eslint . --max-warnings 0
	cd agent-host && npm run typecheck
	cd worker && uv run ruff check . && uv run ruff format --check . && uv run mypy --strict cadence_worker tests

test:          ## unit + contract (no Docker): Go, Vitest (jsdom + headless Chromium), pytest, agent host
	cd control-plane && go test ./...
	cd web && npx vitest run && node scripts/contrast.mjs > /dev/null
	cd worker && uv run pytest
	cd agent-host && npm test

test-integration: ## control plane against Postgres in Docker (testcontainers): commands, outbox → SSE, workspaces
	cd control-plane && go test -tags integration ./...

ui-e2e:        ## Playwright on the shell against the real control plane (Postgres in Docker)
	cd web && npx playwright test e2e/shell.spec.ts

spikes-measure: ## S1, S3, S4 measurements (weekly performance job); results in web/test-results/spikes
	cd web && npx playwright test e2e/spikes.spec.ts

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
