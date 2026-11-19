# PFF developer commands. Run from the repository root.
COMPOSE      = docker compose -f cluster/docker-compose.yml
COMPOSE_DEV  = $(COMPOSE) -f cluster/docker-compose.dev.yml --env-file cluster/.env.dev
DEV_DB_URL   = postgres://pff:pff-dev-password@127.0.0.1:5432/pff?sslmode=disable
export GOTOOLCHAIN = local

.PHONY: help setup dev-db ms-run client-dev demo demo-down up down logs test test-ms test-integration test-client build

help: ## List targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

setup: ## Create local config files from the templates (never overwrites)
	@test -f cluster/.env.dev || cp cluster/.env.dev.example cluster/.env.dev
	@test -f cluster/.env || cp cluster/.env.example cluster/.env
	@test -f cluster/config/allowed-emails.txt || cp cluster/config/allowed-emails.txt.example cluster/config/allowed-emails.txt
	@cd client && npm ci --no-audit --no-fund
	@echo "Edit cluster/config/allowed-emails.txt (and cluster/.env for production)."

dev-db: ## Start only TimescaleDB on 127.0.0.1:5432 (dev)
	$(COMPOSE_DEV) up -d db

ms-run: ## Run the backend locally in dev mode (needs dev-db)
	cd ms && set -a && . ../cluster/.env.dev && set +a && \
	  PFF_DATABASE_URL="$(DEV_DB_URL)" PFF_ALLOWED_EMAILS_FILE=../cluster/config/allowed-emails.txt \
	  PFF_PUBLIC_APP_URL=http://localhost:5173 go run ./cmd/pff

client-dev: ## Run the Vite dev server on :5173 (proxies /api to :8080)
	cd client && npm run dev

demo: ## Full stack in dev mode (mock bank, dev sign-in) on http://localhost:8080
	$(COMPOSE_DEV) up -d --build

demo-down: ## Stop the dev stack
	$(COMPOSE_DEV) down

up: ## Full production stack (needs cluster/.env and secrets)
	$(COMPOSE) up -d --build

down: ## Stop the production stack
	$(COMPOSE) down

logs: ## Follow backend logs
	$(COMPOSE) logs -f ms

test: test-ms test-client ## Unit tests (backend + client)

test-ms: ## Backend unit tests and vet
	cd ms && go vet ./... && go test ./...

test-integration: ## Backend tests against a real TimescaleDB (needs dev-db)
	cd ms && PFF_TEST_DATABASE_URL="$(DEV_DB_URL)" go test -tags integration -count=1 ./internal/integration/...

test-client: ## Client typecheck, unit tests and production build
	cd client && npm run typecheck && npm test && npm run build

build: ## Build the container images
	$(COMPOSE) build
