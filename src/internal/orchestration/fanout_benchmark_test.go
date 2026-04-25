package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeFanoutBenchmarkOptionsDefaults(t *testing.T) {
	opts, err := NormalizeFanoutBenchmarkOptions(FanoutBenchmarkOptions{})
	if err != nil {
		t.Fatalf("normalize options: %v", err)
	}

	if opts.Mode != fanoutModeSerial {
		t.Fatalf("expected default mode %q, got %q", fanoutModeSerial, opts.Mode)
	}
	if opts.Workload != fanoutBenchmarkWorkloadSynthetic {
		t.Fatalf("expected default workload %q, got %q", fanoutBenchmarkWorkloadSynthetic, opts.Workload)
	}
	if opts.Runs != defaultFanoutBenchmarkRuns {
		t.Fatalf("expected default runs %d, got %d", defaultFanoutBenchmarkRuns, opts.Runs)
	}
	if opts.BatchItems != defaultFanoutBenchmarkBatchItems {
		t.Fatalf("expected default batch_items %d, got %d", defaultFanoutBenchmarkBatchItems, opts.BatchItems)
	}
	if opts.WorkIterations != defaultFanoutBenchmarkWorkIterations {
		t.Fatalf("expected default work_iterations %d, got %d", defaultFanoutBenchmarkWorkIterations, opts.WorkIterations)
	}
	if opts.MaxWorkers != defaultFanoutBenchmarkMaxWorkers {
		t.Fatalf("expected default max_workers %d, got %d", defaultFanoutBenchmarkMaxWorkers, opts.MaxWorkers)
	}
	if opts.MaxQueueDepth != defaultFanoutBenchmarkMaxQueueDepth {
		t.Fatalf("expected default max_queue_depth %d, got %d", defaultFanoutBenchmarkMaxQueueDepth, opts.MaxQueueDepth)
	}
}

func TestNormalizeFanoutBenchmarkOptionsRejectsInvalidMode(t *testing.T) {
	_, err := NormalizeFanoutBenchmarkOptions(FanoutBenchmarkOptions{
		Mode: "invalid",
		Runs: 1,
	})
	if err == nil {
		t.Fatal("expected invalid mode error")
	}
}

func TestNormalizeFanoutBenchmarkOptionsRejectsFixtureWorkloadWithoutPath(t *testing.T) {
	t.Parallel()

	for _, workload := range []string{
		fanoutBenchmarkWorkloadToolOutline,
		fanoutBenchmarkWorkloadFindImporters,
		fanoutBenchmarkWorkloadFindReferences,
		fanoutBenchmarkWorkloadCheckReferences,
	} {
		_, err := NormalizeFanoutBenchmarkOptions(FanoutBenchmarkOptions{
			Mode:     fanoutModeSerial,
			Workload: workload,
			Runs:     1,
		})
		if err == nil {
			t.Fatalf("expected fixture workload %q without path to fail", workload)
		}
	}
}

func TestNormalizeFanoutBenchmarkOptionsRejectsMissingFixturePathDirectory(t *testing.T) {
	t.Parallel()

	_, err := NormalizeFanoutBenchmarkOptions(FanoutBenchmarkOptions{
		Mode:        fanoutModeSerial,
		Workload:    fanoutBenchmarkWorkloadToolOutline,
		FixturePath: filepath.Join(t.TempDir(), "missing"),
		Runs:        1,
	})
	if err == nil {
		t.Fatal("expected missing fixture_path directory to fail")
	}
}

func TestNormalizeFanoutBenchmarkOptionsComputesDeterministicFixtureDigest(t *testing.T) {
	fixtureDir := writeBenchmarkFixtureFiles(t)

	opts, err := NormalizeFanoutBenchmarkOptions(FanoutBenchmarkOptions{
		Mode:        fanoutModeSerial,
		Workload:    fanoutBenchmarkWorkloadToolOutline,
		FixturePath: fixtureDir,
		Runs:        1,
	})
	if err != nil {
		t.Fatalf("normalize options: %v", err)
	}
	if !strings.HasPrefix(opts.FixtureDigest, "sha256:") {
		t.Fatalf("expected fixture digest prefix, got %#v", opts)
	}
	if opts.FixtureFileCount != 3 {
		t.Fatalf("expected fixture file count to include all fixture files, got %#v", opts)
	}

	optsRepeat, err := NormalizeFanoutBenchmarkOptions(FanoutBenchmarkOptions{
		Mode:        fanoutModeSerial,
		Workload:    fanoutBenchmarkWorkloadToolOutline,
		FixturePath: fixtureDir,
		Runs:        1,
	})
	if err != nil {
		t.Fatalf("normalize options second pass: %v", err)
	}
	if optsRepeat.FixtureDigest != opts.FixtureDigest {
		t.Fatalf("expected deterministic fixture digest across repeated normalization, first=%q second=%q", opts.FixtureDigest, optsRepeat.FixtureDigest)
	}

	if err := os.WriteFile(
		filepath.Join(fixtureDir, "pkg/a.go"),
		[]byte("package pkg\n\nfunc Alpha() string { return \"changed\" }\n"),
		0o644,
	); err != nil {
		t.Fatalf("rewrite fixture file: %v", err)
	}
	optsMutated, err := NormalizeFanoutBenchmarkOptions(FanoutBenchmarkOptions{
		Mode:        fanoutModeSerial,
		Workload:    fanoutBenchmarkWorkloadToolOutline,
		FixturePath: fixtureDir,
		Runs:        1,
	})
	if err != nil {
		t.Fatalf("normalize options after fixture mutation: %v", err)
	}
	if optsMutated.FixtureDigest == opts.FixtureDigest {
		t.Fatalf("expected fixture digest to change after fixture mutation, before=%q after=%q", opts.FixtureDigest, optsMutated.FixtureDigest)
	}
}

func TestRunFanoutBenchmarkSerialMetrics(t *testing.T) {
	t.Parallel()

	metrics, err := RunFanoutBenchmark(context.Background(), FanoutBenchmarkOptions{
		Mode:           fanoutModeSerial,
		Runs:           6,
		BatchItems:     4,
		WorkIterations: 512,
		MaxWorkers:     1,
		MaxQueueDepth:  16,
	})
	if err != nil {
		t.Fatalf("run benchmark: %v", err)
	}

	if metrics.Workload != fanoutBenchmarkWorkloadSynthetic {
		t.Fatalf("expected synthetic workload, got %#v", metrics)
	}
	if metrics.Runs != 6 {
		t.Fatalf("unexpected runs: %#v", metrics)
	}
	if metrics.SuccessfulRuns != 6 || metrics.FailedRuns != 0 {
		t.Fatalf("unexpected run counts: %#v", metrics)
	}
	if metrics.ErrorRate != 0 {
		t.Fatalf("expected zero error rate, got %#v", metrics)
	}
	if metrics.ThroughputItemsPerSec <= 0 {
		t.Fatalf("expected positive throughput, got %#v", metrics)
	}
	if metrics.LatencyMeanMS <= 0 || metrics.LatencyP50MS <= 0 || metrics.LatencyP95MS <= 0 {
		t.Fatalf("expected positive latency metrics, got %#v", metrics)
	}
	if metrics.LatencyP95MS < metrics.LatencyP50MS {
		t.Fatalf("expected p95 >= p50, got %#v", metrics)
	}
}

func TestRunFanoutBenchmarkQueueDepthFailureIsMeasured(t *testing.T) {
	t.Parallel()

	metrics, err := RunFanoutBenchmark(context.Background(), FanoutBenchmarkOptions{
		Mode:           fanoutModeParallel,
		Runs:           5,
		BatchItems:     4,
		WorkIterations: 64,
		MaxWorkers:     2,
		MaxQueueDepth:  1,
	})
	if err != nil {
		t.Fatalf("run benchmark: %v", err)
	}

	if metrics.SuccessfulRuns != 0 || metrics.FailedRuns != 5 {
		t.Fatalf("expected all runs to fail queue-depth guard, got %#v", metrics)
	}
	if metrics.ErrorRate != 1 {
		t.Fatalf("expected error_rate=1, got %#v", metrics)
	}
	if !strings.Contains(metrics.FirstError, "fanout queue depth limit") {
		t.Fatalf("expected queue-depth failure in first error, got %#v", metrics)
	}
}

func TestRunFanoutBenchmarkToolFixtureOutlineMetrics(t *testing.T) {
	t.Parallel()

	fixtureDir := writeBenchmarkFixtureFiles(t)

	metrics, err := RunFanoutBenchmark(context.Background(), FanoutBenchmarkOptions{
		Mode:          fanoutModeParallel,
		Workload:      fanoutBenchmarkWorkloadToolOutline,
		FixturePath:   fixtureDir,
		Runs:          4,
		BatchItems:    3,
		MaxWorkers:    2,
		MaxQueueDepth: 32,
	})
	if err != nil {
		t.Fatalf("run fixture workload benchmark: %v", err)
	}

	if metrics.Workload != fanoutBenchmarkWorkloadToolOutline {
		t.Fatalf("unexpected workload in metrics: %#v", metrics)
	}
	if !strings.HasPrefix(metrics.FixtureDigest, "sha256:") {
		t.Fatalf("expected fixture digest in metrics, got %#v", metrics)
	}
	if metrics.FixtureFileCount != 3 {
		t.Fatalf("expected fixture file count in metrics, got %#v", metrics)
	}
	if metrics.SuccessfulRuns != 4 || metrics.FailedRuns != 0 {
		t.Fatalf("unexpected run counts: %#v", metrics)
	}
	if metrics.ThroughputItemsPerSec <= 0 {
		t.Fatalf("expected positive throughput: %#v", metrics)
	}
	if metrics.ErrorRate != 0 {
		t.Fatalf("expected zero error rate: %#v", metrics)
	}
}

func TestRunFanoutBenchmarkToolFixtureFindImportersMetrics(t *testing.T) {
	t.Parallel()

	fixtureDir := writeBenchmarkFixtureFiles(t)
	metrics, err := RunFanoutBenchmark(context.Background(), FanoutBenchmarkOptions{
		Mode:          fanoutModeParallel,
		Workload:      fanoutBenchmarkWorkloadFindImporters,
		FixturePath:   fixtureDir,
		Runs:          4,
		BatchItems:    3,
		MaxWorkers:    2,
		MaxQueueDepth: 32,
	})
	if err != nil {
		t.Fatalf("run fixture workload benchmark: %v", err)
	}

	if metrics.Workload != fanoutBenchmarkWorkloadFindImporters {
		t.Fatalf("unexpected workload in metrics: %#v", metrics)
	}
	if metrics.SuccessfulRuns != 4 || metrics.FailedRuns != 0 {
		t.Fatalf("unexpected run counts: %#v", metrics)
	}
	if metrics.ThroughputItemsPerSec <= 0 {
		t.Fatalf("expected positive throughput: %#v", metrics)
	}
	if metrics.ErrorRate != 0 {
		t.Fatalf("expected zero error rate: %#v", metrics)
	}
}

func TestRunFanoutBenchmarkToolFixtureFindReferencesMetrics(t *testing.T) {
	t.Parallel()

	fixtureDir := writeBenchmarkFixtureFiles(t)
	metrics, err := RunFanoutBenchmark(context.Background(), FanoutBenchmarkOptions{
		Mode:          fanoutModeParallel,
		Workload:      fanoutBenchmarkWorkloadFindReferences,
		FixturePath:   fixtureDir,
		Runs:          4,
		BatchItems:    3,
		MaxWorkers:    2,
		MaxQueueDepth: 32,
	})
	if err != nil {
		t.Fatalf("run fixture workload benchmark: %v", err)
	}

	if metrics.Workload != fanoutBenchmarkWorkloadFindReferences {
		t.Fatalf("unexpected workload in metrics: %#v", metrics)
	}
	if metrics.SuccessfulRuns != 4 || metrics.FailedRuns != 0 {
		t.Fatalf("unexpected run counts: %#v", metrics)
	}
	if metrics.ThroughputItemsPerSec <= 0 {
		t.Fatalf("expected positive throughput: %#v", metrics)
	}
	if metrics.ErrorRate != 0 {
		t.Fatalf("expected zero error rate: %#v", metrics)
	}
}

func TestRunFanoutBenchmarkToolFixtureCheckReferencesMetrics(t *testing.T) {
	t.Parallel()

	fixtureDir := writeBenchmarkFixtureFiles(t)
	metrics, err := RunFanoutBenchmark(context.Background(), FanoutBenchmarkOptions{
		Mode:          fanoutModeParallel,
		Workload:      fanoutBenchmarkWorkloadCheckReferences,
		FixturePath:   fixtureDir,
		Runs:          4,
		BatchItems:    3,
		MaxWorkers:    2,
		MaxQueueDepth: 32,
	})
	if err != nil {
		t.Fatalf("run fixture workload benchmark: %v", err)
	}

	if metrics.Workload != fanoutBenchmarkWorkloadCheckReferences {
		t.Fatalf("unexpected workload in metrics: %#v", metrics)
	}
	if metrics.SuccessfulRuns != 4 || metrics.FailedRuns != 0 {
		t.Fatalf("unexpected run counts: %#v", metrics)
	}
	if metrics.ThroughputItemsPerSec <= 0 {
		t.Fatalf("expected positive throughput: %#v", metrics)
	}
	if metrics.ErrorRate != 0 {
		t.Fatalf("expected zero error rate: %#v", metrics)
	}
}

func writeBenchmarkFixtureFiles(t *testing.T) string {
	t.Helper()

	fixtureDir := t.TempDir()
	files := map[string]string{
		"pkg/a.go":    "package pkg\n\nimport \"fmt\"\n\nfunc Alpha() { fmt.Println(\"alpha\") }\n",
		"pkg/b.go":    "package pkg\n\nfunc Beta() {}\n",
		"app/main.py": "from pkg import alpha\n\ndef gamma():\n    return alpha\n",
	}
	for relPath, content := range files {
		absPath := filepath.Join(fixtureDir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			t.Fatalf("mkdir fixture path: %v", err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture file: %v", err)
		}
	}

	return fixtureDir
}
