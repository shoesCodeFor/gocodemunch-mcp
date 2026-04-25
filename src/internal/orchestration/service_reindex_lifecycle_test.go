package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/watcher"
)

type watcherLifecycleSpy struct {
	calls []string
}

func (w *watcherLifecycleSpy) Start(_ context.Context, folder string) error {
	w.calls = append(w.calls, "watch-start:"+strings.TrimSpace(folder))
	return nil
}

func (w *watcherLifecycleSpy) Stop(_ context.Context, folder string) error {
	w.calls = append(w.calls, "watch-stop:"+strings.TrimSpace(folder))
	return nil
}

func (w *watcherLifecycleSpy) Query(_ context.Context, repo string) (watcher.Status, error) {
	return watcher.Status{
		Repo:              repo,
		Fresh:             true,
		IndexStale:        false,
		ReindexInProgress: false,
	}, nil
}
func (w *watcherLifecycleSpy) WaitForFresh(_ context.Context, repo string, _ int) (watcher.Status, error) {
	return watcher.Status{
		Repo:              repo,
		Fresh:             true,
		IndexStale:        false,
		ReindexInProgress: false,
	}, nil
}
func (w *watcherLifecycleSpy) Backpressure(_ context.Context) map[string]any {
	return map[string]any{
		"watched_repo_count":      0,
		"active_reindex_count":    0,
		"any_reindex_in_progress": false,
	}
}
func (w *watcherLifecycleSpy) MarkReindexStart(repo string) {
	w.calls = append(w.calls, "start:"+repo)
}
func (w *watcherLifecycleSpy) MarkReindexDone(repo string) {
	w.calls = append(w.calls, "done:"+repo)
}
func (w *watcherLifecycleSpy) MarkReindexFailed(repo, errMessage string) {
	w.calls = append(w.calls, fmt.Sprintf("failed:%s:%s", repo, strings.TrimSpace(errMessage)))
}
func (w *watcherLifecycleSpy) reset() {
	w.calls = nil
}

func TestIndexingHandlersEmitWatcherLifecycleTransitions(t *testing.T) {
	store := mustIndexStore(t)
	spy := &watcherLifecycleSpy{}
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
		Watcher:    spy,
	})

	emptyRepo := t.TempDir()
	if err := os.WriteFile(filepath.Join(emptyRepo, "README.md"), []byte("# unsupported\n"), 0o644); err != nil {
		t.Fatalf("seed empty repo: %v", err)
	}
	failed := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        emptyRepo,
		"incremental": false,
	})
	if success, _ := failed["success"].(bool); success {
		t.Fatalf("expected unsupported-only folder index to fail: %#v", failed)
	}
	failedRoot := emptyRepo
	if resolved, err := filepath.EvalSymlinks(emptyRepo); err == nil && resolved != "" {
		failedRoot = resolved
	}
	failedRepo := localRepoID(failedRoot)
	if len(spy.calls) != 3 {
		t.Fatalf("expected watch-start+start+failed calls for index_folder failure: %#v", spy.calls)
	}
	if spy.calls[0] != "watch-start:"+failedRoot {
		t.Fatalf("unexpected first failure lifecycle call: %#v", spy.calls)
	}
	if spy.calls[1] != "start:"+failedRepo {
		t.Fatalf("unexpected first failure lifecycle call: %#v", spy.calls)
	}
	if !strings.HasPrefix(spy.calls[2], "failed:"+failedRepo+":") {
		t.Fatalf("unexpected third failure lifecycle call: %#v", spy.calls)
	}

	spy.reset()
	repoRoot := t.TempDir()
	pythonFile := filepath.Join(repoRoot, "main.py")
	if err := os.WriteFile(pythonFile, []byte("def main():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("seed repo source: %v", err)
	}

	indexed := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": false,
	})
	if success, _ := indexed["success"].(bool); !success {
		t.Fatalf("expected index_folder success: %#v", indexed)
	}
	indexedRepo, _ := indexed["repo"].(string)
	indexedRoot, _ := indexed["folder_path"].(string)
	if len(spy.calls) != 3 {
		t.Fatalf("expected watch-start+start+done calls for index_folder success: %#v", spy.calls)
	}
	if spy.calls[0] != "watch-start:"+indexedRoot || spy.calls[1] != "start:"+indexedRepo || spy.calls[2] != "done:"+indexedRepo {
		t.Fatalf("unexpected success lifecycle call order: %#v", spy.calls)
	}

	spy.reset()
	indexFile := service.CallTool(context.Background(), "index_file", map[string]any{
		"path": pythonFile,
	})
	if success, _ := indexFile["success"].(bool); !success {
		t.Fatalf("expected index_file success: %#v", indexFile)
	}
	if len(spy.calls) != 3 {
		t.Fatalf("expected watch-start+start+done calls for index_file success: %#v", spy.calls)
	}
	if spy.calls[0] != "watch-start:"+indexedRoot || spy.calls[1] != "start:"+indexedRepo || spy.calls[2] != "done:"+indexedRepo {
		t.Fatalf("unexpected index_file lifecycle call order: %#v", spy.calls)
	}

	spy.reset()
	invalidate := service.CallTool(context.Background(), "invalidate_cache", map[string]any{
		"repo": indexedRepo,
	})
	if success, _ := invalidate["success"].(bool); !success {
		t.Fatalf("expected invalidate_cache success: %#v", invalidate)
	}
	if len(spy.calls) != 1 || spy.calls[0] != "watch-stop:"+indexedRoot {
		t.Fatalf("expected watch-stop call for invalidate_cache: %#v", spy.calls)
	}

	spy.reset()
	reindexed := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":        repoRoot,
		"incremental": false,
	})
	if success, _ := reindexed["success"].(bool); !success {
		t.Fatalf("expected index_folder restart success: %#v", reindexed)
	}
	if len(spy.calls) != 3 {
		t.Fatalf("expected watch-start+start+done calls for index restart: %#v", spy.calls)
	}
	if spy.calls[0] != "watch-start:"+indexedRoot || spy.calls[1] != "start:"+indexedRepo || spy.calls[2] != "done:"+indexedRepo {
		t.Fatalf("unexpected index restart lifecycle call order: %#v", spy.calls)
	}
}
