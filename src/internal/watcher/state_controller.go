package watcher

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type repoState struct {
	activeReindexes    int
	reindexing         bool
	staleSince         time.Time
	lastError          string
	consecutiveFailure int
	waitCh             chan struct{}
}

// StateController provides in-process reindex state and freshness waits.
// It is intentionally in-memory for parity-lane startup and test determinism.
type StateController struct {
	mu      sync.RWMutex
	watched map[string]struct{}
	states  map[string]*repoState
	locks   *folderLockManager
	runtime map[string]RuntimeBackpressure
}

// NewStateController builds an empty watcher state controller.
func NewStateController() *StateController {
	return NewStateControllerWithStoragePath("")
}

// NewStateControllerWithStoragePath builds a watcher state controller
// with lock files rooted at the configured storage path.
func NewStateControllerWithStoragePath(storagePath string) *StateController {
	lockManager, err := newFolderLockManager(storagePath)
	if err != nil {
		lockManager = nil
	}
	return &StateController{
		watched: map[string]struct{}{},
		states:  map[string]*repoState{},
		locks:   lockManager,
		runtime: map[string]RuntimeBackpressure{},
	}
}

// Start marks a repo as watched. Repeated calls are a no-op.
func (c *StateController) Start(_ context.Context, repo string) error {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return errors.New("repo is required")
	}

	c.mu.RLock()
	_, alreadyWatched := c.watched[repo]
	c.mu.RUnlock()
	if alreadyWatched {
		return nil
	}

	if c.locks != nil {
		acquired, err := c.locks.Acquire(repo)
		if err != nil {
			return err
		}
		if !acquired {
			return nil
		}
	}

	c.mu.Lock()
	c.watched[repo] = struct{}{}
	c.ensureStateLocked(repo)
	c.mu.Unlock()
	return nil
}

// Stop marks a repo as no longer watched. Missing repos are ignored.
func (c *StateController) Stop(_ context.Context, repo string) error {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return errors.New("repo is required")
	}

	c.mu.Lock()
	delete(c.watched, repo)
	c.mu.Unlock()

	if c.locks != nil {
		if err := c.locks.Release(repo); err != nil {
			return err
		}
	}
	return nil
}

// Query returns repo freshness status. Unknown repos are treated as fresh.
func (c *StateController) Query(_ context.Context, repo string) (Status, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return Status{}, errors.New("repo is required")
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	state, ok := c.states[repo]
	if !ok {
		return Status{
			Repo:              repo,
			Fresh:             true,
			IndexStale:        false,
			ReindexInProgress: false,
			StaleSinceMS:      nil,
		}, nil
	}
	return statusFromState(repo, state, false), nil
}

// WaitForFresh blocks until the repo is no longer reindexing, timeout, or cancellation.
func (c *StateController) WaitForFresh(ctx context.Context, repo string, timeoutMS int) (Status, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return Status{}, errors.New("repo is required")
	}
	if timeoutMS < 0 {
		timeoutMS = 0
	}

	c.mu.RLock()
	state, ok := c.states[repo]
	if !ok {
		c.mu.RUnlock()
		return Status{
			Repo:              repo,
			Fresh:             true,
			IndexStale:        false,
			ReindexInProgress: false,
			StaleSinceMS:      nil,
		}, nil
	}
	waitCh := state.waitCh
	snapshot := statusFromState(repo, state, true)
	c.mu.RUnlock()

	if !snapshot.ReindexInProgress {
		return snapshot, nil
	}

	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		return snapshot, context.DeadlineExceeded
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return snapshot, ctx.Err()
	case <-timer.C:
		c.mu.RLock()
		state = c.states[repo]
		timedOut := statusFromState(repo, state, true)
		c.mu.RUnlock()
		return timedOut, context.DeadlineExceeded
	case <-waitCh:
		c.mu.RLock()
		state = c.states[repo]
		fresh := statusFromState(repo, state, true)
		c.mu.RUnlock()
		return fresh, nil
	}
}

// Backpressure exposes a deterministic state summary for observability.
func (c *StateController) Backpressure(_ context.Context) map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	active := 0
	for _, state := range c.states {
		if state.reindexing {
			active++
		}
	}

	pendingEvents := 0
	pendingBatches := 0
	inFlight := 0
	runtimeErrors := 0
	for _, snapshot := range c.runtime {
		pendingEvents += snapshot.PendingEvents
		if snapshot.PendingBatch {
			pendingBatches++
		}
		if snapshot.InFlight {
			inFlight++
		}
		if strings.TrimSpace(snapshot.LastError) != "" {
			runtimeErrors++
		}
	}

	return map[string]any{
		"watched_repo_count":       len(c.watched),
		"active_reindex_count":     active,
		"any_reindex_in_progress":  active > 0,
		"watcher_pending_events":   pendingEvents,
		"watcher_pending_batches":  pendingBatches,
		"watcher_batches_inflight": inFlight,
		"watcher_runtime_errors":   runtimeErrors,
	}
}

// MarkReindexStart records that a repo has entered reindexing state.
func (c *StateController) MarkReindexStart(repo string) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	state := c.ensureStateLocked(repo)
	wasIdle := state.activeReindexes == 0
	state.activeReindexes++
	state.reindexing = true
	state.lastError = ""
	if state.staleSince.IsZero() {
		state.staleSince = time.Now()
	}
	if wasIdle {
		state.waitCh = make(chan struct{})
	}
}

// MarkReindexDone records a successful reindex completion.
func (c *StateController) MarkReindexDone(repo string) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	state := c.ensureStateLocked(repo)
	if state.activeReindexes > 0 {
		state.activeReindexes--
	}
	if state.activeReindexes > 0 {
		state.reindexing = true
		return
	}

	state.activeReindexes = 0
	state.reindexing = false
	state.lastError = ""
	state.consecutiveFailure = 0
	state.staleSince = time.Time{}
	closeSignal(state.waitCh)
}

// MarkReindexFailed records a failed reindex completion.
func (c *StateController) MarkReindexFailed(repo, errMessage string) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	state := c.ensureStateLocked(repo)
	if state.activeReindexes > 0 {
		state.activeReindexes--
	}
	state.lastError = strings.TrimSpace(errMessage)
	state.consecutiveFailure++
	if state.activeReindexes > 0 {
		state.reindexing = true
		if state.staleSince.IsZero() {
			state.staleSince = time.Now()
		}
		return
	}

	state.activeReindexes = 0
	state.reindexing = false
	if state.staleSince.IsZero() {
		state.staleSince = time.Now()
	}
	closeSignal(state.waitCh)
}

func (c *StateController) ensureStateLocked(repo string) *repoState {
	state, ok := c.states[repo]
	if !ok {
		ch := make(chan struct{})
		close(ch)
		state = &repoState{
			waitCh: ch,
		}
		c.states[repo] = state
	}
	return state
}

func statusFromState(repo string, state *repoState, waitSemantics bool) Status {
	if state == nil {
		return Status{
			Repo:              repo,
			Fresh:             true,
			IndexStale:        false,
			ReindexInProgress: false,
		}
	}

	indexStale := state.reindexing || !state.staleSince.IsZero()
	status := Status{
		Repo:              repo,
		Fresh:             !indexStale,
		IndexStale:        indexStale,
		ReindexInProgress: state.reindexing,
	}
	if !state.staleSince.IsZero() {
		staleSinceMS := int64(time.Since(state.staleSince).Milliseconds())
		if staleSinceMS < 0 {
			staleSinceMS = 0
		}
		status.StaleSinceMS = &staleSinceMS
	}

	// Transient first failures keep wait_for_fresh tolerant.
	if waitSemantics && !state.reindexing {
		if state.consecutiveFailure < 2 {
			status.Fresh = true
			return status
		}
	}

	if state.consecutiveFailure >= 2 && state.lastError != "" {
		status.LastError = state.lastError
		status.ReindexFailures = state.consecutiveFailure
		if waitSemantics {
			status.Fresh = false
		}
	}

	return status
}

func closeSignal(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case <-ch:
		return
	default:
		close(ch)
	}
}

// PublishRuntimeBackpressure updates per-folder watcher runtime state.
func (c *StateController) PublishRuntimeBackpressure(folder string, snapshot RuntimeBackpressure) {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return
	}

	c.mu.Lock()
	c.runtime[folder] = snapshot
	c.mu.Unlock()
}

// ClearRuntimeBackpressure removes runtime state for one watched folder.
func (c *StateController) ClearRuntimeBackpressure(folder string) {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return
	}

	c.mu.Lock()
	delete(c.runtime, folder)
	c.mu.Unlock()
}
