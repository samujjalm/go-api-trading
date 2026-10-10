# Every target runs through Docker, so only Docker is required.
# If you install Go locally you can also run `go test ./...` directly.

GO := docker compose run --rm test go

.PHONY: up down logs ps test test-integration test-all cover fmt vet tidy psql clean

up: ## Build and start Postgres + API on :8080
	docker compose up --build -d db api

down: ## Stop containers (keeps data)
	docker compose down

logs: ## Tail API logs
	docker compose logs -f api

ps:
	docker compose ps

test: ## Unit tests (domain, app, security): no database needed
	$(GO) test -race ./...

test-integration: ## Integration + end-to-end tests against real Postgres
	$(GO) test -race -tags integration -count=1 ./internal/adapters/...

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
	docker compose --profile tools down -v
