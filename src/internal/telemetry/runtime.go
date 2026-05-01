package telemetry

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrSnapshotNotFound = errors.New("telemetry snapshot not found")

// SnapshotStore persists cumulative telemetry snapshots.
type SnapshotStore interface {
	LoadLatestSnapshot(ctx context.Context) (PersistedCumulativeSnapshot, error)
	SaveSnapshot(ctx context.Context, snapshot PersistedCumulativeSnapshot) error
}

// RuntimeConfig configures tracker persistence behavior.
type RuntimeConfig struct {
	Pricing          map[string]Pricing
	Store            SnapshotStore
	SnapshotInterval time.Duration
	Now              func() time.Time
}

// Runtime combines the in-memory tracker with periodic persistence.
type Runtime struct {
	tracker *Tracker
	store   SnapshotStore
	now     func() time.Time

	flushMu sync.Mutex
	stateMu sync.Mutex

	interval     time.Duration
	cancel       context.CancelFunc
	done         chan struct{}
	hasPersisted bool
	lastRevision uint64

	closeOnce sync.Once
	closeErr  error
}

// NewRuntime constructs and starts a persistence runtime when configured.
func NewRuntime(cfg RuntimeConfig) (*Runtime, error) {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	runtime := &Runtime{
		tracker:  NewTracker(cfg.Pricing, now),
		store:    cfg.Store,
		now:      now,
		interval: cfg.SnapshotInterval,
	}

	if cfg.Store != nil {
		snapshot, err := cfg.Store.LoadLatestSnapshot(context.Background())
		if err != nil && !errors.Is(err, ErrSnapshotNotFound) {
			return nil, err
		}
		if err == nil {
			runtime.tracker.RestoreCumulative(snapshot.Cumulative)
		}
	}

	if cfg.Store != nil && cfg.SnapshotInterval > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		runtime.cancel = cancel
		runtime.done = make(chan struct{})
		go runtime.run(ctx)
	}

	return runtime, nil
}

// Tracker returns the underlying in-memory collector.
func (r *Runtime) Tracker() *Tracker {
	if r == nil {
		return nil
	}
	return r.tracker
}

// RecordCall delegates to the underlying tracker.
func (r *Runtime) RecordCall(record CallRecord) CallSnapshot {
	if r == nil || r.tracker == nil {
		return CallSnapshot{}
	}
	return r.tracker.RecordCall(record)
}

// SessionSnapshot delegates to the underlying tracker.
func (r *Runtime) SessionSnapshot() SessionSnapshot {
	if r == nil || r.tracker == nil {
		return SessionSnapshot{}
	}
	return r.tracker.SessionSnapshot()
}

// CumulativeSnapshot delegates to the underlying tracker.
func (r *Runtime) CumulativeSnapshot() CumulativeSnapshot {
	if r == nil || r.tracker == nil {
		return CumulativeSnapshot{}
	}
	return r.tracker.CumulativeSnapshot()
}

// Flush persists the latest cumulative snapshot when needed.
func (r *Runtime) Flush(ctx context.Context) error {
	if r == nil || r.store == nil || r.tracker == nil {
		return nil
	}

	r.flushMu.Lock()
	defer r.flushMu.Unlock()

	revision := r.tracker.currentRevision()

	r.stateMu.Lock()
	hasPersisted := r.hasPersisted
	lastRevision := r.lastRevision
	r.stateMu.Unlock()

	if hasPersisted && revision == lastRevision {
		return nil
	}

	snapshot := PersistedCumulativeSnapshot{
		CapturedAt: r.now().UTC(),
		Cumulative: r.tracker.CumulativeSnapshot(),
	}
	if err := r.store.SaveSnapshot(ctx, snapshot); err != nil {
		return err
	}

	r.stateMu.Lock()
	r.hasPersisted = true
	r.lastRevision = revision
	r.stateMu.Unlock()
	return nil
}

// Close stops periodic persistence and flushes one final snapshot.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}

	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.done != nil {
			<-r.done
		}
		r.closeErr = r.Flush(context.Background())
	})

	return r.closeErr
}

func (r *Runtime) run(ctx context.Context) {
	defer close(r.done)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.Flush(context.Background())
		}
	}
}
