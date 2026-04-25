package orchestration

import (
	"errors"
	"testing"
	"time"
)

func TestBuildIndexFileFileNotFoundResult(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		requestedPath string
		wantError     string
	}{
		{
			name:          "empty path keeps trailing delimiter",
			requestedPath: "",
			wantError:     "File not found: ",
		},
		{
			name:          "non-empty path includes requested path",
			requestedPath: "src/main.py",
			wantError:     "File not found: src/main.py",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := buildIndexFileFileNotFoundResult(tc.requestedPath)
			if got, ok := result["success"].(bool); !ok || got {
				t.Fatalf("expected success=false, got %#v", result["success"])
			}
			if got, ok := result["error"].(string); !ok || got != tc.wantError {
				t.Fatalf("expected error %q, got %#v", tc.wantError, result["error"])
			}
		})
	}
}

func TestBuildIndexFileErrorResults(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		buildFunc func() map[string]any
		wantError string
	}{
		{
			name: "path not file",
			buildFunc: func() map[string]any {
				return buildIndexFilePathNotFileResult("repo")
			},
			wantError: "Path is not a file: repo",
		},
		{
			name: "no indexed folder",
			buildFunc: func() map[string]any {
				return buildIndexFileNoIndexedFolderResult("src/main.py")
			},
			wantError: "No indexed folder found that contains src/main.py. Run index_folder on the parent directory first.",
		},
		{
			name: "security validation failure",
			buildFunc: func() map[string]any {
				return buildIndexFileSecurityValidationFailureResult("../outside.py")
			},
			wantError: "File path failed security validation: ../outside.py",
		},
		{
			name: "unsupported file type",
			buildFunc: func() map[string]any {
				return buildIndexFileUnsupportedFileTypeResult(".txt")
			},
			wantError: "Unsupported file type: .txt. File not recognized as a supported language.",
		},
		{
			name: "read failure",
			buildFunc: func() map[string]any {
				return buildIndexFileReadFailureResult(errors.New("permission denied"))
			},
			wantError: "Failed to read file: permission denied",
		},
		{
			name: "load failure",
			buildFunc: func() map[string]any {
				return buildIndexFileLoadFailureResult("local/repo")
			},
			wantError: "Failed to load index for local/repo",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := tc.buildFunc()
			if got, ok := result["success"].(bool); !ok || got {
				t.Fatalf("expected success=false, got %#v", result["success"])
			}
			if got, ok := result["error"].(string); !ok || got != tc.wantError {
				t.Fatalf("expected error %q, got %#v", tc.wantError, result["error"])
			}
		})
	}
}

func TestBuildIndexFileUnchangedSuccessResult(t *testing.T) {
	t.Parallel()

	result := buildIndexFileUnchangedSuccessResult(indexFileUnchangedSuccessResultInput{
		repoID:   "local/repo",
		relPath:  "src/main.py",
		duration: 1520 * time.Millisecond,
	})

	if got, ok := result["success"].(bool); !ok || !got {
		t.Fatalf("expected success=true, got %#v", result["success"])
	}
	if got, ok := result["message"].(string); !ok || got != "File unchanged" {
		t.Fatalf("expected unchanged message, got %#v", result["message"])
	}
	if got, ok := result["repo"].(string); !ok || got != "local/repo" {
		t.Fatalf("expected repo local/repo, got %#v", result["repo"])
	}
	if got, ok := result["file"].(string); !ok || got != "src/main.py" {
		t.Fatalf("expected file src/main.py, got %#v", result["file"])
	}
	if got, ok := result["duration_seconds"].(float64); !ok || got != 1.52 {
		t.Fatalf("expected duration_seconds=1.52, got %#v", result["duration_seconds"])
	}
	if got := result["is_new"]; got != nil {
		t.Fatalf("did not expect is_new for unchanged result, got %#v", got)
	}
	if got := result["indexed_at"]; got != nil {
		t.Fatalf("did not expect indexed_at for unchanged result, got %#v", got)
	}
}

func TestBuildIndexFileIncrementalSuccessResult(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		isNew     bool
		indexedAt string
		wantLabel string
	}{
		{
			name:      "changed file",
			isNew:     false,
			indexedAt: "2026-03-30T00:00:01Z",
			wantLabel: "changed",
		},
		{
			name:      "new file",
			isNew:     true,
			indexedAt: "2026-03-30T00:00:02Z",
			wantLabel: "new",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := buildIndexFileIncrementalSuccessResult(indexFileIncrementalSuccessResultInput{
				repoID:    "local/repo",
				relPath:   "src/main.py",
				isNew:     tc.isNew,
				indexedAt: tc.indexedAt,
				duration:  2010 * time.Millisecond,
			})

			if got, ok := result["success"].(bool); !ok || !got {
				t.Fatalf("%s case expected success=true, got %#v", tc.wantLabel, result["success"])
			}
			if got, ok := result["repo"].(string); !ok || got != "local/repo" {
				t.Fatalf("%s case expected repo local/repo, got %#v", tc.wantLabel, result["repo"])
			}
			if got, ok := result["file"].(string); !ok || got != "src/main.py" {
				t.Fatalf("%s case expected file src/main.py, got %#v", tc.wantLabel, result["file"])
			}
			if got, ok := result["is_new"].(bool); !ok || got != tc.isNew {
				t.Fatalf("%s case expected is_new=%t, got %#v", tc.wantLabel, tc.isNew, result["is_new"])
			}
			if got, ok := result["symbol_count"].(int); !ok || got != 0 {
				t.Fatalf("%s case expected symbol_count=0, got %#v", tc.wantLabel, result["symbol_count"])
			}
			if got, ok := result["indexed_at"].(string); !ok || got != tc.indexedAt {
				t.Fatalf("%s case expected indexed_at=%q, got %#v", tc.wantLabel, tc.indexedAt, result["indexed_at"])
			}
			if got, ok := result["duration_seconds"].(float64); !ok || got != 2.01 {
				t.Fatalf("%s case expected duration_seconds=2.01, got %#v", tc.wantLabel, result["duration_seconds"])
			}
		})
	}
}
