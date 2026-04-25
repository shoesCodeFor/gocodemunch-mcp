package watcher

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	defaultDebounceInterval = 200 * time.Millisecond
	defaultPollInterval     = 200 * time.Millisecond
	defaultEventBufferSize  = 128
)

type pollingEventSource struct {
	step time.Duration
}

type fileSnapshot struct {
	size         int64
	modUnixNanos int64
}

// NewPollingEventSource builds a dependency-free filesystem event source.
func NewPollingEventSource(step time.Duration) EventSource {
	if step <= 0 {
		step = defaultPollInterval
	}
	return &pollingEventSource{
		step: step,
	}
}

func (s *pollingEventSource) Open(ctx context.Context, folder string) (<-chan WatcherChange, <-chan error, error) {
	root := normalizeWatcherPath(folder)
	current, err := scanFolderSnapshot(root)
	if err != nil {
		return nil, nil, fmt.Errorf("scan watcher folder %s: %w", root, err)
	}

	events := make(chan WatcherChange, defaultEventBufferSize)
	errs := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errs)

		ticker := time.NewTicker(s.step)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				next, scanErr := scanFolderSnapshot(root)
				if scanErr != nil {
					select {
					case errs <- fmt.Errorf("scan watcher folder %s: %w", root, scanErr):
					default:
					}
					return
				}

				changes := diffFolderSnapshots(current, next)
				current = next
				for _, change := range changes {
					select {
					case <-ctx.Done():
						return
					case events <- change:
					}
				}
			}
		}
	}()

	return events, errs, nil
}

func scanFolderSnapshot(root string) (map[string]fileSnapshot, error) {
	snapshot := map[string]fileSnapshot{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		cleanPath := normalizeWatcherPath(path)
		if cleanPath == root {
			return nil
		}

		relPath, err := filepath.Rel(root, cleanPath)
		if err != nil {
			return nil
		}
		if relPath == "." {
			return nil
		}
		if hasHiddenPathSegment(relPath) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return nil
		}
		snapshot[cleanPath] = fileSnapshot{
			size:         info.Size(),
			modUnixNanos: info.ModTime().UnixNano(),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func diffFolderSnapshots(before, after map[string]fileSnapshot) []WatcherChange {
	changes := make([]WatcherChange, 0)

	for path, next := range after {
		prev, exists := before[path]
		if !exists {
			changes = append(changes, WatcherChange{
				ChangeType: ChangeAdded,
				Path:       path,
			})
			continue
		}
		if prev.size != next.size || prev.modUnixNanos != next.modUnixNanos {
			changes = append(changes, WatcherChange{
				ChangeType: ChangeModified,
				Path:       path,
			})
		}
	}

	for path := range before {
		if _, exists := after[path]; exists {
			continue
		}
		changes = append(changes, WatcherChange{
			ChangeType: ChangeDeleted,
			Path:       path,
		})
	}

	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].ChangeType < changes[j].ChangeType
		}
		return changes[i].Path < changes[j].Path
	})
	return changes
}

func normalizeWatcherPath(path string) string {
	cleanPath := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleanPath)
	}
	return cleanPath
}
