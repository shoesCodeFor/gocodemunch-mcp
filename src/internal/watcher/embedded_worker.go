package watcher

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// EmbeddedWorker keeps folder watch ownership active while the MCP server runs.
// It owns debounce + single-flight batch processing per folder.
type EmbeddedWorker struct {
	controller Controller
	folders    []string
	source     EventSource
	processor  BatchProcessor
	debounce   time.Duration
	sink       RuntimeBackpressureSink

	mu       sync.Mutex
	running  bool
	started  []string
	cancelFn context.CancelFunc
	wg       sync.WaitGroup
}

// BatchProcessorFunc adapts a function into BatchProcessor.
type BatchProcessorFunc func(ctx context.Context, folder string, changes []WatcherChange) error

func (fn BatchProcessorFunc) ProcessBatch(ctx context.Context, folder string, changes []WatcherChange) error {
	if fn == nil {
		return nil
	}
	return fn(ctx, folder, changes)
}

// EmbeddedOption customizes embedded watcher runtime behavior.
type EmbeddedOption func(*embeddedOptions)

type embeddedOptions struct {
	debounce     time.Duration
	pollInterval time.Duration
	source       EventSource
	processor    BatchProcessor
}

// WithEmbeddedDebounce overrides batch debounce duration.
func WithEmbeddedDebounce(duration time.Duration) EmbeddedOption {
	return func(opts *embeddedOptions) {
		opts.debounce = duration
	}
}

// WithEmbeddedPollInterval overrides polling cadence for default event source.
func WithEmbeddedPollInterval(duration time.Duration) EmbeddedOption {
	return func(opts *embeddedOptions) {
		opts.pollInterval = duration
	}
}

// WithEmbeddedEventSource injects a custom event source.
func WithEmbeddedEventSource(source EventSource) EmbeddedOption {
	return func(opts *embeddedOptions) {
		opts.source = source
	}
}

// WithEmbeddedBatchProcessor injects custom per-batch execution.
func WithEmbeddedBatchProcessor(processor BatchProcessor) EmbeddedOption {
	return func(opts *embeddedOptions) {
		opts.processor = processor
	}
}

// NewEmbeddedWorker validates watcher prerequisites and canonicalizes watched folders.
func NewEmbeddedWorker(controller Controller, folders []string, optionFns ...EmbeddedOption) (*EmbeddedWorker, error) {
	if controller == nil {
		return nil, errors.New("watcher controller is required")
	}

	normalized, err := normalizeEmbeddedFolders(folders)
	if err != nil {
		return nil, err
	}

	opts := embeddedOptions{
		debounce:     defaultDebounceInterval,
		pollInterval: defaultPollInterval,
	}
	for _, option := range optionFns {
		option(&opts)
	}
	if opts.debounce <= 0 {
		opts.debounce = defaultDebounceInterval
	}
	if opts.pollInterval <= 0 {
		opts.pollInterval = defaultPollInterval
	}
	if opts.source == nil {
		opts.source = NewPollingEventSource(opts.pollInterval)
	}
	if opts.processor == nil {
		opts.processor = newLifecycleBatchProcessor(controller)
	}

	sink, _ := controller.(RuntimeBackpressureSink)

	return &EmbeddedWorker{
		controller: controller,
		folders:    normalized,
		source:     opts.source,
		processor:  opts.processor,
		debounce:   opts.debounce,
		sink:       sink,
	}, nil
}

// Start acquires watch ownership for configured folders.
func (w *EmbeddedWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil
	}
	w.running = true
	folders := append([]string(nil), w.folders...)
	w.mu.Unlock()

	started := make([]string, 0, len(folders))
	for _, folder := range folders {
		if err := w.controller.Start(ctx, folder); err != nil {
			for i := len(started) - 1; i >= 0; i-- {
				_ = w.controller.Stop(context.Background(), started[i])
			}
			w.mu.Lock()
			w.running = false
			w.mu.Unlock()
			return fmt.Errorf("start watcher for %s: %w", folder, err)
		}
		started = append(started, folder)
	}

	runCtx, cancel := context.WithCancel(ctx)

	w.mu.Lock()
	w.started = started
	w.cancelFn = cancel
	w.mu.Unlock()

	for _, folder := range started {
		w.publishRuntime(folder, RuntimeBackpressure{})
		w.startFolderLoop(runCtx, folder)
	}
	return nil
}

// Stop releases watch ownership for folders previously started by this worker.
func (w *EmbeddedWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return nil
	}
	started := append([]string(nil), w.started...)
	w.running = false
	w.started = nil
	cancel := w.cancelFn
	w.cancelFn = nil
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	w.wg.Wait()

	var errs []error
	for _, folder := range started {
		w.clearRuntime(folder)
	}

	for i := len(started) - 1; i >= 0; i-- {
		if err := w.controller.Stop(ctx, started[i]); err != nil {
			errs = append(errs, fmt.Errorf("stop watcher for %s: %w", started[i], err))
		}
	}
	return errors.Join(errs...)
}

// Run starts the worker, blocks until cancellation, and always performs shutdown.
func (w *EmbeddedWorker) Run(ctx context.Context) error {
	if err := w.Start(ctx); err != nil {
		return err
	}

	<-ctx.Done()

	stopCtx := context.Background()
	return w.Stop(stopCtx)
}

func (w *EmbeddedWorker) startFolderLoop(ctx context.Context, folder string) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.runFolderLoop(ctx, folder)
	}()
}

func (w *EmbeddedWorker) runFolderLoop(ctx context.Context, folder string) {
	events, errs, err := w.source.Open(ctx, folder)
	if err != nil {
		w.publishRuntime(folder, RuntimeBackpressure{
			LastError: strings.TrimSpace(err.Error()),
		})
		return
	}

	pending := map[string]WatcherChange{}
	lastError := ""
	var debounceTimer *time.Timer
	var debounceCh <-chan time.Time

	resetDebounce := func() {
		if debounceTimer == nil {
			debounceTimer = time.NewTimer(w.debounce)
			debounceCh = debounceTimer.C
			return
		}
		if !debounceTimer.Stop() {
			select {
			case <-debounceTimer.C:
			default:
			}
		}
		debounceTimer.Reset(w.debounce)
	}

	stopDebounce := func() {
		if debounceTimer == nil {
			return
		}
		if !debounceTimer.Stop() {
			select {
			case <-debounceTimer.C:
			default:
			}
		}
		debounceTimer = nil
		debounceCh = nil
	}

	for {
		select {
		case <-ctx.Done():
			stopDebounce()
			return
		case sourceErr, ok := <-errs:
			if !ok {
				errs = nil
				if events == nil {
					stopDebounce()
					return
				}
				continue
			}
			if sourceErr == nil {
				continue
			}
			lastError = strings.TrimSpace(sourceErr.Error())
			w.publishRuntime(folder, RuntimeBackpressure{
				PendingEvents: len(pending),
				PendingBatch:  len(pending) > 0,
				LastError:     lastError,
			})
			stopDebounce()
			return
		case change, ok := <-events:
			if !ok {
				events = nil
				if errs == nil {
					stopDebounce()
					return
				}
				continue
			}
			normalized, ok := normalizeWatcherChange(folder, change)
			if !ok {
				continue
			}
			mergePendingChange(pending, normalized)
			if len(pending) == 0 {
				stopDebounce()
			} else {
				resetDebounce()
			}
			w.publishRuntime(folder, RuntimeBackpressure{
				PendingEvents: len(pending),
				PendingBatch:  len(pending) > 0,
				LastError:     lastError,
			})
		case <-debounceCh:
			stopDebounce()
			if len(pending) == 0 {
				continue
			}

			batch := sortedPendingChanges(pending)
			pending = map[string]WatcherChange{}
			w.publishRuntime(folder, RuntimeBackpressure{
				PendingEvents: 0,
				PendingBatch:  false,
				InFlight:      true,
				LastError:     lastError,
			})

			processErr := w.processor.ProcessBatch(ctx, folder, batch)
			if processErr != nil && !errors.Is(processErr, context.Canceled) {
				lastError = strings.TrimSpace(processErr.Error())
			} else {
				lastError = ""
			}
			w.publishRuntime(folder, RuntimeBackpressure{
				PendingEvents: 0,
				PendingBatch:  false,
				InFlight:      false,
				LastError:     lastError,
			})
		}
	}
}

func (w *EmbeddedWorker) publishRuntime(folder string, snapshot RuntimeBackpressure) {
	if w.sink == nil {
		return
	}
	w.sink.PublishRuntimeBackpressure(folder, snapshot)
}

func (w *EmbeddedWorker) clearRuntime(folder string) {
	if w.sink == nil {
		return
	}
	w.sink.ClearRuntimeBackpressure(folder)
}

func normalizeWatcherChange(folder string, change WatcherChange) (WatcherChange, bool) {
	changeType := change.ChangeType
	if changeType != ChangeAdded && changeType != ChangeModified && changeType != ChangeDeleted {
		return WatcherChange{}, false
	}

	rawPath := strings.TrimSpace(change.Path)
	if rawPath == "" {
		return WatcherChange{}, false
	}
	if !filepath.IsAbs(rawPath) {
		rawPath = filepath.Join(folder, rawPath)
	}

	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		return WatcherChange{}, false
	}

	root := normalizeWatcherPath(folder)
	normalizedPath := normalizeWatcherPath(absPath)

	relPath, err := filepath.Rel(root, normalizedPath)
	if err != nil {
		return WatcherChange{}, false
	}
	if relPath == "." {
		return WatcherChange{}, false
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return WatcherChange{}, false
	}
	if hasHiddenPathSegment(relPath) {
		return WatcherChange{}, false
	}

	return WatcherChange{
		ChangeType: changeType,
		Path:       normalizedPath,
		OldHash:    strings.TrimSpace(change.OldHash),
	}, true
}

func mergePendingChange(pending map[string]WatcherChange, next WatcherChange) {
	current, ok := pending[next.Path]
	if !ok {
		pending[next.Path] = next
		return
	}

	mergedType, keep := collapseChangeTypes(current.ChangeType, next.ChangeType)
	if !keep {
		delete(pending, next.Path)
		return
	}

	oldHash := strings.TrimSpace(current.OldHash)
	if oldHash == "" {
		oldHash = strings.TrimSpace(next.OldHash)
	}
	pending[next.Path] = WatcherChange{
		ChangeType: mergedType,
		Path:       next.Path,
		OldHash:    oldHash,
	}
}

func collapseChangeTypes(current, next ChangeType) (ChangeType, bool) {
	switch current {
	case ChangeAdded:
		switch next {
		case ChangeDeleted:
			return "", false
		case ChangeAdded, ChangeModified:
			return ChangeAdded, true
		default:
			return next, true
		}
	case ChangeModified:
		switch next {
		case ChangeDeleted:
			return ChangeDeleted, true
		case ChangeAdded, ChangeModified:
			return ChangeModified, true
		default:
			return next, true
		}
	case ChangeDeleted:
		switch next {
		case ChangeAdded:
			return ChangeModified, true
		case ChangeDeleted, ChangeModified:
			return ChangeDeleted, true
		default:
			return next, true
		}
	default:
		return next, true
	}
}

func sortedPendingChanges(pending map[string]WatcherChange) []WatcherChange {
	changes := make([]WatcherChange, 0, len(pending))
	for _, change := range pending {
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].ChangeType < changes[j].ChangeType
		}
		return changes[i].Path < changes[j].Path
	})
	return changes
}

func newLifecycleBatchProcessor(controller Controller) BatchProcessor {
	lifecycle, ok := controller.(ReindexLifecycle)
	if !ok || lifecycle == nil {
		return BatchProcessorFunc(func(context.Context, string, []WatcherChange) error {
			return nil
		})
	}

	return BatchProcessorFunc(func(_ context.Context, folder string, _ []WatcherChange) error {
		repoID := localRepoIDForFolder(folder)
		lifecycle.MarkReindexStart(repoID)
		lifecycle.MarkReindexDone(repoID)
		return nil
	})
}

func localRepoIDForFolder(path string) string {
	resolvedPath := normalizeWatcherPath(path)
	base := filepath.Base(resolvedPath)
	digest := sha1.Sum([]byte(resolvedPath))
	hashPrefix := hex.EncodeToString(digest[:])[:8]
	return fmt.Sprintf("local/%s-%s", base, hashPrefix)
}

func normalizeEmbeddedFolders(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, errors.New("watcher requires at least one path")
	}

	normalized := make([]string, 0, len(paths))
	seen := map[string]struct{}{}

	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}

		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve watcher path %q: %w", path, err)
		}
		resolved := absolute
		if evaluated, evalErr := filepath.EvalSymlinks(absolute); evalErr == nil && strings.TrimSpace(evaluated) != "" {
			resolved = evaluated
		}
		resolved = normalizeWatcherPath(resolved)

		info, statErr := os.Stat(resolved)
		if statErr != nil {
			return nil, fmt.Errorf("watcher path %q is invalid: %w", path, statErr)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("watcher path %q must be a directory", path)
		}

		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		normalized = append(normalized, resolved)
	}

	if len(normalized) == 0 {
		return nil, errors.New("watcher requires at least one valid path")
	}
	return normalized, nil
}

func hasHiddenPathSegment(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
