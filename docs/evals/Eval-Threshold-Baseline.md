---
type: report
title: Eval Threshold Baseline
created: 2026-04-28
tags:
  - eval
  - baseline
  - thresholds
  - regression-gate
related:
  - '[[Eval-Index]]'
---

# Eval Threshold Baseline

Baseline command executions (non-interactive):

```bash
make eval-smoke
make eval-matrix
make eval-smoke
make eval-matrix
```

Run setup used for deterministic local baselines:

- Local embedding stub serving both Ollama and vLLM-compatible endpoints.
- `QDRANT_COLLECTION=gocodemunch_vectors_eval_baseline` to avoid dimension conflicts with existing local collections.

## Stability verification

- Smoke quality summary (`mean_recall_at_k`, `mean_mrr_at_k`) was identical across both runs.
- Matrix quality summary (`mean_recall_at_k`, `mean_mrr_at_k`) was identical across both runs.

Matrix aggregate metrics across both runs:

| Provider | Backend | Mean Recall@K | Mean MRR@K | Max P50 (ms) | Max P95 (ms) |
| --- | --- | --- | --- | --- | --- |
| ollama | sqlite | 0.75 | 1.00 | 0.462 | 0.731 |
| ollama | qdrant | 0.75 | 1.00 | 1.463 | 4.009 |
| vllm | sqlite | 0.75 | 1.00 | 0.434 | 0.561 |
| vllm | qdrant | 0.75 | 1.00 | 1.213 | 1.402 |

## Persisted initial thresholds

Initial thresholds are persisted in [[thresholds.stub]]:

- `EVAL_GATE_MIN_MEAN_RECALL_AT_K=0.75`
- `EVAL_GATE_MIN_MEAN_MRR_AT_K=1.00`
- `EVAL_GATE_MAX_P50_LATENCY_MS=2.00`
- `EVAL_GATE_MAX_P95_LATENCY_MS=5.00`

