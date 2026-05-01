package telemetry

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrSnapshotNotFound = errors.New("telemetry snapshot not found")

const defaultMaxPendingCallEvents = 2048

// SnapshotStore persists cumulative telemetry snapshots.
type SnapshotStore interface {
	LoadLatestSnapshot(ctx context.Context) (PersistedCumulativeSnapshot, error)
	SaveSnapshot(ctx context.Context, snapshot PersistedCumulativeSnapshot) error
}

// CallEventStore persists per-call telemetry history.
type CallEventStore interface {
	SaveCallEvents(ctx context.Context, events []PersistedCallEvent) error
}

// RuntimeConfig configures tracker persistence behavior.
type RuntimeConfig struct {
	Pricing               map[string]Pricing
	PricingProfileVersion string
	Store                 SnapshotStore
	SnapshotInterval      time.Duration
	Now                   func() time.Time
}

// Runtime combines the in-memory tracker with periodic persistence.
type Runtime struct {
	tracker *Tracker
	store   SnapshotStore
	now     func() time.Time
	events  CallEventStore
	loader  CallEventLoader
	version string

	flushMu sync.Mutex
	stateMu sync.Mutex

	interval             time.Duration
	cancel               context.CancelFunc
	done                 chan struct{}
	hasPersisted         bool
	lastRevision         uint64
	pendingCallEvents    []PersistedCallEvent
	maxPendingCallEvents int

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
		tracker:              NewTracker(cfg.Pricing, now),
		store:                cfg.Store,
		now:                  now,
		version:              cfg.PricingProfileVersion,
		interval:             cfg.SnapshotInterval,
		maxPendingCallEvents: defaultMaxPendingCallEvents,
	}
	if eventStore, ok := cfg.Store.(CallEventStore); ok {
		runtime.events = eventStore
	}
	if loader, ok := cfg.Store.(CallEventLoader); ok {
		runtime.loader = loader
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

	call := r.tracker.RecordCall(record)
	r.enqueueCallEvent(call)
	return call
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

// QueryTrends aggregates retained persisted call history for requested windows.
func (r *Runtime) QueryTrends(
	ctx context.Context,
	query TrendQuery,
) (map[string]TrendWindowSnapshot, error) {
	if r == nil {
		return map[string]TrendWindowSnapshot{}, nil
	}

	now := query.Now.UTC()
	if now.IsZero() {
		now = r.now().UTC()
	}

	normalizedQuery := TrendQuery{
		Windows: query.Windows,
		Now:     now,
	}

	pricing := map[string]Pricing{}
	if r.tracker != nil {
		pricing = r.tracker.pricing
	}
	if r.loader == nil {
		return aggregateTrendWindows(nil, normalizedQuery, pricing)
	}

	events, err := r.loader.LoadCallEvents(ctx, earliestTrendWindowStart(normalizedQuery))
	if err != nil {
		return nil, err
	}
	return aggregateTrendWindows(events, normalizedQuery, pricing)
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
	pendingCallEvents := append([]PersistedCallEvent(nil), r.pendingCallEvents...)
	r.pendingCallEvents = nil
	r.stateMu.Unlock()

	if len(pendingCallEvents) == 0 && hasPersisted && revision == lastRevision {
		return nil
	}

	if len(pendingCallEvents) > 0 && r.events != nil {
		if err := r.events.SaveCallEvents(ctx, pendingCallEvents); err != nil {
			r.requeueCallEvents(pendingCallEvents)
			return err
		}
	}

	if hasPersisted && revision == lastRevision {
		return nil
	}

	snapshot := PersistedCumulativeSnapshot{
		CapturedAt:            r.now().UTC(),
		PricingProfileVersion: r.version,
		Cumulative:            r.tracker.CumulativeSnapshot(),
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

func (r *Runtime) enqueueCallEvent(call CallSnapshot) {
	if r == nil || r.events == nil {
		return
	}

	event := PersistedCallEvent{
		CapturedAt:            call.FinishedAt.UTC(),
		PricingProfileVersion: r.version,
		Call:                  call,
	}
	if event.CapturedAt.IsZero() {
		event.CapturedAt = r.now().UTC()
	}

	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	r.pendingCallEvents = append(r.pendingCallEvents, event)
	if overflow := len(r.pendingCallEvents) - r.maxPendingCallEvents; overflow > 0 {
		// Bound in-memory growth if persistence is degraded; cumulative snapshots
		// still preserve long-range totals even when the oldest pending events roll off.
		r.pendingCallEvents = append([]PersistedCallEvent(nil), r.pendingCallEvents[overflow:]...)
	}
}

func (r *Runtime) requeueCallEvents(events []PersistedCallEvent) {
	if r == nil || len(events) == 0 {
		return
	}

	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	merged := make([]PersistedCallEvent, 0, len(events)+len(r.pendingCallEvents))
	merged = append(merged, events...)
	merged = append(merged, r.pendingCallEvents...)
	if overflow := len(merged) - r.maxPendingCallEvents; overflow > 0 {
		merged = append([]PersistedCallEvent(nil), merged[overflow:]...)
	}
	r.pendingCallEvents = merged
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
