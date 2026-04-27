package main

import (
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
