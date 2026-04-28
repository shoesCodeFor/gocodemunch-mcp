package main

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/evals"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/orchestration/embeddings"
	vectorqdrant "github.com/jgravelle/gocodemunch-mcp/src/internal/storage/vector/qdrant"
	vectorsqlite "github.com/jgravelle/gocodemunch-mcp/src/internal/storage/vector/sqlite"
)

const (
	defaultFixturesDir     = "tests-go/evals/fixtures"
	defaultNamespacePrefix = "eval-fixtures"
)

type fixtureCorpus struct {
	Dataset   string            `json:"dataset"`
	Documents []fixtureDocument `json:"documents"`
}

type fixtureDocument struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Language string `json:"language"`
	Text     string `json:"text"`
}

type fixtureQueries struct {
	Dataset string       `json:"dataset"`
	Queries []fixtureRow `json:"queries"`
}

type fixtureRow struct {
	ID    string `json:"id"`
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
}

type fixtureRelevance struct {
	Dataset   string            `json:"dataset"`
	Judgments []fixtureJudgment `json:"judgments"`
}

type fixtureJudgment struct {
	QueryID   string `json:"query_id"`
	DocID     string `json:"doc_id"`
	Relevance int    `json:"relevance"`
}

type evalFixtures struct {
	Dataset   string
	Documents []fixtureDocument
	Queries   []fixtureRow
	Relevance map[string]map[string]int
}

type evalCombination struct {
	Provider string
	Backend  string
}

type evalRunReport struct {
	GeneratedAtUTC string                  `json:"generated_at_utc"`
	Dataset        string                  `json:"dataset"`
	FixturesDir    string                  `json:"fixtures_dir"`
	Combinations   []evalCombinationReport `json:"combinations"`
}

type evalCombinationReport struct {
	Provider    string               `json:"provider"`
	Backend     string               `json:"backend"`
	Model       string               `json:"model"`
	Namespace   string               `json:"namespace"`
	IndexedDocs int                  `json:"indexed_docs"`
	PerQuery    []evalPerQueryReport `json:"per_query"`
	Aggregate   evalAggregateReport  `json:"aggregate"`
}

type evalPerQueryReport struct {
	QueryID   string           `json:"query_id"`
	Query     string           `json:"query"`
	TopK      int              `json:"top_k"`
	Returned  int              `json:"returned"`
	Relevant  int              `json:"relevant"`
	RecallAtK float64          `json:"recall_at_k"`
	MRRAtK    float64          `json:"mrr_at_k"`
	LatencyMS float64          `json:"latency_ms"`
	Matches   []evalQueryMatch `json:"matches"`
}

type evalQueryMatch struct {
	Rank  int     `json:"rank"`
	DocID string  `json:"doc_id"`
	Path  string  `json:"path"`
	Score float64 `json:"score"`
}

type evalAggregateReport struct {
	QueryCount     int                      `json:"query_count"`
	MeanRecallAtK  float64                  `json:"mean_recall_at_k"`
	MeanMRRAtK     float64                  `json:"mean_mrr_at_k"`
	LatencyMetrics evals.LatencyPercentiles `json:"latency_metrics"`
}

var (
	loadConfigFn     = config.Load
	createBackendFn  = newVectorBackend
	createEmbedderFn = newEmbedder
	closeBackendFn   = closeVectorBackend
	nowUTCFn         = func() time.Time { return time.Now().UTC() }
)

func main() {
	os.Exit(runWithArgs(os.Args[1:], os.Stdout, os.Stderr))
}

func runWithArgs(args []string, stdout, stderr io.Writer) int {
	cfg, err := loadConfigFn()
	if err != nil {
		fmt.Fprintf(stderr, "config validation failed: %v\n", err)
		return 1
	}

	flags := flag.NewFlagSet("gocodemunch-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)

	fixturesDirArg := flags.String("fixtures-dir", defaultFixturesDir, "Path containing corpus.json, queries.json, and relevance.json")
	providersArg := flags.String("providers", "", "Comma-separated embedding providers (ollama,vllm); defaults to EMBEDDING_PROVIDER")
	backendsArg := flags.String("backends", "", "Comma-separated vector backends (sqlite,qdrant); defaults to VECTOR_BACKEND")
	namespacePrefixArg := flags.String("namespace-prefix", defaultNamespacePrefix, "Namespace prefix used for eval fixtures")
	keepDataArg := flags.Bool("keep-data", false, "Keep temporary sqlite data directories when CODE_INDEX_PATH is not set")
	outPathArg := flags.String("out", "", "Optional output file path for JSON report")

	if err := flags.Parse(args); err != nil {
		return 2
	}

	fixturesDir, err := resolveFixturesDir(*fixturesDirArg)
	if err != nil {
		fmt.Fprintf(stderr, "resolve fixtures dir: %v\n", err)
		return 2
	}

	fixtures, err := loadEvalFixtures(fixturesDir)
	if err != nil {
		fmt.Fprintf(stderr, "load fixtures: %v\n", err)
		return 2
	}

	combinations, err := resolveCombinations(cfg, *providersArg, *backendsArg)
	if err != nil {
		fmt.Fprintf(stderr, "resolve eval matrix: %v\n", err)
		return 2
	}

	namespacePrefix := strings.TrimSpace(*namespacePrefixArg)
	if namespacePrefix == "" {
		fmt.Fprintln(stderr, "namespace-prefix must be non-empty")
		return 2
	}

	report := evalRunReport{
		GeneratedAtUTC: nowUTCFn().Format(time.RFC3339),
		Dataset:        fixtures.Dataset,
		FixturesDir:    fixturesDir,
		Combinations:   make([]evalCombinationReport, 0, len(combinations)),
	}

	for _, combo := range combinations {
		comboCfg := cfg
		comboCfg.EmbeddingProvider = combo.Provider
		comboCfg.VectorBackend = combo.Backend

		result, runErr := runCombination(
			context.Background(),
			comboCfg,
			combo,
			fixtures,
			namespacePrefix,
			*keepDataArg,
		)
		if runErr != nil {
			fmt.Fprintf(stderr, "run combo provider=%s backend=%s: %v\n", combo.Provider, combo.Backend, runErr)
			return 1
		}
		report.Combinations = append(report.Combinations, result)
	}

	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "marshal report: %v\n", err)
		return 1
	}

	if outPath := strings.TrimSpace(*outPathArg); outPath != "" {
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			fmt.Fprintf(stderr, "create output directory: %v\n", err)
			return 1
		}
		if err := os.WriteFile(outPath, append(payload, '\n'), 0o644); err != nil {
			fmt.Fprintf(stderr, "write report: %v\n", err)
			return 1
		}
	}

	_, _ = fmt.Fprintln(stdout, string(payload))
	return 0
}

func runCombination(
	ctx context.Context,
	cfg config.Config,
	combo evalCombination,
	fixtures evalFixtures,
	namespacePrefix string,
	keepData bool,
) (evalCombinationReport, error) {
	namespace := buildNamespace(namespacePrefix, fixtures.Dataset, combo.Provider, combo.Backend)

	cleanupStorage := func() {}
	if combo.Backend == "sqlite" {
		storagePath, cleanup, err := resolveStoragePath(cfg.StoragePath, keepData)
		if err != nil {
			return evalCombinationReport{}, fmt.Errorf("resolve sqlite storage path: %w", err)
		}
		cleanupStorage = cleanup
		cfg.StoragePath = storagePath
	}
	defer cleanupStorage()

	backend, err := createBackendFn(cfg, combo.Backend)
	if err != nil {
		return evalCombinationReport{}, fmt.Errorf("initialize vector backend: %w", err)
	}
	defer func() {
		_ = closeBackendFn(backend)
	}()

	embedder, err := createEmbedderFn(cfg, combo.Provider)
	if err != nil {
		return evalCombinationReport{}, fmt.Errorf("initialize embedder: %w", err)
	}

	if _, err := backend.DeleteNamespace(ctx, indexing.VectorDeleteNamespaceRequest{Namespace: namespace}); err != nil {
		return evalCombinationReport{}, fmt.Errorf("reset namespace %q: %w", namespace, err)
	}

	corpusTexts := make([]string, 0, len(fixtures.Documents))
	for _, doc := range fixtures.Documents {
		corpusTexts = append(corpusTexts, doc.Text)
	}

	corpusEmbeddings, err := embedder.Embed(ctx, corpusTexts)
	if err != nil {
		return evalCombinationReport{}, fmt.Errorf("embed corpus: %w", err)
	}

	records, err := buildFixtureRecords(namespace, combo.Backend, fixtures.Documents, corpusEmbeddings)
	if err != nil {
		return evalCombinationReport{}, fmt.Errorf("build fixture records: %w", err)
	}

	if _, err := backend.Upsert(ctx, indexing.VectorUpsertRequest{Namespace: namespace, Records: records}); err != nil {
		return evalCombinationReport{}, fmt.Errorf("upsert fixture records: %w", err)
	}

	queryReports := make([]evalPerQueryReport, 0, len(fixtures.Queries))
	latencies := make([]time.Duration, 0, len(fixtures.Queries))
	totalRecall := 0.0
	totalMRR := 0.0

	for _, query := range fixtures.Queries {
		start := time.Now()

		embeddings, err := embedder.Embed(ctx, []string{query.Query})
		if err != nil {
			return evalCombinationReport{}, fmt.Errorf("embed query %q: %w", query.ID, err)
		}
		if len(embeddings) != 1 {
			return evalCombinationReport{}, fmt.Errorf(
				"embed query %q: expected one embedding, got %d",
				query.ID,
				len(embeddings),
			)
		}

		response, err := backend.Query(ctx, indexing.VectorQueryRequest{
			Namespace: namespace,
			Embedding: embeddings[0],
			TopK:      query.TopK,
		})
		if err != nil {
			return evalCombinationReport{}, fmt.Errorf("query vectors for %q: %w", query.ID, err)
		}

		latency := time.Since(start)
		latencies = append(latencies, latency)

		rankedDocIDs := make([]string, 0, len(response.Matches))
		matches := make([]evalQueryMatch, 0, len(response.Matches))
		for idx, match := range response.Matches {
			docID := strings.TrimSpace(match.Record.Metadata.ChunkID)
			if docID == "" {
				docID = strings.TrimSpace(match.Record.ID)
			}
			rankedDocIDs = append(rankedDocIDs, docID)
			matches = append(matches, evalQueryMatch{
				Rank:  idx + 1,
				DocID: docID,
				Path:  match.Record.Metadata.Path,
				Score: match.Score,
			})
		}

		relevanceByDoc := fixtures.Relevance[query.ID]
		recall := evals.RecallAtK(rankedDocIDs, relevanceByDoc, query.TopK)
		mrr := evals.MRRAtK(rankedDocIDs, relevanceByDoc, query.TopK)
		totalRecall += recall
		totalMRR += mrr

		queryReports = append(queryReports, evalPerQueryReport{
			QueryID:   query.ID,
			Query:     query.Query,
			TopK:      query.TopK,
			Returned:  len(response.Matches),
			Relevant:  countRelevant(relevanceByDoc),
			RecallAtK: recall,
			MRRAtK:    mrr,
			LatencyMS: float64(latency) / float64(time.Millisecond),
			Matches:   matches,
		})
	}

	queryCount := len(queryReports)
	meanRecall := 0.0
	meanMRR := 0.0
	if queryCount > 0 {
		meanRecall = totalRecall / float64(queryCount)
		meanMRR = totalMRR / float64(queryCount)
	}

	aggregate := evalAggregateReport{
		QueryCount:     queryCount,
		MeanRecallAtK:  meanRecall,
		MeanMRRAtK:     meanMRR,
		LatencyMetrics: evals.ComputeLatencyPercentiles(latencies),
	}

	return evalCombinationReport{
		Provider:    combo.Provider,
		Backend:     combo.Backend,
		Model:       configuredEmbeddingModel(cfg, combo.Provider),
		Namespace:   namespace,
		IndexedDocs: len(fixtures.Documents),
		PerQuery:    queryReports,
		Aggregate:   aggregate,
	}, nil
}

func resolveFixturesDir(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("fixtures dir must be non-empty")
	}
	return filepath.Clean(trimmed), nil
}

func loadEvalFixtures(dir string) (evalFixtures, error) {
	corpus := fixtureCorpus{}
	if err := readJSONFile(filepath.Join(dir, "corpus.json"), &corpus); err != nil {
		return evalFixtures{}, err
	}
	queries := fixtureQueries{}
	if err := readJSONFile(filepath.Join(dir, "queries.json"), &queries); err != nil {
		return evalFixtures{}, err
	}
	relevance := fixtureRelevance{}
	if err := readJSONFile(filepath.Join(dir, "relevance.json"), &relevance); err != nil {
		return evalFixtures{}, err
	}

	if strings.TrimSpace(corpus.Dataset) == "" {
		return evalFixtures{}, errors.New("fixture corpus dataset must be non-empty")
	}
	if corpus.Dataset != queries.Dataset || corpus.Dataset != relevance.Dataset {
		return evalFixtures{}, fmt.Errorf(
			"fixture dataset mismatch: corpus=%q queries=%q relevance=%q",
			corpus.Dataset,
			queries.Dataset,
			relevance.Dataset,
		)
	}
	if len(corpus.Documents) == 0 {
		return evalFixtures{}, errors.New("fixture corpus documents must be non-empty")
	}
	if len(queries.Queries) == 0 {
		return evalFixtures{}, errors.New("fixture queries must be non-empty")
	}

	docIDs := make(map[string]struct{}, len(corpus.Documents))
	for idx, doc := range corpus.Documents {
		doc.ID = strings.TrimSpace(doc.ID)
		doc.Path = strings.TrimSpace(doc.Path)
		doc.Language = strings.TrimSpace(doc.Language)
		doc.Text = strings.TrimSpace(doc.Text)
		if doc.ID == "" || doc.Path == "" || doc.Language == "" || doc.Text == "" {
			return evalFixtures{}, fmt.Errorf("fixture document at index %d includes empty required fields", idx)
		}
		if _, exists := docIDs[doc.ID]; exists {
			return evalFixtures{}, fmt.Errorf("duplicate fixture document id %q", doc.ID)
		}
		docIDs[doc.ID] = struct{}{}
		corpus.Documents[idx] = doc
	}

	queryIDs := make(map[string]struct{}, len(queries.Queries))
	for idx, row := range queries.Queries {
		row.ID = strings.TrimSpace(row.ID)
		row.Query = strings.TrimSpace(row.Query)
		if row.ID == "" || row.Query == "" {
			return evalFixtures{}, fmt.Errorf("fixture query at index %d includes empty required fields", idx)
		}
		if row.TopK <= 0 {
			return evalFixtures{}, fmt.Errorf("fixture query %q has non-positive top_k %d", row.ID, row.TopK)
		}
		if row.TopK > len(corpus.Documents) {
			row.TopK = len(corpus.Documents)
		}
		if _, exists := queryIDs[row.ID]; exists {
			return evalFixtures{}, fmt.Errorf("duplicate fixture query id %q", row.ID)
		}
		queryIDs[row.ID] = struct{}{}
		queries.Queries[idx] = row
	}

	relevanceByQuery := make(map[string]map[string]int, len(queryIDs))
	for _, judgment := range relevance.Judgments {
		queryID := strings.TrimSpace(judgment.QueryID)
		docID := strings.TrimSpace(judgment.DocID)
		if _, exists := queryIDs[queryID]; !exists {
			return evalFixtures{}, fmt.Errorf("relevance references unknown query id %q", queryID)
		}
		if _, exists := docIDs[docID]; !exists {
			return evalFixtures{}, fmt.Errorf("relevance references unknown doc id %q", docID)
		}
		if judgment.Relevance < 0 || judgment.Relevance > 3 {
			return evalFixtures{}, fmt.Errorf(
				"relevance for query %q doc %q must be within [0,3], got %d",
				queryID,
				docID,
				judgment.Relevance,
			)
		}
		if _, exists := relevanceByQuery[queryID]; !exists {
			relevanceByQuery[queryID] = map[string]int{}
		}
		relevanceByQuery[queryID][docID] = judgment.Relevance
	}

	for queryID := range queryIDs {
		if len(relevanceByQuery[queryID]) == 0 {
			return evalFixtures{}, fmt.Errorf("query %q has no relevance judgments", queryID)
		}
	}

	return evalFixtures{
		Dataset:   corpus.Dataset,
		Documents: corpus.Documents,
		Queries:   queries.Queries,
		Relevance: relevanceByQuery,
	}, nil
}

func readJSONFile(path string, out any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(content, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func resolveCombinations(
	cfg config.Config,
	rawProviders string,
	rawBackends string,
) ([]evalCombination, error) {
	providers, err := resolveProviders(cfg, rawProviders)
	if err != nil {
		return nil, err
	}
	backends, err := resolveBackends(cfg, rawBackends)
	if err != nil {
		return nil, err
	}

	combos := make([]evalCombination, 0, len(providers)*len(backends))
	seen := map[string]struct{}{}
	for _, provider := range providers {
		for _, backend := range backends {
			key := provider + ":" + backend
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			combos = append(combos, evalCombination{Provider: provider, Backend: backend})
		}
	}
	if len(combos) == 0 {
		return nil, errors.New("at least one provider/backend combination is required")
	}
	return combos, nil
}

func resolveProviders(cfg config.Config, raw string) ([]string, error) {
	candidates := parseCSV(raw)
	if len(candidates) == 0 {
		candidates = []string{cfg.EmbeddingProvider}
	}
	providers := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		provider, err := normalizeEmbeddingProvider(candidate)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

func resolveBackends(cfg config.Config, raw string) ([]string, error) {
	candidates := parseCSV(raw)
	if len(candidates) == 0 {
		candidates = []string{cfg.VectorBackend}
	}
	backends := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		backend, err := normalizeBackend(candidate)
		if err != nil {
			return nil, err
		}
		backends = append(backends, backend)
	}
	return backends, nil
}

func parseCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		values = append(values, trimmed)
	}
	return values
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
			"unsupported embedding provider %q (allowed: ollama,vllm)",
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
		return "", fmt.Errorf("unsupported backend %q (allowed: sqlite,qdrant)", rawBackend)
	}
}

func newVectorBackend(cfg config.Config, backend string) (indexing.VectorBackend, error) {
	switch backend {
	case "sqlite":
		return vectorsqlite.NewAdapter(cfg.StoragePath)
	case "qdrant":
		return vectorqdrant.NewAdapter(cfg.QdrantURL, cfg.QdrantAPIKey, cfg.QdrantCollection)
	default:
		return nil, fmt.Errorf("unsupported backend %q", backend)
	}
}

func newEmbedder(cfg config.Config, provider string) (indexing.Embedder, error) {
	provider, err := normalizeEmbeddingProvider(provider)
	if err != nil {
		return nil, err
	}

	if cfg.VectorQueryTimeoutMS < 0 {
		return nil, fmt.Errorf("vector query timeout must be non-negative (got %dms)", cfg.VectorQueryTimeoutMS)
	}
	timeout := time.Duration(cfg.VectorQueryTimeoutMS) * time.Millisecond

	switch provider {
	case "ollama":
		embedder, buildErr := embeddings.NewOllamaEmbedder(cfg.OllamaBaseURL, cfg.EmbeddingModel, timeout)
		if buildErr != nil {
			return nil, fmt.Errorf("initialize ollama embedder: %w", buildErr)
		}
		return embedder, nil
	case "vllm":
		embedder, buildErr := embeddings.NewVLLMEmbedder(cfg.VLLMBaseURL, cfg.VLLMModel, cfg.VLLMAPIKey, timeout)
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

func resolveStoragePath(configured string, keepData bool) (string, func(), error) {
	trimmed := strings.TrimSpace(configured)
	if trimmed != "" {
		resolved := filepath.Clean(trimmed)
		if err := os.MkdirAll(resolved, 0o755); err != nil {
			return "", func() {}, fmt.Errorf("ensure configured storage path %q: %w", resolved, err)
		}
		return resolved, func() {}, nil
	}

	tempDir, err := os.MkdirTemp("", "gocodemunch-eval-*")
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

func buildFixtureRecords(
	namespace string,
	backend string,
	documents []fixtureDocument,
	embeddings [][]float32,
) ([]indexing.VectorRecord, error) {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return nil, errors.New("namespace must be non-empty")
	}
	backend = strings.ToLower(strings.TrimSpace(backend))
	if len(documents) != len(embeddings) {
		return nil, fmt.Errorf("document/embedding count mismatch: documents=%d embeddings=%d", len(documents), len(embeddings))
	}

	records := make([]indexing.VectorRecord, 0, len(documents))
	for index, doc := range documents {
		docID := strings.TrimSpace(doc.ID)
		if docID == "" {
			return nil, fmt.Errorf("fixture document at index %d is missing id", index)
		}
		if len(embeddings[index]) == 0 {
			return nil, fmt.Errorf("fixture document %q returned empty embedding", docID)
		}

		recordID := docID
		if backend == "qdrant" {
			recordID = qdrantPointID(docID)
		}

		records = append(records, indexing.VectorRecord{
			ID:        recordID,
			Namespace: namespace,
			Embedding: slices.Clone(embeddings[index]),
			Metadata: indexing.VectorMetadata{
				Repo:      "eval-fixtures",
				Path:      doc.Path,
				Language:  doc.Language,
				ChunkID:   docID,
				ChunkText: doc.Text,
				StartLine: 1,
				EndLine:   1,
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

func buildNamespace(prefix, dataset, provider, backend string) string {
	cleanPrefix := strings.Trim(strings.TrimSpace(prefix), "/")
	cleanDataset := strings.Trim(strings.TrimSpace(dataset), "/")
	if cleanDataset == "" {
		cleanDataset = "dataset"
	}
	if cleanPrefix == "" {
		cleanPrefix = defaultNamespacePrefix
	}
	return strings.Join([]string{cleanPrefix, cleanDataset, provider + "-" + backend}, "/")
}

func configuredEmbeddingModel(cfg config.Config, provider string) string {
	normalizedProvider, err := normalizeEmbeddingProvider(provider)
	if err != nil {
		return strings.TrimSpace(cfg.EmbeddingModel)
	}
	if normalizedProvider == "vllm" {
		return strings.TrimSpace(cfg.VLLMModel)
	}
	return strings.TrimSpace(cfg.EmbeddingModel)
}

func countRelevant(relevanceByDoc map[string]int) int {
	relevant := 0
	for _, score := range relevanceByDoc {
		if score > 0 {
			relevant++
		}
	}
	return relevant
}
