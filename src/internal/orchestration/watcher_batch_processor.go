package orchestration

import (
	"context"
	"errors"
	"strings"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/watcher"
)

// WatcherBatchProcessor returns a watcher batch processor that routes file
// change batches into index_folder incremental tool execution.
func (s *Service) WatcherBatchProcessor() watcher.BatchProcessor {
	return watcher.BatchProcessorFunc(func(ctx context.Context, folder string, changes []watcher.WatcherChange) error {
		folder = strings.TrimSpace(folder)
		if folder == "" {
			return errors.New("watch batch folder is required")
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		changedPaths := make([]map[string]any, 0, len(changes))
		for _, change := range changes {
			path := strings.TrimSpace(change.Path)
			if path == "" {
				continue
			}
			changedPaths = append(changedPaths, map[string]any{
				"change_type": string(change.ChangeType),
				"path":        path,
				"old_hash":    strings.TrimSpace(change.OldHash),
			})
		}

		args := map[string]any{
			"path":        folder,
			"incremental": true,
		}
		if len(changedPaths) > 0 {
			args["changed_paths"] = changedPaths
		}

		payload := s.CallTool(ctx, "index_folder", args)
		if err := ctx.Err(); err != nil {
			return err
		}
		if toolSucceeded(payload) {
			return nil
		}
		return errors.New(reindexFailureMessage(payload))
	})
}
