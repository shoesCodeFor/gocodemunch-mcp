---
type: report
title: Token Savings Run token-savings-smoke 2026-05-01T07:10:27Z
created: 2026-05-01
tags:
  - competitor-amp
  - competitor-claude-code
  - competitor-codex
  - dataset-token-savings-smoke
  - eval
  - mode-token-savings-smoke
  - suite-v1
  - token-savings
related:
  - '[[Eval-Index]]'
  - '[[Savings-Index]]'
  - '[[token-savings-smoke]]'
---

## Summary

- Generated (UTC): `2026-05-01T07:10:27Z`
- Mode: `token-savings-smoke`
- Dataset: `token-savings-smoke`
- Suite Version: `v1`
- Fixtures Dir: `tests-go/evals/fixtures/token-savings-smoke`
- Indexed Repo: `token-savings-token-savings-smoke`
- File Count: `4`
- JSON Artifact: `Auto Run Docs/Working/evals/token-savings-smoke.json`
- Tokens Saved: `793`
- Savings Percentage: `53.65%`

## Aggregate Tokens

| Mode | Input Tokens | Output Tokens | Total Tokens |
| --- | ---: | ---: | ---: |
| with_mcp | 220 | 465 | 685 |
| without_mcp | 1478 | 0 | 1478 |

## Competitor Pricing

| Competitor | Input USD / MTok | Output USD / MTok |
| --- | ---: | ---: |
| amp | 1.500000 | 6.000000 |
| claude_code | 3.000000 | 15.000000 |
| codex | 1.500000 | 6.000000 |

## Aggregate Cost Savings

| Competitor | With MCP Cost (USD) | Without MCP Cost (USD) | Cost Saved (USD) |
| --- | ---: | ---: | ---: |
| amp | 0.001027 | 0.002217 | 0.001190 |
| claude_code | 0.002055 | 0.004434 | 0.002379 |
| codex | 0.001027 | 0.002217 | 0.001190 |

## Per-Case Savings

| Case | Tool | With MCP Tokens | Without MCP Tokens | Tokens Saved | Savings % |
| --- | --- | ---: | ---: | ---: | ---: |
| tree-app-files | get_file_tree | 102 | 321 | 219 | 68.22% |
| outline-http-client | get_file_outline | 79 | 203 | 124 | 61.08% |
| importers-http-client | find_importers | 130 | 320 | 190 | 59.38% |
| references-load-config | find_references | 101 | 317 | 216 | 68.14% |
| search-timeout-seconds | search_text | 273 | 317 | 44 | 13.88% |
