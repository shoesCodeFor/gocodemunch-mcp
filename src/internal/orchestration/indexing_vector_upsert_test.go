package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
)

type embedderSpy struct {
	calls [][]string
}

func (e *embedderSpy) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	clonedInputs := append([]string(nil), inputs...)
	e.calls = append(e.calls, clonedInputs)

	embeddings := make([][]float32, len(inputs))
	for i, input := range inputs {
		value := float32(len(strings.TrimSpace(input)) + i + 1)
		embeddings[i] = []float32{value, 1}
	}
	return embeddings, nil
}

func (e *embedderSpy) reset() {
	e.calls = nil
}

type vectorBackendSpy struct {
	upserts          []indexing.VectorUpsertRequest
	deletes          []indexing.VectorDeleteRequest
	deleteNamespaces []indexing.VectorDeleteNamespaceRequest
	upsertErrs       []error
	deleteErrs       []error
	deleteNSErrs     []error
}

func (v *vectorBackendSpy) Upsert(
	_ context.Context,
	request indexing.VectorUpsertRequest,
) (indexing.VectorUpsertResponse, error) {
	v.upserts = append(v.upserts, cloneUpsertRequest(request))
	if err := popVectorBackendErr(&v.upsertErrs); err != nil {
		return indexing.VectorUpsertResponse{}, err
	}
	return indexing.VectorUpsertResponse{Upserted: len(request.Records)}, nil
}

func (v *vectorBackendSpy) Query(
	_ context.Context,
	_ indexing.VectorQueryRequest,
) (indexing.VectorQueryResponse, error) {
	return indexing.VectorQueryResponse{}, nil
}

func (v *vectorBackendSpy) Delete(
	_ context.Context,
	request indexing.VectorDeleteRequest,
) (indexing.VectorDeleteResponse, error) {
	v.deletes = append(v.deletes, cloneDeleteRequest(request))
	if err := popVectorBackendErr(&v.deleteErrs); err != nil {
		return indexing.VectorDeleteResponse{}, err
	}
	return indexing.VectorDeleteResponse{Deleted: len(request.IDs)}, nil
}

func (v *vectorBackendSpy) DeleteNamespace(
	_ context.Context,
	request indexing.VectorDeleteNamespaceRequest,
) (indexing.VectorDeleteNamespaceResponse, error) {
	v.deleteNamespaces = append(v.deleteNamespaces, request)
	if err := popVectorBackendErr(&v.deleteNSErrs); err != nil {
		return indexing.VectorDeleteNamespaceResponse{}, err
	}
	return indexing.VectorDeleteNamespaceResponse{}, nil
}

func (v *vectorBackendSpy) Health(_ context.Context) (indexing.VectorHealthResponse, error) {
	return indexing.VectorHealthResponse{Ready: true, Message: "ok", Metadata: map[string]any{}}, nil
}

func (v *vectorBackendSpy) reset() {
	v.upserts = nil
	v.deletes = nil
	v.deleteNamespaces = nil
	v.upsertErrs = nil
	v.deleteErrs = nil
	v.deleteNSErrs = nil
}

func popVectorBackendErr(queue *[]error) error {
	if queue == nil || len(*queue) == 0 {
		return nil
	}
	err := (*queue)[0]
	*queue = (*queue)[1:]
	return err
}

func cloneUpsertRequest(request indexing.VectorUpsertRequest) indexing.VectorUpsertRequest {
	clonedRecords := make([]indexing.VectorRecord, 0, len(request.Records))
	for _, record := range request.Records {
		clonedRecords = append(clonedRecords, indexing.VectorRecord{
			ID:        record.ID,
			Namespace: record.Namespace,
			Embedding: cloneEmbeddingVector(record.Embedding),
			Metadata:  cloneVectorMetadata(record.Metadata),
		})
	}
	return indexing.VectorUpsertRequest{
		Namespace: request.Namespace,
		Records:   clonedRecords,
	}
}

func cloneDeleteRequest(request indexing.VectorDeleteRequest) indexing.VectorDeleteRequest {
	return indexing.VectorDeleteRequest{
		Namespace: request.Namespace,
		IDs:       append([]string(nil), request.IDs...),
	}
}

func TestIndexFolderFullUpsertsVectorChunks(t *testing.T) {
	store := mustIndexStore(t)
	embedder := &embedderSpy{}
	vectorBackend := &vectorBackendSpy{}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:    store,
		Embedder:      embedder,
		VectorBackend: vectorBackend,
	})

	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "main.py"), []byte("def main():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("seed python file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "util.go"), []byte("package main\n\nfunc util() {}\n"), 0o644); err != nil {
		t.Fatalf("seed go file: %v", err)
	}

	payload := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": false,
	})
	if success, _ := payload["success"].(bool); !success {
		t.Fatalf("expected index_folder success: %#v", payload)
	}

	if len(embedder.calls) != 1 {
		t.Fatalf("expected one embedder call, got %d", len(embedder.calls))
	}
	if len(vectorBackend.upserts) != 1 {
		t.Fatalf("expected one vector upsert call, got %d", len(vectorBackend.upserts))
	}

	repoID, _ := payload["repo"].(string)
	request := vectorBackend.upserts[0]
	if request.Namespace != repoID {
		t.Fatalf("expected upsert namespace %q, got %q", repoID, request.Namespace)
	}
	if len(request.Records) == 0 {
		t.Fatalf("expected upserted records, got %#v", request)
	}
	for _, record := range request.Records {
		if record.ID == "" {
			t.Fatalf("expected non-empty record id: %#v", record)
		}
		if record.Metadata.Fields["source_type"] != "local" {
			t.Fatalf("expected local source_type metadata, got %#v", record.Metadata.Fields)
		}
	}

	gotPaths := upsertRequestPaths(request)
	wantPaths := []string{"main.py", "util.go"}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("unexpected upsert paths: got %#v, want %#v", gotPaths, wantPaths)
	}
}

func TestIndexFolderChangedPathsUpsertsModifiedAndAddedFiles(t *testing.T) {
	store := mustIndexStore(t)
	embedder := &embedderSpy{}
	vectorBackend := &vectorBackendSpy{}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:    store,
		Embedder:      embedder,
		VectorBackend: vectorBackend,
	})

	repoRoot := t.TempDir()
	mainPath := filepath.Join(repoRoot, "main.py")
	legacyPath := filepath.Join(repoRoot, "legacy.go")
	newPath := filepath.Join(repoRoot, "ui.ts")

	if err := os.WriteFile(mainPath, []byte("def main():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("seed main file: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}

	initial := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": false,
	})
	if success, _ := initial["success"].(bool); !success {
		t.Fatalf("initial index_folder failed: %#v", initial)
	}
	legacyChunkIDs := upsertRequestIDsForPath(vectorBackend.upserts[0], "legacy.go")
	if len(legacyChunkIDs) == 0 {
		t.Fatalf("expected initial legacy.go chunk ids, got %#v", vectorBackend.upserts[0])
	}
	embedder.reset()
	vectorBackend.reset()

	if err := os.WriteFile(mainPath, []byte("def main():\n    return 2\n"), 0o644); err != nil {
		t.Fatalf("mutate main file: %v", err)
	}
	if err := os.WriteFile(newPath, []byte("export const answer = 42;\n"), 0o644); err != nil {
		t.Fatalf("seed new file: %v", err)
	}
	if err := os.Remove(legacyPath); err != nil {
		t.Fatalf("delete legacy file: %v", err)
	}

	incremental := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": true,
		"changed_paths": []map[string]any{
			{"change_type": "modified", "path": mainPath},
			{"change_type": "added", "path": newPath},
			{"change_type": "deleted", "path": legacyPath},
		},
	})
	if success, _ := incremental["success"].(bool); !success {
		t.Fatalf("changed_paths index_folder failed: %#v", incremental)
	}
	if got, _ := incremental["changed"].(int); got != 1 {
		t.Fatalf("expected changed=1, got %#v", incremental)
	}
	if got, _ := incremental["new"].(int); got != 1 {
		t.Fatalf("expected new=1, got %#v", incremental)
	}
	if got, _ := incremental["deleted"].(int); got != 1 {
		t.Fatalf("expected deleted=1, got %#v", incremental)
	}

	if len(embedder.calls) != 1 {
		t.Fatalf("expected one embedder call for changed_paths flow, got %d", len(embedder.calls))
	}
	if len(vectorBackend.upserts) != 1 {
		t.Fatalf("expected one vector upsert for changed_paths flow, got %d", len(vectorBackend.upserts))
	}
	if len(vectorBackend.deletes) == 0 {
		t.Fatalf("expected vector delete for removed/stale chunks, got %#v", vectorBackend.deletes)
	}

	gotPaths := upsertRequestPaths(vectorBackend.upserts[0])
	wantPaths := []string{"main.py", "ui.ts"}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("unexpected changed_paths upsert paths: got %#v, want %#v", gotPaths, wantPaths)
	}
	deletedIDs := deleteRequestIDs(vectorBackend.deletes...)
	if !containsAllStrings(deletedIDs, legacyChunkIDs) {
		t.Fatalf("expected deleted IDs to include legacy.go chunks: deleted=%#v legacy=%#v", deletedIDs, legacyChunkIDs)
	}
}

func TestIndexFileUpsertsOnlyOnContentChange(t *testing.T) {
	store := mustIndexStore(t)
	embedder := &embedderSpy{}
	vectorBackend := &vectorBackendSpy{}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:    store,
		Embedder:      embedder,
		VectorBackend: vectorBackend,
	})

	repoRoot := t.TempDir()
	filePath := filepath.Join(repoRoot, "main.py")
	if err := os.WriteFile(filePath, []byte("def main():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	indexed := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": false,
	})
	if success, _ := indexed["success"].(bool); !success {
		t.Fatalf("initial index_folder failed: %#v", indexed)
	}
	previousChunkIDs := upsertRequestIDsForPath(vectorBackend.upserts[0], "main.py")
	if len(previousChunkIDs) == 0 {
		t.Fatalf("expected initial main.py chunk ids, got %#v", vectorBackend.upserts[0])
	}
	embedder.reset()
	vectorBackend.reset()

	unchanged := service.CallTool(context.Background(), "index_file", map[string]any{
		"path": filePath,
	})
	if success, _ := unchanged["success"].(bool); !success {
		t.Fatalf("expected unchanged index_file success: %#v", unchanged)
	}
	if message, _ := unchanged["message"].(string); message != "File unchanged" {
		t.Fatalf("expected unchanged message, got %#v", unchanged)
	}
	if len(vectorBackend.upserts) != 0 {
		t.Fatalf("expected no vector upsert for unchanged file, got %#v", vectorBackend.upserts)
	}

	if err := os.WriteFile(filePath, []byte("def main():\n    return 7\n"), 0o644); err != nil {
		t.Fatalf("mutate file: %v", err)
	}
	changed := service.CallTool(context.Background(), "index_file", map[string]any{
		"path": filePath,
	})
	if success, _ := changed["success"].(bool); !success {
		t.Fatalf("expected changed index_file success: %#v", changed)
	}
	if len(embedder.calls) != 1 {
		t.Fatalf("expected one embedder call for changed file, got %d", len(embedder.calls))
	}
	if len(vectorBackend.upserts) != 1 {
		t.Fatalf("expected one vector upsert for changed file, got %d", len(vectorBackend.upserts))
	}
	if len(vectorBackend.deletes) == 0 {
		t.Fatalf("expected stale vector delete for changed file, got %#v", vectorBackend.deletes)
	}
	if got := upsertRequestPaths(vectorBackend.upserts[0]); !reflect.DeepEqual(got, []string{"main.py"}) {
		t.Fatalf("unexpected upsert paths for changed file: %#v", got)
	}
	deletedIDs := deleteRequestIDs(vectorBackend.deletes...)
	if !containsAllStrings(deletedIDs, previousChunkIDs) {
		t.Fatalf("expected changed-file delete ids to include previous chunks: deleted=%#v previous=%#v", deletedIDs, previousChunkIDs)
	}
}

func TestIndexRepoUpsertsRemoteVectorChunks(t *testing.T) {
	store := mustIndexStore(t)
	embedder := &embedderSpy{}
	vectorBackend := &vectorBackendSpy{}
	acquirer := &staticRepoAcquirer{
		tree: map[string][]byte{
			"src/main.go": []byte("package main\n"),
		},
	}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:    store,
		RepoAcquirer:  acquirer,
		Embedder:      embedder,
		VectorBackend: vectorBackend,
	})

	full := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": true,
	})
	if success, _ := full["success"].(bool); !success {
		t.Fatalf("full index_repo failed: %#v", full)
	}
	if len(vectorBackend.upserts) != 1 {
		t.Fatalf("expected one vector upsert for full index_repo, got %d", len(vectorBackend.upserts))
	}
	if gotPaths := upsertRequestPaths(vectorBackend.upserts[0]); !reflect.DeepEqual(gotPaths, []string{"src/main.go"}) {
		t.Fatalf("unexpected full index_repo upsert paths: %#v", gotPaths)
	}
	for _, record := range vectorBackend.upserts[0].Records {
		if record.Metadata.Fields["source_type"] != "remote" {
			t.Fatalf("expected remote source_type metadata, got %#v", record.Metadata.Fields)
		}
	}

	embedder.reset()
	vectorBackend.reset()
	acquirer.tree = map[string][]byte{
		"src/main.go": []byte("package main\n\nfunc main() {}\n"),
		"pkg/new.py":  []byte("def new_value():\n    return 1\n"),
	}

	incremental := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": true,
	})
	if success, _ := incremental["success"].(bool); !success {
		t.Fatalf("incremental index_repo failed: %#v", incremental)
	}
	if got, _ := incremental["changed"].(int); got != 1 {
		t.Fatalf("expected changed=1, got %#v", incremental)
	}
	if got, _ := incremental["new"].(int); got != 1 {
		t.Fatalf("expected new=1, got %#v", incremental)
	}
	if len(vectorBackend.upserts) != 1 {
		t.Fatalf("expected one vector upsert for incremental index_repo, got %d", len(vectorBackend.upserts))
	}

	gotPaths := upsertRequestPaths(vectorBackend.upserts[0])
	wantPaths := []string{"pkg/new.py", "src/main.go"}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("unexpected incremental index_repo upsert paths: got %#v, want %#v", gotPaths, wantPaths)
	}

	removedChunkIDs := upsertRequestIDsForPath(vectorBackend.upserts[0], "pkg/new.py")
	if len(removedChunkIDs) == 0 {
		t.Fatalf("expected pkg/new.py chunk ids after incremental upsert, got %#v", vectorBackend.upserts[0])
	}

	embedder.reset()
	vectorBackend.reset()
	acquirer.tree = map[string][]byte{
		"src/main.go": []byte("package main\n\nfunc main() {}\n"),
	}

	deletionRun := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": true,
	})
	if success, _ := deletionRun["success"].(bool); !success {
		t.Fatalf("incremental delete index_repo failed: %#v", deletionRun)
	}
	if got, _ := deletionRun["deleted"].(int); got != 1 {
		t.Fatalf("expected deleted=1, got %#v", deletionRun)
	}
	deletedIDs := deleteRequestIDs(vectorBackend.deletes...)
	if !containsAllStrings(deletedIDs, removedChunkIDs) {
		t.Fatalf("expected deleted ids to include removed remote file chunks: deleted=%#v removed=%#v", deletedIDs, removedChunkIDs)
	}
}

func TestInvalidateCacheDeletesVectorNamespace(t *testing.T) {
	store := mustIndexStore(t)
	vectorBackend := &vectorBackendSpy{}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:    store,
		VectorBackend: vectorBackend,
	})

	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "main.py"), []byte("def main():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	indexed := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": false,
	})
	if success, _ := indexed["success"].(bool); !success {
		t.Fatalf("initial index_folder failed: %#v", indexed)
	}
	repoID, _ := indexed["repo"].(string)
	vectorBackend.reset()

	invalidate := service.CallTool(context.Background(), "invalidate_cache", map[string]any{
		"repo": repoID,
	})
	if success, _ := invalidate["success"].(bool); !success {
		t.Fatalf("invalidate_cache failed: %#v", invalidate)
	}
	if len(vectorBackend.deleteNamespaces) != 1 {
		t.Fatalf("expected one vector namespace delete call, got %#v", vectorBackend.deleteNamespaces)
	}
	if got := vectorBackend.deleteNamespaces[0].Namespace; got != repoID {
		t.Fatalf("expected namespace delete for %q, got %#v", repoID, vectorBackend.deleteNamespaces[0])
	}
}

func TestIndexFolderVectorUpsertRetriesRetryableFailure(t *testing.T) {
	store := mustIndexStore(t)
	embedder := &embedderSpy{}
	vectorBackend := &vectorBackendSpy{
		upsertErrs: []error{
			retryableVectorTestError{message: "temporary upsert failure", retryable: true},
		},
	}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:    store,
		Embedder:      embedder,
		VectorBackend: vectorBackend,
	})

	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "main.py"), []byte("def main():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	payload := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": false,
	})
	if success, _ := payload["success"].(bool); !success {
		t.Fatalf("expected index_folder success after retryable vector failure, got %#v", payload)
	}
	if len(vectorBackend.upserts) != 2 {
		t.Fatalf("expected retried vector upsert calls (2), got %d", len(vectorBackend.upserts))
	}
	if _, hasWarnings := payload["warnings"]; hasWarnings {
		t.Fatalf("did not expect warnings when retry recovered, got %#v", payload["warnings"])
	}
}

func TestIndexFolderVectorUpsertBatchesAreBounded(t *testing.T) {
	store := mustIndexStore(t)
	embedder := &embedderSpy{}
	vectorBackend := &vectorBackendSpy{}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:    store,
		Embedder:      embedder,
		VectorBackend: vectorBackend,
	})

	repoRoot := t.TempDir()
	lines := make([]string, 0, 11200)
	for i := 0; i < 11200; i++ {
		lines = append(lines, "value = 1")
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(repoRoot, "main.py"), []byte(content), 0o644); err != nil {
		t.Fatalf("seed large file: %v", err)
	}

	payload := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": false,
	})
	if success, _ := payload["success"].(bool); !success {
		t.Fatalf("expected index_folder success for large file, got %#v", payload)
	}
	if len(vectorBackend.upserts) < 2 {
		t.Fatalf("expected multiple upsert batches for large file, got %d", len(vectorBackend.upserts))
	}
	for i, request := range vectorBackend.upserts {
		if got := len(request.Records); got > defaultVectorUpsertBatchSize {
			t.Fatalf(
				"expected batch %d to have at most %d records, got %d",
				i+1,
				defaultVectorUpsertBatchSize,
				got,
			)
		}
	}
}

type retryableVectorTestError struct {
	message   string
	retryable bool
}

func (e retryableVectorTestError) Error() string {
	return e.message
}

func (e retryableVectorTestError) Retryable() bool {
	return e.retryable
}

func upsertRequestPaths(request indexing.VectorUpsertRequest) []string {
	unique := map[string]struct{}{}
	for _, record := range request.Records {
		unique[record.Metadata.Path] = struct{}{}
	}
	paths := make([]string, 0, len(unique))
	for path := range unique {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func upsertRequestIDsForPath(request indexing.VectorUpsertRequest, path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}

	ids := make([]string, 0)
	for _, record := range request.Records {
		if record.Metadata.Path != path {
			continue
		}
		ids = append(ids, record.ID)
	}
	sort.Strings(ids)
	return ids
}

func deleteRequestIDs(requests ...indexing.VectorDeleteRequest) []string {
	ids := make([]string, 0)
	for _, request := range requests {
		ids = append(ids, request.IDs...)
	}
	return normalizeUniqueVectorIDs(ids)
}

func containsAllStrings(haystack []string, needles []string) bool {
	if len(needles) == 0 {
		return true
	}

	seen := map[string]struct{}{}
	for _, value := range haystack {
		seen[value] = struct{}{}
	}
	for _, value := range needles {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}
