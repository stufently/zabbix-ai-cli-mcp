# Builds and tests run inside Linux containers, so a host with an older Go
# toolchain still produces a correct Linux binary. Keep this on the latest
# stable Go release. Use the Go toolchain directly for a host-native binary.
GO_IMAGE   ?= golang:1.27.0
GOLANGCI_LINT_VERSION ?= v2.13.1
BINARY     := zabbix-ai-cli-mcp
PKG        := ./cmd/zabbix-ai-cli-mcp
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS    := -s -w -X github.com/stufently/zabbix-ai-cli-mcp/internal/cli.Version=$(VERSION)
UID        := $(shell id -u)
GID        := $(shell id -g)
# The cache lives outside the working tree: it is bind-mounted into /src, so a
# cache inside the repository puts a downloaded Go toolchain's sources under
# "gofmt -l ." — which fails fmt-check on files that are not ours, and which
# "make fmt" would happily rewrite.
CACHE      ?= $(HOME)/.cache/zabbix-ai-cli-mcp

# Containers write into bind mounts as the invoking user, so build artefacts and
# caches stay owned by the person who ran make rather than by root.
GO = docker run --rm -u $(UID):$(GID) \
	-v $(CURDIR):/src -v $(CACHE):/cache \
	-e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod -e GOFLAGS=-buildvcs=false \
	-w /src $(GO_IMAGE)

.PHONY: all build test race vet fmt fmt-check lint tidy docker clean install help

all: fmt-check vet test build

## build: compile a Linux binary into ./bin using Docker
build:
	@mkdir -p bin $(CACHE)
	$(GO) go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)
	@echo "built bin/$(BINARY) $(VERSION)"

## test: run the test suite
test:
	@mkdir -p $(CACHE)
	$(GO) go test ./...

## race: run the test suite under the race detector
race:
	@mkdir -p $(CACHE)
	$(GO) go test -race ./...

## vet: run go vet
vet:
	@mkdir -p $(CACHE)
	$(GO) go vet ./...

## fmt: rewrite sources with gofmt
fmt:
	$(GO) gofmt -w .

## fmt-check: fail if anything is unformatted
fmt-check:
	@out=$$($(GO) gofmt -l .); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

## lint: run golangci-lint
lint:
	@mkdir -p $(CACHE)
	docker run --rm -u $(UID):$(GID) -v $(CURDIR):/src -v $(CACHE):/cache \
		-e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod -e GOLANGCI_LINT_CACHE=/cache/lint \
		-w /src golangci/golangci-lint:$(GOLANGCI_LINT_VERSION)-alpine golangci-lint run

## tidy: tidy go.mod and go.sum
tidy:
	$(GO) go mod tidy

## docker: build the container image
docker:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):$(VERSION) -t $(BINARY):latest .

## install: build and install the Linux binary into ~/bin (Linux hosts only)
install:
	@if [ "$(shell uname -s)" != "Linux" ]; then \
		echo "make install produces a Linux binary; use 'go install ./cmd/zabbix-ai-cli-mcp' for a host-native install"; \
		exit 1; \
	fi
	$(MAKE) build
	@mkdir -p $(HOME)/bin
	install -m 0755 bin/$(BINARY) $(HOME)/bin/$(BINARY)
	@echo "installed $(HOME)/bin/$(BINARY)"

## clean: remove build artefacts
clean:
	rm -rf bin dist

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
