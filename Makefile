.PHONY: up down gen lint test e2e spikes web help-sync

up:            ## start postgres, control-plane stub, agent-host stub
	docker compose up -d --build

down:
	docker compose down

gen:           ## regenerate Go server stubs, TS client and MCP tool manifest from api/openapi.yaml
	@echo "TODO: oapi-codegen -config control-plane/oapi.yaml api/openapi.yaml"
	@echo "TODO: npx @hey-api/openapi-ts -i api/openapi.yaml -o web/src/api"
	@echo "TODO: go run ./control-plane/cmd/mcpgen api/openapi.yaml > control-plane/internal/mcp/tools.json"

lint:          ## golangci-lint, eslint, ruff, mypy
	@echo "TODO: (cd control-plane && golangci-lint run ./...)"
	@echo "TODO: (cd web && npx eslint .)"
	@echo "TODO: (cd worker && ruff check . && mypy --strict cadence_worker)"

test:          ## unit + contract + integration
	@echo "TODO: (cd control-plane && go test ./...) && (cd web && npm test) && (cd worker && pytest) && (cd agent-host && npm test)"

e2e:           ## smoke project on the staging card — run only from the staging host
	@echo "TODO: cadence smoke --project cadence-smoke"

web:           ## build the SPA and copy it into the control plane's embed directory (replaces the placeholder page)
	cd web && npm ci && npm run build
	rm -rf control-plane/internal/webui/dist && mkdir -p control-plane/internal/webui/dist
	cp -R web/dist/. control-plane/internal/webui/dist/

help-sync:     ## mirror docs/help into control-plane/internal/help/content (embedded in the binary)
	cd control-plane && go run ./cmd/helpsync

spikes:        ## list spike briefs and their status
	@grep -H '^Status:' docs/spikes/*.md
