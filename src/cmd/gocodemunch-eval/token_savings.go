package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/orchestration"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/telemetry"
)

const (
	defaultTokenSavingsFixturesDir   = "tests-go/evals/fixtures/token-savings-smoke"
	defaultTokenSavingsOutputPath    = "Auto Run Docs/Working/evals/token-savings-smoke.json"
	tokenSavingsPromptSuiteFileName  = "prompt_suite.json"
	serializedBytesPerEstimatedToken = 4.0
)

var tokenSavingsSupportedTools = map[string]struct{}{
	"get_file_tree":    {},
	"get_file_outline": {},
	"search_text":      {},
	"find_importers":   {},
	"find_references":  {},
}

type tokenSavingsPromptSuiteFile struct {
	Dataset      string                    `json:"dataset"`
	SuiteVersion string                    `json:"suite_version"`
	Cases        []tokenSavingsCaseFixture `json:"cases"`
}

type tokenSavingsCaseFixture struct {
	ID           string         `json:"id"`
	Prompt       string         `json:"prompt"`
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
	Cases             []tokenSavingsCaseReport                   `json:"cases"`
	Aggregate         tokenSavingsAggregateReport                `json:"aggregate"`
}

type tokenSavingsCaseReport struct {
	ID            string                      `json:"id"`
	Prompt        string                      `json:"prompt"`
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
	TokensSaved  int                `json:"tokens_saved"`
	SavingsPct   float64            `json:"savings_pct"`
	CostSavedUSD map[string]float64 `json:"cost_saved_usd"`
}

type tokenSavingsAggregateReport struct {
	CaseCount  int                              `json:"case_count"`
	WithMCP    tokenSavingsModeAggregateMetrics `json:"with_mcp"`
	WithoutMCP tokenSavingsModeAggregateMetrics `json:"without_mcp"`
	Savings    tokenSavingsDeltaReport          `json:"savings"`
}

type tokenSavingsContextFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func runTokenSavingsSmokeMode(
	ctx context.Context,
	cfg config.Config,
	fixturesDirRaw string,
	keepData bool,
	skipMarkdownReport bool,
) (tokenSavingsSmokeReport, error) {
	if !skipMarkdownReport {
		return tokenSavingsSmokeReport{}, errors.New("markdown report generation is not supported for token-savings-smoke yet; pass --skip-markdown-report")
	}

	fixturesDir, err := resolveFixturesDir(fixturesDirRaw)
	if err != nil {
		return tokenSavingsSmokeReport{}, err
	}

	fixtures, err := loadTokenSavingsFixtures(fixturesDir)
	if err != nil {
		return tokenSavingsSmokeReport{}, err
	}

	storagePath, cleanupStorage, err := resolveStoragePath("", keepData)
	if err != nil {
		return tokenSavingsSmokeReport{}, fmt.Errorf("resolve benchmark storage path: %w", err)
	}
	defer cleanupStorage()

	materializedRoot, cleanupCorpus, err := materializeTokenSavingsCorpus(fixtures.Documents)
	if err != nil {
		return tokenSavingsSmokeReport{}, fmt.Errorf("materialize token savings corpus: %w", err)
	}
	defer cleanupCorpus()

	store, err := storage.NewSQLiteIndexStore(storagePath)
	if err != nil {
		return tokenSavingsSmokeReport{}, fmt.Errorf("create benchmark index store: %w", err)
	}

	pricing := cloneSavingsPricing(cfg.SavingsCompetitorPricing)
	tracker := telemetry.NewTracker(tokenSavingsTelemetryPricing(pricing), nowUTCFn)

	serviceCfg := cfg
	serviceCfg.StoragePath = storagePath
	if serviceCfg.Disabled == nil {
		serviceCfg.Disabled = map[string]struct{}{}
	}
	service := orchestration.New(serviceCfg, orchestration.Dependencies{
		IndexStore: store,
		Telemetry:  tracker,
	})

	indexPayload := service.CallTool(ctx, "index_folder", map[string]any{
		"path":             materializedRoot,
		"incremental":      false,
		"use_ai_summaries": false,
	})
	if !payloadBool(indexPayload, "success") {
		return tokenSavingsSmokeReport{}, fmt.Errorf("index fixture corpus: %s", payloadError(indexPayload))
	}

	repoID := payloadString(indexPayload, "repo")
	if repoID == "" {
		return tokenSavingsSmokeReport{}, errors.New("index fixture corpus: missing repo id")
	}

	documentsByPath := make(map[string]fixtureDocument, len(fixtures.Documents))
	for _, doc := range fixtures.Documents {
		documentsByPath[doc.Path] = doc
	}

	caseReports := make([]tokenSavingsCaseReport, 0, len(fixtures.Cases))
	totalWithInput := 0
	totalWithOutput := 0
	totalWithoutInput := 0

	for _, benchmarkCase := range fixtures.Cases {
		before := tracker.SessionSnapshot()

		toolArgs := cloneAnyMap(benchmarkCase.Arguments)
		toolArgs["repo"] = repoID
		response := service.CallTool(ctx, benchmarkCase.Tool, toolArgs)
		if errMsg := payloadError(response); errMsg != "" {
			return tokenSavingsSmokeReport{}, fmt.Errorf("run case %q via %s: %s", benchmarkCase.ID, benchmarkCase.Tool, errMsg)
		}

		after := tracker.SessionSnapshot()
		withRequestTokens := after.RequestTokens - before.RequestTokens
		withResponseTokens := after.ResponseTokens - before.ResponseTokens
		promptTokens := estimateSerializedTokensForReport(map[string]any{
			"prompt": benchmarkCase.Prompt,
		})
		contextPayload := buildWithoutMCPInputPayload(benchmarkCase.Prompt, benchmarkCase.ContextFiles, documentsByPath)
		contextOnlyTokens := estimateSerializedTokensForReport(map[string]any{
			"context_files": contextPayload["context_files"],
		})
		withoutInputTokens := estimateSerializedTokensForReport(contextPayload)
		withInputTokens := promptTokens + withRequestTokens

		withMode := tokenSavingsModeCaseMetrics{
			InputTokens:        withInputTokens,
			OutputTokens:       withResponseTokens,
			TotalTokens:        withInputTokens + withResponseTokens,
			ToolRequestTokens:  withRequestTokens,
			ToolResponseTokens: withResponseTokens,
			CostUSD:            competitorCosts(pricing, withInputTokens, withResponseTokens),
		}
		withoutMode := tokenSavingsModeCaseMetrics{
			InputTokens:   withoutInputTokens,
			OutputTokens:  0,
			TotalTokens:   withoutInputTokens,
			ContextTokens: contextOnlyTokens,
			CostUSD:       competitorCosts(pricing, withoutInputTokens, 0),
		}
		savings := tokenSavingsDeltaReport{
			TokensSaved:  withoutMode.TotalTokens - withMode.TotalTokens,
			SavingsPct:   savingsRatio(withoutMode.TotalTokens, withoutMode.TotalTokens-withMode.TotalTokens),
			CostSavedUSD: diffCostMap(withoutMode.CostUSD, withMode.CostUSD, pricing),
		}

		caseReports = append(caseReports, tokenSavingsCaseReport{
			ID:            benchmarkCase.ID,
			Prompt:        benchmarkCase.Prompt,
			Tool:          benchmarkCase.Tool,
			ToolArguments: cloneAnyMap(benchmarkCase.Arguments),
			ContextFiles:  append([]string(nil), benchmarkCase.ContextFiles...),
			WithMCP:       withMode,
			WithoutMCP:    withoutMode,
			Savings:       savings,
		})

		totalWithInput += withMode.InputTokens
		totalWithOutput += withMode.OutputTokens
		totalWithoutInput += withoutMode.InputTokens
	}

	aggregateWith := tokenSavingsModeAggregateMetrics{
		InputTokens:  totalWithInput,
		OutputTokens: totalWithOutput,
		TotalTokens:  totalWithInput + totalWithOutput,
		CostUSD:      competitorCosts(pricing, totalWithInput, totalWithOutput),
	}
	aggregateWithout := tokenSavingsModeAggregateMetrics{
		InputTokens:  totalWithoutInput,
		OutputTokens: 0,
		TotalTokens:  totalWithoutInput,
		CostUSD:      competitorCosts(pricing, totalWithoutInput, 0),
	}

	return tokenSavingsSmokeReport{
		GeneratedAtUTC:    nowUTCFn().Format(time.RFC3339),
		Mode:              evalModeTokenSavings,
		Dataset:           fixtures.Dataset,
		SuiteVersion:      fixtures.SuiteVersion,
		FixturesDir:       fixturesDir,
		IndexedRepo:       repoID,
		FileCount:         len(fixtures.Documents),
		CompetitorPricing: pricing,
		Cases:             caseReports,
		Aggregate: tokenSavingsAggregateReport{
			CaseCount:  len(caseReports),
			WithMCP:    aggregateWith,
			WithoutMCP: aggregateWithout,
			Savings: tokenSavingsDeltaReport{
				TokensSaved:  aggregateWithout.TotalTokens - aggregateWith.TotalTokens,
				SavingsPct:   savingsRatio(aggregateWithout.TotalTokens, aggregateWithout.TotalTokens-aggregateWith.TotalTokens),
				CostSavedUSD: diffCostMap(aggregateWithout.CostUSD, aggregateWith.CostUSD, pricing),
			},
		},
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

func buildWithoutMCPInputPayload(
	prompt string,
	contextFiles []string,
	documentsByPath map[string]fixtureDocument,
) map[string]any {
	context := make([]tokenSavingsContextFile, 0, len(contextFiles))
	for _, path := range contextFiles {
		doc := documentsByPath[path]
		context = append(context, tokenSavingsContextFile{
			Path:    path,
			Content: doc.Text,
		})
	}
	return map[string]any{
		"prompt":        prompt,
		"context_files": context,
	}
}

func estimateSerializedTokensForReport(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 {
		return 0
	}
	return int(math.Ceil(float64(len(encoded)) / serializedBytesPerEstimatedToken))
}

func tokenSavingsTelemetryPricing(
	pricing map[string]config.SavingsCompetitorPricing,
) map[string]telemetry.Pricing {
	converted := make(map[string]telemetry.Pricing, len(pricing))
	for competitor, rate := range pricing {
		converted[competitor] = telemetry.Pricing{
			InputUSDPerMTok:  rate.InputUSDPerMTok,
			OutputUSDPerMTok: rate.OutputUSDPerMTok,
		}
	}
	return converted
}

func cloneSavingsPricing(
	pricing map[string]config.SavingsCompetitorPricing,
) map[string]config.SavingsCompetitorPricing {
	cloned := make(map[string]config.SavingsCompetitorPricing, len(pricing))
	for competitor, value := range pricing {
		cloned[competitor] = value
	}
	return cloned
}

func competitorCosts(
	pricing map[string]config.SavingsCompetitorPricing,
	inputTokens int,
	outputTokens int,
) map[string]float64 {
	costs := make(map[string]float64, len(pricing))
	billableTokens := inputTokens + outputTokens
	for competitor, rate := range pricing {
		costs[competitor] = roundUSDForReport(
			float64(billableTokens) * rate.InputUSDPerMTok / 1_000_000.0,
		)
	}
	return costs
}

func diffCostMap(
	left map[string]float64,
	right map[string]float64,
	pricing map[string]config.SavingsCompetitorPricing,
) map[string]float64 {
	diff := make(map[string]float64, len(pricing))
	for competitor := range pricing {
		diff[competitor] = roundUSDForReport(left[competitor] - right[competitor])
	}
	return diff
}

func savingsRatio(baseline int, delta int) float64 {
	if baseline <= 0 {
		return 0
	}
	return math.Round((float64(delta)/float64(baseline))*1_000_000) / 1_000_000
}

func roundUSDForReport(value float64) float64 {
	return math.Round(value*1_000_000_000_000) / 1_000_000_000_000
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
