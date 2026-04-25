package server

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
)

func TestBuildDependenciesInjectsVectorBackendAndEmbedder(t *testing.T) {
	cfg := config.Config{
		StoragePath:          t.TempDir(),
		VectorBackend:        "sqlite",
		VectorTopK:           5,
		VectorQueryTimeoutMS: 8000,
		EmbeddingProvider:    "ollama",
		EmbeddingModel:       "bge-m3",
		OllamaBaseURL:        "http://localhost:11434",
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

func TestBuildEmbedderRejectsUnsupportedProvider(t *testing.T) {
	_, err := buildEmbedder(config.Config{
		EmbeddingProvider:    "vllm",
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
