# Phase 01: Autonomous Token-Savings Prototype

This phase delivers a fully autonomous vertical slice: the MCP runtime estimates token and cost savings per tool call and per session, persists cumulative snapshots to SQLite on a periodic cadence, and runs a fixed with-MCP vs without-MCP benchmark for Claude Code, Codex, and Amp that produces tangible JSON + Markdown outputs.

## Tasks

- [x] Add savings configuration and competitor pricing scaffolding by reusing existing config validation patterns:
  - Inspect and reuse `src/internal/config/config.go` patterns used by vector/env parsing before adding new logic.
  - Add config fields and env parsing for savings telemetry enablement, snapshot interval, and pricing defaults for `claude_code`, `codex`, and `amp`.
  - Keep defaults deterministic and non-interactive so Phase 01 can run on a clean machine without manual decisions.
  - Completed in loop `00001`: added deterministic savings defaults and env validation/parsing in `src/internal/config/config.go` (telemetry toggle, snapshot interval, competitor pricing), plus coverage in `src/internal/config/config_test.go`.

- [x] Implement telemetry domain and periodic SQLite persistence for cumulative savings:
  - Create a focused telemetry package (or module) that tracks per-call, per-tool, per-session, and cumulative estimated token/cost metrics.
  - Reuse SQLite schema/init and retry patterns from `src/internal/storage/sqlite_store.go` for a new telemetry store, including schema creation and safe writes.
  - Persist cumulative snapshots periodically (time-based) and at graceful shutdown points used in current server/CLI flows.
  - Completed in loop `00001`: added `src/internal/telemetry` tracker/runtime primitives for per-call, per-tool, per-session, and cumulative savings metrics; added `src/internal/storage/sqlite_telemetry_store.go` with WAL/busy-retry snapshot persistence; and wired server/CLI shutdown via `Service.Close()` / `Server.Close()` so telemetry flushes on exit as well as on periodic cadence.

- [x] Wire runtime instrumentation into orchestration and replace the `get_session_stats` stub:
  - Reuse `Service.CallTool` flow in `src/internal/orchestration/service.go` to capture call start/end, request/response size estimates, and MCP savings deltas without changing tool-specific business logic.
  - Update `src/internal/orchestration/handlers_retrieval.go` so `get_session_stats` returns real session + cumulative stats and competitor cost-avoided maps for Claude Code, Codex, and Amp.
  - Populate `_meta.tokens_saved` and `_meta.total_tokens_saved` with real values while preserving existing response envelope compatibility.
  - Completed in loop `00001`: added centralized successful-call telemetry instrumentation in `src/internal/orchestration/service.go` plus `src/internal/orchestration/service_telemetry.go`, replaced the `get_session_stats` stub with live session/cumulative snapshots keyed by `claude_code`, `codex`, and `amp`, and added orchestration + stdio integration coverage in `src/internal/orchestration/service_telemetry_test.go` and `tests-go/indexing_tools_test.go`.

- [x] Build an autonomous token-savings smoke benchmark path in the eval CLI:
  - Reuse `src/cmd/gocodemunch-eval/main.go` matrix/report flow to add a `token-savings-smoke` mode that runs a fixed prompt suite in both `with_mcp` and `without_mcp` modes.
  - Add fixture inputs for the fixed prompt suite under existing eval fixture conventions so no user-authored prompts are required during execution.
  - Score token and cost deltas per competitor (`claude_code`, `codex`, `amp`) and write JSON output to `Auto Run Docs/Working/evals/token-savings-smoke.json`.
  - Completed in loop `00001`: added `--mode token-savings-smoke` in `src/cmd/gocodemunch-eval`, a deterministic prompt suite under `tests-go/evals/fixtures/token-savings-smoke`, and a real MCP-vs-raw-context scorer that emits per-case plus aggregate token/cost deltas for `claude_code`, `codex`, and `amp`.
  - Completed in loop `00001`: added `make eval-savings-smoke`, verified `Auto Run Docs/Working/evals/token-savings-smoke.json` is generated end-to-end, and added coverage in `src/cmd/gocodemunch-eval/main_test.go` plus `tests-go/token_savings_fixtures_test.go`.

- [x] Emit structured Markdown savings artifacts compatible with DocGraph navigation:
  - Extend report rendering to write savings Markdown run reports with YAML front matter (`type`, `title`, `created`, `tags`, `related`) and wiki-links.
  - Create/update an index-style document (for example `Savings-Index`) that links newest-first run reports using `[[...]]` cross-references.
  - Ensure generated docs are deterministic and tied to the JSON output artifact for quick verification.
  - Completed in loop `00001`: enabled Markdown report generation for `token-savings-smoke` in `src/cmd/gocodemunch-eval`, added deterministic savings docs under `docs/evals/savings-runs/`, and added `docs/evals/Savings-Index.md` with newest-first wiki-link updates.
  - Completed in loop `00001`: stabilized savings report token estimation by canonicalizing request/response sizing for benchmark artifacts, linked reports back to `Auto Run Docs/Working/evals/token-savings-smoke.json`, and added coverage in `src/cmd/gocodemunch-eval/main_test.go`.

- [x] Add test coverage for telemetry math, persistence, orchestration integration, and eval reporting:
  - Add unit tests for token estimation, competitor pricing conversion, and delta math edge cases.
  - Add store tests for periodic snapshot writes and cumulative reload correctness.
  - Update integration tests (including `tests-go/indexing_tools_test.go`) to validate non-zero/expected session stats behavior and stable response contracts.
  - Completed in loop `00001`: added telemetry normalization + runtime restore/flush coverage in `src/internal/telemetry/tracker_test.go`, real SQLite periodic/reload coverage in `src/internal/storage/sqlite_telemetry_store_test.go`, stable zero/non-zero `get_session_stats` contract assertions in `src/internal/orchestration/service_telemetry_test.go` and `tests-go/indexing_tools_test.go`, and eval helper math coverage in `src/cmd/gocodemunch-eval/main_test.go`.
  - Completed in loop `00001`: fixed `src/cmd/gocodemunch-eval/token_savings.go` so competitor cost scoring prices input and output tokens with their respective rates; verification passed via scoped `go test` and `go vet` over config, telemetry, storage, orchestration, eval CLI, and `tests-go`.

- [ ] Run verification commands and ensure the prototype is visibly working:
  - Run targeted Go test suites for config, orchestration, storage/telemetry, and eval CLI.
  - Run the new savings smoke command (or Make target) end-to-end and verify JSON + Markdown artifacts are generated and populated.
  - Fix regressions until Phase 01 executes without manual prompts or runtime decisions.
