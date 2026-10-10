# Every target except `run` works with only Docker installed.
# `make help` lists the targets; README "Local development and debugging" explains them.

GO := docker compose run --rm test go

# Narrow a test run: make test PKG=./internal/app/ RUN=TestPriceTimePriority
PKG ?= ./...
RUN ?=
RUN_FLAG = $(if $(RUN),-run '$(RUN)')

# Used by `make run` (native Go) to reach the dockerised Postgres.
POSTGRES_PORT ?= 5432
LOCAL_ENV = DATABASE_URL='postgres://trading:trading@localhost:$(POSTGRES_PORT)/trading?sslmode=disable' \
	TEST_DATABASE_URL='postgres://trading:trading@localhost:$(POSTGRES_PORT)/trading_test?sslmode=disable' \
	JWT_SECRET=dev-secret-change-me-dev-secret-change-me

.PHONY: help up down logs ps db run debug debug-stop dlv debug-test test test-integration test-all cover fmt vet tidy psql clean

up: ## Build and start Postgres + API on :8080
	docker compose up --build -d db api

down: ## Stop containers (keeps data)
	docker compose down

logs: ## Tail API logs (normal or debug container, whichever is running)
	docker compose --profile debug logs -f api api-debug

ps:
	docker compose ps

db: ## Start only Postgres (for running the API natively)
	docker compose up -d --wait db

run: db ## Run the API natively with `go run` (needs Go installed); DB in Docker
	docker compose stop api api-debug >/dev/null 2>&1 || true
	$(LOCAL_ENV) go run ./cmd/api

debug: ## Run the API under Delve in Docker; attach a debugger to :2345
	docker compose stop api
	docker compose --profile debug up --build -d --wait db api-debug
	@echo "API on :8080, Delve on :2345. Attach your IDE, or: make dlv"

debug-stop: ## Stop the debug container and bring the normal API back
	docker compose --profile debug stop api-debug
	docker compose up -d api

dlv: ## Open the Delve CLI attached to the running debug container
	docker compose exec api-debug dlv connect localhost:2345

debug-test: ## Step through tests in Delve: make debug-test PKG=./internal/app/ RUN=TestCancelOrder
	docker compose --profile tools run --rm test go run github.com/go-delve/delve/cmd/dlv@v1.27.2 test $(if $(filter ./...,$(PKG)),$(error debug-test needs one package, e.g. PKG=./internal/app/),$(PKG)) -- -test.run '$(RUN)'

test: ## Unit tests (domain, app, security): no database needed
	$(GO) test -race $(RUN_FLAG) $(PKG)

test-integration: ## Integration + end-to-end tests against real Postgres
	$(GO) test -race -tags integration -count=1 $(RUN_FLAG) $(if $(filter ./...,$(PKG)),./internal/adapters/...,$(PKG))

test-all: ## Everything
	$(GO) test -race -tags integration -count=1 ./...

cover: ## Coverage report for all tests -> coverage.html
	$(GO) test -tags integration -count=1 -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet -tags integration ./...

tidy:
	$(GO) mod tidy

psql: ## Open a psql shell on the dev database
	docker compose exec db psql -U trading -d trading

clean: ## Stop everything and delete volumes (wipes the database)
	docker compose --profile tools --profile debug down -v

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-17s %s\n", $$1, $$2}'

