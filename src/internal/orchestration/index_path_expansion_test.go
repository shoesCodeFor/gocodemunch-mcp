package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
)

func TestIndexFolderExpandsHomePath(t *testing.T) {
	store := mustIndexStore(t)
	base := t.TempDir()
	homeDir := filepath.Join(base, "home")
	repoRoot := filepath.Join(homeDir, "repo")

	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repoRoot, "main.py"),
		[]byte("def main():\n    return 1\n"),
		0o644,
	); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	t.Setenv("HOME", homeDir)
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
	})

	payload := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path": "~/repo",
	})
	if got, _ := payload["success"].(bool); !got {
		t.Fatalf("expected ~/ path index_folder to succeed, got %#v", payload)
	}
	expectedRoot := filepath.Clean(repoRoot)
	if resolvedRoot, err := filepath.EvalSymlinks(expectedRoot); err == nil && strings.TrimSpace(resolvedRoot) != "" {
		expectedRoot = filepath.Clean(resolvedRoot)
	}
	if got := stringArg(payload, "folder_path", ""); filepath.Clean(got) != expectedRoot {
		t.Fatalf("expected folder_path to resolve to %q, got %#v", expectedRoot, payload)
	}

	for _, warning := range warningsFromPayload(payload) {
		if strings.Contains(warning, "Relative path") {
			t.Fatalf("expected ~/ input to avoid relative-path warning, got %#v", payload)
		}
	}
}

func TestIndexFileExpandsHomePath(t *testing.T) {
	store := mustIndexStore(t)
	base := t.TempDir()
	homeDir := filepath.Join(base, "home")
	repoRoot := filepath.Join(homeDir, "repo")

	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	sourceFile := filepath.Join(repoRoot, "main.py")
	if err := os.WriteFile(sourceFile, []byte("def main():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	t.Setenv("HOME", homeDir)
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
	})

	indexed := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path": repoRoot,
	})
	if got, _ := indexed["success"].(bool); !got {
		t.Fatalf("expected setup index_folder to succeed, got %#v", indexed)
	}

	payload := service.CallTool(context.Background(), "index_file", map[string]any{
		"path": "~/repo/main.py",
	})
	if got, _ := payload["success"].(bool); !got {
		t.Fatalf("expected ~/ path index_file to succeed, got %#v", payload)
	}
	if got := stringArg(payload, "file", ""); got != "main.py" {
		t.Fatalf("expected file=main.py, got %#v", payload)
	}
}
