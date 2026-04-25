package watcher

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeEventSource struct {
	mu      sync.Mutex
	streams map[string]*fakeEventStream
}

type fakeEventStream struct {
	events chan WatcherChange
	errs   chan error
}

func newFakeEventSource() *fakeEventSource {
	return &fakeEventSource{
		streams: map[string]*fakeEventStream{},
	}
}

func (s *fakeEventSource) Open(_ context.Context, folder string) (<-chan WatcherChange, <-chan error, error) {
	stream := s.ensureStream(folder)
	return stream.events, stream.errs, nil
}

func (s *fakeEventSource) emit(folder string, change WatcherChange) {
	stream := s.ensureStream(folder)
	stream.events <- change
}

func (s *fakeEventSource) emitErr(folder string, err error) {
	stream := s.ensureStream(folder)
	stream.errs <- err
}

func (s *fakeEventSource) ensureStream(folder string) *fakeEventStream {
	s.mu.Lock()
	defer s.mu.Unlock()

	if stream, ok := s.streams[folder]; ok {
		return stream
	}
	stream := &fakeEventStream{
		events: make(chan WatcherChange, 64),
		errs:   make(chan error, 8),
	}
	s.streams[folder] = stream
	return stream
}

func TestEmbeddedWorkerDebounceBatchesAndFiltersHiddenChanges(t *testing.T) {
	controller := NewStateControllerWithStoragePath(t.TempDir())
	folder := t.TempDir()
	normalizedFolder := mustNormalizeFolder(t, folder)
	source := newFakeEventSource()

	batches := make(chan []WatcherChange, 2)
	worker, err := NewEmbeddedWorker(controller, []string{folder},
		WithEmbeddedEventSource(source),
		WithEmbeddedDebounce(40*time.Millisecond),
		WithEmbeddedBatchProcessor(BatchProcessorFunc(func(_ context.Context, _ string, changes []WatcherChange) error {
			copied := append([]WatcherChange(nil), changes...)
			batches <- copied
			return nil
		})),
	)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer func() {
		if stopErr := worker.Stop(context.Background()); stopErr != nil {
			t.Fatalf("stop worker: %v", stopErr)
		}
	}()

	fileB := filepath.Join(normalizedFolder, "b.go")
	fileA := filepath.Join(normalizedFolder, "a.go")
	hidden := filepath.Join(normalizedFolder, ".git", "index")
	source.emit(normalizedFolder, WatcherChange{ChangeType: ChangeAdded, Path: fileB})
	source.emit(normalizedFolder, WatcherChange{ChangeType: ChangeModified, Path: hidden})
	source.emit(normalizedFolder, WatcherChange{ChangeType: ChangeModified, Path: fileA})

	select {
	case batch := <-batches:
		if len(batch) != 2 {
			t.Fatalf("expected 2 visible changes, got %#v", batch)
		}
		wantA := normalizeWatcherPath(fileA)
		wantB := normalizeWatcherPath(fileB)
		if batch[0].Path != wantA || batch[0].ChangeType != ChangeModified {
			t.Fatalf("unexpected first change: %#v", batch[0])
		}
		if batch[1].Path != wantB || batch[1].ChangeType != ChangeAdded {
			t.Fatalf("unexpected second change: %#v", batch[1])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for debounced batch")
	}
}

func TestEmbeddedWorkerSingleFlightProcessesDeferredBatches(t *testing.T) {
	controller := NewStateControllerWithStoragePath(t.TempDir())
	folder := t.TempDir()
	normalizedFolder := mustNormalizeFolder(t, folder)
	source := newFakeEventSource()

	releaseFirst := make(chan struct{})
	firstStarted := make(chan struct{})
	doneCalls := make(chan int32, 4)

	var (
		active    int32
		maxActive int32
		callCount int32
	)

	processor := BatchProcessorFunc(func(ctx context.Context, _ string, _ []WatcherChange) error {
		current := atomic.AddInt32(&active, 1)
		for {
			previous := atomic.LoadInt32(&maxActive)
			if current <= previous || atomic.CompareAndSwapInt32(&maxActive, previous, current) {
				break
			}
		}

		callNumber := atomic.AddInt32(&callCount, 1)
		if callNumber == 1 {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
			}
		}

		atomic.AddInt32(&active, -1)
		doneCalls <- callNumber
		return nil
	})

	worker, err := NewEmbeddedWorker(controller, []string{folder},
		WithEmbeddedEventSource(source),
		WithEmbeddedDebounce(20*time.Millisecond),
		WithEmbeddedBatchProcessor(processor),
	)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer func() {
		if stopErr := worker.Stop(context.Background()); stopErr != nil {
			t.Fatalf("stop worker: %v", stopErr)
		}
	}()

	source.emit(normalizedFolder, WatcherChange{
		ChangeType: ChangeAdded,
		Path:       filepath.Join(normalizedFolder, "first.go"),
	})

	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first batch did not start")
	}

	source.emit(normalizedFolder, WatcherChange{
		ChangeType: ChangeModified,
		Path:       filepath.Join(normalizedFolder, "second.go"),
	})

	close(releaseFirst)

	for i := 0; i < 2; i++ {
		select {
		case <-doneCalls:
		case <-time.After(2 * time.Second):
			t.Fatal("expected two processed batches")
		}
	}

	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("expected single-flight processor execution, got max active=%d", got)
	}
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Fatalf("expected two batch calls, got %d", got)
	}
}

func TestEmbeddedWorkerPublishesRuntimeBackpressure(t *testing.T) {
	controller := NewStateControllerWithStoragePath(t.TempDir())
	folder := t.TempDir()
	normalizedFolder := mustNormalizeFolder(t, folder)
	source := newFakeEventSource()

	started := make(chan struct{})
	release := make(chan struct{})
	processed := make(chan struct{}, 1)

	worker, err := NewEmbeddedWorker(controller, []string{folder},
		WithEmbeddedEventSource(source),
		WithEmbeddedDebounce(30*time.Millisecond),
		WithEmbeddedBatchProcessor(BatchProcessorFunc(func(ctx context.Context, _ string, _ []WatcherChange) error {
			select {
			case <-started:
			default:
				close(started)
			}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			processed <- struct{}{}
			return nil
		})),
	)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer func() {
		if stopErr := worker.Stop(context.Background()); stopErr != nil {
			t.Fatalf("stop worker: %v", stopErr)
		}
	}()

	source.emit(normalizedFolder, WatcherChange{
		ChangeType: ChangeAdded,
		Path:       filepath.Join(normalizedFolder, "main.go"),
	})

	if err := waitForBackpressure(controller, func(snapshot map[string]any) bool {
		return intFromAny(snapshot["watcher_pending_events"]) == 1 &&
			intFromAny(snapshot["watcher_pending_batches"]) == 1
	}); err != nil {
		t.Fatalf("pending batch metadata not observed: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("batch processor did not start")
	}

	if err := waitForBackpressure(controller, func(snapshot map[string]any) bool {
		return intFromAny(snapshot["watcher_batches_inflight"]) == 1 &&
			intFromAny(snapshot["watcher_pending_events"]) == 0
	}); err != nil {
		t.Fatalf("inflight metadata not observed: %v", err)
	}

	close(release)
	select {
	case <-processed:
	case <-time.After(2 * time.Second):
		t.Fatal("batch processor did not complete")
	}

	if err := waitForBackpressure(controller, func(snapshot map[string]any) bool {
		return intFromAny(snapshot["watcher_pending_events"]) == 0 &&
			intFromAny(snapshot["watcher_pending_batches"]) == 0 &&
			intFromAny(snapshot["watcher_batches_inflight"]) == 0 &&
			intFromAny(snapshot["watcher_runtime_errors"]) == 0
	}); err != nil {
		t.Fatalf("final backpressure metadata not observed: %v", err)
	}
}

func TestEmbeddedWorkerRecordsRuntimeErrorsFromSource(t *testing.T) {
	controller := NewStateControllerWithStoragePath(t.TempDir())
	folder := t.TempDir()
	normalizedFolder := mustNormalizeFolder(t, folder)
	source := newFakeEventSource()
	expectedErr := errors.New("source failed")

	worker, err := NewEmbeddedWorker(controller, []string{folder},
		WithEmbeddedEventSource(source),
		WithEmbeddedDebounce(10*time.Millisecond),
		WithEmbeddedBatchProcessor(BatchProcessorFunc(func(context.Context, string, []WatcherChange) error {
			return nil
		})),
	)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer func() {
		if stopErr := worker.Stop(context.Background()); stopErr != nil {
			t.Fatalf("stop worker: %v", stopErr)
		}
	}()

	source.emitErr(normalizedFolder, expectedErr)
	if err := waitForBackpressure(controller, func(snapshot map[string]any) bool {
		return intFromAny(snapshot["watcher_runtime_errors"]) == 1
	}); err != nil {
		t.Fatalf("runtime error metadata not observed: %v", err)
	}
}

func waitForBackpressure(controller *StateController, match func(snapshot map[string]any) bool) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot := controller.Backpressure(context.Background())
		if match(snapshot) {
			return nil
		}
		if time.Now().After(deadline) {
			return context.DeadlineExceeded
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func intFromAny(value any) int {
	if value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	default:
		return 0
	}
}

func mustNormalizeFolder(t *testing.T, folder string) string {
	t.Helper()

	normalized, err := normalizeEmbeddedFolders([]string{folder})
	if err != nil {
		t.Fatalf("normalize folder %s: %v", folder, err)
	}
	if len(normalized) != 1 {
		t.Fatalf("expected one normalized folder for %s, got %#v", folder, normalized)
	}
	return normalized[0]
}
