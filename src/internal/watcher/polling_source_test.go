package watcher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanFolderSnapshotSkipsHiddenEntries(t *testing.T) {
	root := t.TempDir()

	visible := filepath.Join(root, "pkg", "main.go")
	if err := os.MkdirAll(filepath.Dir(visible), 0o755); err != nil {
		t.Fatalf("create visible parent: %v", err)
	}
	if err := os.WriteFile(visible, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write visible file: %v", err)
	}

	hiddenDirFile := filepath.Join(root, ".git", "config")
	if err := os.MkdirAll(filepath.Dir(hiddenDirFile), 0o755); err != nil {
		t.Fatalf("create hidden dir parent: %v", err)
	}
	if err := os.WriteFile(hiddenDirFile, []byte("[core]\n"), 0o644); err != nil {
		t.Fatalf("write hidden dir file: %v", err)
	}

	hiddenFile := filepath.Join(root, ".env")
	if err := os.WriteFile(hiddenFile, []byte("TOKEN=x\n"), 0o644); err != nil {
		t.Fatalf("write hidden root file: %v", err)
	}

	snapshot, err := scanFolderSnapshot(normalizeWatcherPath(root))
	if err != nil {
		t.Fatalf("scan folder snapshot: %v", err)
	}

	if len(snapshot) != 1 {
		t.Fatalf("expected only one visible file in snapshot, got %d (%#v)", len(snapshot), snapshot)
	}
	if _, ok := snapshot[normalizeWatcherPath(visible)]; !ok {
		t.Fatalf("expected visible file snapshot entry for %s", visible)
	}
	if _, ok := snapshot[normalizeWatcherPath(hiddenDirFile)]; ok {
		t.Fatalf("did not expect hidden dir file snapshot entry for %s", hiddenDirFile)
	}
	if _, ok := snapshot[normalizeWatcherPath(hiddenFile)]; ok {
		t.Fatalf("did not expect hidden root file snapshot entry for %s", hiddenFile)
	}
}

func TestDiffFolderSnapshotsDetectsAddedModifiedDeletedDeterministically(t *testing.T) {
	before := map[string]fileSnapshot{
		"/tmp/a.go": {size: 10, modUnixNanos: 1},
		"/tmp/c.go": {size: 30, modUnixNanos: 3},
	}
	after := map[string]fileSnapshot{
		"/tmp/a.go": {size: 11, modUnixNanos: 2},
		"/tmp/b.go": {size: 20, modUnixNanos: 2},
	}

	changes := diffFolderSnapshots(before, after)
	if len(changes) != 3 {
		t.Fatalf("expected three changes, got %#v", changes)
	}

	if changes[0].Path != "/tmp/a.go" || changes[0].ChangeType != ChangeModified {
		t.Fatalf("unexpected first change: %#v", changes[0])
	}
	if changes[1].Path != "/tmp/b.go" || changes[1].ChangeType != ChangeAdded {
		t.Fatalf("unexpected second change: %#v", changes[1])
	}
	if changes[2].Path != "/tmp/c.go" || changes[2].ChangeType != ChangeDeleted {
		t.Fatalf("unexpected third change: %#v", changes[2])
	}
}
