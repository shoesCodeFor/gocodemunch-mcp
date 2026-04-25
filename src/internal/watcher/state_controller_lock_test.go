package watcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateControllerStartStopHonorsFolderLock(t *testing.T) {
	storageRoot := t.TempDir()
	folder := t.TempDir()

	first := NewStateControllerWithStoragePath(storageRoot)
	second := NewStateControllerWithStoragePath(storageRoot)

	if err := first.Start(context.Background(), folder); err != nil {
		t.Fatalf("first start failed: %v", err)
	}
	firstBackpressure := first.Backpressure(context.Background())
	if got, _ := firstBackpressure["watched_repo_count"].(int); got != 1 {
		t.Fatalf("expected first controller to watch one folder, got %v (%#v)", got, firstBackpressure)
	}

	if err := second.Start(context.Background(), folder); err != nil {
		t.Fatalf("second start failed: %v", err)
	}
	secondBackpressure := second.Backpressure(context.Background())
	if got, _ := secondBackpressure["watched_repo_count"].(int); got != 0 {
		t.Fatalf("expected second controller to skip already-watched folder, got %v (%#v)", got, secondBackpressure)
	}

	if err := first.Stop(context.Background(), folder); err != nil {
		t.Fatalf("first stop failed: %v", err)
	}

	if err := second.Start(context.Background(), folder); err != nil {
		t.Fatalf("second start after release failed: %v", err)
	}
	secondBackpressure = second.Backpressure(context.Background())
	if got, _ := secondBackpressure["watched_repo_count"].(int); got != 1 {
		t.Fatalf("expected second controller to acquire folder after release, got %v (%#v)", got, secondBackpressure)
	}
}

func TestStateControllerStartCleansStaleLockFile(t *testing.T) {
	storageRoot := t.TempDir()
	folder := t.TempDir()

	controller := NewStateControllerWithStoragePath(storageRoot)
	if controller.locks == nil {
		t.Fatal("expected lock manager to be configured")
	}

	lockPath, err := controller.locks.lockPath(folder)
	if err != nil {
		t.Fatalf("compute lock path: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte(`{"folder":"missing-pid"}`), 0o644); err != nil {
		t.Fatalf("seed stale lock: %v", err)
	}

	if err := controller.Start(context.Background(), folder); err != nil {
		t.Fatalf("start with stale lock failed: %v", err)
	}

	payload, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read rewritten lock: %v", err)
	}
	var decoded watcherLockPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode rewritten lock: %v", err)
	}
	if decoded.PID != os.Getpid() {
		t.Fatalf("expected lock pid=%d, got %d", os.Getpid(), decoded.PID)
	}
	if decoded.Folder == "" {
		t.Fatalf("expected rewritten lock folder, got %#v", decoded)
	}
}

func TestStateControllerStopReleasesLockWithCanceledContext(t *testing.T) {
	storageRoot := t.TempDir()
	folder := t.TempDir()

	first := NewStateControllerWithStoragePath(storageRoot)
	second := NewStateControllerWithStoragePath(storageRoot)
	if first.locks == nil || second.locks == nil {
		t.Fatal("expected lock manager to be configured")
	}

	lockPath, err := first.locks.lockPath(folder)
	if err != nil {
		t.Fatalf("compute lock path: %v", err)
	}

	if err := first.Start(context.Background(), folder); err != nil {
		t.Fatalf("first start failed: %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("expected lock file after start: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := first.Stop(canceledCtx, folder); err != nil {
		t.Fatalf("first stop with canceled context failed: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected lock file removal after stop, stat err=%v", err)
	}

	if err := second.Start(context.Background(), folder); err != nil {
		t.Fatalf("second start after canceled stop failed: %v", err)
	}
	secondBackpressure := second.Backpressure(context.Background())
	if got, _ := secondBackpressure["watched_repo_count"].(int); got != 1 {
		t.Fatalf("expected second controller to acquire folder after canceled stop, got %v (%#v)", got, secondBackpressure)
	}
}

func TestStateControllerRestartReacquiresLockAfterStop(t *testing.T) {
	storageRoot := t.TempDir()
	folder := t.TempDir()

	controller := NewStateControllerWithStoragePath(storageRoot)
	if controller.locks == nil {
		t.Fatal("expected lock manager to be configured")
	}

	lockPath, err := controller.locks.lockPath(folder)
	if err != nil {
		t.Fatalf("compute lock path: %v", err)
	}

	if err := controller.Start(context.Background(), folder); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if err := controller.Stop(context.Background(), folder); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected lock file removal after stop, stat err=%v", err)
	}

	if err := controller.Start(context.Background(), folder); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	restartedBackpressure := controller.Backpressure(context.Background())
	if got, _ := restartedBackpressure["watched_repo_count"].(int); got != 1 {
		t.Fatalf("expected watched_repo_count=1 after restart, got %v (%#v)", got, restartedBackpressure)
	}

	payload, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock file after restart: %v", err)
	}
	var decoded watcherLockPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode lock payload after restart: %v", err)
	}
	if decoded.PID != os.Getpid() {
		t.Fatalf("expected restarted lock pid=%d, got %d", os.Getpid(), decoded.PID)
	}
	if decoded.Folder == "" {
		t.Fatalf("expected restarted lock folder, got %#v", decoded)
	}
}

func TestFolderLockManagerRetriesOnceAfterStaleCleanupRace(t *testing.T) {
	lockManager, err := newFolderLockManager(t.TempDir())
	if err != nil {
		t.Fatalf("create lock manager: %v", err)
	}

	folder := t.TempDir()
	lockPath, err := lockManager.lockPath(folder)
	if err != nil {
		t.Fatalf("compute lock path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("ensure lock dir: %v", err)
	}

	// Seed stale lock (dead PID) so Acquire enters cleanup + retry path.
	if err := os.WriteFile(lockPath, []byte(`{"pid":999999999}`), 0o644); err != nil {
		t.Fatalf("seed stale lock: %v", err)
	}

	raceDone := make(chan struct{})
	go func() {
		defer close(raceDone)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			_, statErr := os.Stat(lockPath)
			if os.IsNotExist(statErr) {
				payload := watcherLockPayload{
					PID:       os.Getpid(),
					Folder:    folder,
					StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
				}
				encoded, _ := json.Marshal(payload)
				_ = os.WriteFile(lockPath, encoded, 0o644)
				return
			}
			time.Sleep(1 * time.Millisecond)
		}
	}()

	acquired, err := lockManager.Acquire(folder)
	if err != nil {
		t.Fatalf("acquire after stale cleanup race failed: %v", err)
	}
	<-raceDone
	if acquired {
		t.Fatalf("expected cleanup-retry race to skip watcher start, acquired=%v", acquired)
	}
}
