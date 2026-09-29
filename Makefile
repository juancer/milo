# All targets run inside Docker. No Go, Node or Python is needed on the host.

NEXO_UID := $(shell id -u)
NEXO_GID := $(shell id -g)
export NEXO_UID NEXO_GID

COMPOSE := docker compose -f deploy/docker-compose.yml
GO      := $(COMPOSE) run --rm go
NODE    := $(COMPOSE) run --rm node
PKGS    := ./cmd/... ./internal/... ./web/... ./systemd/...
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help test vet web-deps web-build build-arm64 dev web-dev shell clean

help:
	@echo "make test         run go vet + go test in Docker"
	@echo "make build-arm64  build the Astro UI, then dist/nexo-linux-arm64 (CGO off)"
	@echo "make dev          run Nexo with mocked hardware on http://localhost:8080"
	@echo "make web-dev      run the Astro dev server on http://localhost:4321"
	@echo "make shell        open a shell in the Go container"

vet:
	$(GO) go vet $(PKGS)

test: vet
	$(GO) go test -count=1 $(PKGS)

web-deps:
	$(NODE) corepack pnpm install --frozen-lockfile

web-build: web-deps
	$(NODE) corepack pnpm run build

build-arm64: web-build
	mkdir -p dist
	$(GO) env GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "$(LDFLAGS)" -o dist/nexo-linux-arm64 ./cmd/nexo

dev: web-build
	$(COMPOSE) up --build dev

web-dev: web-deps
	$(COMPOSE) up --build dev web-dev

shell:
	$(COMPOSE) run --rm go bash

clean:
	rm -rf dist web/admin/dist/* web/admin/.astro
	touch web/admin/dist/.gitkeep
