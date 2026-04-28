# Phase 04: Evaluation Harness and Operational Readiness

Goal: add a repeatable, non-interactive evaluation harness with quality/performance thresholds and machine-readable outputs for regression gating.

## Auto-Run Tasks

- [x] Create deterministic eval fixtures under `tests-go/evals/fixtures` with corpus docs, query set, and relevance labels that can run in under a few minutes locally.
  - Completed: Added `tests-go/evals/fixtures/corpus.json` (12 corpus docs), `tests-go/evals/fixtures/queries.json` (12 deterministic queries with fixed `top_k`), and `tests-go/evals/fixtures/relevance.json` (23 graded relevance judgments) as a compact local fixture set designed for fast eval runs.
- [x] Add `src/internal/evals/metrics.go` implementing `recall@k`, `mrr@k`, and latency percentile calculations (`p50`, `p95`) used by the eval runner.
  - Completed: Added `src/internal/evals/metrics.go` with reusable eval metrics helpers for `RecallAtK`, `MRRAtK`, and `ComputeLatencyPercentiles` (`p50_ms`, `p95_ms` via deterministic interpolated percentile math), including relevance-score handling (`>0` as relevant), duplicate-result protection for recall counts, and empty-input safeguards.
- [x] Add `src/internal/evals/metrics_test.go` with deterministic metric assertions and run `go test ./src/internal/evals -count=1`.
  - Completed: Added `src/internal/evals/metrics_test.go` with deterministic table-driven assertions for `RecallAtK`, `MRRAtK`, and `ComputeLatencyPercentiles` (including duplicate IDs, rank cutoffs, negative-latency clamping, and interpolated percentile expectations), and verified with `go test ./src/internal/evals -count=1`.
- [x] Add an eval runner command (new `src/cmd` module) that executes the fixture query set against configured provider/backend combinations and emits per-query and aggregate metrics.
  - Completed: Added `src/cmd/gocodemunch-eval/main.go` with a non-interactive eval runner that loads deterministic fixtures (`corpus.json`, `queries.json`, `relevance.json`), resolves provider/backend matrix combinations from config plus `--providers`/`--backends` overrides, indexes fixture docs into each combo namespace, executes all fixture queries, and emits machine-readable JSON containing per-query metrics (`recall_at_k`, `mrr_at_k`, latency, ranked matches) and aggregate metrics (`mean_recall_at_k`, `mean_mrr_at_k`, `latency_metrics.p50_ms/p95_ms`).
  - Completed: Added `src/cmd/gocodemunch-eval/main_test.go` with offline deterministic coverage for report structure/metrics, unsupported-provider validation, and fixture dataset mismatch handling; verified with `go test ./src/internal/evals ./src/cmd/gocodemunch-eval -count=1`.
- [ ] Add threshold gating logic (config file or env-driven) so eval runs exit non-zero when quality or latency targets are missed.
- [ ] Add markdown report generation under `docs/evals/runs/` with YAML front matter containing run metadata (`type`, `title`, `created`, provider/backend/model tags, related links).
- [ ] Add or update `docs/evals/Eval-Index.md` so each new run report is linked via wiki-link and listed newest-first.
- [ ] Add `make eval-smoke`, `make eval-matrix`, and `make eval-gate` targets in `Makefile` with documented defaults and non-interactive behavior.
- [ ] Add eval runner integration tests for threshold failures and report output determinism, then run targeted eval tests.
- [ ] Run `make eval-smoke` and `make eval-matrix` twice, verify stable baselines, and persist initial thresholds in the repo.
