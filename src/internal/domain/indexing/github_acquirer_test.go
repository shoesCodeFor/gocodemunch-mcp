package indexing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGitHubRepoAcquirerAcquireTreeFetchesSupportedFiles(t *testing.T) {
	var readmeContentRequests atomic.Int32

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/git/trees/HEAD":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sha": "tree-sha-123",
				"tree": []map[string]any{
					{"path": "src/main.go", "type": "blob", "sha": "blob-src", "size": 13},
					{"path": "pkg/util.py", "type": "blob", "sha": "blob-util", "size": 24},
					{"path": "README.md", "type": "blob", "sha": "blob-readme", "size": 21},
					{"path": "node_modules/library/index.js", "type": "blob", "sha": "blob-dep", "size": 12},
				},
			})
			return
		case "/repos/org/repo/contents/src/main.go":
			_, _ = w.Write([]byte("package main\n"))
			return
		case "/repos/org/repo/contents/pkg/util.py":
			_, _ = w.Write([]byte("def util():\n    return 1\n"))
			return
		case "/repos/org/repo/contents/README.md":
			readmeContentRequests.Add(1)
			http.Error(w, "unexpected README content request", http.StatusInternalServerError)
			return
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer apiServer.Close()

	acquirer := NewGitHubRepoAcquirer(
		WithGitHubAPIBaseURL(apiServer.URL),
		WithGitHubHTTPClient(apiServer.Client()),
		WithGitHubFetchConcurrency(2),
	)

	tree, err := acquirer.AcquireTree(context.Background(), "https://github.com/org/repo")
	if err != nil {
		t.Fatalf("acquire tree: %v", err)
	}

	if got := len(tree); got != 2 {
		t.Fatalf("expected 2 fetched source files, got %d (%#v)", got, tree)
	}
	if _, ok := tree["src/main.go"]; !ok {
		t.Fatalf("expected src/main.go in fetched tree: %#v", tree)
	}
	if _, ok := tree["pkg/util.py"]; !ok {
		t.Fatalf("expected pkg/util.py in fetched tree: %#v", tree)
	}
	if _, ok := tree["README.md"]; ok {
		t.Fatalf("did not expect README.md to be fetched: %#v", tree)
	}
	if got := readmeContentRequests.Load(); got != 0 {
		t.Fatalf("expected README.md content endpoint to remain unfetched, got %d request(s)", got)
	}
}

func TestGitHubRepoAcquirerAcquireTreeSubsetSkipsTreeAPIAndUnsupportedPaths(t *testing.T) {
	var (
		treeRequests        atomic.Int32
		mainContentRequests atomic.Int32
	)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/git/trees/HEAD":
			treeRequests.Add(1)
			http.Error(w, "unexpected tree request", http.StatusInternalServerError)
			return
		case "/repos/org/repo/contents/src/main.go":
			mainContentRequests.Add(1)
			_, _ = w.Write([]byte("package main\n"))
			return
		case "/repos/org/repo/contents/pkg/util.py":
			_, _ = w.Write([]byte("def util():\n    return 1\n"))
			return
		case "/repos/org/repo/contents/README.md":
			http.Error(w, "unexpected README content request", http.StatusInternalServerError)
			return
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer apiServer.Close()

	acquirer := NewGitHubRepoAcquirer(
		WithGitHubAPIBaseURL(apiServer.URL),
		WithGitHubHTTPClient(apiServer.Client()),
		WithGitHubFetchConcurrency(2),
	)

	tree, err := acquirer.AcquireTreeSubset(
		context.Background(),
		"https://github.com/org/repo",
		[]string{
			"pkg/util.py",
			"README.md", // unsupported extension; should be filtered.
			"pkg/util.py",
		},
	)
	if err != nil {
		t.Fatalf("acquire subset tree: %v", err)
	}

	if got := len(tree); got != 1 {
		t.Fatalf("expected one fetched subset file, got %d (%#v)", got, tree)
	}
	if _, ok := tree["pkg/util.py"]; !ok {
		t.Fatalf("expected pkg/util.py in fetched subset tree: %#v", tree)
	}
	if _, ok := tree["README.md"]; ok {
		t.Fatalf("did not expect README.md in fetched subset tree: %#v", tree)
	}
	if got := treeRequests.Load(); got != 0 {
		t.Fatalf("expected no tree API requests during subset fetch, got %d", got)
	}
	if got := mainContentRequests.Load(); got != 0 {
		t.Fatalf("expected no src/main.go requests when omitted from subset paths, got %d", got)
	}
}

func TestGitHubRepoAcquirerAcquireTreeMetadata(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/git/trees/HEAD":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sha": "TREE-SHA-ABC",
				"tree": []map[string]any{
					{"path": "src/main.go", "type": "blob", "sha": "BLOB-MAIN", "size": 14},
					{"path": "pkg/util.py", "type": "blob", "sha": "BLOB-UTIL", "size": 27},
					{"path": "README.md", "type": "blob", "sha": "BLOB-README", "size": 17},
				},
			})
			return
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer apiServer.Close()

	acquirer := NewGitHubRepoAcquirer(
		WithGitHubAPIBaseURL(apiServer.URL),
		WithGitHubHTTPClient(apiServer.Client()),
	)

	metadata, err := acquirer.AcquireTreeMetadata(context.Background(), "https://github.com/org/repo")
	if err != nil {
		t.Fatalf("acquire tree metadata: %v", err)
	}
	if metadata.TreeSHA != "tree-sha-abc" {
		t.Fatalf("expected normalized tree sha, got %q", metadata.TreeSHA)
	}
	if got := len(metadata.Files); got != 2 {
		t.Fatalf("expected 2 supported files in metadata, got %d (%#v)", got, metadata.Files)
	}
	if got := metadata.Files["src/main.go"].BlobSHA; got != "blob-main" {
		t.Fatalf("expected src/main.go blob sha in metadata, got %q", got)
	}
	if got := metadata.Files["pkg/util.py"].SizeBytes; got != 27 {
		t.Fatalf("expected pkg/util.py size in metadata, got %d", got)
	}
	if _, ok := metadata.Files["README.md"]; ok {
		t.Fatalf("did not expect README.md in supported metadata: %#v", metadata.Files)
	}
}

func TestGitHubRepoAcquirerReadGitignore(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/contents/.gitignore":
			_, _ = w.Write([]byte("pkg/\n*.md\n"))
			return
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer apiServer.Close()

	acquirer := NewGitHubRepoAcquirer(
		WithGitHubAPIBaseURL(apiServer.URL),
		WithGitHubHTTPClient(apiServer.Client()),
	)

	content, err := acquirer.ReadGitignore(context.Background(), "https://github.com/org/repo")
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if got := string(content); got != "pkg/\n*.md\n" {
		t.Fatalf("unexpected .gitignore content %q", got)
	}
}

func TestGitHubRepoAcquirerFromEnvDisableToggle(t *testing.T) {
	t.Setenv("GOCODEMUNCH_ENABLE_REMOTE_INDEX_REPO", "0")
	if got := NewGitHubRepoAcquirerFromEnv(); got != nil {
		t.Fatalf("expected nil acquirer when remote index_repo is disabled, got %#v", got)
	}
}
