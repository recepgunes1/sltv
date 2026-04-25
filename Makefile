# SLTV - Shared Logical Thin Volume - Makefile

GO            ?= go
GOFLAGS       ?=
LDFLAGS       ?= -s -w -X github.com/sltv/sltv/internal/version.Version=$(VERSION) -X github.com/sltv/sltv/internal/version.Commit=$(COMMIT) -X github.com/sltv/sltv/internal/version.Date=$(DATE)
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT        ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE          ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BIN_DIR       ?= bin
PKGS          ?= ./...

.PHONY: all
all: build

.PHONY: build
build: build-sltvd build-sctl

.PHONY: build-sltvd
build-sltvd:
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/sltvd ./cmd/sltvd

.PHONY: build-sctl
build-sctl:
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/sctl ./cmd/sctl

.PHONY: install
install:
	$(GO) install $(GOFLAGS) -ldflags '$(LDFLAGS)' ./cmd/sltvd ./cmd/sctl

.PHONY: test
test:
	$(GO) test $(GOFLAGS) -race -count=1 $(PKGS)

.PHONY: test-cover
test-cover:
	$(GO) test $(GOFLAGS) -race -count=1 -covermode=atomic -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out | tail -n 1

.PHONY: vet
vet:
	$(GO) vet $(PKGS)

.PHONY: fmt
fmt:
	$(GO) fmt $(PKGS)

.PHONY: lint
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed"; exit 1; }
	golangci-lint run

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: proto
proto:
	@command -v buf >/dev/null 2>&1 || { echo "buf not installed: see https://buf.build"; exit 1; }
	buf generate

.PHONY: proto-docs
proto-docs:
	@command -v protoc >/dev/null 2>&1 || { echo "protoc not installed"; exit 1; }
	@command -v protoc-gen-doc >/dev/null 2>&1 || { echo "protoc-gen-doc not installed"; exit 1; }
	protoc --doc_out=docs --doc_opt=markdown,api.md -I api/proto api/proto/v1/sltv.proto

.PHONY: docker
docker:
	docker build -f deploy/docker/Dockerfile -t sltv/sltvd:$(VERSION) .

.PHONY: clean
clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html

.PHONY: help
help:
	@echo "Targets:"
	@echo "  build         Build sltvd and sctl"
	@echo "  test          Run unit tests with race detector"
	@echo "  test-cover    Run tests with coverage"
	@echo "  proto         Regenerate gRPC code from .proto"
	@echo "  proto-docs    Regenerate docs/api.md from .proto"
	@echo "  docker        Build Docker image"
	@echo "  vet|fmt|lint  Static analysis"
	@echo "  clean         Remove build artifacts"
