package main

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
	defaultEvalMode          = "retrieval-eval"
	evalModeTokenSavings     = "token-savings-smoke"
	defaultFixturesDir       = "tests-go/evals/fixtures"
	defaultNamespacePrefix   = "eval-fixtures"
	defaultMarkdownReportDir = "docs/evals/runs"
	evalIndexFileName        = "Eval-Index.md"
	evalGateFailureExitCode  = 3
)

const (
	envEvalMinMeanRecallAtKPrimary = "GOCODEMUNCH_EVAL_MIN_MEAN_RECALL_AT_K"
	envEvalMinMeanRecallAtKCompat  = "EVAL_MIN_MEAN_RECALL_AT_K"
	envEvalMinMeanMRRAtKPrimary    = "GOCODEMUNCH_EVAL_MIN_MEAN_MRR_AT_K"
	envEvalMinMeanMRRAtKCompat     = "EVAL_MIN_MEAN_MRR_AT_K"
	envEvalMaxP50LatencyMSPrimary  = "GOCODEMUNCH_EVAL_MAX_P50_LATENCY_MS"
	envEvalMaxP50LatencyMSCompat   = "EVAL_MAX_P50_LATENCY_MS"
	envEvalMaxP95LatencyMSPrimary  = "GOCODEMUNCH_EVAL_MAX_P95_LATENCY_MS"
	envEvalMaxP95LatencyMSCompat   = "EVAL_MAX_P95_LATENCY_MS"
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
	Thresholds     evalThresholdReport     `json:"thresholds"`
	GatePassed     bool                    `json:"gate_passed"`
	GateFailures   []evalGateFailureReport `json:"gate_failures,omitempty"`
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
	Gate        evalGateReport       `json:"gate"`
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

type evalThresholdConfig struct {
	MinMeanRecallAtK *float64
	MinMeanMRRAtK    *float64
	MaxP50LatencyMS  *float64
	MaxP95LatencyMS  *float64
}

type evalThresholdReport struct {
	MinMeanRecallAtK *float64 `json:"min_mean_recall_at_k,omitempty"`
	MinMeanMRRAtK    *float64 `json:"min_mean_mrr_at_k,omitempty"`
	MaxP50LatencyMS  *float64 `json:"max_p50_latency_ms,omitempty"`
	MaxP95LatencyMS  *float64 `json:"max_p95_latency_ms,omitempty"`
}

type evalGateCheckReport struct {
	Metric     string  `json:"metric"`
	Comparator string  `json:"comparator"`
	Actual     float64 `json:"actual"`
	Target     float64 `json:"target"`
	Message    string  `json:"message"`
}

type evalGateReport struct {
	Passed       bool                  `json:"passed"`
	FailedChecks []evalGateCheckReport `json:"failed_checks,omitempty"`
}

type evalGateFailureReport struct {
	Provider string              `json:"provider"`
	Backend  string              `json:"backend"`
	Check    evalGateCheckReport `json:"check"`
}

var (
	loadConfigFn     = config.Load
	createBackendFn  = newVectorBackend
	createEmbedderFn = newEmbedder
	closeBackendFn   = closeVectorBackend
	nowUTCFn         = func() time.Time { return time.Now().UTC() }
	wikiLinkPattern  = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	evalRunDocNameRe = regexp.MustCompile(`^\d{8}-\d{6}z-.+$`)
	createdDateRe    = regexp.MustCompile(`(?m)^created:\s*([0-9]{4}-[0-9]{2}-[0-9]{2})\s*$`)
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

	modeArg := flags.String("mode", defaultEvalMode, "Eval mode (retrieval-eval|token-savings-smoke)")
	fixturesDirArg := flags.String("fixtures-dir", defaultFixturesDir, "Path containing corpus.json, queries.json, and relevance.json")
	providersArg := flags.String("providers", "", "Comma-separated embedding providers (ollama,vllm); defaults to EMBEDDING_PROVIDER")
	backendsArg := flags.String("backends", "", "Comma-separated vector backends (sqlite,qdrant); defaults to VECTOR_BACKEND")
	namespacePrefixArg := flags.String("namespace-prefix", defaultNamespacePrefix, "Namespace prefix used for eval fixtures")
	keepDataArg := flags.Bool("keep-data", false, "Keep temporary sqlite data directories when CODE_INDEX_PATH is not set")
	outPathArg := flags.String("out", "", "Optional output file path for JSON report")
	markdownReportDirArg := flags.String(
		"markdown-report-dir",
		defaultMarkdownReportDir,
		"Directory where markdown eval run reports are written",
	)
	skipMarkdownReportArg := flags.Bool("skip-markdown-report", false, "Skip markdown report generation")
	minMeanRecallArg := flags.Float64(
		"min-mean-recall-at-k",
		math.NaN(),
		"Minimum required aggregate mean recall@k (0..1); overrides env if set",
	)
	minMeanMRRArg := flags.Float64(
		"min-mean-mrr-at-k",
		math.NaN(),
		"Minimum required aggregate mean mrr@k (0..1); overrides env if set",
	)
	maxP50LatencyArg := flags.Float64(
		"max-p50-latency-ms",
		math.NaN(),
		"Maximum allowed aggregate latency p50 in milliseconds (>=0); overrides env if set",
	)
	maxP95LatencyArg := flags.Float64(
		"max-p95-latency-ms",
		math.NaN(),
		"Maximum allowed aggregate latency p95 in milliseconds (>=0); overrides env if set",
	)

	if err := flags.Parse(args); err != nil {
		return 2
	}

	explicitFlags := map[string]struct{}{}
	flags.Visit(func(f *flag.Flag) {
		explicitFlags[f.Name] = struct{}{}
	})

	mode, err := normalizeEvalMode(*modeArg)
	if err != nil {
		fmt.Fprintf(stderr, "resolve eval mode: %v\n", err)
		return 2
	}

	if mode == evalModeTokenSavings {
		fixturesDirValue := strings.TrimSpace(*fixturesDirArg)
		if _, ok := explicitFlags["fixtures-dir"]; !ok && fixturesDirValue == defaultFixturesDir {
			fixturesDirValue = defaultTokenSavingsFixturesDir
		}

		outPathValue := strings.TrimSpace(*outPathArg)
		if _, ok := explicitFlags["out"]; !ok && outPathValue == "" {
			outPathValue = defaultTokenSavingsOutputPath
		}

		skipMarkdownReport := *skipMarkdownReportArg
		if _, ok := explicitFlags["skip-markdown-report"]; !ok {
			skipMarkdownReport = true
		}

		report, runErr := runTokenSavingsSmokeMode(
			context.Background(),
			cfg,
			fixturesDirValue,
			*keepDataArg,
			skipMarkdownReport,
		)
		if runErr != nil {
			fmt.Fprintf(stderr, "run token savings smoke: %v\n", runErr)
			return 1
		}

		payload, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			fmt.Fprintf(stderr, "marshal token savings report: %v\n", marshalErr)
			return 1
		}
		if err := writeJSONReport(outPathValue, payload); err != nil {
			fmt.Fprintf(stderr, "write token savings report: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, string(payload))
		return 0
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

	thresholds, err := resolveEvalThresholdConfig(
		*minMeanRecallArg,
		*minMeanMRRArg,
		*maxP50LatencyArg,
		*maxP95LatencyArg,
	)
	if err != nil {
		fmt.Fprintf(stderr, "resolve eval thresholds: %v\n", err)
		return 2
	}

	report := evalRunReport{
		GeneratedAtUTC: nowUTCFn().Format(time.RFC3339),
		Dataset:        fixtures.Dataset,
		FixturesDir:    fixturesDir,
		Thresholds:     thresholds.toReport(),
		GatePassed:     true,
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

		result.Gate = evaluateGate(result.Aggregate, thresholds)
		if !result.Gate.Passed {
			report.GatePassed = false
			for _, failed := range result.Gate.FailedChecks {
				report.GateFailures = append(report.GateFailures, evalGateFailureReport{
					Provider: result.Provider,
					Backend:  result.Backend,
					Check:    failed,
				})
			}
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

	if !*skipMarkdownReportArg {
		reportPath, err := writeMarkdownRunReport(*markdownReportDirArg, report, *outPathArg)
		if err != nil {
			fmt.Fprintf(stderr, "write markdown report: %v\n", err)
			return 1
		}
		if _, err := writeEvalIndex(*markdownReportDirArg, reportPath, report.GeneratedAtUTC); err != nil {
			fmt.Fprintf(stderr, "write eval index: %v\n", err)
			return 1
		}
	}

	_, _ = fmt.Fprintln(stdout, string(payload))
	if !report.GatePassed {
		fmt.Fprintln(stderr, "eval gate failed: one or more thresholds were not met")
		return evalGateFailureExitCode
	}
	return 0
}

func normalizeEvalMode(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" {
		return defaultEvalMode, nil
	}

	switch mode {
	case defaultEvalMode, evalModeTokenSavings:
		return mode, nil
	default:
		return "", fmt.Errorf(
			"unsupported mode %q (allowed: %s,%s)",
			raw,
			defaultEvalMode,
			evalModeTokenSavings,
		)
	}
}

func writeJSONReport(outPath string, payload []byte) error {
	trimmed := strings.TrimSpace(outPath)
	if trimmed == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(trimmed), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(trimmed, append(payload, '\n'), 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

func resolveEvalThresholdConfig(
	minMeanRecallAtKFlag float64,
	minMeanMRRAtKFlag float64,
	maxP50LatencyMSFlag float64,
	maxP95LatencyMSFlag float64,
) (evalThresholdConfig, error) {
	minMeanRecallAtK, err := resolveThresholdValue(
		minMeanRecallAtKFlag,
		[]string{envEvalMinMeanRecallAtKPrimary, envEvalMinMeanRecallAtKCompat},
		func(value float64) error {
			if value < 0 || value > 1 {
				return fmt.Errorf("must be within [0,1], got %v", value)
			}
			return nil
		},
	)
	if err != nil {
		return evalThresholdConfig{}, fmt.Errorf("min mean recall@k: %w", err)
	}

	minMeanMRRAtK, err := resolveThresholdValue(
		minMeanMRRAtKFlag,
		[]string{envEvalMinMeanMRRAtKPrimary, envEvalMinMeanMRRAtKCompat},
		func(value float64) error {
			if value < 0 || value > 1 {
				return fmt.Errorf("must be within [0,1], got %v", value)
			}
			return nil
		},
	)
	if err != nil {
		return evalThresholdConfig{}, fmt.Errorf("min mean mrr@k: %w", err)
	}

	maxP50LatencyMS, err := resolveThresholdValue(
		maxP50LatencyMSFlag,
		[]string{envEvalMaxP50LatencyMSPrimary, envEvalMaxP50LatencyMSCompat},
		func(value float64) error {
			if value < 0 {
				return fmt.Errorf("must be >= 0, got %v", value)
			}
			return nil
		},
	)
	if err != nil {
		return evalThresholdConfig{}, fmt.Errorf("max p50 latency ms: %w", err)
	}

	maxP95LatencyMS, err := resolveThresholdValue(
		maxP95LatencyMSFlag,
		[]string{envEvalMaxP95LatencyMSPrimary, envEvalMaxP95LatencyMSCompat},
		func(value float64) error {
			if value < 0 {
				return fmt.Errorf("must be >= 0, got %v", value)
			}
			return nil
		},
	)
	if err != nil {
		return evalThresholdConfig{}, fmt.Errorf("max p95 latency ms: %w", err)
	}

	return evalThresholdConfig{
		MinMeanRecallAtK: minMeanRecallAtK,
		MinMeanMRRAtK:    minMeanMRRAtK,
		MaxP50LatencyMS:  maxP50LatencyMS,
		MaxP95LatencyMS:  maxP95LatencyMS,
	}, nil
}

func resolveThresholdValue(
	flagValue float64,
	envKeys []string,
	validate func(float64) error,
) (*float64, error) {
	value, set, err := resolveThresholdRaw(flagValue, envKeys)
	if err != nil {
		return nil, err
	}
	if !set {
		return nil, nil
	}
	if validate != nil {
		if err := validate(value); err != nil {
			return nil, err
		}
	}
	return float64Ptr(value), nil
}

func resolveThresholdRaw(flagValue float64, envKeys []string) (float64, bool, error) {
	if !math.IsNaN(flagValue) {
		return flagValue, true, nil
	}
	value, source, ok := firstEnvValue(envKeys...)
	if !ok {
		return 0, false, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid %s value %q: %w", source, value, err)
	}
	return parsed, true, nil
}

func firstEnvValue(keys ...string) (value string, source string, ok bool) {
	for _, key := range keys {
		trimmed := strings.TrimSpace(os.Getenv(key))
		if trimmed == "" {
			continue
		}
		return trimmed, key, true
	}
	return "", "", false
}

func evaluateGate(aggregate evalAggregateReport, thresholds evalThresholdConfig) evalGateReport {
	failures := make([]evalGateCheckReport, 0, 4)

	if thresholds.MinMeanRecallAtK != nil && aggregate.MeanRecallAtK < *thresholds.MinMeanRecallAtK {
		failures = append(failures, evalGateCheckReport{
			Metric:     "mean_recall_at_k",
			Comparator: ">=",
			Actual:     aggregate.MeanRecallAtK,
			Target:     *thresholds.MinMeanRecallAtK,
			Message: fmt.Sprintf(
				"mean_recall_at_k %.6f is below required threshold %.6f",
				aggregate.MeanRecallAtK,
				*thresholds.MinMeanRecallAtK,
			),
		})
	}

	if thresholds.MinMeanMRRAtK != nil && aggregate.MeanMRRAtK < *thresholds.MinMeanMRRAtK {
		failures = append(failures, evalGateCheckReport{
			Metric:     "mean_mrr_at_k",
			Comparator: ">=",
			Actual:     aggregate.MeanMRRAtK,
			Target:     *thresholds.MinMeanMRRAtK,
			Message: fmt.Sprintf(
				"mean_mrr_at_k %.6f is below required threshold %.6f",
				aggregate.MeanMRRAtK,
				*thresholds.MinMeanMRRAtK,
			),
		})
	}

	if thresholds.MaxP50LatencyMS != nil && aggregate.LatencyMetrics.P50MS > *thresholds.MaxP50LatencyMS {
		failures = append(failures, evalGateCheckReport{
			Metric:     "latency_metrics.p50_ms",
			Comparator: "<=",
			Actual:     aggregate.LatencyMetrics.P50MS,
			Target:     *thresholds.MaxP50LatencyMS,
			Message: fmt.Sprintf(
				"latency_metrics.p50_ms %.6f exceeds allowed threshold %.6f",
				aggregate.LatencyMetrics.P50MS,
				*thresholds.MaxP50LatencyMS,
			),
		})
	}

	if thresholds.MaxP95LatencyMS != nil && aggregate.LatencyMetrics.P95MS > *thresholds.MaxP95LatencyMS {
		failures = append(failures, evalGateCheckReport{
			Metric:     "latency_metrics.p95_ms",
			Comparator: "<=",
			Actual:     aggregate.LatencyMetrics.P95MS,
			Target:     *thresholds.MaxP95LatencyMS,
			Message: fmt.Sprintf(
				"latency_metrics.p95_ms %.6f exceeds allowed threshold %.6f",
				aggregate.LatencyMetrics.P95MS,
				*thresholds.MaxP95LatencyMS,
			),
		})
	}

	return evalGateReport{
		Passed:       len(failures) == 0,
		FailedChecks: failures,
	}
}

func (cfg evalThresholdConfig) toReport() evalThresholdReport {
	return evalThresholdReport{
		MinMeanRecallAtK: cloneFloat64Ptr(cfg.MinMeanRecallAtK),
		MinMeanMRRAtK:    cloneFloat64Ptr(cfg.MinMeanMRRAtK),
		MaxP50LatencyMS:  cloneFloat64Ptr(cfg.MaxP50LatencyMS),
		MaxP95LatencyMS:  cloneFloat64Ptr(cfg.MaxP95LatencyMS),
	}
}

func float64Ptr(value float64) *float64 {
	v := value
	return &v
}

func cloneFloat64Ptr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	return float64Ptr(*value)
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

func writeMarkdownRunReport(
	rawDir string,
	report evalRunReport,
	jsonOutPath string,
) (string, error) {
	reportDir := strings.TrimSpace(rawDir)
	if reportDir == "" {
		return "", errors.New("markdown report dir must be non-empty")
	}
	reportDir = filepath.Clean(reportDir)

	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return "", fmt.Errorf("create markdown report directory %q: %w", reportDir, err)
	}

	fileName := buildMarkdownReportFileName(report)
	reportPath := filepath.Join(reportDir, fileName)
	content := renderMarkdownRunReport(report, jsonOutPath)
	if err := os.WriteFile(reportPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write markdown report %q: %w", reportPath, err)
	}

	return reportPath, nil
}

func writeEvalIndex(markdownReportDir string, markdownReportPath string, generatedAtUTC string) (string, error) {
	reportDir := filepath.Clean(strings.TrimSpace(markdownReportDir))
	if reportDir == "." || reportDir == "" {
		return "", errors.New("markdown report dir must be non-empty")
	}
	reportName := strings.TrimSpace(markdownReportPath)
	if reportName == "" {
		return "", errors.New("markdown report path must be non-empty")
	}

	evalDir := filepath.Dir(reportDir)
	if err := os.MkdirAll(evalDir, 0o755); err != nil {
		return "", fmt.Errorf("create eval docs directory %q: %w", evalDir, err)
	}
	indexPath := filepath.Join(evalDir, evalIndexFileName)

	links, createdDate, err := loadEvalIndexState(indexPath)
	if err != nil {
		return "", err
	}

	reportDocName := strings.TrimSuffix(filepath.Base(reportName), filepath.Ext(reportName))
	if reportDocName == "" {
		return "", errors.New("markdown report path must include a filename")
	}
	links = append(links, reportDocName)
	links = uniqueEvalRunLinks(links)
	sortEvalRunLinksNewestFirst(links)

	if createdDate == "" {
		createdDate = createdDateFromRFC3339(generatedAtUTC)
	}
	content := renderEvalIndexMarkdown(createdDate, links)
	if err := os.WriteFile(indexPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write eval index %q: %w", indexPath, err)
	}
	return indexPath, nil
}

func loadEvalIndexState(indexPath string) ([]string, string, error) {
	contentBytes, err := os.ReadFile(indexPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("read eval index %q: %w", indexPath, err)
	}
	content := string(contentBytes)

	links := make([]string, 0)
	matches := wikiLinkPattern.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		name := normalizeEvalRunDocName(match[1])
		if name == "" {
			continue
		}
		links = append(links, name)
	}

	createdDate := ""
	if match := createdDateRe.FindStringSubmatch(content); len(match) == 2 {
		createdDate = strings.TrimSpace(match[1])
	}

	return uniqueEvalRunLinks(links), createdDate, nil
}

func normalizeEvalRunDocName(raw string) string {
	name := strings.TrimSpace(raw)
	name = strings.Trim(name, "\"'")
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = filepath.Base(name)
	if name == "" {
		return ""
	}
	if !evalRunDocNameRe.MatchString(strings.ToLower(name)) {
		return ""
	}
	return name
}

func uniqueEvalRunLinks(links []string) []string {
	if len(links) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	ordered := make([]string, 0, len(links))
	for _, link := range links {
		normalized := normalizeEvalRunDocName(link)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		ordered = append(ordered, normalized)
	}
	return ordered
}

func sortEvalRunLinksNewestFirst(links []string) {
	slices.SortFunc(links, func(left, right string) int {
		leftTime, leftOK := parseEvalRunDocTime(left)
		rightTime, rightOK := parseEvalRunDocTime(right)
		switch {
		case leftOK && rightOK:
			if leftTime.After(rightTime) {
				return -1
			}
			if leftTime.Before(rightTime) {
				return 1
			}
		case leftOK && !rightOK:
			return -1
		case !leftOK && rightOK:
			return 1
		}
		return strings.Compare(right, left)
	})
}

func parseEvalRunDocTime(docName string) (time.Time, bool) {
	normalized := strings.ToLower(strings.TrimSpace(docName))
	if len(normalized) < len("20060102-150405z") {
		return time.Time{}, false
	}
	prefix := normalized[:len("20060102-150405z")]
	parsed, err := time.Parse("20060102-150405z", prefix)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func renderEvalIndexMarkdown(createdDate string, links []string) string {
	created := strings.TrimSpace(createdDate)
	if created == "" {
		created = nowUTCFn().Format("2006-01-02")
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: reference\n")
	b.WriteString("title: Eval Index\n")
	b.WriteString(fmt.Sprintf("created: %s\n", created))
	b.WriteString("tags:\n")
	b.WriteString("  - eval\n")
	b.WriteString("  - index\n")
	b.WriteString("related: []\n")
	b.WriteString("---\n\n")
	b.WriteString("# Eval Index\n\n")
	b.WriteString("Newest-first wiki-links to reports in `docs/evals/runs`.\n\n")
	if len(links) == 0 {
		b.WriteString("- None yet\n")
		return b.String()
	}
	for _, link := range links {
		b.WriteString(fmt.Sprintf("- [[%s]]\n", link))
	}
	return b.String()
}

func buildMarkdownReportFileName(report evalRunReport) string {
	timestamp := sanitizeTagValue(strings.TrimSpace(report.GeneratedAtUTC))
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(report.GeneratedAtUTC)); err == nil {
		timestamp = strings.ToLower(parsed.UTC().Format("20060102-150405Z"))
	}
	if timestamp == "" {
		timestamp = "unknown-time"
	}

	dataset := sanitizeTagValue(report.Dataset)
	if dataset == "" {
		dataset = "dataset"
	}
	return fmt.Sprintf("%s-%s.md", timestamp, dataset)
}

func renderMarkdownRunReport(report evalRunReport, jsonOutPath string) string {
	createdDate := createdDateFromRFC3339(report.GeneratedAtUTC)
	title := fmt.Sprintf("Eval Run %s %s", strings.TrimSpace(report.Dataset), strings.TrimSpace(report.GeneratedAtUTC))
	tags := collectMarkdownReportTags(report)
	relatedLinks := collectMarkdownRelatedLinks(report, jsonOutPath)

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: report\n")
	b.WriteString(fmt.Sprintf("title: %s\n", title))
	b.WriteString(fmt.Sprintf("created: %s\n", createdDate))
	b.WriteString("tags:\n")
	for _, tag := range tags {
		b.WriteString(fmt.Sprintf("  - %s\n", tag))
	}
	b.WriteString("related:\n")
	for _, link := range relatedLinks {
		b.WriteString(fmt.Sprintf("  - '[[%s]]'\n", link))
	}
	b.WriteString("---\n\n")
	b.WriteString("## Summary\n\n")
	b.WriteString(fmt.Sprintf("- Generated (UTC): `%s`\n", report.GeneratedAtUTC))
	b.WriteString(fmt.Sprintf("- Dataset: `%s`\n", report.Dataset))
	b.WriteString(fmt.Sprintf("- Fixtures Dir: `%s`\n", report.FixturesDir))
	b.WriteString(fmt.Sprintf("- Gate Passed: `%t`\n", report.GatePassed))
	b.WriteString("\n## Aggregates\n\n")
	b.WriteString("| Provider | Backend | Model | Mean Recall@k | Mean MRR@k | P50 Latency (ms) | P95 Latency (ms) | Gate |\n")
	b.WriteString("| --- | --- | --- | ---: | ---: | ---: | ---: | --- |\n")
	for _, combo := range report.Combinations {
		b.WriteString(fmt.Sprintf(
			"| %s | %s | %s | %.4f | %.4f | %.2f | %.2f | %t |\n",
			combo.Provider,
			combo.Backend,
			combo.Model,
			combo.Aggregate.MeanRecallAtK,
			combo.Aggregate.MeanMRRAtK,
			combo.Aggregate.LatencyMetrics.P50MS,
			combo.Aggregate.LatencyMetrics.P95MS,
			combo.Gate.Passed,
		))
	}

	b.WriteString("\n## Gate Failures\n\n")
	if len(report.GateFailures) == 0 {
		b.WriteString("- None\n")
	} else {
		for _, failure := range report.GateFailures {
			b.WriteString(fmt.Sprintf(
				"- `%s/%s` %s %s %.6f (actual %.6f)\n",
				failure.Provider,
				failure.Backend,
				failure.Check.Metric,
				failure.Check.Comparator,
				failure.Check.Target,
				failure.Check.Actual,
			))
		}
	}

	return b.String()
}

func collectMarkdownReportTags(report evalRunReport) []string {
	tagSet := map[string]struct{}{
		"eval": {},
	}
	dataset := sanitizeTagValue(report.Dataset)
	if dataset != "" {
		tagSet["dataset-"+dataset] = struct{}{}
	}
	if report.GatePassed {
		tagSet["gate-pass"] = struct{}{}
	} else {
		tagSet["gate-fail"] = struct{}{}
	}
	for _, combo := range report.Combinations {
		if provider := sanitizeTagValue(combo.Provider); provider != "" {
			tagSet["provider-"+provider] = struct{}{}
		}
		if backend := sanitizeTagValue(combo.Backend); backend != "" {
			tagSet["backend-"+backend] = struct{}{}
		}
		if model := sanitizeTagValue(combo.Model); model != "" {
			tagSet["model-"+model] = struct{}{}
		}
	}

	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	return tags
}

func collectMarkdownRelatedLinks(report evalRunReport, jsonOutPath string) []string {
	relatedSet := map[string]struct{}{
		"Eval-Index": {},
	}
	dataset := sanitizeTagValue(report.Dataset)
	if dataset != "" {
		relatedSet["Eval-Dataset-"+dataset] = struct{}{}
	}
	if outPath := strings.TrimSpace(jsonOutPath); outPath != "" {
		baseName := strings.TrimSuffix(filepath.Base(outPath), filepath.Ext(outPath))
		baseName = strings.TrimSpace(baseName)
		if baseName != "" {
			relatedSet[baseName] = struct{}{}
		}
	}

	related := make([]string, 0, len(relatedSet))
	for link := range relatedSet {
		related = append(related, link)
	}
	slices.Sort(related)
	return related
}

func createdDateFromRFC3339(timestamp string) string {
	trimmed := strings.TrimSpace(timestamp)
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed.UTC().Format("2006-01-02")
	}
	if trimmed == "" {
		return nowUTCFn().Format("2006-01-02")
	}
	return trimmed
}

func sanitizeTagValue(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(normalized))
	needsDash := false
	for _, ch := range normalized {
		switch {
		case ch >= 'a' && ch <= 'z':
			if needsDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(ch)
			needsDash = false
		case ch >= '0' && ch <= '9':
			if needsDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(ch)
			needsDash = false
		default:
			needsDash = true
		}
	}

	return strings.Trim(b.String(), "-")
}
