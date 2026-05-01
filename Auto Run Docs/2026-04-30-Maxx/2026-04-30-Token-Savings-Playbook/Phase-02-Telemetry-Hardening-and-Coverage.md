# Phase 02: Telemetry Hardening and Coverage

This phase hardens the prototype into reliable runtime infrastructure by covering all tool execution paths, making telemetry storage migration-safe, and exposing richer trend data from SQLite without breaking existing MCP response contracts.

## Tasks

- [x] Expand call instrumentation to all runtime paths using existing orchestration control-flow patterns:
  - Reuse `Service.CallTool` timeout/error handling paths so successful calls, validation failures, internal errors, and canceled calls are all recorded consistently.
  - Cover batch/fanout execution paths to avoid undercounting in multi-item operations.
  - Keep overhead bounded and ensure telemetry failures never block normal tool responses.
  - Completed in loop `00001`: refactored `Service.CallTool` finalization so telemetry is applied on every return path (including validation failures, disabled-tool exits, request cancellations, and internal errors), added panic-safe telemetry collection fallbacks so collector failures degrade to zeroed stats instead of breaking tool responses, and taught telemetry call counting to weight successful batch/fanout-style operations (`file_paths`, `symbol_ids`, `identifiers`, and incremental `changed_paths`) without inflating token totals.
  - Completed in loop `00001`: added orchestration + tracker coverage for validation/internal/canceled call recording, batch logical-call counting, and telemetry panic isolation; verified with `go test ./src/internal/orchestration ./src/internal/telemetry ./tests-go ./src/server -count=1` and `go vet ./src/internal/orchestration ./src/internal/telemetry`.

- [x] Add telemetry schema versioning, migrations, and retention controls in SQLite:
  - Reuse SQLite initialization and compatibility patterns from existing storage modules before introducing new migration helpers.
  - Add schema version tracking and forward-only migrations for telemetry tables.
  - Implement retention/compaction for old per-call events while preserving cumulative trend history.
  - Completed in loop `00001`: versioned the telemetry SQLite store with forward-only `meta.schema_version` migrations, preserving compatibility with existing snapshot-only databases while upgrading them in place to a `call_events`-aware schema.
  - Completed in loop `00001`: extended the telemetry runtime/store seam so per-call events are batched and persisted alongside periodic cumulative snapshots, added 30-day retention compaction for stale call-event rows, and preserved cumulative snapshot history for long-range trend reconstruction.
  - Completed in loop `00001`: added migration, retention, and runtime retry coverage in `src/internal/storage/sqlite_telemetry_store_test.go` and `src/internal/telemetry/tracker_test.go`; verified with `go test ./src/internal/telemetry ./src/internal/storage ./src/internal/orchestration ./src/server ./tests-go -count=1` and `go vet ./src/internal/telemetry ./src/internal/storage ./src/internal/orchestration ./src/server ./tests-go`.

- [ ] Enrich `get_session_stats` with trend windows while preserving backward compatibility:
  - Add optional arguments for time windows (for example `last_24h`, `last_7d`, `last_30d`) and return aggregated trend points from SQLite.
  - Include per-tool and per-competitor rollups for session and cumulative scopes.
  - Preserve existing keys and response envelope shape so current clients keep working.

- [ ] Strengthen pricing/profile normalization for Claude Code, Codex, and Amp:
  - Centralize competitor profile definitions and unit costs in one reusable module instead of scattering constants.
  - Add config validation for malformed or negative pricing values with safe fallback behavior.
  - Include an explicit version tag in stored snapshots so future pricing updates remain auditable.

- [ ] Add robust tests for migrations, trend queries, and failure isolation:
  - Add migration tests that upgrade older telemetry schema versions to the latest schema safely.
  - Add orchestration integration tests ensuring tool responses still succeed when telemetry persistence is unavailable.
  - Add `get_session_stats` contract tests for new trend fields and compatibility with existing consumers.

- [ ] Run hardening verification and performance checks:
  - Run targeted test suites for telemetry/storage/orchestration and existing parity integration tests.
  - Execute a local stress pass (high call count) to confirm persistence cadence and retention behave correctly.
  - Fix regressions until telemetry remains accurate under normal and degraded conditions.
