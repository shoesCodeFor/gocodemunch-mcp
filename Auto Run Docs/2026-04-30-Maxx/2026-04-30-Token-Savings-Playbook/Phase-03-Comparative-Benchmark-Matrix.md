# Phase 03: Comparative Benchmark Matrix

This phase builds the full benchmark workflow requested in discovery: a fixed prompt suite executed in with-MCP and without-MCP modes, first-class competitor comparisons for Claude Code, Codex, and Amp, and scored outputs that show token and cost savings trends over time.

## Tasks

- [x] Build a fixed prompt suite and benchmark dataset using existing eval fixture conventions:
  - Reuse fixture-loading patterns already used by `gocodemunch-eval` before introducing new schema fields.
  - Define a stable prompt suite with representative code-indexing tasks and deterministic IDs so runs are comparable over time.
  - Include explicit mode metadata per case so each prompt runs in both `with_mcp` and `without_mcp` paths automatically.
  - Completed on 2026-05-01: the checked-in `token-savings-smoke` prompt suite now declares deterministic per-case `modes`, fixture loading validates and canonicalizes the metadata, and token-savings reports retain the explicit mode list for each benchmark case.

- [ ] Implement benchmark runner adapters for `with_mcp` and `without_mcp` execution:
  - Reuse existing eval matrix orchestration loops for provider/backend combinations to avoid duplicate runner logic.
  - Add mode adapters that estimate token usage consistently for both paths and keep input prompts identical.
  - Ensure competitor comparisons are emitted for `claude_code`, `codex`, and `amp` in every run.

- [ ] Add scoring and trend aggregation for token/cost deltas:
  - Compute per-prompt and aggregate deltas (`tokens_saved`, `cost_saved`, `savings_pct`) per competitor.
  - Calculate distribution metrics (mean/median/p95) for savings across the suite.
  - Merge current run metrics with historical SQLite snapshots to produce trend points for each competitor.

- [ ] Persist benchmark history for longitudinal analysis:
  - Store run-level metadata (timestamp, suite version, mode, competitor, aggregate metrics) in SQLite tables dedicated to savings benchmarks.
  - Keep references from runtime telemetry snapshots to benchmark runs where applicable.
  - Add idempotency guards so reruns with the same run ID do not create duplicate trend entries.

- [ ] Emit structured benchmark artifacts for graph-based navigation:
  - Write JSON reports to `Auto Run Docs/Working/evals/` with per-prompt and aggregate savings sections.
  - Write Markdown reports with YAML front matter (`type: report`, `title`, `created`, `tags`, `related`) and wiki-links between benchmark runs, `Eval-Index`, and a new savings index.
  - Update/create index-style Markdown pages that list newest-first run links for fast browsing.

- [ ] Add command and Makefile ergonomics for repeatable benchmark runs:
  - Add CLI flags for suite path, competitors, output path, and trend window selection with safe defaults.
  - Add Make targets (for example `eval-savings-smoke` and `eval-savings-matrix`) that are non-interactive and CI-friendly.
  - Keep defaults deterministic so nightly or local reruns do not require manual input.

- [ ] Add tests and execute the full benchmark matrix:
  - Add unit tests for scorer math, trend rollups, and artifact rendering.
  - Add integration tests for fixed-suite execution across both modes and all competitors.
  - Run the benchmark commands end-to-end and fix failures until JSON + Markdown artifacts are consistently produced.
