package orchestration

import (
	"context"
	"strings"
	"sync"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/watcher"
)

func (s *Service) runWithReindexLifecycle(ctx context.Context, repo, watchFolder string, run func() (map[string]any, error)) (map[string]any, error) {
	repo = strings.TrimSpace(repo)
	watchFolder = strings.TrimSpace(watchFolder)

	controller := s.deps.Watcher
	if controller != nil && watchFolder != "" {
		if err := controller.Start(ctx, watchFolder); err != nil {
			return nil, err
		}
	}

	if repo == "" {
		return run()
	}

	repoMu := s.repoReindexMutex(repo)
	repoMu.Lock()
	defer repoMu.Unlock()

	lifecycle, ok := controller.(watcher.ReindexLifecycle)
	if !ok || lifecycle == nil {
		return run()
	}

	lifecycle.MarkReindexStart(repo)

	payload, err := run()
	if err != nil {
		lifecycle.MarkReindexFailed(repo, err.Error())
		return payload, err
	}

	if toolSucceeded(payload) {
		lifecycle.MarkReindexDone(repo)
		return payload, nil
	}

	lifecycle.MarkReindexFailed(repo, reindexFailureMessage(payload))
	return payload, nil
}

func toolSucceeded(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	if success, ok := payload["success"].(bool); ok {
		return success
	}
	_, hasError := payload["error"]
	return !hasError
}

func (s *Service) repoReindexMutex(repo string) *sync.Mutex {
	s.reindexMu.Lock()
	defer s.reindexMu.Unlock()
	if s.reindexLock == nil {
		s.reindexLock = map[string]*sync.Mutex{}
	}
	if mu, ok := s.reindexLock[repo]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	s.reindexLock[repo] = mu
	return mu
}

func reindexFailureMessage(payload map[string]any) string {
	if payload == nil {
		return "reindex failed"
	}
	if errText, ok := payload["error"].(string); ok {
		errText = strings.TrimSpace(errText)
		if errText != "" {
			return errText
		}
	}
	if message, ok := payload["message"].(string); ok {
		message = strings.TrimSpace(message)
		if message != "" {
			return message
		}
	}
	return "reindex failed"
}
