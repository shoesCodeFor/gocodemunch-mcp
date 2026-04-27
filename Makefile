.PHONY: help build build-all build-mcp build-parity build-slo-bench test smoke vector-up vector-down vector-health vector-smoke bench race fmt clean

BINDIR := bin
MCP_BIN := $(BINDIR)/gocodemunch-mcp
PARITY_BIN := $(BINDIR)/gocodemunch-parity
SLO_BENCH_BIN := $(BINDIR)/gocodemunch-slo-bench
VECTOR_COMPOSE_FILE := docker-compose.vector.yml
DOCKER_COMPOSE ?= docker compose

help:
	@printf "Common targets:\n"
	@printf "  make build          Build the main MCP binary\n"
	@printf "  make build-all      Build all project binaries\n"
	@printf "  make build-mcp      Build $(MCP_BIN)\n"
	@printf "  make build-parity   Build $(PARITY_BIN)\n"
	@printf "  make build-slo-bench Build $(SLO_BENCH_BIN)\n"
	@printf "  make test           Run the full Go test suite\n"
	@printf "  make smoke          Run stdio startup smoke test\n"
	@printf "  make vector-up      Start local Qdrant vector stack and wait for health\n"
	@printf "  make vector-down    Stop local Qdrant vector stack\n"
	@printf "  make vector-health  Print and verify local Qdrant health status\n"
	@printf "  make vector-smoke   Run local vector retrieval smoke test\n"
	@printf "  make fmt            Run gofmt across the repo\n"
	@printf "  make clean          Remove built binaries\n"
	@printf "  make bench          Run benchmark script\n"
	@printf "  make race           Run race script\n"

build: build-mcp

build-all: build-mcp build-parity build-slo-bench

build-mcp:
	mkdir -p $(BINDIR)
	go build -o $(MCP_BIN) ./src/cmd/gocodemunch-mcp

build-parity:
	mkdir -p $(BINDIR)
	go build -o $(PARITY_BIN) ./src/cmd/gocodemunch-parity

build-slo-bench:
	mkdir -p $(BINDIR)
	go build -o $(SLO_BENCH_BIN) ./src/cmd/gocodemunch-slo-bench

test:
	go test ./...

smoke:
	go test ./tests-go -run TestStdIOServerStartupSmoke -v

vector-up:
	$(DOCKER_COMPOSE) -f $(VECTOR_COMPOSE_FILE) up -d --wait --quiet-pull

vector-down:
	$(DOCKER_COMPOSE) -f $(VECTOR_COMPOSE_FILE) down --remove-orphans

vector-health:
	@status="$$( $(DOCKER_COMPOSE) -f $(VECTOR_COMPOSE_FILE) ps --format json qdrant )"; \
	printf '%s\n' "$$status"; \
	echo "$$status" | grep -q '"Health":"healthy"'

vector-smoke:
	./scripts/vector-smoke.sh

fmt:
	go fmt ./...

bench:
	./scripts/run-wp11-benchmark.sh

race:
	./scripts/run-wp10-race.sh

clean:
	rm -rf $(BINDIR)
