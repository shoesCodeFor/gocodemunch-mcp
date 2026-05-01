package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/orchestration"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/orchestration/embeddings"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
	vectorqdrant "github.com/jgravelle/gocodemunch-mcp/src/internal/storage/vector/qdrant"
	vectorsqlite "github.com/jgravelle/gocodemunch-mcp/src/internal/storage/vector/sqlite"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/telemetry"
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

	vectorBackend any
	closeOnce     sync.Once
	closeErr      error
}

// New wires config, orchestration, and stdio transport.
func New(in io.Reader, out io.Writer, optionFns ...Option) *Server {
	opts := serverOptions{
		cfg: config.MustLoad(),
	}
	for _, option := range optionFns {
		option(&opts)
	}

	deps, err := buildDependencies(opts)
	if err != nil {
		panic(fmt.Errorf("server startup dependency wiring failed: %w", err))
	}

	service := orchestration.New(opts.cfg, deps)
	return &Server{
		inner:         mcp.NewServer(in, out, service, opts.cfg),
		service:       service,
		vectorBackend: deps.VectorBackend,
	}
}

func buildDependencies(opts serverOptions) (orchestration.Dependencies, error) {
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

	vectorBackend, err := buildVectorBackend(opts.cfg)
	if err != nil {
		return deps, err
	}
	deps.VectorBackend = vectorBackend

	embedder, err := buildEmbedder(opts.cfg)
	if err != nil {
		closeIfPossible(vectorBackend)
		return deps, err
	}
	deps.Embedder = embedder

	telemetryRuntime, err := buildTelemetryRuntime(opts.cfg)
	if err != nil {
		closeIfPossible(vectorBackend)
		return deps, err
	}
	deps.Telemetry = telemetryRuntime

	return deps, nil
}

func buildVectorBackend(cfg config.Config) (indexing.VectorBackend, error) {
	backend := strings.ToLower(strings.TrimSpace(cfg.VectorBackend))
	if backend == "" {
		backend = "sqlite"
	}

	switch backend {
	case "sqlite":
		adapter, err := vectorsqlite.NewAdapter(cfg.StoragePath)
		if err != nil {
			return nil, fmt.Errorf("initialize sqlite vector backend: %w", err)
		}
		return adapter, nil
	case "qdrant":
		adapter, err := vectorqdrant.NewAdapter(
			cfg.QdrantURL,
			cfg.QdrantAPIKey,
			cfg.QdrantCollection,
		)
		if err != nil {
			return nil, fmt.Errorf("initialize qdrant vector backend: %w", err)
		}
		return adapter, nil
	default:
		return nil, fmt.Errorf(
			"unsupported vector backend %q (set VECTOR_BACKEND to one of: sqlite, qdrant)",
			cfg.VectorBackend,
		)
	}
}

func buildEmbedder(cfg config.Config) (indexing.Embedder, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.EmbeddingProvider))
	if provider == "" {
		provider = "ollama"
	}
	if cfg.VectorQueryTimeoutMS < 0 {
		return nil, fmt.Errorf(
			"vector query timeout must be non-negative (got %dms)",
			cfg.VectorQueryTimeoutMS,
		)
	}

	switch provider {
	case "ollama":
		embedder, err := embeddings.NewOllamaEmbedder(
			cfg.OllamaBaseURL,
			cfg.EmbeddingModel,
			time.Duration(cfg.VectorQueryTimeoutMS)*time.Millisecond,
		)
		if err != nil {
			return nil, fmt.Errorf("initialize ollama embedder: %w", err)
		}
		return embedder, nil
	case "vllm":
		embedder, err := embeddings.NewVLLMEmbedder(
			cfg.VLLMBaseURL,
			cfg.VLLMModel,
			cfg.VLLMAPIKey,
			time.Duration(cfg.VectorQueryTimeoutMS)*time.Millisecond,
		)
		if err != nil {
			return nil, fmt.Errorf("initialize vllm embedder: %w", err)
		}
		return embedder, nil
	default:
		return nil, fmt.Errorf(
			"unsupported embedding provider %q (set EMBEDDING_PROVIDER to one of: ollama, vllm)",
			cfg.EmbeddingProvider,
		)
	}
}

func buildTelemetryRuntime(cfg config.Config) (*telemetry.Runtime, error) {
	if !cfg.SavingsTelemetryEnabled {
		return nil, nil
	}

	store, err := storage.NewSQLiteTelemetryStore(cfg.StoragePath)
	if err != nil {
		return nil, fmt.Errorf("initialize savings telemetry store: %w", err)
	}

	runtime, err := telemetry.NewRuntime(telemetry.RuntimeConfig{
		Pricing:          telemetryPricing(cfg.SavingsCompetitorPricing),
		Store:            store,
		SnapshotInterval: time.Duration(cfg.SavingsSnapshotIntervalMS) * time.Millisecond,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize savings telemetry runtime: %w", err)
	}
	return runtime, nil
}

func telemetryPricing(
	pricing map[string]config.SavingsCompetitorPricing,
) map[string]telemetry.Pricing {
	converted := make(map[string]telemetry.Pricing, len(pricing))
	for competitor, value := range pricing {
		converted[competitor] = telemetry.Pricing{
			InputUSDPerMTok:  value.InputUSDPerMTok,
			OutputUSDPerMTok: value.OutputUSDPerMTok,
		}
	}
	return converted
}

func closeIfPossible(candidate any) {
	type closer interface {
		Close() error
	}
	if closable, ok := candidate.(closer); ok {
		_ = closable.Close()
	}
}

func closeWithError(candidate any) error {
	type closer interface {
		Close() error
	}
	if closable, ok := candidate.(closer); ok {
		return closable.Close()
	}
	return nil
}

// Serve runs the MCP stdio loop until context cancellation or EOF.
func (s *Server) Serve(ctx context.Context) error {
	return s.inner.Serve(ctx)
}

// Close flushes runtime telemetry and closes closeable dependencies.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}

	s.closeOnce.Do(func() {
		s.closeErr = errors.Join(
			s.service.Close(),
			closeWithError(s.vectorBackend),
		)
	})
	return s.closeErr
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
