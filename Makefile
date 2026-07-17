.DEFAULT_GOAL := help
SHELL := /usr/bin/env bash

BINARY     := hubcdn
IMAGE      := hubcdn:latest
COMPOSE    := docker compose

DEPLOY_HOST ?= dev@192.168.1.3
# Relative to the remote user's home directory. Deliberately not "~/hubcdn":
# bash tilde-expands VAR=value assignments locally using *your* $HOME before
# the value ever reaches ssh, not the remote user's. A bare relative path
# has no such ambiguity - non-interactive ssh commands always start in the
# remote user's own home directory.
DEPLOY_DIR  ?= hubcdn

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN{FS=":.*## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## --- Local development ---

.PHONY: generate
generate: ## Regenerate templ view code
	templ generate

.PHONY: build
build: generate ## Build the hubcdn binary
	go build -trimpath -o $(BINARY) ./cmd/hubcdn

.PHONY: dev
dev: generate ## Run locally against staging ACME with a local data dir
	HUBCDN_DATA_DIR=./data \
	HUBCDN_HTTPS_ADDR=:4403 \
	HUBCDN_ACME_STAGING=true \
	HUBCDN_ACME_EMAIL=$${HUBCDN_ACME_EMAIL:-dev@localhost} \
	HUBCDN_PUBLIC_IPS=$${HUBCDN_PUBLIC_IPS:-127.0.0.1} \
	HUBCDN_HOSTNAME=$${HUBCDN_HOSTNAME:-localhost} \
	go run ./cmd/hubcdn

.PHONY: test
test: ## Run the test suite
	go test ./...

.PHONY: test-race
test-race: ## Run the test suite with the race detector
	go test -race ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt-formatted
	@diff -u <(echo -n) <(gofmt -l .)

.PHONY: check
check: fmt-check vet test ## Run fmt-check, vet and tests

.PHONY: clean
clean: ## Remove local build artifacts
	rm -f $(BINARY)

## --- Docker ---

.PHONY: docker-build
docker-build: ## Build the production Docker image
	docker build -t $(IMAGE) .

.PHONY: up
up: ## Start the stack with docker-compose (reads .env)
	$(COMPOSE) up -d --build

.PHONY: down
down: ## Stop the stack
	$(COMPOSE) down

.PHONY: logs
logs: ## Follow the stack's logs
	$(COMPOSE) logs -f

.PHONY: ps
ps: ## Show stack status
	$(COMPOSE) ps

## --- Deployment ---

.PHONY: deploy
deploy: ## Deploy to the production server (dev@192.168.1.3 by default)
	@DEPLOY_HOST=$(DEPLOY_HOST) DEPLOY_DIR=$(DEPLOY_DIR) ./scripts/deploy.sh

.PHONY: remote-logs
remote-logs: ## Tail logs on the deployed server
	ssh $(DEPLOY_HOST) 'cd $(DEPLOY_DIR) && docker compose logs -f --tail=200'

.PHONY: remote-status
remote-status: ## Show container status on the deployed server
	ssh $(DEPLOY_HOST) 'cd $(DEPLOY_DIR) && docker compose ps'

.PHONY: remote-down
remote-down: ## Stop the stack on the deployed server
	ssh $(DEPLOY_HOST) 'cd $(DEPLOY_DIR) && docker compose down'
