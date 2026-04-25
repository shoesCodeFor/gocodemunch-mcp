package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
)

func TestCallToolRejectsProjectDisabledTool(t *testing.T) {
	store := mustIndexStore(t)
	sourceRoot := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(sourceRoot, "main.py"),
		[]byte("def main():\n    return 1\n"),
		0o644,
	); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(sourceRoot, ".jcodemunch.jsonc"),
		[]byte(`{"disabled_tools": ["get_file_tree"]}`),
		0o644,
	); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	repoID := "local/project-disabled"
	if err := store.Save(context.Background(), repoID, storage.RepoIndex{
		Repo:         repoID,
		IndexedAt:    time.Now().UTC().Format(time.RFC3339),
		SourceRoot:   sourceRoot,
		DisplayName:  "project-disabled",
		Languages:    map[string]int{"python": 1},
		IndexVersion: repoIndexVersion,
		Files: map[string]string{
			"main.py": "hash",
		},
		FileMTimes: map[string]int64{
			"main.py": time.Now().Unix(),
		},
		Symbols: map[string]any{},
	}); err != nil {
		t.Fatalf("seed repo index: %v", err)
	}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
	})

	payload := service.CallTool(context.Background(), "get_file_tree", map[string]any{
		"repo": repoID,
	})
	errorText, _ := payload["error"].(string)
	if !strings.Contains(errorText, "Tool 'get_file_tree' is disabled in this project's configuration.") {
		t.Fatalf("expected project disabled tool error, got %#v", payload)
	}
}
