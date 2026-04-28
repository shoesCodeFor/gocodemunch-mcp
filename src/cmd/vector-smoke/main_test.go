package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	vectorqdrant "github.com/jgravelle/gocodemunch-mcp/src/internal/storage/vector/qdrant"
	vectorsqlite "github.com/jgravelle/gocodemunch-mcp/src/internal/storage/vector/sqlite"
)

var uuidPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

func TestNormalizeBackend(t *testing.T) {
	testCases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "defaults to sqlite when empty",
			raw:  "",
			want: "sqlite",
		},
		{
			name: "normalizes sqlite casing",
			raw:  "SQLITE",
			want: "sqlite",
		},
		{
			name: "normalizes qdrant casing",
			raw:  "QDRANT",
			want: "qdrant",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			got, err := normalizeBackend(testCase.raw)
			if err != nil {
				t.Fatalf("normalize backend returned error: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("normalized backend mismatch: got %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestNormalizeBackendRejectsUnsupportedValue(t *testing.T) {
	_, err := normalizeBackend("redis")
	if err == nil {
		t.Fatal("expected unsupported backend error")
	}
	if !strings.Contains(err.Error(), "unsupported backend") {
		t.Fatalf("expected unsupported backend message, got %v", err)
	}
}

func TestNormalizeEmbeddingProvider(t *testing.T) {
	testCases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "defaults to ollama when empty",
			raw:  "",
			want: "ollama",
		},
		{
			name: "normalizes ollama casing",
			raw:  "OLLAMA",
			want: "ollama",
		},
		{
			name: "normalizes vllm casing",
			raw:  "VLLM",
			want: "vllm",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			got, err := normalizeEmbeddingProvider(testCase.raw)
			if err != nil {
				t.Fatalf("normalize embedding provider returned error: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("normalized provider mismatch: got %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestNormalizeEmbeddingProviderRejectsUnsupportedValue(t *testing.T) {
	_, err := normalizeEmbeddingProvider("openai")
	if err == nil {
		t.Fatal("expected unsupported embedding provider error")
	}
	if !strings.Contains(err.Error(), "unsupported embedding provider") {
		t.Fatalf("expected unsupported provider message, got %v", err)
	}
}

func TestNewVectorBackendBuildsSQLiteAdapter(t *testing.T) {
	cfg := config.Config{
		StoragePath: t.TempDir(),
	}

	backend, err := newVectorBackend(cfg, "sqlite")
	if err != nil {
		t.Fatalf("build sqlite backend: %v", err)
	}

	if _, ok := backend.(*vectorsqlite.Adapter); !ok {
		t.Fatalf("expected sqlite adapter, got %T", backend)
	}

	if err := closeVectorBackend(backend); err != nil {
		t.Fatalf("close sqlite adapter: %v", err)
	}
}

func TestNewVectorBackendBuildsQdrantAdapter(t *testing.T) {
	cfg := config.Config{
		QdrantURL:        "http://localhost:6333",
		QdrantCollection: "vector_smoke_tests",
	}

	backend, err := newVectorBackend(cfg, "qdrant")
	if err != nil {
		t.Fatalf("build qdrant backend: %v", err)
	}

	adapter, ok := backend.(*vectorqdrant.Adapter)
	if !ok {
		t.Fatalf("expected qdrant adapter, got %T", backend)
	}
	if got := adapter.BaseURL(); got != cfg.QdrantURL {
		t.Fatalf("expected qdrant base url %q, got %q", cfg.QdrantURL, got)
	}
	if got := adapter.Collection(); got != cfg.QdrantCollection {
		t.Fatalf("expected qdrant collection %q, got %q", cfg.QdrantCollection, got)
	}

	if err := closeVectorBackend(backend); err != nil {
		t.Fatalf("close qdrant adapter: %v", err)
	}
}

func TestNewVectorBackendRejectsUnsupportedBackend(t *testing.T) {
	_, err := newVectorBackend(config.Config{}, "redis")
	if err == nil {
		t.Fatal("expected unsupported backend error")
	}
}

func TestNewEmbedderBuildsOllamaEmbedder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/api/embed" {
			t.Fatalf("expected /api/embed path, got %q", r.URL.Path)
		}

		requestPayload := struct {
			Input []string `json:"input"`
		}{}
		if err := json.NewDecoder(r.Body).Decode(&requestPayload); err != nil {
			t.Fatalf("decode ollama request: %v", err)
		}
		embeddings := make([][]float32, 0, len(requestPayload.Input))
		for index := range requestPayload.Input {
			embeddings = append(embeddings, []float32{float32(index + 1), 1, 1})
		}

		if err := json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings}); err != nil {
			t.Fatalf("encode ollama response: %v", err)
		}
	}))
	defer server.Close()

	cfg := config.Config{
		EmbeddingProvider:    "ollama",
		EmbeddingModel:       "test-embedding-model",
		OllamaBaseURL:        server.URL,
		VectorQueryTimeoutMS: 2500,
	}

	embedder, err := newEmbedder(cfg)
	if err != nil {
		t.Fatalf("build ollama embedder: %v", err)
	}

	embeddings, err := embedder.Embed(context.Background(), []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("embed with ollama provider: %v", err)
	}
	if len(embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(embeddings))
	}
	if len(embeddings[0]) == 0 || len(embeddings[1]) == 0 {
		t.Fatal("expected non-empty embeddings")
	}
}

func TestNewEmbedderBuildsVLLMEmbedder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("expected /v1/embeddings path, got %q", r.URL.Path)
		}
		if got := strings.TrimSpace(r.Header.Get("Authorization")); got != "Bearer smoke-vllm-key" {
			t.Fatalf("expected authorization header, got %q", got)
		}

		requestPayload := struct {
			Input []string `json:"input"`
		}{}
		if err := json.NewDecoder(r.Body).Decode(&requestPayload); err != nil {
			t.Fatalf("decode vllm request: %v", err)
		}

		data := make([]map[string]any, 0, len(requestPayload.Input))
		for index := len(requestPayload.Input) - 1; index >= 0; index-- {
			data = append(data, map[string]any{
				"index":     index,
				"embedding": []float32{float32(index + 1), 2, 2},
			})
		}

		if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
			t.Fatalf("encode vllm response: %v", err)
		}
	}))
	defer server.Close()

	cfg := config.Config{
		EmbeddingProvider:    "vllm",
		VLLMBaseURL:          server.URL + "/v1",
		VLLMModel:            "test-embedding-model",
		VLLMAPIKey:           "smoke-vllm-key",
		VectorQueryTimeoutMS: 2500,
	}

	embedder, err := newEmbedder(cfg)
	if err != nil {
		t.Fatalf("build vllm embedder: %v", err)
	}

	embeddings, err := embedder.Embed(context.Background(), []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("embed with vllm provider: %v", err)
	}
	if len(embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(embeddings))
	}
	if len(embeddings[0]) == 0 || len(embeddings[1]) == 0 {
		t.Fatal("expected non-empty embeddings")
	}
	if embeddings[0][0] != 1 || embeddings[1][0] != 2 {
		t.Fatalf("expected normalized embedding order [1,2], got [%v,%v]", embeddings[0][0], embeddings[1][0])
	}
}

func TestNewEmbedderRejectsUnsupportedProvider(t *testing.T) {
	_, err := newEmbedder(config.Config{EmbeddingProvider: "openai"})
	if err == nil {
		t.Fatal("expected unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported embedding provider") {
		t.Fatalf("expected unsupported provider message, got %v", err)
	}
}

func TestQdrantPointIDDeterministicUUIDFormat(t *testing.T) {
	first := qdrantPointID("go-json-decode")
	second := qdrantPointID("go-json-decode")

	if first != second {
		t.Fatalf("expected deterministic qdrant point id, got %q and %q", first, second)
	}
	if !uuidPattern.MatchString(first) {
		t.Fatalf("expected UUID-formatted qdrant point id, got %q", first)
	}

	other := qdrantPointID("go-http-timeout")
	if first == other {
		t.Fatalf("expected distinct qdrant point ids for different source ids, got %q", first)
	}
}
