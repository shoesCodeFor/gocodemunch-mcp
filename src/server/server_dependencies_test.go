package server

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
	vectorqdrant "github.com/jgravelle/gocodemunch-mcp/src/internal/storage/vector/qdrant"
)

func TestBuildDependenciesInjectsVectorBackendAndEmbedder(t *testing.T) {
	cfg := config.Config{
		StoragePath:               t.TempDir(),
		VectorBackend:             "sqlite",
		VectorTopK:                5,
		VectorQueryTimeoutMS:      8000,
		EmbeddingProvider:         "ollama",
		EmbeddingModel:            "bge-m3",
		OllamaBaseURL:             "http://localhost:11434",
		SavingsTelemetryEnabled:   true,
		SavingsSnapshotIntervalMS: 30000,
		SavingsCompetitorPricing: map[string]config.SavingsCompetitorPricing{
			"codex": {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6},
		},
	}

	deps, err := buildDependencies(serverOptions{cfg: cfg})
	if err != nil {
		t.Fatalf("build dependencies: %v", err)
	}
	if deps.VectorBackend == nil {
		t.Fatal("expected vector backend dependency to be injected")
	}
	if deps.Embedder == nil {
		t.Fatal("expected embedder dependency to be injected")
	}
	if deps.Watcher == nil {
		t.Fatal("expected watcher dependency to be injected")
	}
	if deps.IndexStore == nil {
		t.Fatal("expected index store dependency to be injected")
	}
	if deps.Telemetry == nil {
		t.Fatal("expected telemetry dependency to be injected")
	}

	closeIfPossible(deps.Telemetry)
	closeIfPossible(deps.VectorBackend)
}

func TestBuildVectorBackendRejectsUnsupportedBackend(t *testing.T) {
	_, err := buildVectorBackend(config.Config{
		StoragePath:   t.TempDir(),
		VectorBackend: "redis",
	})
	if err == nil {
		t.Fatal("expected unsupported backend error")
	}
	if !strings.Contains(err.Error(), "unsupported vector backend") {
		t.Fatalf("expected unsupported vector backend error, got %v", err)
	}
}

func TestBuildVectorBackendBuildsQdrantAdapter(t *testing.T) {
	backend, err := buildVectorBackend(config.Config{
		VectorBackend:     "qdrant",
		QdrantURL:         "http://localhost:6333/",
		QdrantAPIKey:      "test-key",
		QdrantCollection:  "unit-test-vectors",
		VectorTopK:        5,
		StoragePath:       t.TempDir(),
		EmbeddingProvider: "ollama",
	})
	if err != nil {
		t.Fatalf("build qdrant vector backend: %v", err)
	}

	adapter, ok := backend.(*vectorqdrant.Adapter)
	if !ok {
		t.Fatalf("expected qdrant adapter, got %T", backend)
	}
	if got := adapter.BaseURL(); got != "http://localhost:6333" {
		t.Fatalf("expected normalized qdrant base URL, got %q", got)
	}
	if got := adapter.Collection(); got != "unit-test-vectors" {
		t.Fatalf("expected qdrant collection unit-test-vectors, got %q", got)
	}
}

func TestBuildEmbedderBuildsVLLMProvider(t *testing.T) {
	embedder, err := buildEmbedder(config.Config{
		EmbeddingProvider:    "vllm",
		VLLMBaseURL:          "http://localhost:8000/v1",
		VLLMModel:            "bge-m3",
		VectorQueryTimeoutMS: 8000,
	})
	if err != nil {
		t.Fatalf("build vllm embedder: %v", err)
	}
	if embedder == nil {
		t.Fatal("expected non-nil vllm embedder")
	}
}

func TestBuildEmbedderRejectsUnsupportedProvider(t *testing.T) {
	_, err := buildEmbedder(config.Config{
		EmbeddingProvider:    "openai",
		EmbeddingModel:       "bge-m3",
		OllamaBaseURL:        "http://localhost:11434",
		VectorQueryTimeoutMS: 8000,
	})
	if err == nil {
		t.Fatal("expected unsupported embedding provider error")
	}
	if !strings.Contains(err.Error(), "unsupported embedding provider") {
		t.Fatalf("expected unsupported embedding provider error, got %v", err)
	}
}

func TestNewPanicsWhenVectorDependencyWiringFails(t *testing.T) {
	t.Setenv("CODE_INDEX_PATH", "~invalid-user/.code-index")
	t.Setenv("VECTOR_BACKEND", "sqlite")
	t.Setenv("EMBEDDING_PROVIDER", "ollama")
	t.Setenv("EMBEDDING_MODEL", "bge-m3")
	t.Setenv("OLLAMA_BASE_URL", "http://localhost:11434")

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected panic when vector dependency wiring fails")
		}

		message := recovered.(error).Error()
		if !strings.Contains(message, "server startup dependency wiring failed") {
			t.Fatalf("unexpected panic message: %s", message)
		}
	}()

	_ = New(bytes.NewBuffer(nil), io.Discard)
}

func TestServerCloseFlushesTelemetrySnapshot(t *testing.T) {
	storagePath := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storagePath)
	t.Setenv("VECTOR_BACKEND", "sqlite")
	t.Setenv("EMBEDDING_PROVIDER", "ollama")
	t.Setenv("EMBEDDING_MODEL", "bge-m3")
	t.Setenv("OLLAMA_BASE_URL", "http://localhost:11434")
	t.Setenv("GOCODEMUNCH_SAVINGS_TELEMETRY_ENABLED", "true")
	t.Setenv("GOCODEMUNCH_SAVINGS_SNAPSHOT_INTERVAL_MS", "60000")
	t.Setenv("GOCODEMUNCH_SAVINGS_CODEX_INPUT_USD_PER_MTOK", "1.50")
	t.Setenv("GOCODEMUNCH_SAVINGS_CODEX_OUTPUT_USD_PER_MTOK", "6.00")
	t.Setenv("GOCODEMUNCH_SAVINGS_CLAUDE_CODE_INPUT_USD_PER_MTOK", "3.00")
	t.Setenv("GOCODEMUNCH_SAVINGS_CLAUDE_CODE_OUTPUT_USD_PER_MTOK", "15.00")
	t.Setenv("GOCODEMUNCH_SAVINGS_AMP_INPUT_USD_PER_MTOK", "1.50")
	t.Setenv("GOCODEMUNCH_SAVINGS_AMP_OUTPUT_USD_PER_MTOK", "6.00")

	srv := New(bytes.NewBuffer(nil), io.Discard)
	if err := srv.Close(); err != nil {
		t.Fatalf("close server: %v", err)
	}

	store, err := storage.NewSQLiteTelemetryStore(storagePath)
	if err != nil {
		t.Fatalf("create telemetry store: %v", err)
	}
	snapshot, err := store.LoadLatestSnapshot(context.Background())
	if err != nil {
		t.Fatalf("load latest telemetry snapshot after server close: %v", err)
	}
	if snapshot.Cumulative.CallCount != 0 || snapshot.Cumulative.SessionCount != 0 {
		t.Fatalf("expected zeroed close snapshot for idle server, got %#v", snapshot)
	}
	if _, err := os.Stat(store.DBPath()); err != nil {
		t.Fatalf("expected persisted telemetry db on close: %v", err)
	}
}
