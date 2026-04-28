package testsgo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSearchTextProviderSwitchingAndVectorSync(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)
	t.Setenv("VECTOR_BACKEND", "sqlite")
	t.Setenv("VECTOR_TOP_K", "8")
	t.Setenv("VECTOR_QUERY_TIMEOUT_MS", "3000")
	t.Setenv("EMBEDDING_MODEL", "stub-embed")
	t.Setenv("VLLM_MODEL", "stub-embed")
	t.Setenv("VLLM_API_KEY", "test-vllm-key")

	var ollamaCalls atomic.Int32
	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		ollamaCalls.Add(1)

		var payload struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		embeddings := make([][]float32, len(payload.Input))
		for index, input := range payload.Input {
			embeddings[index] = semanticFixtureEmbedding(input)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":      "stub-embed",
			"embeddings": embeddings,
		})
	}))
	defer ollamaServer.Close()

	var vllmCalls atomic.Int32
	vllmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		if got := strings.TrimSpace(r.Header.Get("Authorization")); got != "Bearer test-vllm-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		vllmCalls.Add(1)

		var payload struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		data := make([]map[string]any, len(payload.Input))
		for index, input := range payload.Input {
			data[index] = map[string]any{
				"index":     index,
				"embedding": semanticFixtureEmbedding(input),
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "stub-embed",
			"data":  data,
		})
	}))
	defer vllmServer.Close()

	t.Setenv("OLLAMA_BASE_URL", ollamaServer.URL)
	t.Setenv("VLLM_BASE_URL", vllmServer.URL+"/v1")

	alphaPath := filepath.Join(repoRoot, "alpha.py")
	betaPath := filepath.Join(repoRoot, "beta.py")
	writeFile(t, alphaPath, "def alpha_rank():\n    # TODO rank alpha fallback\n    return 'alpha'\n")
	writeFile(t, betaPath, "def beta_rank():\n    # TODO rank beta preferred\n    return 'beta'\n")

	t.Setenv("EMBEDDING_PROVIDER", "ollama")
	ollamaResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})

	indexPayload := toolPayload(t, ollamaResponses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder with ollama failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")
	if strings.TrimSpace(repoID) == "" {
		t.Fatalf("expected repo id in index response: %#v", indexPayload)
	}

	if ollamaCalls.Load() == 0 {
		t.Fatalf("expected ollama embedder stub calls during indexing")
	}

	semanticWithOllama := runMCPRequests(t, []map[string]any{
		initializeRequest(4),
		toolCallRequest(5, "search_text", map[string]any{
			"repo":           repoID,
			"query":          "rank",
			"retrieval_mode": "semantic",
			"max_results":    2,
		}),
		toolCallRequest(6, "search_text", map[string]any{
			"repo":            repoID,
			"query":           "rank",
			"retrieval_mode":  "hybrid",
			"max_results":     2,
			"lexical_weight":  1.0,
			"semantic_weight": 0.0,
		}),
	})

	assertTopSearchFile(t, toolPayload(t, semanticWithOllama[1]), "beta.py")
	assertTopSearchFile(t, toolPayload(t, semanticWithOllama[2]), "alpha.py")

	writeFile(t, alphaPath, "def alpha_rank():\n    # TODO rank alpha refreshed\n    return 'alpha'\n")
	if err := os.Remove(betaPath); err != nil {
		t.Fatalf("remove beta fixture: %v", err)
	}

	t.Setenv("EMBEDDING_PROVIDER", "vllm")
	vllmResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(7),
		toolCallRequest(8, "search_text", map[string]any{
			"repo":           repoID,
			"query":          "rank",
			"retrieval_mode": "semantic",
			"max_results":    2,
		}),
		toolCallRequest(9, "index_folder", map[string]any{
			"path":        repoRoot,
			"incremental": true,
			"changed_paths": []map[string]any{
				{"change_type": "modified", "path": alphaPath},
				{"change_type": "deleted", "path": betaPath},
			},
		}),
		toolCallRequest(10, "search_text", map[string]any{
			"repo":           repoID,
			"query":          "rank",
			"retrieval_mode": "semantic",
			"max_results":    2,
		}),
		toolCallRequest(11, "search_text", map[string]any{
			"repo":            repoID,
			"query":           "rank",
			"retrieval_mode":  "hybrid",
			"max_results":     2,
			"lexical_weight":  0.0,
			"semantic_weight": 1.0,
		}),
	})

	assertTopSearchFile(t, toolPayload(t, vllmResponses[1]), "beta.py")
	if vllmCalls.Load() == 0 {
		t.Fatalf("expected vllm embedder stub calls after provider switch")
	}

	incrementalPayload := toolPayload(t, vllmResponses[2])
	if !boolField(incrementalPayload, "success") {
		t.Fatalf("incremental index_folder with vllm failed: %#v", incrementalPayload)
	}
	if got := intField(incrementalPayload, "deleted"); got != 1 {
		t.Fatalf("expected deleted=1 in incremental vector sync response, got %#v", incrementalPayload)
	}

	semanticAfterSync := toolPayload(t, vllmResponses[3])
	assertTopSearchFile(t, semanticAfterSync, "alpha.py")
	if got := intField(semanticAfterSync, "result_count"); got != 1 {
		t.Fatalf("expected one semantic result after deleting beta.py, got %#v", semanticAfterSync)
	}
	assertTopSearchFile(t, toolPayload(t, vllmResponses[4]), "alpha.py")
}

func semanticFixtureEmbedding(text string) []float32 {
	normalized := strings.ToLower(strings.TrimSpace(text))
	alphaScore := float32(1 + strings.Count(normalized, "alpha"))
	betaScore := float32(1 + strings.Count(normalized, "beta"))
	betaScore += float32(strings.Count(normalized, "rank") * 2)
	return []float32{alphaScore, betaScore}
}

func assertTopSearchFile(t *testing.T, payload map[string]any, wantFile string) {
	t.Helper()
	if got := stringField(payload, "error"); got != "" {
		t.Fatalf("expected successful search payload, got error=%q payload=%#v", got, payload)
	}
	results := mapSliceField(payload, "results")
	if len(results) == 0 {
		t.Fatalf("expected non-empty search results, got %#v", payload)
	}
	if got := stringField(results[0], "file"); got != wantFile {
		t.Fatalf("unexpected top file: got %q want %q payload=%#v", got, wantFile, payload)
	}
}
