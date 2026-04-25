package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/orchestration"
)

func TestRunWithArgsWritesPassingReport(t *testing.T) {
	t.Parallel()

	outPath := filepath.Join(t.TempDir(), "serial-baseline.json")
	args := []string{
		"--mode", "serial",
		"--runs", "3",
		"--batch-items", "4",
		"--work-iterations", "256",
		"--max-workers", "1",
		"--queue-depth", "32",
		"--min-throughput-items-per-sec", "0",
		"--max-p95-ms", "1000",
		"--max-error-rate", "0",
		"--out", outPath,
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%s", code, stderr.String())
	}

	payload, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}

	report := report{}
	if err := json.Unmarshal(payload, &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Verdict != "pass" {
		t.Fatalf("expected pass verdict, got %#v", report)
	}
	if report.Benchmark.SuccessfulRuns != 3 || report.Benchmark.FailedRuns != 0 {
		t.Fatalf("unexpected benchmark run counts: %#v", report.Benchmark)
	}
}

func TestRunWithArgsFailsGateWhenThresholdIsTooHigh(t *testing.T) {
	t.Parallel()

	args := []string{
		"--mode", "serial",
		"--runs", "3",
		"--batch-items", "4",
		"--work-iterations", "256",
		"--max-workers", "1",
		"--queue-depth", "32",
		"--min-throughput-items-per-sec", "1000000000",
		"--max-p95-ms", "1000",
		"--max-error-rate", "0",
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(args, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected gate failure exit code 1, got %d stderr=%s", code, stderr.String())
	}

	report := report{}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode stdout report: %v output=%s", err, stdout.String())
	}
	if report.Verdict != "fail" {
		t.Fatalf("expected fail verdict, got %#v", report)
	}
}

func TestRunWithArgsRequiresBaselineWhenComparisonGateEnabled(t *testing.T) {
	t.Parallel()

	args := []string{
		"--mode", "parallel",
		"--runs", "2",
		"--batch-items", "4",
		"--work-iterations", "128",
		"--max-workers", "2",
		"--queue-depth", "32",
		"--min-throughput-ratio-vs-baseline", "1.10",
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(args, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected invalid-input exit code 2, got %d stderr=%s", code, stderr.String())
	}
}

func TestRunWithArgsRequiresFixturePathForToolFixtureWorkload(t *testing.T) {
	t.Parallel()

	for _, workload := range []string{
		"tool_get_file_outline_fixture",
		"tool_find_importers_fixture",
		"tool_find_references_fixture",
		"tool_check_references_fixture",
	} {
		args := []string{
			"--mode", "serial",
			"--workload", workload,
			"--runs", "2",
			"--batch-items", "2",
			"--max-workers", "1",
			"--queue-depth", "8",
		}

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := runWithArgs(args, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("expected invalid-input exit code 2 for workload %q, got %d stderr=%s", workload, code, stderr.String())
		}
	}
}

func TestRunWithArgsToolFixtureWorkloadPasses(t *testing.T) {
	t.Parallel()

	fixtureDir := writeBenchmarkFixture(t)
	for _, workload := range []string{
		"tool_get_file_outline_fixture",
		"tool_find_importers_fixture",
		"tool_find_references_fixture",
		"tool_check_references_fixture",
	} {
		args := []string{
			"--mode", "parallel",
			"--workload", workload,
			"--fixture-path", fixtureDir,
			"--runs", "3",
			"--batch-items", "2",
			"--max-workers", "2",
			"--queue-depth", "16",
			"--min-fixture-file-count", "3",
			"--max-p95-ms", "2000",
			"--max-error-rate", "0",
		}

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := runWithArgs(args, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("expected exit code 0 for workload %q, got %d stderr=%s", workload, code, stderr.String())
		}

		decoded := report{}
		if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
			t.Fatalf("decode report: %v", err)
		}
		if decoded.Benchmark.Workload != workload {
			t.Fatalf("expected workload %q in benchmark report, got %#v", workload, decoded.Benchmark)
		}
		if decoded.Benchmark.FixtureDigest == "" {
			t.Fatalf("expected fixture digest in benchmark report for workload %q: %#v", workload, decoded.Benchmark)
		}
		if decoded.Benchmark.FixtureFileCount <= 0 {
			t.Fatalf("expected positive fixture file count in benchmark report for workload %q: %#v", workload, decoded.Benchmark)
		}
		if decoded.Thresholds.MinFixtureFileCount != 3 {
			t.Fatalf("expected min fixture file count threshold to round-trip for workload %q: %#v", workload, decoded.Thresholds)
		}
		if decoded.Verdict != "pass" {
			t.Fatalf("expected pass verdict for workload %q, got %#v", workload, decoded)
		}
	}
}

func TestRunWithArgsRejectsMinFixtureFileCountForNonFixtureWorkload(t *testing.T) {
	t.Parallel()

	args := []string{
		"--mode", "serial",
		"--workload", "synthetic",
		"--runs", "2",
		"--batch-items", "2",
		"--max-workers", "1",
		"--queue-depth", "8",
		"--min-fixture-file-count", "1",
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(args, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected invalid-input exit code 2, got %d stderr=%s", code, stderr.String())
	}
}

func TestRunWithArgsRejectsFixtureWorkloadBelowMinFixtureFileCount(t *testing.T) {
	t.Parallel()

	fixtureDir := writeBenchmarkFixture(t)
	args := []string{
		"--mode", "serial",
		"--workload", "tool_get_file_outline_fixture",
		"--fixture-path", fixtureDir,
		"--runs", "2",
		"--batch-items", "2",
		"--max-workers", "1",
		"--queue-depth", "8",
		"--min-fixture-file-count", "999",
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(args, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected invalid-input exit code 2, got %d stderr=%s", code, stderr.String())
	}
}

func TestRunWithArgsEvaluatesBaselineComparisonGate(t *testing.T) {
	t.Parallel()

	baseline := report{
		GeneratedAtUTC: "2026-03-30T00:00:00Z",
		Benchmark: orchestration.FanoutBenchmarkMetrics{
			Mode:                  "serial",
			Workload:              "synthetic",
			Runs:                  3,
			BatchItems:            4,
			WorkIterations:        256,
			MaxWorkers:            1,
			MaxQueueDepth:         32,
			SuccessfulRuns:        3,
			FailedRuns:            0,
			ErrorRate:             0,
			ThroughputItemsPerSec: 1_000_000_000,
			LatencyP95MS:          0.001,
		},
		Verdict: "pass",
	}
	baselinePath := writeBaselineReportFile(t, baseline)

	args := []string{
		"--mode", "parallel",
		"--runs", "3",
		"--batch-items", "4",
		"--work-iterations", "256",
		"--max-workers", "2",
		"--queue-depth", "32",
		"--max-p95-ms", "1000",
		"--max-error-rate", "0",
		"--baseline-report", baselinePath,
		"--min-throughput-ratio-vs-baseline", "0.10",
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(args, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected gate failure exit code 1, got %d stderr=%s", code, stderr.String())
	}

	decoded := report{}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode stdout report: %v output=%s", err, stdout.String())
	}
	if decoded.Comparison == nil {
		t.Fatalf("expected comparison metrics to be present: %#v", decoded)
	}
	if decoded.Comparison.BaselineReportPath != baselinePath {
		t.Fatalf("expected baseline report path to round-trip, got %#v", decoded.Comparison)
	}
	if decoded.Verdict != "fail" {
		t.Fatalf("expected comparison gate failure verdict, got %#v", decoded)
	}
}

func TestRunWithArgsRejectsIncompatibleBaselineReport(t *testing.T) {
	t.Parallel()

	baselinePath := writeBaselineReportFile(t, report{
		GeneratedAtUTC: "2026-03-30T00:00:00Z",
		Benchmark: orchestration.FanoutBenchmarkMetrics{
			Mode:                  "serial",
			Workload:              "tool_find_importers_fixture",
			Runs:                  3,
			BatchItems:            4,
			WorkIterations:        12000,
			MaxWorkers:            1,
			MaxQueueDepth:         32,
			SuccessfulRuns:        3,
			FailedRuns:            0,
			ErrorRate:             0,
			ThroughputItemsPerSec: 100,
			LatencyP95MS:          2,
		},
		Verdict: "pass",
	})

	args := []string{
		"--mode", "parallel",
		"--workload", "tool_get_file_outline_fixture",
		"--fixture-path", writeBenchmarkFixture(t),
		"--runs", "3",
		"--batch-items", "4",
		"--max-workers", "2",
		"--queue-depth", "32",
		"--max-p95-ms", "1000",
		"--max-error-rate", "0",
		"--baseline-report", baselinePath,
		"--min-throughput-ratio-vs-baseline", "0.1",
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(args, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected incompatible-baseline exit code 2, got %d stderr=%s", code, stderr.String())
	}
}

func TestRunWithArgsRejectsNonPassingBaselineVerdict(t *testing.T) {
	t.Parallel()

	baselinePath := writeBaselineReportFile(t, report{
		GeneratedAtUTC: "2026-03-30T00:00:00Z",
		Benchmark: orchestration.FanoutBenchmarkMetrics{
			Mode:                  "serial",
			Workload:              "synthetic",
			Runs:                  3,
			BatchItems:            4,
			WorkIterations:        256,
			MaxWorkers:            1,
			MaxQueueDepth:         32,
			SuccessfulRuns:        2,
			FailedRuns:            1,
			ErrorRate:             0.33,
			ThroughputItemsPerSec: 10,
			LatencyP95MS:          100,
		},
		Verdict: "fail",
	})

	args := []string{
		"--mode", "parallel",
		"--runs", "3",
		"--batch-items", "4",
		"--work-iterations", "256",
		"--max-workers", "2",
		"--queue-depth", "32",
		"--max-p95-ms", "1000",
		"--max-error-rate", "0",
		"--baseline-report", baselinePath,
		"--min-throughput-ratio-vs-baseline", "0.1",
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(args, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected invalid-baseline exit code 2, got %d stderr=%s", code, stderr.String())
	}
}

func TestRunWithArgsRejectsBaselineReportShapeMismatches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		currentMode      string
		currentMaxWorker string
		baselineMetrics  orchestration.FanoutBenchmarkMetrics
	}{
		{
			name:             "runs mismatch",
			currentMode:      "parallel",
			currentMaxWorker: "2",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:                  "serial",
				Workload:              "synthetic",
				Runs:                  5,
				BatchItems:            4,
				WorkIterations:        256,
				MaxWorkers:            1,
				MaxQueueDepth:         32,
				SuccessfulRuns:        5,
				FailedRuns:            0,
				ErrorRate:             0,
				ThroughputItemsPerSec: 100,
				LatencyP95MS:          2,
			},
		},
		{
			name:             "work-iterations mismatch",
			currentMode:      "parallel",
			currentMaxWorker: "2",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:                  "serial",
				Workload:              "synthetic",
				Runs:                  3,
				BatchItems:            4,
				WorkIterations:        1024,
				MaxWorkers:            1,
				MaxQueueDepth:         32,
				SuccessfulRuns:        3,
				FailedRuns:            0,
				ErrorRate:             0,
				ThroughputItemsPerSec: 100,
				LatencyP95MS:          2,
			},
		},
		{
			name:             "queue-depth mismatch",
			currentMode:      "parallel",
			currentMaxWorker: "2",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:                  "serial",
				Workload:              "synthetic",
				Runs:                  3,
				BatchItems:            4,
				WorkIterations:        256,
				MaxWorkers:            1,
				MaxQueueDepth:         64,
				SuccessfulRuns:        3,
				FailedRuns:            0,
				ErrorRate:             0,
				ThroughputItemsPerSec: 100,
				LatencyP95MS:          2,
			},
		},
		{
			name:             "same-mode max-workers mismatch",
			currentMode:      "parallel",
			currentMaxWorker: "2",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:                  "parallel",
				Workload:              "synthetic",
				Runs:                  3,
				BatchItems:            4,
				WorkIterations:        256,
				MaxWorkers:            1,
				MaxQueueDepth:         32,
				SuccessfulRuns:        3,
				FailedRuns:            0,
				ErrorRate:             0,
				ThroughputItemsPerSec: 100,
				LatencyP95MS:          2,
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			baselinePath := writeBaselineReportFile(t, report{
				GeneratedAtUTC: "2026-03-30T00:00:00Z",
				Benchmark:      tc.baselineMetrics,
				Verdict:        "pass",
			})

			args := []string{
				"--mode", tc.currentMode,
				"--runs", "3",
				"--batch-items", "4",
				"--work-iterations", "256",
				"--max-workers", tc.currentMaxWorker,
				"--queue-depth", "32",
				"--max-p95-ms", "1000",
				"--max-error-rate", "0",
				"--baseline-report", baselinePath,
				"--min-throughput-ratio-vs-baseline", "0.1",
			}

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := runWithArgs(args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("expected invalid-baseline exit code 2, got %d stderr=%s", code, stderr.String())
			}
		})
	}
}

func TestRunWithArgsRejectsBaselineReportMissingRequiredMetadata(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		baselineMetrics orchestration.FanoutBenchmarkMetrics
	}{
		{
			name: "missing mode",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Workload:       "synthetic",
				Runs:           3,
				BatchItems:     4,
				WorkIterations: 256,
				MaxWorkers:     1,
				MaxQueueDepth:  32,
			},
		},
		{
			name: "missing workload",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:           "serial",
				Runs:           3,
				BatchItems:     4,
				WorkIterations: 256,
				MaxWorkers:     1,
				MaxQueueDepth:  32,
			},
		},
		{
			name: "missing runs",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:           "serial",
				Workload:       "synthetic",
				BatchItems:     4,
				WorkIterations: 256,
				MaxWorkers:     1,
				MaxQueueDepth:  32,
			},
		},
		{
			name: "missing batch-items",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:           "serial",
				Workload:       "synthetic",
				Runs:           3,
				WorkIterations: 256,
				MaxWorkers:     1,
				MaxQueueDepth:  32,
			},
		},
		{
			name: "missing work-iterations",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:          "serial",
				Workload:      "synthetic",
				Runs:          3,
				BatchItems:    4,
				MaxWorkers:    1,
				MaxQueueDepth: 32,
			},
		},
		{
			name: "missing max-workers",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:           "serial",
				Workload:       "synthetic",
				Runs:           3,
				BatchItems:     4,
				WorkIterations: 256,
				MaxQueueDepth:  32,
			},
		},
		{
			name: "missing queue-depth",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:           "serial",
				Workload:       "synthetic",
				Runs:           3,
				BatchItems:     4,
				WorkIterations: 256,
				MaxWorkers:     1,
			},
		},
		{
			name: "invalid mode",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:           "invalid",
				Workload:       "synthetic",
				Runs:           3,
				BatchItems:     4,
				WorkIterations: 256,
				MaxWorkers:     1,
				MaxQueueDepth:  32,
			},
		},
		{
			name: "fixture workload missing fixture digest",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:           "serial",
				Workload:       "tool_get_file_outline_fixture",
				Runs:           3,
				BatchItems:     4,
				WorkIterations: 256,
				MaxWorkers:     1,
				MaxQueueDepth:  32,
			},
		},
		{
			name: "fixture workload missing fixture file count",
			baselineMetrics: orchestration.FanoutBenchmarkMetrics{
				Mode:           "serial",
				Workload:       "tool_get_file_outline_fixture",
				FixtureDigest:  "sha256:deadbeef",
				Runs:           3,
				BatchItems:     4,
				WorkIterations: 256,
				MaxWorkers:     1,
				MaxQueueDepth:  32,
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			baselinePath := writeBaselineReportFile(t, report{
				GeneratedAtUTC: "2026-03-30T00:00:00Z",
				Benchmark:      tc.baselineMetrics,
				Verdict:        "pass",
			})

			args := []string{
				"--mode", "parallel",
				"--runs", "3",
				"--batch-items", "4",
				"--work-iterations", "256",
				"--max-workers", "2",
				"--queue-depth", "32",
				"--max-p95-ms", "1000",
				"--max-error-rate", "0",
				"--baseline-report", baselinePath,
				"--min-throughput-ratio-vs-baseline", "0.1",
			}

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := runWithArgs(args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("expected invalid-baseline exit code 2, got %d stderr=%s", code, stderr.String())
			}
		})
	}
}

func TestRunWithArgsRejectsFixtureBaselineCompatibilityMismatches(t *testing.T) {
	t.Parallel()

	fixtureDir := writeBenchmarkFixture(t)
	normalized, err := orchestration.NormalizeFanoutBenchmarkOptions(orchestration.FanoutBenchmarkOptions{
		Mode:           "parallel",
		Workload:       "tool_get_file_outline_fixture",
		FixturePath:    fixtureDir,
		Runs:           3,
		BatchItems:     4,
		WorkIterations: 256,
		MaxWorkers:     2,
		MaxQueueDepth:  32,
	})
	if err != nil {
		t.Fatalf("normalize benchmark options: %v", err)
	}

	cases := []struct {
		name             string
		fixtureDigest    string
		fixtureFileCount int
	}{
		{
			name:             "fixture digest mismatch",
			fixtureDigest:    "sha256:deadbeef",
			fixtureFileCount: normalized.FixtureFileCount,
		},
		{
			name:             "fixture file count mismatch",
			fixtureDigest:    normalized.FixtureDigest,
			fixtureFileCount: normalized.FixtureFileCount + 1,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			baselinePath := writeBaselineReportFile(t, report{
				GeneratedAtUTC: "2026-03-30T00:00:00Z",
				Benchmark: orchestration.FanoutBenchmarkMetrics{
					Mode:                  "serial",
					Workload:              "tool_get_file_outline_fixture",
					FixtureDigest:         tc.fixtureDigest,
					FixtureFileCount:      tc.fixtureFileCount,
					Runs:                  3,
					BatchItems:            4,
					WorkIterations:        256,
					MaxWorkers:            1,
					MaxQueueDepth:         32,
					SuccessfulRuns:        3,
					FailedRuns:            0,
					ErrorRate:             0,
					ThroughputItemsPerSec: 100,
					LatencyP95MS:          2,
				},
				Verdict: "pass",
			})

			args := []string{
				"--mode", "parallel",
				"--workload", "tool_get_file_outline_fixture",
				"--fixture-path", fixtureDir,
				"--runs", "3",
				"--batch-items", "4",
				"--work-iterations", "256",
				"--max-workers", "2",
				"--queue-depth", "32",
				"--max-p95-ms", "1000",
				"--max-error-rate", "0",
				"--baseline-report", baselinePath,
				"--min-throughput-ratio-vs-baseline", "0.1",
			}

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := runWithArgs(args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("expected invalid-baseline exit code 2, got %d stderr=%s", code, stderr.String())
			}
		})
	}
}

func TestRunWithArgsRejectsInvalidBenchmarkOptions(t *testing.T) {
	t.Parallel()

	args := []string{
		"--mode", "serial",
		"--runs", "-1",
		"--batch-items", "-1",
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(args, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected invalid-input exit code 2, got %d stderr=%s", code, stderr.String())
	}
}

func TestValidateThresholds(t *testing.T) {
	t.Parallel()

	if err := validateThresholds(sloThresholds{
		MinThroughputItemsPerSec: 0,
		MaxLatencyP95MS:          0,
		MaxErrorRate:             0,
	}); err != nil {
		t.Fatalf("expected zero thresholds to be valid: %v", err)
	}

	if err := validateThresholds(sloThresholds{MaxErrorRate: 1.2}); err == nil {
		t.Fatal("expected max-error-rate > 1 to be invalid")
	}
	if err := validateThresholds(sloThresholds{MaxErrorRateDeltaVsBase: 1.2}); err == nil {
		t.Fatal("expected max-error-rate-delta-vs-baseline > 1 to be invalid")
	}
	if err := validateThresholds(sloThresholds{MinFixtureFileCount: -1}); err == nil {
		t.Fatal("expected min-fixture-file-count < 0 to be invalid")
	}
}

func TestEvaluateGateWithBaselineComparison(t *testing.T) {
	t.Parallel()

	metrics := orchestration.FanoutBenchmarkMetrics{
		ErrorRate:             0.05,
		ThroughputItemsPerSec: 250,
		LatencyP95MS:          15,
	}
	comparison := &baselineComparison{
		BaselineThroughputItemsPerSec: 100,
		BaselineLatencyP95MS:          12,
		ThroughputRatioVsBaseline:     2.5,
		LatencyP95RatioVsBaseline:     1.25,
		ErrorRateDeltaVsBaseline:      0.02,
	}

	pass := evaluateGate(metrics, sloThresholds{
		MaxErrorRate:             0.10,
		MinThroughputRatioVsBase: 2.0,
		MaxP95RatioVsBase:        1.3,
		MaxErrorRateDeltaVsBase:  0.03,
	}, comparison)
	if !pass {
		t.Fatal("expected baseline comparison gate to pass")
	}

	fail := evaluateGate(metrics, sloThresholds{
		MaxErrorRate:             0.10,
		MinThroughputRatioVsBase: 2.6,
	}, comparison)
	if fail {
		t.Fatal("expected throughput-ratio gate to fail")
	}
}

func writeBenchmarkFixture(t *testing.T) string {
	t.Helper()

	fixtureDir := t.TempDir()
	files := map[string]string{
		"pkg/a.go":    "package pkg\n\nimport \"fmt\"\n\nfunc A() { fmt.Println(\"a\") }\n",
		"pkg/b.go":    "package pkg\n\nimport \"pkg/a\"\n\nfunc B() { a.A() }\n",
		"app/main.py": "from pkg import a\n\ndef run():\n    return a\n",
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

func writeBaselineReportFile(t *testing.T, baseline report) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "baseline.json")
	encodedBaseline, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		t.Fatalf("encode baseline report: %v", err)
	}
	if err := os.WriteFile(path, append(encodedBaseline, '\n'), 0o644); err != nil {
		t.Fatalf("write baseline report: %v", err)
	}

	return path
}
