package orchestration

import (
	"reflect"
	"testing"
	"time"
)

func TestBuildIndexRepoNoChangeResult(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		input        indexRepoNoChangeResultInput
		wantGitHead  string
		wantWarnings []string
		wantMessage  string
		wantDuration float64
	}{
		{
			name: "tree sha fast path includes git head and warnings",
			input: indexRepoNoChangeResultInput{
				message:        "No changes detected (tree SHA unchanged)",
				repoID:         "org/repo",
				canonicalURL:   "https://github.com/org/repo",
				gitHead:        "tree-sha-123",
				includeGitHead: true,
				warnings:       []string{"Failed to read .gitignore: timeout"},
				duration:       1450 * time.Millisecond,
			},
			wantGitHead:  "tree-sha-123",
			wantWarnings: []string{"Failed to read .gitignore: timeout"},
			wantMessage:  "No changes detected (tree SHA unchanged)",
			wantDuration: 1.45,
		},
		{
			name: "generic no-change omits optional fields when disabled",
			input: indexRepoNoChangeResultInput{
				message:        "No changes detected",
				repoID:         "org/repo",
				canonicalURL:   "https://github.com/org/repo",
				gitHead:        "tree-sha-123",
				includeGitHead: false,
				duration:       2510 * time.Millisecond,
			},
			wantMessage:  "No changes detected",
			wantDuration: 2.51,
		},
		{
			name: "git head flag requires non-empty head value",
			input: indexRepoNoChangeResultInput{
				message:        "No changes detected",
				repoID:         "org/repo",
				canonicalURL:   "https://github.com/org/repo",
				gitHead:        "",
				includeGitHead: true,
				duration:       50 * time.Millisecond,
			},
			wantMessage:  "No changes detected",
			wantDuration: 0.05,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := buildIndexRepoNoChangeResult(tc.input)
			if got, ok := result["success"].(bool); !ok || !got {
				t.Fatalf("expected success=true, got %#v", result["success"])
			}
			if got, ok := result["message"].(string); !ok || got != tc.wantMessage {
				t.Fatalf("expected message %q, got %#v", tc.wantMessage, result["message"])
			}
			if got, ok := result["repo"].(string); !ok || got != "org/repo" {
				t.Fatalf("expected repo org/repo, got %#v", result["repo"])
			}
			if got, ok := result["url"].(string); !ok || got != "https://github.com/org/repo" {
				t.Fatalf("expected canonical URL, got %#v", result["url"])
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

			gotGitHead, hasGitHead := result["git_head"]
			if tc.wantGitHead == "" {
				if hasGitHead {
					t.Fatalf("did not expect git_head in result, got %#v", gotGitHead)
				}
			} else if got, ok := gotGitHead.(string); !ok || got != tc.wantGitHead {
				t.Fatalf("expected git_head=%q, got %#v", tc.wantGitHead, gotGitHead)
			}

			gotWarnings, hasWarnings := result["warnings"]
			if len(tc.wantWarnings) == 0 {
				if hasWarnings {
					t.Fatalf("did not expect warnings in result, got %#v", gotWarnings)
				}
			} else {
				typed, ok := gotWarnings.([]string)
				if !ok {
					t.Fatalf("expected []string warnings, got %#v", gotWarnings)
				}
				if !reflect.DeepEqual(typed, tc.wantWarnings) {
					t.Fatalf("expected warnings %#v, got %#v", tc.wantWarnings, typed)
				}
			}
		})
	}
}
