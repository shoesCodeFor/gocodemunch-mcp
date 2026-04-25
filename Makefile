.PHONY: help build build-all build-mcp build-parity build-slo-bench test smoke bench race fmt clean

BINDIR := bin
MCP_BIN := $(BINDIR)/gocodemunch-mcp
PARITY_BIN := $(BINDIR)/gocodemunch-parity
SLO_BENCH_BIN := $(BINDIR)/gocodemunch-slo-bench

help:
	@printf "Common targets:\n"
	@printf "  make build          Build the main MCP binary\n"
	@printf "  make build-all      Build all project binaries\n"
	@printf "  make build-mcp      Build $(MCP_BIN)\n"
	@printf "  make build-parity   Build $(PARITY_BIN)\n"
	@printf "  make build-slo-bench Build $(SLO_BENCH_BIN)\n"
	@printf "  make test           Run the full Go test suite\n"
	@printf "  make smoke          Run stdio startup smoke test\n"
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

fmt:
	go fmt ./...

bench:
	./scripts/run-wp11-benchmark.sh

race:
	./scripts/run-wp10-race.sh

clean:
	rm -rf $(BINDIR)
