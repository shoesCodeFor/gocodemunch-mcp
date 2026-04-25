package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/watcher"
)

type serverFunc func(context.Context) error

func (s serverFunc) Serve(ctx context.Context) error {
	return s(ctx)
}

func TestResolveWatcherEnabledPrecedence(t *testing.T) {
	if !resolveWatcherEnabled(nil, false, "1") {
		t.Fatal("expected env watcher toggle to enable watcher when CLI flag absent")
	}
	if resolveWatcherEnabled([]string{"--watcher=false"}, false, "1") {
		t.Fatal("expected explicit CLI --watcher=false to override env enablement")
	}
	if !resolveWatcherEnabled([]string{"--watcher"}, true, "0") {
		t.Fatal("expected explicit CLI --watcher to override env disablement")
	}
}

func TestRunServerWithEmbeddedWatcherReleasesOwnershipOnExit(t *testing.T) {
	storageRoot := t.TempDir()
	folder := t.TempDir()

	controller := watcher.NewStateControllerWithStoragePath(storageRoot)
	err := runServerWithEmbeddedWatcher(context.Background(), serverFunc(func(context.Context) error {
		return nil
	}), controller, []string{folder})
	if err != nil {
		t.Fatalf("run server with embedded watcher failed: %v", err)
	}

	other := watcher.NewStateControllerWithStoragePath(storageRoot)
	if err := other.Start(context.Background(), folder); err != nil {
		t.Fatalf("acquire folder after shutdown failed: %v", err)
	}
	backpressure := other.Backpressure(context.Background())
	if got, _ := backpressure["watched_repo_count"].(int); got != 1 {
		t.Fatalf("expected lock ownership to be released after shutdown, got %v (%#v)", got, backpressure)
	}
}

func TestRunServerWithEmbeddedWatcherSurfacesStartupValidationError(t *testing.T) {
	storageRoot := t.TempDir()
	controller := watcher.NewStateControllerWithStoragePath(storageRoot)
	missing := filepath.Join(t.TempDir(), "missing")

	err := runServerWithEmbeddedWatcher(context.Background(), serverFunc(func(context.Context) error {
		t.Fatal("server should not run when watcher startup validation fails")
		return nil
	}), controller, []string{missing})
	if err == nil {
		t.Fatalf("expected startup validation error for missing watcher path %s", missing)
	}
}
