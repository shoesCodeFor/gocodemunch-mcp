package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/watcher"
)

func TestStrictFreshnessWaitAndMetaInjection(t *testing.T) {
	store := mustIndexStore(t)
	repoID := seedRepoIndex(t, store)
	controller := watcher.NewStateController()

	cfg := config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "strict",
		Disabled:      map[string]struct{}{},
	}
	service := New(cfg, Dependencies{
		IndexStore: store,
		Watcher:    controller,
	})

	controller.MarkReindexStart(repoID)
	go func() {
		time.Sleep(120 * time.Millisecond)
		controller.MarkReindexDone(repoID)
	}()

	started := time.Now()
	payload := service.CallTool(context.Background(), "get_file_tree", map[string]any{
		"repo": repoID,
	})
	elapsed := time.Since(started)
	if elapsed < 90*time.Millisecond {
		t.Fatalf("expected strict freshness pre-wait, elapsed=%s payload=%#v", elapsed, payload)
	}

	if got, _ := payload["error"].(string); got != "" {
		t.Fatalf("unexpected strict-mode error payload: %#v", payload)
	}

	meta := mustMetaMap(t, payload)
	if value, ok := meta["index_stale"].(bool); !ok || value {
		t.Fatalf("expected strict wait to return fresh index_stale=false: %#v", payload)
	}
	if value, ok := meta["reindex_in_progress"].(bool); !ok || value {
		t.Fatalf("expected strict wait to return reindex_in_progress=false: %#v", payload)
	}
	if _, ok := meta["stale_since_ms"]; !ok {
		t.Fatalf("expected stale_since_ms in _meta: %#v", payload)
	}
}

func TestWaitForFreshEscalationAndGlobalFreshnessMeta(t *testing.T) {
	store := mustIndexStore(t)
	repoID := seedRepoIndex(t, store)
	controller := watcher.NewStateController()

	cfg := config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}
	service := New(cfg, Dependencies{
		IndexStore: store,
		Watcher:    controller,
	})

	controller.MarkReindexStart(repoID)
	relaxedPayload := service.CallTool(context.Background(), "get_file_tree", map[string]any{
		"repo": repoID,
	})
	relaxedMeta := mustMetaMap(t, relaxedPayload)
	if value, ok := relaxedMeta["index_stale"].(bool); !ok || !value {
		t.Fatalf("expected relaxed-mode stale metadata while reindexing: %#v", relaxedPayload)
	}
	if value, ok := relaxedMeta["reindex_in_progress"].(bool); !ok || !value {
		t.Fatalf("expected relaxed-mode in-progress metadata while reindexing: %#v", relaxedPayload)
	}

	controller.MarkReindexFailed(repoID, "failure-1")
	firstFailure := service.CallTool(context.Background(), "wait_for_fresh", map[string]any{
		"repo":       repoID,
		"timeout_ms": 50,
	})
	if fresh, ok := firstFailure["fresh"].(bool); !ok || !fresh {
		t.Fatalf("expected first failure to remain tolerant in wait_for_fresh: %#v", firstFailure)
	}

	controller.MarkReindexStart(repoID)
	controller.MarkReindexFailed(repoID, "failure-2")
	secondFailure := service.CallTool(context.Background(), "wait_for_fresh", map[string]any{
		"repo":       repoID,
		"timeout_ms": 50,
	})
	if fresh, ok := secondFailure["fresh"].(bool); !ok || fresh {
		t.Fatalf("expected second failure to escalate in wait_for_fresh: %#v", secondFailure)
	}
	if reason, _ := secondFailure["reason"].(string); reason != "reindex_failed" {
		t.Fatalf("unexpected wait_for_fresh reason after escalation: %#v", secondFailure)
	}
	if reindexError, _ := secondFailure["reindex_error"].(string); reindexError != "failure-2" {
		t.Fatalf("unexpected reindex_error after escalation: %#v", secondFailure)
	}
	if failures, ok := secondFailure["reindex_failures"].(int); !ok || failures != 2 {
		t.Fatalf("unexpected reindex_failures after escalation: %#v", secondFailure)
	}

	controller.MarkReindexStart("local/in-progress")
	globalPayload := service.CallTool(context.Background(), "get_symbol_diff", map[string]any{
		"repo_a": "local/missing-a",
		"repo_b": "local/missing-b",
	})
	globalMeta := mustMetaMap(t, globalPayload)
	if value, ok := globalMeta["index_stale"].(bool); !ok || !value {
		t.Fatalf("expected global stale marker for non-repo tool path: %#v", globalPayload)
	}
	if value, ok := globalMeta["reindex_in_progress"].(bool); !ok || !value {
		t.Fatalf("expected global in-progress marker for non-repo tool path: %#v", globalPayload)
	}
	if staleSince, ok := globalMeta["stale_since_ms"]; !ok || staleSince != nil {
		t.Fatalf("expected global stale_since_ms=nil for non-repo tool path: %#v", globalPayload)
	}
}

func mustIndexStore(t *testing.T) *storage.SQLiteIndexStore {
	t.Helper()
	store, err := storage.NewSQLiteIndexStore(t.TempDir())
	if err != nil {
		t.Fatalf("create index store: %v", err)
	}
	return store
}

func seedRepoIndex(t *testing.T, store *storage.SQLiteIndexStore) string {
	t.Helper()

	sourceRoot := t.TempDir()
	filePath := filepath.Join(sourceRoot, "main.py")
	if err := os.WriteFile(filePath, []byte("def main():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	repoID := "local/repo-seeded"
	index := storage.RepoIndex{
		Repo:         repoID,
		IndexedAt:    time.Now().UTC().Format(time.RFC3339),
		SourceRoot:   sourceRoot,
		DisplayName:  "repo-seeded",
		Languages:    map[string]int{"python": 1},
		IndexVersion: repoIndexVersion,
		Files: map[string]string{
			"main.py": "hash",
		},
		FileMTimes: map[string]int64{
			"main.py": time.Now().Unix(),
		},
		Symbols: map[string]any{},
	}
	if err := store.Save(context.Background(), repoID, index); err != nil {
		t.Fatalf("seed repo index: %v", err)
	}
	return repoID
}

func mustMetaMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	meta, ok := payload["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("payload missing _meta: %#v", payload)
	}
	return meta
}
