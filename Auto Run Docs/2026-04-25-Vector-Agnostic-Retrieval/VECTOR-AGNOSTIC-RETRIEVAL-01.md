# Phase 01: Autonomous Local Vector Prototype

Goal: add a fully local semantic retrieval prototype with env-driven configuration, `ollama+bge-m3` embeddings, and a `sqlite` default backend while preserving current non-vector behavior.

## Auto-Run Tasks

- [x] In `src/internal/config/config.go`, add config struct fields for vector backend, top-k, query timeout, embedding provider, embedding model, and Ollama base URL without removing or renaming any existing fields.
- [x] In `src/internal/config/config.go`, add defaults for the new settings (`sqlite`, `5`, `8000`, `ollama`, `bge-m3`, `http://host.docker.internal:11434`) and keep all existing defaults unchanged.
- [x] In `src/internal/config/config.go`, load env vars `VECTOR_BACKEND`, `VECTOR_TOP_K`, `VECTOR_QUERY_TIMEOUT_MS`, `EMBEDDING_PROVIDER`, `EMBEDDING_MODEL`, and `OLLAMA_BASE_URL`, then return actionable validation errors for invalid values.
  - Completed: `Load()` now reads all vector env vars, validates them, and returns aggregated actionable errors; `MustLoad()` preserves fail-fast startup behavior for non-error-returning constructors.
- [x] In `src/internal/config/config_test.go`, add `TestVectorConfigDefaults` to verify defaults and run `go test ./src/internal/config -run TestVectorConfigDefaults -count=1`.
  - Completed: Added `TestVectorConfigDefaults` covering default vector backend/top-k/query-timeout/provider/model/Ollama URL values; `go test ./src/internal/config -run TestVectorConfigDefaults -count=1` passed.
- [x] In `src/internal/config/config_test.go`, add `TestVectorConfigEnvOverrides` to verify env precedence and run `go test ./src/internal/config -run TestVectorConfigEnvOverrides -count=1`.
  - Completed: Renamed the existing override coverage to `TestVectorConfigEnvOverrides` (same env precedence assertions across vector backend/top-k/timeout/provider/model/Ollama URL) and verified with `go test ./src/internal/config -run TestVectorConfigEnvOverrides -count=1`.
- [ ] Add `src/internal/domain/indexing/vector_types.go` with shared vector request/response structs, score fields, and metadata types used by all backends.
- [ ] Add `src/internal/domain/indexing/vector_contracts.go` with interfaces for `Upsert`, `Query`, `Delete`, `DeleteNamespace`, and `Health(ctx)` plus embedder contract and retryable error classification.
- [ ] Add `src/internal/orchestration/embeddings/ollama.go` implementing batched embedding requests with context timeout handling and `bge-m3` dimension checks.
- [ ] Add `src/internal/orchestration/embeddings/ollama_test.go` for success path, timeout behavior, malformed payload handling, and dimension mismatch handling, then run `go test ./src/internal/orchestration/... -run Ollama -count=1`.
- [ ] Add `src/internal/storage/vector/sqlite/adapter.go` implementing sqlite vector bootstrap, `Upsert`, `Query`, `Delete`, `DeleteNamespace`, and `Health` with deterministic ordering guarantees.
- [ ] Add `src/internal/storage/vector/sqlite/adapter_test.go` for bootstrap, CRUD lifecycle, deterministic ranking order, and health checks, then run `go test ./src/internal/storage/... -run Vector -count=1`.
- [ ] Update `src/server/server.go` and `src/internal/orchestration/service.go` to construct and inject embedder/vector dependencies through existing constructor patterns with fail-fast startup errors.
- [ ] Add `scripts/vector-smoke.sh` and `Makefile` target `vector-smoke` that indexes fixed fixture data and prints top-k semantic matches, then run `go test ./... -count=1` and `make vector-smoke`.
