BINARY    := jira-ai-creator
MODULE    := github.com/kengou/jira-ticket-creator
GO        := go
GOFLAGS   :=
LDFLAGS   := -s -w

# Build metadata (overridable via environment or CLI)
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: all build test vet lint validate clean install fmt tidy verify-schema-sync help

## all: build the binary (default target)
all: build

## build: compile the binary
build: $(BINDIR)
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/$(BINARY) .

## test: run all tests
test:
	$(GO) test $(GOFLAGS) -race ./...

## vet: run go vet
vet:
	$(GO) vet ./...

BINDIR ?= $(shell pwd)/bin
GOLINT_VERSION ?= 2.14.0
GOLINT ?= $(BINDIR)/golangci-lint

$(BINDIR):
	mkdir -p $(BINDIR)

$(GOLINT): $(BINDIR)
	GOBIN=$(BINDIR) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLINT_VERSION)

## lint: always run golangci-lint (installs if missing)
lint: vet $(GOLINT)
	$(GOLINT) run --timeout=5m

## validate: run vet, lint, and tests
validate: vet lint test

## fmt: format all Go source files
fmt:
	$(GO) fmt ./...

## verify-schema-sync: check that the embedded schema copy matches the canonical schema
verify-schema-sync:
	@diff schema/stories.schema.json internal/validation/stories.schema.json || \
		(echo "ERROR: schema files are out of sync; run: cp schema/stories.schema.json internal/validation/" && exit 1)

## tidy: tidy and verify module dependencies
tidy:
	$(GO) mod tidy
	$(GO) mod verify

## clean: remove build artifacts
clean:
	rm -f $(BINDIR)/$(BINARY)

## install: install the binary to $GOPATH/bin
install:
	$(GO) install $(GOFLAGS) -ldflags "$(LDFLAGS)" .

## docker-build: build the Docker image
docker-build:
	docker build -t $(BINARY):$(VERSION) -t $(BINARY):latest .

## help: show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
