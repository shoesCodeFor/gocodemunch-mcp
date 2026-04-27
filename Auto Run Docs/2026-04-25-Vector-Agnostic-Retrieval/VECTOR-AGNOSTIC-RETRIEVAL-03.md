# Phase 03: Provider Expansion and Retrieval Integration

Goal: integrate vector ingestion/retrieval into normal indexing flows and add vLLM as an alternate embedding provider while keeping Ollama as default.

## Auto-Run Tasks

- [x] Extend `src/internal/config/config.go` with vLLM embedding settings (`VLLM_BASE_URL`, `VLLM_MODEL`, optional auth key) and validation rules used when `EMBEDDING_PROVIDER=vllm`.
  - Completed: Added `Config` fields and defaults for `VLLM_BASE_URL`, `VLLM_MODEL`, and optional `VLLM_API_KEY`; expanded provider validation to accept `vllm`; and added provider-gated validation rules requiring a valid HTTP `VLLM_BASE_URL` and non-empty `VLLM_MODEL` when `EMBEDDING_PROVIDER=vllm`.
- [x] Add vLLM config coverage in `src/internal/config/config_test.go`, including provider switching and invalid-provider errors, then run `go test ./src/internal/config -run VLLM -count=1`.
  - Completed: Added `TestVectorConfigVLLMProviderSwitching` (vLLM <-> Ollama provider switching and provider-gated validation behavior) and `TestVectorConfigVLLMInvalidProviderError` (invalid provider message coverage), then ran `go test ./src/internal/config -run VLLM -count=1`.
- [ ] Add `src/internal/orchestration/embeddings/vllm.go` implementing an OpenAI-compatible embedding client under the shared embedder interface.
- [ ] Add `src/internal/orchestration/embeddings/vllm_test.go` for request mapping, timeout behavior, error mapping, and response normalization, then run `go test ./src/internal/orchestration/... -run VLLM -count=1`.
- [ ] Add a deterministic chunking helper in `src/internal/orchestration` (or `src/internal/domain/indexing`) that converts indexed file content into stable chunks with source metadata.
- [ ] Update index create/update flows in `src/internal/orchestration/handlers_indexing.go` and related helpers so chunked content is embedded and upserted on every successful indexing operation.
- [ ] Update invalidate/delete/index-removal flows in `src/internal/orchestration` so vector entries are deleted whenever file or namespace state is removed.
- [ ] Implement semantic retrieval execution in `src/internal/orchestration/handlers_retrieval.go` and add a hybrid ranker that combines lexical and vector scores with config-driven weights.
- [ ] Update MCP contracts in `src/internal/orchestration/tooldefs.go` (and related tests) to expose semantic and hybrid retrieval without breaking current tool response envelopes.
- [ ] Add resilience controls (timeouts, retries, bounded batch sizes, graceful degradation) in retrieval/index paths with structured logs for provider/backend failure cases.
- [ ] Add integration tests in `src/internal/orchestration` and `tests-go` for index-to-vector sync, provider switching, semantic quality, and hybrid ranking behavior.
- [ ] Run `go test ./... -count=1`, then run smoke retrieval with `EMBEDDING_PROVIDER=ollama` and `EMBEDDING_PROVIDER=vllm` (when available) across both `sqlite` and `qdrant` backends.
