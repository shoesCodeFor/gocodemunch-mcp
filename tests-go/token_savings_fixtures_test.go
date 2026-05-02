package testsgo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type tokenSavingsPromptSuiteFixture struct {
	Dataset      string `json:"dataset"`
	SuiteVersion string `json:"suite_version"`
	Cases        []struct {
		ID           string         `json:"id"`
		Prompt       string         `json:"prompt"`
		Modes        []string       `json:"modes"`
		Tool         string         `json:"tool"`
		Arguments    map[string]any `json:"arguments"`
		ContextFiles []string       `json:"context_files"`
	} `json:"cases"`
}

func TestTokenSavingsFixturesAreDeterministicAndInternallyConsistent(t *testing.T) {
	t.Parallel()

	var corpus evalCorpusFixture
	readTokenSavingsFixtureJSON(t, "corpus.json", &corpus)
	if corpus.Dataset == "" {
		t.Fatal("token savings corpus dataset must be non-empty")
	}
	if got := len(corpus.Documents); got != 4 {
		t.Fatalf("expected 4 token savings fixture documents, got %d", got)
	}

	docPaths := make(map[string]struct{}, len(corpus.Documents))
	for _, doc := range corpus.Documents {
		if doc.ID == "" || doc.Path == "" || doc.Language == "" || doc.Text == "" {
			t.Fatalf("token savings document fields must be non-empty: %#v", doc)
		}
		if _, exists := docPaths[doc.Path]; exists {
			t.Fatalf("duplicate token savings document path %q", doc.Path)
		}
		docPaths[doc.Path] = struct{}{}
	}

	var suite tokenSavingsPromptSuiteFixture
	readTokenSavingsFixtureJSON(t, "prompt_suite.json", &suite)
	if suite.Dataset != corpus.Dataset {
		t.Fatalf("token savings suite dataset mismatch: got %q want %q", suite.Dataset, corpus.Dataset)
	}
	if suite.SuiteVersion == "" {
		t.Fatal("token savings suite version must be non-empty")
	}
	if got := len(suite.Cases); got != 5 {
		t.Fatalf("expected 5 token savings cases, got %d", got)
	}

	expectedModes := []string{"with_mcp", "without_mcp"}
	caseIDs := make(map[string]struct{}, len(suite.Cases))
	for _, benchmarkCase := range suite.Cases {
		if benchmarkCase.ID == "" || benchmarkCase.Prompt == "" || benchmarkCase.Tool == "" {
			t.Fatalf("token savings case fields must be non-empty: %#v", benchmarkCase)
		}
		if !reflect.DeepEqual(benchmarkCase.Modes, expectedModes) {
			t.Fatalf("token savings case %q must declare deterministic modes %v, got %#v", benchmarkCase.ID, expectedModes, benchmarkCase.Modes)
		}
		if len(benchmarkCase.ContextFiles) == 0 {
			t.Fatalf("token savings case must include context files: %#v", benchmarkCase)
		}
		if _, exists := caseIDs[benchmarkCase.ID]; exists {
			t.Fatalf("duplicate token savings case id %q", benchmarkCase.ID)
		}
		caseIDs[benchmarkCase.ID] = struct{}{}

		for _, path := range benchmarkCase.ContextFiles {
			if _, exists := docPaths[path]; !exists {
				t.Fatalf("token savings case %q references unknown context file %q", benchmarkCase.ID, path)
			}
		}
	}
}

func readTokenSavingsFixtureJSON(t *testing.T, fileName string, out any) {
	t.Helper()

	path := filepath.Join("evals", "fixtures", "token-savings-smoke", fileName)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token savings fixture %q: %v", path, err)
	}
	if err := json.Unmarshal(content, out); err != nil {
		t.Fatalf("decode token savings fixture %q: %v", path, err)
	}
}
