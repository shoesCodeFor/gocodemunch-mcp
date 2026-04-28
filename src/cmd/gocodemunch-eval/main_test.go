package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
)

func TestRunWithArgsEmitsPerQueryAndAggregateMetrics(t *testing.T) {
	fixturesDir := writeEvalFixtures(t, fixtureCorpus{
		Dataset: "eval-fixtures-test",
		Documents: []fixtureDocument{
			{ID: "doc-a", Path: "fixtures/a.go", Language: "go", Text: "json decode"},
			{ID: "doc-b", Path: "fixtures/b.go", Language: "go", Text: "http timeout"},
			{ID: "doc-c", Path: "fixtures/c.go", Language: "go", Text: "context cancel"},
		},
	}, fixtureQueries{
		Dataset: "eval-fixtures-test",
		Queries: []fixtureRow{
			{ID: "q-1", Query: "decode json", TopK: 2},
			{ID: "q-2", Query: "cancel context", TopK: 2},
		},
	}, fixtureRelevance{
		Dataset: "eval-fixtures-test",
		Judgments: []fixtureJudgment{
			{QueryID: "q-1", DocID: "doc-a", Relevance: 3},
			{QueryID: "q-1", DocID: "doc-b", Relevance: 1},
			{QueryID: "q-2", DocID: "doc-c", Relevance: 3},
		},
	})

	backend := &scriptedVectorBackend{queryPlans: [][]string{
		{"doc-b", "doc-c"},
		{"doc-a", "doc-c"},
	}}

	restore := overrideEvalRunnerHooks(
		func() (config.Config, error) {
			return config.Config{
				EmbeddingProvider:    "ollama",
				VectorBackend:        "sqlite",
				EmbeddingModel:       "eval-test-model",
				VectorQueryTimeoutMS: 500,
			}, nil
		},
		func(config.Config, string) (indexing.VectorBackend, error) {
			return backend, nil
		},
		func(config.Config, string) (indexing.Embedder, error) {
			return staticEmbedder{}, nil
		},
		func(indexing.VectorBackend) error { return nil },
		func() time.Time { return time.Date(2026, time.April, 28, 10, 0, 0, 0, time.UTC) },
	)
	defer restore()

	args := []string{"--fixtures-dir", fixturesDir, "--skip-markdown-report"}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := runWithArgs(args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%s", code, stderr.String())
	}

	report := evalRunReport{}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report json: %v output=%s", err, stdout.String())
	}

	if report.Dataset != "eval-fixtures-test" {
		t.Fatalf("unexpected dataset: %#v", report)
	}
	if !report.GatePassed {
		t.Fatalf("expected gate_passed=true without configured thresholds, got %#v", report)
	}
	if len(report.GateFailures) != 0 {
		t.Fatalf("expected no gate failures without configured thresholds, got %#v", report.GateFailures)
	}
	if len(report.Combinations) != 1 {
		t.Fatalf("expected one combination report, got %#v", report.Combinations)
	}

	combo := report.Combinations[0]
	if combo.Provider != "ollama" || combo.Backend != "sqlite" {
		t.Fatalf("unexpected combo identity: %#v", combo)
	}
	if combo.Model != "eval-test-model" {
		t.Fatalf("expected model eval-test-model, got %#v", combo)
	}
	if combo.IndexedDocs != 3 {
		t.Fatalf("expected 3 indexed docs, got %#v", combo)
	}
	if len(combo.PerQuery) != 2 {
		t.Fatalf("expected 2 per-query rows, got %#v", combo.PerQuery)
	}

	if got := combo.PerQuery[0].QueryID; got != "q-1" {
		t.Fatalf("expected first query id q-1, got %q", got)
	}
	if got := combo.PerQuery[0].RecallAtK; got != 0.5 {
		t.Fatalf("expected q-1 recall@k 0.5, got %v", got)
	}
	if got := combo.PerQuery[0].MRRAtK; got != 1 {
		t.Fatalf("expected q-1 mrr@k 1, got %v", got)
	}

	if got := combo.PerQuery[1].QueryID; got != "q-2" {
		t.Fatalf("expected second query id q-2, got %q", got)
	}
	if got := combo.PerQuery[1].RecallAtK; got != 1 {
		t.Fatalf("expected q-2 recall@k 1, got %v", got)
	}
	if got := combo.PerQuery[1].MRRAtK; got != 0.5 {
		t.Fatalf("expected q-2 mrr@k 0.5, got %v", got)
	}

	if got := combo.Aggregate.QueryCount; got != 2 {
		t.Fatalf("expected query_count 2, got %#v", combo.Aggregate)
	}
	if got := combo.Aggregate.MeanRecallAtK; got != 0.75 {
		t.Fatalf("expected mean recall@k 0.75, got %v", got)
	}
	if got := combo.Aggregate.MeanMRRAtK; got != 0.75 {
		t.Fatalf("expected mean mrr@k 0.75, got %v", got)
	}
	if combo.Aggregate.LatencyMetrics.P50MS < 0 || combo.Aggregate.LatencyMetrics.P95MS < 0 {
		t.Fatalf("latency percentiles must be non-negative: %#v", combo.Aggregate.LatencyMetrics)
	}
	if !combo.Gate.Passed || len(combo.Gate.FailedChecks) != 0 {
		t.Fatalf("expected combo gate to pass without thresholds, got %#v", combo.Gate)
	}
}

func TestRunWithArgsRejectsUnsupportedProvider(t *testing.T) {
	fixturesDir := writeEvalFixtures(t, fixtureCorpus{
		Dataset:   "eval-fixtures-test",
		Documents: []fixtureDocument{{ID: "doc-a", Path: "fixtures/a.go", Language: "go", Text: "json decode"}},
	}, fixtureQueries{
		Dataset: "eval-fixtures-test",
		Queries: []fixtureRow{{ID: "q-1", Query: "decode json", TopK: 1}},
	}, fixtureRelevance{
		Dataset:   "eval-fixtures-test",
		Judgments: []fixtureJudgment{{QueryID: "q-1", DocID: "doc-a", Relevance: 3}},
	})

	restore := overrideEvalRunnerHooks(
		func() (config.Config, error) {
			return config.Config{EmbeddingProvider: "ollama", VectorBackend: "sqlite"}, nil
		},
		createBackendFn,
		createEmbedderFn,
		closeBackendFn,
		nowUTCFn,
	)
	defer restore()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(
		[]string{"--fixtures-dir", fixturesDir, "--providers", "openai", "--skip-markdown-report"},
		&stdout,
		&stderr,
	)
	if code != 2 {
		t.Fatalf("expected invalid-input exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "unsupported embedding provider") {
		t.Fatalf("expected unsupported provider error, stderr=%s", stderr.String())
	}
}

func TestRunWithArgsRejectsDatasetMismatch(t *testing.T) {
	fixturesDir := writeEvalFixtures(t, fixtureCorpus{
		Dataset:   "dataset-a",
		Documents: []fixtureDocument{{ID: "doc-a", Path: "fixtures/a.go", Language: "go", Text: "json decode"}},
	}, fixtureQueries{
		Dataset: "dataset-b",
		Queries: []fixtureRow{{ID: "q-1", Query: "decode json", TopK: 1}},
	}, fixtureRelevance{
		Dataset:   "dataset-a",
		Judgments: []fixtureJudgment{{QueryID: "q-1", DocID: "doc-a", Relevance: 3}},
	})

	restore := overrideEvalRunnerHooks(
		func() (config.Config, error) {
			return config.Config{EmbeddingProvider: "ollama", VectorBackend: "sqlite"}, nil
		},
		createBackendFn,
		createEmbedderFn,
		closeBackendFn,
		nowUTCFn,
	)
	defer restore()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs([]string{"--fixtures-dir", fixturesDir, "--skip-markdown-report"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected invalid-input exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "fixture dataset mismatch") {
		t.Fatalf("expected fixture dataset mismatch error, stderr=%s", stderr.String())
	}
}

func TestRunWithArgsThresholdGateFailsWhenQualityMissed(t *testing.T) {
	fixturesDir := writeEvalFixtures(t, fixtureCorpus{
		Dataset: "eval-fixtures-test",
		Documents: []fixtureDocument{
			{ID: "doc-a", Path: "fixtures/a.go", Language: "go", Text: "json decode"},
			{ID: "doc-b", Path: "fixtures/b.go", Language: "go", Text: "http timeout"},
			{ID: "doc-c", Path: "fixtures/c.go", Language: "go", Text: "context cancel"},
		},
	}, fixtureQueries{
		Dataset: "eval-fixtures-test",
		Queries: []fixtureRow{
			{ID: "q-1", Query: "decode json", TopK: 2},
			{ID: "q-2", Query: "cancel context", TopK: 2},
		},
	}, fixtureRelevance{
		Dataset: "eval-fixtures-test",
		Judgments: []fixtureJudgment{
			{QueryID: "q-1", DocID: "doc-a", Relevance: 3},
			{QueryID: "q-1", DocID: "doc-b", Relevance: 1},
			{QueryID: "q-2", DocID: "doc-c", Relevance: 3},
		},
	})

	backend := &scriptedVectorBackend{queryPlans: [][]string{
		{"doc-c", "doc-b"},
		{"doc-a", "doc-b"},
	}}

	restore := overrideEvalRunnerHooks(
		func() (config.Config, error) {
			return config.Config{
				EmbeddingProvider:    "ollama",
				VectorBackend:        "sqlite",
				EmbeddingModel:       "eval-test-model",
				VectorQueryTimeoutMS: 500,
			}, nil
		},
		func(config.Config, string) (indexing.VectorBackend, error) {
			return backend, nil
		},
		func(config.Config, string) (indexing.Embedder, error) {
			return staticEmbedder{}, nil
		},
		func(indexing.VectorBackend) error { return nil },
		func() time.Time { return time.Date(2026, time.April, 28, 10, 0, 0, 0, time.UTC) },
	)
	defer restore()

	t.Setenv("GOCODEMUNCH_EVAL_MIN_MEAN_RECALL_AT_K", "0.90")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs([]string{"--fixtures-dir", fixturesDir, "--skip-markdown-report"}, &stdout, &stderr)
	if code != evalGateFailureExitCode {
		t.Fatalf("expected gate failure exit code %d, got %d stderr=%s", evalGateFailureExitCode, code, stderr.String())
	}

	report := evalRunReport{}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report json: %v output=%s", err, stdout.String())
	}

	if report.GatePassed {
		t.Fatalf("expected gate_passed=false when threshold misses, got %#v", report)
	}
	if len(report.GateFailures) == 0 {
		t.Fatalf("expected at least one gate failure, got %#v", report)
	}

	combo := report.Combinations[0]
	if combo.Gate.Passed {
		t.Fatalf("expected combination gate failure, got %#v", combo.Gate)
	}
	if len(combo.Gate.FailedChecks) == 0 {
		t.Fatalf("expected combination gate failure checks, got %#v", combo.Gate)
	}
	if combo.Gate.FailedChecks[0].Metric != "mean_recall_at_k" {
		t.Fatalf("expected first failed metric mean_recall_at_k, got %#v", combo.Gate.FailedChecks)
	}
	if !strings.Contains(stderr.String(), "eval gate failed") {
		t.Fatalf("expected gate failure summary on stderr, got %s", stderr.String())
	}
}

func TestRunWithArgsThresholdGatePassesWithFlags(t *testing.T) {
	fixturesDir := writeEvalFixtures(t, fixtureCorpus{
		Dataset: "eval-fixtures-test",
		Documents: []fixtureDocument{
			{ID: "doc-a", Path: "fixtures/a.go", Language: "go", Text: "json decode"},
			{ID: "doc-b", Path: "fixtures/b.go", Language: "go", Text: "http timeout"},
			{ID: "doc-c", Path: "fixtures/c.go", Language: "go", Text: "context cancel"},
		},
	}, fixtureQueries{
		Dataset: "eval-fixtures-test",
		Queries: []fixtureRow{
			{ID: "q-1", Query: "decode json", TopK: 2},
			{ID: "q-2", Query: "cancel context", TopK: 2},
		},
	}, fixtureRelevance{
		Dataset: "eval-fixtures-test",
		Judgments: []fixtureJudgment{
			{QueryID: "q-1", DocID: "doc-a", Relevance: 3},
			{QueryID: "q-1", DocID: "doc-b", Relevance: 1},
			{QueryID: "q-2", DocID: "doc-c", Relevance: 3},
		},
	})

	backend := &scriptedVectorBackend{queryPlans: [][]string{
		{"doc-b", "doc-c"},
		{"doc-a", "doc-c"},
	}}

	restore := overrideEvalRunnerHooks(
		func() (config.Config, error) {
			return config.Config{
				EmbeddingProvider:    "ollama",
				VectorBackend:        "sqlite",
				EmbeddingModel:       "eval-test-model",
				VectorQueryTimeoutMS: 500,
			}, nil
		},
		func(config.Config, string) (indexing.VectorBackend, error) {
			return backend, nil
		},
		func(config.Config, string) (indexing.Embedder, error) {
			return staticEmbedder{}, nil
		},
		func(indexing.VectorBackend) error { return nil },
		func() time.Time { return time.Date(2026, time.April, 28, 10, 0, 0, 0, time.UTC) },
	)
	defer restore()

	args := []string{
		"--fixtures-dir", fixturesDir,
		"--min-mean-recall-at-k", "0.70",
		"--min-mean-mrr-at-k", "0.70",
		"--max-p50-latency-ms", "5000",
		"--max-p95-latency-ms", "5000",
		"--skip-markdown-report",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected success exit code 0, got %d stderr=%s", code, stderr.String())
	}

	report := evalRunReport{}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report json: %v output=%s", err, stdout.String())
	}
	if !report.GatePassed {
		t.Fatalf("expected gate_passed=true, got %#v", report)
	}
	if report.Thresholds.MinMeanRecallAtK == nil || *report.Thresholds.MinMeanRecallAtK != 0.7 {
		t.Fatalf("expected min_mean_recall_at_k threshold=0.7, got %#v", report.Thresholds)
	}
	if report.Thresholds.MinMeanMRRAtK == nil || *report.Thresholds.MinMeanMRRAtK != 0.7 {
		t.Fatalf("expected min_mean_mrr_at_k threshold=0.7, got %#v", report.Thresholds)
	}
	if report.Thresholds.MaxP50LatencyMS == nil || *report.Thresholds.MaxP50LatencyMS != 5000 {
		t.Fatalf("expected max_p50_latency_ms threshold=5000, got %#v", report.Thresholds)
	}
	if report.Thresholds.MaxP95LatencyMS == nil || *report.Thresholds.MaxP95LatencyMS != 5000 {
		t.Fatalf("expected max_p95_latency_ms threshold=5000, got %#v", report.Thresholds)
	}

	combo := report.Combinations[0]
	if !combo.Gate.Passed || len(combo.Gate.FailedChecks) != 0 {
		t.Fatalf("expected combination gate pass, got %#v", combo.Gate)
	}
}

func TestRunWithArgsWritesMarkdownReportWithFrontMatter(t *testing.T) {
	fixturesDir := writeEvalFixtures(t, fixtureCorpus{
		Dataset: "eval-fixtures-test",
		Documents: []fixtureDocument{
			{ID: "doc-a", Path: "fixtures/a.go", Language: "go", Text: "json decode"},
			{ID: "doc-b", Path: "fixtures/b.go", Language: "go", Text: "http timeout"},
		},
	}, fixtureQueries{
		Dataset: "eval-fixtures-test",
		Queries: []fixtureRow{
			{ID: "q-1", Query: "decode json", TopK: 2},
		},
	}, fixtureRelevance{
		Dataset: "eval-fixtures-test",
		Judgments: []fixtureJudgment{
			{QueryID: "q-1", DocID: "doc-a", Relevance: 3},
		},
	})

	backend := &scriptedVectorBackend{queryPlans: [][]string{
		{"doc-a", "doc-b"},
	}}

	restore := overrideEvalRunnerHooks(
		func() (config.Config, error) {
			return config.Config{
				EmbeddingProvider:    "ollama",
				VectorBackend:        "sqlite",
				EmbeddingModel:       "eval-test-model",
				VectorQueryTimeoutMS: 500,
			}, nil
		},
		func(config.Config, string) (indexing.VectorBackend, error) {
			return backend, nil
		},
		func(config.Config, string) (indexing.Embedder, error) {
			return staticEmbedder{}, nil
		},
		func(indexing.VectorBackend) error { return nil },
		func() time.Time { return time.Date(2026, time.April, 28, 10, 0, 0, 0, time.UTC) },
	)
	defer restore()

	reportDir := filepath.Join(t.TempDir(), "docs", "evals", "runs")
	args := []string{
		"--fixtures-dir", fixturesDir,
		"--markdown-report-dir", reportDir,
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected success exit code 0, got %d stderr=%s", code, stderr.String())
	}

	entries, err := os.ReadDir(reportDir)
	if err != nil {
		t.Fatalf("read markdown report directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one markdown report file, got %d", len(entries))
	}
	if got := entries[0].Name(); got != "20260428-100000z-eval-fixtures-test.md" {
		t.Fatalf("unexpected markdown report filename %q", got)
	}

	reportPath := filepath.Join(reportDir, entries[0].Name())
	contentBytes, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read markdown report file: %v", err)
	}
	content := string(contentBytes)

	for _, expected := range []string{
		"type: report",
		"title: Eval Run eval-fixtures-test 2026-04-28T10:00:00Z",
		"created: 2026-04-28",
		"- provider-ollama",
		"- backend-sqlite",
		"- model-eval-test-model",
		"- '[[Eval-Index]]'",
		"- '[[Eval-Dataset-eval-fixtures-test]]'",
		"| ollama | sqlite | eval-test-model |",
		"## Gate Failures",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("expected markdown report to include %q\nfull report:\n%s", expected, content)
		}
	}

	indexPath := filepath.Join(filepath.Dir(reportDir), evalIndexFileName)
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read eval index file: %v", err)
	}
	indexContent := string(indexBytes)
	for _, expected := range []string{
		"type: reference",
		"title: Eval Index",
		"created: 2026-04-28",
		"- [[20260428-100000z-eval-fixtures-test]]",
	} {
		if !strings.Contains(indexContent, expected) {
			t.Fatalf("expected eval index to include %q\nfull index:\n%s", expected, indexContent)
		}
	}
}

func TestWriteEvalIndexListsRunsNewestFirstAndDedupes(t *testing.T) {
	baseDir := t.TempDir()
	reportDir := filepath.Join(baseDir, "docs", "evals", "runs")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatalf("create report dir: %v", err)
	}

	firstReportPath := filepath.Join(reportDir, "20260427-093000z-eval-fixtures-test.md")
	if _, err := writeEvalIndex(reportDir, firstReportPath, "2026-04-27T09:30:00Z"); err != nil {
		t.Fatalf("write first eval index: %v", err)
	}

	secondReportPath := filepath.Join(reportDir, "20260428-100000z-eval-fixtures-test.md")
	if _, err := writeEvalIndex(reportDir, secondReportPath, "2026-04-28T10:00:00Z"); err != nil {
		t.Fatalf("write second eval index: %v", err)
	}

	if _, err := writeEvalIndex(reportDir, secondReportPath, "2026-04-28T10:00:00Z"); err != nil {
		t.Fatalf("write duplicate eval index entry: %v", err)
	}

	indexPath := filepath.Join(baseDir, "docs", "evals", evalIndexFileName)
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read eval index file: %v", err)
	}

	lines := strings.Split(string(indexBytes), "\n")
	runLines := make([]string, 0, 2)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [[20") {
			runLines = append(runLines, trimmed)
		}
	}

	expectedRunLines := []string{
		"- [[20260428-100000z-eval-fixtures-test]]",
		"- [[20260427-093000z-eval-fixtures-test]]",
	}
	if len(runLines) != len(expectedRunLines) {
		t.Fatalf("expected %d run links, got %d\nfull index:\n%s", len(expectedRunLines), len(runLines), string(indexBytes))
	}
	for i := range expectedRunLines {
		if runLines[i] != expectedRunLines[i] {
			t.Fatalf("unexpected run link order at index %d: got %q want %q\nfull index:\n%s", i, runLines[i], expectedRunLines[i], string(indexBytes))
		}
	}
}

func TestRunWithArgsRejectsInvalidThreshold(t *testing.T) {
	fixturesDir := writeEvalFixtures(t, fixtureCorpus{
		Dataset:   "eval-fixtures-test",
		Documents: []fixtureDocument{{ID: "doc-a", Path: "fixtures/a.go", Language: "go", Text: "json decode"}},
	}, fixtureQueries{
		Dataset: "eval-fixtures-test",
		Queries: []fixtureRow{{ID: "q-1", Query: "decode json", TopK: 1}},
	}, fixtureRelevance{
		Dataset:   "eval-fixtures-test",
		Judgments: []fixtureJudgment{{QueryID: "q-1", DocID: "doc-a", Relevance: 3}},
	})

	restore := overrideEvalRunnerHooks(
		func() (config.Config, error) {
			return config.Config{EmbeddingProvider: "ollama", VectorBackend: "sqlite"}, nil
		},
		createBackendFn,
		createEmbedderFn,
		closeBackendFn,
		nowUTCFn,
	)
	defer restore()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(
		[]string{
			"--fixtures-dir", fixturesDir,
			"--min-mean-recall-at-k", "1.2",
			"--skip-markdown-report",
		},
		&stdout,
		&stderr,
	)
	if code != 2 {
		t.Fatalf("expected invalid-input exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "resolve eval thresholds") {
		t.Fatalf("expected threshold resolution error prefix, stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "must be within [0,1]") {
		t.Fatalf("expected threshold validation error, stderr=%s", stderr.String())
	}
}

func TestRunWithArgsIntegrationThresholdGateFailsOnLatency(t *testing.T) {
	fixturesDir := writeEvalFixtures(t, fixtureCorpus{
		Dataset: "eval-integration-latency",
		Documents: []fixtureDocument{
			{ID: "doc-a", Path: "fixtures/a.go", Language: "go", Text: "json decode"},
			{ID: "doc-b", Path: "fixtures/b.go", Language: "go", Text: "context cancel"},
		},
	}, fixtureQueries{
		Dataset: "eval-integration-latency",
		Queries: []fixtureRow{
			{ID: "q-1", Query: "decode json", TopK: 2},
			{ID: "q-2", Query: "cancel context", TopK: 2},
		},
	}, fixtureRelevance{
		Dataset: "eval-integration-latency",
		Judgments: []fixtureJudgment{
			{QueryID: "q-1", DocID: "doc-a", Relevance: 3},
			{QueryID: "q-2", DocID: "doc-b", Relevance: 3},
		},
	})

	ollamaServer := newOllamaEvalStubServer(t, 20*time.Millisecond)
	defer ollamaServer.Close()

	setEvalRunnerIntegrationEnv(t, ollamaServer.URL, t.TempDir())
	restore := overrideEvalRunnerHooks(
		config.Load,
		newVectorBackend,
		newEmbedder,
		closeVectorBackend,
		func() time.Time { return time.Now().UTC() },
	)
	defer restore()

	args := []string{
		"--fixtures-dir", fixturesDir,
		"--max-p50-latency-ms", "1",
		"--max-p95-latency-ms", "1",
		"--skip-markdown-report",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithArgs(args, &stdout, &stderr)
	if code != evalGateFailureExitCode {
		t.Fatalf("expected gate failure exit code %d, got %d stderr=%s", evalGateFailureExitCode, code, stderr.String())
	}

	report := evalRunReport{}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report json: %v output=%s", err, stdout.String())
	}
	if report.GatePassed {
		t.Fatalf("expected gate_passed=false when latency thresholds are missed, got %#v", report)
	}
	if len(report.GateFailures) == 0 {
		t.Fatalf("expected latency gate failures, got %#v", report)
	}

	failedMetrics := map[string]struct{}{}
	for _, failure := range report.GateFailures {
		failedMetrics[failure.Check.Metric] = struct{}{}
	}
	if _, ok := failedMetrics["latency_metrics.p50_ms"]; !ok {
		t.Fatalf("expected p50 latency failure, got %#v", report.GateFailures)
	}
	if _, ok := failedMetrics["latency_metrics.p95_ms"]; !ok {
		t.Fatalf("expected p95 latency failure, got %#v", report.GateFailures)
	}

	if len(report.Combinations) != 1 {
		t.Fatalf("expected one combination report, got %#v", report.Combinations)
	}
	if report.Combinations[0].Gate.Passed {
		t.Fatalf("expected combination gate failure, got %#v", report.Combinations[0].Gate)
	}
	if !strings.Contains(stderr.String(), "eval gate failed") {
		t.Fatalf("expected gate failure summary on stderr, got %s", stderr.String())
	}
}

func TestRunWithArgsIntegrationReportOutputDeterminism(t *testing.T) {
	fixturesDir := writeEvalFixtures(t, fixtureCorpus{
		Dataset: "eval-report-determinism",
		Documents: []fixtureDocument{
			{ID: "doc-a", Path: "fixtures/a.go", Language: "go", Text: "json decode"},
			{ID: "doc-b", Path: "fixtures/b.go", Language: "go", Text: "context cancel"},
		},
	}, fixtureQueries{
		Dataset: "eval-report-determinism",
		Queries: []fixtureRow{
			{ID: "q-1", Query: "decode json", TopK: 2},
			{ID: "q-2", Query: "cancel context", TopK: 2},
		},
	}, fixtureRelevance{
		Dataset: "eval-report-determinism",
		Judgments: []fixtureJudgment{
			{QueryID: "q-1", DocID: "doc-a", Relevance: 3},
			{QueryID: "q-2", DocID: "doc-b", Relevance: 3},
		},
	})

	ollamaServer := newOllamaEvalStubServer(t, 0)
	defer ollamaServer.Close()

	setEvalRunnerIntegrationEnv(t, ollamaServer.URL, t.TempDir())
	fixedNow := time.Date(2026, time.April, 28, 11, 22, 33, 0, time.UTC)
	restore := overrideEvalRunnerHooks(
		config.Load,
		newVectorBackend,
		newEmbedder,
		closeVectorBackend,
		func() time.Time { return fixedNow },
	)
	defer restore()

	reportDir := filepath.Join(t.TempDir(), "docs", "evals", "runs")
	outPath := filepath.Join(t.TempDir(), "outputs", "eval-report.json")
	args := []string{
		"--fixtures-dir", fixturesDir,
		"--markdown-report-dir", reportDir,
		"--out", outPath,
	}

	var stdoutRun1 bytes.Buffer
	var stderrRun1 bytes.Buffer
	codeRun1 := runWithArgs(args, &stdoutRun1, &stderrRun1)
	if codeRun1 != 0 {
		t.Fatalf("expected first eval run success, got code=%d stderr=%s", codeRun1, stderrRun1.String())
	}

	reportRun1 := evalRunReport{}
	if err := json.Unmarshal(stdoutRun1.Bytes(), &reportRun1); err != nil {
		t.Fatalf("decode first report json: %v output=%s", err, stdoutRun1.String())
	}

	reportFileName := "20260428-112233z-eval-report-determinism.md"
	reportPath := filepath.Join(reportDir, reportFileName)
	if _, err := os.Stat(reportPath); err != nil {
		t.Fatalf("expected markdown report %q after first run: %v", reportPath, err)
	}

	indexPath := filepath.Join(filepath.Dir(reportDir), evalIndexFileName)
	indexRun1, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read eval index after first run: %v", err)
	}

	var stdoutRun2 bytes.Buffer
	var stderrRun2 bytes.Buffer
	codeRun2 := runWithArgs(args, &stdoutRun2, &stderrRun2)
	if codeRun2 != 0 {
		t.Fatalf("expected second eval run success, got code=%d stderr=%s", codeRun2, stderrRun2.String())
	}

	reportRun2 := evalRunReport{}
	if err := json.Unmarshal(stdoutRun2.Bytes(), &reportRun2); err != nil {
		t.Fatalf("decode second report json: %v output=%s", err, stdoutRun2.String())
	}

	normalizedRun1 := normalizeEvalReportForDeterministicComparison(reportRun1)
	normalizedRun2 := normalizeEvalReportForDeterministicComparison(reportRun2)
	if !reflect.DeepEqual(normalizedRun1, normalizedRun2) {
		t.Fatalf(
			"expected deterministic report content across repeated runs\nrun1=%#v\nrun2=%#v",
			normalizedRun1,
			normalizedRun2,
		)
	}

	entries, err := os.ReadDir(reportDir)
	if err != nil {
		t.Fatalf("read markdown report directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one deterministic markdown report file after repeated runs, got %d", len(entries))
	}
	if got := entries[0].Name(); got != reportFileName {
		t.Fatalf("unexpected markdown report filename %q", got)
	}

	indexRun2, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read eval index after second run: %v", err)
	}
	if string(indexRun1) != string(indexRun2) {
		t.Fatalf("expected stable eval index content across repeated runs\nrun1:\n%s\nrun2:\n%s", string(indexRun1), string(indexRun2))
	}

	runLink := "[[20260428-112233z-eval-report-determinism]]"
	if strings.Count(string(indexRun2), runLink) != 1 {
		t.Fatalf("expected eval index to contain deterministic run link exactly once, index:\n%s", string(indexRun2))
	}
}

func writeEvalFixtures(
	t *testing.T,
	corpus fixtureCorpus,
	queries fixtureQueries,
	relevance fixtureRelevance,
) string {
	t.Helper()

	dir := t.TempDir()
	writeJSONFile(t, filepath.Join(dir, "corpus.json"), corpus)
	writeJSONFile(t, filepath.Join(dir, "queries.json"), queries)
	writeJSONFile(t, filepath.Join(dir, "relevance.json"), relevance)
	return dir
}

func setEvalRunnerIntegrationEnv(t *testing.T, ollamaBaseURL string, storagePath string) {
	t.Helper()

	t.Setenv("CODE_INDEX_PATH", storagePath)
	t.Setenv("VECTOR_BACKEND", "sqlite")
	t.Setenv("VECTOR_TOP_K", "8")
	t.Setenv("VECTOR_QUERY_TIMEOUT_MS", "5000")
	t.Setenv("VECTOR_LEXICAL_WEIGHT", "0.5")
	t.Setenv("VECTOR_SEMANTIC_WEIGHT", "0.5")
	t.Setenv("EMBEDDING_PROVIDER", "ollama")
	t.Setenv("EMBEDDING_MODEL", "stub-embed")
	t.Setenv("OLLAMA_BASE_URL", strings.TrimSpace(ollamaBaseURL))
	t.Setenv("VLLM_BASE_URL", "")
	t.Setenv("VLLM_MODEL", "")
	t.Setenv("VLLM_API_KEY", "")
	t.Setenv("QDRANT_URL", "")
	t.Setenv("QDRANT_COLLECTION", "")
	t.Setenv("QDRANT_API_KEY", "")

	for _, key := range []string{
		envEvalMinMeanRecallAtKPrimary,
		envEvalMinMeanRecallAtKCompat,
		envEvalMinMeanMRRAtKPrimary,
		envEvalMinMeanMRRAtKCompat,
		envEvalMaxP50LatencyMSPrimary,
		envEvalMaxP50LatencyMSCompat,
		envEvalMaxP95LatencyMSPrimary,
		envEvalMaxP95LatencyMSCompat,
	} {
		t.Setenv(key, "")
	}
}

func newOllamaEvalStubServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		if delay > 0 {
			time.Sleep(delay)
		}

		var payload struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		embeddings := make([][]float32, len(payload.Input))
		for index, input := range payload.Input {
			embeddings[index] = evalFixtureEmbedding(input)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":      strings.TrimSpace(payload.Model),
			"embeddings": embeddings,
		})
	}))
}

func evalFixtureEmbedding(text string) []float32 {
	normalized := strings.ToLower(strings.TrimSpace(text))
	return []float32{
		float32(1 + strings.Count(normalized, "json")),
		float32(1 + strings.Count(normalized, "decode")),
		float32(1 + strings.Count(normalized, "context")),
		float32(1 + strings.Count(normalized, "cancel")),
	}
}

func normalizeEvalReportForDeterministicComparison(report evalRunReport) evalRunReport {
	normalized := report
	normalized.GeneratedAtUTC = ""

	combinations := make([]evalCombinationReport, 0, len(report.Combinations))
	for _, combo := range report.Combinations {
		comboCopy := combo
		comboCopy.Aggregate.LatencyMetrics.P50MS = 0
		comboCopy.Aggregate.LatencyMetrics.P95MS = 0

		perQuery := make([]evalPerQueryReport, 0, len(combo.PerQuery))
		for _, row := range combo.PerQuery {
			rowCopy := row
			rowCopy.LatencyMS = 0
			perQuery = append(perQuery, rowCopy)
		}
		comboCopy.PerQuery = perQuery
		combinations = append(combinations, comboCopy)
	}
	normalized.Combinations = combinations

	for index := range normalized.GateFailures {
		if strings.HasPrefix(normalized.GateFailures[index].Check.Metric, "latency_metrics.") {
			normalized.GateFailures[index].Check.Actual = 0
		}
	}
	return normalized
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()

	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

func overrideEvalRunnerHooks(
	load func() (config.Config, error),
	backendFactory func(config.Config, string) (indexing.VectorBackend, error),
	embedderFactory func(config.Config, string) (indexing.Embedder, error),
	closer func(indexing.VectorBackend) error,
	nowFn func() time.Time,
) func() {
	previousLoad := loadConfigFn
	previousBackendFactory := createBackendFn
	previousEmbedderFactory := createEmbedderFn
	previousCloser := closeBackendFn
	previousNow := nowUTCFn

	loadConfigFn = load
	createBackendFn = backendFactory
	createEmbedderFn = embedderFactory
	closeBackendFn = closer
	nowUTCFn = nowFn

	return func() {
		loadConfigFn = previousLoad
		createBackendFn = previousBackendFactory
		createEmbedderFn = previousEmbedderFactory
		closeBackendFn = previousCloser
		nowUTCFn = previousNow
	}
}

type staticEmbedder struct{}

func (staticEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	embeddings := make([][]float32, 0, len(inputs))
	for range inputs {
		embeddings = append(embeddings, []float32{1, 0, 0})
	}
	return embeddings, nil
}

type scriptedVectorBackend struct {
	queryPlans [][]string

	recordsByNamespace map[string]map[string]indexing.VectorRecord
	queryIndex         int
}

func (b *scriptedVectorBackend) Upsert(
	_ context.Context,
	request indexing.VectorUpsertRequest,
) (indexing.VectorUpsertResponse, error) {
	if b.recordsByNamespace == nil {
		b.recordsByNamespace = map[string]map[string]indexing.VectorRecord{}
	}
	if _, exists := b.recordsByNamespace[request.Namespace]; !exists {
		b.recordsByNamespace[request.Namespace] = map[string]indexing.VectorRecord{}
	}
	for _, record := range request.Records {
		b.recordsByNamespace[request.Namespace][record.ID] = record
	}
	return indexing.VectorUpsertResponse{Upserted: len(request.Records)}, nil
}

func (b *scriptedVectorBackend) Query(
	_ context.Context,
	request indexing.VectorQueryRequest,
) (indexing.VectorQueryResponse, error) {
	namespaceRecords := b.recordsByNamespace[request.Namespace]
	if len(namespaceRecords) == 0 {
		return indexing.VectorQueryResponse{Matches: nil}, nil
	}

	plan := []string{}
	if b.queryIndex < len(b.queryPlans) {
		plan = b.queryPlans[b.queryIndex]
	}
	b.queryIndex++

	if len(plan) == 0 {
		for _, record := range namespaceRecords {
			plan = append(plan, record.Metadata.ChunkID)
		}
	}

	matches := make([]indexing.VectorQueryMatch, 0, min(request.TopK, len(plan)))
	for idx, docID := range plan {
		for _, record := range namespaceRecords {
			if record.Metadata.ChunkID != docID {
				continue
			}
			matches = append(matches, indexing.VectorQueryMatch{
				Record: record,
				Score:  float64(len(plan) - idx),
			})
			break
		}
		if len(matches) >= request.TopK {
			break
		}
	}

	return indexing.VectorQueryResponse{Matches: matches}, nil
}

func (b *scriptedVectorBackend) Delete(
	_ context.Context,
	request indexing.VectorDeleteRequest,
) (indexing.VectorDeleteResponse, error) {
	deleted := 0
	namespaceRecords := b.recordsByNamespace[request.Namespace]
	for _, id := range request.IDs {
		if _, exists := namespaceRecords[id]; exists {
			delete(namespaceRecords, id)
			deleted++
		}
	}
	return indexing.VectorDeleteResponse{Deleted: deleted}, nil
}

func (b *scriptedVectorBackend) DeleteNamespace(
	_ context.Context,
	request indexing.VectorDeleteNamespaceRequest,
) (indexing.VectorDeleteNamespaceResponse, error) {
	deleted := 0
	if len(b.recordsByNamespace[request.Namespace]) > 0 {
		deleted = len(b.recordsByNamespace[request.Namespace])
	}
	delete(b.recordsByNamespace, request.Namespace)
	return indexing.VectorDeleteNamespaceResponse{Deleted: deleted}, nil
}

func (b *scriptedVectorBackend) Health(context.Context) (indexing.VectorHealthResponse, error) {
	return indexing.VectorHealthResponse{Ready: true}, nil
}
