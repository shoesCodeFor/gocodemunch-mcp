package orchestration

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestBuildIndexRepoNoSourceFilesResult(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		input        indexRepoNoSourceFilesResultInput
		wantWarnings []string
	}{
		{
			name: "includes warnings when provided",
			input: indexRepoNoSourceFilesResultInput{
				repoID:       "org/repo",
				canonicalURL: "https://github.com/org/repo",
				warnings:     []string{"Failed to read .gitignore: timeout"},
			},
			wantWarnings: []string{"Failed to read .gitignore: timeout"},
		},
		{
			name: "omits warnings when empty",
			input: indexRepoNoSourceFilesResultInput{
				repoID:       "org/repo",
				canonicalURL: "https://github.com/org/repo",
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := buildIndexRepoNoSourceFilesResult(tc.input)
			if got, ok := result["success"].(bool); !ok || got {
				t.Fatalf("expected success=false, got %#v", result["success"])
			}
			if got, ok := result["repo"].(string); !ok || got != "org/repo" {
				t.Fatalf("expected repo org/repo, got %#v", result["repo"])
			}
			if got, ok := result["url"].(string); !ok || got != "https://github.com/org/repo" {
				t.Fatalf("expected canonical URL, got %#v", result["url"])
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

func TestBuildIndexRepoIncrementalSuccessResult(t *testing.T) {
	t.Parallel()

	input := indexRepoIncrementalSuccessResultInput{
		repoID:       "org/repo",
		canonicalURL: "https://github.com/org/repo",
		changedCount: 3,
		newCount:     2,
		deletedCount: 1,
		symbolCount:  9,
		indexedAt:    "2026-03-30T00:00:00Z",
		duration:     1500 * time.Millisecond,
		skipCounts: map[string]int{
			"file_limit": 0,
			"secret":     1,
		},
		warnings: []string{"Failed to read .gitignore: timeout"},
	}

	result := buildIndexRepoIncrementalSuccessResult(input)
	if got, ok := result["success"].(bool); !ok || !got {
		t.Fatalf("expected success=true, got %#v", result["success"])
	}
	if got, ok := result["incremental"].(bool); !ok || !got {
		t.Fatalf("expected incremental=true, got %#v", result["incremental"])
	}
	if got, ok := result["repo"].(string); !ok || got != "org/repo" {
		t.Fatalf("expected repo org/repo, got %#v", result["repo"])
	}
	if got, ok := result["url"].(string); !ok || got != "https://github.com/org/repo" {
		t.Fatalf("expected canonical URL, got %#v", result["url"])
	}
	if got, ok := result["changed"].(int); !ok || got != 3 {
		t.Fatalf("expected changed=3, got %#v", result["changed"])
	}
	if got, ok := result["new"].(int); !ok || got != 2 {
		t.Fatalf("expected new=2, got %#v", result["new"])
	}
	if got, ok := result["deleted"].(int); !ok || got != 1 {
		t.Fatalf("expected deleted=1, got %#v", result["deleted"])
	}
	if got, ok := result["symbol_count"].(int); !ok || got != 9 {
		t.Fatalf("expected symbol_count=9, got %#v", result["symbol_count"])
	}
	if got, ok := result["indexed_at"].(string); !ok || got != "2026-03-30T00:00:00Z" {
		t.Fatalf("expected indexed_at, got %#v", result["indexed_at"])
	}
	if got, ok := result["duration_seconds"].(float64); !ok || got != 1.5 {
		t.Fatalf("expected duration_seconds=1.5, got %#v", result["duration_seconds"])
	}
	if got, ok := result["discovery_skip_counts"].(map[string]int); !ok || !reflect.DeepEqual(got, input.skipCounts) {
		t.Fatalf("expected discovery_skip_counts %#v, got %#v", input.skipCounts, result["discovery_skip_counts"])
	}
	if got, ok := result["no_symbols_count"].(int); !ok || got != 0 {
		t.Fatalf("expected no_symbols_count=0, got %#v", result["no_symbols_count"])
	}
	if got, ok := result["no_symbols_files"].([]string); !ok || len(got) != 0 {
		t.Fatalf("expected no_symbols_files=[], got %#v", result["no_symbols_files"])
	}
	if got, ok := result["warnings"].([]string); !ok || !reflect.DeepEqual(got, input.warnings) {
		t.Fatalf("expected warnings %#v, got %#v", input.warnings, result["warnings"])
	}
}

func TestBuildIndexRepoFullSuccessResult(t *testing.T) {
	t.Parallel()

	allFiles := make([]string, 0, 25)
	for index := 1; index <= 25; index++ {
		allFiles = append(allFiles, fmt.Sprintf("src/file_%02d.py", index))
	}

	testCases := []struct {
		name              string
		input             indexRepoFullSuccessResultInput
		wantSampleLen     int
		wantDiscovered    int
		wantIndexed       int
		wantSkippedCap    int
		wantWarnings      []string
		expectCapMetadata bool
	}{
		{
			name: "without file cap omits cap metadata and warnings",
			input: indexRepoFullSuccessResultInput{
				repoID:       "org/repo",
				canonicalURL: "https://github.com/org/repo",
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
				maxIndexFiles: 5,
			},
			wantSampleLen:     3,
			wantWarnings:      nil,
			expectCapMetadata: false,
		},
		{
			name: "file cap appends warning and cap metadata",
			input: indexRepoFullSuccessResultInput{
				repoID:       "org/repo",
				canonicalURL: "https://github.com/org/repo",
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
				warnings:      []string{"Failed to read .gitignore: timeout"},
				maxIndexFiles: 5,
			},
			wantSampleLen:  20,
			wantDiscovered: 7,
			wantIndexed:    5,
			wantSkippedCap: 2,
			wantWarnings: []string{
				"Failed to read .gitignore: timeout",
				"File cap reached: 7 files discovered, 5 indexed, 2 dropped. Raise JCODEMUNCH_MAX_INDEX_FILES or narrow the path.",
			},
			expectCapMetadata: true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := buildIndexRepoFullSuccessResult(tc.input)
			if got, ok := result["success"].(bool); !ok || !got {
				t.Fatalf("expected success=true, got %#v", result["success"])
			}
			if got, ok := result["repo"].(string); !ok || got != "org/repo" {
				t.Fatalf("expected repo org/repo, got %#v", result["repo"])
			}
			if got, ok := result["url"].(string); !ok || got != "https://github.com/org/repo" {
				t.Fatalf("expected canonical URL, got %#v", result["url"])
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
