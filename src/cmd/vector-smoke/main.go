package main

import (
	"context"
	"crypto/sha1"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/orchestration/embeddings"
	vectorqdrant "github.com/jgravelle/gocodemunch-mcp/src/internal/storage/vector/qdrant"
	vectorsqlite "github.com/jgravelle/gocodemunch-mcp/src/internal/storage/vector/sqlite"
)

const (
	defaultSmokeNamespace = "vector-smoke/fixtures"
	defaultSmokeQuery     = "How do I parse JSON into a Go struct?"
)

type fixtureChunk struct {
	ID        string
	Path      string
	Language  string
	StartLine int
	EndLine   int
	Text      string
}

var fixtureCorpus = []fixtureChunk{
	{
		ID:        "go-json-decode",
		Path:      "fixtures/go/json_decode.go",
		Language:  "go",
		StartLine: 1,
		EndLine:   16,
		Text:      "Use encoding/json Decoder to parse request bodies into typed Go structs and validate required fields.",
	},
	{
		ID:        "go-http-timeout",
		Path:      "fixtures/go/http_timeout.go",
		Language:  "go",
		StartLine: 1,
		EndLine:   14,
		Text:      "Wrap outbound HTTP calls with context.WithTimeout and return clear timeout errors for retries.",
	},
	{
		ID:        "python-csv-clean",
		Path:      "fixtures/python/csv_clean.py",
		Language:  "python",
		StartLine: 1,
		EndLine:   22,
		Text:      "Load CSV files with pandas, normalize column names, and fill null values before analytics.",
	},
	{
		ID:        "postgres-index",
		Path:      "fixtures/sql/add_index.sql",
		Language:  "sql",
		StartLine: 1,
		EndLine:   8,
		Text:      "Create a PostgreSQL btree index on created_at and status to speed up filtered dashboard queries.",
	},
	{
		ID:        "react-useeffect",
		Path:      "fixtures/web/use_effect.tsx",
		Language:  "typescript",
		StartLine: 1,
		EndLine:   20,
		Text:      "Use React useEffect with dependency arrays to fetch API data only when selected filters change.",
	},
	{
		ID:        "docker-redis",
		Path:      "fixtures/infra/docker_compose.yml",
		Language:  "yaml",
		StartLine: 1,
		EndLine:   15,
		Text:      "Configure docker compose services for web and redis with shared network aliases and restart policies.",
	},
}

func main() {
	os.Exit(runWithArgs(os.Args[1:]))
}

func runWithArgs(args []string) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config validation failed: %v\n", err)
		return 1
	}
	for _, warning := range cfg.Warnings {
		fmt.Fprintf(os.Stderr, "config warning: %s\n", warning)
	}

	defaultTopK := cfg.VectorTopK
	if defaultTopK <= 0 {
		defaultTopK = 5
	}

	flags := flag.NewFlagSet("vector-smoke", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	queryArg := flags.String("query", defaultSmokeQuery, "Semantic query used for retrieval")
	topKArg := flags.Int("top-k", defaultTopK, "How many ranked matches to print")
	namespaceArg := flags.String("namespace", defaultSmokeNamespace, "Vector namespace used for fixture records")
	keepDataArg := flags.Bool("keep-data", false, "Keep temporary vector storage path after completion")

	if err := flags.Parse(args); err != nil {
		return 2
	}

	backend, err := normalizeBackend(cfg.VectorBackend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid vector backend: %v\n", err)
		return 2
	}
	cfg.VectorBackend = backend

	namespace := strings.TrimSpace(*namespaceArg)
	if namespace == "" {
		fmt.Fprintln(os.Stderr, "namespace must be non-empty")
		return 2
	}

	query := strings.TrimSpace(*queryArg)
	if query == "" {
		fmt.Fprintln(os.Stderr, "query must be non-empty")
		return 2
	}

	if *topKArg <= 0 {
		fmt.Fprintf(os.Stderr, "top-k must be positive (got %d)\n", *topKArg)
		return 2
	}

	topK, err := normalizeTopK(*topKArg, len(fixtureCorpus))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid top-k: %v\n", err)
		return 2
	}

	cleanupStorage := func() {}
	if backend == "sqlite" {
		storagePath, cleanup, resolveErr := resolveStoragePath(cfg.StoragePath, *keepDataArg)
		if resolveErr != nil {
			fmt.Fprintf(os.Stderr, "resolve storage path: %v\n", resolveErr)
			return 1
		}
		cleanupStorage = cleanup
		cfg.StoragePath = storagePath
	}
	defer cleanupStorage()

	ctx := context.Background()

	adapter, err := newVectorBackend(cfg, backend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize %s vector backend: %v\n", backend, err)
		return 1
	}
	defer func() {
		if closeErr := closeVectorBackend(adapter); closeErr != nil {
			fmt.Fprintf(os.Stderr, "close %s vector backend: %v\n", backend, closeErr)
		}
	}()

	if _, err := adapter.DeleteNamespace(ctx, indexing.VectorDeleteNamespaceRequest{Namespace: namespace}); err != nil {
		fmt.Fprintf(os.Stderr, "reset fixture namespace %q: %v\n", namespace, err)
		return 1
	}

	embedder, err := newEmbedder(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize embedder: %v\n", err)
		return 1
	}

	corpusTexts := fixtureTexts(fixtureCorpus)
	fixtureEmbeddings, err := embedder.Embed(ctx, corpusTexts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "embed fixture corpus: %v\n", err)
		printProviderHint(cfg)
		return 1
	}

	records, err := buildFixtureRecords(namespace, backend, fixtureCorpus, fixtureEmbeddings)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build fixture records: %v\n", err)
		return 1
	}

	upsertResponse, err := adapter.Upsert(ctx, indexing.VectorUpsertRequest{
		Namespace: namespace,
		Records:   records,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "upsert fixture records: %v\n", err)
		return 1
	}

	queryEmbeddings, err := embedder.Embed(ctx, []string{query})
	if err != nil {
		fmt.Fprintf(os.Stderr, "embed query: %v\n", err)
		printProviderHint(cfg)
		return 1
	}
	if len(queryEmbeddings) != 1 {
		fmt.Fprintf(os.Stderr, "unexpected query embedding count: got %d, want 1\n", len(queryEmbeddings))
		return 1
	}

	queryResponse, err := adapter.Query(ctx, indexing.VectorQueryRequest{
		Namespace: namespace,
		Embedding: queryEmbeddings[0],
		TopK:      topK,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "query vector matches: %v\n", err)
		return 1
	}

	printSmokeSummary(cfg, namespace, query, upsertResponse.Upserted, topK, len(queryResponse.Matches))
	printMatches(queryResponse.Matches)

	return 0
}

func normalizeEmbeddingProvider(rawProvider string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(rawProvider))
	if normalized == "" {
		normalized = "ollama"
	}

	switch normalized {
	case "ollama", "vllm":
		return normalized, nil
	default:
		return "", fmt.Errorf(
			"unsupported embedding provider %q (set EMBEDDING_PROVIDER to one of: ollama, vllm)",
			rawProvider,
		)
	}
}

func normalizeBackend(rawBackend string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(rawBackend))
	if normalized == "" {
		normalized = "sqlite"
	}

	switch normalized {
	case "sqlite", "qdrant":
		return normalized, nil
	default:
		return "", fmt.Errorf(
			"unsupported backend %q (set VECTOR_BACKEND to one of: sqlite, qdrant)",
			rawBackend,
		)
	}
}

func newVectorBackend(
	cfg config.Config,
	backend string,
) (indexing.VectorBackend, error) {
	switch backend {
	case "sqlite":
		adapter, err := vectorsqlite.NewAdapter(cfg.StoragePath)
		if err != nil {
			return nil, err
		}
		return adapter, nil
	case "qdrant":
		adapter, err := vectorqdrant.NewAdapter(
			cfg.QdrantURL,
			cfg.QdrantAPIKey,
			cfg.QdrantCollection,
		)
		if err != nil {
			return nil, err
		}
		return adapter, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q", backend)
	}
}

func newEmbedder(cfg config.Config) (indexing.Embedder, error) {
	provider, err := normalizeEmbeddingProvider(cfg.EmbeddingProvider)
	if err != nil {
		return nil, err
	}

	if cfg.VectorQueryTimeoutMS < 0 {
		return nil, fmt.Errorf(
			"vector query timeout must be non-negative (got %dms)",
			cfg.VectorQueryTimeoutMS,
		)
	}
	timeout := time.Duration(cfg.VectorQueryTimeoutMS) * time.Millisecond

	switch provider {
	case "ollama":
		embedder, buildErr := embeddings.NewOllamaEmbedder(
			cfg.OllamaBaseURL,
			cfg.EmbeddingModel,
			timeout,
		)
		if buildErr != nil {
			return nil, fmt.Errorf("initialize ollama embedder: %w", buildErr)
		}
		return embedder, nil
	case "vllm":
		embedder, buildErr := embeddings.NewVLLMEmbedder(
			cfg.VLLMBaseURL,
			cfg.VLLMModel,
			cfg.VLLMAPIKey,
			timeout,
		)
		if buildErr != nil {
			return nil, fmt.Errorf("initialize vllm embedder: %w", buildErr)
		}
		return embedder, nil
	default:
		return nil, fmt.Errorf("unsupported embedding provider %q", provider)
	}
}

func closeVectorBackend(backend indexing.VectorBackend) error {
	type closer interface {
		Close() error
	}
	if closeable, ok := backend.(closer); ok {
		return closeable.Close()
	}
	return nil
}

func normalizeTopK(requested int, corpusSize int) (int, error) {
	if requested <= 0 {
		return 0, fmt.Errorf("top-k must be positive (got %d)", requested)
	}
	if corpusSize <= 0 {
		return 0, errors.New("fixture corpus must be non-empty")
	}
	if requested > corpusSize {
		return corpusSize, nil
	}
	return requested, nil
}

func resolveStoragePath(configured string, keepData bool) (string, func(), error) {
	trimmed := strings.TrimSpace(configured)
	if trimmed != "" {
		resolved := filepath.Clean(trimmed)
		if err := os.MkdirAll(resolved, 0o755); err != nil {
			return "", func() {}, fmt.Errorf("ensure configured storage path %q: %w", resolved, err)
		}
		return resolved, func() {}, nil
	}

	tempDir, err := os.MkdirTemp("", "gocodemunch-vector-smoke-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temporary storage path: %w", err)
	}

	cleanup := func() {
		if keepData {
			return
		}
		_ = os.RemoveAll(tempDir)
	}
	return tempDir, cleanup, nil
}

func fixtureTexts(chunks []fixtureChunk) []string {
	texts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		texts = append(texts, chunk.Text)
	}
	return texts
}

func buildFixtureRecords(
	namespace string,
	backend string,
	chunks []fixtureChunk,
	embeddings [][]float32,
) ([]indexing.VectorRecord, error) {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return nil, errors.New("namespace must be non-empty")
	}
	backend = strings.ToLower(strings.TrimSpace(backend))
	if len(chunks) != len(embeddings) {
		return nil, fmt.Errorf("fixture/embedding count mismatch: chunks=%d embeddings=%d", len(chunks), len(embeddings))
	}

	records := make([]indexing.VectorRecord, 0, len(chunks))
	for index, chunk := range chunks {
		sourceID := strings.TrimSpace(chunk.ID)
		if sourceID == "" {
			return nil, fmt.Errorf("fixture at index %d is missing id", index)
		}
		if len(embeddings[index]) == 0 {
			return nil, fmt.Errorf("fixture %q returned empty embedding", sourceID)
		}
		recordID := sourceID
		if backend == "qdrant" {
			recordID = qdrantPointID(sourceID)
		}

		records = append(records, indexing.VectorRecord{
			ID:        recordID,
			Namespace: namespace,
			Embedding: cloneEmbedding(embeddings[index]),
			Metadata: indexing.VectorMetadata{
				Repo:      "vector-smoke-fixtures",
				Path:      chunk.Path,
				Language:  chunk.Language,
				ChunkID:   sourceID,
				ChunkText: chunk.Text,
				StartLine: chunk.StartLine,
				EndLine:   chunk.EndLine,
				Fields: map[string]any{
					"fixture": true,
				},
			},
		})
	}

	return records, nil
}

func qdrantPointID(sourceID string) string {
	sum := sha1.Sum([]byte(sourceID))
	uuid := sum[:16]

	// RFC 4122 variant/version bits for deterministic UUID-like ids.
	uuid[6] = (uuid[6] & 0x0f) | 0x50
	uuid[8] = (uuid[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

func cloneEmbedding(embedding []float32) []float32 {
	clone := make([]float32, len(embedding))
	copy(clone, embedding)
	return clone
}

func configuredEmbeddingModel(cfg config.Config) string {
	provider, err := normalizeEmbeddingProvider(cfg.EmbeddingProvider)
	if err != nil {
		return strings.TrimSpace(cfg.EmbeddingModel)
	}
	if provider == "vllm" {
		return strings.TrimSpace(cfg.VLLMModel)
	}
	return strings.TrimSpace(cfg.EmbeddingModel)
}

func printProviderHint(cfg config.Config) {
	provider, err := normalizeEmbeddingProvider(cfg.EmbeddingProvider)
	if err != nil {
		provider = "ollama"
	}

	switch provider {
	case "vllm":
		model := configuredEmbeddingModel(cfg)
		fmt.Fprintf(
			os.Stderr,
			"hint: ensure vLLM is reachable at %q and model %q is available\n",
			cfg.VLLMBaseURL,
			model,
		)
	default:
		model := configuredEmbeddingModel(cfg)
		fmt.Fprintf(
			os.Stderr,
			"hint: ensure Ollama is reachable at %q and model %q is available (for example: ollama pull %s)\n",
			cfg.OllamaBaseURL,
			model,
			model,
		)
	}
}

func printSmokeSummary(
	cfg config.Config,
	namespace string,
	query string,
	indexedCount int,
	topK int,
	matchCount int,
) {
	provider, err := normalizeEmbeddingProvider(cfg.EmbeddingProvider)
	if err != nil {
		provider = strings.ToLower(strings.TrimSpace(cfg.EmbeddingProvider))
	}

	fmt.Println("Vector smoke run")
	fmt.Printf("- backend: %s\n", strings.ToLower(strings.TrimSpace(cfg.VectorBackend)))
	fmt.Printf("- embedding provider: %s\n", provider)
	fmt.Printf("- embedding model: %s\n", configuredEmbeddingModel(cfg))
	switch provider {
	case "vllm":
		fmt.Printf("- vllm base url: %s\n", cfg.VLLMBaseURL)
	default:
		fmt.Printf("- ollama base url: %s\n", cfg.OllamaBaseURL)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.VectorBackend)) {
	case "qdrant":
		fmt.Printf("- qdrant url: %s\n", cfg.QdrantURL)
		fmt.Printf("- qdrant collection: %s\n", cfg.QdrantCollection)
	default:
		fmt.Printf("- storage path: %s\n", cfg.StoragePath)
	}
	fmt.Printf("- namespace: %s\n", namespace)
	fmt.Printf("- indexed fixture chunks: %d\n", indexedCount)
	fmt.Printf("- query: %q\n", query)
	fmt.Printf("- requested top-k: %d\n", topK)
	fmt.Printf("- matches returned: %d\n", matchCount)
}

func printMatches(matches []indexing.VectorQueryMatch) {
	fmt.Println("Top semantic matches")
	if len(matches) == 0 {
		fmt.Println("(none)")
		return
	}

	for index, match := range matches {
		snippet := compactSnippet(match.Record.Metadata.ChunkText, 120)
		fmt.Printf(
			"%d. score=%.6f id=%s path=%s\n",
			index+1,
			match.Score,
			match.Record.ID,
			match.Record.Metadata.Path,
		)
		fmt.Printf("   snippet: %s\n", snippet)
	}
}

func compactSnippet(raw string, limit int) string {
	snippet := strings.Join(strings.Fields(raw), " ")
	if limit <= 0 || len(snippet) <= limit {
		return snippet
	}
	if limit <= 3 {
		return snippet[:limit]
	}
	return snippet[:limit-3] + "..."
}
