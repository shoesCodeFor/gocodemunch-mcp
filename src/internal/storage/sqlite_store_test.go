package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSQLiteIndexStoreSaveLoadListDelete(t *testing.T) {
	store, err := NewSQLiteIndexStore(t.TempDir())
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}

	repoID := "local/example"
	index := RepoIndex{
		Repo:         repoID,
		IndexedAt:    "2026-01-01T00:00:00Z",
		SourceRoot:   "/tmp/example",
		DisplayName:  "example",
		Languages:    map[string]int{"go": 1},
		IndexVersion: currentIndexVersion,
		GitHead:      "abc123",
		Files: map[string]string{
			"main.go": "hash-main",
		},
		FileBlobSHAs: map[string]string{
			"main.go": "blob-main",
		},
		FileMTimes: map[string]int64{
			"main.go": 123,
		},
		Symbols: map[string]any{
			"main.go::main#function": map[string]any{"id": "main.go::main#function", "name": "main"},
		},
	}

	if err := store.Save(context.Background(), repoID, index); err != nil {
		t.Fatalf("save index: %v", err)
	}

	if _, err := filepath.Glob(filepath.Join(store.BasePath(), "*.db")); err != nil {
		t.Fatalf("glob sqlite files: %v", err)
	}

	loaded, err := store.Load(context.Background(), repoID)
	if err != nil {
		t.Fatalf("load index: %v", err)
	}
	if loaded.Repo != repoID || loaded.GitHead != "abc123" {
		t.Fatalf("unexpected loaded repo: %#v", loaded)
	}
	if got := loaded.Files["main.go"]; got != "hash-main" {
		t.Fatalf("unexpected file hash %q", got)
	}
	if got := loaded.FileBlobSHAs["main.go"]; got != "blob-main" {
		t.Fatalf("unexpected blob sha %q", got)
	}

	listed, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected one listed repo, got %d", len(listed))
	}
	if listed[0].Repo != repoID || listed[0].FileCount != 1 || listed[0].SymbolCount != 1 {
		t.Fatalf("unexpected listed repo metadata: %#v", listed[0])
	}

	if err := store.Delete(context.Background(), repoID); err != nil {
		t.Fatalf("delete index: %v", err)
	}
	if _, err := store.Load(context.Background(), repoID); err != ErrRepoNotFound {
		t.Fatalf("expected ErrRepoNotFound after delete, got %v", err)
	}
}

func TestSQLiteIndexStoreIncrementalSaveAndDetectChanges(t *testing.T) {
	store, err := NewSQLiteIndexStore(t.TempDir())
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}

	repoID := "local/example"
	index := RepoIndex{
		Repo:         repoID,
		IndexedAt:    "2026-01-01T00:00:00Z",
		SourceRoot:   "/tmp/example",
		DisplayName:  "example",
		Languages:    map[string]int{"python": 2},
		IndexVersion: currentIndexVersion,
		Files: map[string]string{
			"a.py": "hash-a",
			"b.py": "hash-b",
		},
		FileBlobSHAs: map[string]string{
			"a.py": "blob-a",
			"b.py": "blob-b",
		},
		FileMTimes: map[string]int64{
			"a.py": 1,
			"b.py": 2,
		},
		Symbols: map[string]any{},
	}
	if err := store.Save(context.Background(), repoID, index); err != nil {
		t.Fatalf("save index: %v", err)
	}

	index.Files["a.py"] = "hash-a2"
	delete(index.Files, "b.py")
	index.Files["c.py"] = "hash-c"
	index.FileBlobSHAs["a.py"] = "blob-a2"
	delete(index.FileBlobSHAs, "b.py")
	index.FileBlobSHAs["c.py"] = "blob-c"
	index.FileMTimes["a.py"] = 3
	delete(index.FileMTimes, "b.py")
	index.FileMTimes["c.py"] = 4

	if err := store.IncrementalSave(context.Background(), repoID, index, ChangeSet{
		Changed: []string{"a.py"},
		New:     []string{"c.py"},
		Deleted: []string{"b.py"},
	}); err != nil {
		t.Fatalf("incremental save: %v", err)
	}

	loaded, err := store.Load(context.Background(), repoID)
	if err != nil {
		t.Fatalf("load after incremental save: %v", err)
	}
	if _, ok := loaded.Files["b.py"]; ok {
		t.Fatalf("expected b.py to be deleted, got %#v", loaded.Files)
	}
	if loaded.Files["a.py"] != "hash-a2" || loaded.Files["c.py"] != "hash-c" {
		t.Fatalf("unexpected files after incremental save: %#v", loaded.Files)
	}

	changes, err := store.DetectChanges(context.Background(), repoID, []string{"a.py", "c.py", "d.py"})
	if err != nil {
		t.Fatalf("detect changes: %v", err)
	}
	if len(changes.New) != 1 || changes.New[0] != "d.py" {
		t.Fatalf("unexpected new files: %#v", changes.New)
	}
	if len(changes.Deleted) != 0 {
		t.Fatalf("unexpected deleted files: %#v", changes.Deleted)
	}
}

func TestSQLiteIndexStoreMigratesLegacyJSONOnLoadAndListsLegacyRepos(t *testing.T) {
	storeDir := t.TempDir()
	store, err := NewSQLiteIndexStore(storeDir)
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}

	repoID := "local/legacy"
	legacy := sqlitePersistedRepo{
		Repo:         repoID,
		IndexedAt:    "2026-01-01T00:00:00Z",
		SourceRoot:   "/tmp/legacy",
		DisplayName:  "legacy",
		Languages:    map[string]int{"go": 1},
		IndexVersion: currentIndexVersion,
		GitHead:      "deadbeef",
		Files: map[string]string{
			"main.go": "hash-main",
		},
		FileBlobSHAs: map[string]string{
			"main.go": "blob-main",
		},
		FileMTimes: map[string]int64{
			"main.go": 42,
		},
		Symbols: map[string]any{
			"main.go::main#function": map[string]any{"id": "main.go::main#function", "name": "main"},
		},
	}

	owner, name, err := splitRepoID(repoID)
	if err != nil {
		t.Fatalf("split repo id: %v", err)
	}
	legacyPath := filepath.Join(storeDir, repoSlug(owner, name)+".json")
	payload, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy payload: %v", err)
	}
	if err := os.WriteFile(legacyPath, payload, 0o644); err != nil {
		t.Fatalf("write legacy payload: %v", err)
	}

	listed, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	if len(listed) != 1 || listed[0].Repo != repoID {
		t.Fatalf("unexpected listed repos: %#v", listed)
	}

	loaded, err := store.Load(context.Background(), repoID)
	if err != nil {
		t.Fatalf("load migrated index: %v", err)
	}
	if loaded.Repo != repoID || loaded.Files["main.go"] != "hash-main" {
		t.Fatalf("unexpected loaded repo after migration: %#v", loaded)
	}

	dbPath := filepath.Join(storeDir, repoSlug(owner, name)+".db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected sqlite db after migration: %v", err)
	}

	listed, err = store.List(context.Background())
	if err != nil {
		t.Fatalf("list indexes after migration: %v", err)
	}
	if len(listed) != 1 || listed[0].Repo != repoID {
		t.Fatalf("unexpected listed repos after migration: %#v", listed)
	}
	if listed[0].GitHead != "deadbeef" || listed[0].SymbolCount != 1 || listed[0].FileCount != 1 {
		t.Fatalf("unexpected migrated metadata: %#v", listed[0])
	}
}

func TestSQLiteIndexStoreConcurrentSaveLoad(t *testing.T) {
	store, err := NewSQLiteIndexStore(t.TempDir())
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}

	repoID := "local/concurrent"
	index := RepoIndex{
		Repo:         repoID,
		IndexedAt:    "2026-01-01T00:00:00Z",
		SourceRoot:   "/tmp/repo",
		DisplayName:  "concurrent",
		Languages:    map[string]int{"python": 1},
		IndexVersion: currentIndexVersion,
		Files:        map[string]string{"a.py": "h1"},
		FileMTimes:   map[string]int64{"a.py": 1},
		Symbols:      map[string]any{},
	}
	if err := store.Save(context.Background(), repoID, index); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	errCh := make(chan error, 64)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for round := 0; round < 20; round++ {
				loaded, err := store.Load(context.Background(), repoID)
				if err != nil {
					errCh <- err
					return
				}
				loaded.Files["a.py"] = fmt.Sprintf("%d-%d", worker, round)
				loaded.FileMTimes["a.py"] = int64(worker*100 + round)
				if err := store.IncrementalSave(context.Background(), repoID, loaded, ChangeSet{Changed: []string{"a.py"}}); err != nil {
					errCh <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent sqlite store operation failed: %v", err)
		}
	}
}
