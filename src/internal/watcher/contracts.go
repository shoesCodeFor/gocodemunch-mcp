package watcher

import "context"

// Status describes watcher freshness state for a repo.
type Status struct {
	Repo              string
	Fresh             bool
	IndexStale        bool
	ReindexInProgress bool
	StaleSinceMS      *int64
	LastError         string
	ReindexFailures   int
}

// Controller owns watcher lifecycle and freshness querying.
type Controller interface {
	Start(ctx context.Context, repo string) error
	Stop(ctx context.Context, repo string) error
	Query(ctx context.Context, repo string) (Status, error)
	WaitForFresh(ctx context.Context, repo string, timeoutMS int) (Status, error)
	Backpressure(ctx context.Context) map[string]any
}

// ReindexLifecycle marks reindex transitions that drive freshness state.
// Implementations may embed lock/recovery semantics around these signals.
type ReindexLifecycle interface {
	MarkReindexStart(repo string)
	MarkReindexDone(repo string)
	MarkReindexFailed(repo, errMessage string)
}

// ChangeType describes filesystem change semantics consumed by watcher batches.
type ChangeType string

const (
	ChangeAdded    ChangeType = "added"
	ChangeModified ChangeType = "modified"
	ChangeDeleted  ChangeType = "deleted"
)

// WatcherChange is a normalized filesystem event routed into incremental reindexing.
type WatcherChange struct {
	ChangeType ChangeType
	Path       string
	OldHash    string
}

// EventSource streams watcher change events for a folder path.
type EventSource interface {
	Open(ctx context.Context, folder string) (<-chan WatcherChange, <-chan error, error)
}

// BatchProcessor executes one debounced batch for a watched folder.
type BatchProcessor interface {
	ProcessBatch(ctx context.Context, folder string, changes []WatcherChange) error
}

// RuntimeBackpressure captures per-folder watcher runtime state.
type RuntimeBackpressure struct {
	PendingEvents int
	PendingBatch  bool
	InFlight      bool
	LastError     string
}

// RuntimeBackpressureSink accepts runtime backpressure snapshots from workers.
type RuntimeBackpressureSink interface {
	PublishRuntimeBackpressure(folder string, snapshot RuntimeBackpressure)
	ClearRuntimeBackpressure(folder string)
}
