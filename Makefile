SHELL := /bin/sh
GO ?= go
COMPOSE ?= docker compose

.PHONY: setup dev down test test-race lint build migrate-up migrate-down integration incident-simulations security fmt vet docker-build

setup:
	cp -n .env.example .env || true
	$(GO) mod download

dev:
	$(COMPOSE) up --build

down:
	$(COMPOSE) down

fmt:
	$(GO) fmt ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run

build:
	$(GO) build ./...

migrate-up:
	$(COMPOSE) run --rm migrate

migrate-down:
	$(COMPOSE) run --rm --entrypoint /usr/local/bin/migrate migrate down

integration:
	$(GO) test -tags=integration -p 1 ./internal/...

incident-simulations:
	GO=$(GO) sh scripts/incident-simulations

security:
	govulncheck ./...

docker-build:
	docker build -t eth402/facilitator:local .
