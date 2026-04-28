package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
)

type searchTextEmbedderStub struct {
	calls      [][]string
	embeddings [][]float32
	err        error
}

func (s *searchTextEmbedderStub) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	cloned := append([]string(nil), inputs...)
	s.calls = append(s.calls, cloned)
	if s.err != nil {
		return nil, s.err
	}
	out := make([][]float32, len(s.embeddings))
	for i := range s.embeddings {
		out[i] = append([]float32(nil), s.embeddings[i]...)
	}
	return out, nil
}

type searchTextVectorBackendStub struct {
	queryRequests []indexing.VectorQueryRequest
	queryResponse indexing.VectorQueryResponse
	queryErr      error
}

func (s *searchTextVectorBackendStub) Upsert(
	_ context.Context,
	request indexing.VectorUpsertRequest,
) (indexing.VectorUpsertResponse, error) {
	return indexing.VectorUpsertResponse{Upserted: len(request.Records)}, nil
}

func (s *searchTextVectorBackendStub) Query(
	_ context.Context,
	request indexing.VectorQueryRequest,
) (indexing.VectorQueryResponse, error) {
	s.queryRequests = append(s.queryRequests, request)
	if s.queryErr != nil {
		return indexing.VectorQueryResponse{}, s.queryErr
	}
	return s.queryResponse, nil
}

func (s *searchTextVectorBackendStub) Delete(
	_ context.Context,
	request indexing.VectorDeleteRequest,
) (indexing.VectorDeleteResponse, error) {
	return indexing.VectorDeleteResponse{Deleted: len(request.IDs)}, nil
}

func (s *searchTextVectorBackendStub) DeleteNamespace(
	_ context.Context,
	_ indexing.VectorDeleteNamespaceRequest,
) (indexing.VectorDeleteNamespaceResponse, error) {
	return indexing.VectorDeleteNamespaceResponse{Deleted: 0}, nil
}

func (s *searchTextVectorBackendStub) Health(_ context.Context) (indexing.VectorHealthResponse, error) {
	return indexing.VectorHealthResponse{Ready: true, Message: "ok", Metadata: map[string]any{}}, nil
}

func TestSearchTextSemanticModeUsesVectorRanking(t *testing.T) {
	store := mustIndexStore(t)
	sourceRoot := t.TempDir()
	writeSearchTextFixture(t, sourceRoot, "a.go", "package main\n// TODO parse config\nfunc parse() {}\n")
	writeSearchTextFixture(t, sourceRoot, "b.go", "package main\n// TODO vector rank\nfunc rank() {}\n")

	repoID := "local/retrieval-semantic"
	saveSearchTextIndex(t, store, repoID, sourceRoot, map[string]string{
		"a.go": "hash-a",
		"b.go": "hash-b",
	})

	embedder := &searchTextEmbedderStub{
		embeddings: [][]float32{{0.4, 0.6}},
	}
	vectorBackend := &searchTextVectorBackendStub{
		queryResponse: indexing.VectorQueryResponse{
			Matches: []indexing.VectorQueryMatch{
				{
					Record: indexing.VectorRecord{
						ID:        "b.go::2",
						Namespace: repoID,
						Metadata: indexing.VectorMetadata{
							Path:      "b.go",
							ChunkText: "// TODO vector rank",
							StartLine: 2,
						},
					},
					Score: 0.91,
				},
				{
					Record: indexing.VectorRecord{
						ID:        "a.go::2",
						Namespace: repoID,
						Metadata: indexing.VectorMetadata{
							Path:      "a.go",
							ChunkText: "// TODO parse config",
							StartLine: 2,
						},
					},
					Score: 0.42,
				},
			},
		},
	}

	service := New(config.Config{
		ServerName:           "gocodemunch-mcp",
		ServerVersion:        "test",
		FreshnessMode:        "relaxed",
		VectorTopK:           3,
		VectorLexicalWeight:  0.5,
		VectorSemanticWeight: 0.5,
		Disabled:             map[string]struct{}{},
	}, Dependencies{
		IndexStore:    store,
		Embedder:      embedder,
		VectorBackend: vectorBackend,
	})

	payload := service.CallTool(context.Background(), "search_text", map[string]any{
		"repo":           repoID,
		"query":          "todo rank",
		"retrieval_mode": "semantic",
		"max_results":    2,
		"context_lines":  1,
	})
	if errValue, hasErr := payload["error"]; hasErr {
		t.Fatalf("expected semantic search success, got error: %#v", errValue)
	}

	if len(embedder.calls) != 1 || len(embedder.calls[0]) != 1 || embedder.calls[0][0] != "todo rank" {
		t.Fatalf("expected single embedder query call, got %#v", embedder.calls)
	}
	if len(vectorBackend.queryRequests) != 1 {
		t.Fatalf("expected one vector query request, got %#v", vectorBackend.queryRequests)
	}
	if got := vectorBackend.queryRequests[0].TopK; got != 3 {
		t.Fatalf("expected semantic query topK=3, got %d", got)
	}

	if got := payload["result_count"]; got != 2 {
		t.Fatalf("expected result_count=2, got %#v", payload)
	}
	results := payload["results"].([]map[string]any)
	if got := results[0]["file"]; got != "b.go" {
		t.Fatalf("expected highest-score semantic file b.go first, got %#v", got)
	}
	firstMatch := results[0]["matches"].([]map[string]any)[0]
	if got := firstMatch["score"].(float64); got != 0.91 {
		t.Fatalf("expected semantic score 0.91 for first match, got %v", got)
	}
	if before := firstMatch["before"].([]string); len(before) != 1 || before[0] != "package main" {
		t.Fatalf("expected context before semantic match, got %#v", firstMatch)
	}

	meta := payload["_meta"].(map[string]any)
	if got := meta["retrieval_mode"]; got != "semantic" {
		t.Fatalf("expected semantic retrieval_mode meta, got %#v", meta)
	}
}

func TestSearchTextHybridModeCombinesLexicalAndVectorScores(t *testing.T) {
	store := mustIndexStore(t)
	sourceRoot := t.TempDir()
	writeSearchTextFixture(t, sourceRoot, "a.go", "package main\n// TODO alpha\nfunc alpha() {}\n")
	writeSearchTextFixture(t, sourceRoot, "b.go", "package main\n// TODO beta\nfunc beta() {}\n")

	repoID := "local/retrieval-hybrid"
	saveSearchTextIndex(t, store, repoID, sourceRoot, map[string]string{
		"a.go": "hash-a",
		"b.go": "hash-b",
	})

	embedder := &searchTextEmbedderStub{
		embeddings: [][]float32{{0.1, 0.9}},
	}
	vectorBackend := &searchTextVectorBackendStub{
		queryResponse: indexing.VectorQueryResponse{
			Matches: []indexing.VectorQueryMatch{
				{
					Record: indexing.VectorRecord{
						ID:        "a.go::2",
						Namespace: repoID,
						Metadata: indexing.VectorMetadata{
							Path:      "a.go",
							ChunkText: "// TODO alpha",
							StartLine: 2,
						},
					},
					Score: 0.10,
				},
				{
					Record: indexing.VectorRecord{
						ID:        "b.go::2",
						Namespace: repoID,
						Metadata: indexing.VectorMetadata{
							Path:      "b.go",
							ChunkText: "// TODO beta",
							StartLine: 2,
						},
					},
					Score: 0.90,
				},
			},
		},
	}

	service := New(config.Config{
		ServerName:           "gocodemunch-mcp",
		ServerVersion:        "test",
		FreshnessMode:        "relaxed",
		VectorTopK:           5,
		VectorLexicalWeight:  0.1,
		VectorSemanticWeight: 0.9,
		Disabled:             map[string]struct{}{},
	}, Dependencies{
		IndexStore:    store,
		Embedder:      embedder,
		VectorBackend: vectorBackend,
	})

	hybridPayload := service.CallTool(context.Background(), "search_text", map[string]any{
		"repo":           repoID,
		"query":          "TODO",
		"retrieval_mode": "hybrid",
		"max_results":    2,
	})
	if errValue, hasErr := hybridPayload["error"]; hasErr {
		t.Fatalf("expected hybrid search success, got error: %#v", errValue)
	}
	hybridResults := hybridPayload["results"].([]map[string]any)
	if got := hybridResults[0]["file"]; got != "b.go" {
		t.Fatalf("expected semantic-favored hybrid ranking to put b.go first, got %#v", got)
	}
	firstHybridMatch := hybridResults[0]["matches"].([]map[string]any)[0]
	if _, ok := firstHybridMatch["lexical_score"]; !ok {
		t.Fatalf("expected hybrid match lexical_score, got %#v", firstHybridMatch)
	}
	if _, ok := firstHybridMatch["vector_score"]; !ok {
		t.Fatalf("expected hybrid match vector_score, got %#v", firstHybridMatch)
	}
	meta := hybridPayload["_meta"].(map[string]any)
	if got := meta["lexical_weight"].(float64); got != 0.1 {
		t.Fatalf("expected config-driven lexical weight 0.1, got %v", got)
	}
	if got := meta["semantic_weight"].(float64); got != 0.9 {
		t.Fatalf("expected config-driven semantic weight 0.9, got %v", got)
	}

	lexicalOnlyPayload := service.CallTool(context.Background(), "search_text", map[string]any{
		"repo":            repoID,
		"query":           "TODO",
		"retrieval_mode":  "hybrid",
		"max_results":     2,
		"lexical_weight":  1.0,
		"semantic_weight": 0.0,
	})
	if errValue, hasErr := lexicalOnlyPayload["error"]; hasErr {
		t.Fatalf("expected hybrid override search success, got error: %#v", errValue)
	}
	overrideResults := lexicalOnlyPayload["results"].([]map[string]any)
	if got := overrideResults[0]["file"]; got != "a.go" {
		t.Fatalf("expected lexical-only override to follow lexical ordering (a.go first), got %#v", got)
	}
}

func TestSearchTextRejectsUnknownRetrievalMode(t *testing.T) {
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: mustIndexStore(t),
	})

	payload := service.CallTool(context.Background(), "search_text", map[string]any{
		"repo":           "local/unknown",
		"query":          "TODO",
		"retrieval_mode": "fuzzy",
	})
	if _, ok := payload["error"]; !ok {
		t.Fatalf("expected retrieval_mode validation error, got %#v", payload)
	}
}

func writeSearchTextFixture(t *testing.T, sourceRoot, relativePath, content string) {
	t.Helper()
	absolute := filepath.Join(sourceRoot, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("create fixture parent dir: %v", err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture file %s: %v", relativePath, err)
	}
}

func saveSearchTextIndex(
	t *testing.T,
	store storage.IndexStore,
	repoID string,
	sourceRoot string,
	files map[string]string,
) {
	t.Helper()
	index := storage.RepoIndex{
		Repo:         repoID,
		IndexedAt:    time.Now().UTC().Format(time.RFC3339),
		SourceRoot:   sourceRoot,
		DisplayName:  "retrieval-fixture",
		Languages:    map[string]int{"go": len(files)},
		IndexVersion: repoIndexVersion,
		Files:        files,
		FileMTimes:   map[string]int64{},
		Symbols:      map[string]any{},
	}
	for filePath := range files {
		index.FileMTimes[filePath] = time.Now().Unix()
	}
	if err := store.Save(context.Background(), repoID, index); err != nil {
		t.Fatalf("seed search_text index: %v", err)
	}
}
