package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
)

func TestIndexFolderRejectsUntrustedPathWhenTrustedFoldersConfigured(t *testing.T) {
	store := mustIndexStore(t)
	sourceRoot := t.TempDir()
	trustedOther := t.TempDir()

	if err := os.WriteFile(
		filepath.Join(sourceRoot, "main.py"),
		[]byte("def main():\n    return 1\n"),
		0o644,
	); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(sourceRoot, ".jcodemunch.jsonc"),
		[]byte(fmt.Sprintf(`{"trusted_folders": [%q]}`, trustedOther)),
		0o644,
	); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
	})

	payload := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path": sourceRoot,
	})
	if got, _ := payload["success"].(bool); got {
		t.Fatalf("expected untrusted path rejection, got success payload %#v", payload)
	}
	errorText, _ := payload["error"].(string)
	if !strings.Contains(errorText, "is not under trusted_folders") {
		t.Fatalf("expected trusted_folders rejection message, got %#v", payload)
	}
}

func TestIndexFolderBlacklistModeEmptyTrustedListReturnsError(t *testing.T) {
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
		[]byte(`{"trusted_folders": [], "trusted_folders_whitelist_mode": false}`),
		0o644,
	); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
	})

	payload := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path": sourceRoot,
	})
	if got, _ := payload["success"].(bool); got {
		t.Fatalf("expected blacklist empty trusted list rejection, got %#v", payload)
	}
	errorText, _ := payload["error"].(string)
	if !strings.Contains(errorText, "trusted_folders_whitelist_mode is False") {
		t.Fatalf("expected blacklist empty-list error, got %#v", payload)
	}
}

func TestIndexFolderRejectsBroadRootWhenUntrusted(t *testing.T) {
	store := mustIndexStore(t)
	rootPath := string(filepath.Separator)
	if _, err := os.Stat(rootPath); err != nil {
		t.Skipf("root path %q not available: %v", rootPath, err)
	}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
	})

	payload := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path": rootPath,
	})
	if got, _ := payload["success"].(bool); got {
		t.Fatalf("expected broad-root rejection, got %#v", payload)
	}
	errorText, _ := payload["error"].(string)
	if !strings.Contains(errorText, "too broad to index safely") {
		t.Fatalf("expected broad-root safety message, got %#v", payload)
	}
}

func TestIndexFolderRelativePathAddsResolutionWarning(t *testing.T) {
	store := mustIndexStore(t)
	base := t.TempDir()
	sourceRoot := filepath.Join(base, "repo")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("create source root: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(sourceRoot, "main.py"),
		[]byte("def main():\n    return 1\n"),
		0o644,
	); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatalf("chdir to temp base: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previousWD)
	})

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
	})

	payload := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path": "repo",
	})
	if got, _ := payload["success"].(bool); !got {
		t.Fatalf("expected relative-path index to succeed with warning, got %#v", payload)
	}
	warnings := warningsFromPayload(payload)
	if len(warnings) == 0 {
		t.Fatalf("expected relative-path warning, got %#v", payload)
	}

	found := false
	for _, warning := range warnings {
		if strings.Contains(warning, "Relative path 'repo' resolved to") &&
			strings.Contains(warning, filepath.Clean(sourceRoot)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected relative-path resolution warning, got %#v", warnings)
	}
}

func TestIndexFolderFollowSymlinksSkipsSymlinkEscapes(t *testing.T) {
	store := mustIndexStore(t)
	workspace := t.TempDir()
	sourceRoot := filepath.Join(workspace, "repo")
	outsideRoot := filepath.Join(workspace, "outside")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("create source root: %v", err)
	}
	if err := os.MkdirAll(outsideRoot, 0o755); err != nil {
		t.Fatalf("create outside root: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(sourceRoot, "main.py"),
		[]byte("def main():\n    return 1\n"),
		0o644,
	); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	outsideFile := filepath.Join(outsideRoot, "escape.py")
	if err := os.WriteFile(outsideFile, []byte("def escape():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	escapeLink := filepath.Join(sourceRoot, "escape_link.py")
	if err := os.Symlink(outsideFile, escapeLink); err != nil {
		t.Skipf("symlink unavailable on this filesystem: %v", err)
	}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
	})

	payload := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":            sourceRoot,
		"follow_symlinks": true,
	})
	if got, _ := payload["success"].(bool); !got {
		t.Fatalf("expected symlink-escape run to succeed, got %#v", payload)
	}
	if got := intArg(payload, "file_count"); got != 1 {
		t.Fatalf("expected only in-root source file to be indexed, got %#v", payload)
	}

	skipCounts := discoverySkipCounts(payload)
	if got := skipCounts["symlink_escape"]; got != 1 {
		t.Fatalf("expected symlink_escape=1, got %#v", skipCounts)
	}
	if got := skipCounts["symlink"]; got != 0 {
		t.Fatalf("expected plain symlink skips=0 when follow_symlinks=true, got %#v", skipCounts)
	}

	warnings := warningsFromPayload(payload)
	foundEscapeWarning := false
	for _, warning := range warnings {
		if strings.Contains(warning, "Skipped symlink escape:") && strings.Contains(warning, "escape_link.py") {
			foundEscapeWarning = true
			break
		}
	}
	if !foundEscapeWarning {
		t.Fatalf("expected symlink-escape warning, got %#v", warnings)
	}
}

func TestIndexFolderDefaultSkipsSymlinksWithoutEscapeWarning(t *testing.T) {
	store := mustIndexStore(t)
	workspace := t.TempDir()
	sourceRoot := filepath.Join(workspace, "repo")
	outsideRoot := filepath.Join(workspace, "outside")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("create source root: %v", err)
	}
	if err := os.MkdirAll(outsideRoot, 0o755); err != nil {
		t.Fatalf("create outside root: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(sourceRoot, "main.py"),
		[]byte("def main():\n    return 1\n"),
		0o644,
	); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	outsideFile := filepath.Join(outsideRoot, "escape.py")
	if err := os.WriteFile(outsideFile, []byte("def escape():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(sourceRoot, "escape_link.py")); err != nil {
		t.Skipf("symlink unavailable on this filesystem: %v", err)
	}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
	})

	payload := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path": sourceRoot,
	})
	if got, _ := payload["success"].(bool); !got {
		t.Fatalf("expected default symlink skip run to succeed, got %#v", payload)
	}
	if got := intArg(payload, "file_count"); got != 1 {
		t.Fatalf("expected only regular in-root file to be indexed, got %#v", payload)
	}

	skipCounts := discoverySkipCounts(payload)
	if got := skipCounts["symlink"]; got != 1 {
		t.Fatalf("expected symlink=1 for follow_symlinks default false, got %#v", skipCounts)
	}
	if got := skipCounts["symlink_escape"]; got != 0 {
		t.Fatalf("expected symlink_escape=0 when follow_symlinks=false, got %#v", skipCounts)
	}

	for _, warning := range warningsFromPayload(payload) {
		if strings.Contains(warning, "Skipped symlink escape:") {
			t.Fatalf("expected no symlink-escape warning when follow_symlinks=false, got %#v", payload)
		}
	}
}

func discoverySkipCounts(payload map[string]any) map[string]int {
	if payload == nil {
		return map[string]int{}
	}

	value, ok := payload["discovery_skip_counts"]
	if !ok || value == nil {
		return map[string]int{}
	}

	switch typed := value.(type) {
	case map[string]int:
		out := make(map[string]int, len(typed))
		for key, count := range typed {
			out[key] = count
		}
		return out
	case map[string]any:
		out := make(map[string]int, len(typed))
		for key, raw := range typed {
			switch count := raw.(type) {
			case int:
				out[key] = count
			case int32:
				out[key] = int(count)
			case int64:
				out[key] = int(count)
			case float64:
				out[key] = int(count)
			}
		}
		return out
	default:
		return map[string]int{}
	}
}

func warningsFromPayload(payload map[string]any) []string {
	if payload == nil {
		return nil
	}
	switch typed := payload["warnings"].(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			if strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
