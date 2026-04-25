package orchestration

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestBuildIndexFolderNoSourceFilesResult(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		input        indexFolderNoSourceFilesResultInput
		wantWarnings []string
	}{
		{
			name: "includes warnings when provided",
			input: indexFolderNoSourceFilesResultInput{
				warnings: []string{"Relative path '.' resolved to '/tmp/workdir'"},
			},
			wantWarnings: []string{"Relative path '.' resolved to '/tmp/workdir'"},
		},
		{
			name:         "omits warnings when empty",
			input:        indexFolderNoSourceFilesResultInput{},
			wantWarnings: nil,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := buildIndexFolderNoSourceFilesResult(tc.input)
			if got, ok := result["success"].(bool); !ok || got {
				t.Fatalf("expected success=false, got %#v", result["success"])
			}
			if got, ok := result["error"].(string); !ok || got != "No source files found" {
				t.Fatalf("expected no-source-files error, got %#v", result["error"])
			}

			gotWarnings, hasWarnings := result["warnings"]
			if len(tc.wantWarnings) == 0 {
				if hasWarnings {
					t.Fatalf("did not expect warnings, got %#v", gotWarnings)
				}
				return
			}

			typed, ok := gotWarnings.([]string)
			if !ok {
				t.Fatalf("expected warnings []string, got %#v", gotWarnings)
			}
			if !reflect.DeepEqual(typed, tc.wantWarnings) {
				t.Fatalf("expected warnings %#v, got %#v", tc.wantWarnings, typed)
			}
		})
	}
}

func TestBuildIndexFolderNoChangeResult(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		input        indexFolderNoChangeResultInput
		wantWarnings []string
		wantDuration float64
	}{
		{
			name: "includes warnings when provided",
			input: indexFolderNoChangeResultInput{
				repoID:       "local:/workspace/project",
				resolvedPath: "/workspace/project",
				warnings:     []string{"Failed to stat /workspace/project/a.py: permission denied"},
				duration:     1450 * time.Millisecond,
			},
			wantWarnings: []string{"Failed to stat /workspace/project/a.py: permission denied"},
			wantDuration: 1.45,
		},
		{
			name: "omits warnings when empty",
			input: indexFolderNoChangeResultInput{
				repoID:       "local:/workspace/project",
				resolvedPath: "/workspace/project",
				duration:     2510 * time.Millisecond,
			},
			wantDuration: 2.51,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := buildIndexFolderNoChangeResult(tc.input)
			if got, ok := result["success"].(bool); !ok || !got {
				t.Fatalf("expected success=true, got %#v", result["success"])
			}
			if got, ok := result["message"].(string); !ok || got != "No changes detected" {
				t.Fatalf("expected message No changes detected, got %#v", result["message"])
			}
			if got, ok := result["repo"].(string); !ok || got != "local:/workspace/project" {
				t.Fatalf("expected repo local:/workspace/project, got %#v", result["repo"])
			}
			if got, ok := result["folder_path"].(string); !ok || got != "/workspace/project" {
				t.Fatalf("expected folder_path /workspace/project, got %#v", result["folder_path"])
			}
			if got, ok := result["changed"].(int); !ok || got != 0 {
				t.Fatalf("expected changed=0, got %#v", result["changed"])
			}
			if got, ok := result["new"].(int); !ok || got != 0 {
				t.Fatalf("expected new=0, got %#v", result["new"])
			}
			if got, ok := result["deleted"].(int); !ok || got != 0 {
				t.Fatalf("expected deleted=0, got %#v", result["deleted"])
			}
			if got, ok := result["duration_seconds"].(float64); !ok || got != tc.wantDuration {
				t.Fatalf("expected duration_seconds=%v, got %#v", tc.wantDuration, result["duration_seconds"])
			}

			gotWarnings, hasWarnings := result["warnings"]
			if len(tc.wantWarnings) == 0 {
				if hasWarnings {
					t.Fatalf("did not expect warnings in result, got %#v", gotWarnings)
				}
				return
			}

			typed, ok := gotWarnings.([]string)
			if !ok {
				t.Fatalf("expected []string warnings, got %#v", gotWarnings)
			}
			if !reflect.DeepEqual(typed, tc.wantWarnings) {
				t.Fatalf("expected warnings %#v, got %#v", tc.wantWarnings, typed)
			}
		})
	}
}

func TestBuildIndexFolderIncrementalSuccessResult(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name              string
		input             indexFolderIncrementalSuccessResultInput
		expectSkipCounts  bool
		wantWarningValues []string
	}{
		{
			name: "includes skip counts when enabled",
			input: indexFolderIncrementalSuccessResultInput{
				repoID:            "local:/workspace/project",
				resolvedPath:      "/workspace/project",
				changedCount:      3,
				newCount:          2,
				deletedCount:      1,
				symbolCount:       7,
				indexedAt:         "2026-03-30T00:00:00Z",
				duration:          1500 * time.Millisecond,
				includeSkipCounts: true,
				skipCounts: map[string]int{
					"file_limit": 1,
				},
				warnings: []string{"Relative path '.' resolved to '/workspace/project'"},
			},
			expectSkipCounts:  true,
			wantWarningValues: []string{"Relative path '.' resolved to '/workspace/project'"},
		},
		{
			name: "omits skip counts when disabled",
			input: indexFolderIncrementalSuccessResultInput{
				repoID:            "local:/workspace/project",
				resolvedPath:      "/workspace/project",
				changedCount:      1,
				newCount:          0,
				deletedCount:      0,
				symbolCount:       7,
				indexedAt:         "2026-03-30T00:00:00Z",
				duration:          200 * time.Millisecond,
				includeSkipCounts: false,
				skipCounts: map[string]int{
					"file_limit": 9,
				},
			},
			expectSkipCounts: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := buildIndexFolderIncrementalSuccessResult(tc.input)
			if got, ok := result["success"].(bool); !ok || !got {
				t.Fatalf("expected success=true, got %#v", result["success"])
			}
			if got, ok := result["incremental"].(bool); !ok || !got {
				t.Fatalf("expected incremental=true, got %#v", result["incremental"])
			}
			if got, ok := result["repo"].(string); !ok || got != "local:/workspace/project" {
				t.Fatalf("expected repo local:/workspace/project, got %#v", result["repo"])
			}
			if got, ok := result["folder_path"].(string); !ok || got != "/workspace/project" {
				t.Fatalf("expected folder_path /workspace/project, got %#v", result["folder_path"])
			}
			if got, ok := result["changed"].(int); !ok || got != tc.input.changedCount {
				t.Fatalf("expected changed=%d, got %#v", tc.input.changedCount, result["changed"])
			}
			if got, ok := result["new"].(int); !ok || got != tc.input.newCount {
				t.Fatalf("expected new=%d, got %#v", tc.input.newCount, result["new"])
			}
			if got, ok := result["deleted"].(int); !ok || got != tc.input.deletedCount {
				t.Fatalf("expected deleted=%d, got %#v", tc.input.deletedCount, result["deleted"])
			}
			if got, ok := result["symbol_count"].(int); !ok || got != tc.input.symbolCount {
				t.Fatalf("expected symbol_count=%d, got %#v", tc.input.symbolCount, result["symbol_count"])
			}
			if got, ok := result["indexed_at"].(string); !ok || got != tc.input.indexedAt {
				t.Fatalf("expected indexed_at %q, got %#v", tc.input.indexedAt, result["indexed_at"])
			}
			if got, ok := result["duration_seconds"].(float64); !ok || got != roundSeconds(tc.input.duration) {
				t.Fatalf("expected duration_seconds=%v, got %#v", roundSeconds(tc.input.duration), result["duration_seconds"])
			}

			if tc.expectSkipCounts {
				if got, ok := result["discovery_skip_counts"].(map[string]int); !ok || !reflect.DeepEqual(got, tc.input.skipCounts) {
					t.Fatalf("expected discovery_skip_counts %#v, got %#v", tc.input.skipCounts, result["discovery_skip_counts"])
				}
			} else if got := result["discovery_skip_counts"]; got != nil {
				t.Fatalf("did not expect discovery_skip_counts, got %#v", got)
			}

			gotWarnings, hasWarnings := result["warnings"]
			if len(tc.wantWarningValues) == 0 {
				if hasWarnings {
					t.Fatalf("did not expect warnings, got %#v", gotWarnings)
				}
				return
			}

			typed, ok := gotWarnings.([]string)
			if !ok {
				t.Fatalf("expected warnings []string, got %#v", gotWarnings)
			}
			if !reflect.DeepEqual(typed, tc.wantWarningValues) {
				t.Fatalf("expected warnings %#v, got %#v", tc.wantWarningValues, typed)
			}
		})
	}
}

func TestBuildIndexFolderFullSuccessResult(t *testing.T) {
	t.Parallel()

	allFiles := make([]string, 0, 25)
	for index := 1; index <= 25; index++ {
		allFiles = append(allFiles, fmt.Sprintf("src/file_%02d.py", index))
	}

	testCases := []struct {
		name              string
		input             indexFolderFullSuccessResultInput
		wantSampleLen     int
		wantDiscovered    int
		wantIndexed       int
		wantSkippedCap    int
		wantWarnings      []string
		expectCapMetadata bool
	}{
		{
			name: "without file cap omits cap metadata and warnings",
			input: indexFolderFullSuccessResultInput{
				repoID:       "local:/workspace/project",
				resolvedPath: "/workspace/project",
				indexedAt:    "2026-03-30T00:00:00Z",
				fileCount:    3,
				languageCounts: map[string]int{
					"python": 3,
				},
				sourceFiles: allFiles[:3],
				duration:    2500 * time.Millisecond,
				skipCounts: map[string]int{
					"file_limit": 0,
				},
				maxFolderFiles: 5,
			},
			wantSampleLen:     3,
			wantWarnings:      nil,
			expectCapMetadata: false,
		},
		{
			name: "file cap appends warning and cap metadata",
			input: indexFolderFullSuccessResultInput{
				repoID:       "local:/workspace/project",
				resolvedPath: "/workspace/project",
				indexedAt:    "2026-03-30T00:00:00Z",
				fileCount:    25,
				languageCounts: map[string]int{
					"python": 25,
				},
				sourceFiles: allFiles,
				duration:    3100 * time.Millisecond,
				skipCounts: map[string]int{
					"file_limit": 2,
				},
				warnings:       []string{"Relative path '.' resolved to '/workspace/project'"},
				maxFolderFiles: 5,
			},
			wantSampleLen:  20,
			wantDiscovered: 7,
			wantIndexed:    5,
			wantSkippedCap: 2,
			wantWarnings: []string{
				"Relative path '.' resolved to '/workspace/project'",
				"File cap reached: 7 files discovered, 5 indexed, 2 dropped. Raise JCODEMUNCH_MAX_FOLDER_FILES or narrow the path.",
			},
			expectCapMetadata: true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := buildIndexFolderFullSuccessResult(tc.input)
			if got, ok := result["success"].(bool); !ok || !got {
				t.Fatalf("expected success=true, got %#v", result["success"])
			}
			if got, ok := result["repo"].(string); !ok || got != "local:/workspace/project" {
				t.Fatalf("expected repo local:/workspace/project, got %#v", result["repo"])
			}
			if got, ok := result["folder_path"].(string); !ok || got != "/workspace/project" {
				t.Fatalf("expected folder_path /workspace/project, got %#v", result["folder_path"])
			}
			if got, ok := result["indexed_at"].(string); !ok || got != "2026-03-30T00:00:00Z" {
				t.Fatalf("expected indexed_at, got %#v", result["indexed_at"])
			}
			if got, ok := result["duration_seconds"].(float64); !ok {
				t.Fatalf("expected duration_seconds float64, got %#v", result["duration_seconds"])
			} else if got != roundSeconds(tc.input.duration) {
				t.Fatalf("expected duration_seconds=%v, got %#v", roundSeconds(tc.input.duration), result["duration_seconds"])
			}

			files, ok := result["files"].([]string)
			if !ok {
				t.Fatalf("expected files []string, got %#v", result["files"])
			}
			if len(files) != tc.wantSampleLen {
				t.Fatalf("expected sampled files len=%d, got %#v", tc.wantSampleLen, files)
			}

			if tc.expectCapMetadata {
				if got, ok := result["files_discovered"].(int); !ok || got != tc.wantDiscovered {
					t.Fatalf("expected files_discovered=%d, got %#v", tc.wantDiscovered, result["files_discovered"])
				}
				if got, ok := result["files_indexed"].(int); !ok || got != tc.wantIndexed {
					t.Fatalf("expected files_indexed=%d, got %#v", tc.wantIndexed, result["files_indexed"])
				}
				if got, ok := result["files_skipped_cap"].(int); !ok || got != tc.wantSkippedCap {
					t.Fatalf("expected files_skipped_cap=%d, got %#v", tc.wantSkippedCap, result["files_skipped_cap"])
				}
			} else {
				if got := result["files_discovered"]; got != nil {
					t.Fatalf("did not expect files_discovered, got %#v", got)
				}
				if got := result["files_indexed"]; got != nil {
					t.Fatalf("did not expect files_indexed, got %#v", got)
				}
				if got := result["files_skipped_cap"]; got != nil {
					t.Fatalf("did not expect files_skipped_cap, got %#v", got)
				}
			}

			gotWarnings, hasWarnings := result["warnings"]
			if len(tc.wantWarnings) == 0 {
				if hasWarnings {
					t.Fatalf("did not expect warnings, got %#v", gotWarnings)
				}
			} else {
				typed, ok := gotWarnings.([]string)
				if !ok {
					t.Fatalf("expected warnings []string, got %#v", gotWarnings)
				}
				if !reflect.DeepEqual(typed, tc.wantWarnings) {
					t.Fatalf("expected warnings %#v, got %#v", tc.wantWarnings, typed)
				}
			}
		})
	}
}
