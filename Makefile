# Single source of truth for the lint toolchain version; CI runs these same targets.
GOLANGCI_LINT_VERSION := v2.12.2
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build test test-integration test-sabotage test-flake test-fuzz test-mutation lint fmt clean
.PHONY: web web-install web-lint web-fmt
.PHONY: docker-up docker-down docker-logs

# The React SPA in web/ is embedded into the binary via web/embed.go, so `make
# build` (and `go test ./...`) need web/dist present — run `make web` once, or
# keep dist committed.
NPM := npm --prefix web

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

# Determinism gate: shuffle test order and run every test twice. Injected clocks
# and no package-level mutable state must keep this green across orderings.
test-flake:
	go test -race -shuffle=on -count=2 ./...

# Bounded native fuzzing of the surfaces where a silent mis-read becomes a silent
# mis-verdict. Each target runs in isolation; raise FUZZTIME for longer CI runs.
FUZZTIME ?= 20s
test-fuzz:
	go test -run=^$$ -fuzz=^FuzzParseExpect$$ -fuzztime=$(FUZZTIME) ./internal/checks/
	go test -run=^$$ -fuzz=^FuzzParse$$ -fuzztime=$(FUZZTIME) ./internal/config/
	go test -run=^$$ -fuzz=^FuzzStageDump$$ -fuzztime=$(FUZZTIME) ./internal/exec/
	go test -run=^$$ -fuzz=^FuzzRedactNoSecretSurvives$$ -fuzztime=$(FUZZTIME) ./internal/redact/

# Mutation testing of the pure-logic verifier packages (checks, config) — where
# unit tests are the complete coverage; engine glue is integration-tested, out of
# reach here. Run periodically, not per-push (it is slow). workers=1 avoids CPU
# contention that inflates timeouts; the testcache is cleared so gremlins
# calibrates its per-mutant timeout against a real (uncached) baseline.
GREMLINS_VERSION := v0.6.0
MUTATION_FLOOR ?= 80
GREMLINS := go run github.com/go-gremlins/gremlins/cmd/gremlins@$(GREMLINS_VERSION) unleash --workers 1 --timeout-coefficient 5 --threshold-efficacy $(MUTATION_FLOOR)
test-mutation:
	go clean -testcache
	$(GREMLINS) ./internal/checks
	$(GREMLINS) ./internal/config

lint:
	$(GOLANGCI_LINT) run

fmt:
	$(GOLANGCI_LINT) fmt

# Frontend gates (LINTING.md): TypeScript strict typecheck + ESLint + Prettier.
web-install:
	$(NPM) install

web:
	$(NPM) run build

web-lint:
	$(NPM) run typecheck
	$(NPM) run lint
	$(NPM) run format:check

web-fmt:
	$(NPM) run format

# Run the product image the way it ships: built from deploy/docker/Dockerfile
# (multi-stage — web UI + static binary), wired by the compose example (volumes,
# ports, config). UI + API at http://127.0.0.1:8090/.
COMPOSE := docker compose -f deploy/compose/compose.yaml

docker-up:
	$(COMPOSE) up --build -d
	@echo "redrill running — open http://127.0.0.1:8090/  (logs: make docker-logs - stop: make docker-down)"

docker-down:
	$(COMPOSE) down

docker-logs:
	$(COMPOSE) logs -f

clean:
	rm -rf bin
