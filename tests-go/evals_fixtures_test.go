package testsgo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type evalCorpusFixture struct {
	Dataset   string `json:"dataset"`
	Documents []struct {
		ID       string `json:"id"`
		Path     string `json:"path"`
		Language string `json:"language"`
		Text     string `json:"text"`
	} `json:"documents"`
}

type evalQueryFixture struct {
	Dataset string `json:"dataset"`
	Queries []struct {
		ID    string `json:"id"`
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	} `json:"queries"`
}

type evalRelevanceFixture struct {
	Dataset   string `json:"dataset"`
	Judgments []struct {
		QueryID   string `json:"query_id"`
		DocID     string `json:"doc_id"`
		Relevance int    `json:"relevance"`
	} `json:"judgments"`
}

func TestEvalFixturesAreDeterministicAndInternallyConsistent(t *testing.T) {
	t.Parallel()

	var corpus evalCorpusFixture
	readFixtureJSON(t, "corpus.json", &corpus)
	if corpus.Dataset == "" {
		t.Fatal("corpus dataset must be non-empty")
	}
	if got := len(corpus.Documents); got != 12 {
		t.Fatalf("expected 12 fixture documents, got %d", got)
	}

	docIDs := make(map[string]struct{}, len(corpus.Documents))
	for _, doc := range corpus.Documents {
		if doc.ID == "" || doc.Path == "" || doc.Language == "" || doc.Text == "" {
			t.Fatalf("fixture document fields must be non-empty: %#v", doc)
		}
		if _, exists := docIDs[doc.ID]; exists {
			t.Fatalf("duplicate fixture document id %q", doc.ID)
		}
		docIDs[doc.ID] = struct{}{}
	}

	var queries evalQueryFixture
	readFixtureJSON(t, "queries.json", &queries)
	if queries.Dataset != corpus.Dataset {
		t.Fatalf("query dataset mismatch: got %q want %q", queries.Dataset, corpus.Dataset)
	}
	if got := len(queries.Queries); got != 12 {
		t.Fatalf("expected 12 fixture queries, got %d", got)
	}

	queryIDs := make(map[string]struct{}, len(queries.Queries))
	for _, query := range queries.Queries {
		if query.ID == "" || query.Query == "" {
			t.Fatalf("fixture query fields must be non-empty: %#v", query)
		}
		if query.TopK <= 0 {
			t.Fatalf("fixture query top_k must be positive: %#v", query)
		}
		if _, exists := queryIDs[query.ID]; exists {
			t.Fatalf("duplicate fixture query id %q", query.ID)
		}
		queryIDs[query.ID] = struct{}{}
	}

	var relevance evalRelevanceFixture
	readFixtureJSON(t, "relevance.json", &relevance)
	if relevance.Dataset != corpus.Dataset {
		t.Fatalf("relevance dataset mismatch: got %q want %q", relevance.Dataset, corpus.Dataset)
	}
	if got := len(relevance.Judgments); got != 23 {
		t.Fatalf("expected 23 relevance judgments, got %d", got)
	}

	queryJudgmentCounts := make(map[string]int, len(queryIDs))
	for _, judgment := range relevance.Judgments {
		if _, exists := queryIDs[judgment.QueryID]; !exists {
			t.Fatalf("relevance references unknown query id %q", judgment.QueryID)
		}
		if _, exists := docIDs[judgment.DocID]; !exists {
			t.Fatalf("relevance references unknown doc id %q", judgment.DocID)
		}
		if judgment.Relevance < 1 || judgment.Relevance > 3 {
			t.Fatalf("relevance must be in [1,3], got %d for query %q doc %q", judgment.Relevance, judgment.QueryID, judgment.DocID)
		}
		queryJudgmentCounts[judgment.QueryID]++
	}

	for queryID := range queryIDs {
		if queryJudgmentCounts[queryID] == 0 {
			t.Fatalf("query %q has no relevance judgments", queryID)
		}
	}
}

func readFixtureJSON(t *testing.T, fileName string, out any) {
	t.Helper()

	path := filepath.Join("evals", "fixtures", fileName)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %q: %v", path, err)
	}
	if err := json.Unmarshal(content, out); err != nil {
		t.Fatalf("decode fixture %q: %v", path, err)
	}
}
