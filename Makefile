.DEFAULT_GOAL := help
.PHONY: help up down logs ps migrate-up migrate-down migrate-version \
        test test-race test-integration test-all vet fmt fmt-check check \
        scale queues token smoke

POSTGRES_ENV := POSTGRES_HOST=localhost POSTGRES_PORT=5432 POSTGRES_USER=jungle \
                POSTGRES_PASSWORD=jungle POSTGRES_DB=jungle POSTGRES_SSLMODE=disable

INTEGRATION_ENV := TEST_POSTGRES_DSN="postgres://jungle:jungle@localhost:5432/jungle?sslmode=disable" \
                   TEST_KEYCLOAK_URL=http://localhost:8081

LIFECYCLE_ENV := JUNGLE_PORT=18080 $(POSTGRES_ENV) \
                 AWS_REGION=us-east-1 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test \
                 SQS_ENDPOINT_URL=http://localhost:4566 \
                 SQS_REQUEST_QUEUE_URL=http://localhost:4566/000000000000/wager-transactions.fifo \
                 SQS_EVENT_QUEUE_URL=http://localhost:4566/000000000000/wager-events.fifo \
                 OIDC_ISSUER_URL=http://localhost:8081/realms/jungle OIDC_AUDIENCE=jungle-api

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

up: ## Build and start the whole stack
	docker compose up --build -d
	@echo "waiting for readiness..."
	@for i in $$(seq 1 60); do \
		if curl -sf http://localhost:8080/health/ready >/dev/null 2>&1; then \
			echo "ready:"; curl -s http://localhost:8080/health/ready; echo; exit 0; \
		fi; sleep 2; \
	done; echo "did not become ready"; docker compose logs --tail 50; exit 1

down: ## Stop everything and drop volumes
	docker compose down -v

deps: ## Start only the dependencies (for running the app or tests on the host)
	docker compose up -d postgres localstack keycloak adminer

logs: ## Tail the application logs
	docker compose logs -f app

ps: ## Show container status
	docker compose ps

scale: ## Run three independent app instances on ports 8090-8092
	docker compose -f docker-compose.yml -f scale.override.yml up -d --scale app=3
	docker compose ps

queues: ## List the SQS queues and their attributes
	docker compose exec -T localstack awslocal sqs list-queues --region us-east-1

migrate-up: ## Apply every pending migration
	$(POSTGRES_ENV) go run ./cmd/migrate up

migrate-down: ## Revert every migration
	$(POSTGRES_ENV) go run ./cmd/migrate down

migrate-version: ## Print the current schema version
	$(POSTGRES_ENV) go run ./cmd/migrate version

fmt: ## Format the code
	gofmt -w .

fmt-check: ## Fail if anything is unformatted
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "not gofmt'd:"; echo "$$unformatted"; exit 1; fi
	@echo "gofmt clean"

vet: ## Run go vet, including the integration build tag
	go vet ./...
	go vet -tags=integration ./...

test: ## Unit tests
	go test ./...

test-race: ## Unit tests with the race detector
	go test -race ./...

test-integration: ## Integration tests (needs `make deps` and `make migrate-up` first)
	$(INTEGRATION_ENV) go test -tags=integration -race -count=1 ./internal/integration/...
	$(LIFECYCLE_ENV) go test -tags=integration -count=1 ./cmd/jungle/...

test-all: test-race test-integration ## Every test

check: fmt-check vet test-race ## Everything CI runs without external dependencies

token: ## Print an access token for the internal service
	@curl -s -X POST http://localhost:8081/realms/jungle/protocol/openid-connect/token \
		-d grant_type=client_credentials -d client_id=jungle-internal -d client_secret=internal-secret \
		| python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])'
