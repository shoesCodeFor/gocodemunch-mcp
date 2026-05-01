.PHONY: help build build-all build-mcp build-parity build-slo-bench test smoke vector-up vector-down vector-health vector-smoke eval-smoke eval-matrix eval-gate eval-savings-smoke bench race fmt clean

BINDIR := bin
MCP_BIN := $(BINDIR)/gocodemunch-mcp
PARITY_BIN := $(BINDIR)/gocodemunch-parity
SLO_BENCH_BIN := $(BINDIR)/gocodemunch-slo-bench
VECTOR_COMPOSE_FILE := docker-compose.vector.yml
DOCKER_COMPOSE ?= docker compose
EVAL_CMD := go run ./src/cmd/gocodemunch-eval
EVAL_FIXTURES_DIR ?= tests-go/evals/fixtures
EVAL_TOKEN_SAVINGS_FIXTURES_DIR ?= tests-go/evals/fixtures/token-savings-smoke
EVAL_NAMESPACE_PREFIX ?= eval-fixtures
EVAL_MARKDOWN_REPORT_DIR ?= docs/evals/runs
EVAL_OUTPUT_DIR ?= Auto Run Docs/Working/evals
EVAL_SMOKE_PROVIDERS ?= ollama
EVAL_SMOKE_BACKENDS ?= sqlite
EVAL_MATRIX_PROVIDERS ?= ollama,vllm
EVAL_MATRIX_BACKENDS ?= sqlite,qdrant
EVAL_GATE_PROVIDERS ?= $(EVAL_MATRIX_PROVIDERS)
EVAL_GATE_BACKENDS ?= $(EVAL_MATRIX_BACKENDS)
EVAL_GATE_MIN_MEAN_RECALL_AT_K ?= 0.70
EVAL_GATE_MIN_MEAN_MRR_AT_K ?= 0.70
EVAL_GATE_MAX_P50_LATENCY_MS ?= 5000
EVAL_GATE_MAX_P95_LATENCY_MS ?= 5000

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
	@printf "  make eval-smoke     Run non-interactive eval smoke (ollama/sqlite default)\n"
	@printf "  make eval-matrix    Run non-interactive eval matrix (ollama,vllm x sqlite,qdrant default)\n"
	@printf "  make eval-gate      Run non-interactive eval matrix with default thresholds (0.70 recall/mrr, 5000ms p50/p95)\n"
	@printf "  make eval-savings-smoke Run token savings smoke benchmark and write JSON output\n"
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

eval-smoke:
	mkdir -p "$(EVAL_OUTPUT_DIR)"
	$(EVAL_CMD) \
		--fixtures-dir "$(EVAL_FIXTURES_DIR)" \
		--providers "$(EVAL_SMOKE_PROVIDERS)" \
		--backends "$(EVAL_SMOKE_BACKENDS)" \
		--namespace-prefix "$(EVAL_NAMESPACE_PREFIX)" \
		--markdown-report-dir "$(EVAL_MARKDOWN_REPORT_DIR)" \
		--out "$(EVAL_OUTPUT_DIR)/eval-smoke.json" \
		--skip-markdown-report

eval-matrix:
	mkdir -p "$(EVAL_OUTPUT_DIR)"
	$(EVAL_CMD) \
		--fixtures-dir "$(EVAL_FIXTURES_DIR)" \
		--providers "$(EVAL_MATRIX_PROVIDERS)" \
		--backends "$(EVAL_MATRIX_BACKENDS)" \
		--namespace-prefix "$(EVAL_NAMESPACE_PREFIX)" \
		--markdown-report-dir "$(EVAL_MARKDOWN_REPORT_DIR)" \
		--out "$(EVAL_OUTPUT_DIR)/eval-matrix.json" \
		--skip-markdown-report

eval-gate:
	mkdir -p "$(EVAL_OUTPUT_DIR)"
	$(EVAL_CMD) \
		--fixtures-dir "$(EVAL_FIXTURES_DIR)" \
		--providers "$(EVAL_GATE_PROVIDERS)" \
		--backends "$(EVAL_GATE_BACKENDS)" \
		--namespace-prefix "$(EVAL_NAMESPACE_PREFIX)" \
		--markdown-report-dir "$(EVAL_MARKDOWN_REPORT_DIR)" \
		--min-mean-recall-at-k "$(EVAL_GATE_MIN_MEAN_RECALL_AT_K)" \
		--min-mean-mrr-at-k "$(EVAL_GATE_MIN_MEAN_MRR_AT_K)" \
		--max-p50-latency-ms "$(EVAL_GATE_MAX_P50_LATENCY_MS)" \
		--max-p95-latency-ms "$(EVAL_GATE_MAX_P95_LATENCY_MS)" \
		--out "$(EVAL_OUTPUT_DIR)/eval-gate.json" \
		--skip-markdown-report

eval-savings-smoke:
	mkdir -p "$(EVAL_OUTPUT_DIR)"
	$(EVAL_CMD) \
		--mode token-savings-smoke \
		--fixtures-dir "$(EVAL_TOKEN_SAVINGS_FIXTURES_DIR)" \
		--out "$(EVAL_OUTPUT_DIR)/token-savings-smoke.json" \
		--skip-markdown-report

fmt:
	go fmt ./...

bench:
	./scripts/run-wp11-benchmark.sh

race:
	./scripts/run-wp10-race.sh

clean:
	rm -rf $(BINDIR)
