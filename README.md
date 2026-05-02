# gocodemunch-mcp

Go implementation of the `gocodemunch-mcp` server and related tooling.

## Common development commands

Use the root `Makefile` for the most common workflows:

```bash
make help
make build
make build-all
make test
make smoke
make vector-up
make vector-health
make vector-down
make vector-smoke
make eval-smoke
make eval-matrix
make eval-gate
make eval-savings-smoke
make eval-savings-matrix
make fmt
make clean
```

## Build outputs

Compiled binaries are written to `./bin`:

- `bin/gocodemunch-mcp`
- `bin/gocodemunch-parity`
- `bin/gocodemunch-slo-bench`

## Target reference

- `make build` – build the main MCP server binary
- `make build-all` – build all project binaries
- `make build-mcp` – build only `bin/gocodemunch-mcp`
- `make build-parity` – build only `bin/gocodemunch-parity`
- `make build-slo-bench` – build only `bin/gocodemunch-slo-bench`
- `make test` – run the full Go test suite
- `make smoke` – run the stdio server startup smoke test
- `make vector-up` – start Qdrant via `docker-compose.vector.yml` and wait for health
- `make vector-health` – print and verify Qdrant health status from compose
- `make vector-down` – stop and remove Qdrant compose resources
- `make vector-smoke` – index fixture vectors and print top semantic matches
- `make eval-smoke` – run non-interactive eval smoke with defaults (`ollama` + `sqlite`) and write JSON to `Auto Run Docs/Working/evals/eval-smoke.json`
- `make eval-matrix` – run non-interactive eval matrix with defaults (`ollama,vllm` x `sqlite,qdrant`) and write JSON to `Auto Run Docs/Working/evals/eval-matrix.json`
- `make eval-gate` – run non-interactive eval matrix with default thresholds (`min_mean_recall_at_k=0.70`, `min_mean_mrr_at_k=0.70`, `max_p50_latency_ms=5000`, `max_p95_latency_ms=5000`) and write JSON to `Auto Run Docs/Working/evals/eval-gate.json`
- `make eval-savings-smoke` – run the fixed token-savings smoke suite with deterministic defaults (`ollama` + `sqlite`, `claude_code,codex,amp`, `last_30d`) and write JSON to `Auto Run Docs/Working/evals/token-savings-smoke.json`
- `make eval-savings-matrix` – run the token-savings benchmark matrix with deterministic defaults (`ollama,vllm` x `sqlite,qdrant`, `claude_code,codex,amp`, `last_30d`) and write JSON to `Auto Run Docs/Working/evals/token-savings-matrix.json`
- `make fmt` – format Go source files with `gofmt`
- `make clean` – remove generated binaries from `bin/`
- `make bench` – run the benchmark script
- `make race` – run the race-detection script

All six eval make targets are non-interactive and pass `--skip-markdown-report` by default. Override defaults with make variables like `EVAL_FIXTURES_DIR`, `EVAL_NAMESPACE_PREFIX`, `EVAL_SMOKE_PROVIDERS`, `EVAL_MATRIX_BACKENDS`, `EVAL_GATE_MIN_MEAN_RECALL_AT_K`, `EVAL_SAVINGS_COMPETITORS`, and `EVAL_SAVINGS_TREND_WINDOW`.

The token-savings benchmark CLI also supports benchmark-specific aliases and selectors:

- `--suite-path` accepts either the suite directory or the `prompt_suite.json` path inside it
- `--output-path` is an alias for `--out`
- `--competitors` filters the emitted scorecards and trend series to a stable subset of `claude_code,codex,amp`
- `--trend-window` selects persisted benchmark history lookback for report trends (`all`, `last_24h`, `last_7d`, `last_30d`)

When markdown reporting is enabled, retrieval eval reports are written under `docs/evals/runs/` with `docs/evals/Eval-Index.md`, and token-savings reports are written under `docs/evals/savings-runs/` with `docs/evals/Savings-Index.md`.

Deterministic local baseline thresholds are persisted in `docs/evals/thresholds.stub` with accompanying run evidence in `docs/evals/Eval-Threshold-Baseline.md`.

## Main server binary

Build and run the MCP server locally:

```bash
make build
./bin/gocodemunch-mcp -version
```

The main server currently runs over stdio transport.
