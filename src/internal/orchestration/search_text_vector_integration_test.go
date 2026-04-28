package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	vectorsqlite "github.com/jgravelle/gocodemunch-mcp/src/internal/storage/vector/sqlite"
)

type keywordSemanticEmbedder struct{}

func (e *keywordSemanticEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	embeddings := make([][]float32, len(inputs))
	for i, input := range inputs {
		text := strings.ToLower(strings.TrimSpace(input))
		alphaScore := float32(1 + strings.Count(text, "alpha"))
		betaScore := float32(1 + strings.Count(text, "beta"))
		betaScore += float32(strings.Count(text, "rank") * 2)
		embeddings[i] = []float32{alphaScore, betaScore}
	}
	return embeddings, nil
}

func TestSearchTextSemanticHybridAndVectorSyncIntegration(t *testing.T) {
	store := mustIndexStore(t)
	vectorBackend, err := vectorsqlite.NewAdapter(t.TempDir())
	if err != nil {
		t.Fatalf("create sqlite vector backend: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := vectorBackend.Close(); closeErr != nil {
			t.Errorf("close sqlite vector backend: %v", closeErr)
		}
	})

	embedder := &keywordSemanticEmbedder{}
	service := New(config.Config{
		ServerName:           "gocodemunch-mcp",
		ServerVersion:        "test",
		FreshnessMode:        "relaxed",
		VectorTopK:           8,
		VectorLexicalWeight:  0.2,
		VectorSemanticWeight: 0.8,
		Disabled:             map[string]struct{}{},
	}, Dependencies{
		IndexStore:    store,
		Embedder:      embedder,
		VectorBackend: vectorBackend,
	})

	repoRoot := t.TempDir()
	alphaPath := filepath.Join(repoRoot, "alpha.py")
	betaPath := filepath.Join(repoRoot, "beta.py")

	if err := os.WriteFile(alphaPath, []byte("def alpha_rank():\n    # TODO rank alpha fallback\n    return 'alpha'\n"), 0o644); err != nil {
		t.Fatalf("seed alpha fixture: %v", err)
	}
	if err := os.WriteFile(betaPath, []byte("def beta_rank():\n    # TODO rank beta preferred\n    return 'beta'\n"), 0o644); err != nil {
		t.Fatalf("seed beta fixture: %v", err)
	}

	indexed := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": false,
	})
	if success, _ := indexed["success"].(bool); !success {
		t.Fatalf("index_folder failed: %#v", indexed)
	}
	repoID, _ := indexed["repo"].(string)
	if strings.TrimSpace(repoID) == "" {
		t.Fatalf("expected non-empty repo id: %#v", indexed)
	}

	baselineVectors := mustQueryVectorPaths(t, vectorBackend, embedder, repoID, "rank")
	if !containsString(baselineVectors, "beta.py") {
		t.Fatalf("expected baseline vector namespace to include beta.py chunks, got %#v", baselineVectors)
	}

	semanticPayload := service.CallTool(context.Background(), "search_text", map[string]any{
		"repo":           repoID,
		"query":          "rank",
		"retrieval_mode": "semantic",
		"max_results":    2,
	})
	assertSearchTopFile(t, semanticPayload, "beta.py")

	hybridSemanticFavored := service.CallTool(context.Background(), "search_text", map[string]any{
		"repo":            repoID,
		"query":           "rank",
		"retrieval_mode":  "hybrid",
		"max_results":     2,
		"lexical_weight":  0.1,
		"semantic_weight": 0.9,
	})
	assertSearchTopFile(t, hybridSemanticFavored, "beta.py")

	hybridLexicalOnly := service.CallTool(context.Background(), "search_text", map[string]any{
		"repo":            repoID,
		"query":           "rank",
		"retrieval_mode":  "hybrid",
		"max_results":     2,
		"lexical_weight":  1.0,
		"semantic_weight": 0.0,
	})
	assertSearchTopFile(t, hybridLexicalOnly, "alpha.py")

	if err := os.WriteFile(alphaPath, []byte("def alpha_rank():\n    # TODO rank alpha refreshed\n    return 'alpha'\n"), 0o644); err != nil {
		t.Fatalf("update alpha fixture: %v", err)
	}
	if err := os.Remove(betaPath); err != nil {
		t.Fatalf("delete beta fixture: %v", err)
	}

	incremental := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": true,
		"changed_paths": []map[string]any{
			{"change_type": "modified", "path": alphaPath},
			{"change_type": "deleted", "path": betaPath},
		},
	})
	if success, _ := incremental["success"].(bool); !success {
		t.Fatalf("incremental index_folder failed: %#v", incremental)
	}
	if got, _ := incremental["deleted"].(int); got != 1 {
		t.Fatalf("expected deleted=1 after removing beta.py, got %#v", incremental)
	}

	semanticAfterDelete := service.CallTool(context.Background(), "search_text", map[string]any{
		"repo":           repoID,
		"query":          "rank",
		"retrieval_mode": "semantic",
		"max_results":    2,
	})
	assertSearchTopFile(t, semanticAfterDelete, "alpha.py")
	if got, _ := semanticAfterDelete["result_count"].(int); got != 1 {
		t.Fatalf("expected one semantic result after beta delete, got %#v", semanticAfterDelete)
	}

	afterDeleteVectors := mustQueryVectorPaths(t, vectorBackend, embedder, repoID, "rank")
	if containsString(afterDeleteVectors, "beta.py") {
		t.Fatalf("expected beta.py vectors to be deleted, got %#v", afterDeleteVectors)
	}
}

func mustQueryVectorPaths(
	t *testing.T,
	vectorBackend indexing.VectorBackend,
	embedder indexing.Embedder,
	namespace string,
	query string,
) []string {
	t.Helper()

	embeddings, err := embedder.Embed(context.Background(), []string{query})
	if err != nil {
		t.Fatalf("embed query %q: %v", query, err)
	}
	if len(embeddings) != 1 {
		t.Fatalf("expected one query embedding for %q, got %d", query, len(embeddings))
	}

	response, err := vectorBackend.Query(context.Background(), indexing.VectorQueryRequest{
		Namespace: namespace,
		Embedding: embeddings[0],
		TopK:      16,
	})
	if err != nil {
		t.Fatalf("query vector namespace %q: %v", namespace, err)
	}

	unique := map[string]struct{}{}
	paths := make([]string, 0, len(response.Matches))
	for _, match := range response.Matches {
		path := strings.TrimSpace(match.Record.Metadata.Path)
		if path == "" {
			continue
		}
		if _, seen := unique[path]; seen {
			continue
		}
		unique[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}

func assertSearchTopFile(t *testing.T, payload map[string]any, wantFile string) {
	t.Helper()

	if errValue, hasErr := payload["error"]; hasErr {
		t.Fatalf("expected search success, got error=%#v payload=%#v", errValue, payload)
	}

	results, ok := payload["results"].([]map[string]any)
	if !ok || len(results) == 0 {
		t.Fatalf("expected non-empty grouped results, got %#v", payload)
	}

	if got := strings.TrimSpace(stringAny(results[0]["file"])); got != wantFile {
		t.Fatalf("unexpected top ranked file: got %q want %q payload=%#v", got, wantFile, payload)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func stringAny(value any) string {
	text, _ := value.(string)
	return text
}
