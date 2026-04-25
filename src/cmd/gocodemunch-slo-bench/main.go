package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/orchestration"
)

type sloThresholds struct {
	MinThroughputItemsPerSec float64 `json:"min_throughput_items_per_sec"`
	MaxLatencyP95MS          float64 `json:"max_latency_p95_ms"`
	MaxErrorRate             float64 `json:"max_error_rate"`
	MinFixtureFileCount      int     `json:"min_fixture_file_count,omitempty"`
	MinThroughputRatioVsBase float64 `json:"min_throughput_ratio_vs_baseline,omitempty"`
	MaxP95RatioVsBase        float64 `json:"max_p95_ratio_vs_baseline,omitempty"`
	MaxErrorRateDeltaVsBase  float64 `json:"max_error_rate_delta_vs_baseline,omitempty"`
}

type runtimeMetrics struct {
	WallClockSeconds         float64 `json:"wall_clock_seconds"`
	CPUUserSeconds           float64 `json:"cpu_user_seconds"`
	CPUSystemSeconds         float64 `json:"cpu_system_seconds"`
	HeapAllocBytes           uint64  `json:"heap_alloc_bytes"`
	HeapTotalAllocDeltaBytes uint64  `json:"heap_total_alloc_delta_bytes"`
	HeapObjectsDelta         uint64  `json:"heap_objects_delta"`
}

type baselineComparison struct {
	BaselineReportPath            string  `json:"baseline_report_path,omitempty"`
	BaselineMode                  string  `json:"baseline_mode,omitempty"`
	BaselineThroughputItemsPerSec float64 `json:"baseline_throughput_items_per_sec"`
	BaselineLatencyP95MS          float64 `json:"baseline_latency_p95_ms"`
	BaselineErrorRate             float64 `json:"baseline_error_rate"`
	ThroughputRatioVsBaseline     float64 `json:"throughput_ratio_vs_baseline"`
	LatencyP95RatioVsBaseline     float64 `json:"latency_p95_ratio_vs_baseline"`
	ErrorRateDeltaVsBaseline      float64 `json:"error_rate_delta_vs_baseline"`
}

type report struct {
	GeneratedAtUTC string                               `json:"generated_at_utc"`
	Benchmark      orchestration.FanoutBenchmarkMetrics `json:"benchmark"`
	Runtime        runtimeMetrics                       `json:"runtime"`
	Thresholds     sloThresholds                        `json:"thresholds"`
	Comparison     *baselineComparison                  `json:"comparison,omitempty"`
	Verdict        string                               `json:"verdict"`
}

const (
	benchmarkModeSerial   = "serial"
	benchmarkModeParallel = "parallel"
)

func main() {
	os.Exit(runWithArgs(os.Args[1:], os.Stdout, os.Stderr))
}

func runWithArgs(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gocodemunch-slo-bench", flag.ContinueOnError)
	flags.SetOutput(stderr)

	mode := flags.String("mode", "serial", "Fanout mode benchmarked (serial|parallel)")
	workload := flags.String(
		"workload",
		"synthetic",
		"Benchmark workload (synthetic|tool_get_file_outline_fixture|tool_find_importers_fixture|tool_find_references_fixture|tool_check_references_fixture)",
	)
	fixturePath := flags.String("fixture-path", "", "Fixture folder path used by fixture-backed tool workloads")
	runs := flags.Int("runs", 30, "Number of benchmark runs")
	batchItems := flags.Int("batch-items", 32, "Fanout items executed per run")
	workIterations := flags.Int("work-iterations", 12000, "Deterministic CPU work iterations per item")
	maxWorkers := flags.Int("max-workers", 4, "Maximum parallel fanout workers")
	queueDepth := flags.Int("queue-depth", 256, "Maximum fanout queue depth")
	minFixtureFileCount := flags.Int(
		"min-fixture-file-count",
		0,
		"Minimum fixture file count required for fixture-backed workloads (0 disables)",
	)
	minThroughput := flags.Float64("min-throughput-items-per-sec", 0, "Minimum throughput gate (0 disables)")
	maxP95MS := flags.Float64("max-p95-ms", 0, "Maximum p95 latency gate in milliseconds (0 disables)")
	maxErrorRate := flags.Float64("max-error-rate", 0, "Maximum error rate gate")
	minThroughputRatioVsBase := flags.Float64(
		"min-throughput-ratio-vs-baseline",
		0,
		"Minimum throughput ratio compared to baseline report (0 disables)",
	)
	maxP95RatioVsBase := flags.Float64(
		"max-p95-ratio-vs-baseline",
		0,
		"Maximum p95 latency ratio compared to baseline report (0 disables)",
	)
	maxErrorRateDeltaVsBase := flags.Float64(
		"max-error-rate-delta-vs-baseline",
		0,
		"Maximum error-rate delta compared to baseline report (0 disables)",
	)
	baselineReportPath := flags.String("baseline-report", "", "Optional JSON report path used for baseline-vs-shadow comparisons")
	outPath := flags.String("out", "", "Optional output file path for JSON report")

	if err := flags.Parse(args); err != nil {
		return 2
	}

	thresholds := sloThresholds{
		MinThroughputItemsPerSec: *minThroughput,
		MaxLatencyP95MS:          *maxP95MS,
		MaxErrorRate:             *maxErrorRate,
		MinFixtureFileCount:      *minFixtureFileCount,
		MinThroughputRatioVsBase: *minThroughputRatioVsBase,
		MaxP95RatioVsBase:        *maxP95RatioVsBase,
		MaxErrorRateDeltaVsBase:  *maxErrorRateDeltaVsBase,
	}
	if err := validateThresholds(thresholds); err != nil {
		fmt.Fprintf(stderr, "invalid thresholds: %v\n", err)
		return 2
	}

	baselinePath := strings.TrimSpace(*baselineReportPath)
	if requiresBaselineReport(thresholds) && baselinePath == "" {
		fmt.Fprintln(stderr, "invalid thresholds: baseline-report is required when baseline comparison gates are enabled")
		return 2
	}

	var baseline *report
	if baselinePath != "" {
		loadedBaseline, err := loadReport(baselinePath)
		if err != nil {
			fmt.Fprintf(stderr, "load baseline report: %v\n", err)
			return 2
		}
		baseline = &loadedBaseline
	}

	opts := orchestration.FanoutBenchmarkOptions{
		Mode:           *mode,
		Workload:       *workload,
		FixturePath:    *fixturePath,
		Runs:           *runs,
		BatchItems:     *batchItems,
		WorkIterations: *workIterations,
		MaxWorkers:     *maxWorkers,
		MaxQueueDepth:  *queueDepth,
	}
	normalizedOpts, err := orchestration.NormalizeFanoutBenchmarkOptions(opts)
	if err != nil {
		fmt.Fprintf(stderr, "invalid benchmark options: %v\n", err)
		return 2
	}
	if thresholds.MinFixtureFileCount > 0 {
		if !orchestration.IsFixtureBenchmarkWorkload(normalizedOpts.Workload) {
			fmt.Fprintln(
				stderr,
				"invalid thresholds: min-fixture-file-count requires a fixture-backed workload",
			)
			return 2
		}
		if normalizedOpts.FixtureFileCount < thresholds.MinFixtureFileCount {
			fmt.Fprintf(
				stderr,
				"invalid benchmark options: fixture file count (%d) is below min-fixture-file-count (%d)\n",
				normalizedOpts.FixtureFileCount,
				thresholds.MinFixtureFileCount,
			)
			return 2
		}
	}
	if baseline != nil {
		if err := validateBaselineCompatibility(normalizedOpts, *baseline); err != nil {
			fmt.Fprintf(stderr, "invalid baseline report: %v\n", err)
			return 2
		}
	}

	cpuBefore, err := readCPUUsageSnapshot()
	if err != nil {
		fmt.Fprintf(stderr, "read cpu usage snapshot: %v\n", err)
		return 1
	}
	memBefore := runtime.MemStats{}
	runtime.ReadMemStats(&memBefore)

	started := time.Now()
	metrics, err := orchestration.RunFanoutBenchmark(context.Background(), normalizedOpts)
	if err != nil {
		fmt.Fprintf(stderr, "run benchmark: %v\n", err)
		return 2
	}
	wallClockSeconds := time.Since(started).Seconds()

	cpuAfter, err := readCPUUsageSnapshot()
	if err != nil {
		fmt.Fprintf(stderr, "read cpu usage snapshot: %v\n", err)
		return 1
	}
	memAfter := runtime.MemStats{}
	runtime.ReadMemStats(&memAfter)

	benchReport := report{
		GeneratedAtUTC: time.Now().UTC().Format(time.RFC3339),
		Benchmark:      metrics,
		Runtime: runtimeMetrics{
			WallClockSeconds:         wallClockSeconds,
			CPUUserSeconds:           nonNegativeFloat(cpuAfter.UserSeconds - cpuBefore.UserSeconds),
			CPUSystemSeconds:         nonNegativeFloat(cpuAfter.SystemSeconds - cpuBefore.SystemSeconds),
			HeapAllocBytes:           memAfter.HeapAlloc,
			HeapTotalAllocDeltaBytes: nonNegativeUint64Delta(memAfter.TotalAlloc, memBefore.TotalAlloc),
			HeapObjectsDelta:         nonNegativeUint64Delta(memAfter.HeapObjects, memBefore.HeapObjects),
		},
		Thresholds: thresholds,
	}

	if baseline != nil {
		benchReport.Comparison = buildBaselineComparison(metrics, *baseline, baselinePath)
	}

	if evaluateGate(metrics, thresholds, benchReport.Comparison) {
		benchReport.Verdict = "pass"
	} else {
		benchReport.Verdict = "fail"
	}

	payload, err := json.MarshalIndent(benchReport, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "marshal report: %v\n", err)
		return 1
	}

	if *outPath != "" {
		if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
			fmt.Fprintf(stderr, "create output directory: %v\n", err)
			return 1
		}
		if err := os.WriteFile(*outPath, append(payload, '\n'), 0o644); err != nil {
			fmt.Fprintf(stderr, "write output report: %v\n", err)
			return 1
		}
	}

	_, _ = fmt.Fprintln(stdout, string(payload))
	if benchReport.Verdict == "pass" {
		return 0
	}
	return 1
}

func evaluateGate(
	metrics orchestration.FanoutBenchmarkMetrics,
	thresholds sloThresholds,
	comparison *baselineComparison,
) bool {
	if thresholds.MaxErrorRate >= 0 && metrics.ErrorRate > thresholds.MaxErrorRate {
		return false
	}
	if thresholds.MaxLatencyP95MS > 0 && metrics.LatencyP95MS > thresholds.MaxLatencyP95MS {
		return false
	}
	if thresholds.MinThroughputItemsPerSec > 0 && metrics.ThroughputItemsPerSec < thresholds.MinThroughputItemsPerSec {
		return false
	}
	if thresholds.MinThroughputRatioVsBase > 0 {
		if comparison == nil || comparison.BaselineThroughputItemsPerSec <= 0 {
			return false
		}
		if comparison.ThroughputRatioVsBaseline < thresholds.MinThroughputRatioVsBase {
			return false
		}
	}
	if thresholds.MaxP95RatioVsBase > 0 {
		if comparison == nil || comparison.BaselineLatencyP95MS <= 0 {
			return false
		}
		if comparison.LatencyP95RatioVsBaseline > thresholds.MaxP95RatioVsBase {
			return false
		}
	}
	if thresholds.MaxErrorRateDeltaVsBase > 0 {
		if comparison == nil {
			return false
		}
		if comparison.ErrorRateDeltaVsBaseline > thresholds.MaxErrorRateDeltaVsBase {
			return false
		}
	}
	return true
}

func validateThresholds(thresholds sloThresholds) error {
	if thresholds.MinThroughputItemsPerSec < 0 {
		return fmt.Errorf("min-throughput-items-per-sec must be >= 0")
	}
	if thresholds.MaxLatencyP95MS < 0 {
		return fmt.Errorf("max-p95-ms must be >= 0")
	}
	if thresholds.MaxErrorRate < 0 {
		return fmt.Errorf("max-error-rate must be >= 0")
	}
	if thresholds.MaxErrorRate > 1 {
		return fmt.Errorf("max-error-rate must be <= 1")
	}
	if thresholds.MinFixtureFileCount < 0 {
		return fmt.Errorf("min-fixture-file-count must be >= 0")
	}
	if thresholds.MinThroughputRatioVsBase < 0 {
		return fmt.Errorf("min-throughput-ratio-vs-baseline must be >= 0")
	}
	if thresholds.MaxP95RatioVsBase < 0 {
		return fmt.Errorf("max-p95-ratio-vs-baseline must be >= 0")
	}
	if thresholds.MaxErrorRateDeltaVsBase < 0 {
		return fmt.Errorf("max-error-rate-delta-vs-baseline must be >= 0")
	}
	if thresholds.MaxErrorRateDeltaVsBase > 1 {
		return fmt.Errorf("max-error-rate-delta-vs-baseline must be <= 1")
	}
	return nil
}

func requiresBaselineReport(thresholds sloThresholds) bool {
	return thresholds.MinThroughputRatioVsBase > 0 ||
		thresholds.MaxP95RatioVsBase > 0 ||
		thresholds.MaxErrorRateDeltaVsBase > 0
}

func validateBaselineBenchmarkShape(metrics orchestration.FanoutBenchmarkMetrics) error {
	mode := strings.ToLower(strings.TrimSpace(metrics.Mode))
	if mode != benchmarkModeSerial && mode != benchmarkModeParallel {
		return fmt.Errorf("baseline mode must be %q or %q", benchmarkModeSerial, benchmarkModeParallel)
	}
	workload := strings.TrimSpace(metrics.Workload)
	if workload == "" {
		return fmt.Errorf("baseline workload must be set")
	}
	if metrics.Runs <= 0 {
		return fmt.Errorf("baseline runs must be > 0")
	}
	if metrics.BatchItems <= 0 {
		return fmt.Errorf("baseline batch-items must be > 0")
	}
	if metrics.WorkIterations <= 0 {
		return fmt.Errorf("baseline work-iterations must be > 0")
	}
	if metrics.MaxWorkers <= 0 {
		return fmt.Errorf("baseline max-workers must be > 0")
	}
	if metrics.MaxQueueDepth <= 0 {
		return fmt.Errorf("baseline queue-depth must be > 0")
	}
	if orchestration.IsFixtureBenchmarkWorkload(workload) {
		if strings.TrimSpace(metrics.FixtureDigest) == "" {
			return fmt.Errorf("baseline fixture-digest must be set")
		}
		if metrics.FixtureFileCount <= 0 {
			return fmt.Errorf("baseline fixture-file-count must be > 0")
		}
	}
	return nil
}

func validateBaselineCompatibility(current orchestration.FanoutBenchmarkOptions, baseline report) error {
	baselineVerdict := strings.ToLower(strings.TrimSpace(baseline.Verdict))
	if baselineVerdict != "" && baselineVerdict != "pass" {
		return fmt.Errorf("baseline verdict must be pass, got %q", baselineVerdict)
	}
	if err := validateBaselineBenchmarkShape(baseline.Benchmark); err != nil {
		return err
	}

	baselineWorkload := strings.TrimSpace(baseline.Benchmark.Workload)
	if baselineWorkload != current.Workload {
		return fmt.Errorf(
			"baseline workload mismatch: baseline=%q current=%q",
			baselineWorkload,
			current.Workload,
		)
	}

	if baseline.Benchmark.BatchItems != current.BatchItems {
		return fmt.Errorf(
			"baseline batch-items mismatch: baseline=%d current=%d",
			baseline.Benchmark.BatchItems,
			current.BatchItems,
		)
	}

	if baseline.Benchmark.Runs != current.Runs {
		return fmt.Errorf(
			"baseline runs mismatch: baseline=%d current=%d",
			baseline.Benchmark.Runs,
			current.Runs,
		)
	}

	if baseline.Benchmark.WorkIterations != current.WorkIterations {
		return fmt.Errorf(
			"baseline work-iterations mismatch: baseline=%d current=%d",
			baseline.Benchmark.WorkIterations,
			current.WorkIterations,
		)
	}

	if baseline.Benchmark.MaxQueueDepth != current.MaxQueueDepth {
		return fmt.Errorf(
			"baseline queue-depth mismatch: baseline=%d current=%d",
			baseline.Benchmark.MaxQueueDepth,
			current.MaxQueueDepth,
		)
	}

	baselineMode := strings.ToLower(strings.TrimSpace(baseline.Benchmark.Mode))
	if baselineMode == current.Mode && baseline.Benchmark.MaxWorkers != current.MaxWorkers {
		return fmt.Errorf(
			"baseline max-workers mismatch: baseline=%d current=%d",
			baseline.Benchmark.MaxWorkers,
			current.MaxWorkers,
		)
	}
	if orchestration.IsFixtureBenchmarkWorkload(current.Workload) {
		baselineFixtureDigest := strings.TrimSpace(baseline.Benchmark.FixtureDigest)
		if baselineFixtureDigest != current.FixtureDigest {
			return fmt.Errorf(
				"baseline fixture-digest mismatch: baseline=%q current=%q",
				baselineFixtureDigest,
				current.FixtureDigest,
			)
		}
		if baseline.Benchmark.FixtureFileCount != current.FixtureFileCount {
			return fmt.Errorf(
				"baseline fixture-file-count mismatch: baseline=%d current=%d",
				baseline.Benchmark.FixtureFileCount,
				current.FixtureFileCount,
			)
		}
	}

	return nil
}

func loadReport(path string) (report, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return report{}, err
	}
	decoded := report{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return report{}, err
	}
	return decoded, nil
}

func buildBaselineComparison(
	metrics orchestration.FanoutBenchmarkMetrics,
	baseline report,
	baselinePath string,
) *baselineComparison {
	throughputRatio := 0.0
	if baseline.Benchmark.ThroughputItemsPerSec > 0 {
		throughputRatio = metrics.ThroughputItemsPerSec / baseline.Benchmark.ThroughputItemsPerSec
	}

	latencyRatio := 0.0
	if baseline.Benchmark.LatencyP95MS > 0 {
		latencyRatio = metrics.LatencyP95MS / baseline.Benchmark.LatencyP95MS
	}

	return &baselineComparison{
		BaselineReportPath:            baselinePath,
		BaselineMode:                  baseline.Benchmark.Mode,
		BaselineThroughputItemsPerSec: baseline.Benchmark.ThroughputItemsPerSec,
		BaselineLatencyP95MS:          baseline.Benchmark.LatencyP95MS,
		BaselineErrorRate:             baseline.Benchmark.ErrorRate,
		ThroughputRatioVsBaseline:     throughputRatio,
		LatencyP95RatioVsBaseline:     latencyRatio,
		ErrorRateDeltaVsBaseline:      metrics.ErrorRate - baseline.Benchmark.ErrorRate,
	}
}

func nonNegativeFloat(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func nonNegativeUint64Delta(after, before uint64) uint64 {
	if after <= before {
		return 0
	}
	return after - before
}
