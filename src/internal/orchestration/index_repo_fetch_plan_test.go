package orchestration

import (
	"context"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
)

func TestPlanRemoteTreeFetch(t *testing.T) {
	t.Parallel()

	t.Run("non metadata prefetch uses full tree acquisition", func(t *testing.T) {
		t.Parallel()

		plan := planRemoteTreeFetch(
			false,
			storage.RepoIndex{},
			[]string{"src/main.go"},
			map[string]string{"src/main.go": "sha"},
		)
		if plan.useMetadataPrefetch {
			t.Fatalf("expected metadata prefetch disabled, got %#v", plan)
		}
		if plan.useIncrementalBlobDiff {
			t.Fatalf("expected incremental blob diff disabled, got %#v", plan)
		}
		if len(plan.fetchPaths) != 0 {
			t.Fatalf("expected no fetch paths when metadata prefetch is disabled, got %#v", plan.fetchPaths)
		}
	})

	t.Run("legacy incremental state fetches metadata subset paths", func(t *testing.T) {
		t.Parallel()

		metadataSourceFiles := []string{"cmd/new.ts", "src/main.go"}
		plan := planRemoteTreeFetch(
			true,
			storage.RepoIndex{
				Files: map[string]string{
					"src/main.go": "legacy-content-hash",
				},
				FileBlobSHAs: map[string]string{},
			},
			metadataSourceFiles,
			map[string]string{
				"cmd/new.ts":  "blob-a",
				"src/main.go": "blob-b",
			},
		)
		if !plan.useMetadataPrefetch {
			t.Fatalf("expected metadata prefetch enabled, got %#v", plan)
		}
		if plan.useIncrementalBlobDiff {
			t.Fatalf("expected incremental blob diff disabled for legacy state, got %#v", plan)
		}
		if !slicesEqual(plan.fetchPaths, metadataSourceFiles) {
			t.Fatalf("expected metadata source paths %#v, got %#v", metadataSourceFiles, plan.fetchPaths)
		}
	})

	t.Run("blob sha incremental state fetches changed and missing content", func(t *testing.T) {
		t.Parallel()

		plan := planRemoteTreeFetch(
			true,
			storage.RepoIndex{
				Files: map[string]string{
					"pkg/util.py": "",
					"src/main.go": "existing-hash",
				},
				FileBlobSHAs: map[string]string{
					"pkg/util.py": "blob-2",
					"src/main.go": "blob-1",
				},
			},
			[]string{"cmd/new.ts", "pkg/util.py", "src/main.go"},
			map[string]string{
				"cmd/new.ts":  "blob-3",
				"pkg/util.py": "blob-2",
				"src/main.go": "blob-4",
			},
		)
		if !plan.useMetadataPrefetch {
			t.Fatalf("expected metadata prefetch enabled, got %#v", plan)
		}
		if !plan.useIncrementalBlobDiff {
			t.Fatalf("expected incremental blob diff enabled, got %#v", plan)
		}
		expected := []string{"cmd/new.ts", "pkg/util.py", "src/main.go"}
		if !slicesEqual(plan.fetchPaths, expected) {
			t.Fatalf("expected incremental fetch paths %#v, got %#v", expected, plan.fetchPaths)
		}
	})
}

func TestAcquireRemoteTreeForIndex(t *testing.T) {
	t.Parallel()

	t.Run("full tree path calls acquire tree once", func(t *testing.T) {
		t.Parallel()

		acquirer := &staticRepoAcquirer{
			tree: map[string][]byte{
				"src/main.go": []byte("package main\n"),
			},
		}

		tree, err := acquireRemoteTreeForIndex(context.Background(), acquirer, "https://github.com/org/repo", remoteTreeFetchPlan{})
		if err != nil {
			t.Fatalf("expected full-tree acquisition success, got error %v", err)
		}
		if len(tree) != 1 {
			t.Fatalf("expected one fetched file, got %#v", tree)
		}
		if got := acquirer.acquireTreeCalls; got != 1 {
			t.Fatalf("expected one full-tree fetch, acquire_tree_calls=%d", got)
		}
		if got := acquirer.acquireTreeSubsetCalls; got != 0 {
			t.Fatalf("expected no subset fetch, acquire_tree_subset_calls=%d", got)
		}
	})

	t.Run("metadata subset path calls acquire tree subset", func(t *testing.T) {
		t.Parallel()

		acquirer := &staticRepoAcquirer{
			tree: map[string][]byte{
				"src/main.go": []byte("package main\n"),
			},
		}

		tree, err := acquireRemoteTreeForIndex(context.Background(), acquirer, "https://github.com/org/repo", remoteTreeFetchPlan{
			useMetadataPrefetch: true,
			fetchPaths:          []string{"src/main.go"},
		})
		if err != nil {
			t.Fatalf("expected subset acquisition success, got error %v", err)
		}
		if len(tree) != 1 {
			t.Fatalf("expected one fetched file from subset path, got %#v", tree)
		}
		if got := acquirer.acquireTreeCalls; got != 0 {
			t.Fatalf("expected no full-tree fetch, acquire_tree_calls=%d", got)
		}
		if got := acquirer.acquireTreeSubsetCalls; got != 1 {
			t.Fatalf("expected one subset fetch, acquire_tree_subset_calls=%d", got)
		}
	})

	t.Run("metadata prefetch with no fetch paths skips tree acquisition", func(t *testing.T) {
		t.Parallel()

		acquirer := &staticRepoAcquirer{
			tree: map[string][]byte{
				"src/main.go": []byte("package main\n"),
			},
		}

		tree, err := acquireRemoteTreeForIndex(context.Background(), acquirer, "https://github.com/org/repo", remoteTreeFetchPlan{
			useMetadataPrefetch: true,
		})
		if err != nil {
			t.Fatalf("expected empty subset path to return empty tree, got error %v", err)
		}
		if len(tree) != 0 {
			t.Fatalf("expected no fetched files when subset path list is empty, got %#v", tree)
		}
		if got := acquirer.acquireTreeCalls; got != 0 {
			t.Fatalf("expected no full-tree fetch, acquire_tree_calls=%d", got)
		}
		if got := acquirer.acquireTreeSubsetCalls; got != 0 {
			t.Fatalf("expected no subset fetch when fetch paths are empty, acquire_tree_subset_calls=%d", got)
		}
	})
}
