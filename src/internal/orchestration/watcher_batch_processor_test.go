package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/watcher"
)

func TestIndexFolderChangedPathsAppliesIncrementalDelta(t *testing.T) {
	store := mustIndexStore(t)
	controller := watcher.NewStateControllerWithStoragePath(t.TempDir())
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
		Watcher:    controller,
	})

	repoRoot := t.TempDir()
	changedFile := filepath.Join(repoRoot, "main.py")
	deletedFile := filepath.Join(repoRoot, "legacy.go")
	newFile := filepath.Join(repoRoot, "ui.ts")

	if err := os.WriteFile(changedFile, []byte("def main():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("seed changed file: %v", err)
	}
	if err := os.WriteFile(deletedFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("seed deleted file: %v", err)
	}

	full := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": false,
	})
	if success, _ := full["success"].(bool); !success {
		t.Fatalf("initial full index failed: %#v", full)
	}
	repoID, _ := full["repo"].(string)
	if strings.TrimSpace(repoID) == "" {
		t.Fatalf("expected repo id in initial full index payload: %#v", full)
	}

	if err := os.WriteFile(changedFile, []byte("def main():\n    return 2\n"), 0o644); err != nil {
		t.Fatalf("mutate changed file: %v", err)
	}
	if err := os.WriteFile(newFile, []byte("export const answer = 42;\n"), 0o644); err != nil {
		t.Fatalf("seed new file: %v", err)
	}
	if err := os.Remove(deletedFile); err != nil {
		t.Fatalf("delete legacy file: %v", err)
	}

	incremental := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": true,
		"changed_paths": []map[string]any{
			{"change_type": "modified", "path": changedFile},
			{"change_type": "added", "path": newFile},
			{"change_type": "deleted", "path": deletedFile},
		},
	})
	if success, _ := incremental["success"].(bool); !success {
		t.Fatalf("changed_paths incremental index failed: %#v", incremental)
	}
	if got, _ := incremental["changed"].(int); got != 1 {
		t.Fatalf("expected changed=1, got %d (%#v)", got, incremental)
	}
	if got, _ := incremental["new"].(int); got != 1 {
		t.Fatalf("expected new=1, got %d (%#v)", got, incremental)
	}
	if got, _ := incremental["deleted"].(int); got != 1 {
		t.Fatalf("expected deleted=1, got %d (%#v)", got, incremental)
	}

	index, err := store.Load(context.Background(), repoID)
	if err != nil {
		t.Fatalf("load updated index: %v", err)
	}
	if _, ok := index.Files["main.py"]; !ok {
		t.Fatalf("expected main.py in updated index: %#v", index.Files)
	}
	if _, ok := index.Files["ui.ts"]; !ok {
		t.Fatalf("expected ui.ts in updated index: %#v", index.Files)
	}
	if _, ok := index.Files["legacy.go"]; ok {
		t.Fatalf("expected legacy.go to be removed from updated index: %#v", index.Files)
	}

	wantChangedHash, err := hashFile(changedFile)
	if err != nil {
		t.Fatalf("hash changed file: %v", err)
	}
	if got := index.Files["main.py"]; got != wantChangedHash {
		t.Fatalf("unexpected main.py hash %q (want %q)", got, wantChangedHash)
	}
	if got := index.Languages["python"]; got != 1 {
		t.Fatalf("expected python language count=1, got %d", got)
	}
	if got := index.Languages["typescript"]; got != 1 {
		t.Fatalf("expected typescript language count=1, got %d", got)
	}
	if _, ok := index.Languages["go"]; ok {
		t.Fatalf("expected go language bucket to be removed after deletion: %#v", index.Languages)
	}
}

func TestWatcherBatchProcessorRoutesToIndexFolder(t *testing.T) {
	store := mustIndexStore(t)
	controller := watcher.NewStateControllerWithStoragePath(t.TempDir())
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
		Watcher:    controller,
	})

	repoRoot := t.TempDir()
	filePath := filepath.Join(repoRoot, "main.py")
	if err := os.WriteFile(filePath, []byte("def main():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("seed source file: %v", err)
	}

	full := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": false,
	})
	if success, _ := full["success"].(bool); !success {
		t.Fatalf("initial full index failed: %#v", full)
	}
	repoID, _ := full["repo"].(string)

	processor := service.WatcherBatchProcessor()
	if processor == nil {
		t.Fatal("expected watcher batch processor")
	}

	if err := os.WriteFile(filePath, []byte("def main():\n    return 7\n"), 0o644); err != nil {
		t.Fatalf("mutate source file: %v", err)
	}

	processErr := processor.ProcessBatch(context.Background(), repoRoot, []watcher.WatcherChange{
		{
			ChangeType: watcher.ChangeModified,
			Path:       filePath,
		},
	})
	if processErr != nil {
		t.Fatalf("process batch returned error: %v", processErr)
	}

	index, err := store.Load(context.Background(), repoID)
	if err != nil {
		t.Fatalf("load updated index: %v", err)
	}
	wantHash, err := hashFile(filePath)
	if err != nil {
		t.Fatalf("hash updated file: %v", err)
	}
	if got := index.Files["main.py"]; got != wantHash {
		t.Fatalf("unexpected post-batch hash %q (want %q)", got, wantHash)
	}

	status, err := controller.Query(context.Background(), repoID)
	if err != nil {
		t.Fatalf("query watcher status: %v", err)
	}
	if status.ReindexInProgress {
		t.Fatalf("expected watcher lifecycle to be idle after batch completion: %#v", status)
	}
	if !status.Fresh {
		t.Fatalf("expected watcher lifecycle to report fresh after batch completion: %#v", status)
	}
}

func TestWatcherBatchProcessorConcurrentBurstsRemainFresh(t *testing.T) {
	store := mustIndexStore(t)
	controller := watcher.NewStateControllerWithStoragePath(t.TempDir())
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
		Watcher:    controller,
	})

	repoRoot := t.TempDir()
	const workerCount = 6
	const roundsPerWorker = 12
	workerFiles := make([]string, 0, workerCount)
	for workerID := 0; workerID < workerCount; workerID++ {
		filePath := filepath.Join(repoRoot, fmt.Sprintf("worker_%02d.py", workerID))
		seed := fmt.Sprintf("def worker_%d():\n    return 0\n", workerID)
		if err := os.WriteFile(filePath, []byte(seed), 0o644); err != nil {
			t.Fatalf("seed worker file %d: %v", workerID, err)
		}
		workerFiles = append(workerFiles, filePath)
	}

	full := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": false,
	})
	if success, _ := full["success"].(bool); !success {
		t.Fatalf("initial full index failed: %#v", full)
	}
	repoID, _ := full["repo"].(string)
	if strings.TrimSpace(repoID) == "" {
		t.Fatalf("expected repo id from initial full index: %#v", full)
	}

	processor := service.WatcherBatchProcessor()
	if processor == nil {
		t.Fatal("expected watcher batch processor")
	}

	errCh := make(chan error, workerCount*roundsPerWorker)
	var wg sync.WaitGroup
	for workerID := 0; workerID < workerCount; workerID++ {
		workerID := workerID
		wg.Add(1)
		go func() {
			defer wg.Done()
			filePath := workerFiles[workerID]
			for round := 1; round <= roundsPerWorker; round++ {
				content := fmt.Sprintf("def worker_%d():\n    return %d\n", workerID, round)
				if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
					errCh <- fmt.Errorf("worker %d round %d write: %w", workerID, round, err)
					return
				}

				if err := processor.ProcessBatch(context.Background(), repoRoot, []watcher.WatcherChange{
					{
						ChangeType: watcher.ChangeModified,
						Path:       filePath,
					},
				}); err != nil {
					errCh <- fmt.Errorf("worker %d round %d process batch: %w", workerID, round, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent watcher batch processing failed: %v", err)
		}
	}

	status, err := controller.Query(context.Background(), repoID)
	if err != nil {
		t.Fatalf("query watcher status: %v", err)
	}
	if status.ReindexInProgress {
		t.Fatalf("expected watcher lifecycle idle after concurrent bursts: %#v", status)
	}
	if !status.Fresh {
		t.Fatalf("expected watcher lifecycle fresh after concurrent bursts: %#v", status)
	}

	index, err := store.Load(context.Background(), repoID)
	if err != nil {
		t.Fatalf("load index after concurrent bursts: %v", err)
	}
	for _, filePath := range workerFiles {
		rel := filepath.Base(filePath)
		wantHash, err := hashFile(filePath)
		if err != nil {
			t.Fatalf("hash worker file %s: %v", filePath, err)
		}
		if gotHash := index.Files[rel]; gotHash != wantHash {
			t.Fatalf("unexpected file hash for %s after bursts: got=%q want=%q", rel, gotHash, wantHash)
		}
	}
}
