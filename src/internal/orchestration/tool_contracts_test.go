package orchestration

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
)

func TestSearchSymbolsLanguageEnumDefaultsToSupportedLanguages(t *testing.T) {
	svc := New(config.Config{Disabled: map[string]struct{}{}}, Dependencies{})
	tools := svc.ListTools()

	if _, ok := toolByName(tools, "search_columns"); !ok {
		t.Fatalf("expected search_columns tool to be present when sql language is enabled by default")
	}

	searchSymbols, ok := toolByName(tools, "search_symbols")
	if !ok {
		t.Fatalf("search_symbols tool missing from registry")
	}

	expected := []string{
		"al",
		"blade",
		"c",
		"cpp",
		"csharp",
		"dart",
		"elixir",
		"go",
		"java",
		"javascript",
		"perl",
		"php",
		"python",
		"razor",
		"ruby",
		"rust",
		"sql",
		"swift",
		"typescript",
		"vue",
		"xml",
	}
	got := enumStringsForProperty(t, searchSymbols, "language")
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("unexpected search_symbols language enum: got=%#v expected=%#v", got, expected)
	}
}

func TestConfiguredLanguagesFilterEnumAndDisableSearchColumns(t *testing.T) {
	cfg := config.Config{
		Languages: []string{"Python", "go", "unknown-language", "go"},
		Disabled:  map[string]struct{}{},
	}
	svc := New(cfg, Dependencies{})
	tools := svc.ListTools()

	if _, ok := toolByName(tools, "search_columns"); ok {
		t.Fatalf("expected search_columns tool to be disabled when sql language is not enabled")
	}

	searchSymbols, ok := toolByName(tools, "search_symbols")
	if !ok {
		t.Fatalf("search_symbols tool missing from registry")
	}

	gotEnum := enumStringsForProperty(t, searchSymbols, "language")
	expectedEnum := []string{"python", "go"}
	if !reflect.DeepEqual(gotEnum, expectedEnum) {
		t.Fatalf("unexpected configured language enum: got=%#v expected=%#v", gotEnum, expectedEnum)
	}

	invalid := svc.CallTool(context.Background(), "search_symbols", map[string]any{
		"repo":     "any",
		"query":    "symbol",
		"language": "sql",
	})
	errorText, _ := invalid["error"].(string)
	if !strings.Contains(errorText, "Input validation error") || !strings.Contains(errorText, `"language" must be one of`) {
		t.Fatalf("expected input validation error for out-of-enum language, got %#v", invalid)
	}
}

func TestSearchTextContractExposesRetrievalModesAndHybridWeightOverrides(t *testing.T) {
	svc := New(config.Config{Disabled: map[string]struct{}{}}, Dependencies{})
	tools := svc.ListTools()

	searchText, ok := toolByName(tools, "search_text")
	if !ok {
		t.Fatalf("search_text tool missing from registry")
	}

	retrievalModeSchema := propertySchemaFor(t, searchText, "retrieval_mode")
	expectedModes := []string{"lexical", "semantic", "hybrid"}
	if gotModes := enumStringsForProperty(t, searchText, "retrieval_mode"); !reflect.DeepEqual(gotModes, expectedModes) {
		t.Fatalf("unexpected retrieval_mode enum: got=%#v expected=%#v", gotModes, expectedModes)
	}
	if gotDefault, ok := retrievalModeSchema["default"].(string); !ok || gotDefault != "lexical" {
		t.Fatalf("expected retrieval_mode default lexical, got %#v", retrievalModeSchema["default"])
	}

	lexicalWeightSchema := propertySchemaFor(t, searchText, "lexical_weight")
	if gotType, _ := lexicalWeightSchema["type"].(string); gotType != "number" {
		t.Fatalf("expected lexical_weight type number, got %#v", lexicalWeightSchema)
	}

	semanticWeightSchema := propertySchemaFor(t, searchText, "semantic_weight")
	if gotType, _ := semanticWeightSchema["type"].(string); gotType != "number" {
		t.Fatalf("expected semantic_weight type number, got %#v", semanticWeightSchema)
	}
}

func TestSearchTextContractRequiredFieldsRemainStable(t *testing.T) {
	svc := New(config.Config{Disabled: map[string]struct{}{}}, Dependencies{})
	tools := svc.ListTools()

	searchText, ok := toolByName(tools, "search_text")
	if !ok {
		t.Fatalf("search_text tool missing from registry")
	}

	required := schemaStrings(searchText.InputSchema, "required")
	expected := []string{"repo", "query"}
	if !reflect.DeepEqual(required, expected) {
		t.Fatalf("unexpected required fields for search_text: got=%#v expected=%#v", required, expected)
	}
}

func toolByName(tools []Tool, name string) (Tool, bool) {
	for _, tool := range tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

func enumStringsForProperty(t *testing.T, tool Tool, property string) []string {
	t.Helper()

	propertiesRaw, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("tool %s missing properties schema", tool.Name)
	}
	propertyRaw, ok := propertiesRaw[property].(map[string]any)
	if !ok {
		t.Fatalf("tool %s missing %s property schema", tool.Name, property)
	}
	enumRaw, ok := propertyRaw["enum"].([]any)
	if !ok {
		t.Fatalf("tool %s property %s missing enum", tool.Name, property)
	}

	out := make([]string, 0, len(enumRaw))
	for _, value := range enumRaw {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("tool %s property %s enum contains non-string value %#v", tool.Name, property, value)
		}
		out = append(out, text)
	}
	return out
}

func propertySchemaFor(t *testing.T, tool Tool, property string) map[string]any {
	t.Helper()

	propertiesRaw, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("tool %s missing properties schema", tool.Name)
	}
	propertyRaw, ok := propertiesRaw[property].(map[string]any)
	if !ok {
		t.Fatalf("tool %s missing %s property schema", tool.Name, property)
	}
	return propertyRaw
}
