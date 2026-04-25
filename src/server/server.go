package server

import (
	"context"
	"io"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/orchestration"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/transport/mcp"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/watcher"
)

type serverOptions struct {
	cfg             config.Config
	watcher         watcher.Controller
	repoAcquirer    indexing.RepoAcquirer
	repoAcquirerSet bool
}

// Option customizes server construction.
type Option func(opts *serverOptions)

// WithServerInfo overrides the server name/version reported in initialize.
func WithServerInfo(name, version string) Option {
	return func(opts *serverOptions) {
		if name != "" {
			opts.cfg.ServerName = name
		}
		if version != "" {
			opts.cfg.ServerVersion = version
		}
	}
}

// WithWatcher injects a shared watcher controller instance.
func WithWatcher(controller watcher.Controller) Option {
	return func(opts *serverOptions) {
		opts.watcher = controller
	}
}

// WithRepoAcquirer injects a repo acquirer implementation.
// Passing nil explicitly disables default remote index_repo acquisition wiring.
func WithRepoAcquirer(acquirer indexing.RepoAcquirer) Option {
	return func(opts *serverOptions) {
		opts.repoAcquirer = acquirer
		opts.repoAcquirerSet = true
	}
}

// Server is the public MCP server entrypoint for in-process wiring.
type Server struct {
	inner   *mcp.Server
	service *orchestration.Service
}

// New wires config, orchestration, and stdio transport.
func New(in io.Reader, out io.Writer, optionFns ...Option) *Server {
	opts := serverOptions{
		cfg: config.MustLoad(),
	}
	for _, option := range optionFns {
		option(&opts)
	}

	deps := orchestration.Dependencies{}
	if indexStore, err := storage.NewSQLiteIndexStore(opts.cfg.StoragePath); err == nil {
		deps.IndexStore = indexStore
	}
	if opts.watcher != nil {
		deps.Watcher = opts.watcher
	} else {
		deps.Watcher = watcher.NewStateControllerWithStoragePath(opts.cfg.StoragePath)
	}
	if opts.repoAcquirerSet {
		deps.RepoAcquirer = opts.repoAcquirer
	} else {
		if acquirer := indexing.NewGitHubRepoAcquirerFromEnv(); acquirer != nil {
			deps.RepoAcquirer = acquirer
		}
	}

	service := orchestration.New(opts.cfg, deps)
	return &Server{
		inner:   mcp.NewServer(in, out, service, opts.cfg),
		service: service,
	}
}

// Serve runs the MCP stdio loop until context cancellation or EOF.
func (s *Server) Serve(ctx context.Context) error {
	return s.inner.Serve(ctx)
}

// WatcherBatchProcessor exposes an index_folder-backed batch processor for
// embedded watcher runtime wiring.
func (s *Server) WatcherBatchProcessor() watcher.BatchProcessor {
	if s == nil || s.service == nil {
		return nil
	}
	return s.service.WatcherBatchProcessor()
}

// ToolNames returns currently enabled tool names in deterministic order.
func ToolNames(optionFns ...Option) []string {
	opts := serverOptions{
		cfg: config.MustLoad(),
	}
	for _, option := range optionFns {
		option(&opts)
	}

	service := orchestration.New(opts.cfg, orchestration.Dependencies{})
	listed := service.ListTools()
	names := make([]string, 0, len(listed))
	for _, tool := range listed {
		names = append(names, tool.Name)
	}
	return names
}
