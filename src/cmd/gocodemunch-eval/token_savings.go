package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/orchestration"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/savings"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/telemetry"
)

const (
	defaultTokenSavingsFixturesDir   = "tests-go/evals/fixtures/token-savings-smoke"
	defaultTokenSavingsOutputPath    = "Auto Run Docs/Working/evals/token-savings-smoke.json"
	tokenSavingsPromptSuiteFileName  = "prompt_suite.json"
	serializedBytesPerEstimatedToken = 4.0
	tokenSavingsModeWithMCP          = "with_mcp"
	tokenSavingsModeWithoutMCP       = "without_mcp"
)

var tokenSavingsSupportedTools = map[string]struct{}{
	"get_file_tree":    {},
	"get_file_outline": {},
	"search_text":      {},
	"find_importers":   {},
	"find_references":  {},
}

var tokenSavingsRequiredModes = []string{
	tokenSavingsModeWithMCP,
	tokenSavingsModeWithoutMCP,
}

type tokenSavingsPromptSuiteFile struct {
	Dataset      string                    `json:"dataset"`
	SuiteVersion string                    `json:"suite_version"`
	Cases        []tokenSavingsCaseFixture `json:"cases"`
}

type tokenSavingsCaseFixture struct {
	ID           string         `json:"id"`
	Prompt       string         `json:"prompt"`
	Modes        []string       `json:"modes"`
	Tool         string         `json:"tool"`
	Arguments    map[string]any `json:"arguments"`
	ContextFiles []string       `json:"context_files"`
}

type tokenSavingsFixtureSet struct {
	Dataset      string
	SuiteVersion string
	Documents    []fixtureDocument
	Cases        []tokenSavingsCaseFixture
}

type tokenSavingsSmokeReport struct {
	GeneratedAtUTC    string                                     `json:"generated_at_utc"`
	Mode              string                                     `json:"mode"`
	Dataset           string                                     `json:"dataset"`
	SuiteVersion      string                                     `json:"suite_version"`
	FixturesDir       string                                     `json:"fixtures_dir"`
	IndexedRepo       string                                     `json:"indexed_repo"`
	FileCount         int                                        `json:"file_count"`
	CompetitorPricing map[string]config.SavingsCompetitorPricing `json:"competitor_pricing_usd_per_mtok"`
	CombinationCount  int                                        `json:"combination_count"`
	Combinations      []tokenSavingsCombinationReport            `json:"combinations,omitempty"`
	Cases             []tokenSavingsCaseReport                   `json:"cases"`
	Aggregate         tokenSavingsAggregateReport                `json:"aggregate"`
}

type tokenSavingsCombinationReport struct {
	Provider    string                      `json:"provider"`
	Backend     string                      `json:"backend"`
	Model       string                      `json:"model"`
	IndexedRepo string                      `json:"indexed_repo"`
	FileCount   int                         `json:"file_count"`
	Cases       []tokenSavingsCaseReport    `json:"cases"`
	Aggregate   tokenSavingsAggregateReport `json:"aggregate"`
}

type tokenSavingsCaseReport struct {
	ID            string                      `json:"id"`
	Prompt        string                      `json:"prompt"`
	Modes         []string                    `json:"modes"`
	Tool          string                      `json:"tool"`
	ToolArguments map[string]any              `json:"tool_arguments"`
	ContextFiles  []string                    `json:"context_files"`
	WithMCP       tokenSavingsModeCaseMetrics `json:"with_mcp"`
	WithoutMCP    tokenSavingsModeCaseMetrics `json:"without_mcp"`
	Savings       tokenSavingsDeltaReport     `json:"savings"`
}

type tokenSavingsModeCaseMetrics struct {
	InputTokens        int                `json:"input_tokens"`
	OutputTokens       int                `json:"output_tokens"`
	TotalTokens        int                `json:"total_tokens"`
	ToolRequestTokens  int                `json:"tool_request_tokens,omitempty"`
	ToolResponseTokens int                `json:"tool_response_tokens,omitempty"`
	ContextTokens      int                `json:"context_tokens,omitempty"`
	CostUSD            map[string]float64 `json:"cost_usd"`
}

type tokenSavingsModeAggregateMetrics struct {
	InputTokens  int                `json:"input_tokens"`
	OutputTokens int                `json:"output_tokens"`
	TotalTokens  int                `json:"total_tokens"`
	CostUSD      map[string]float64 `json:"cost_usd"`
}

type tokenSavingsDeltaReport struct {
	TokensSaved  int                                    `json:"tokens_saved"`
	SavingsPct   float64                                `json:"savings_pct"`
	CostSavedUSD map[string]float64                     `json:"cost_saved_usd"`
	Scores       map[string]tokenSavingsCompetitorScore `json:"scores"`
}

type tokenSavingsCompetitorScore struct {
	TokensSaved  int     `json:"tokens_saved"`
	CostSavedUSD float64 `json:"cost_saved_usd"`
	SavingsPct   float64 `json:"savings_pct"`
}

type tokenSavingsDistributionMetric struct {
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P95    float64 `json:"p95"`
}

type tokenSavingsDistributionReport struct {
	TokensSaved  tokenSavingsDistributionMetric            `json:"tokens_saved"`
	SavingsPct   tokenSavingsDistributionMetric            `json:"savings_pct"`
	CostSavedUSD map[string]tokenSavingsDistributionMetric `json:"cost_saved_usd"`
}

type tokenSavingsTrendPoint struct {
	CapturedAtUTC string  `json:"captured_at_utc"`
	TokensSaved   int     `json:"tokens_saved"`
	CostSavedUSD  float64 `json:"cost_saved_usd"`
	SavingsPct    float64 `json:"savings_pct"`
}

type tokenSavingsAggregateReport struct {
	CaseCount    int                                 `json:"case_count"`
	WithMCP      tokenSavingsModeAggregateMetrics    `json:"with_mcp"`
	WithoutMCP   tokenSavingsModeAggregateMetrics    `json:"without_mcp"`
	Savings      tokenSavingsDeltaReport             `json:"savings"`
	Distribution tokenSavingsDistributionReport      `json:"distribution"`
	Trends       map[string][]tokenSavingsTrendPoint `json:"trends"`
}

type tokenSavingsContextFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type tokenSavingsExecutionContext struct {
	service         *orchestration.Service
	repoID          string
	reportedRepoID  string
	documentsByPath map[string]fixtureDocument
	pricing         map[string]config.SavingsCompetitorPricing
}

type tokenSavingsModeAdapter struct {
	name    string
	execute func(
		context.Context,
		tokenSavingsExecutionContext,
		tokenSavingsCaseFixture,
		int,
	) (tokenSavingsModeCaseMetrics, error)
}

var tokenSavingsModeAdapters = []tokenSavingsModeAdapter{
	{
		name:    tokenSavingsModeWithMCP,
		execute: runTokenSavingsWithMCPMode,
	},
	{
		name:    tokenSavingsModeWithoutMCP,
		execute: runTokenSavingsWithoutMCPMode,
	},
}

func runTokenSavingsSmokeMode(
	ctx context.Context,
	cfg config.Config,
	fixturesDirRaw string,
	combinations []evalCombination,
	keepData bool,
	useCombinationDependencies bool,
) (tokenSavingsSmokeReport, error) {
	generatedAt := nowUTCFn().UTC()

	fixturesDir, err := resolveFixturesDir(fixturesDirRaw)
	if err != nil {
		return tokenSavingsSmokeReport{}, err
	}

	fixtures, err := loadTokenSavingsFixtures(fixturesDir)
	if err != nil {
		return tokenSavingsSmokeReport{}, err
	}

	materializedRoot, cleanupCorpus, err := materializeTokenSavingsCorpus(fixtures.Documents)
	if err != nil {
		return tokenSavingsSmokeReport{}, fmt.Errorf("materialize token savings corpus: %w", err)
	}
	defer cleanupCorpus()

	pricing, _ := savings.NormalizePricing(cfg.SavingsCompetitorPricing)

	documentsByPath := make(map[string]fixtureDocument, len(fixtures.Documents))
	for _, doc := range fixtures.Documents {
		documentsByPath[doc.Path] = doc
	}

	combinationReports := make([]tokenSavingsCombinationReport, 0, len(combinations))
	for _, combo := range combinations {
		comboCfg := cfg
		comboCfg.EmbeddingProvider = combo.Provider
		comboCfg.VectorBackend = combo.Backend
		comboModel := configuredEmbeddingModel(comboCfg, combo.Provider)
		benchmarkRunID := buildTokenSavingsBenchmarkRunID(
			fixtures.Dataset,
			fixtures.SuiteVersion,
			generatedAt,
			combo.Provider,
			combo.Backend,
			comboModel,
		)
		historicalRuns, err := loadTokenSavingsHistoricalRuns(ctx, cfg.StoragePath, storage.SavingsBenchmarkRunFilter{
			Dataset:      fixtures.Dataset,
			SuiteVersion: fixtures.SuiteVersion,
			Mode:         evalModeTokenSavings,
			Provider:     combo.Provider,
			Backend:      combo.Backend,
			Model:        comboModel,
			ExcludeRunID: benchmarkRunID,
		})
		if err != nil {
			historicalRuns = nil
		}

		comboReport, benchmarkSnapshot, err := runTokenSavingsCombination(
			ctx,
			comboCfg,
			combo,
			fixtures,
			pricing,
			materializedRoot,
			documentsByPath,
			historicalRuns,
			generatedAt,
			keepData,
			useCombinationDependencies,
		)
		if err != nil {
			return tokenSavingsSmokeReport{}, fmt.Errorf(
				"run token savings combination provider=%s backend=%s: %w",
				combo.Provider,
				combo.Backend,
				err,
			)
		}
		if err := persistTokenSavingsBenchmarkRun(
			ctx,
			cfg.StoragePath,
			buildTokenSavingsStoredBenchmarkRun(
				benchmarkRunID,
				fixtures,
				comboReport,
				benchmarkSnapshot,
			),
		); err != nil {
			return tokenSavingsSmokeReport{}, fmt.Errorf(
				"persist token savings history provider=%s backend=%s: %w",
				combo.Provider,
				combo.Backend,
				err,
			)
		}
		combinationReports = append(combinationReports, comboReport)
	}

	report := tokenSavingsSmokeReport{
		GeneratedAtUTC:    generatedAt.Format(time.RFC3339),
		Mode:              evalModeTokenSavings,
		Dataset:           fixtures.Dataset,
		SuiteVersion:      fixtures.SuiteVersion,
		FixturesDir:       fixturesDir,
		CompetitorPricing: pricing,
		CombinationCount:  len(combinationReports),
		Combinations:      combinationReports,
	}
	if len(combinationReports) > 0 {
		report.IndexedRepo = combinationReports[0].IndexedRepo
		report.FileCount = combinationReports[0].FileCount
		report.Cases = cloneTokenSavingsCaseReports(combinationReports[0].Cases)
		report.Aggregate = combinationReports[0].Aggregate
	}
	return report, nil
}

func runTokenSavingsCombination(
	ctx context.Context,
	cfg config.Config,
	combo evalCombination,
	fixtures tokenSavingsFixtureSet,
	pricing map[string]config.SavingsCompetitorPricing,
	materializedRoot string,
	documentsByPath map[string]fixtureDocument,
	historicalRuns []storage.SavingsBenchmarkRun,
	generatedAt time.Time,
	keepData bool,
	useDependencies bool,
) (tokenSavingsCombinationReport, telemetry.PersistedCumulativeSnapshot, error) {
	storagePath, cleanupStorage, err := resolveStoragePath("", keepData)
	if err != nil {
		return tokenSavingsCombinationReport{}, telemetry.PersistedCumulativeSnapshot{}, fmt.Errorf("resolve benchmark storage path: %w", err)
	}
	defer cleanupStorage()

	store, err := storage.NewSQLiteIndexStore(storagePath)
	if err != nil {
		return tokenSavingsCombinationReport{}, telemetry.PersistedCumulativeSnapshot{}, fmt.Errorf("create benchmark index store: %w", err)
	}

	tracker := telemetry.NewTracker(telemetry.PricingFromSavings(pricing), nowUTCFn)

	serviceCfg := cfg
	serviceCfg.StoragePath = storagePath
	if serviceCfg.Disabled == nil {
		serviceCfg.Disabled = map[string]struct{}{}
	}

	deps := orchestration.Dependencies{
		IndexStore: store,
		Telemetry:  tracker,
	}
	cleanupVectorDeps := func() {}
	if useDependencies {
		vectorBackend, embedder, cleanup, err := createTokenSavingsVectorDependencies(serviceCfg, combo)
		if err != nil {
			return tokenSavingsCombinationReport{}, telemetry.PersistedCumulativeSnapshot{}, err
		}
		deps.VectorBackend = vectorBackend
		deps.Embedder = embedder
		cleanupVectorDeps = cleanup
	}
	defer cleanupVectorDeps()

	service := orchestration.New(serviceCfg, deps)

	indexPayload := service.CallTool(ctx, "index_folder", map[string]any{
		"path":             materializedRoot,
		"incremental":      false,
		"use_ai_summaries": false,
	})
	if !payloadBool(indexPayload, "success") {
		return tokenSavingsCombinationReport{}, telemetry.PersistedCumulativeSnapshot{}, fmt.Errorf("index fixture corpus: %s", payloadError(indexPayload))
	}

	repoID := payloadString(indexPayload, "repo")
	if repoID == "" {
		return tokenSavingsCombinationReport{}, telemetry.PersistedCumulativeSnapshot{}, errors.New("index fixture corpus: missing repo id")
	}
	reportedRepoID := buildTokenSavingsReportRepoID(fixtures.Dataset)

	executionCtx := tokenSavingsExecutionContext{
		service:         service,
		repoID:          repoID,
		reportedRepoID:  reportedRepoID,
		documentsByPath: documentsByPath,
		pricing:         pricing,
	}

	caseReports := make([]tokenSavingsCaseReport, 0, len(fixtures.Cases))
	totalWithInput := 0
	totalWithOutput := 0
	totalWithoutInput := 0

	for _, benchmarkCase := range fixtures.Cases {
		enabledModes := buildTokenSavingsModeSet(benchmarkCase.Modes)
		promptTokens := estimateTokenSavingsPromptTokens(benchmarkCase.Prompt)
		modeResults := make(map[string]tokenSavingsModeCaseMetrics, len(enabledModes))

		for _, adapter := range tokenSavingsModeAdapters {
			if _, ok := enabledModes[adapter.name]; !ok {
				continue
			}
			result, err := adapter.execute(ctx, executionCtx, benchmarkCase, promptTokens)
			if err != nil {
				return tokenSavingsCombinationReport{}, telemetry.PersistedCumulativeSnapshot{}, fmt.Errorf(
					"run case %q mode %s: %w",
					benchmarkCase.ID,
					adapter.name,
					err,
				)
			}
			modeResults[adapter.name] = result
		}

		withMode, ok := modeResults[tokenSavingsModeWithMCP]
		if !ok {
			return tokenSavingsCombinationReport{}, telemetry.PersistedCumulativeSnapshot{}, fmt.Errorf(
				"run case %q: missing %s mode result",
				benchmarkCase.ID,
				tokenSavingsModeWithMCP,
			)
		}
		withoutMode, ok := modeResults[tokenSavingsModeWithoutMCP]
		if !ok {
			return tokenSavingsCombinationReport{}, telemetry.PersistedCumulativeSnapshot{}, fmt.Errorf(
				"run case %q: missing %s mode result",
				benchmarkCase.ID,
				tokenSavingsModeWithoutMCP,
			)
		}

		totalWithInput += withMode.InputTokens
		totalWithOutput += withMode.OutputTokens
		totalWithoutInput += withoutMode.InputTokens

		savingsReport := buildTokenSavingsDeltaReport(
			withMode.TotalTokens,
			withMode.CostUSD,
			withoutMode.TotalTokens,
			withoutMode.CostUSD,
			pricing,
		)

		caseReports = append(caseReports, tokenSavingsCaseReport{
			ID:            benchmarkCase.ID,
			Prompt:        benchmarkCase.Prompt,
			Modes:         append([]string(nil), benchmarkCase.Modes...),
			Tool:          benchmarkCase.Tool,
			ToolArguments: cloneAnyMap(benchmarkCase.Arguments),
			ContextFiles:  append([]string(nil), benchmarkCase.ContextFiles...),
			WithMCP:       withMode,
			WithoutMCP:    withoutMode,
			Savings:       savingsReport,
		})
	}

	aggregateWith := tokenSavingsModeAggregateMetrics{
		InputTokens:  totalWithInput,
		OutputTokens: totalWithOutput,
		TotalTokens:  totalWithInput + totalWithOutput,
		CostUSD:      savings.CostsForTokens(pricing, totalWithInput, totalWithOutput),
	}
	aggregateWithout := tokenSavingsModeAggregateMetrics{
		InputTokens:  totalWithoutInput,
		OutputTokens: 0,
		TotalTokens:  totalWithoutInput,
		CostUSD:      savings.CostsForTokens(pricing, totalWithoutInput, 0),
	}
	aggregateSavings := buildTokenSavingsDeltaReport(
		aggregateWith.TotalTokens,
		aggregateWith.CostUSD,
		aggregateWithout.TotalTokens,
		aggregateWithout.CostUSD,
		pricing,
	)

	benchmarkSnapshot := telemetry.PersistedCumulativeSnapshot{
		CapturedAt: generatedAt.UTC(),
		Cumulative: tracker.CumulativeSnapshot(),
	}

	return tokenSavingsCombinationReport{
		Provider:    combo.Provider,
		Backend:     combo.Backend,
		Model:       configuredEmbeddingModel(cfg, combo.Provider),
		IndexedRepo: reportedRepoID,
		FileCount:   len(fixtures.Documents),
		Cases:       caseReports,
		Aggregate: tokenSavingsAggregateReport{
			CaseCount:    len(caseReports),
			WithMCP:      aggregateWith,
			WithoutMCP:   aggregateWithout,
			Savings:      aggregateSavings,
			Distribution: buildTokenSavingsDistributionReport(caseReports, pricing),
			Trends:       buildTokenSavingsTrendSeriesFromBenchmarkRuns(historicalRuns, generatedAt, aggregateSavings, pricing),
		},
	}, benchmarkSnapshot, nil
}

func createTokenSavingsVectorDependencies(
	cfg config.Config,
	combo evalCombination,
) (indexing.VectorBackend, indexing.Embedder, func(), error) {
	vectorBackend, err := createBackendFn(cfg, combo.Backend)
	if err != nil {
		return nil, nil, func() {}, fmt.Errorf("initialize vector backend: %w", err)
	}

	cleanup := func() {
		_ = closeBackendFn(vectorBackend)
	}

	embedder, err := createEmbedderFn(cfg, combo.Provider)
	if err != nil {
		cleanup()
		return nil, nil, func() {}, fmt.Errorf("initialize embedder: %w", err)
	}

	return vectorBackend, embedder, cleanup, nil
}

func runTokenSavingsWithMCPMode(
	ctx context.Context,
	executionCtx tokenSavingsExecutionContext,
	benchmarkCase tokenSavingsCaseFixture,
	promptTokens int,
) (tokenSavingsModeCaseMetrics, error) {
	toolArgs := cloneAnyMap(benchmarkCase.Arguments)
	toolArgs["repo"] = executionCtx.repoID
	response := executionCtx.service.CallTool(ctx, benchmarkCase.Tool, toolArgs)
	if errMsg := payloadError(response); errMsg != "" {
		return tokenSavingsModeCaseMetrics{}, fmt.Errorf("run tool %s: %s", benchmarkCase.Tool, errMsg)
	}

	requestTokens := estimateSerializedTokensForReport(map[string]any{
		"name": benchmarkCase.Tool,
		"arguments": buildTokenSavingsRequestArguments(
			benchmarkCase.Arguments,
			executionCtx.reportedRepoID,
		),
	})
	responseTokens := estimateSerializedTokensForReport(
		canonicalizeTokenSavingsResponsePayload(response),
	)
	inputTokens := promptTokens + requestTokens

	return tokenSavingsModeCaseMetrics{
		InputTokens:        inputTokens,
		OutputTokens:       responseTokens,
		TotalTokens:        inputTokens + responseTokens,
		ToolRequestTokens:  requestTokens,
		ToolResponseTokens: responseTokens,
		CostUSD:            savings.CostsForTokens(executionCtx.pricing, inputTokens, responseTokens),
	}, nil
}

func runTokenSavingsWithoutMCPMode(
	_ context.Context,
	executionCtx tokenSavingsExecutionContext,
	benchmarkCase tokenSavingsCaseFixture,
	promptTokens int,
) (tokenSavingsModeCaseMetrics, error) {
	contextFiles := buildWithoutMCPContextFiles(benchmarkCase.ContextFiles, executionCtx.documentsByPath)
	contextTokens := estimateSerializedTokensForReport(map[string]any{
		"context_files": contextFiles,
	})
	inputTokens := promptTokens + contextTokens

	return tokenSavingsModeCaseMetrics{
		InputTokens:   inputTokens,
		OutputTokens:  0,
		TotalTokens:   inputTokens,
		ContextTokens: contextTokens,
		CostUSD:       savings.CostsForTokens(executionCtx.pricing, inputTokens, 0),
	}, nil
}

func loadTokenSavingsFixtures(dir string) (tokenSavingsFixtureSet, error) {
	corpus := fixtureCorpus{}
	if err := readJSONFile(filepath.Join(dir, "corpus.json"), &corpus); err != nil {
		return tokenSavingsFixtureSet{}, err
	}

	suite := tokenSavingsPromptSuiteFile{}
	if err := readJSONFile(filepath.Join(dir, tokenSavingsPromptSuiteFileName), &suite); err != nil {
		return tokenSavingsFixtureSet{}, err
	}

	if strings.TrimSpace(corpus.Dataset) == "" {
		return tokenSavingsFixtureSet{}, errors.New("token savings corpus dataset must be non-empty")
	}
	if corpus.Dataset != strings.TrimSpace(suite.Dataset) {
		return tokenSavingsFixtureSet{}, fmt.Errorf(
			"token savings fixture dataset mismatch: corpus=%q prompt_suite=%q",
			corpus.Dataset,
			suite.Dataset,
		)
	}
	if strings.TrimSpace(suite.SuiteVersion) == "" {
		return tokenSavingsFixtureSet{}, errors.New("token savings prompt suite version must be non-empty")
	}
	if len(corpus.Documents) == 0 {
		return tokenSavingsFixtureSet{}, errors.New("token savings corpus documents must be non-empty")
	}
	if len(suite.Cases) == 0 {
		return tokenSavingsFixtureSet{}, errors.New("token savings prompt suite cases must be non-empty")
	}

	docPaths := make(map[string]struct{}, len(corpus.Documents))
	docIDs := make(map[string]struct{}, len(corpus.Documents))
	for index, doc := range corpus.Documents {
		doc.ID = strings.TrimSpace(doc.ID)
		doc.Language = strings.TrimSpace(doc.Language)
		doc.Text = strings.TrimSpace(doc.Text)
		cleanPath, err := normalizeFixtureRelativePath(doc.Path)
		if err != nil {
			return tokenSavingsFixtureSet{}, fmt.Errorf("normalize corpus document path %q: %w", doc.Path, err)
		}
		doc.Path = cleanPath
		if doc.ID == "" || doc.Language == "" || doc.Text == "" {
			return tokenSavingsFixtureSet{}, fmt.Errorf("token savings document at index %d includes empty required fields", index)
		}
		if _, exists := docIDs[doc.ID]; exists {
			return tokenSavingsFixtureSet{}, fmt.Errorf("duplicate token savings document id %q", doc.ID)
		}
		if _, exists := docPaths[doc.Path]; exists {
			return tokenSavingsFixtureSet{}, fmt.Errorf("duplicate token savings document path %q", doc.Path)
		}
		docIDs[doc.ID] = struct{}{}
		docPaths[doc.Path] = struct{}{}
		corpus.Documents[index] = doc
	}

	caseIDs := make(map[string]struct{}, len(suite.Cases))
	for index, benchmarkCase := range suite.Cases {
		benchmarkCase.ID = strings.TrimSpace(benchmarkCase.ID)
		benchmarkCase.Prompt = strings.TrimSpace(benchmarkCase.Prompt)
		benchmarkCase.Tool = strings.TrimSpace(benchmarkCase.Tool)
		if benchmarkCase.ID == "" || benchmarkCase.Prompt == "" || benchmarkCase.Tool == "" {
			return tokenSavingsFixtureSet{}, fmt.Errorf("token savings case at index %d includes empty required fields", index)
		}
		if _, exists := caseIDs[benchmarkCase.ID]; exists {
			return tokenSavingsFixtureSet{}, fmt.Errorf("duplicate token savings case id %q", benchmarkCase.ID)
		}
		if _, supported := tokenSavingsSupportedTools[benchmarkCase.Tool]; !supported {
			return tokenSavingsFixtureSet{}, fmt.Errorf("unsupported token savings tool %q", benchmarkCase.Tool)
		}
		if len(benchmarkCase.ContextFiles) == 0 {
			return tokenSavingsFixtureSet{}, fmt.Errorf("token savings case %q must include at least one context file", benchmarkCase.ID)
		}
		normalizedModes, err := normalizeTokenSavingsModes(benchmarkCase.Modes)
		if err != nil {
			return tokenSavingsFixtureSet{}, fmt.Errorf("normalize token savings case %q modes: %w", benchmarkCase.ID, err)
		}
		benchmarkCase.Modes = normalizedModes
		if benchmarkCase.Arguments == nil {
			benchmarkCase.Arguments = map[string]any{}
		}
		if _, exists := benchmarkCase.Arguments["repo"]; exists {
			return tokenSavingsFixtureSet{}, fmt.Errorf("token savings case %q must not set repo explicitly", benchmarkCase.ID)
		}
		for fileIndex, rawPath := range benchmarkCase.ContextFiles {
			cleanPath, err := normalizeFixtureRelativePath(rawPath)
			if err != nil {
				return tokenSavingsFixtureSet{}, fmt.Errorf("normalize token savings case %q context file %q: %w", benchmarkCase.ID, rawPath, err)
			}
			if _, exists := docPaths[cleanPath]; !exists {
				return tokenSavingsFixtureSet{}, fmt.Errorf("token savings case %q references unknown context file %q", benchmarkCase.ID, cleanPath)
			}
			benchmarkCase.ContextFiles[fileIndex] = cleanPath
		}
		caseIDs[benchmarkCase.ID] = struct{}{}
		suite.Cases[index] = benchmarkCase
	}

	return tokenSavingsFixtureSet{
		Dataset:      corpus.Dataset,
		SuiteVersion: suite.SuiteVersion,
		Documents:    corpus.Documents,
		Cases:        suite.Cases,
	}, nil
}

func normalizeTokenSavingsModes(rawModes []string) ([]string, error) {
	if len(rawModes) == 0 {
		return nil, errors.New("modes must be non-empty")
	}

	seen := make(map[string]struct{}, len(rawModes))
	for _, rawMode := range rawModes {
		mode := strings.ToLower(strings.TrimSpace(rawMode))
		if mode == "" {
			return nil, errors.New("mode must be non-empty")
		}
		if !isSupportedTokenSavingsMode(mode) {
			return nil, fmt.Errorf("unsupported mode %q", rawMode)
		}
		if _, exists := seen[mode]; exists {
			return nil, fmt.Errorf("duplicate mode %q", mode)
		}
		seen[mode] = struct{}{}
	}

	for _, requiredMode := range tokenSavingsRequiredModes {
		if _, ok := seen[requiredMode]; !ok {
			return nil, fmt.Errorf("must include %q", requiredMode)
		}
	}

	normalized := make([]string, 0, len(tokenSavingsRequiredModes))
	for _, requiredMode := range tokenSavingsRequiredModes {
		normalized = append(normalized, requiredMode)
	}
	return normalized, nil
}

func buildTokenSavingsModeSet(modes []string) map[string]struct{} {
	modeSet := make(map[string]struct{}, len(modes))
	for _, mode := range modes {
		modeSet[mode] = struct{}{}
	}
	return modeSet
}

func isSupportedTokenSavingsMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case tokenSavingsModeWithMCP, tokenSavingsModeWithoutMCP:
		return true
	default:
		return false
	}
}

func materializeTokenSavingsCorpus(documents []fixtureDocument) (string, func(), error) {
	root, err := os.MkdirTemp("", "gocodemunch-token-savings-*")
	if err != nil {
		return "", func() {}, err
	}

	cleanup := func() {
		_ = os.RemoveAll(root)
	}

	for _, doc := range documents {
		relativePath, err := normalizeFixtureRelativePath(doc.Path)
		if err != nil {
			cleanup()
			return "", func() {}, err
		}
		targetPath := filepath.Join(root, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			cleanup()
			return "", func() {}, err
		}
		if err := os.WriteFile(targetPath, []byte(doc.Text), 0o644); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}

	return root, cleanup, nil
}

func normalizeFixtureRelativePath(raw string) (string, error) {
	normalized := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
	switch {
	case normalized == "", normalized == ".":
		return "", errors.New("path must be non-empty")
	case strings.HasPrefix(normalized, "../"):
		return "", errors.New("path must stay within the fixture root")
	case filepath.IsAbs(raw):
		return "", errors.New("path must be relative")
	default:
		return normalized, nil
	}
}

func buildWithoutMCPContextFiles(
	contextFiles []string,
	documentsByPath map[string]fixtureDocument,
) []tokenSavingsContextFile {
	context := make([]tokenSavingsContextFile, 0, len(contextFiles))
	for _, path := range contextFiles {
		doc := documentsByPath[path]
		context = append(context, tokenSavingsContextFile{
			Path:    path,
			Content: doc.Text,
		})
	}
	return context
}

func estimateTokenSavingsPromptTokens(prompt string) int {
	return estimateSerializedTokensForReport(map[string]any{
		"prompt": prompt,
	})
}

func estimateSerializedTokensForReport(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 {
		return 0
	}
	return int(math.Ceil(float64(len(encoded)) / serializedBytesPerEstimatedToken))
}

func savingsRatio(baseline int, delta int) float64 {
	if baseline <= 0 {
		return 0
	}
	return math.Round((float64(delta)/float64(baseline))*1_000_000) / 1_000_000
}

func buildTokenSavingsDeltaReport(
	withTotalTokens int,
	withCost map[string]float64,
	withoutTotalTokens int,
	withoutCost map[string]float64,
	pricing map[string]config.SavingsCompetitorPricing,
) tokenSavingsDeltaReport {
	tokensSaved := withoutTotalTokens - withTotalTokens
	savingsPct := savingsRatio(withoutTotalTokens, tokensSaved)
	costSaved := savings.DiffCostMap(withoutCost, withCost, pricing)
	return tokenSavingsDeltaReport{
		TokensSaved:  tokensSaved,
		SavingsPct:   savingsPct,
		CostSavedUSD: costSaved,
		Scores:       buildTokenSavingsCompetitorScores(tokensSaved, savingsPct, costSaved, pricing),
	}
}

func buildTokenSavingsCompetitorScores(
	tokensSaved int,
	savingsPct float64,
	costSaved map[string]float64,
	pricing map[string]config.SavingsCompetitorPricing,
) map[string]tokenSavingsCompetitorScore {
	competitors := savings.OrderedCompetitors(pricing)
	if len(competitors) == 0 {
		return map[string]tokenSavingsCompetitorScore{}
	}

	scores := make(map[string]tokenSavingsCompetitorScore, len(competitors))
	for _, competitor := range competitors {
		scores[competitor] = tokenSavingsCompetitorScore{
			TokensSaved:  tokensSaved,
			CostSavedUSD: roundTokenSavingsFloat(costSaved[competitor]),
			SavingsPct:   savingsPct,
		}
	}
	return scores
}

func buildTokenSavingsDistributionReport(
	caseReports []tokenSavingsCaseReport,
	pricing map[string]config.SavingsCompetitorPricing,
) tokenSavingsDistributionReport {
	tokenSamples := make([]float64, 0, len(caseReports))
	savingsPctSamples := make([]float64, 0, len(caseReports))
	competitorCostSamples := make(map[string][]float64, len(pricing))
	for _, competitor := range savings.OrderedCompetitors(pricing) {
		competitorCostSamples[competitor] = make([]float64, 0, len(caseReports))
	}

	for _, row := range caseReports {
		tokenSamples = append(tokenSamples, float64(row.Savings.TokensSaved))
		savingsPctSamples = append(savingsPctSamples, row.Savings.SavingsPct)
		for competitor := range competitorCostSamples {
			competitorCostSamples[competitor] = append(
				competitorCostSamples[competitor],
				row.Savings.CostSavedUSD[competitor],
			)
		}
	}

	costDistribution := make(map[string]tokenSavingsDistributionMetric, len(competitorCostSamples))
	for competitor, samples := range competitorCostSamples {
		costDistribution[competitor] = buildTokenSavingsDistributionMetric(samples)
	}

	return tokenSavingsDistributionReport{
		TokensSaved:  buildTokenSavingsDistributionMetric(tokenSamples),
		SavingsPct:   buildTokenSavingsDistributionMetric(savingsPctSamples),
		CostSavedUSD: costDistribution,
	}
}

func buildTokenSavingsDistributionMetric(samples []float64) tokenSavingsDistributionMetric {
	if len(samples) == 0 {
		return tokenSavingsDistributionMetric{}
	}

	total := 0.0
	for _, sample := range samples {
		total += sample
	}
	return tokenSavingsDistributionMetric{
		Mean:   roundTokenSavingsFloat(total / float64(len(samples))),
		Median: roundTokenSavingsFloat(tokenSavingsPercentile(samples, 50)),
		P95:    roundTokenSavingsFloat(tokenSavingsPercentile(samples, 95)),
	}
}

func buildTokenSavingsTrendSeriesFromBenchmarkRuns(
	history []storage.SavingsBenchmarkRun,
	generatedAt time.Time,
	currentSavings tokenSavingsDeltaReport,
	pricing map[string]config.SavingsCompetitorPricing,
) map[string][]tokenSavingsTrendPoint {
	competitors := savings.OrderedCompetitors(pricing)
	if len(competitors) == 0 {
		return map[string][]tokenSavingsTrendPoint{}
	}

	trends := make(map[string][]tokenSavingsTrendPoint, len(competitors))
	for _, competitor := range competitors {
		trends[competitor] = make([]tokenSavingsTrendPoint, 0, len(history)+1)
		for _, run := range history {
			score, ok := run.CompetitorScores[competitor]
			if !ok {
				score = storage.SavingsBenchmarkCompetitorScore{
					TokensSaved: run.TokensSaved,
					SavingsPct:  run.SavingsPct,
				}
			}
			trends[competitor] = append(trends[competitor], tokenSavingsTrendPoint{
				CapturedAtUTC: run.CapturedAt.UTC().Format(time.RFC3339),
				TokensSaved:   score.TokensSaved,
				CostSavedUSD:  roundTokenSavingsFloat(score.CostSavedUSD),
				SavingsPct:    roundTokenSavingsFloat(score.SavingsPct),
			})
		}
		currentTokensSaved := currentSavings.TokensSaved
		if score, ok := currentSavings.Scores[competitor]; ok {
			currentTokensSaved = score.TokensSaved
		}
		trends[competitor] = append(trends[competitor], tokenSavingsTrendPoint{
			CapturedAtUTC: generatedAt.UTC().Format(time.RFC3339),
			TokensSaved:   currentTokensSaved,
			CostSavedUSD:  roundTokenSavingsFloat(currentSavings.CostSavedUSD[competitor]),
			SavingsPct:    roundTokenSavingsFloat(currentSavings.SavingsPct),
		})
	}
	return trends
}

func persistTokenSavingsBenchmarkRun(
	ctx context.Context,
	storagePath string,
	run storage.SavingsBenchmarkRun,
) error {
	if strings.TrimSpace(storagePath) == "" {
		return nil
	}

	store, err := storage.NewSQLiteTelemetryStore(storagePath)
	if err != nil {
		return err
	}
	return store.SaveSavingsBenchmarkRun(ctx, run)
}

func loadTokenSavingsHistoricalRuns(
	ctx context.Context,
	storagePath string,
	filter storage.SavingsBenchmarkRunFilter,
) ([]storage.SavingsBenchmarkRun, error) {
	if strings.TrimSpace(storagePath) == "" {
		return nil, nil
	}

	store, err := storage.NewSQLiteTelemetryStore(storagePath)
	if err != nil {
		return nil, err
	}
	return store.LoadSavingsBenchmarkRuns(ctx, filter)
}

func buildTokenSavingsTrendSeries(
	historicalSnapshots []telemetry.PersistedCumulativeSnapshot,
	generatedAt time.Time,
	withTotalTokens int,
	currentSavings tokenSavingsDeltaReport,
	pricing map[string]config.SavingsCompetitorPricing,
) map[string][]tokenSavingsTrendPoint {
	competitors := savings.OrderedCompetitors(pricing)
	if len(competitors) == 0 {
		return map[string][]tokenSavingsTrendPoint{}
	}

	trends := make(map[string][]tokenSavingsTrendPoint, len(competitors))
	for _, competitor := range competitors {
		trends[competitor] = make([]tokenSavingsTrendPoint, 0, len(historicalSnapshots)+1)
	}

	var previous telemetry.PersistedCumulativeSnapshot
	hasPrevious := false
	for _, snapshot := range historicalSnapshots {
		delta := buildTokenSavingsSnapshotDelta(snapshot, previous, hasPrevious, pricing)
		hasPrevious = true
		previous = snapshot

		baselineTokens := delta.TotalTokens + delta.TokensSaved
		savingsPct := savingsRatio(baselineTokens, delta.TokensSaved)
		for _, competitor := range competitors {
			trends[competitor] = append(trends[competitor], tokenSavingsTrendPoint{
				CapturedAtUTC: snapshot.CapturedAt.UTC().Format(time.RFC3339),
				TokensSaved:   delta.TokensSaved,
				CostSavedUSD:  roundTokenSavingsFloat(delta.CostSavedUSD[competitor]),
				SavingsPct:    savingsPct,
			})
		}
	}

	for _, competitor := range competitors {
		trends[competitor] = append(trends[competitor], tokenSavingsTrendPoint{
			CapturedAtUTC: generatedAt.UTC().Format(time.RFC3339),
			TokensSaved:   currentSavings.TokensSaved,
			CostSavedUSD:  roundTokenSavingsFloat(currentSavings.CostSavedUSD[competitor]),
			SavingsPct:    savingsRatio(withTotalTokens+currentSavings.TokensSaved, currentSavings.TokensSaved),
		})
	}
	return trends
}

type tokenSavingsSnapshotDelta struct {
	TotalTokens  int
	TokensSaved  int
	CostSavedUSD map[string]float64
}

func buildTokenSavingsSnapshotDelta(
	current telemetry.PersistedCumulativeSnapshot,
	previous telemetry.PersistedCumulativeSnapshot,
	hasPrevious bool,
	pricing map[string]config.SavingsCompetitorPricing,
) tokenSavingsSnapshotDelta {
	normalizedCurrentCost := normalizeTokenSavingsCostMap(current.Cumulative.CostAvoidedUSD, pricing)
	if !hasPrevious {
		return tokenSavingsSnapshotDelta{
			TotalTokens:  max(0, current.Cumulative.TotalTokens),
			TokensSaved:  max(0, current.Cumulative.TokensSaved),
			CostSavedUSD: normalizedCurrentCost,
		}
	}

	if current.Cumulative.TotalTokens < previous.Cumulative.TotalTokens ||
		current.Cumulative.TokensSaved < previous.Cumulative.TokensSaved {
		return tokenSavingsSnapshotDelta{
			TotalTokens:  max(0, current.Cumulative.TotalTokens),
			TokensSaved:  max(0, current.Cumulative.TokensSaved),
			CostSavedUSD: normalizedCurrentCost,
		}
	}

	normalizedPreviousCost := normalizeTokenSavingsCostMap(previous.Cumulative.CostAvoidedUSD, pricing)
	return tokenSavingsSnapshotDelta{
		TotalTokens:  current.Cumulative.TotalTokens - previous.Cumulative.TotalTokens,
		TokensSaved:  current.Cumulative.TokensSaved - previous.Cumulative.TokensSaved,
		CostSavedUSD: savings.DiffCostMap(normalizedCurrentCost, normalizedPreviousCost, pricing),
	}
}

func normalizeTokenSavingsCostMap(
	input map[string]float64,
	pricing map[string]config.SavingsCompetitorPricing,
) map[string]float64 {
	normalized := make(map[string]float64, len(pricing))
	for _, competitor := range savings.OrderedCompetitors(pricing) {
		normalized[competitor] = roundTokenSavingsFloat(input[competitor])
	}
	return normalized
}

func loadTokenSavingsHistoricalSnapshots(
	ctx context.Context,
	storagePath string,
) ([]telemetry.PersistedCumulativeSnapshot, error) {
	if strings.TrimSpace(storagePath) == "" {
		return nil, nil
	}

	store, err := storage.NewSQLiteTelemetryStore(storagePath)
	if err != nil {
		return nil, err
	}
	return store.LoadSnapshots(ctx, time.Time{})
}

func tokenSavingsPercentile(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if len(values) == 1 {
		return values[0]
	}

	clamped := math.Max(0, math.Min(100, percentile))
	sorted := slices.Clone(values)
	slices.Sort(sorted)

	position := (clamped / 100.0) * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}

	weight := position - float64(lower)
	return sorted[lower] + (sorted[upper]-sorted[lower])*weight
}

func roundTokenSavingsFloat(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}

func cloneAnyMap(source map[string]any) map[string]any {
	if len(source) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func payloadBool(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func payloadError(payload map[string]any) string {
	return payloadString(payload, "error")
}

func cloneTokenSavingsCaseReports(input []tokenSavingsCaseReport) []tokenSavingsCaseReport {
	if len(input) == 0 {
		return nil
	}

	cloned := make([]tokenSavingsCaseReport, 0, len(input))
	for _, row := range input {
		rowCopy := row
		rowCopy.Modes = append([]string(nil), row.Modes...)
		rowCopy.ToolArguments = cloneAnyMap(row.ToolArguments)
		rowCopy.ContextFiles = append([]string(nil), row.ContextFiles...)
		rowCopy.WithMCP.CostUSD = cloneFloatMap(row.WithMCP.CostUSD)
		rowCopy.WithoutMCP.CostUSD = cloneFloatMap(row.WithoutMCP.CostUSD)
		rowCopy.Savings.CostSavedUSD = cloneFloatMap(row.Savings.CostSavedUSD)
		rowCopy.Savings.Scores = cloneTokenSavingsCompetitorScores(row.Savings.Scores)
		cloned = append(cloned, rowCopy)
	}
	return cloned
}

func cloneFloatMap(input map[string]float64) map[string]float64 {
	if len(input) == 0 {
		return map[string]float64{}
	}
	cloned := make(map[string]float64, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func cloneTokenSavingsCompetitorScores(
	input map[string]tokenSavingsCompetitorScore,
) map[string]tokenSavingsCompetitorScore {
	if len(input) == 0 {
		return map[string]tokenSavingsCompetitorScore{}
	}
	cloned := make(map[string]tokenSavingsCompetitorScore, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func writeTokenSavingsMarkdownRunReport(
	rawDir string,
	report tokenSavingsSmokeReport,
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

	fileName := buildMarkdownReportFileName(report.Dataset, report.GeneratedAtUTC)
	reportPath := filepath.Join(reportDir, fileName)
	relatedLinks, err := collectTokenSavingsMarkdownRelatedLinks(reportDir, reportPath)
	if err != nil {
		return "", err
	}
	content := renderTokenSavingsMarkdownRunReport(report, jsonOutPath, relatedLinks)
	if err := os.WriteFile(reportPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write token savings markdown report %q: %w", reportPath, err)
	}

	return reportPath, nil
}

func writeTokenSavingsIndex(markdownReportDir string, markdownReportPath string, generatedAtUTC string) (string, error) {
	return writeMarkdownRunIndex(
		markdownReportDir,
		markdownReportPath,
		generatedAtUTC,
		tokenSavingsIndexFileName,
		renderTokenSavingsIndexMarkdown,
	)
}

func renderTokenSavingsMarkdownRunReport(
	report tokenSavingsSmokeReport,
	jsonOutPath string,
	relatedLinks []string,
) string {
	createdDate := createdDateFromRFC3339(report.GeneratedAtUTC)
	title := fmt.Sprintf(
		"Token Savings Run %s %s",
		strings.TrimSpace(report.Dataset),
		strings.TrimSpace(report.GeneratedAtUTC),
	)
	tags := collectTokenSavingsMarkdownTags(report)
	competitors := orderedTokenSavingsCompetitors(report.CompetitorPricing)
	combinations := tokenSavingsReportCombinations(report)

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
	b.WriteString(fmt.Sprintf("- Mode: `%s`\n", report.Mode))
	b.WriteString(fmt.Sprintf("- Dataset: `%s`\n", report.Dataset))
	b.WriteString(fmt.Sprintf("- Suite Version: `%s`\n", report.SuiteVersion))
	b.WriteString(fmt.Sprintf("- Fixtures Dir: `%s`\n", report.FixturesDir))
	b.WriteString(fmt.Sprintf("- Combination Count: `%d`\n", len(combinations)))
	if outPath := strings.TrimSpace(jsonOutPath); outPath != "" {
		b.WriteString(fmt.Sprintf("- JSON Artifact: `%s`\n", outPath))
	}
	if len(combinations) == 1 {
		combo := combinations[0]
		if combo.Provider != "" {
			b.WriteString(fmt.Sprintf("- Provider: `%s`\n", combo.Provider))
		}
		if combo.Backend != "" {
			b.WriteString(fmt.Sprintf("- Backend: `%s`\n", combo.Backend))
		}
		if combo.Model != "" {
			b.WriteString(fmt.Sprintf("- Model: `%s`\n", combo.Model))
		}
		b.WriteString(fmt.Sprintf("- Indexed Repo: `%s`\n", combo.IndexedRepo))
		b.WriteString(fmt.Sprintf("- File Count: `%d`\n", combo.FileCount))
		b.WriteString(fmt.Sprintf("- Tokens Saved: `%d`\n", combo.Aggregate.Savings.TokensSaved))
		b.WriteString(fmt.Sprintf("- Savings Percentage: `%s`\n", formatSavingsPctForMarkdown(combo.Aggregate.Savings.SavingsPct)))
	}

	b.WriteString("\n## Combination Summary\n\n")
	b.WriteString("| Provider | Backend | Model | Indexed Repo | File Count | Tokens Saved | Savings % |\n")
	b.WriteString("| --- | --- | --- | --- | ---: | ---: | ---: |\n")
	for _, combo := range combinations {
		b.WriteString(fmt.Sprintf(
			"| %s | %s | %s | %s | %d | %d | %s |\n",
			tokenSavingsMarkdownValue(combo.Provider),
			tokenSavingsMarkdownValue(combo.Backend),
			tokenSavingsMarkdownValue(combo.Model),
			tokenSavingsMarkdownValue(combo.IndexedRepo),
			combo.FileCount,
			combo.Aggregate.Savings.TokensSaved,
			formatSavingsPctForMarkdown(combo.Aggregate.Savings.SavingsPct),
		))
	}

	if len(competitors) > 0 {
		b.WriteString("\n## Competitor Pricing\n\n")
		b.WriteString("| Competitor | Input USD / MTok | Output USD / MTok |\n")
		b.WriteString("| --- | ---: | ---: |\n")
		for _, competitor := range competitors {
			rate := report.CompetitorPricing[competitor]
			b.WriteString(fmt.Sprintf(
				"| %s | %.6f | %.6f |\n",
				competitor,
				rate.InputUSDPerMTok,
				rate.OutputUSDPerMTok,
			))
		}
	}

	b.WriteString("\n## Combination Details\n")
	for _, combo := range combinations {
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf(
			"### %s / %s\n\n",
			tokenSavingsMarkdownValue(combo.Provider),
			tokenSavingsMarkdownValue(combo.Backend),
		))
		if combo.Model != "" {
			b.WriteString(fmt.Sprintf("- Model: `%s`\n", combo.Model))
		}
		b.WriteString(fmt.Sprintf("- Indexed Repo: `%s`\n", combo.IndexedRepo))
		b.WriteString(fmt.Sprintf("- File Count: `%d`\n", combo.FileCount))
		b.WriteString(fmt.Sprintf("- Tokens Saved: `%d`\n", combo.Aggregate.Savings.TokensSaved))
		b.WriteString(fmt.Sprintf("- Savings Percentage: `%s`\n", formatSavingsPctForMarkdown(combo.Aggregate.Savings.SavingsPct)))

		b.WriteString("\n#### Aggregate Tokens\n\n")
		b.WriteString("| Mode | Input Tokens | Output Tokens | Total Tokens |\n")
		b.WriteString("| --- | ---: | ---: | ---: |\n")
		b.WriteString(fmt.Sprintf(
			"| with_mcp | %d | %d | %d |\n",
			combo.Aggregate.WithMCP.InputTokens,
			combo.Aggregate.WithMCP.OutputTokens,
			combo.Aggregate.WithMCP.TotalTokens,
		))
		b.WriteString(fmt.Sprintf(
			"| without_mcp | %d | %d | %d |\n",
			combo.Aggregate.WithoutMCP.InputTokens,
			combo.Aggregate.WithoutMCP.OutputTokens,
			combo.Aggregate.WithoutMCP.TotalTokens,
		))

		if len(competitors) > 0 {
			b.WriteString("\n#### Aggregate Cost Savings\n\n")
			b.WriteString("| Competitor | With MCP Cost (USD) | Without MCP Cost (USD) | Cost Saved (USD) |\n")
			b.WriteString("| --- | ---: | ---: | ---: |\n")
			for _, competitor := range competitors {
				b.WriteString(fmt.Sprintf(
					"| %s | %s | %s | %s |\n",
					competitor,
					formatUSDForMarkdown(combo.Aggregate.WithMCP.CostUSD[competitor]),
					formatUSDForMarkdown(combo.Aggregate.WithoutMCP.CostUSD[competitor]),
					formatUSDForMarkdown(combo.Aggregate.Savings.CostSavedUSD[competitor]),
				))
			}
		}

		b.WriteString("\n#### Per-Case Savings\n\n")
		b.WriteString("| Case | Tool | With MCP Tokens | Without MCP Tokens | Tokens Saved | Savings % |\n")
		b.WriteString("| --- | --- | ---: | ---: | ---: | ---: |\n")
		for _, row := range combo.Cases {
			b.WriteString(fmt.Sprintf(
				"| %s | %s | %d | %d | %d | %s |\n",
				row.ID,
				row.Tool,
				row.WithMCP.TotalTokens,
				row.WithoutMCP.TotalTokens,
				row.Savings.TokensSaved,
				formatSavingsPctForMarkdown(row.Savings.SavingsPct),
			))
		}
	}

	return b.String()
}

func renderTokenSavingsIndexMarkdown(createdDate string, links []string) string {
	created := strings.TrimSpace(createdDate)
	if created == "" {
		created = nowUTCFn().Format("2006-01-02")
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: reference\n")
	b.WriteString("title: Savings Index\n")
	b.WriteString(fmt.Sprintf("created: %s\n", created))
	b.WriteString("tags:\n")
	b.WriteString("  - eval\n")
	b.WriteString("  - token-savings\n")
	b.WriteString("  - index\n")
	b.WriteString("related:\n")
	b.WriteString("  - '[[Eval-Index]]'\n")
	b.WriteString("---\n\n")
	b.WriteString("# Savings Index\n\n")
	b.WriteString("Newest-first wiki-links to token savings reports in `docs/evals/savings-runs`.\n\n")
	if len(links) == 0 {
		b.WriteString("- None yet\n")
		return b.String()
	}
	for _, link := range links {
		b.WriteString(fmt.Sprintf("- [[%s]]\n", link))
	}
	return b.String()
}

func collectTokenSavingsMarkdownTags(report tokenSavingsSmokeReport) []string {
	tagSet := map[string]struct{}{
		"eval":          {},
		"token-savings": {},
	}
	if dataset := sanitizeTagValue(report.Dataset); dataset != "" {
		tagSet["dataset-"+dataset] = struct{}{}
	}
	if mode := sanitizeTagValue(report.Mode); mode != "" {
		tagSet["mode-"+mode] = struct{}{}
	}
	if suiteVersion := sanitizeTagValue(report.SuiteVersion); suiteVersion != "" {
		tagSet["suite-"+suiteVersion] = struct{}{}
	}
	for competitor := range report.CompetitorPricing {
		if normalized := sanitizeTagValue(competitor); normalized != "" {
			tagSet["competitor-"+normalized] = struct{}{}
		}
	}
	for _, combo := range tokenSavingsReportCombinations(report) {
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

func collectTokenSavingsMarkdownRelatedLinks(reportDir string, reportPath string) ([]string, error) {
	relatedSet := map[string]struct{}{
		"Eval-Index":    {},
		"Savings-Index": {},
	}
	neighbors, err := neighboringMarkdownRunLinks(reportDir, reportPath)
	if err != nil {
		return nil, fmt.Errorf("resolve token savings markdown related links: %w", err)
	}
	for _, link := range neighbors {
		relatedSet[link] = struct{}{}
	}

	related := make([]string, 0, len(relatedSet))
	for link := range relatedSet {
		related = append(related, link)
	}
	slices.Sort(related)
	return related, nil
}

func orderedTokenSavingsCompetitors(
	pricing map[string]config.SavingsCompetitorPricing,
) []string {
	return savings.OrderedCompetitors(pricing)
}

func tokenSavingsReportCombinations(report tokenSavingsSmokeReport) []tokenSavingsCombinationReport {
	if len(report.Combinations) > 0 {
		return report.Combinations
	}
	return []tokenSavingsCombinationReport{
		{
			IndexedRepo: report.IndexedRepo,
			FileCount:   report.FileCount,
			Cases:       cloneTokenSavingsCaseReports(report.Cases),
			Aggregate:   report.Aggregate,
		},
	}
}

func tokenSavingsMarkdownValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "-"
	}
	return trimmed
}

func formatSavingsPctForMarkdown(value float64) string {
	return fmt.Sprintf("%.2f%%", value*100.0)
}

func formatUSDForMarkdown(value float64) string {
	return fmt.Sprintf("%.6f", value)
}

func buildTokenSavingsReportRepoID(dataset string) string {
	normalized := sanitizeTagValue(dataset)
	if normalized == "" {
		normalized = "fixture-corpus"
	}
	return fmt.Sprintf("token-savings-%s", normalized)
}

func buildTokenSavingsBenchmarkRunID(
	dataset string,
	suiteVersion string,
	generatedAt time.Time,
	provider string,
	backend string,
	model string,
) string {
	timestamp := strings.ToLower(generatedAt.UTC().Format("20060102-150405Z"))
	parts := []string{
		"token-savings",
		timestamp,
		sanitizeTagValue(dataset),
		sanitizeTagValue(suiteVersion),
		sanitizeTagValue(provider),
		sanitizeTagValue(backend),
		sanitizeTagValue(model),
	}

	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, "-")
}

func buildTokenSavingsStoredBenchmarkRun(
	runID string,
	fixtures tokenSavingsFixtureSet,
	report tokenSavingsCombinationReport,
	benchmarkSnapshot telemetry.PersistedCumulativeSnapshot,
) storage.SavingsBenchmarkRun {
	competitorScores := make(map[string]storage.SavingsBenchmarkCompetitorScore, len(report.Aggregate.Savings.Scores))
	for competitor, score := range report.Aggregate.Savings.Scores {
		competitorScores[competitor] = storage.SavingsBenchmarkCompetitorScore{
			TokensSaved:  score.TokensSaved,
			CostSavedUSD: roundTokenSavingsFloat(score.CostSavedUSD),
			SavingsPct:   roundTokenSavingsFloat(score.SavingsPct),
		}
	}

	return storage.SavingsBenchmarkRun{
		RunID:                  runID,
		CapturedAt:             benchmarkSnapshot.CapturedAt.UTC(),
		Dataset:                fixtures.Dataset,
		SuiteVersion:           fixtures.SuiteVersion,
		Mode:                   evalModeTokenSavings,
		Provider:               report.Provider,
		Backend:                report.Backend,
		Model:                  report.Model,
		IndexedRepo:            report.IndexedRepo,
		FileCount:              report.FileCount,
		CaseCount:              report.Aggregate.CaseCount,
		WithMCPInputTokens:     report.Aggregate.WithMCP.InputTokens,
		WithMCPOutputTokens:    report.Aggregate.WithMCP.OutputTokens,
		WithMCPTotalTokens:     report.Aggregate.WithMCP.TotalTokens,
		WithoutMCPInputTokens:  report.Aggregate.WithoutMCP.InputTokens,
		WithoutMCPOutputTokens: report.Aggregate.WithoutMCP.OutputTokens,
		WithoutMCPTotalTokens:  report.Aggregate.WithoutMCP.TotalTokens,
		TokensSaved:            report.Aggregate.Savings.TokensSaved,
		SavingsPct:             roundTokenSavingsFloat(report.Aggregate.Savings.SavingsPct),
		CompetitorScores:       competitorScores,
		TelemetrySnapshot:      benchmarkSnapshot,
	}
}

func buildTokenSavingsRequestArguments(arguments map[string]any, repoID string) map[string]any {
	cloned := cloneAnyMap(arguments)
	cloned["repo"] = repoID
	return cloned
}

func canonicalizeTokenSavingsResponsePayload(payload map[string]any) map[string]any {
	cloned := cloneAnyMap(payload)
	delete(cloned, "_meta")
	return cloned
}
