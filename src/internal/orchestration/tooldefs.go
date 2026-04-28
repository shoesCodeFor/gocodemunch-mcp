package orchestration

import (
	"context"
	"errors"
	"sort"
	"strings"
)

var errToolNotImplemented = errors.New("tool not implemented")

// DefaultToolDefinitions returns the parity-lane tool registry skeleton for all 27 tools.
func DefaultToolDefinitions() []Tool {
	return ToolDefinitionsForLanguages(nil)
}

// ToolDefinitionsForLanguages returns tool definitions with a language-aware
// search_symbols schema surface.
func ToolDefinitionsForLanguages(configuredLanguages []string) []Tool {
	languageEnum := searchSymbolsLanguageEnum(configuredLanguages)

	stub := func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return nil, errToolNotImplemented
	}

	return []Tool{
		{
			Name:        "index_repo",
			Description: "Index a GitHub repository.",
			InputSchema: objectSchema(map[string]any{
				"url":                   stringProp("GitHub repository URL or owner/repo string"),
				"use_ai_summaries":      boolProp("Use AI summaries", true),
				"extra_ignore_patterns": stringArrayProp("Additional ignore patterns"),
				"incremental":           boolProp("Incremental indexing", true),
			}, "url"),
			Handler: stub,
		},
		{
			Name:        "index_folder",
			Description: "Index a local folder.",
			InputSchema: objectSchema(map[string]any{
				"path":                  stringProp("Local path"),
				"use_ai_summaries":      boolProp("Use AI summaries", true),
				"extra_ignore_patterns": stringArrayProp("Additional ignore patterns"),
				"follow_symlinks":       boolProp("Follow symlinks", false),
				"incremental":           boolProp("Incremental indexing", true),
			}, "path"),
			Handler: stub,
		},
		{
			Name:        "index_file",
			Description: "Index a single file.",
			InputSchema: objectSchema(map[string]any{
				"path":              stringProp("Absolute file path"),
				"use_ai_summaries":  boolProp("Use AI summaries", true),
				"context_providers": boolProp("Run context providers", true),
			}, "path"),
			Handler: stub,
		},
		{
			Name:        "list_repos",
			Description: "List indexed repositories.",
			InputSchema: objectSchema(map[string]any{}),
			Handler:     stub,
		},
		{
			Name:        "resolve_repo",
			Description: "Resolve filesystem path to repo identifier.",
			InputSchema: objectSchema(map[string]any{
				"path": stringProp("Absolute path"),
			}, "path"),
			Handler: stub,
		},
		{
			Name:        "get_file_tree",
			Description: "Get indexed file tree.",
			InputSchema: objectSchema(map[string]any{
				"repo":              stringProp("Repository identifier"),
				"path_prefix":       stringPropWithDefault("Optional path prefix", ""),
				"include_summaries": boolProp("Include file summaries", false),
			}, "repo"),
			Handler: stub,
		},
		{
			Name:        "get_file_outline",
			Description: "Get symbol outline for one file or batch of files.",
			InputSchema: objectSchema(map[string]any{
				"repo":       stringProp("Repository identifier"),
				"file_path":  stringProp("Single file path"),
				"file_paths": stringArrayProp("Batch file paths"),
			}, "repo"),
			Handler: stub,
		},
		{
			Name:        "get_symbol_source",
			Description: "Get source for one or many symbols.",
			InputSchema: objectSchema(map[string]any{
				"repo":          stringProp("Repository identifier"),
				"symbol_id":     stringProp("Single symbol ID"),
				"symbol_ids":    stringArrayProp("Batch symbol IDs"),
				"verify":        boolProp("Verify content hash", false),
				"context_lines": intPropWithDefault("Context lines", 0),
			}, "repo"),
			Handler: stub,
		},
		{
			Name:        "get_file_content",
			Description: "Get file content, optionally sliced by lines.",
			InputSchema: objectSchema(map[string]any{
				"repo":       stringProp("Repository identifier"),
				"file_path":  stringProp("File path"),
				"start_line": intProp("Start line"),
				"end_line":   intProp("End line"),
			}, "repo", "file_path"),
			Handler: stub,
		},
		{
			Name:        "search_symbols",
			Description: "Search symbols in a repository.",
			InputSchema: objectSchema(map[string]any{
				"repo":         stringProp("Repository identifier"),
				"query":        stringProp("Query"),
				"kind":         stringProp("Symbol kind"),
				"file_pattern": stringProp("File filter"),
				"language":     stringEnumProp("Language filter", languageEnum),
				"max_results":  intPropWithDefault("Max results", 10),
				"token_budget": intProp("Token budget"),
				"detail_level": stringPropWithDefault("Detail level", "standard"),
				"debug":        boolProp("Include scoring debug details", false),
			}, "repo", "query"),
			Handler: stub,
		},
		{
			Name:        "invalidate_cache",
			Description: "Delete cached index and content for a repo.",
			InputSchema: objectSchema(map[string]any{
				"repo": stringProp("Repository identifier"),
			}, "repo"),
			Handler: stub,
		},
		{
			Name:        "search_text",
			Description: "Search file contents with optional regex.",
			InputSchema: objectSchema(map[string]any{
				"repo":            stringProp("Repository identifier"),
				"query":           stringProp("Query"),
				"is_regex":        boolProp("Treat query as regex", false),
				"file_pattern":    stringProp("File filter"),
				"max_results":     intPropWithDefault("Max results", 20),
				"context_lines":   intPropWithDefault("Context lines", 0),
				"retrieval_mode":  stringEnumPropWithDefault("Retrieval mode", []string{"lexical", "semantic", "hybrid"}, "lexical"),
				"lexical_weight":  numberProp("Hybrid lexical weight override"),
				"semantic_weight": numberProp("Hybrid semantic weight override"),
			}, "repo", "query"),
			Handler: stub,
		},
		{
			Name:        "get_repo_outline",
			Description: "Get repository outline summary.",
			InputSchema: objectSchema(map[string]any{
				"repo": stringProp("Repository identifier"),
			}, "repo"),
			Handler: stub,
		},
		{
			Name:        "find_importers",
			Description: "Find files that import a file.",
			InputSchema: objectSchema(map[string]any{
				"repo":        stringProp("Repository identifier"),
				"file_path":   stringProp("Single file path"),
				"file_paths":  stringArrayProp("Batch file paths"),
				"max_results": intPropWithDefault("Max results", 50),
			}, "repo"),
			Handler: stub,
		},
		{
			Name:        "find_references",
			Description: "Find import/content references for identifiers.",
			InputSchema: objectSchema(map[string]any{
				"repo":        stringProp("Repository identifier"),
				"identifier":  stringProp("Single identifier"),
				"identifiers": stringArrayProp("Batch identifiers"),
				"max_results": intPropWithDefault("Max results", 50),
			}, "repo"),
			Handler: stub,
		},
		{
			Name:        "check_references",
			Description: "Check whether identifiers are referenced.",
			InputSchema: objectSchema(map[string]any{
				"repo":                stringProp("Repository identifier"),
				"identifier":          stringProp("Single identifier"),
				"identifiers":         stringArrayProp("Batch identifiers"),
				"search_content":      boolProp("Include content search", true),
				"max_content_results": intPropWithDefault("Max content matches", 20),
			}, "repo"),
			Handler: stub,
		},
		{
			Name:        "search_columns",
			Description: "Search indexed column metadata.",
			InputSchema: objectSchema(map[string]any{
				"repo":          stringProp("Repository identifier"),
				"query":         stringProp("Query"),
				"model_pattern": stringProp("Model filter"),
				"max_results":   intPropWithDefault("Max results", 20),
			}, "repo", "query"),
			Handler: stub,
		},
		{
			Name:        "get_context_bundle",
			Description: "Get context bundle for one or many symbols.",
			InputSchema: objectSchema(map[string]any{
				"repo":            stringProp("Repository identifier"),
				"symbol_id":       stringProp("Single symbol ID"),
				"symbol_ids":      stringArrayProp("Batch symbol IDs"),
				"include_callers": boolProp("Include importer files", false),
				"output_format":   stringPropWithDefault("Output format", "json"),
			}, "repo"),
			Handler: stub,
		},
		{
			Name:        "get_session_stats",
			Description: "Get token savings stats for current session.",
			InputSchema: objectSchema(map[string]any{}),
			Handler:     stub,
		},
		{
			Name:        "get_dependency_graph",
			Description: "Get file dependency graph.",
			InputSchema: objectSchema(map[string]any{
				"repo":      stringProp("Repository identifier"),
				"file":      stringProp("File path"),
				"direction": stringPropWithDefault("Graph direction", "imports"),
				"depth":     intPropWithDefault("Traversal depth", 1),
			}, "repo", "file"),
			Handler: stub,
		},
		{
			Name:        "get_symbol_diff",
			Description: "Diff symbols between two indexed repos.",
			InputSchema: objectSchema(map[string]any{
				"repo_a": stringProp("Before repo"),
				"repo_b": stringProp("After repo"),
			}, "repo_a", "repo_b"),
			Handler: stub,
		},
		{
			Name:        "get_class_hierarchy",
			Description: "Get class hierarchy for a class.",
			InputSchema: objectSchema(map[string]any{
				"repo":       stringProp("Repository identifier"),
				"class_name": stringProp("Class name"),
			}, "repo", "class_name"),
			Handler: stub,
		},
		{
			Name:        "get_related_symbols",
			Description: "Get symbols related to a symbol.",
			InputSchema: objectSchema(map[string]any{
				"repo":        stringProp("Repository identifier"),
				"symbol_id":   stringProp("Symbol ID"),
				"max_results": intPropWithDefault("Max related symbols", 10),
			}, "repo", "symbol_id"),
			Handler: stub,
		},
		{
			Name:        "suggest_queries",
			Description: "Suggest starter queries for a repo.",
			InputSchema: objectSchema(map[string]any{
				"repo": stringProp("Repository identifier"),
			}, "repo"),
			Handler: stub,
		},
		{
			Name:        "get_blast_radius",
			Description: "Estimate affected files for a symbol change.",
			InputSchema: objectSchema(map[string]any{
				"repo":   stringProp("Repository identifier"),
				"symbol": stringProp("Symbol name or ID"),
				"depth":  intPropWithDefault("Traversal depth", 1),
			}, "repo", "symbol"),
			Handler: stub,
		},
		{
			Name:        "wait_for_fresh",
			Description: "Wait for repo freshness.",
			InputSchema: objectSchema(map[string]any{
				"repo":       stringProp("Repository identifier"),
				"timeout_ms": intPropWithDefault("Timeout milliseconds", 500),
			}, "repo"),
			Handler: stub,
		},
		{
			Name:        "check_freshness",
			Description: "Check freshness by comparing indexed and current SHA.",
			InputSchema: objectSchema(map[string]any{
				"repo": stringProp("Repository identifier"),
			}, "repo"),
			Handler: stub,
		},
	}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		requiredAny := make([]any, 0, len(required))
		for _, item := range required {
			requiredAny = append(requiredAny, item)
		}
		schema["required"] = requiredAny
	}
	return schema
}

func stringProp(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
	}
}

func stringPropWithDefault(description, value string) map[string]any {
	property := stringProp(description)
	property["default"] = value
	return property
}

func stringEnumProp(description string, values []string) map[string]any {
	property := stringProp(description)
	enum := make([]any, 0, len(values))
	for _, value := range values {
		enum = append(enum, value)
	}
	property["enum"] = enum
	return property
}

func stringEnumPropWithDefault(description string, values []string, defaultValue string) map[string]any {
	property := stringEnumProp(description, values)
	property["default"] = defaultValue
	return property
}

func boolProp(description string, value bool) map[string]any {
	return map[string]any{
		"type":        "boolean",
		"description": description,
		"default":     value,
	}
}

func intProp(description string) map[string]any {
	return map[string]any{
		"type":        "integer",
		"description": description,
	}
}

func intPropWithDefault(description string, value int) map[string]any {
	property := intProp(description)
	property["default"] = value
	return property
}

func numberProp(description string) map[string]any {
	return map[string]any{
		"type":        "number",
		"description": description,
	}
}

func stringArrayProp(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items": map[string]any{
			"type": "string",
		},
	}
}

func searchSymbolsLanguageEnum(configured []string) []string {
	supported := allSupportedLanguages()
	if len(configured) == 0 {
		return supported
	}

	allowed := make(map[string]struct{}, len(supported))
	for _, language := range supported {
		allowed[language] = struct{}{}
	}

	out := make([]string, 0, len(configured))
	seen := map[string]struct{}{}
	for _, raw := range configured {
		language := strings.ToLower(strings.TrimSpace(raw))
		if language == "" {
			continue
		}
		if _, ok := allowed[language]; !ok {
			continue
		}
		if _, ok := seen[language]; ok {
			continue
		}
		seen[language] = struct{}{}
		out = append(out, language)
	}
	return out
}

func allSupportedLanguages() []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(sourceExtensions))
	for _, language := range sourceExtensions {
		normalized := strings.ToLower(strings.TrimSpace(language))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
}
