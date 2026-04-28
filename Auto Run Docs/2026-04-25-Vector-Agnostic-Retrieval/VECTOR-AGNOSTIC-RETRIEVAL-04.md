# Phase 04: Evaluation Harness and Operational Readiness

Goal: add a repeatable, non-interactive evaluation harness with quality/performance thresholds and machine-readable outputs for regression gating.

## Auto-Run Tasks

- [x] Create deterministic eval fixtures under `tests-go/evals/fixtures` with corpus docs, query set, and relevance labels that can run in under a few minutes locally.
  - Completed: Added `tests-go/evals/fixtures/corpus.json` (12 corpus docs), `tests-go/evals/fixtures/queries.json` (12 deterministic queries with fixed `top_k`), and `tests-go/evals/fixtures/relevance.json` (23 graded relevance judgments) as a compact local fixture set designed for fast eval runs.
- [x] Add `src/internal/evals/metrics.go` implementing `recall@k`, `mrr@k`, and latency percentile calculations (`p50`, `p95`) used by the eval runner.
  - Completed: Added `src/internal/evals/metrics.go` with reusable eval metrics helpers for `RecallAtK`, `MRRAtK`, and `ComputeLatencyPercentiles` (`p50_ms`, `p95_ms` via deterministic interpolated percentile math), including relevance-score handling (`>0` as relevant), duplicate-result protection for recall counts, and empty-input safeguards.
- [ ] Add `src/internal/evals/metrics_test.go` with deterministic metric assertions and run `go test ./src/internal/evals -count=1`.
- [ ] Add an eval runner command (new `src/cmd` module) that executes the fixture query set against configured provider/backend combinations and emits per-query and aggregate metrics.
- [ ] Add threshold gating logic (config file or env-driven) so eval runs exit non-zero when quality or latency targets are missed.
- [ ] Add markdown report generation under `docs/evals/runs/` with YAML front matter containing run metadata (`type`, `title`, `created`, provider/backend/model tags, related links).
- [ ] Add or update `docs/evals/Eval-Index.md` so each new run report is linked via wiki-link and listed newest-first.
- [ ] Add `make eval-smoke`, `make eval-matrix`, and `make eval-gate` targets in `Makefile` with documented defaults and non-interactive behavior.
- [ ] Add eval runner integration tests for threshold failures and report output determinism, then run targeted eval tests.
- [ ] Run `make eval-smoke` and `make eval-matrix` twice, verify stable baselines, and persist initial thresholds in the repo.
