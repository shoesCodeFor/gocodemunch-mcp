package orchestration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
)

func TestIndexFolderAppliesLocalDiscoveryFilters(t *testing.T) {
	store := mustIndexStore(t)
	sourceRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(sourceRoot, "nested"), 0o755); err != nil {
		t.Fatalf("create nested folder: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "extra"), 0o755); err != nil {
		t.Fatalf("create extra folder: %v", err)
	}

	writeLocalSourceFile(t, filepath.Join(sourceRoot, "main.py"), "def main():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, "root_ignored.py"), "def ignored_root():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, "nested", "nested_ignored.py"), "def ignored_nested():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, "extra", "ignored_by_extra.py"), "def ignored_extra():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, "api_token.py"), "def token_file():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, ".gitignore"), "root_ignored.py\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, "nested", ".gitignore"), "nested_ignored.py\n")

	tooLarge := bytes.Repeat([]byte("a"), defaultMaxDiscoveryFileSize+1)
	if err := os.WriteFile(filepath.Join(sourceRoot, "too_large.py"), tooLarge, 0o644); err != nil {
		t.Fatalf("write oversized source file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "binary.py"), []byte{0x00, 0x10, 0x20, 0x30}, 0o644); err != nil {
		t.Fatalf("write binary source file: %v", err)
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
		"path":                  sourceRoot,
		"extra_ignore_patterns": []string{"extra/"},
	})
	if got, _ := payload["success"].(bool); !got {
		t.Fatalf("expected index_folder success with filtered files, got %#v", payload)
	}
	if got := intArg(payload, "file_count"); got != 1 {
		t.Fatalf("expected exactly one accepted source file, got %#v", payload)
	}

	files := stringsFromAnySlice(payload["files"])
	if len(files) != 1 {
		t.Fatalf("expected files sample with one item, got %#v", payload["files"])
	}
	if files[0] != "main.py" {
		t.Fatalf("expected only main.py to remain after filtering, got %#v", payload["files"])
	}

	skipCounts := discoverySkipCounts(payload)
	if got := skipCounts["gitignore"]; got != 2 {
		t.Fatalf("expected gitignore=2, got %#v", skipCounts)
	}
	if got := skipCounts["extra_ignore"]; got != 1 {
		t.Fatalf("expected extra_ignore=1, got %#v", skipCounts)
	}
	if got := skipCounts["secret"]; got != 1 {
		t.Fatalf("expected secret=1, got %#v", skipCounts)
	}
	if got := skipCounts["too_large"]; got != 1 {
		t.Fatalf("expected too_large=1, got %#v", skipCounts)
	}
	if got := skipCounts["binary"]; got != 1 {
		t.Fatalf("expected binary=1, got %#v", skipCounts)
	}

	warnings := warningsFromPayload(payload)
	expectContainsWarning(t, warnings, "Skipped secret file: api_token.py")
	expectContainsWarning(t, warnings, "Skipped binary file: binary.py")
}

func TestIndexFolderAppliesProjectConfigDiscoveryOverrides(t *testing.T) {
	store := mustIndexStore(t)
	sourceRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(sourceRoot, "src"), 0o755); err != nil {
		t.Fatalf("create src folder: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("create pkg folder: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "internal"), 0o755); err != nil {
		t.Fatalf("create internal folder: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "vendor"), 0o755); err != nil {
		t.Fatalf("create vendor folder: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "docs"), 0o755); err != nil {
		t.Fatalf("create docs folder: %v", err)
	}

	writeLocalSourceFile(t, filepath.Join(sourceRoot, "src", "high_priority.py"), "def keep_src():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, "pkg", "second_priority.py"), "def keep_pkg():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, "internal", "third_priority.py"), "def keep_internal():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, "vendor", "ignored_vendor.py"), "def ignored_vendor():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, "docs", "api_token.py"), "def keep_token_doc():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, ".jcodemunch.jsonc"), `{
		"max_folder_files": 2,
		"extra_ignore_patterns": ["vendor/"],
		"exclude_secret_patterns": ["*token*"]
	}`)

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
		t.Fatalf("expected index_folder success with project overrides, got %#v", payload)
	}
	if got := intArg(payload, "file_count"); got != 2 {
		t.Fatalf("expected capped file_count=2, got %#v", payload)
	}
	if got := intArg(payload, "files_discovered"); got != 4 {
		t.Fatalf("expected files_discovered=4 after cap, got %#v", payload)
	}
	if got := intArg(payload, "files_indexed"); got != 2 {
		t.Fatalf("expected files_indexed=2 after cap, got %#v", payload)
	}
	if got := intArg(payload, "files_skipped_cap"); got != 2 {
		t.Fatalf("expected files_skipped_cap=2 after cap, got %#v", payload)
	}

	files := stringsFromAnySlice(payload["files"])
	expectedFiles := []string{"src/high_priority.py", "pkg/second_priority.py"}
	if !slicesEqual(files, expectedFiles) {
		t.Fatalf("expected capped priority files %#v, got %#v", expectedFiles, files)
	}

	skipCounts := discoverySkipCounts(payload)
	if got := skipCounts["extra_ignore"]; got != 1 {
		t.Fatalf("expected extra_ignore=1 from project config pattern, got %#v", skipCounts)
	}
	if got := skipCounts["secret"]; got != 0 {
		t.Fatalf("expected secret=0 with exclude_secret_patterns override, got %#v", skipCounts)
	}
	if got := skipCounts["file_limit"]; got != 2 {
		t.Fatalf("expected file_limit=2 from max_folder_files cap, got %#v", skipCounts)
	}

	warnings := warningsFromPayload(payload)
	expectContainsWarning(t, warnings, "File cap reached: 4 files discovered, 2 indexed, 2 dropped")
	expectWarningAbsent(t, warnings, "Skipped secret file: docs/api_token.py")
}

func TestIndexFolderMergesProjectAndCallExtraIgnorePatterns(t *testing.T) {
	store := mustIndexStore(t)
	sourceRoot := t.TempDir()

	writeLocalSourceFile(t, filepath.Join(sourceRoot, "keep.py"), "def keep():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, "project_ignored.py"), "def project_ignored():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, "call_ignored.py"), "def call_ignored():\n    return 1\n")
	writeLocalSourceFile(t, filepath.Join(sourceRoot, ".jcodemunch.jsonc"), `{
		"extra_ignore_patterns": ["project_ignored.py"]
	}`)

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
	})

	payload := service.CallTool(context.Background(), "index_folder", map[string]any{
		"path":                  sourceRoot,
		"extra_ignore_patterns": []string{"call_ignored.py"},
	})
	if got, _ := payload["success"].(bool); !got {
		t.Fatalf("expected index_folder success with merged ignore patterns, got %#v", payload)
	}
	if got := intArg(payload, "file_count"); got != 1 {
		t.Fatalf("expected file_count=1 after merged ignore filters, got %#v", payload)
	}

	files := stringsFromAnySlice(payload["files"])
	expectedFiles := []string{"keep.py"}
	if !slicesEqual(files, expectedFiles) {
		t.Fatalf("expected only keep.py after merged ignore filters, got %#v", files)
	}

	skipCounts := discoverySkipCounts(payload)
	if got := skipCounts["extra_ignore"]; got != 2 {
		t.Fatalf("expected extra_ignore=2 from project+call patterns, got %#v", skipCounts)
	}
}

func writeLocalSourceFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write source file %s: %v", path, err)
	}
}

func expectContainsWarning(t *testing.T, warnings []string, expected string) {
	t.Helper()
	for _, warning := range warnings {
		if strings.Contains(warning, expected) {
			return
		}
	}
	t.Fatalf("expected warning containing %q, got %#v", expected, warnings)
}

func expectWarningAbsent(t *testing.T, warnings []string, unexpected string) {
	t.Helper()
	for _, warning := range warnings {
		if strings.Contains(warning, unexpected) {
			t.Fatalf("did not expect warning containing %q, got %#v", unexpected, warnings)
		}
	}
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func stringsFromAnySlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
