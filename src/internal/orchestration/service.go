package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/security"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/telemetry"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/watcher"
)

var strictFreshnessExcludedTools = map[string]struct{}{
	"list_repos":        {},
	"resolve_repo":      {},
	"get_session_stats": {},
	"wait_for_fresh":    {},
	"index_repo":        {},
	"index_folder":      {},
	"index_file":        {},
	"invalidate_cache":  {},
}

var nonRepoFreshnessMetaExcludedTools = map[string]struct{}{
	"list_repos":        {},
	"resolve_repo":      {},
	"get_session_stats": {},
	"index_repo":        {},
	"index_folder":      {},
}

// Dependencies captures cross-boundary contracts required by orchestration.
type Dependencies struct {
	IndexStore      storage.IndexStore
	ContentCache    storage.ContentCache
	ParserExtractor indexing.ParserExtractor
	Summarizer      indexing.SummarizerProvider
	Embedder        indexing.Embedder
	VectorBackend   indexing.VectorBackend
	RepoAcquirer    indexing.RepoAcquirer
	Watcher         watcher.Controller
	PathGuard       security.PathGuard
	RegexGuard      security.RegexGuard
	SecretFilter    security.SecretFilter
	Telemetry       telemetry.Collector
}

// Service owns tool registration, validation, and call routing.
type Service struct {
	cfg   config.Config
	deps  Dependencies
	tools map[string]Tool
	order []string

	reindexMu   sync.Mutex
	reindexLock map[string]*sync.Mutex
}

// New builds an orchestration service with all default tool definitions.
func New(cfg config.Config, deps Dependencies) *Service {
	svc := &Service{
		cfg:         cfg,
		deps:        deps,
		tools:       map[string]Tool{},
		order:       make([]string, 0, 27),
		reindexLock: map[string]*sync.Mutex{},
	}

	for _, tool := range ToolDefinitionsForLanguages(cfg.Languages) {
		if cfg.IsToolDisabled(tool.Name) {
			continue
		}
		svc.tools[tool.Name] = tool
		svc.order = append(svc.order, tool.Name)
	}

	svc.bindImplementedHandlers()

	return svc
}

// ListTools returns registered tools in deterministic order.
func (s *Service) ListTools() []Tool {
	result := make([]Tool, 0, len(s.order))
	for _, name := range s.order {
		tool := s.tools[name]
		result = append(result, Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: deepCopyMap(tool.InputSchema),
		})
	}
	return result
}

// CallTool executes one tool call and returns a JSON-serializable payload.
func (s *Service) CallTool(ctx context.Context, name string, arguments map[string]any) map[string]any {
	tool, ok := s.tools[name]
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Unknown tool: %s", name)}
	}

	coerced := CoerceArguments(arguments, tool.InputSchema)
	if err := ValidateArguments(coerced, tool.InputSchema); err != nil {
		return map[string]any{"error": fmt.Sprintf("Input validation error: %s", err.Error())}
	}

	startedAt := time.Now().UTC()
	callCtx, cancel := s.newRequestContext(ctx)
	defer cancel()

	if err := callCtx.Err(); err != nil {
		return internalToolErrorPayload(name)
	}

	s.awaitFreshnessIfStrict(callCtx, name, coerced)
	if err := callCtx.Err(); err != nil {
		return internalToolErrorPayload(name)
	}

	repoScopedCfg := s.repoScopedConfig(callCtx, coerced)
	repoArg := strings.TrimSpace(stringArg(coerced, "repo", ""))
	if repoArg != "" {
		if _, disabled := repoScopedCfg.Disabled[name]; disabled {
			return projectToolDisabledPayload(name)
		}
	}

	payload, err := tool.Handler(callCtx, coerced)
	if err != nil {
		return internalToolErrorPayload(name)
	}

	s.attachFreshnessMeta(callCtx, name, coerced, payload)
	callSnapshot, sessionSnapshot, cumulativeSnapshot := s.recordTelemetry(name, coerced, payload, startedAt)
	payload = s.applySavingsMeta(payload, callSnapshot, cumulativeSnapshot)
	if name == "get_session_stats" {
		payload = s.applySessionStatsPayload(payload, sessionSnapshot, cumulativeSnapshot)
	}

	return s.applyMetaPolicy(payload)
}

func internalToolErrorPayload(name string) map[string]any {
	return map[string]any{"error": fmt.Sprintf("Internal error processing %s", name)}
}

func projectToolDisabledPayload(name string) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf(
			"Tool '%s' is disabled in this project's configuration. "+
				"Project-level tool disabling is set via the 'disabled_tools' key in the .jcodemunch.jsonc file. "+
				"Remove '%s' from 'disabled_tools' to re-enable.",
			name,
			name,
		),
	}
}

func (s *Service) newRequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeoutMS := s.cfg.RequestTimeoutMS
	if timeoutMS <= 0 {
		return ctx, func() {}
	}

	timeout := time.Duration(timeoutMS) * time.Millisecond
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ctx, func() {}
		}
		if remaining < timeout {
			timeout = remaining
		}
	}

	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func (s *Service) awaitFreshnessIfStrict(ctx context.Context, toolName string, arguments map[string]any) {
	if s.cfg.FreshnessMode != "strict" {
		return
	}
	if _, excluded := strictFreshnessExcludedTools[toolName]; excluded {
		return
	}

	controller := s.deps.Watcher
	if controller == nil {
		return
	}

	repo := strings.TrimSpace(stringArg(arguments, "repo", ""))
	if repo == "" {
		return
	}

	_, _ = controller.WaitForFresh(ctx, repo, 500)
}

func (s *Service) attachFreshnessMeta(ctx context.Context, toolName string, arguments map[string]any, payload map[string]any) {
	controller := s.deps.Watcher
	if controller == nil {
		return
	}
	if payload == nil {
		return
	}

	meta, _ := payload["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		payload["_meta"] = meta
	}

	repo := strings.TrimSpace(stringArg(arguments, "repo", ""))
	if repo != "" {
		status, err := controller.Query(ctx, repo)
		if err != nil {
			return
		}
		meta["index_stale"] = status.IndexStale
		meta["reindex_in_progress"] = status.ReindexInProgress
		if status.StaleSinceMS != nil {
			meta["stale_since_ms"] = *status.StaleSinceMS
		} else {
			meta["stale_since_ms"] = nil
		}
		if strings.TrimSpace(status.LastError) != "" && status.ReindexFailures >= 2 {
			meta["reindex_error"] = status.LastError
			meta["reindex_failures"] = status.ReindexFailures
		}
		return
	}

	if _, excluded := nonRepoFreshnessMetaExcludedTools[toolName]; excluded {
		return
	}

	backpressure := controller.Backpressure(ctx)
	inProgress, _ := backpressure["any_reindex_in_progress"].(bool)
	meta["index_stale"] = inProgress
	meta["reindex_in_progress"] = inProgress
	meta["stale_since_ms"] = nil
}

func (s *Service) applyMetaPolicy(payload map[string]any) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}

	metaFields := s.cfg.MetaFields
	if metaFields == nil {
		if _, ok := payload["_meta"]; !ok {
			payload["_meta"] = map[string]any{}
		}
		return payload
	}

	if len(metaFields) == 0 {
		delete(payload, "_meta")
		return payload
	}

	existingMeta, _ := payload["_meta"].(map[string]any)
	filtered := map[string]any{}
	for _, field := range metaFields {
		if existingMeta == nil {
			continue
		}
		if value, ok := existingMeta[field]; ok {
			filtered[field] = value
		}
	}

	if len(filtered) == 0 {
		delete(payload, "_meta")
		return payload
	}

	payload["_meta"] = filtered
	return payload
}

// Close flushes service-owned lifecycle dependencies.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}

	type closer interface {
		Close() error
	}
	if closable, ok := s.deps.Telemetry.(closer); ok {
		return closable.Close()
	}
	return nil
}

func deepCopyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		switch typed := value.(type) {
		case map[string]any:
			out[key] = deepCopyMap(typed)
		case []any:
			copied := make([]any, len(typed))
			for i := range typed {
				copied[i] = typed[i]
			}
			out[key] = copied
		default:
			out[key] = typed
		}
	}
	return out
}
