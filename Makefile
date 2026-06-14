# Single source of truth for the lint toolchain version; CI runs these same targets.
GOLANGCI_LINT_VERSION := v2.12.2
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build test test-integration test-sabotage lint fmt clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/redrill ./cmd/redrill

test:
	go test -race ./...

# Real-engine tests in containers (needs Docker); build tag keeps them out of `make test`.
# -p 1 serializes package binaries: the sandbox janitor reaps every redrill
# container, so two packages' container tests must not run at once (redrill is
# single-flight in production anyway).
test-integration:
	go test -race -tags integration -p 1 ./...

# The sabotage gate (TESTING.md): every fixture must be flagged. Borg/Docker
# fixtures skip when the engine is absent and block in CI where both are present.
# -p 1 serializes packages: the L3 fixtures share the janitor, which reaps every
# redrill container, so two packages' container tests must not run at once.
test-sabotage:
	go test -tags sabotage -p 1 ./...

lint:
	$(GOLANGCI_LINT) run

fmt:
	$(GOLANGCI_LINT) fmt

clean:
	rm -rf bin
