package testsgo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIndexRepoErrorEnvelopesWhenRemoteAcquirerDisabled(t *testing.T) {
	t.Setenv("CODE_INDEX_PATH", t.TempDir())
	t.Setenv("GOCODEMUNCH_ENABLE_REMOTE_INDEX_REPO", "0")

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_repo", map[string]any{
			"url":              "https://example.com/org/repo",
			"incremental":      true,
			"use_ai_summaries": false,
		}),
		toolCallRequest(3, "index_repo", map[string]any{
			"url":              "https://github.com/org/repo",
			"incremental":      true,
			"use_ai_summaries": false,
		}),
	})

	unsupportedHost := toolPayload(t, responses[1])
	if got, _ := unsupportedHost["success"].(bool); got {
		t.Fatalf("expected success=false for unsupported host envelope: %#v", unsupportedHost)
	}
	if got := stringField(unsupportedHost, "error"); !strings.Contains(got, "Unsupported host") {
		t.Fatalf("expected unsupported host validation error, got %#v", unsupportedHost)
	}
	if got := stringField(unsupportedHost, "error"); strings.Contains(got, "Internal error processing index_repo") {
		t.Fatalf("expected explicit tool-level error, got generic internal error envelope: %#v", unsupportedHost)
	}

	placeholder := toolPayload(t, responses[2])
	if got, _ := placeholder["success"].(bool); got {
		t.Fatalf("expected success=false for placeholder github envelope: %#v", placeholder)
	}
	if got := stringField(placeholder, "repo"); got != "org/repo" {
		t.Fatalf("expected parsed repo id in placeholder envelope, got %#v", placeholder)
	}
	if got := stringField(placeholder, "error"); !strings.Contains(got, "not implemented yet") {
		t.Fatalf("expected not-implemented placeholder for github host, got %#v", placeholder)
	}
}

func TestIndexRepoUsesDefaultGitHubAcquirerWiring(t *testing.T) {
	t.Setenv("CODE_INDEX_PATH", t.TempDir())
	t.Setenv("GOCODEMUNCH_ENABLE_REMOTE_INDEX_REPO", "1")

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/git/trees/HEAD":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tree": []map[string]any{
					{"path": "src/main.go", "type": "blob"},
					{"path": "pkg/util.py", "type": "blob"},
					{"path": "README.md", "type": "blob"},
				},
			})
			return
		case "/repos/org/repo/contents/src/main.go":
			_, _ = w.Write([]byte("package main\n"))
			return
		case "/repos/org/repo/contents/pkg/util.py":
			_, _ = w.Write([]byte("def util():\n    return 1\n"))
			return
		case "/repos/org/repo/contents/.gitignore":
			_, _ = w.Write([]byte("pkg/\n"))
			return
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer apiServer.Close()

	t.Setenv("GOCODEMUNCH_GITHUB_API_BASE_URL", apiServer.URL)

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_repo", map[string]any{
			"url":              "https://github.com/org/repo",
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})

	indexed := toolPayload(t, responses[1])
	if got, _ := indexed["success"].(bool); !got {
		t.Fatalf("expected index_repo success with wired acquirer, got %#v", indexed)
	}
	if got := stringField(indexed, "repo"); got != "org/repo" {
		t.Fatalf("expected repo id org/repo, got %#v", indexed)
	}
	if got := intField(indexed, "file_count"); got != 1 {
		t.Fatalf("expected .gitignore to filter pkg/util.py, got %#v", indexed)
	}
	if got := stringField(indexed, "error"); got != "" {
		t.Fatalf("did not expect error field on successful index_repo response, got %#v", indexed)
	}
}
