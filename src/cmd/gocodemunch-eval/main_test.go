package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

	args := []string{"--fixtures-dir", fixturesDir}
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
	code := runWithArgs([]string{"--fixtures-dir", fixturesDir, "--providers", "openai"}, &stdout, &stderr)
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
	code := runWithArgs([]string{"--fixtures-dir", fixturesDir}, &stdout, &stderr)
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
	code := runWithArgs([]string{"--fixtures-dir", fixturesDir}, &stdout, &stderr)
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
