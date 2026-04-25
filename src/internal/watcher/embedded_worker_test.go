package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewEmbeddedWorkerValidatesPaths(t *testing.T) {
	controller := NewStateControllerWithStoragePath(t.TempDir())

	missingPath := filepath.Join(t.TempDir(), "missing")
	if _, err := NewEmbeddedWorker(controller, []string{missingPath}); err == nil {
		t.Fatalf("expected missing path validation error for %s", missingPath)
	}

	filePath := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file path: %v", err)
	}
	if _, err := NewEmbeddedWorker(controller, []string{filePath}); err == nil {
		t.Fatalf("expected non-directory validation error for %s", filePath)
	}
}

func TestEmbeddedWorkerRunReleasesLockAfterCancellation(t *testing.T) {
	storageRoot := t.TempDir()
	folder := t.TempDir()

	controller := NewStateControllerWithStoragePath(storageRoot)
	if controller.locks == nil {
		t.Fatal("expected lock manager to be configured")
	}

	worker, err := NewEmbeddedWorker(controller, []string{folder})
	if err != nil {
		t.Fatalf("create embedded worker: %v", err)
	}

	lockPath, err := controller.locks.lockPath(folder)
	if err != nil {
		t.Fatalf("compute lock path: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- worker.Run(ctx)
	}()

	if err := waitForLockPresence(lockPath, true, 2*time.Second); err != nil {
		t.Fatalf("wait for lock creation: %v", err)
	}

	cancel()

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("worker run failed: %v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker run did not exit after cancellation")
	}

	if err := waitForLockPresence(lockPath, false, 2*time.Second); err != nil {
		t.Fatalf("wait for lock release: %v", err)
	}
}

func TestEmbeddedWorkerStopDoesNotReleaseForeignLock(t *testing.T) {
	storageRoot := t.TempDir()
	folder := t.TempDir()

	owner := NewStateControllerWithStoragePath(storageRoot)
	other := NewStateControllerWithStoragePath(storageRoot)
	if owner.locks == nil || other.locks == nil {
		t.Fatal("expected lock manager to be configured")
	}

	if err := owner.Start(context.Background(), folder); err != nil {
		t.Fatalf("owner start failed: %v", err)
	}

	worker, err := NewEmbeddedWorker(other, []string{folder})
	if err != nil {
		t.Fatalf("create embedded worker: %v", err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("worker start failed: %v", err)
	}

	otherBackpressure := other.Backpressure(context.Background())
	if got, _ := otherBackpressure["watched_repo_count"].(int); got != 0 {
		t.Fatalf("expected worker controller to skip foreign-held lock, got watched_repo_count=%v (%#v)", got, otherBackpressure)
	}

	lockPath, err := owner.locks.lockPath(folder)
	if err != nil {
		t.Fatalf("compute lock path: %v", err)
	}
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Fatalf("expected owner lock file to exist: %v", statErr)
	}

	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("worker stop failed: %v", err)
	}
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Fatalf("expected owner lock file to remain after foreign stop: %v", statErr)
	}

	ownerBackpressure := owner.Backpressure(context.Background())
	if got, _ := ownerBackpressure["watched_repo_count"].(int); got != 1 {
		t.Fatalf("expected owner controller to remain watched, got %v (%#v)", got, ownerBackpressure)
	}
}

func waitForLockPresence(lockPath string, shouldExist bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := os.Stat(lockPath)
		exists := err == nil
		if shouldExist && exists {
			return nil
		}
		if !shouldExist && os.IsNotExist(err) {
			return nil
		}
		if time.Now().After(deadline) {
			if shouldExist {
				return context.DeadlineExceeded
			}
			return context.DeadlineExceeded
		}
		time.Sleep(5 * time.Millisecond)
	}
}
