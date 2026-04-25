package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
)

const (
	defaultFanoutBenchmarkRuns             = 30
	defaultFanoutBenchmarkBatchItems       = 32
	defaultFanoutBenchmarkWorkIterations   = 12000
	defaultFanoutBenchmarkMaxWorkers       = 4
	defaultFanoutBenchmarkMaxQueueDepth    = 256
	fanoutBenchmarkWorkloadSynthetic       = "synthetic"
	fanoutBenchmarkWorkloadToolOutline     = "tool_get_file_outline_fixture"
	fanoutBenchmarkWorkloadFindImporters   = "tool_find_importers_fixture"
	fanoutBenchmarkWorkloadFindReferences  = "tool_find_references_fixture"
	fanoutBenchmarkWorkloadCheckReferences = "tool_check_references_fixture"
)

var fanoutBenchmarkSink uint64

// FanoutBenchmarkOptions configures one deterministic fanout benchmark workload.
type FanoutBenchmarkOptions struct {
	Mode             string `json:"mode"`
	Workload         string `json:"workload"`
	FixturePath      string `json:"fixture_path,omitempty"`
	FixtureDigest    string `json:"fixture_digest,omitempty"`
	FixtureFileCount int    `json:"fixture_file_count,omitempty"`
	Runs             int    `json:"runs"`
	BatchItems       int    `json:"batch_items"`
	WorkIterations   int    `json:"work_iterations"`
	MaxWorkers       int    `json:"max_workers"`
	MaxQueueDepth    int    `json:"max_queue_depth"`
}

// FanoutBenchmarkMetrics captures one benchmark execution result set.
type FanoutBenchmarkMetrics struct {
	Mode                  string  `json:"mode"`
	Workload              string  `json:"workload"`
	FixtureDigest         string  `json:"fixture_digest,omitempty"`
	FixtureFileCount      int     `json:"fixture_file_count,omitempty"`
	Runs                  int     `json:"runs"`
	BatchItems            int     `json:"batch_items"`
	WorkIterations        int     `json:"work_iterations"`
	MaxWorkers            int     `json:"max_workers"`
	MaxQueueDepth         int     `json:"max_queue_depth"`
	SuccessfulRuns        int     `json:"successful_runs"`
	FailedRuns            int     `json:"failed_runs"`
	ErrorRate             float64 `json:"error_rate"`
	ThroughputItemsPerSec float64 `json:"throughput_items_per_sec"`
	LatencyMeanMS         float64 `json:"latency_mean_ms"`
	LatencyP50MS          float64 `json:"latency_p50_ms"`
	LatencyP95MS          float64 `json:"latency_p95_ms"`
	FirstError            string  `json:"first_error,omitempty"`
}

// NormalizeFanoutBenchmarkOptions validates options and applies deterministic defaults.
func NormalizeFanoutBenchmarkOptions(opts FanoutBenchmarkOptions) (FanoutBenchmarkOptions, error) {
	normalized := opts

	mode := strings.ToLower(strings.TrimSpace(normalized.Mode))
	if mode == "" {
		mode = fanoutModeSerial
	}
	if mode != fanoutModeSerial && mode != fanoutModeParallel {
		return FanoutBenchmarkOptions{}, fmt.Errorf("mode must be %q or %q", fanoutModeSerial, fanoutModeParallel)
	}
	normalized.Mode = mode

	workload := strings.ToLower(strings.TrimSpace(normalized.Workload))
	if workload == "" {
		workload = fanoutBenchmarkWorkloadSynthetic
	}
	switch workload {
	case fanoutBenchmarkWorkloadSynthetic,
		fanoutBenchmarkWorkloadToolOutline,
		fanoutBenchmarkWorkloadFindImporters,
		fanoutBenchmarkWorkloadFindReferences,
		fanoutBenchmarkWorkloadCheckReferences:
	default:
		return FanoutBenchmarkOptions{}, fmt.Errorf(
			"workload must be %q, %q, %q, %q, or %q",
			fanoutBenchmarkWorkloadSynthetic,
			fanoutBenchmarkWorkloadToolOutline,
			fanoutBenchmarkWorkloadFindImporters,
			fanoutBenchmarkWorkloadFindReferences,
			fanoutBenchmarkWorkloadCheckReferences,
		)
	}
	normalized.Workload = workload

	normalized.FixturePath = strings.TrimSpace(normalized.FixturePath)
	if isFixtureBenchmarkWorkload(normalized.Workload) {
		if normalized.FixturePath == "" {
			return FanoutBenchmarkOptions{}, fmt.Errorf("fixture_path is required when workload=%q", normalized.Workload)
		}
		fixtureDigest, fixtureFileCount, err := computeFixtureDigest(normalized.FixturePath)
		if err != nil {
			return FanoutBenchmarkOptions{}, fmt.Errorf("fingerprint fixture_path: %w", err)
		}
		normalized.FixtureDigest = fixtureDigest
		normalized.FixtureFileCount = fixtureFileCount
	} else {
		normalized.FixtureDigest = ""
		normalized.FixtureFileCount = 0
	}

	if normalized.Runs == 0 {
		normalized.Runs = defaultFanoutBenchmarkRuns
	}
	if normalized.Runs < 0 {
		return FanoutBenchmarkOptions{}, fmt.Errorf("runs must be > 0")
	}
	if normalized.Runs <= 0 {
		return FanoutBenchmarkOptions{}, fmt.Errorf("runs must be > 0")
	}

	if normalized.BatchItems == 0 {
		normalized.BatchItems = defaultFanoutBenchmarkBatchItems
	}
	if normalized.BatchItems <= 0 {
		return FanoutBenchmarkOptions{}, fmt.Errorf("batch_items must be > 0")
	}

	if normalized.WorkIterations == 0 {
		normalized.WorkIterations = defaultFanoutBenchmarkWorkIterations
	}
	if normalized.WorkIterations <= 0 {
		return FanoutBenchmarkOptions{}, fmt.Errorf("work_iterations must be > 0")
	}

	if normalized.MaxWorkers == 0 {
		normalized.MaxWorkers = defaultFanoutBenchmarkMaxWorkers
	}
	if normalized.MaxWorkers <= 0 {
		return FanoutBenchmarkOptions{}, fmt.Errorf("max_workers must be > 0")
	}

	if normalized.MaxQueueDepth == 0 {
		normalized.MaxQueueDepth = defaultFanoutBenchmarkMaxQueueDepth
	}
	if normalized.MaxQueueDepth <= 0 {
		return FanoutBenchmarkOptions{}, fmt.Errorf("max_queue_depth must be > 0")
	}

	return normalized, nil
}

// RunFanoutBenchmark executes one deterministic fanout benchmark workload.
func RunFanoutBenchmark(ctx context.Context, opts FanoutBenchmarkOptions) (FanoutBenchmarkMetrics, error) {
	normalized, err := NormalizeFanoutBenchmarkOptions(opts)
	if err != nil {
		return FanoutBenchmarkMetrics{}, err
	}

	runOne, cleanup, err := newBenchmarkRunner(ctx, normalized)
	if err != nil {
		return FanoutBenchmarkMetrics{}, err
	}
	defer cleanup()

	sampleLatenciesMS := make([]float64, 0, normalized.Runs)
	successful := 0
	failed := 0
	firstErr := ""
	suiteStarted := time.Now()

	for runID := 0; runID < normalized.Runs; runID++ {
		if err := ctx.Err(); err != nil {
			return FanoutBenchmarkMetrics{}, err
		}

		started := time.Now()
		err := runOne(ctx, runID)
		elapsedMS := float64(time.Since(started)) / float64(time.Millisecond)

		if err != nil {
			failed++
			if firstErr == "" {
				firstErr = err.Error()
			}
			continue
		}

		successful++
		sampleLatenciesMS = append(sampleLatenciesMS, elapsedMS)
	}

	suiteElapsed := time.Since(suiteStarted)
	if suiteElapsed <= 0 {
		suiteElapsed = time.Nanosecond
	}

	metrics := FanoutBenchmarkMetrics{
		Mode:             normalized.Mode,
		Workload:         normalized.Workload,
		FixtureDigest:    normalized.FixtureDigest,
		FixtureFileCount: normalized.FixtureFileCount,
		Runs:             normalized.Runs,
		BatchItems:       normalized.BatchItems,
		WorkIterations:   normalized.WorkIterations,
		MaxWorkers:       normalized.MaxWorkers,
		MaxQueueDepth:    normalized.MaxQueueDepth,
		SuccessfulRuns:   successful,
		FailedRuns:       failed,
		ErrorRate:        float64(failed) / float64(normalized.Runs),
		FirstError:       firstErr,
	}

	if successful > 0 {
		itemsProcessed := successful * normalized.BatchItems
		metrics.ThroughputItemsPerSec = float64(itemsProcessed) / suiteElapsed.Seconds()
		metrics.LatencyMeanMS = meanFloat64(sampleLatenciesMS)
		metrics.LatencyP50MS = percentile(sampleLatenciesMS, 50)
		metrics.LatencyP95MS = percentile(sampleLatenciesMS, 95)
	}

	return metrics, nil
}

func newBenchmarkRunner(
	ctx context.Context,
	opts FanoutBenchmarkOptions,
) (func(context.Context, int) error, func(), error) {
	baseConfig := config.Config{
		ServerName:          "gocodemunch-mcp",
		ServerVersion:       "bench",
		FreshnessMode:       "relaxed",
		FanoutMode:          opts.Mode,
		FanoutMaxWorkers:    opts.MaxWorkers,
		FanoutMaxQueueDepth: opts.MaxQueueDepth,
		Disabled:            map[string]struct{}{},
	}

	if opts.Workload == fanoutBenchmarkWorkloadSynthetic {
		service := New(baseConfig, Dependencies{})
		runOne := func(runCtx context.Context, runID int) error {
			return service.runBatchFanout(runCtx, opts.BatchItems, func(itemCtx context.Context, itemIndex int) error {
				return runDeterministicFanoutWork(itemCtx, opts.WorkIterations, runID, itemIndex)
			})
		}
		return runOne, func() {}, nil
	}

	if isFixtureBenchmarkWorkload(opts.Workload) {
		storeDir, err := os.MkdirTemp("", "gocodemunch-bench-store-*")
		if err != nil {
			return nil, nil, fmt.Errorf("create benchmark store dir: %w", err)
		}
		cleanup := func() {
			_ = os.RemoveAll(storeDir)
		}

		store, err := storage.NewSQLiteIndexStore(storeDir)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("create benchmark fixture store: %w", err)
		}

		service := New(baseConfig, Dependencies{IndexStore: store})
		indexPayload := service.CallTool(ctx, "index_folder", map[string]any{
			"path":        opts.FixturePath,
			"incremental": false,
		})
		if !boolArg(indexPayload, "success", false) {
			cleanup()
			errMsg := strings.TrimSpace(stringArg(indexPayload, "error", ""))
			if errMsg == "" {
				errMsg = "fixture indexing failed"
			}
			return nil, nil, fmt.Errorf("index fixture for benchmark workload: %s", errMsg)
		}

		repoID := strings.TrimSpace(stringArg(indexPayload, "repo", ""))
		if repoID == "" {
			cleanup()
			return nil, nil, fmt.Errorf("index fixture for benchmark workload: missing repo id")
		}

		index, err := store.Load(ctx, repoID)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("load benchmark fixture index: %w", err)
		}
		files := sortedIndexedFiles(index.Files)
		if len(files) == 0 {
			cleanup()
			return nil, nil, fmt.Errorf("benchmark fixture index has no source files")
		}

		runOne := func(runCtx context.Context, runID int) error {
			selection := buildFixtureSelection(files, runID, opts.BatchItems)
			if usesIdentifierFixtureSelection(opts.Workload) {
				selection = buildFixtureIdentifierSelection(index, files, runID, opts.BatchItems)
			}
			payload, err := runFixtureToolWorkload(runCtx, service, repoID, opts.Workload, selection)
			if err != nil {
				return err
			}
			if errMsg := strings.TrimSpace(stringArg(payload, "error", "")); errMsg != "" {
				return fmt.Errorf("tool fixture workload failed: %s", errMsg)
			}
			gotCount, ok := sliceLength(payload["results"])
			if !ok {
				return fmt.Errorf("tool fixture workload failed: missing results slice")
			}
			if gotCount != opts.BatchItems {
				return fmt.Errorf("tool fixture workload failed: expected %d results, got %d", opts.BatchItems, gotCount)
			}
			return nil
		}

		return runOne, cleanup, nil
	}

	return nil, nil, fmt.Errorf("unsupported benchmark workload: %s", opts.Workload)
}

func isFixtureBenchmarkWorkload(workload string) bool {
	switch workload {
	case fanoutBenchmarkWorkloadToolOutline,
		fanoutBenchmarkWorkloadFindImporters,
		fanoutBenchmarkWorkloadFindReferences,
		fanoutBenchmarkWorkloadCheckReferences:
		return true
	default:
		return false
	}
}

// IsFixtureBenchmarkWorkload indicates whether a benchmark workload requires a fixture corpus path.
func IsFixtureBenchmarkWorkload(workload string) bool {
	return isFixtureBenchmarkWorkload(strings.ToLower(strings.TrimSpace(workload)))
}

func computeFixtureDigest(fixturePath string) (string, int, error) {
	rootPath := filepath.Clean(strings.TrimSpace(fixturePath))
	if rootPath == "" {
		return "", 0, fmt.Errorf("fixture path is empty")
	}
	info, err := os.Stat(rootPath)
	if err != nil {
		return "", 0, err
	}
	if !info.IsDir() {
		return "", 0, fmt.Errorf("fixture path must be a directory")
	}

	relativePaths := make([]string, 0, 64)
	if err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		relativePath, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}
		relativePaths = append(relativePaths, filepath.ToSlash(relativePath))
		return nil
	}); err != nil {
		return "", 0, err
	}

	sort.Strings(relativePaths)

	treeDigest := sha256.New()
	for _, relativePath := range relativePaths {
		fileDigest, err := digestFixtureFile(filepath.Join(rootPath, filepath.FromSlash(relativePath)))
		if err != nil {
			return "", 0, err
		}
		_, _ = io.WriteString(treeDigest, relativePath)
		_, _ = io.WriteString(treeDigest, "\n")
		_, _ = treeDigest.Write(fileDigest[:])
	}

	return "sha256:" + hex.EncodeToString(treeDigest.Sum(nil)), len(relativePaths), nil
}

func digestFixtureFile(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()

	fileDigest := sha256.New()
	if _, err := io.Copy(fileDigest, file); err != nil {
		return [sha256.Size]byte{}, err
	}

	var out [sha256.Size]byte
	copy(out[:], fileDigest.Sum(nil))
	return out, nil
}

func usesIdentifierFixtureSelection(workload string) bool {
	switch workload {
	case fanoutBenchmarkWorkloadFindReferences, fanoutBenchmarkWorkloadCheckReferences:
		return true
	default:
		return false
	}
}

func runFixtureToolWorkload(
	ctx context.Context,
	service *Service,
	repoID string,
	workload string,
	selection []string,
) (map[string]any, error) {
	switch workload {
	case fanoutBenchmarkWorkloadToolOutline:
		return service.CallTool(ctx, "get_file_outline", map[string]any{
			"repo":       repoID,
			"file_paths": selection,
		}), nil
	case fanoutBenchmarkWorkloadFindImporters:
		return service.CallTool(ctx, "find_importers", map[string]any{
			"repo":       repoID,
			"file_paths": selection,
		}), nil
	case fanoutBenchmarkWorkloadFindReferences:
		return service.CallTool(ctx, "find_references", map[string]any{
			"repo":        repoID,
			"identifiers": selection,
		}), nil
	case fanoutBenchmarkWorkloadCheckReferences:
		return service.CallTool(ctx, "check_references", map[string]any{
			"repo":           repoID,
			"identifiers":    selection,
			"search_content": true,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported fixture benchmark workload: %s", workload)
	}
}

func buildFixtureSelection(files []string, runID, batchItems int) []string {
	if batchItems <= 0 || len(files) == 0 {
		return []string{}
	}

	out := make([]string, batchItems)
	start := runID * batchItems
	for i := 0; i < batchItems; i++ {
		out[i] = files[(start+i)%len(files)]
	}
	return out
}

func buildFixtureIdentifierSelection(
	index storage.RepoIndex,
	files []string,
	runID int,
	batchItems int,
) []string {
	if batchItems <= 0 {
		return []string{}
	}

	candidates := collectFixtureIdentifierCandidates(index, files)
	if len(candidates) == 0 {
		return []string{}
	}

	out := make([]string, batchItems)
	start := runID * batchItems
	for i := 0; i < batchItems; i++ {
		out[i] = candidates[(start+i)%len(candidates)]
	}
	return out
}

func collectFixtureIdentifierCandidates(index storage.RepoIndex, files []string) []string {
	seen := map[string]struct{}{}
	candidates := make([]string, 0, 16)
	add := func(raw string) {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, trimmed)
	}

	for _, record := range collectIndexedImportRecords(index) {
		for _, name := range record.Names {
			add(name)
		}
		add(importSpecifierStem(record.Specifier))
	}

	for _, filePath := range files {
		add(importSpecifierStem(filePath))
	}

	slices.Sort(candidates)
	return candidates
}

func sliceLength(value any) (int, bool) {
	raw := reflect.ValueOf(value)
	if !raw.IsValid() || raw.Kind() != reflect.Slice {
		return 0, false
	}
	return raw.Len(), true
}

func runDeterministicFanoutWork(ctx context.Context, iterations, runID, itemIndex int) error {
	seed := uint64((runID + 1) * 7919)
	value := seed ^ uint64((itemIndex+1)*104729)

	for i := 0; i < iterations; i++ {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		mix := uint64(i+1) * 0x9e3779b97f4a7c15
		value ^= mix + uint64(itemIndex+1)
		value = bits.RotateLeft64(value, 11) ^ (value >> 7)
		value *= 0xbf58476d1ce4e5b9
	}

	atomic.AddUint64(&fanoutBenchmarkSink, value)

	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func meanFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if len(values) == 1 {
		return values[0]
	}

	clampedP := math.Max(0, math.Min(100, p))
	sorted := slices.Clone(values)
	slices.Sort(sorted)

	position := (clampedP / 100.0) * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}

	weight := position - float64(lower)
	return sorted[lower] + (sorted[upper]-sorted[lower])*weight
}
