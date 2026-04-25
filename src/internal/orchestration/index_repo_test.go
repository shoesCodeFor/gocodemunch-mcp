package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
)

type staticRepoAcquirer struct {
	tree                   map[string][]byte
	metadata               indexing.RepoTreeMetadata
	gitignore              []byte
	err                    error
	metadataErr            error
	acquireTreeCalls       int
	acquireMetadataCalls   int
	acquireTreeSubsetCalls int
	acquiredSubsetPaths    []string
}

func (a *staticRepoAcquirer) AcquireTree(_ context.Context, _ string) (map[string][]byte, error) {
	a.acquireTreeCalls++
	if a.err != nil {
		return nil, a.err
	}
	clone := make(map[string][]byte, len(a.tree))
	for path, content := range a.tree {
		clonedContent := make([]byte, len(content))
		copy(clonedContent, content)
		clone[path] = clonedContent
	}
	return clone, nil
}

func (a *staticRepoAcquirer) AcquireTreeMetadata(_ context.Context, _ string) (indexing.RepoTreeMetadata, error) {
	a.acquireMetadataCalls++
	if a.metadataErr != nil {
		return indexing.RepoTreeMetadata{}, a.metadataErr
	}
	if len(a.metadata.Files) > 0 || strings.TrimSpace(a.metadata.TreeSHA) != "" {
		return cloneRepoTreeMetadata(a.metadata), nil
	}

	files := make(map[string]indexing.RemoteFileMetadata, len(a.tree))
	fileBlobSHAs := map[string]string{}
	for relPath, content := range a.tree {
		if _, ok := classifyLanguage(relPath); !ok {
			continue
		}
		if shouldSkipRemotePath(relPath) {
			continue
		}
		sum := sha256.Sum256(content)
		blobSHA := hex.EncodeToString(sum[:])
		files[relPath] = indexing.RemoteFileMetadata{
			BlobSHA:   blobSHA,
			SizeBytes: int64(len(content)),
		}
		fileBlobSHAs[relPath] = blobSHA
	}

	return indexing.RepoTreeMetadata{
		TreeSHA: remoteTreeSHA(fileBlobSHAs),
		Files:   files,
	}, nil
}

func (a *staticRepoAcquirer) AcquireTreeSubset(
	_ context.Context,
	_ string,
	relPaths []string,
) (map[string][]byte, error) {
	a.acquireTreeSubsetCalls++
	if a.err != nil {
		return nil, a.err
	}

	unique := map[string]struct{}{}
	for _, relPath := range relPaths {
		trimmed := strings.TrimSpace(relPath)
		if trimmed == "" {
			continue
		}
		unique[trimmed] = struct{}{}
	}
	recorded := make([]string, 0, len(unique))
	for relPath := range unique {
		recorded = append(recorded, relPath)
	}
	sort.Strings(recorded)
	a.acquiredSubsetPaths = append([]string(nil), recorded...)

	clone := make(map[string][]byte, len(recorded))
	for _, relPath := range recorded {
		content, ok := a.tree[relPath]
		if !ok {
			continue
		}
		clonedContent := make([]byte, len(content))
		copy(clonedContent, content)
		clone[relPath] = clonedContent
	}
	return clone, nil
}

func (a *staticRepoAcquirer) ReadGitignore(_ context.Context, _ string) ([]byte, error) {
	if len(a.gitignore) == 0 {
		return nil, nil
	}
	cloned := make([]byte, len(a.gitignore))
	copy(cloned, a.gitignore)
	return cloned, nil
}

func cloneRepoTreeMetadata(metadata indexing.RepoTreeMetadata) indexing.RepoTreeMetadata {
	clonedFiles := make(map[string]indexing.RemoteFileMetadata, len(metadata.Files))
	for relPath, file := range metadata.Files {
		clonedFiles[relPath] = file
	}
	return indexing.RepoTreeMetadata{
		TreeSHA: metadata.TreeSHA,
		Files:   clonedFiles,
	}
}

func TestParseGitHubRepoURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		raw       string
		wantOwner string
		wantRepo  string
		wantError string
	}{
		{
			name:      "full github url",
			raw:       "https://github.com/owner/repo",
			wantOwner: "owner",
			wantRepo:  "repo",
		},
		{
			name:      "github url with git suffix",
			raw:       "https://github.com/owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
		},
		{
			name:      "owner repo shorthand",
			raw:       "owner/repo-name.with_dots",
			wantOwner: "owner",
			wantRepo:  "repo-name.with_dots",
		},
		{
			name:      "owner repo shorthand with extra path segments",
			raw:       "owner/repo/extra/path",
			wantOwner: "owner",
			wantRepo:  "repo",
		},
		{
			name:      "unsupported host rejected",
			raw:       "https://example.com/owner/repo",
			wantError: "Unsupported host",
		},
		{
			name:      "invalid owner repo format rejected",
			raw:       "owner/repo;rm -rf /",
			wantError: "Invalid owner/repo format",
		},
		{
			name:      "missing path rejected",
			raw:       "https://github.com/owner",
			wantError: "Could not parse GitHub URL",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			owner, repo, err := parseGitHubRepoURL(testCase.raw)
			if testCase.wantError != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", testCase.wantError)
				}
				if !strings.Contains(err.Error(), testCase.wantError) {
					t.Fatalf("expected error containing %q, got %q", testCase.wantError, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGitHubRepoURL(%q) returned error: %v", testCase.raw, err)
			}
			if owner != testCase.wantOwner || repo != testCase.wantRepo {
				t.Fatalf(
					"parseGitHubRepoURL(%q) = (%q, %q), want (%q, %q)",
					testCase.raw,
					owner,
					repo,
					testCase.wantOwner,
					testCase.wantRepo,
				)
			}
		})
	}
}

func TestIndexRepoHandlerReturnsExplicitToolErrorEnvelope(t *testing.T) {
	t.Parallel()

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{})

	unsupportedHost := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url": "https://example.com/org/repo",
	})
	if got, _ := unsupportedHost["success"].(bool); got {
		t.Fatalf("expected success=false for unsupported host envelope: %#v", unsupportedHost)
	}
	if got := stringArg(unsupportedHost, "error", ""); !strings.Contains(got, "Unsupported host") {
		t.Fatalf("expected unsupported-host error envelope, got %#v", unsupportedHost)
	}

	githubAccepted := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url": "https://github.com/org/repo",
	})
	if got, _ := githubAccepted["success"].(bool); got {
		t.Fatalf("expected success=false for current migration placeholder envelope: %#v", githubAccepted)
	}
	if got := stringArg(githubAccepted, "repo", ""); got != "org/repo" {
		t.Fatalf("expected parsed repo id in placeholder response, got %#v", githubAccepted)
	}
	if got := stringArg(githubAccepted, "error", ""); !strings.Contains(got, "not implemented yet") {
		t.Fatalf("expected not-implemented placeholder envelope, got %#v", githubAccepted)
	}
}

func TestIndexRepoHandlerIndexesRemoteTreeIncrementally(t *testing.T) {
	store := mustIndexStore(t)
	acquirer := &staticRepoAcquirer{
		tree: map[string][]byte{
			"src/main.go": []byte("package main\n"),
			"pkg/util.py": []byte("def util():\n    return 1\n"),
			"README.md":   []byte("# ignored extension\n"),
		},
	}
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:   store,
		RepoAcquirer: acquirer,
	})

	first := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": true,
	})
	if got, _ := first["success"].(bool); !got {
		t.Fatalf("expected first index_repo call to succeed, got %#v", first)
	}
	if got := stringArg(first, "repo", ""); got != "org/repo" {
		t.Fatalf("expected repo id org/repo, got %#v", first)
	}
	if got := intArg(first, "file_count"); got != 2 {
		t.Fatalf("expected file_count=2 after language filtering, got %#v", first)
	}

	noChanges := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": true,
	})
	if got, _ := noChanges["success"].(bool); !got {
		t.Fatalf("expected no-change incremental run to succeed, got %#v", noChanges)
	}
	if got := stringArg(noChanges, "message", ""); got != "No changes detected (tree SHA unchanged)" {
		t.Fatalf("expected no-change message, got %#v", noChanges)
	}
	if got := stringArg(noChanges, "git_head", ""); got == "" {
		t.Fatalf("expected tree-SHA no-change path to include git_head, got %#v", noChanges)
	}
	if intArg(noChanges, "changed") != 0 || intArg(noChanges, "new") != 0 || intArg(noChanges, "deleted") != 0 {
		t.Fatalf("expected zero diff counts on no-change incremental run, got %#v", noChanges)
	}
	if got := acquirer.acquireTreeCalls; got != 1 {
		t.Fatalf("expected metadata preflight to skip second tree download, acquire_tree_calls=%d", got)
	}

	acquirer.tree = map[string][]byte{
		"src/main.go": []byte("package main\n\nfunc main() {}\n"),
		"cmd/new.ts":  []byte("export const answer = 42;\n"),
		"README.md":   []byte("# ignored extension\n"),
	}

	delta := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": true,
	})
	if got, _ := delta["success"].(bool); !got {
		t.Fatalf("expected incremental delta run to succeed, got %#v", delta)
	}
	if got := intArg(delta, "changed"); got != 1 {
		t.Fatalf("expected changed=1, got %#v", delta)
	}
	if got := intArg(delta, "new"); got != 1 {
		t.Fatalf("expected new=1, got %#v", delta)
	}
	if got := intArg(delta, "deleted"); got != 1 {
		t.Fatalf("expected deleted=1, got %#v", delta)
	}
	if got := acquirer.acquireTreeCalls; got != 1 {
		t.Fatalf("expected delta run to avoid full tree download, acquire_tree_calls=%d", got)
	}
	if got := acquirer.acquireTreeSubsetCalls; got != 1 {
		t.Fatalf("expected one selective tree fetch on delta run, acquire_tree_subset_calls=%d", got)
	}
	expectedSubsetPaths := []string{"cmd/new.ts", "src/main.go"}
	if !slicesEqual(acquirer.acquiredSubsetPaths, expectedSubsetPaths) {
		t.Fatalf("expected selective fetch paths %#v, got %#v", expectedSubsetPaths, acquirer.acquiredSubsetPaths)
	}

	index, err := store.Load(context.Background(), "org/repo")
	if err != nil {
		t.Fatalf("load persisted remote index: %v", err)
	}
	if _, ok := index.Files["pkg/util.py"]; ok {
		t.Fatalf("expected deleted file pkg/util.py to be removed from index: %#v", index.Files)
	}
	if _, ok := index.Files["src/main.go"]; !ok {
		t.Fatalf("expected src/main.go to remain indexed: %#v", index.Files)
	}
	if _, ok := index.Files["cmd/new.ts"]; !ok {
		t.Fatalf("expected cmd/new.ts to be indexed: %#v", index.Files)
	}
	if got := index.Languages["go"]; got != 1 {
		t.Fatalf("expected go language count=1, got %#v", index.Languages)
	}
	if got := index.Languages["typescript"]; got != 1 {
		t.Fatalf("expected typescript language count=1, got %#v", index.Languages)
	}
}

func TestIndexRepoSkipsMetadataPrefetchOnInitialIncrementalRun(t *testing.T) {
	store := mustIndexStore(t)
	acquirer := &staticRepoAcquirer{
		tree: map[string][]byte{
			"src/main.go": []byte("package main\n"),
		},
		metadataErr: errors.New("AcquireTreeMetadata should not run on initial incremental indexing"),
	}
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:   store,
		RepoAcquirer: acquirer,
	})

	result := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": true,
	})
	if got, _ := result["success"].(bool); !got {
		t.Fatalf("expected initial incremental run to succeed without metadata prefetch, got %#v", result)
	}
	if got := acquirer.acquireMetadataCalls; got != 0 {
		t.Fatalf("expected no metadata prefetch on initial incremental run, acquire_metadata_calls=%d", got)
	}
	if got := acquirer.acquireTreeCalls; got != 1 {
		t.Fatalf("expected one full tree acquisition on initial incremental run, acquire_tree_calls=%d", got)
	}
}

func TestIndexRepoSkipsMetadataPrefetchOnNonIncrementalRun(t *testing.T) {
	store := mustIndexStore(t)
	acquirer := &staticRepoAcquirer{
		tree: map[string][]byte{
			"src/main.go": []byte("package main\n"),
		},
		metadataErr: errors.New("AcquireTreeMetadata should not run on non-incremental indexing"),
	}
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:   store,
		RepoAcquirer: acquirer,
	})

	result := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": false,
	})
	if got, _ := result["success"].(bool); !got {
		t.Fatalf("expected non-incremental run to succeed without metadata prefetch, got %#v", result)
	}
	if got := acquirer.acquireMetadataCalls; got != 0 {
		t.Fatalf("expected no metadata prefetch on non-incremental run, acquire_metadata_calls=%d", got)
	}
	if got := acquirer.acquireTreeCalls; got != 1 {
		t.Fatalf("expected one full tree acquisition on non-incremental run, acquire_tree_calls=%d", got)
	}
}

func TestIndexRepoLegacyIncrementalStateUsesMetadataSubsetFetch(t *testing.T) {
	store := mustIndexStore(t)
	if err := store.Save(context.Background(), "org/repo", storage.RepoIndex{
		Repo:         "org/repo",
		IndexedAt:    "2026-01-01T00:00:00Z",
		SourceRoot:   "https://github.com/org/repo",
		DisplayName:  "repo",
		Languages:    map[string]int{"go": 1},
		IndexVersion: repoIndexVersion,
		GitHead:      "stale-tree-sha",
		Files: map[string]string{
			"src/main.go": "legacy-content-hash",
		},
		FileBlobSHAs: map[string]string{},
		FileMTimes: map[string]int64{
			"src/main.go": 0,
		},
		Symbols: map[string]any{},
	}); err != nil {
		t.Fatalf("seed legacy incremental index: %v", err)
	}

	acquirer := &staticRepoAcquirer{
		tree: map[string][]byte{
			"src/main.go": []byte("package main\n"),
		},
	}
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:   store,
		RepoAcquirer: acquirer,
	})

	result := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": true,
	})
	if got, _ := result["success"].(bool); !got {
		t.Fatalf("expected legacy incremental run to succeed, got %#v", result)
	}
	if got := acquirer.acquireMetadataCalls; got != 1 {
		t.Fatalf("expected one metadata prefetch call, acquire_metadata_calls=%d", got)
	}
	if got := acquirer.acquireTreeCalls; got != 0 {
		t.Fatalf("expected legacy incremental path to avoid full tree acquisition, acquire_tree_calls=%d", got)
	}
	if got := acquirer.acquireTreeSubsetCalls; got != 1 {
		t.Fatalf("expected legacy incremental path to use one subset tree fetch, acquire_tree_subset_calls=%d", got)
	}
	expectedSubsetPaths := []string{"src/main.go"}
	if !slicesEqual(acquirer.acquiredSubsetPaths, expectedSubsetPaths) {
		t.Fatalf("expected metadata-driven subset paths %#v, got %#v", expectedSubsetPaths, acquirer.acquiredSubsetPaths)
	}
}

func TestIndexRepoUsesBlobSHADiffForIncrementalNoChange(t *testing.T) {
	store := mustIndexStore(t)
	acquirer := &staticRepoAcquirer{
		tree: map[string][]byte{
			"src/main.go": []byte("package main\n"),
		},
	}
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:   store,
		RepoAcquirer: acquirer,
	})

	first := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": true,
	})
	if got, _ := first["success"].(bool); !got {
		t.Fatalf("expected initial index_repo call to succeed, got %#v", first)
	}

	index, err := store.Load(context.Background(), "org/repo")
	if err != nil {
		t.Fatalf("load persisted remote index: %v", err)
	}
	if got := len(index.FileBlobSHAs); got != 1 {
		t.Fatalf("expected persisted blob-sha map for remote index, got %#v", index.FileBlobSHAs)
	}
	index.Files["src/main.go"] = "stale-content-hash"
	index.GitHead = "stale-tree-sha"
	if err := store.Save(context.Background(), "org/repo", index); err != nil {
		t.Fatalf("save corrupted index for blob-sha characterization: %v", err)
	}
	acquirer.err = errors.New("AcquireTree should not be called during blob-sha no-change preflight")

	noChanges := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": true,
	})
	if got, _ := noChanges["success"].(bool); !got {
		t.Fatalf("expected blob-sha incremental no-change run to succeed, got %#v", noChanges)
	}
	if got := stringArg(noChanges, "message", ""); got != "No changes detected" {
		t.Fatalf("expected no-change message from blob-sha diff fast path, got %#v", noChanges)
	}
	if got := stringArg(noChanges, "git_head", ""); got == "" {
		t.Fatalf("expected blob-sha no-change result to include git_head, got %#v", noChanges)
	}
	if intArg(noChanges, "changed") != 0 || intArg(noChanges, "new") != 0 || intArg(noChanges, "deleted") != 0 {
		t.Fatalf("expected zero diff counts on blob-sha no-change run, got %#v", noChanges)
	}
}

func TestIndexRepoRecoversMissingLegacyFileHashesOnDeltaRuns(t *testing.T) {
	store := mustIndexStore(t)
	acquirer := &staticRepoAcquirer{
		tree: map[string][]byte{
			"src/main.go": []byte("package main\n"),
			"pkg/util.py": []byte("def util():\n    return 1\n"),
		},
	}
	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:   store,
		RepoAcquirer: acquirer,
	})

	first := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": true,
	})
	if got, _ := first["success"].(bool); !got {
		t.Fatalf("expected initial index_repo call to succeed, got %#v", first)
	}

	index, err := store.Load(context.Background(), "org/repo")
	if err != nil {
		t.Fatalf("load persisted remote index: %v", err)
	}
	index.GitHead = "stale-tree-sha"
	index.Files["src/main.go"] = ""
	if err := store.Save(context.Background(), "org/repo", index); err != nil {
		t.Fatalf("save corrupted index for missing-hash recovery characterization: %v", err)
	}

	acquirer.tree = map[string][]byte{
		"src/main.go": []byte("package main\n"),
		"pkg/util.py": []byte("def util():\n    return 2\n"),
	}
	delta := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": true,
	})
	if got, _ := delta["success"].(bool); !got {
		t.Fatalf("expected incremental delta run to succeed, got %#v", delta)
	}
	if got := intArg(delta, "changed"); got != 1 {
		t.Fatalf("expected changed=1 from util.py delta, got %#v", delta)
	}
	if got := acquirer.acquireTreeSubsetCalls; got != 1 {
		t.Fatalf("expected one selective fetch on delta run, acquire_tree_subset_calls=%d", got)
	}
	expectedSubsetPaths := []string{"pkg/util.py", "src/main.go"}
	if !slicesEqual(acquirer.acquiredSubsetPaths, expectedSubsetPaths) {
		t.Fatalf("expected selective recovery fetch paths %#v, got %#v", expectedSubsetPaths, acquirer.acquiredSubsetPaths)
	}

	recovered, err := store.Load(context.Background(), "org/repo")
	if err != nil {
		t.Fatalf("load recovered index: %v", err)
	}
	if got := strings.TrimSpace(recovered.Files["src/main.go"]); got == "" {
		t.Fatalf("expected recovered hash for src/main.go, got %#v", recovered.Files)
	}
	if _, ok := recovered.Files["pkg/util.py"]; !ok {
		t.Fatalf("expected pkg/util.py to remain indexed: %#v", recovered.Files)
	}
	if got := len(recovered.Files); got != 2 {
		t.Fatalf("expected two indexed files after recovery merge, got %#v", recovered.Files)
	}
}

func TestIndexRepoHandlerAppliesRemoteGitignore(t *testing.T) {
	store := mustIndexStore(t)
	acquirer := &staticRepoAcquirer{
		tree: map[string][]byte{
			"src/main.go": []byte("package main\n"),
			"pkg/util.py": []byte("def util():\n    return 1\n"),
		},
		gitignore: []byte("pkg/\n"),
	}

	service := New(config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		FreshnessMode: "relaxed",
		Disabled:      map[string]struct{}{},
	}, Dependencies{
		IndexStore:   store,
		RepoAcquirer: acquirer,
	})

	result := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":         "https://github.com/org/repo",
		"incremental": false,
	})
	if got, _ := result["success"].(bool); !got {
		t.Fatalf("expected index_repo call to succeed, got %#v", result)
	}
	if got := intArg(result, "file_count"); got != 1 {
		t.Fatalf("expected gitignore filtering to keep one file, got %#v", result)
	}

	index, err := store.Load(context.Background(), "org/repo")
	if err != nil {
		t.Fatalf("load persisted remote index: %v", err)
	}
	if _, ok := index.Files["pkg/util.py"]; ok {
		t.Fatalf("expected gitignore-filtered file pkg/util.py to be absent: %#v", index.Files)
	}
	if _, ok := index.Files["src/main.go"]; !ok {
		t.Fatalf("expected src/main.go to remain indexed: %#v", index.Files)
	}
}

func TestIndexRepoAppliesConfigDiscoveryOverrides(t *testing.T) {
	store := mustIndexStore(t)
	acquirer := &staticRepoAcquirer{
		tree: map[string][]byte{
			"src/high_priority.py":       []byte("def keep_src():\n    return 1\n"),
			"pkg/second_priority.py":     []byte("def keep_pkg():\n    return 1\n"),
			"internal/third_priority.py": []byte("def keep_internal():\n    return 1\n"),
			"vendor/ignored_vendor.py":   []byte("def ignored_vendor():\n    return 1\n"),
			"docs/api_token.py":          []byte("def keep_token_doc():\n    return 1\n"),
		},
	}

	service := New(config.Config{
		ServerName:            "gocodemunch-mcp",
		ServerVersion:         "test",
		FreshnessMode:         "relaxed",
		Disabled:              map[string]struct{}{},
		MaxIndexFiles:         2,
		ExtraIgnorePatterns:   []string{"vendor/"},
		ExcludeSecretPatterns: []string{"*token*"},
	}, Dependencies{
		IndexStore:   store,
		RepoAcquirer: acquirer,
	})

	payload := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url": "https://github.com/org/repo",
	})
	if got, _ := payload["success"].(bool); !got {
		t.Fatalf("expected index_repo success with config overrides, got %#v", payload)
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
		t.Fatalf("expected extra_ignore=1 from config pattern, got %#v", skipCounts)
	}
	if got := skipCounts["secret"]; got != 0 {
		t.Fatalf("expected secret=0 with exclude_secret_patterns override, got %#v", skipCounts)
	}
	if got := skipCounts["file_limit"]; got != 2 {
		t.Fatalf("expected file_limit=2 from max_index_files cap, got %#v", skipCounts)
	}

	warnings := warningsFromPayload(payload)
	expectContainsWarning(t, warnings, "File cap reached: 4 files discovered, 2 indexed, 2 dropped")
}

func TestIndexRepoMergesConfigAndCallExtraIgnorePatterns(t *testing.T) {
	store := mustIndexStore(t)
	acquirer := &staticRepoAcquirer{
		tree: map[string][]byte{
			"keep.py":            []byte("def keep():\n    return 1\n"),
			"project_ignored.py": []byte("def project_ignored():\n    return 1\n"),
			"call_ignored.py":    []byte("def call_ignored():\n    return 1\n"),
		},
	}

	service := New(config.Config{
		ServerName:          "gocodemunch-mcp",
		ServerVersion:       "test",
		FreshnessMode:       "relaxed",
		Disabled:            map[string]struct{}{},
		ExtraIgnorePatterns: []string{"project_ignored.py"},
	}, Dependencies{
		IndexStore:   store,
		RepoAcquirer: acquirer,
	})

	payload := service.CallTool(context.Background(), "index_repo", map[string]any{
		"url":                   "https://github.com/org/repo",
		"extra_ignore_patterns": []string{"call_ignored.py"},
	})
	if got, _ := payload["success"].(bool); !got {
		t.Fatalf("expected index_repo success with merged ignore patterns, got %#v", payload)
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
		t.Fatalf("expected extra_ignore=2 from config+call patterns, got %#v", skipCounts)
	}
}
