# Phase 02: Qdrant Parity and Local Dev Stack

Goal: add a Qdrant backend with contract parity to sqlite and make backend switching entirely config-driven for local development.

## Auto-Run Tasks

- [x] Create `docker-compose.vector.yml` at repo root with a `qdrant` service, deterministic exposed ports, named volume persistence, and a healthcheck command suitable for automated waits.
  - Completed: Added `docker-compose.vector.yml` with `qdrant/qdrant:latest`, fixed host port mappings (`6333`, `6334`), named volume `gocodemunch_qdrant_storage`, and a `/readyz` bash TCP healthcheck validated with `docker compose -f docker-compose.vector.yml up -d --wait`.
- [x] Add `make vector-up`, `make vector-down`, and `make vector-health` targets in `Makefile` that use `docker-compose.vector.yml` non-interactively.
  - Completed: Added targets plus `make help` wiring; `vector-up` now runs `docker compose -f docker-compose.vector.yml up -d --wait --quiet-pull`, `vector-health` prints JSON status and asserts `"Health":"healthy"`, and `vector-down` performs non-interactive teardown with `down --remove-orphans`.
- [ ] Extend `src/internal/config/config.go` with Qdrant settings (`QDRANT_URL`, `QDRANT_API_KEY`, `QDRANT_COLLECTION`) and validation for required values when `VECTOR_BACKEND=qdrant`.
- [ ] Add Qdrant config tests in `src/internal/config/config_test.go` for defaults, env overrides, and validation errors, then run `go test ./src/internal/config -run Qdrant -count=1`.
- [ ] Add `src/internal/storage/vector/qdrant/adapter.go` to implement collection bootstrap, `Upsert`, and `Query` compatible with shared vector contracts.
- [ ] Complete `src/internal/storage/vector/qdrant/adapter.go` with `Delete`, `DeleteNamespace`, and `Health` methods plus explicit transport/error mapping.
- [ ] Add `src/internal/storage/vector/qdrant/adapter_test.go` for deterministic tie ordering and error translation behavior, then run `go test ./src/internal/storage/... -run Qdrant -count=1`.
- [ ] Update backend factory wiring in `src/server/server.go` (or existing factory module) so sqlite and Qdrant are selected by `VECTOR_BACKEND` without code edits.
- [ ] Add contract parity tests in `src/internal/storage/vector/contract_parity_test.go` that run identical cases against sqlite and Qdrant adapters.
- [ ] Add integration tests in `tests-go` for live Qdrant lifecycle operations (upsert/query/delete/namespace-delete) and skip cleanly when `QDRANT_URL` is unset.
- [ ] Run `make vector-up`, `go test ./... -count=1`, `VECTOR_BACKEND=sqlite make vector-smoke`, and `VECTOR_BACKEND=qdrant make vector-smoke`, then run `make vector-down`.
