package sqlite

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
)

func TestSQLiteVectorAdapterBootstrap(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "nested", "vector-store")
	adapter, err := NewAdapter(basePath)
	if err != nil {
		t.Fatalf("create sqlite vector adapter: %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(); err != nil {
			t.Errorf("close sqlite vector adapter: %v", err)
		}
	})

	if got, want := adapter.BasePath(), filepath.Clean(basePath); got != want {
		t.Fatalf("unexpected adapter base path: got %q, want %q", got, want)
	}

	expectedDBPath := filepath.Join(filepath.Clean(basePath), defaultVectorDBFileName)
	if got := adapter.DBPath(); got != expectedDBPath {
		t.Fatalf("unexpected db path: got %q, want %q", got, expectedDBPath)
	}
	if _, err := os.Stat(expectedDBPath); err != nil {
		t.Fatalf("stat sqlite vector db file: %v", err)
	}

	ctx := context.Background()

	var tableCount int
	if err := adapter.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'vector_records'`,
	).Scan(&tableCount); err != nil {
		t.Fatalf("query vector_records table existence: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("expected vector_records table to exist once, got %d", tableCount)
	}

	var indexCount int
	if err := adapter.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_vector_records_namespace'`,
	).Scan(&indexCount); err != nil {
		t.Fatalf("query namespace index existence: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("expected namespace index to exist once, got %d", indexCount)
	}

	var journalMode string
	if err := adapter.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query sqlite journal mode pragma: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("expected sqlite journal_mode WAL, got %q", journalMode)
	}
}

func TestSQLiteVectorAdapterCRUDLifecycle(t *testing.T) {
	adapter := newVectorTestAdapter(t)
	ctx := context.Background()

	const namespace = "local/example"

	upsertResponse, err := adapter.Upsert(ctx, indexing.VectorUpsertRequest{
		Namespace: namespace,
		Records: []indexing.VectorRecord{
			{
				ID:        "chunk-a",
				Embedding: []float32{1, 0},
				Metadata: indexing.VectorMetadata{
					Repo:      "local/example",
					Path:      "a.go",
					Language:  "go",
					ChunkID:   "a-1",
					ChunkText: "func alpha()",
					StartLine: 1,
					EndLine:   8,
					Fields: map[string]any{
						"section": "intro",
					},
				},
			},
			{
				ID:        "chunk-b",
				Embedding: []float32{0, 1},
				Metadata: indexing.VectorMetadata{
					Repo:      "local/example",
					Path:      "b.go",
					Language:  "go",
					ChunkID:   "b-1",
					ChunkText: "func beta()",
					StartLine: 10,
					EndLine:   17,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("seed upsert vector records: %v", err)
	}
	if upsertResponse.Upserted != 2 {
		t.Fatalf("unexpected upserted count: got %d, want 2", upsertResponse.Upserted)
	}

	initialQuery, err := adapter.Query(ctx, indexing.VectorQueryRequest{
		Namespace: namespace,
		Embedding: []float32{1, 0},
		TopK:      10,
	})
	if err != nil {
		t.Fatalf("query seeded vectors: %v", err)
	}
	if len(initialQuery.Matches) != 2 {
		t.Fatalf("expected two query matches, got %d", len(initialQuery.Matches))
	}
	if got := initialQuery.Matches[0].Record.ID; got != "chunk-a" {
		t.Fatalf("expected highest-ranked record chunk-a, got %q", got)
	}
	if !(initialQuery.Matches[0].Score > initialQuery.Matches[1].Score) {
		t.Fatalf("expected first score to be greater than second: %+v", initialQuery.Matches)
	}
	if !almostEqual(initialQuery.Matches[0].Score, 1, 1e-9) {
		t.Fatalf("expected chunk-a cosine score to be 1, got %f", initialQuery.Matches[0].Score)
	}

	fields := initialQuery.Matches[0].Record.Metadata.Fields
	if fields == nil {
		t.Fatalf("expected metadata fields to be initialized")
	}
	if got, ok := fields["section"].(string); !ok || got != "intro" {
		t.Fatalf("expected metadata field section=intro, got %#v", fields["section"])
	}

	updateResponse, err := adapter.Upsert(ctx, indexing.VectorUpsertRequest{
		Namespace: namespace,
		Records: []indexing.VectorRecord{
			{
				ID:        "chunk-b",
				Embedding: []float32{1, 0},
				Metadata: indexing.VectorMetadata{
					Repo:      "local/example",
					Path:      "b-updated.go",
					Language:  "go",
					ChunkID:   "b-2",
					ChunkText: "func betaUpdated()",
					StartLine: 20,
					EndLine:   28,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("update existing vector record: %v", err)
	}
	if updateResponse.Upserted != 1 {
		t.Fatalf("unexpected update upserted count: got %d, want 1", updateResponse.Upserted)
	}

	updatedQuery, err := adapter.Query(ctx, indexing.VectorQueryRequest{
		Namespace: namespace,
		Embedding: []float32{1, 0},
		TopK:      2,
	})
	if err != nil {
		t.Fatalf("query vectors after update: %v", err)
	}
	if got := matchIDs(updatedQuery.Matches); !reflect.DeepEqual(got, []string{"chunk-a", "chunk-b"}) {
		t.Fatalf("unexpected tie ordering after update: got %#v", got)
	}
	if got := updatedQuery.Matches[1].Record.Metadata.Path; got != "b-updated.go" {
		t.Fatalf("expected updated metadata path for chunk-b, got %q", got)
	}

	deleteResponse, err := adapter.Delete(ctx, indexing.VectorDeleteRequest{
		Namespace: namespace,
		IDs:       []string{" chunk-b ", "", "chunk-b"},
	})
	if err != nil {
		t.Fatalf("delete vector ids: %v", err)
	}
	if deleteResponse.Deleted != 1 {
		t.Fatalf("unexpected delete count: got %d, want 1", deleteResponse.Deleted)
	}

	afterDeleteQuery, err := adapter.Query(ctx, indexing.VectorQueryRequest{
		Namespace: namespace,
		Embedding: []float32{1, 0},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("query vectors after delete: %v", err)
	}
	if got := matchIDs(afterDeleteQuery.Matches); !reflect.DeepEqual(got, []string{"chunk-a"}) {
		t.Fatalf("unexpected ids after delete: got %#v", got)
	}

	deleteNamespaceResponse, err := adapter.DeleteNamespace(ctx, indexing.VectorDeleteNamespaceRequest{
		Namespace: namespace,
	})
	if err != nil {
		t.Fatalf("delete vector namespace: %v", err)
	}
	if deleteNamespaceResponse.Deleted != 1 {
		t.Fatalf("unexpected namespace delete count: got %d, want 1", deleteNamespaceResponse.Deleted)
	}

	afterDeleteNamespaceQuery, err := adapter.Query(ctx, indexing.VectorQueryRequest{
		Namespace: namespace,
		Embedding: []float32{1, 0},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("query vectors after namespace delete: %v", err)
	}
	if len(afterDeleteNamespaceQuery.Matches) != 0 {
		t.Fatalf("expected no matches after namespace delete, got %d", len(afterDeleteNamespaceQuery.Matches))
	}
}

func TestSQLiteVectorAdapterDeterministicRankingOrder(t *testing.T) {
	adapter := newVectorTestAdapter(t)
	ctx := context.Background()

	const namespace = "local/deterministic"

	_, err := adapter.Upsert(ctx, indexing.VectorUpsertRequest{
		Namespace: namespace,
		Records: []indexing.VectorRecord{
			{
				ID:        "id-c",
				Embedding: []float32{1, 0},
				Metadata:  indexing.VectorMetadata{Repo: "repo-c", Path: "z.go", ChunkID: "z"},
			},
			{
				ID:        "id-a",
				Embedding: []float32{1, 0},
				Metadata:  indexing.VectorMetadata{Repo: "repo-a", Path: "x.go", ChunkID: "x"},
			},
			{
				ID:        "id-b",
				Embedding: []float32{1, 0},
				Metadata:  indexing.VectorMetadata{Repo: "repo-b", Path: "y.go", ChunkID: "y"},
			},
		},
	})
	if err != nil {
		t.Fatalf("seed deterministic ordering vectors: %v", err)
	}

	request := indexing.VectorQueryRequest{
		Namespace: namespace,
		Embedding: []float32{1, 0},
		TopK:      3,
	}

	firstQuery, err := adapter.Query(ctx, request)
	if err != nil {
		t.Fatalf("first deterministic query: %v", err)
	}
	secondQuery, err := adapter.Query(ctx, request)
	if err != nil {
		t.Fatalf("second deterministic query: %v", err)
	}

	wantIDs := []string{"id-a", "id-b", "id-c"}
	if got := matchIDs(firstQuery.Matches); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("unexpected deterministic ordering from first query: got %#v, want %#v", got, wantIDs)
	}
	if got := matchIDs(secondQuery.Matches); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("unexpected deterministic ordering from second query: got %#v, want %#v", got, wantIDs)
	}

	for _, match := range firstQuery.Matches {
		if !almostEqual(match.Score, 1, 1e-6) {
			t.Fatalf("expected tie scores of 1 for deterministic order test, got %f", match.Score)
		}
		if !almostEqual(match.RawScore, match.Score, 1e-12) {
			t.Fatalf("expected raw score and score to match, got raw=%f score=%f", match.RawScore, match.Score)
		}
	}
}

func TestSQLiteVectorAdapterHealthChecks(t *testing.T) {
	ctx := context.Background()

	adapter, err := NewAdapter(t.TempDir())
	if err != nil {
		t.Fatalf("create sqlite vector adapter: %v", err)
	}
	defer func() {
		_ = adapter.Close()
	}()

	healthy, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("health check for ready adapter: %v", err)
	}
	if !healthy.Ready {
		t.Fatalf("expected healthy adapter to be ready: %#v", healthy)
	}
	if healthy.Message != "ok" {
		t.Fatalf("expected healthy adapter message ok, got %q", healthy.Message)
	}
	if got := healthy.Metadata["backend"]; got != "sqlite" {
		t.Fatalf("expected backend metadata sqlite, got %#v", got)
	}
	if got := healthy.Metadata["base_path"]; got != adapter.BasePath() {
		t.Fatalf("unexpected health base_path metadata: got %#v, want %q", got, adapter.BasePath())
	}
	if got := healthy.Metadata["db_path"]; got != adapter.DBPath() {
		t.Fatalf("unexpected health db_path metadata: got %#v, want %q", got, adapter.DBPath())
	}

	if err := adapter.Close(); err != nil {
		t.Fatalf("close sqlite vector adapter for unhealthy check: %v", err)
	}

	unhealthy, err := adapter.Health(ctx)
	if err == nil {
		t.Fatalf("expected health check error after closing adapter")
	}
	if unhealthy.Ready {
		t.Fatalf("expected closed adapter to be unhealthy: %#v", unhealthy)
	}
	if unhealthy.Message != "sqlite ping failed" {
		t.Fatalf("expected unhealthy message sqlite ping failed, got %q", unhealthy.Message)
	}

	var nilAdapter *Adapter
	nilHealth, err := nilAdapter.Health(ctx)
	if err == nil {
		t.Fatalf("expected nil adapter health check to return error")
	}
	if nilHealth.Ready {
		t.Fatalf("expected nil adapter health ready=false: %#v", nilHealth)
	}
	if nilHealth.Message != "sqlite vector adapter is nil" {
		t.Fatalf("unexpected nil adapter health message: %q", nilHealth.Message)
	}
}

func newVectorTestAdapter(t *testing.T) *Adapter {
	t.Helper()

	adapter, err := NewAdapter(t.TempDir())
	if err != nil {
		t.Fatalf("create sqlite vector adapter: %v", err)
	}

	t.Cleanup(func() {
		if err := adapter.Close(); err != nil {
			t.Errorf("close sqlite vector adapter: %v", err)
		}
	})

	return adapter
}

func matchIDs(matches []indexing.VectorQueryMatch) []string {
	ids := make([]string, len(matches))
	for i, match := range matches {
		ids[i] = match.Record.ID
	}
	return ids
}

func almostEqual(left, right, epsilon float64) bool {
	return math.Abs(left-right) <= epsilon
}
