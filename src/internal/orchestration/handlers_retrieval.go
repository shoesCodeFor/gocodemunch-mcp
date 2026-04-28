package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
)

const (
	searchTextMaxQueryLength    = 500
	searchTextMaxRegexLength    = 200
	searchTextMaxLineLength     = 200
	searchTextHybridScanLimit   = 250
	searchSymbolsMaxQueryLength = 500
	searchSymbolsBytesPerToken  = 4
	searchSymbolsMinByteLength  = 20
	findReferencesMaxResults    = 200
	checkReferencesMaxContent   = 100
	searchColumnsHardCap        = 500
	relatedSymbolsMaxResults    = 50
	dependencyGraphMaxDepth     = 3
	blastRadiusMaxDepth         = 3
	classHierarchyKindClass     = "class"
	classHierarchyKindType      = "type"
	relatedSymbolSameFileScore  = 3.0
	relatedSymbolSharedImpScore = 1.5
	relatedSymbolTokenScore     = 0.5
)

var (
	searchTextNestedQuantifierPattern = regexp.MustCompile(`[+*]\s*\)\s*[+*{]`)
	searchSymbolCamelBoundaryPattern  = regexp.MustCompile(`([a-z])([A-Z])`)
	searchSymbolTokenPattern          = regexp.MustCompile(`[a-zA-Z0-9]{2,}`)
	jsImportFromPattern               = regexp.MustCompile(`(?m)^\s*import\s+(.+?)\s+from\s+['"]([^'"]+)['"]`)
	jsImportBarePattern               = regexp.MustCompile(`(?m)^\s*import\s+['"]([^'"]+)['"]`)
	jsRequirePattern                  = regexp.MustCompile(`(?m)require\(\s*['"]([^'"]+)['"]\s*\)`)
	pythonFromImportPattern           = regexp.MustCompile(`(?m)^\s*from\s+([A-Za-z0-9_\.]+)\s+import\s+(.+)$`)
	pythonImportPattern               = regexp.MustCompile(`(?m)^\s*import\s+(.+)$`)
	cIncludePattern                   = regexp.MustCompile(`(?m)^\s*#include\s*[<"]([^>"]+)[>"]`)
	dbtRefPattern                     = regexp.MustCompile(`(?i)ref\(\s*['"]([A-Za-z0-9_.-]+)['"]`)
	classHierarchyExtendsPattern      = regexp.MustCompile(`(?i)\bextends\s+([A-Za-z0-9_$.,\s]+)`)
	classHierarchyImplementsPattern   = regexp.MustCompile(`(?i)\bimplements\s+([A-Za-z0-9_$.,\s]+)`)
	classHierarchyParenBasesPattern   = regexp.MustCompile(`(?i)\bclass\s+\w[\w$]*\s*\(([^)]+)\)`)
	classHierarchyExternalBasePattern = regexp.MustCompile(`^[A-Z][\w.]*$`)
	contextBundleImportPatterns       = map[string][]*regexp.Regexp{
		"python":     {regexp.MustCompile(`^\s*(import |from \S+ import )`)},
		"javascript": {regexp.MustCompile(`^\s*(import |.*\brequire\s*\()`)},
		"typescript": {regexp.MustCompile(`^\s*(import |.*\brequire\s*\()`)},
		"tsx":        {regexp.MustCompile(`^\s*(import |.*\brequire\s*\()`)},
		"go":         {regexp.MustCompile(`^\s*import\b`)},
		"rust":       {regexp.MustCompile(`^\s*use \S`)},
		"java":       {regexp.MustCompile(`^\s*import \S`)},
		"kotlin":     {regexp.MustCompile(`^\s*import \S`)},
		"csharp":     {regexp.MustCompile(`^\s*using \S`)},
		"c":          {regexp.MustCompile(`^\s*#\s*include\b`)},
		"cpp":        {regexp.MustCompile(`^\s*#\s*include\b`)},
		"swift":      {regexp.MustCompile(`^\s*import \S`)},
		"ruby":       {regexp.MustCompile(`^\s*(require |require_relative )`)},
		"php":        {regexp.MustCompile(`^\s*(use |require|include)\b`)},
		"elixir":     {regexp.MustCompile(`^\s*(import |alias |use |require )\S`)},
		"scala":      {regexp.MustCompile(`^\s*import \S`)},
		"haskell":    {regexp.MustCompile(`^\s*import \S`)},
		"lua":        {regexp.MustCompile(`^\s*(require\s*[\(\"])`)},
		"dart":       {regexp.MustCompile(`^\s*import \S`)},
	}
	searchSymbolAllowedKinds = map[string]struct{}{
		"function": {},
		"class":    {},
		"method":   {},
		"constant": {},
		"type":     {},
		"template": {},
		"import":   {},
	}
)

type searchSymbolCandidate struct {
	ID        string
	Name      string
	Kind      string
	File      string
	Language  string
	Line      int
	EndLine   int
	Signature string
	Summary   string
	Docstring string
	Keywords  []string
	ByteLen   int
}

type scoredSymbolCandidate struct {
	Candidate searchSymbolCandidate
	Score     float64
	Breakdown map[string]any
}

type importRecord struct {
	SourceFile string
	Specifier  string
	Names      []string
	Resolved   string
}

type searchColumnMatch struct {
	Model       string
	File        string
	Column      string
	Description string
	Source      string
	Score       int
}

type contextBundleSymbolEntry struct {
	SymbolID  string
	Name      string
	Kind      string
	File      string
	Line      int
	EndLine   int
	Signature string
	Docstring string
	Language  string
	Source    string
	Imports   []string
	Callers   []string
}

type dependencyGraphEdge struct {
	From string
	To   string
}

type symbolDiffIdentityKey struct {
	Name string
	Kind string
}

func (k symbolDiffIdentityKey) serialize() string {
	return k.Name + "\x00" + k.Kind
}

type symbolDiffSymbol struct {
	ID          string
	Name        string
	Kind        string
	File        string
	Line        int
	Signature   string
	ContentHash string
}

type scoredRelatedSymbol struct {
	Candidate searchSymbolCandidate
	Score     float64
}

type rankedIntKey struct {
	Key   string
	Count int
}

type searchTextRetrievalMode string

const (
	searchTextRetrievalModeLexical  searchTextRetrievalMode = "lexical"
	searchTextRetrievalModeSemantic searchTextRetrievalMode = "semantic"
	searchTextRetrievalModeHybrid   searchTextRetrievalMode = "hybrid"
)

type searchTextMatchCandidate struct {
	File          string
	Line          int
	Text          string
	Before        []string
	After         []string
	LexicalScore  float64
	SemanticScore float64
	HybridScore   float64
}

func (s *Service) handleGetFileTree(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repo := strings.TrimSpace(stringArg(arguments, "repo", ""))
	index, err := store.Load(ctx, repo)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repo)}, nil
		}
		return nil, err
	}

	pathPrefix := stringArg(arguments, "path_prefix", "")
	includeSummaries := boolArg(arguments, "include_summaries", false)
	filteredFiles := filterIndexedFiles(index.Files, pathPrefix)
	tree := buildFileTree(filteredFiles, pathPrefix, includeSummaries)

	return map[string]any{
		"repo":        repo,
		"path_prefix": pathPrefix,
		"tree":        tree,
		"_meta": map[string]any{
			"timing_ms":  roundMilliseconds(time.Since(started)),
			"file_count": len(filteredFiles),
		},
	}, nil
}

func (s *Service) handleGetFileOutline(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repo := strings.TrimSpace(stringArg(arguments, "repo", ""))
	index, err := store.Load(ctx, repo)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repo)}, nil
		}
		return nil, err
	}

	singlePath, hasSingle := optionalStringArg(arguments, "file_path")
	if !hasSingle {
		singlePath, hasSingle = optionalStringArg(arguments, "file")
	}
	batchPaths, hasBatch := optionalStringSliceArg(arguments, "file_paths")
	if hasSingle == hasBatch {
		return nil, errors.New("Provide exactly one of 'file_path' or 'file_paths', not both and not neither.")
	}

	if hasBatch {
		results := make([]map[string]any, len(batchPaths))
		if err := s.runBatchFanout(ctx, len(batchPaths), func(_ context.Context, i int) error {
			outline := buildSingleOutline(repo, index, batchPaths[i], started)
			if meta, ok := outline["_meta"].(map[string]any); ok {
				delete(meta, "tip")
			}
			results[i] = outline
			return nil
		}); err != nil {
			return batchFanoutErrorPayload(err), nil
		}
		return map[string]any{
			"repo":    repo,
			"results": results,
			"_meta": map[string]any{
				"timing_ms": roundMilliseconds(time.Since(started)),
			},
		}, nil
	}

	return buildSingleOutline(repo, index, singlePath, started), nil
}

func (s *Service) handleGetFileContent(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repo := strings.TrimSpace(stringArg(arguments, "repo", ""))
	index, err := store.Load(ctx, repo)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repo)}, nil
		}
		return nil, err
	}

	filePath := normalizeRepoFilePath(stringArg(arguments, "file_path", ""))
	if _, ok := index.Files[filePath]; !ok {
		return map[string]any{"error": fmt.Sprintf("File not found: %s", filePath)}, nil
	}

	rawSourceRoot := strings.TrimSpace(index.SourceRoot)
	if rawSourceRoot == "" {
		return map[string]any{"error": fmt.Sprintf("File content not found: %s", filePath)}, nil
	}
	sourceRoot := filepath.Clean(rawSourceRoot)
	absoluteFile := filepath.Clean(filepath.Join(sourceRoot, filepath.FromSlash(filePath)))
	if sourceRoot == "" || !pathWithin(sourceRoot, absoluteFile) {
		return map[string]any{"error": fmt.Sprintf("File content not found: %s", filePath)}, nil
	}

	contentBytes, err := os.ReadFile(absoluteFile)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("File content not found: %s", filePath)}, nil
	}
	content := string(contentBytes)
	lines := splitContentLines(content)

	lineCount := len(lines)
	actualStart := 0
	actualEnd := 0
	selected := ""

	startLine, hasStart := optionalIntArg(arguments, "start_line")
	endLine, hasEnd := optionalIntArg(arguments, "end_line")
	if lineCount > 0 {
		if !hasStart && !hasEnd {
			actualStart = 1
			actualEnd = lineCount
			selected = content
		} else {
			requestedStart := 1
			if hasStart {
				requestedStart = startLine
			}
			requestedEnd := lineCount
			if hasEnd {
				requestedEnd = endLine
			}

			actualStart = clampInt(requestedStart, 1, lineCount)
			actualEnd = clampInt(requestedEnd, actualStart, lineCount)
			selected = strings.Join(lines[actualStart-1:actualEnd], "\n")
		}
	}

	language, _ := classifyLanguage(filePath)
	return map[string]any{
		"repo":         repo,
		"file":         filePath,
		"language":     language,
		"file_summary": "",
		"start_line":   actualStart,
		"end_line":     actualEnd,
		"line_count":   lineCount,
		"content":      selected,
		"_meta": map[string]any{
			"timing_ms":          roundMilliseconds(time.Since(started)),
			"tokens_saved":       0,
			"total_tokens_saved": 0,
		},
	}, nil
}

func (s *Service) handleInvalidateCache(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{
				"success": false,
				"error":   fmt.Sprintf("No index found for %s", repoID),
			}, nil
		}
		return nil, err
	}
	sourceRoot := strings.TrimSpace(index.SourceRoot)
	warnings := make([]string, 0, 1)

	if err := s.deleteVectorNamespace(ctx, repoID); err != nil {
		warnings = append(warnings, fmt.Sprintf("Vector namespace delete skipped: %v", err))
	}

	if err := store.Delete(ctx, repoID); err != nil {
		return nil, err
	}
	if controller := s.deps.Watcher; controller != nil && sourceRoot != "" {
		_ = controller.Stop(ctx, sourceRoot)
	}

	result := map[string]any{
		"success": true,
		"repo":    repoID,
		"message": fmt.Sprintf("Index and cached files deleted for %s", repoID),
	}
	appendWarnings(result, warnings)
	return result, nil
}

func (s *Service) handleSearchSymbols(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	query := stringArg(arguments, "query", "")
	if len(query) > searchSymbolsMaxQueryLength {
		return map[string]any{
			"error": fmt.Sprintf("Query too long (%d chars, max %d)", len(query), searchSymbolsMaxQueryLength),
		}, nil
	}

	detailInput := strings.TrimSpace(stringArg(arguments, "detail_level", "standard"))
	if detailInput == "" {
		detailInput = "standard"
	}
	detailLevel := strings.ToLower(detailInput)
	if detailLevel != "compact" && detailLevel != "standard" && detailLevel != "full" {
		return map[string]any{
			"error": fmt.Sprintf(
				"Invalid detail_level '%s'. Must be 'compact', 'standard', or 'full'.",
				detailInput,
			),
		}, nil
	}

	maxResults, hasMaxResults := optionalIntArg(arguments, "max_results")
	if !hasMaxResults {
		maxResults = 10
	}
	maxResults = clampInt(maxResults, 1, 100)

	tokenBudget, hasTokenBudget := optionalIntArg(arguments, "token_budget")
	if hasTokenBudget && tokenBudget < 0 {
		tokenBudget = 0
	}

	kindInput := strings.TrimSpace(stringArg(arguments, "kind", ""))
	kindFilter := strings.ToLower(kindInput)
	if kindFilter != "" {
		if _, allowed := searchSymbolAllowedKinds[kindFilter]; !allowed {
			return map[string]any{"error": fmt.Sprintf("Unknown kind: %s", kindInput)}, nil
		}
	}

	filePattern := strings.TrimSpace(stringArg(arguments, "file_pattern", ""))
	languageFilter := strings.ToLower(strings.TrimSpace(stringArg(arguments, "language", "")))
	debug := boolArg(arguments, "debug", false)

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	allCandidates := flattenSearchSymbolCandidates(index.Symbols)
	centrality := computeSearchSymbolCentrality(allCandidates)
	queryTerms := tokenizeSearchSymbolText(query)
	if len(queryTerms) == 0 {
		queryTerms = []string{strings.ToLower(strings.TrimSpace(query))}
	}
	hasQueryTerm := false
	for _, term := range queryTerms {
		if term != "" {
			hasQueryTerm = true
			break
		}
	}

	scored := make([]scoredSymbolCandidate, 0, len(allCandidates))
	candidatesScored := 0
	for _, candidate := range allCandidates {
		if kindFilter != "" && strings.ToLower(candidate.Kind) != kindFilter {
			continue
		}
		if filePattern != "" && !matchesSearchTextPattern(candidate.File, filePattern) {
			continue
		}
		if languageFilter != "" && strings.ToLower(candidate.Language) != languageFilter {
			continue
		}

		score, lexicalScore, breakdown := scoreSearchSymbolCandidate(candidate, queryTerms, centrality)
		if hasQueryTerm && lexicalScore <= 0 {
			continue
		}
		if score <= 0 {
			continue
		}

		candidatesScored++
		scored = append(scored, scoredSymbolCandidate{
			Candidate: candidate,
			Score:     score,
			Breakdown: breakdown,
		})
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		if scored[i].Candidate.File != scored[j].Candidate.File {
			return scored[i].Candidate.File < scored[j].Candidate.File
		}
		if scored[i].Candidate.Line != scored[j].Candidate.Line {
			return scored[i].Candidate.Line < scored[j].Candidate.Line
		}
		if scored[i].Candidate.Name != scored[j].Candidate.Name {
			return scored[i].Candidate.Name < scored[j].Candidate.Name
		}
		return scored[i].Candidate.ID < scored[j].Candidate.ID
	})

	selected := scored
	tokensUsed := 0
	truncated := false
	if hasTokenBudget {
		budgetBytes := tokenBudget * searchSymbolsBytesPerToken
		packed := make([]scoredSymbolCandidate, 0, len(scored))
		usedBytes := 0
		for _, candidate := range scored {
			size := candidate.Candidate.ByteLen
			if size <= 0 {
				size = searchSymbolsMinByteLength
			}
			if usedBytes+size <= budgetBytes {
				packed = append(packed, candidate)
				usedBytes += size
			}
		}
		selected = packed
		tokensUsed = usedBytes / searchSymbolsBytesPerToken
		truncated = candidatesScored > len(selected)
	} else if len(selected) > maxResults {
		selected = selected[:maxResults]
		truncated = true
	} else {
		truncated = candidatesScored > len(selected)
	}

	results := make([]map[string]any, 0, len(selected))
	for _, candidate := range selected {
		entry := map[string]any{
			"id":          candidate.Candidate.ID,
			"name":        candidate.Candidate.Name,
			"kind":        candidate.Candidate.Kind,
			"file":        candidate.Candidate.File,
			"line":        candidate.Candidate.Line,
			"byte_length": candidate.Candidate.ByteLen,
		}

		if detailLevel != "compact" {
			entry["signature"] = candidate.Candidate.Signature
			entry["summary"] = candidate.Candidate.Summary
		}
		if detailLevel == "full" {
			entry["end_line"] = candidate.Candidate.EndLine
			entry["docstring"] = candidate.Candidate.Docstring
			source, _, _ := symbolSourceFromIndex(index, map[string]any{
				"file":     candidate.Candidate.File,
				"line":     candidate.Candidate.Line,
				"end_line": candidate.Candidate.EndLine,
			}, 0)
			entry["source"] = source
		}
		if debug {
			entry["score"] = roundSearchSymbolScore(candidate.Score)
			entry["score_breakdown"] = candidate.Breakdown
		}

		results = append(results, entry)
	}

	meta := map[string]any{
		"timing_ms":          roundMilliseconds(time.Since(started)),
		"total_symbols":      len(allCandidates),
		"truncated":          truncated,
		"tokens_saved":       0,
		"total_tokens_saved": 0,
	}
	if hasTokenBudget {
		meta["token_budget"] = tokenBudget
		meta["tokens_used"] = tokensUsed
		tokensRemaining := tokenBudget - tokensUsed
		if tokensRemaining < 0 {
			tokensRemaining = 0
		}
		meta["tokens_remaining"] = tokensRemaining
	}
	if debug {
		meta["candidates_scored"] = candidatesScored
	}
	if len(results) > 0 {
		meta["hint"] = "Use get_context_bundle(symbol_id) to retrieve source + imports in one call"
	}

	return map[string]any{
		"result_count": len(results),
		"results":      results,
		"_meta":        meta,
	}, nil
}

func (s *Service) handleFindImporters(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	singlePath, hasSingle := optionalStringArg(arguments, "file_path")
	if hasSingle {
		singlePath = normalizeRepoFilePath(singlePath)
	}
	batchPaths, hasBatch := optionalStringSliceArg(arguments, "file_paths")
	if hasSingle == hasBatch {
		return nil, errors.New("Provide exactly one of 'file_path' or 'file_paths', not both and not neither.")
	}

	maxResults, hasMaxResults := optionalIntArg(arguments, "max_results")
	if !hasMaxResults {
		maxResults = 50
	}
	maxResults = clampInt(maxResults, 1, findReferencesMaxResults)

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	records := collectIndexedImportRecords(index)
	importersByTarget, importedFiles := groupImportersByTarget(records)
	_ = importedFiles

	if hasBatch {
		results := make([]map[string]any, len(batchPaths))
		if err := s.runBatchFanout(ctx, len(batchPaths), func(_ context.Context, i int) error {
			filePath := batchPaths[i]
			importers := importersByTarget[filePath]
			results[i] = map[string]any{
				"file_path":      filePath,
				"importer_count": len(importers),
				"importers":      cloneImportersSlice(importers[:minInt(len(importers), maxResults)]),
			}
			return nil
		}); err != nil {
			return batchFanoutErrorPayload(err), nil
		}
		return map[string]any{
			"repo":    repoID,
			"results": results,
			"_meta": map[string]any{
				"timing_ms": roundMilliseconds(time.Since(started)),
			},
		}, nil
	}

	filePath := normalizeRepoFilePath(singlePath)
	rawImporters := importersByTarget[filePath]
	deduped := dedupeImportersByFile(rawImporters)
	truncated := len(deduped) > maxResults

	tip := fmt.Sprintf("Tip: use file_paths=['%s','...'] to query multiple files in one call.", filePath)
	if !truncated {
		tip += " For usage-site matching beyond imports, also try check_references."
	}

	return map[string]any{
		"repo":           repoID,
		"file_path":      filePath,
		"importer_count": len(deduped),
		"importers":      cloneImportersSlice(deduped[:minInt(len(deduped), maxResults)]),
		"_meta": map[string]any{
			"timing_ms": roundMilliseconds(time.Since(started)),
			"truncated": truncated,
			"tip":       tip,
		},
	}, nil
}

func (s *Service) handleFindReferences(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	singleIdentifier, hasSingle := optionalStringArg(arguments, "identifier")
	if hasSingle {
		singleIdentifier = strings.TrimSpace(singleIdentifier)
	}
	batchIdentifiers, hasBatch := optionalRawStringSliceArg(arguments, "identifiers")
	if hasSingle == hasBatch {
		return nil, errors.New("Provide exactly one of 'identifier' or 'identifiers', not both and not neither.")
	}

	maxResults, hasMaxResults := optionalIntArg(arguments, "max_results")
	if !hasMaxResults {
		maxResults = 50
	}
	maxResults = clampInt(maxResults, 1, findReferencesMaxResults)

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	records := collectIndexedImportRecords(index)
	if hasBatch {
		results := make([]map[string]any, len(batchIdentifiers))
		if err := s.runBatchFanout(ctx, len(batchIdentifiers), func(_ context.Context, i int) error {
			identifier := batchIdentifiers[i]
			entryRows := buildBatchReferenceEntries(records, identifier)
			results[i] = map[string]any{
				"identifier":      identifier,
				"reference_count": len(entryRows),
				"references":      cloneReferencesSlice(entryRows[:minInt(len(entryRows), maxResults)]),
			}
			return nil
		}); err != nil {
			return batchFanoutErrorPayload(err), nil
		}

		return map[string]any{
			"repo":    repoID,
			"results": results,
			"_meta": map[string]any{
				"timing_ms": roundMilliseconds(time.Since(started)),
			},
		}, nil
	}

	referenceRows := buildSingleReferenceRows(records, singleIdentifier)
	truncated := len(referenceRows) > maxResults
	return map[string]any{
		"repo":            repoID,
		"identifier":      singleIdentifier,
		"reference_count": len(referenceRows),
		"references":      cloneReferencesSlice(referenceRows[:minInt(len(referenceRows), maxResults)]),
		"_meta": map[string]any{
			"timing_ms": roundMilliseconds(time.Since(started)),
			"truncated": truncated,
			"tip": "Tip: use identifiers=[...] to query multiple identifiers in one call. " +
				"For usage-site matching beyond imports, also try search_text or check_references.",
		},
	}, nil
}

func (s *Service) handleCheckReferences(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	singleIdentifier, hasSingle := optionalStringArg(arguments, "identifier")
	if hasSingle {
		singleIdentifier = strings.TrimSpace(singleIdentifier)
	}
	batchIdentifiers, hasBatch := optionalRawStringSliceArg(arguments, "identifiers")
	if hasSingle == hasBatch {
		return nil, errors.New("Provide exactly one of 'identifier' or 'identifiers', not both and not neither.")
	}

	searchContent := boolArg(arguments, "search_content", true)
	maxContentResults, hasMaxContentResults := optionalIntArg(arguments, "max_content_results")
	if !hasMaxContentResults {
		maxContentResults = 20
	}
	maxContentResults = clampInt(maxContentResults, 1, checkReferencesMaxContent)

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	records := collectIndexedImportRecords(index)
	if hasBatch {
		results := make([]map[string]any, len(batchIdentifiers))
		if err := s.runBatchFanout(ctx, len(batchIdentifiers), func(_ context.Context, i int) error {
			identifier := batchIdentifiers[i]
			entry := buildCheckReferenceEntry(index, records, identifier, searchContent, maxContentResults)
			entry["identifier"] = identifier
			results[i] = entry
			return nil
		}); err != nil {
			return batchFanoutErrorPayload(err), nil
		}

		return map[string]any{
			"repo":    repoID,
			"results": results,
			"_meta": map[string]any{
				"timing_ms":           roundMilliseconds(time.Since(started)),
				"identifiers_checked": len(batchIdentifiers),
			},
		}, nil
	}

	result := buildCheckReferenceEntry(index, records, singleIdentifier, searchContent, maxContentResults)
	result["repo"] = repoID
	result["identifier"] = singleIdentifier
	result["_meta"] = map[string]any{
		"timing_ms": roundMilliseconds(time.Since(started)),
	}
	return result, nil
}

func (s *Service) handleSearchColumns(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	maxResults, hasMaxResults := optionalIntArg(arguments, "max_results")
	if !hasMaxResults {
		maxResults = 20
	}
	maxResults = clampInt(maxResults, 1, searchColumnsHardCap)

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	columnSources := collectColumnSources(index.ContextMetadata)
	if len(columnSources) == 0 {
		return map[string]any{
			"error": "No column metadata found. Ensure the project has a supported ecosystem " +
				"(dbt, etc.) and re-index to populate column data.",
		}, nil
	}

	query := stringArg(arguments, "query", "")
	queryLower := strings.ToLower(query)
	queryWords := map[string]struct{}{}
	for _, word := range strings.Fields(queryLower) {
		queryWords[word] = struct{}{}
	}
	modelPattern := strings.TrimSpace(stringArg(arguments, "model_pattern", ""))
	modelFiles := buildColumnModelFileLookup(index.Symbols)

	sources := make([]string, 0, len(columnSources))
	for source := range columnSources {
		sources = append(sources, source)
	}
	sort.Strings(sources)

	totalModels := 0
	totalColumns := 0
	matches := make([]searchColumnMatch, 0, 64)
	for _, source := range sources {
		models := columnSources[source]
		totalModels += len(models)
		for _, columns := range models {
			totalColumns += len(columns)
		}

		modelNames := make([]string, 0, len(models))
		for modelName := range models {
			modelNames = append(modelNames, modelName)
		}
		sort.Strings(modelNames)

		for _, modelName := range modelNames {
			if modelPattern != "" && !matchesColumnModelPattern(modelName, modelPattern) {
				continue
			}

			columns := models[modelName]
			columnNames := make([]string, 0, len(columns))
			for columnName := range columns {
				columnNames = append(columnNames, columnName)
			}
			sort.Strings(columnNames)

			for _, columnName := range columnNames {
				description := columns[columnName]
				columnLower := strings.ToLower(columnName)
				descriptionLower := strings.ToLower(description)
				score := 0

				if queryLower == columnLower {
					score = 30
				} else if strings.Contains(columnLower, queryLower) {
					score = 15
				} else {
					for word := range queryWords {
						if strings.Contains(columnLower, word) {
							score += 5
						}
					}
				}

				if strings.Contains(descriptionLower, queryLower) {
					score += 8
				} else {
					for word := range queryWords {
						if strings.Contains(descriptionLower, word) {
							score += 2
						}
					}
				}

				if score <= 0 {
					continue
				}

				matches = append(matches, searchColumnMatch{
					Model:       modelName,
					File:        modelFiles[modelName],
					Column:      columnName,
					Description: description,
					Source:      source,
					Score:       score,
				})
			}
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if matches[i].Model != matches[j].Model {
			return matches[i].Model < matches[j].Model
		}
		if matches[i].Column != matches[j].Column {
			return matches[i].Column < matches[j].Column
		}
		return matches[i].Source < matches[j].Source
	})

	truncated := len(matches) > maxResults
	if truncated {
		matches = matches[:maxResults]
	}

	results := make([]map[string]any, 0, len(matches))
	includeSource := len(columnSources) > 1
	for _, match := range matches {
		entry := map[string]any{
			"model":       match.Model,
			"file":        match.File,
			"column":      match.Column,
			"description": match.Description,
			"score":       match.Score,
		}
		if includeSource {
			entry["source"] = match.Source
		}
		results = append(results, entry)
	}

	return map[string]any{
		"repo":          repoID,
		"query":         query,
		"result_count":  len(results),
		"total_models":  totalModels,
		"total_columns": totalColumns,
		"sources":       sources,
		"results":       results,
		"_meta": map[string]any{
			"timing_ms":          roundMilliseconds(time.Since(started)),
			"truncated":          truncated,
			"tokens_saved":       0,
			"total_tokens_saved": 0,
		},
	}, nil
}

func (s *Service) handleGetContextBundle(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	outputFormat := strings.ToLower(strings.TrimSpace(stringArg(arguments, "output_format", "json")))
	if outputFormat == "" {
		outputFormat = "json"
	}
	if outputFormat != "json" && outputFormat != "markdown" {
		return map[string]any{
			"error": fmt.Sprintf("Invalid output_format '%s'. Must be 'json' or 'markdown'.", outputFormat),
		}, nil
	}

	singleID, hasSingle := optionalStringArg(arguments, "symbol_id")
	batchIDs, hasBatch := optionalRawStringSliceArg(arguments, "symbol_ids")
	if !hasSingle && !hasBatch {
		return map[string]any{"error": "Provide either 'symbol_id' or 'symbol_ids'."}, nil
	}

	multi := hasBatch
	ids := []string{strings.TrimSpace(singleID)}
	if hasBatch {
		ids = dedupePreservingOrder(batchIDs)
	}

	includeCallers := boolArg(arguments, "include_callers", false)

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	callersByFile := map[string][]string{}
	if includeCallers {
		callersByFile = collectDirectCallersByFile(collectIndexedImportRecords(index))
	}

	var (
		contentCache sync.Map
		importCache  sync.Map
		entries      []contextBundleSymbolEntry
	)
	if multi {
		entriesByIndex := make([]contextBundleSymbolEntry, len(ids))
		missingByIndex := make([]string, len(ids))
		missingFlags := make([]bool, len(ids))
		if err := s.runBatchFanout(ctx, len(ids), func(_ context.Context, i int) error {
			symbolID := strings.TrimSpace(ids[i])
			symbol, found := findIndexedSymbol(index.Symbols, symbolID)
			if !found {
				missingByIndex[i] = symbolID
				missingFlags[i] = true
				return nil
			}

			entriesByIndex[i] = buildContextBundleEntry(
				index,
				symbol,
				includeCallers,
				callersByFile,
				&contentCache,
				&importCache,
			)
			return nil
		}); err != nil {
			return batchFanoutErrorPayload(err), nil
		}

		missing := make([]string, 0)
		entries = make([]contextBundleSymbolEntry, 0, len(ids))
		for i := range ids {
			if missingFlags[i] {
				missing = append(missing, missingByIndex[i])
				continue
			}
			entries = append(entries, entriesByIndex[i])
		}
		if len(missing) > 0 {
			return map[string]any{"error": fmt.Sprintf("Symbol(s) not found: %s", strings.Join(missing, ", "))}, nil
		}
	} else {
		symbolID := strings.TrimSpace(ids[0])
		symbol, found := findIndexedSymbol(index.Symbols, symbolID)
		if !found {
			return map[string]any{"error": fmt.Sprintf("Symbol(s) not found: %s", symbolID)}, nil
		}
		entries = []contextBundleSymbolEntry{
			buildContextBundleEntry(
				index,
				symbol,
				includeCallers,
				callersByFile,
				&contentCache,
				&importCache,
			),
		}
	}

	meta := map[string]any{
		"timing_ms":          roundMilliseconds(time.Since(started)),
		"tokens_saved":       0,
		"total_tokens_saved": 0,
	}

	if outputFormat == "markdown" {
		return map[string]any{
			"markdown": renderContextBundleMarkdown(repoID, entries),
			"_meta":    meta,
		}, nil
	}

	if !multi {
		result := contextBundleEntryToMap(entries[0], includeCallers)
		result["_meta"] = meta
		return result, nil
	}

	filesMap := map[string]any{}
	fileImports := map[string][]string{}
	fileKeys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, exists := fileImports[entry.File]; exists {
			continue
		}
		fileImports[entry.File] = cloneStringSlice(entry.Imports)
		fileKeys = append(fileKeys, entry.File)
	}
	sort.Strings(fileKeys)
	for _, filePath := range fileKeys {
		filesMap[filePath] = map[string]any{
			"imports": cloneStringSlice(fileImports[filePath]),
		}
	}

	symbols := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		symbols = append(symbols, contextBundleEntryToMap(entry, includeCallers))
	}

	return map[string]any{
		"repo":         repoID,
		"symbol_count": len(symbols),
		"symbols":      symbols,
		"files":        filesMap,
		"_meta":        meta,
	}, nil
}

func (s *Service) handleSearchText(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	mode, modeErr := parseSearchTextRetrievalMode(arguments)
	if modeErr != nil {
		return map[string]any{"error": modeErr.Error()}, nil
	}

	query := stringArg(arguments, "query", "")
	if len(query) > searchTextMaxQueryLength {
		return map[string]any{
			"error": fmt.Sprintf("Query too long (%d chars, max %d)", len(query), searchTextMaxQueryLength),
		}, nil
	}

	maxResults, hasMaxResults := optionalIntArg(arguments, "max_results")
	if !hasMaxResults {
		maxResults = 20
	}
	maxResults = clampInt(maxResults, 1, 100)

	contextLines, hasContextLines := optionalIntArg(arguments, "context_lines")
	if !hasContextLines {
		contextLines = 0
	}
	contextLines = clampInt(contextLines, 0, 10)

	isRegex := boolArg(arguments, "is_regex", false)
	var pattern *regexp.Regexp
	queryLower := strings.ToLower(query)
	if isRegex {
		if len(query) > searchTextMaxRegexLength {
			return map[string]any{
				"error": fmt.Sprintf("Regex too long (%d chars, max %d)", len(query), searchTextMaxRegexLength),
			}, nil
		}
		if searchTextNestedQuantifierPattern.MatchString(query) {
			return map[string]any{
				"error": "Regex rejected: nested quantifiers can cause catastrophic backtracking",
			}, nil
		}
		compiled, compileErr := regexp.Compile("(?i)" + query)
		if compileErr != nil {
			return map[string]any{"error": fmt.Sprintf("Invalid regex: %v", compileErr)}, nil
		}
		pattern = compiled
	}

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	filePattern := strings.TrimSpace(stringArg(arguments, "file_pattern", ""))
	files := sortedIndexedFiles(index.Files)
	filteredFiles := make([]string, 0, len(files))
	for _, filePath := range files {
		if !matchesSearchTextPattern(filePath, filePattern) {
			continue
		}
		filteredFiles = append(filteredFiles, filePath)
	}

	sourceRoot := filepath.Clean(strings.TrimSpace(index.SourceRoot))
	if mode == searchTextRetrievalModeLexical {
		results := make([]map[string]any, 0, len(filteredFiles))
		resultCount := 0
		filesSearched := 0
		truncated := false
		for _, filePath := range filteredFiles {
			if sourceRoot == "" {
				continue
			}

			absoluteFile := filepath.Clean(filepath.Join(sourceRoot, filepath.FromSlash(filePath)))
			if !pathWithin(sourceRoot, absoluteFile) {
				continue
			}

			contentBytes, readErr := os.ReadFile(absoluteFile)
			if readErr != nil {
				continue
			}
			lines := splitContentLines(string(contentBytes))
			filesSearched++

			fileMatches := make([]map[string]any, 0, 4)
			for lineIndex, line := range lines {
				matched := false
				if pattern != nil {
					matched = pattern.MatchString(line)
				} else {
					matched = strings.Contains(strings.ToLower(line), queryLower)
				}
				if !matched {
					continue
				}

				match := map[string]any{
					"line": lineIndex + 1,
					"text": trimAndClampSearchTextLine(line),
				}
				if contextLines > 0 {
					beforeStart := lineIndex - contextLines
					if beforeStart < 0 {
						beforeStart = 0
					}
					afterEnd := lineIndex + contextLines + 1
					if afterEnd > len(lines) {
						afterEnd = len(lines)
					}

					before := make([]string, 0, lineIndex-beforeStart)
					for _, item := range lines[beforeStart:lineIndex] {
						before = append(before, trimAndClampSearchTextLine(item))
					}

					after := make([]string, 0, afterEnd-lineIndex-1)
					for _, item := range lines[lineIndex+1 : afterEnd] {
						after = append(after, trimAndClampSearchTextLine(item))
					}

					match["before"] = before
					match["after"] = after
				}

				fileMatches = append(fileMatches, match)
				resultCount++
				if resultCount >= maxResults {
					truncated = true
					break
				}
			}

			if len(fileMatches) > 0 {
				results = append(results, map[string]any{
					"file":    filePath,
					"matches": fileMatches,
				})
			}
			if truncated {
				break
			}
		}

		return map[string]any{
			"result_count": resultCount,
			"results":      results,
			"_meta": map[string]any{
				"timing_ms":          roundMilliseconds(time.Since(started)),
				"files_searched":     filesSearched,
				"truncated":          truncated,
				"tokens_saved":       0,
				"total_tokens_saved": 0,
			},
		}, nil
	}

	embedder := s.deps.Embedder
	vectorBackend := s.deps.VectorBackend
	if embedder == nil || vectorBackend == nil {
		return map[string]any{
			"error": "Semantic retrieval unavailable: embedding/vector dependencies are not configured.",
		}, nil
	}

	semanticTopK := s.cfg.VectorTopK
	if semanticTopK <= 0 {
		semanticTopK = maxResults
	}
	if semanticTopK < maxResults {
		semanticTopK = maxResults
	}
	if mode == searchTextRetrievalModeHybrid {
		hybridTopK := maxResults * 3
		if hybridTopK > semanticTopK {
			semanticTopK = hybridTopK
		}
	}

	semanticCandidates, semanticErr := collectSemanticSearchTextCandidates(
		ctx,
		embedder,
		vectorBackend,
		repoID,
		query,
		filePattern,
		contextLines,
		semanticTopK,
		sourceRoot,
	)
	if semanticErr != nil {
		return nil, semanticErr
	}

	filesSearched := len(filteredFiles)
	var rankedCandidates []searchTextMatchCandidate
	meta := map[string]any{
		"timing_ms":          roundMilliseconds(time.Since(started)),
		"files_searched":     filesSearched,
		"retrieval_mode":     string(mode),
		"tokens_saved":       0,
		"total_tokens_saved": 0,
	}

	if mode == searchTextRetrievalModeHybrid {
		lexicalLimit := searchTextHybridScanLimit
		if maxResults*4 > lexicalLimit {
			lexicalLimit = maxResults * 4
		}
		lexicalCandidates, lexicalFilesSearched, _ := collectLexicalSearchTextCandidates(
			filteredFiles,
			sourceRoot,
			queryLower,
			pattern,
			contextLines,
			lexicalLimit,
		)
		if lexicalFilesSearched > filesSearched {
			filesSearched = lexicalFilesSearched
			meta["files_searched"] = filesSearched
		}

		lexicalWeight, semanticWeight := s.resolveSearchTextHybridWeights(arguments)
		meta["lexical_weight"] = lexicalWeight
		meta["semantic_weight"] = semanticWeight
		rankedCandidates = rankHybridSearchTextCandidates(
			lexicalCandidates,
			semanticCandidates,
			lexicalWeight,
			semanticWeight,
		)
	} else {
		rankedCandidates = append([]searchTextMatchCandidate(nil), semanticCandidates...)
		sort.SliceStable(rankedCandidates, func(i, j int) bool {
			if rankedCandidates[i].SemanticScore != rankedCandidates[j].SemanticScore {
				return rankedCandidates[i].SemanticScore > rankedCandidates[j].SemanticScore
			}
			if rankedCandidates[i].File != rankedCandidates[j].File {
				return rankedCandidates[i].File < rankedCandidates[j].File
			}
			if rankedCandidates[i].Line != rankedCandidates[j].Line {
				return rankedCandidates[i].Line < rankedCandidates[j].Line
			}
			return rankedCandidates[i].Text < rankedCandidates[j].Text
		})
	}

	truncated := false
	if len(rankedCandidates) > maxResults {
		rankedCandidates = rankedCandidates[:maxResults]
		truncated = true
	}
	meta["truncated"] = truncated

	return map[string]any{
		"result_count": len(rankedCandidates),
		"results":      buildSearchTextGroupedResults(rankedCandidates, contextLines, mode),
		"_meta":        meta,
	}, nil
}

func (s *Service) handleGetRepoOutline(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	directories := map[string]int{}
	for filePath := range index.Files {
		parts := strings.Split(filePath, "/")
		if len(parts) > 1 {
			directories[parts[0]+"/"]++
			continue
		}
		directories["(root)"]++
	}

	symbolKinds := map[string]int{}
	for _, raw := range index.Symbols {
		kind := "unknown"
		if symbol, ok := raw.(map[string]any); ok {
			if parsedKind := strings.TrimSpace(stringArg(symbol, "kind", "")); parsedKind != "" {
				kind = parsedKind
			}
		}
		symbolKinds[kind]++
	}

	var isStale any
	stalenessWarning := ""
	if parsedIndexedAt, parseErr := time.Parse(time.RFC3339, index.IndexedAt); parseErr == nil {
		const staleAfterDays = 7
		ageDays := int(time.Since(parsedIndexedAt.UTC()).Hours() / 24)
		if ageDays >= staleAfterDays {
			isStale = true
			stalenessWarning = fmt.Sprintf("Index is %d days old. Run index_repo to refresh.", ageDays)
		}
	}

	result := map[string]any{
		"repo":         repoID,
		"indexed_at":   index.IndexedAt,
		"file_count":   len(index.Files),
		"symbol_count": len(index.Symbols),
		"languages":    cloneLanguageCounts(index.Languages),
		"directories":  directories,
		"symbol_kinds": symbolKinds,
		"_meta": map[string]any{
			"timing_ms":          roundMilliseconds(time.Since(started)),
			"tokens_saved":       0,
			"total_tokens_saved": 0,
			"is_stale":           isStale,
		},
	}
	if stalenessWarning != "" {
		result["staleness_warning"] = stalenessWarning
	}
	return result, nil
}

func (s *Service) handleGetSymbolSource(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	singleID, hasSingle := optionalStringArg(arguments, "symbol_id")
	batchIDs, hasBatch := optionalRawStringSliceArg(arguments, "symbol_ids")
	if !hasSingle && !hasBatch {
		return map[string]any{"error": "Provide symbol_id (string) or symbol_ids (array)."}, nil
	}
	if hasSingle && hasBatch {
		return map[string]any{"error": "Provide symbol_id or symbol_ids, not both."}, nil
	}

	contextLines, hasContextLines := optionalIntArg(arguments, "context_lines")
	if !hasContextLines {
		contextLines = 0
	}
	contextLines = clampInt(contextLines, 0, 50)
	verify := boolArg(arguments, "verify", false)

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	ids := []string{strings.TrimSpace(singleID)}
	if hasBatch {
		ids = make([]string, 0, len(batchIDs))
		for _, symbolID := range batchIDs {
			ids = append(ids, strings.TrimSpace(symbolID))
		}
	}

	if hasBatch {
		symbolsByIndex := make([]map[string]any, len(ids))
		errorsByIndex := make([]map[string]any, len(ids))
		if err := s.runBatchFanout(ctx, len(ids), func(_ context.Context, i int) error {
			symbolID := ids[i]
			if symbolID == "" {
				errorsByIndex[i] = map[string]any{
					"id":    symbolID,
					"error": "Symbol not found: ",
				}
				return nil
			}

			entry, found := buildSymbolSourceEntry(index, symbolID, contextLines, verify)
			if !found {
				errorsByIndex[i] = map[string]any{
					"id":    symbolID,
					"error": fmt.Sprintf("Symbol not found: %s", symbolID),
				}
				return nil
			}

			symbolsByIndex[i] = entry
			return nil
		}); err != nil {
			return batchFanoutErrorPayload(err), nil
		}

		symbolsOut := make([]map[string]any, 0, len(ids))
		errorsOut := make([]map[string]any, 0)
		for i := range ids {
			if symbolsByIndex[i] != nil {
				symbolsOut = append(symbolsOut, symbolsByIndex[i])
			}
			if errorsByIndex[i] != nil {
				errorsOut = append(errorsOut, errorsByIndex[i])
			}
		}

		meta := map[string]any{
			"timing_ms":          roundMilliseconds(time.Since(started)),
			"tokens_saved":       0,
			"total_tokens_saved": 0,
			"symbol_count":       len(symbolsOut),
		}
		return map[string]any{
			"symbols": symbolsOut,
			"errors":  errorsOut,
			"_meta":   meta,
		}, nil
	}

	symbolID := strings.TrimSpace(singleID)
	if symbolID == "" {
		return map[string]any{"error": "Symbol not found: "}, nil
	}
	result, found := buildSymbolSourceEntry(index, symbolID, contextLines, verify)
	if !found {
		return map[string]any{"error": fmt.Sprintf("Symbol not found: %s", symbolID)}, nil
	}

	meta := map[string]any{
		"timing_ms":          roundMilliseconds(time.Since(started)),
		"tokens_saved":       0,
		"total_tokens_saved": 0,
	}
	meta["hint"] = "Use get_context_bundle(symbol_id) to retrieve source + imports in one call"
	result["_meta"] = meta
	return result, nil
}

func (s *Service) handleGetSessionStats(_ context.Context, _ map[string]any) (map[string]any, error) {
	sessionCost := zeroCostAvoidedMap()
	return map[string]any{
		"session_tokens_saved": 0,
		"session_calls":        0,
		"session_duration_s":   0.0,
		"total_tokens_saved":   0,
		"tool_breakdown":       map[string]any{},
		"session_cost_avoided": sessionCost,
		"total_cost_avoided":   zeroCostAvoidedMap(),
	}, nil
}

func (s *Service) handleGetDependencyGraph(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	directionInput := strings.ToLower(strings.TrimSpace(stringArg(arguments, "direction", "imports")))
	if directionInput == "" {
		directionInput = "imports"
	}
	if directionInput != "imports" && directionInput != "importers" && directionInput != "both" {
		return map[string]any{
			"error": fmt.Sprintf(
				"Invalid direction '%s'. Must be 'imports', 'importers', or 'both'.",
				directionInput,
			),
		}, nil
	}

	depth, hasDepth := optionalIntArg(arguments, "depth")
	if !hasDepth {
		depth = 1
	}
	depth = clampInt(depth, 1, dependencyGraphMaxDepth)

	filePath := normalizeRepoFilePath(stringArg(arguments, "file", ""))

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	if _, exists := index.Files[filePath]; !exists {
		return map[string]any{"error": fmt.Sprintf("File not found in index: %s", filePath)}, nil
	}
	if strings.TrimSpace(index.SourceRoot) == "" {
		return map[string]any{
			"error": "No import data available. Re-index with jcodemunch-mcp >= 1.3.0 to enable dependency graph.",
		}, nil
	}

	records := collectIndexedImportRecords(index)
	forward := buildDependencyAdjacency(records)
	reverse := invertDependencyAdjacency(forward)

	nodeSet := map[string]struct{}{filePath: {}}
	allEdges := make([]dependencyGraphEdge, 0, 16)
	if directionInput == "imports" || directionInput == "both" {
		nodes, edges := bfsDependencyGraph(filePath, forward, depth)
		mergeStringSet(nodeSet, nodes)
		allEdges = append(allEdges, edges...)
	}
	if directionInput == "importers" || directionInput == "both" {
		nodes, edges := bfsDependencyGraph(filePath, reverse, depth)
		mergeStringSet(nodeSet, nodes)
		allEdges = append(allEdges, edges...)
	}

	uniqueEdges := dedupeDependencyEdges(allEdges)
	sort.Slice(uniqueEdges, func(i, j int) bool {
		if uniqueEdges[i].From != uniqueEdges[j].From {
			return uniqueEdges[i].From < uniqueEdges[j].From
		}
		return uniqueEdges[i].To < uniqueEdges[j].To
	})

	nodes := make([]string, 0, len(nodeSet))
	for node := range nodeSet {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)

	edges := make([][]string, 0, len(uniqueEdges))
	for _, edge := range uniqueEdges {
		edges = append(edges, []string{edge.From, edge.To})
	}

	neighbors := map[string]any{}
	for _, node := range nodes {
		entry := map[string]any{}
		imports := cloneStringSlice(filterDependencyNeighbors(forward[node], nodeSet))
		importedBy := cloneStringSlice(filterDependencyNeighbors(reverse[node], nodeSet))
		if len(imports) > 0 {
			entry["imports"] = imports
		}
		if len(importedBy) > 0 {
			entry["imported_by"] = importedBy
		}
		neighbors[node] = entry
	}

	return map[string]any{
		"repo":       repoID,
		"file":       filePath,
		"direction":  directionInput,
		"depth":      depth,
		"node_count": len(nodes),
		"edge_count": len(edges),
		"nodes":      nodes,
		"edges":      edges,
		"neighbors":  neighbors,
		"_meta": map[string]any{
			"timing_ms": roundMilliseconds(time.Since(started)),
		},
	}, nil
}

func (s *Service) handleGetSymbolDiff(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoAInput := strings.TrimSpace(stringArg(arguments, "repo_a", ""))
	repoAID, ok, err := resolveRepoArgument(ctx, store, repoAInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoAInput)}, nil
	}

	repoBInput := strings.TrimSpace(stringArg(arguments, "repo_b", ""))
	repoBID, ok, err := resolveRepoArgument(ctx, store, repoBInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoBInput)}, nil
	}

	indexA, err := store.Load(ctx, repoAID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoAID)}, nil
		}
		return nil, err
	}

	indexB, err := store.Load(ctx, repoBID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoBID)}, nil
		}
		return nil, err
	}

	symbolsA := flattenSymbolsForDiff(indexA.Symbols)
	symbolsB := flattenSymbolsForDiff(indexB.Symbols)
	mapA := buildSymbolDiffIdentityMap(symbolsA)
	mapB := buildSymbolDiffIdentityMap(symbolsB)

	keysA := map[string]symbolDiffIdentityKey{}
	for key := range mapA {
		keysA[key.serialize()] = key
	}
	keysB := map[string]symbolDiffIdentityKey{}
	for key := range mapB {
		keysB[key.serialize()] = key
	}

	added := make([]map[string]any, 0)
	for serialized, key := range keysB {
		if _, exists := keysA[serialized]; exists {
			continue
		}
		symbol := mapB[key]
		added = append(added, map[string]any{
			"name": key.Name,
			"kind": key.Kind,
			"file": symbol.File,
			"line": symbol.Line,
		})
	}
	sort.Slice(added, func(i, j int) bool {
		if stringArg(added[i], "file", "") != stringArg(added[j], "file", "") {
			return stringArg(added[i], "file", "") < stringArg(added[j], "file", "")
		}
		return stringArg(added[i], "name", "") < stringArg(added[j], "name", "")
	})

	removed := make([]map[string]any, 0)
	for serialized, key := range keysA {
		if _, exists := keysB[serialized]; exists {
			continue
		}
		symbol := mapA[key]
		removed = append(removed, map[string]any{
			"name": key.Name,
			"kind": key.Kind,
			"file": symbol.File,
			"line": symbol.Line,
		})
	}
	sort.Slice(removed, func(i, j int) bool {
		if stringArg(removed[i], "file", "") != stringArg(removed[j], "file", "") {
			return stringArg(removed[i], "file", "") < stringArg(removed[j], "file", "")
		}
		return stringArg(removed[i], "name", "") < stringArg(removed[j], "name", "")
	})

	changed := make([]map[string]any, 0)
	unchangedCount := 0
	for serialized, key := range keysA {
		if _, exists := keysB[serialized]; !exists {
			continue
		}
		symbolA := mapA[key]
		symbolB := mapB[key]

		hashChanged := symbolA.ContentHash != "" && symbolB.ContentHash != "" && symbolA.ContentHash != symbolB.ContentHash
		signatureChanged := symbolA.ContentHash == "" && symbolA.Signature != symbolB.Signature
		if hashChanged || signatureChanged {
			row := map[string]any{
				"name":        key.Name,
				"kind":        key.Kind,
				"file_a":      symbolA.File,
				"file_b":      symbolB.File,
				"signature_a": symbolA.Signature,
				"signature_b": symbolB.Signature,
			}
			if symbolA.ContentHash != "" && symbolB.ContentHash != "" {
				row["hash_changed"] = symbolA.ContentHash != symbolB.ContentHash
			} else {
				row["hash_changed"] = nil
			}
			changed = append(changed, row)
			continue
		}
		unchangedCount++
	}
	sort.Slice(changed, func(i, j int) bool {
		if stringArg(changed[i], "file_b", "") != stringArg(changed[j], "file_b", "") {
			return stringArg(changed[i], "file_b", "") < stringArg(changed[j], "file_b", "")
		}
		return stringArg(changed[i], "name", "") < stringArg(changed[j], "name", "")
	})

	return map[string]any{
		"repo_a":          repoAID,
		"repo_b":          repoBID,
		"added_count":     len(added),
		"removed_count":   len(removed),
		"changed_count":   len(changed),
		"unchanged_count": unchangedCount,
		"added":           added,
		"removed":         removed,
		"changed":         changed,
		"_meta": map[string]any{
			"timing_ms": roundMilliseconds(time.Since(started)),
			"symbols_a": len(symbolsA),
			"symbols_b": len(symbolsB),
			"tip":       "Index the same repo under two names (e.g. repo-main, repo-feature) to diff branches.",
		},
	}, nil
}

func (s *Service) handleGetClassHierarchy(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	className := strings.TrimSpace(stringArg(arguments, "class_name", ""))

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	classSymbols := make([]searchSymbolCandidate, 0, len(index.Symbols))
	for _, symbol := range flattenSearchSymbolCandidates(index.Symbols) {
		kind := strings.ToLower(strings.TrimSpace(symbol.Kind))
		if kind != classHierarchyKindClass && kind != classHierarchyKindType {
			continue
		}
		classSymbols = append(classSymbols, symbol)
	}

	classByName := map[string]searchSymbolCandidate{}
	for _, symbol := range classSymbols {
		if _, exists := classByName[symbol.Name]; !exists {
			classByName[symbol.Name] = symbol
		}
	}

	if _, exists := classByName[className]; !exists {
		lowerQuery := strings.ToLower(className)
		matched := ""
		classNames := make([]string, 0, len(classByName))
		for name := range classByName {
			classNames = append(classNames, name)
		}
		sort.Strings(classNames)
		for _, name := range classNames {
			if strings.ToLower(name) == lowerQuery {
				matched = name
				break
			}
		}
		if matched == "" {
			return map[string]any{
				"error": fmt.Sprintf(
					"Class '%s' not found in index. Only 'class' and 'type' kinds are searched.",
					className,
				),
			}, nil
		}
		className = matched
	}

	childrenByBase := map[string][]string{}
	for _, symbol := range classSymbols {
		for _, base := range parseClassHierarchyBases(symbol.Signature) {
			if _, exists := classByName[base]; !exists {
				continue
			}
			childrenByBase[base] = append(childrenByBase[base], symbol.Name)
		}
	}

	target := classByName[className]
	format := func(symbol searchSymbolCandidate) map[string]any {
		return map[string]any{
			"name":      symbol.Name,
			"file":      symbol.File,
			"line":      symbol.Line,
			"signature": symbol.Signature,
		}
	}

	ancestors := make([]map[string]any, 0, 4)
	visitedUp := map[string]struct{}{className: {}}
	queue := append([]string{}, parseClassHierarchyBases(target.Signature)...)
	for len(queue) > 0 {
		base := queue[0]
		queue = queue[1:]
		if _, seen := visitedUp[base]; seen {
			continue
		}
		visitedUp[base] = struct{}{}

		if symbol, exists := classByName[base]; exists {
			ancestors = append(ancestors, format(symbol))
			queue = append(queue, parseClassHierarchyBases(symbol.Signature)...)
			continue
		}

		ancestors = append(ancestors, map[string]any{
			"name":      base,
			"file":      "(external)",
			"line":      0,
			"signature": "",
		})
	}

	descendants := make([]map[string]any, 0, 4)
	visitedDown := map[string]struct{}{className: {}}
	queue = append([]string{}, childrenByBase[className]...)
	for len(queue) > 0 {
		child := queue[0]
		queue = queue[1:]
		if _, seen := visitedDown[child]; seen {
			continue
		}
		visitedDown[child] = struct{}{}

		symbol, exists := classByName[child]
		if !exists {
			continue
		}
		descendants = append(descendants, format(symbol))
		queue = append(queue, childrenByBase[child]...)
	}

	return map[string]any{
		"repo":             repoID,
		"class":            format(target),
		"ancestor_count":   len(ancestors),
		"descendant_count": len(descendants),
		"ancestors":        ancestors,
		"descendants":      descendants,
		"_meta": map[string]any{
			"timing_ms": roundMilliseconds(time.Since(started)),
		},
	}, nil
}

func (s *Service) handleGetRelatedSymbols(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	symbolID := strings.TrimSpace(stringArg(arguments, "symbol_id", ""))
	maxResults, hasMaxResults := optionalIntArg(arguments, "max_results")
	if !hasMaxResults {
		maxResults = 10
	}
	maxResults = clampInt(maxResults, 1, relatedSymbolsMaxResults)

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	targetSymbol, found := findIndexedSymbol(index.Symbols, symbolID)
	if !found {
		return map[string]any{"error": fmt.Sprintf("Symbol not found: %s", symbolID)}, nil
	}

	candidates := flattenSearchSymbolCandidates(index.Symbols)
	candidateByID := map[string]searchSymbolCandidate{}
	for _, candidate := range candidates {
		candidateByID[candidate.ID] = candidate
	}

	targetName := strings.TrimSpace(stringArg(targetSymbol, "name", ""))
	targetKind := strings.TrimSpace(stringArg(targetSymbol, "kind", ""))
	targetFile := normalizeRepoFilePath(stringArg(targetSymbol, "file", ""))
	if mapped, exists := candidateByID[symbolID]; exists {
		if targetName == "" {
			targetName = mapped.Name
		}
		if targetKind == "" {
			targetKind = mapped.Kind
		}
		if targetFile == "" {
			targetFile = mapped.File
		}
	}

	targetTokens := toTokenSet(tokenizeSearchSymbolText(targetName))
	fileImporters := buildFileImportersByTarget(collectIndexedImportRecords(index))
	targetImporters := fileImporters[targetFile]

	scored := make([]scoredRelatedSymbol, 0, 16)
	for _, candidate := range candidates {
		if candidate.ID == symbolID {
			continue
		}

		score := 0.0
		if targetFile != "" && candidate.File == targetFile {
			score += relatedSymbolSameFileScore
		} else if hasSharedImporter(targetImporters, fileImporters[candidate.File]) {
			score += relatedSymbolSharedImpScore
		}

		overlap := tokenSetOverlapCount(targetTokens, toTokenSet(tokenizeSearchSymbolText(candidate.Name)))
		if overlap > 0 {
			score += float64(overlap) * relatedSymbolTokenScore
		}
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredRelatedSymbol{
			Candidate: candidate,
			Score:     score,
		})
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		if scored[i].Candidate.File != scored[j].Candidate.File {
			return scored[i].Candidate.File < scored[j].Candidate.File
		}
		if scored[i].Candidate.Name != scored[j].Candidate.Name {
			return scored[i].Candidate.Name < scored[j].Candidate.Name
		}
		return scored[i].Candidate.ID < scored[j].Candidate.ID
	})

	limit := minInt(len(scored), maxResults)
	related := make([]map[string]any, 0, limit)
	for _, item := range scored[:limit] {
		related = append(related, map[string]any{
			"id":                item.Candidate.ID,
			"name":              item.Candidate.Name,
			"kind":              item.Candidate.Kind,
			"file":              item.Candidate.File,
			"line":              item.Candidate.Line,
			"signature":         item.Candidate.Signature,
			"relatedness_score": roundToPlaces(item.Score, 2),
		})
	}

	return map[string]any{
		"repo": repoID,
		"symbol": map[string]any{
			"id":   symbolID,
			"name": targetName,
			"kind": targetKind,
			"file": targetFile,
		},
		"related_count": len(related),
		"related":       related,
		"_meta": map[string]any{
			"timing_ms": roundMilliseconds(time.Since(started)),
		},
	}, nil
}

func (s *Service) handleSuggestQueries(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	symbols := flattenSearchSymbolCandidates(index.Symbols)
	if len(symbols) == 0 {
		return map[string]any{"error": "Index is empty — no symbols found."}, nil
	}

	kindCounts := map[string]int{}
	languageCounts := map[string]int{}
	keywordCounts := map[string]int{}
	classNames := make([]string, 0, 5)
	functionNames := make([]string, 0, 5)
	for _, symbol := range symbols {
		kind := strings.TrimSpace(symbol.Kind)
		if kind == "" {
			kind = "unknown"
		}
		kindCounts[kind]++

		language := strings.TrimSpace(symbol.Language)
		if language == "" {
			language = "unknown"
		}
		languageCounts[language]++

		for _, keyword := range symbol.Keywords {
			normalized := strings.ToLower(strings.TrimSpace(keyword))
			if normalized == "" {
				continue
			}
			keywordCounts[normalized]++
		}

		if len(classNames) < 5 && strings.EqualFold(kind, classHierarchyKindClass) {
			classNames = append(classNames, symbol.Name)
		}
		if len(functionNames) < 5 &&
			(strings.EqualFold(kind, "function") || strings.EqualFold(kind, "method")) &&
			!strings.HasPrefix(symbol.Name, "_") {
			functionNames = append(functionNames, symbol.Name)
		}
	}

	topKeywords := rankMapTopKeys(keywordCounts, 15)
	importCountByFile := map[string]int{}
	for _, record := range collectIndexedImportRecords(index) {
		if record.Resolved == "" {
			continue
		}
		importCountByFile[record.Resolved]++
	}
	mostImported := make([]rankedIntKey, 0, len(importCountByFile))
	for filePath, count := range importCountByFile {
		mostImported = append(mostImported, rankedIntKey{Key: filePath, Count: count})
	}
	sort.Slice(mostImported, func(i, j int) bool {
		if mostImported[i].Count != mostImported[j].Count {
			return mostImported[i].Count > mostImported[j].Count
		}
		return mostImported[i].Key < mostImported[j].Key
	})
	if len(mostImported) > 8 {
		mostImported = mostImported[:8]
	}

	mostImportedOut := make([]map[string]any, 0, len(mostImported))
	for _, item := range mostImported {
		mostImportedOut = append(mostImportedOut, map[string]any{
			"file":        item.Key,
			"imported_by": item.Count,
		})
	}

	exampleQueries := make([]map[string]any, 0, 5)
	if len(topKeywords) > 0 {
		exampleQueries = append(exampleQueries, map[string]any{
			"query":       topKeywords[0],
			"tool":        "search_symbols",
			"description": fmt.Sprintf("Find symbols related to '%s' (most common keyword)", topKeywords[0]),
		})
	}
	if len(classNames) > 0 {
		exampleQueries = append(exampleQueries, map[string]any{
			"query":       classNames[0],
			"tool":        "search_symbols",
			"description": fmt.Sprintf("Look up the '%s' class definition", classNames[0]),
		})
	}
	if len(functionNames) > 0 {
		exampleQueries = append(exampleQueries, map[string]any{
			"query":       functionNames[0],
			"tool":        "search_symbols",
			"description": fmt.Sprintf("Find the '%s' function", functionNames[0]),
		})
	}
	if len(mostImportedOut) > 0 {
		exampleQueries = append(exampleQueries, map[string]any{
			"query":       stringArg(mostImportedOut[0], "file", ""),
			"tool":        "get_file_outline",
			"description": fmt.Sprintf("Outline the most-imported file (imported by %d files)", mostImported[0].Count),
		})
	}
	if len(topKeywords) > 1 {
		sliceEnd := minInt(len(topKeywords), 3)
		exampleQueries = append(exampleQueries, map[string]any{
			"query":       strings.Join(topKeywords[1:sliceEnd], " "),
			"tool":        "search_symbols",
			"description": "Multi-keyword search combining two common topics",
		})
	}

	return map[string]any{
		"repo":                  repoID,
		"symbol_count":          len(symbols),
		"file_count":            len(index.Files),
		"kind_distribution":     rankMapToOrderedMap(kindCounts),
		"language_distribution": rankMapToOrderedMap(languageCounts),
		"top_keywords":          topKeywords,
		"most_imported_files":   mostImportedOut,
		"example_queries":       exampleQueries,
		"_meta": map[string]any{
			"timing_ms": roundMilliseconds(time.Since(started)),
		},
	}, nil
}

func (s *Service) handleGetBlastRadius(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	depth, hasDepth := optionalIntArg(arguments, "depth")
	if !hasDepth {
		depth = 1
	}
	depth = clampInt(depth, 1, blastRadiusMaxDepth)
	symbolQuery := strings.TrimSpace(stringArg(arguments, "symbol", ""))

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	if strings.TrimSpace(index.SourceRoot) == "" {
		return map[string]any{
			"error": "No import data available. Re-index with jcodemunch-mcp >= 1.3.0 to enable blast radius analysis.",
		}, nil
	}

	matches := resolveBlastRadiusSymbols(index.Symbols, symbolQuery)
	if len(matches) == 0 {
		return map[string]any{
			"error": fmt.Sprintf("Symbol not found: '%s'. Try search_symbols first.", symbolQuery),
		}, nil
	}

	if len(matches) > 1 {
		candidates := make([]map[string]any, 0, len(matches))
		for _, match := range matches {
			candidates = append(candidates, map[string]any{
				"name": match.Name,
				"file": match.File,
				"id":   match.ID,
			})
		}
		sort.Slice(candidates, func(i, j int) bool {
			leftFile := stringArg(candidates[i], "file", "")
			rightFile := stringArg(candidates[j], "file", "")
			if leftFile != rightFile {
				return leftFile < rightFile
			}
			return stringArg(candidates[i], "id", "") < stringArg(candidates[j], "id", "")
		})

		return map[string]any{
			"error": fmt.Sprintf(
				"Ambiguous symbol '%s': found %d definitions. Use the symbol 'id' field to disambiguate.",
				symbolQuery,
				len(matches),
			),
			"candidates": candidates,
		}, nil
	}

	symbol := matches[0]
	records := collectIndexedImportRecords(index)
	forward := buildDependencyAdjacency(records)
	reverse := invertDependencyAdjacency(forward)

	nodes, _ := bfsDependencyGraph(symbol.File, reverse, depth)
	importerFiles := make([]string, 0, len(nodes))
	for file := range nodes {
		if file == symbol.File {
			continue
		}
		importerFiles = append(importerFiles, file)
	}
	sort.Strings(importerFiles)

	confirmed, potential := classifyBlastRadiusImporters(index, importerFiles, symbol.Name)

	return map[string]any{
		"repo": repoID,
		"symbol": map[string]any{
			"name": symbol.Name,
			"kind": symbol.Kind,
			"file": symbol.File,
			"line": symbol.Line,
			"id":   symbol.ID,
		},
		"depth":           depth,
		"importer_count":  len(importerFiles),
		"confirmed_count": len(confirmed),
		"potential_count": len(potential),
		"confirmed":       confirmed,
		"potential":       potential,
		"_meta": map[string]any{
			"timing_ms": roundMilliseconds(time.Since(started)),
			"tip": "confirmed = imports the file + mentions the symbol name; " +
				"potential = imports the file only (wildcard/namespace import)",
		},
	}, nil
}

func (s *Service) handleWaitForFresh(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	timeoutMS, hasTimeout := optionalIntArg(arguments, "timeout_ms")
	if !hasTimeout {
		timeoutMS = 500
	}
	if timeoutMS < 0 {
		timeoutMS = 0
	}

	controller := s.deps.Watcher
	if controller == nil {
		return map[string]any{
			"fresh":     true,
			"waited_ms": 0,
		}, nil
	}

	waitStarted := time.Now()
	repoID := strings.TrimSpace(stringArg(arguments, "repo", ""))
	status, err := controller.WaitForFresh(ctx, repoID, timeoutMS)
	waitedMS := int(math.Round(time.Since(waitStarted).Seconds() * 1000))
	if waitedMS < 0 {
		waitedMS = 0
	}
	if err != nil {
		if status.ReindexFailures >= 2 && strings.TrimSpace(status.LastError) != "" {
			return map[string]any{
				"fresh":            false,
				"waited_ms":        waitedMS,
				"reason":           "reindex_failed",
				"reindex_error":    status.LastError,
				"reindex_failures": status.ReindexFailures,
			}, nil
		}
		return map[string]any{
			"fresh":     false,
			"waited_ms": waitedMS,
			"reason":    "timeout",
		}, nil
	}

	if status.Fresh {
		return map[string]any{
			"fresh":     true,
			"waited_ms": waitedMS,
		}, nil
	}

	if status.ReindexFailures >= 2 && strings.TrimSpace(status.LastError) != "" {
		return map[string]any{
			"fresh":            false,
			"waited_ms":        waitedMS,
			"reason":           "reindex_failed",
			"reindex_error":    status.LastError,
			"reindex_failures": status.ReindexFailures,
		}, nil
	}

	return map[string]any{
		"fresh":     false,
		"waited_ms": waitedMS,
		"reason":    "timeout",
	}, nil
}

func (s *Service) handleCheckFreshness(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repoInput := strings.TrimSpace(stringArg(arguments, "repo", ""))
	repoID, ok, err := resolveRepoArgument(ctx, store, repoInput)
	if err != nil {
		if strings.HasPrefix(err.Error(), "Ambiguous repository name:") {
			return map[string]any{"error": err.Error()}, nil
		}
		return nil, err
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Repository not found: %s", repoInput)}, nil
	}

	index, err := store.Load(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrRepoNotFound) {
			return map[string]any{"error": fmt.Sprintf("Repository not indexed: %s", repoID)}, nil
		}
		return nil, err
	}

	meta := map[string]any{
		"timing_ms": roundMilliseconds(time.Since(started)),
	}

	sourceRoot := strings.TrimSpace(index.SourceRoot)
	indexedSHA := strings.TrimSpace(index.GitHead)
	if sourceRoot == "" {
		return map[string]any{
			"fresh":       nil,
			"is_local":    false,
			"message":     "Freshness check requires a locally indexed repo (index_folder). For GitHub repos, call index_repo — it compares tree SHAs automatically.",
			"indexed_sha": indexedSHA,
			"_meta":       meta,
		}, nil
	}
	sourceRoot = filepath.Clean(sourceRoot)

	if indexedSHA == "" {
		return map[string]any{
			"fresh":       nil,
			"is_local":    true,
			"source_root": sourceRoot,
			"message": "No SHA stored at index time — repo may not be a git repo, " +
				"or was indexed before git tracking was added. Re-run index_folder.",
			"_meta": meta,
		}, nil
	}

	currentSHA, ok := gitHeadAtPath(ctx, sourceRoot)
	if !ok {
		return map[string]any{
			"fresh":       nil,
			"is_local":    true,
			"source_root": sourceRoot,
			"indexed_sha": indexedSHA,
			"message":     "Could not read current git HEAD. Is git installed and is this a git repo?",
			"_meta":       meta,
		}, nil
	}

	fresh := indexedSHA == currentSHA
	commitsBehind := any(nil)
	if !fresh {
		if count, countOK := commitDistance(ctx, sourceRoot, indexedSHA, currentSHA); countOK {
			commitsBehind = count
		}
	}

	return map[string]any{
		"fresh":          fresh,
		"is_local":       true,
		"source_root":    sourceRoot,
		"indexed_sha":    indexedSHA,
		"current_sha":    currentSHA,
		"commits_behind": commitsBehind,
		"_meta":          meta,
	}, nil
}

func resolveBlastRadiusSymbols(rawSymbols map[string]any, query string) []searchSymbolCandidate {
	query = strings.TrimSpace(query)
	if query == "" {
		return []searchSymbolCandidate{}
	}

	if direct, ok := findIndexedSymbol(rawSymbols, query); ok {
		if candidate, ok := toSearchSymbolCandidate(direct, query); ok {
			return []searchSymbolCandidate{candidate}
		}
	}

	candidates := flattenSearchSymbolCandidates(rawSymbols)
	exact := make([]searchSymbolCandidate, 0, 4)
	for _, candidate := range candidates {
		if candidate.Name == query {
			exact = append(exact, candidate)
		}
	}
	if len(exact) > 0 {
		return exact
	}

	folded := make([]searchSymbolCandidate, 0, 4)
	for _, candidate := range candidates {
		if strings.EqualFold(candidate.Name, query) {
			folded = append(folded, candidate)
		}
	}
	return folded
}

func classifyBlastRadiusImporters(
	index storage.RepoIndex,
	importerFiles []string,
	symbolName string,
) ([]map[string]any, []map[string]any) {
	confirmed := make([]map[string]any, 0, len(importerFiles))
	potential := make([]map[string]any, 0, len(importerFiles))
	for _, importerFile := range importerFiles {
		content, ok := readIndexedFileContent(index, importerFile)
		if !ok {
			potential = append(potential, map[string]any{
				"file":   importerFile,
				"reason": "content unavailable",
			})
			continue
		}

		references := countWordTokenOccurrences(content, symbolName)
		if references > 0 {
			confirmed = append(confirmed, map[string]any{
				"file":       importerFile,
				"references": references,
			})
			continue
		}

		potential = append(potential, map[string]any{
			"file":   importerFile,
			"reason": "symbol name not found (may use namespace/wildcard import)",
		})
	}

	sort.Slice(confirmed, func(i, j int) bool {
		return stringArg(confirmed[i], "file", "") < stringArg(confirmed[j], "file", "")
	})
	sort.Slice(potential, func(i, j int) bool {
		return stringArg(potential[i], "file", "") < stringArg(potential[j], "file", "")
	})

	return confirmed, potential
}

func countWordTokenOccurrences(content, token string) int {
	content = strings.TrimSpace(content)
	token = strings.TrimSpace(token)
	if content == "" || token == "" {
		return 0
	}

	pattern, err := regexp.Compile(`\b` + regexp.QuoteMeta(token) + `\b`)
	if err != nil {
		return 0
	}
	return len(pattern.FindAllStringIndex(content, -1))
}

func gitHeadAtPath(ctx context.Context, repoPath string) (string, bool) {
	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	command := exec.CommandContext(gitCtx, "git", "rev-parse", "HEAD")
	command.Dir = repoPath
	output, err := command.Output()
	if err != nil {
		return "", false
	}
	head := strings.TrimSpace(string(output))
	if head == "" {
		return "", false
	}
	return head, true
}

func commitDistance(ctx context.Context, repoPath, baseSHA, headSHA string) (int, bool) {
	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	command := exec.CommandContext(gitCtx, "git", "rev-list", "--count", fmt.Sprintf("%s..%s", baseSHA, headSHA))
	command.Dir = repoPath
	output, err := command.Output()
	if err != nil {
		return 0, false
	}

	count, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0, false
	}
	return count, true
}

func collectColumnSources(contextMetadata map[string]any) map[string]map[string]map[string]string {
	if len(contextMetadata) == 0 {
		return map[string]map[string]map[string]string{}
	}

	out := map[string]map[string]map[string]string{}
	for key, value := range contextMetadata {
		if !strings.HasSuffix(key, "_columns") {
			continue
		}

		source := strings.TrimSuffix(strings.TrimSpace(key), "_columns")
		if source == "" {
			continue
		}

		models, ok := parseColumnModels(value)
		if !ok {
			continue
		}
		out[source] = models
	}
	return out
}

func parseColumnModels(raw any) (map[string]map[string]string, bool) {
	switch typed := raw.(type) {
	case map[string]map[string]string:
		out := make(map[string]map[string]string, len(typed))
		for modelName, columns := range typed {
			out[modelName] = cloneColumnDescriptions(columns)
		}
		return out, true
	case map[string]any:
		out := make(map[string]map[string]string, len(typed))
		for modelName, columnsRaw := range typed {
			columns, ok := parseColumnDescriptions(columnsRaw)
			if !ok {
				continue
			}
			out[modelName] = columns
		}
		return out, true
	default:
		return nil, false
	}
}

func parseColumnDescriptions(raw any) (map[string]string, bool) {
	switch typed := raw.(type) {
	case map[string]string:
		return cloneColumnDescriptions(typed), true
	case map[string]any:
		out := make(map[string]string, len(typed))
		for columnName, descriptionRaw := range typed {
			switch description := descriptionRaw.(type) {
			case string:
				out[columnName] = description
			case nil:
				out[columnName] = ""
			default:
				out[columnName] = strings.TrimSpace(fmt.Sprintf("%v", description))
			}
		}
		return out, true
	default:
		return nil, false
	}
}

func cloneColumnDescriptions(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func buildColumnModelFileLookup(symbols map[string]any) map[string]string {
	out := map[string]string{}
	for _, symbol := range flattenSearchSymbolCandidates(symbols) {
		if !strings.HasSuffix(strings.ToLower(symbol.File), ".sql") {
			continue
		}
		fileName := path.Base(symbol.File)
		extension := path.Ext(fileName)
		model := strings.TrimSuffix(fileName, extension)
		if model == "" {
			continue
		}
		if _, exists := out[model]; !exists {
			out[model] = symbol.File
		}
	}
	return out
}

func matchesColumnModelPattern(modelName, pattern string) bool {
	matched, err := path.Match(pattern, modelName)
	if err != nil {
		return modelName == pattern
	}
	return matched
}

func dedupePreservingOrder(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		normalized := strings.TrimSpace(item)
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func collectDirectCallersByFile(records []importRecord) map[string][]string {
	callersByFile := map[string]map[string]struct{}{}
	for _, record := range records {
		if record.Resolved == "" || record.SourceFile == "" || record.SourceFile == record.Resolved {
			continue
		}
		callers, ok := callersByFile[record.Resolved]
		if !ok {
			callers = map[string]struct{}{}
			callersByFile[record.Resolved] = callers
		}
		callers[record.SourceFile] = struct{}{}
	}

	out := map[string][]string{}
	for filePath, callerSet := range callersByFile {
		callers := make([]string, 0, len(callerSet))
		for caller := range callerSet {
			callers = append(callers, caller)
		}
		sort.Strings(callers)
		out[filePath] = callers
	}
	return out
}

func readIndexedFileContent(index storage.RepoIndex, filePath string) (string, bool) {
	sourceRoot := filepath.Clean(strings.TrimSpace(index.SourceRoot))
	if sourceRoot == "" {
		return "", false
	}

	absolutePath := filepath.Clean(filepath.Join(sourceRoot, filepath.FromSlash(filePath)))
	if !pathWithin(sourceRoot, absolutePath) {
		return "", false
	}

	contentBytes, err := os.ReadFile(absolutePath)
	if err != nil {
		return "", false
	}
	return string(contentBytes), true
}

func extractContextBundleImports(content, language string) []string {
	if content == "" {
		return []string{}
	}

	patterns := contextBundleImportPatterns[strings.ToLower(strings.TrimSpace(language))]
	if len(patterns) == 0 {
		return []string{}
	}

	lines := splitContentLines(content)
	imports := make([]string, 0, 8)
	if language == "go" {
		inBlock := false
		for _, line := range lines {
			stripped := strings.TrimSpace(line)
			if stripped == "import (" {
				inBlock = true
				imports = append(imports, line)
				continue
			}
			if inBlock {
				imports = append(imports, line)
				if stripped == ")" {
					inBlock = false
				}
				continue
			}
			if matchesAnyPattern(line, patterns) {
				imports = append(imports, line)
			}
		}
		return imports
	}

	for _, line := range lines {
		if matchesAnyPattern(line, patterns) {
			imports = append(imports, line)
		}
	}
	return imports
}

func matchesAnyPattern(text string, patterns []*regexp.Regexp) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

func contextBundleEntryToMap(entry contextBundleSymbolEntry, includeCallers bool) map[string]any {
	out := map[string]any{
		"symbol_id": entry.SymbolID,
		"name":      entry.Name,
		"kind":      entry.Kind,
		"file":      entry.File,
		"line":      entry.Line,
		"end_line":  entry.EndLine,
		"signature": entry.Signature,
		"docstring": entry.Docstring,
		"source":    entry.Source,
		"imports":   cloneStringSlice(entry.Imports),
	}
	if includeCallers {
		out["callers"] = cloneStringSlice(entry.Callers)
	}
	return out
}

func renderContextBundleMarkdown(repo string, entries []contextBundleSymbolEntry) string {
	lines := []string{fmt.Sprintf("# Context Bundle: %s\n", repo)}
	for _, entry := range entries {
		fence := strings.TrimSpace(entry.Language)
		lines = append(lines, fmt.Sprintf("## `%s` (%s) — `%s:%d`\n", entry.Name, entry.Kind, entry.File, entry.Line))
		if len(entry.Imports) > 0 {
			lines = append(lines, fmt.Sprintf("### Imports\n```%s\n%s\n```\n", fence, strings.Join(entry.Imports, "\n")))
		}
		docstring := strings.TrimSpace(entry.Docstring)
		if docstring != "" {
			lines = append(lines, fmt.Sprintf("> %s\n", docstring))
		}
		if strings.TrimSpace(entry.Source) != "" {
			lines = append(lines, fmt.Sprintf("### Definition\n```%s\n%s\n```\n", fence, strings.TrimRight(entry.Source, "\n")))
		}
		if len(entry.Callers) > 0 {
			callerLines := make([]string, 0, len(entry.Callers))
			for _, caller := range entry.Callers {
				callerLines = append(callerLines, fmt.Sprintf("- `%s`", caller))
			}
			lines = append(lines, "### Callers\n"+strings.Join(callerLines, "\n")+"\n")
		}
		lines = append(lines, "---\n")
	}
	return strings.Join(lines, "\n")
}

func collectIndexedImportRecords(index storage.RepoIndex) []importRecord {
	sourceRoot := filepath.Clean(strings.TrimSpace(index.SourceRoot))
	if sourceRoot == "" {
		return []importRecord{}
	}

	sourceFiles := sortedIndexedFiles(index.Files)
	if len(sourceFiles) == 0 {
		return []importRecord{}
	}

	sourceSet := make(map[string]struct{}, len(sourceFiles))
	for _, file := range sourceFiles {
		sourceSet[file] = struct{}{}
	}

	out := make([]importRecord, 0, len(sourceFiles))
	for _, filePath := range sourceFiles {
		absolutePath := filepath.Clean(filepath.Join(sourceRoot, filepath.FromSlash(filePath)))
		if !pathWithin(sourceRoot, absolutePath) {
			continue
		}
		contentBytes, err := os.ReadFile(absolutePath)
		if err != nil {
			continue
		}

		fileRecords := extractImportRecordsFromFile(filePath, string(contentBytes))
		for i := range fileRecords {
			fileRecords[i].Resolved = resolveImportTargetFile(fileRecords[i].Specifier, filePath, sourceSet, sourceFiles)
			out = append(out, fileRecords[i])
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].SourceFile != out[j].SourceFile {
			return out[i].SourceFile < out[j].SourceFile
		}
		if out[i].Specifier != out[j].Specifier {
			return out[i].Specifier < out[j].Specifier
		}
		return strings.Join(out[i].Names, ",") < strings.Join(out[j].Names, ",")
	})
	return out
}

func extractImportRecordsFromFile(filePath, content string) []importRecord {
	out := make([]importRecord, 0, 8)

	addRecord := func(specifier string, names []string) {
		specifier = strings.TrimSpace(specifier)
		if specifier == "" {
			return
		}
		out = append(out, importRecord{
			SourceFile: filePath,
			Specifier:  specifier,
			Names:      normalizeImportNames(names),
		})
	}

	for _, match := range jsImportFromPattern.FindAllStringSubmatch(content, -1) {
		if len(match) < 3 {
			continue
		}
		addRecord(match[2], parseJSImportClause(match[1]))
	}
	for _, match := range jsImportBarePattern.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		addRecord(match[1], []string{})
	}
	for _, match := range jsRequirePattern.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		addRecord(match[1], []string{})
	}
	for _, match := range pythonFromImportPattern.FindAllStringSubmatch(content, -1) {
		if len(match) < 3 {
			continue
		}
		addRecord(strings.ReplaceAll(strings.TrimSpace(match[1]), ".", "/"), parsePythonImportNames(match[2]))
	}
	for _, match := range pythonImportPattern.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		if strings.Contains(strings.TrimSpace(match[1]), " import ") {
			continue
		}
		for _, segment := range strings.Split(match[1], ",") {
			module, alias := splitImportAlias(segment)
			module = strings.ReplaceAll(strings.TrimSpace(module), ".", "/")
			name := strings.TrimSpace(alias)
			if name == "" {
				name = path.Base(module)
			}
			names := []string{}
			if name != "" {
				names = []string{name}
			}
			addRecord(module, names)
		}
	}
	for _, match := range cIncludePattern.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		addRecord(match[1], []string{})
	}
	for _, match := range dbtRefPattern.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		addRecord(match[1], []string{})
	}
	return out
}

func parseJSImportClause(clause string) []string {
	clause = strings.TrimSpace(clause)
	clause = strings.TrimPrefix(clause, "type ")
	if clause == "" {
		return []string{}
	}

	names := make([]string, 0, 4)
	addName := func(value string) {
		normalized := normalizeImportName(value)
		if normalized != "" {
			names = append(names, normalized)
		}
	}

	if open := strings.Index(clause, "{"); open >= 0 {
		prefix := strings.TrimSpace(clause[:open])
		if prefix != "" {
			for _, segment := range strings.Split(prefix, ",") {
				addName(segment)
			}
		}
		closeIndex := strings.Index(clause[open:], "}")
		if closeIndex >= 0 {
			inside := clause[open+1 : open+closeIndex]
			for _, segment := range strings.Split(inside, ",") {
				base, _ := splitImportAlias(segment)
				addName(base)
			}
		}
		return dedupeStrings(names)
	}

	if strings.HasPrefix(strings.TrimSpace(clause), "*") {
		_, alias := splitImportAlias(clause)
		addName(alias)
		return dedupeStrings(names)
	}

	for _, segment := range strings.Split(clause, ",") {
		addName(segment)
	}
	return dedupeStrings(names)
}

func parsePythonImportNames(clause string) []string {
	names := make([]string, 0, 4)
	for _, segment := range strings.Split(clause, ",") {
		base, _ := splitImportAlias(segment)
		base = strings.TrimSpace(base)
		if base == "" || base == "*" {
			continue
		}
		base = strings.ReplaceAll(base, ".", "/")
		names = append(names, normalizeImportName(path.Base(base)))
	}
	return dedupeStrings(names)
}

func splitImportAlias(raw string) (string, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ""
	}
	lowered := strings.ToLower(trimmed)
	index := strings.Index(lowered, " as ")
	if index < 0 {
		return trimmed, ""
	}
	return strings.TrimSpace(trimmed[:index]), strings.TrimSpace(trimmed[index+4:])
}

func normalizeImportName(raw string) string {
	candidate := strings.TrimSpace(raw)
	candidate = strings.Trim(candidate, "{}")
	candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "type "))
	if candidate == "" {
		return ""
	}
	if strings.HasPrefix(candidate, "...") {
		candidate = strings.TrimPrefix(candidate, "...")
	}
	fields := strings.Fields(candidate)
	if len(fields) == 0 {
		return ""
	}
	candidate = strings.Trim(fields[0], ",")
	if candidate == "*" {
		return ""
	}
	return candidate
}

func normalizeImportNames(in []string) []string {
	out := make([]string, 0, len(in))
	for _, name := range in {
		normalized := normalizeImportName(name)
		if normalized != "" {
			out = append(out, normalized)
		}
	}
	return dedupeStrings(out)
}

func dedupeStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		normalized := strings.TrimSpace(item)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func resolveImportTargetFile(
	specifier string,
	sourceFile string,
	sourceSet map[string]struct{},
	sourceFiles []string,
) string {
	candidates := buildImportCandidates(specifier, sourceFile)
	for _, candidate := range candidates {
		if _, ok := sourceSet[candidate]; ok {
			return candidate
		}
	}

	for _, candidate := range candidates {
		base := candidate
		ext := path.Ext(base)
		if ext != "" {
			base = strings.TrimSuffix(base, ext)
		}
		if base == "" {
			continue
		}
		directPrefix := base + "."
		indexPrefix := strings.TrimSuffix(base, "/") + "/index."
		for _, filePath := range sourceFiles {
			if strings.HasPrefix(filePath, directPrefix) || strings.HasPrefix(filePath, indexPrefix) {
				return filePath
			}
		}
	}
	return ""
}

func buildImportCandidates(specifier string, sourceFile string) []string {
	specifier = strings.TrimSpace(specifier)
	if specifier == "" {
		return []string{}
	}

	seen := map[string]struct{}{}
	out := make([]string, 0, 4)
	add := func(candidate string) {
		normalized := normalizeRepoFilePath(candidate)
		if normalized == "" {
			return
		}
		if _, ok := seen[normalized]; ok {
			return
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}

	if strings.HasPrefix(specifier, "./") || strings.HasPrefix(specifier, "../") {
		add(path.Join(path.Dir(sourceFile), specifier))
	} else if strings.HasPrefix(specifier, "/") {
		add(strings.TrimPrefix(specifier, "/"))
	}

	if strings.Contains(specifier, ".") {
		add(strings.ReplaceAll(specifier, ".", "/"))
	}
	add(specifier)
	return out
}

func groupImportersByTarget(records []importRecord) (map[string][]map[string]any, map[string]struct{}) {
	importedFiles := map[string]struct{}{}
	for _, record := range records {
		if record.Resolved != "" {
			importedFiles[record.Resolved] = struct{}{}
		}
	}

	byTarget := map[string][]map[string]any{}
	for _, record := range records {
		if record.Resolved == "" {
			continue
		}
		_, hasImporters := importedFiles[record.SourceFile]
		byTarget[record.Resolved] = append(byTarget[record.Resolved], map[string]any{
			"file":          record.SourceFile,
			"specifier":     record.Specifier,
			"names":         cloneStringSlice(record.Names),
			"has_importers": hasImporters,
		})
	}

	for target := range byTarget {
		sort.Slice(byTarget[target], func(i, j int) bool {
			left := byTarget[target][i]
			right := byTarget[target][j]
			if stringArg(left, "file", "") != stringArg(right, "file", "") {
				return stringArg(left, "file", "") < stringArg(right, "file", "")
			}
			return stringArg(left, "specifier", "") < stringArg(right, "specifier", "")
		})
	}

	return byTarget, importedFiles
}

func dedupeImportersByFile(importers []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(importers))
	seen := map[string]struct{}{}
	for _, importer := range importers {
		file := stringArg(importer, "file", "")
		if file == "" {
			continue
		}
		if _, ok := seen[file]; ok {
			continue
		}
		seen[file] = struct{}{}
		out = append(out, shallowCopyMap(importer))
	}
	return out
}

func cloneImportersSlice(in []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, item := range in {
		out = append(out, shallowCopyMap(item))
	}
	return out
}

func buildSingleReferenceRows(records []importRecord, identifier string) []map[string]any {
	identifierLower := strings.ToLower(strings.TrimSpace(identifier))
	if identifierLower == "" {
		return []map[string]any{}
	}

	fileMatches := map[string][]map[string]any{}
	seen := map[string]struct{}{}
	for _, record := range records {
		matchType, ok := importRecordMatchType(record, identifierLower)
		if !ok {
			continue
		}

		key := record.SourceFile + "\x00" + record.Specifier
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		fileMatches[record.SourceFile] = append(fileMatches[record.SourceFile], map[string]any{
			"specifier":  record.Specifier,
			"names":      cloneStringSlice(record.Names),
			"match_type": matchType,
		})
	}

	files := make([]string, 0, len(fileMatches))
	for file := range fileMatches {
		files = append(files, file)
	}
	sort.Strings(files)

	rows := make([]map[string]any, 0, len(files))
	for _, file := range files {
		matches := fileMatches[file]
		sort.Slice(matches, func(i, j int) bool {
			return stringArg(matches[i], "specifier", "") < stringArg(matches[j], "specifier", "")
		})
		rows = append(rows, map[string]any{
			"file":    file,
			"matches": cloneReferencesSlice(matches),
		})
	}
	return rows
}

func buildBatchReferenceEntries(records []importRecord, identifier string) []map[string]any {
	identifierLower := strings.ToLower(strings.TrimSpace(identifier))
	if identifierLower == "" {
		return []map[string]any{}
	}

	entriesByFile := map[string]map[string]any{}
	for _, record := range records {
		matchType, ok := importRecordMatchType(record, identifierLower)
		if !ok {
			continue
		}

		existing, exists := entriesByFile[record.SourceFile]
		if !exists {
			entriesByFile[record.SourceFile] = map[string]any{
				"file":       record.SourceFile,
				"specifier":  record.Specifier,
				"match_type": matchType,
			}
			continue
		}
		if stringArg(existing, "match_type", "") == "specifier_stem" && matchType == "named" {
			existing["specifier"] = record.Specifier
			existing["match_type"] = matchType
			entriesByFile[record.SourceFile] = existing
		}
	}

	files := make([]string, 0, len(entriesByFile))
	for file := range entriesByFile {
		files = append(files, file)
	}
	sort.Strings(files)

	out := make([]map[string]any, 0, len(files))
	for _, file := range files {
		out = append(out, shallowCopyMap(entriesByFile[file]))
	}
	return out
}

func importRecordMatchType(record importRecord, identifierLower string) (string, bool) {
	namedMatch := false
	for _, name := range record.Names {
		if strings.EqualFold(strings.TrimSpace(name), identifierLower) {
			namedMatch = true
			break
		}
	}
	stemMatch := importSpecifierStem(record.Specifier) == identifierLower
	if !namedMatch && !stemMatch {
		return "", false
	}
	if namedMatch {
		return "named", true
	}
	return "specifier_stem", true
}

func importSpecifierStem(specifier string) string {
	normalized := strings.TrimSpace(specifier)
	if normalized == "" {
		return ""
	}
	normalized = strings.ReplaceAll(normalized, "\\", "/")
	if !strings.Contains(normalized, "/") && strings.Contains(normalized, ".") {
		normalized = strings.ReplaceAll(normalized, ".", "/")
	}
	stem := path.Base(normalized)
	ext := path.Ext(stem)
	if ext != "" {
		stem = strings.TrimSuffix(stem, ext)
	}
	return strings.ToLower(strings.TrimSpace(stem))
}

func buildCheckReferenceEntry(
	index storage.RepoIndex,
	records []importRecord,
	identifier string,
	searchContent bool,
	maxContentResults int,
) map[string]any {
	importReferences := buildSingleReferenceRows(records, identifier)
	importCount := len(importReferences)
	contentReferences := []map[string]any{}

	if searchContent {
		definingFiles := collectDefiningFilesForIdentifier(index.Symbols, identifier)
		contentReferences = searchIdentifierContentReferences(index, identifier, definingFiles, maxContentResults)
	}

	contentCount := len(contentReferences)
	result := map[string]any{
		"is_referenced":     importCount > 0 || contentCount > 0,
		"import_count":      importCount,
		"import_references": cloneReferencesSlice(importReferences),
		"content_count":     contentCount,
	}
	if searchContent {
		result["content_references"] = cloneReferencesSlice(contentReferences)
	}
	return result
}

func collectDefiningFilesForIdentifier(symbols map[string]any, identifier string) map[string]struct{} {
	definingFiles := map[string]struct{}{}
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return definingFiles
	}

	for _, candidate := range flattenSearchSymbolCandidates(symbols) {
		if strings.EqualFold(candidate.Name, identifier) {
			definingFiles[candidate.File] = struct{}{}
		}
	}
	return definingFiles
}

func searchIdentifierContentReferences(
	index storage.RepoIndex,
	identifier string,
	definingFiles map[string]struct{},
	maxResults int,
) []map[string]any {
	identifierLower := strings.ToLower(strings.TrimSpace(identifier))
	if identifierLower == "" {
		return []map[string]any{}
	}

	sourceRoot := filepath.Clean(strings.TrimSpace(index.SourceRoot))
	if sourceRoot == "" {
		return []map[string]any{}
	}

	files := sortedIndexedFiles(index.Files)
	results := make([]map[string]any, 0, minInt(len(files), maxResults))
	for _, filePath := range files {
		if _, skip := definingFiles[filePath]; skip {
			continue
		}

		absolutePath := filepath.Clean(filepath.Join(sourceRoot, filepath.FromSlash(filePath)))
		if !pathWithin(sourceRoot, absolutePath) {
			continue
		}
		contentBytes, err := os.ReadFile(absolutePath)
		if err != nil {
			continue
		}

		lines := splitContentLines(string(contentBytes))
		matches := make([]map[string]any, 0, 4)
		for lineIndex, line := range lines {
			if !strings.Contains(strings.ToLower(line), identifierLower) {
				continue
			}
			matches = append(matches, map[string]any{
				"line": lineIndex + 1,
				"text": trimAndClampSearchTextLine(line),
			})
		}
		if len(matches) == 0 {
			continue
		}

		results = append(results, map[string]any{
			"file":    filePath,
			"matches": matches,
		})
		if len(results) >= maxResults {
			break
		}
	}
	return results
}

func cloneReferencesSlice(in []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, item := range in {
		out = append(out, shallowCopyMap(item))
	}
	return out
}

func cloneStringSlice(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func buildSymbolSourceEntry(index storage.RepoIndex, symbolID string, contextLines int, verify bool) (map[string]any, bool) {
	symbol, found := findIndexedSymbol(index.Symbols, symbolID)
	if !found {
		return nil, false
	}

	entry := map[string]any{
		"id":           symbolID,
		"kind":         strings.TrimSpace(stringArg(symbol, "kind", "")),
		"name":         strings.TrimSpace(stringArg(symbol, "name", "")),
		"file":         normalizeRepoFilePath(stringArg(symbol, "file", "")),
		"line":         intArg(symbol, "line"),
		"end_line":     intArg(symbol, "end_line"),
		"signature":    stringArg(symbol, "signature", ""),
		"decorators":   normalizeDecorators(symbol["decorators"]),
		"docstring":    stringArg(symbol, "docstring", ""),
		"content_hash": stringArg(symbol, "content_hash", ""),
		"source":       "",
	}

	source, contextBefore, contextAfter := symbolSourceFromIndex(index, entry, contextLines)
	entry["source"] = source
	if contextBefore != "" {
		entry["context_before"] = contextBefore
	}
	if contextAfter != "" {
		entry["context_after"] = contextAfter
	}
	if verify && source != "" {
		sum := sha256.Sum256([]byte(source))
		actualHash := hex.EncodeToString(sum[:])
		storedHash := stringArg(entry, "content_hash", "")
		if storedHash != "" {
			entry["content_verified"] = actualHash == storedHash
		} else {
			entry["content_verified"] = nil
		}
	}

	return entry, true
}

func buildContextBundleEntry(
	index storage.RepoIndex,
	symbol map[string]any,
	includeCallers bool,
	callersByFile map[string][]string,
	contentCache *sync.Map,
	importCache *sync.Map,
) contextBundleSymbolEntry {
	filePath := normalizeRepoFilePath(stringArg(symbol, "file", ""))
	source, _, _ := symbolSourceFromIndex(index, symbol, 0)

	language := strings.ToLower(strings.TrimSpace(stringArg(symbol, "language", "")))
	if language == "" {
		if detected, ok := classifyLanguage(filePath); ok {
			language = detected
		}
	}
	imports := loadContextBundleImports(index, filePath, language, contentCache, importCache)

	entry := contextBundleSymbolEntry{
		SymbolID:  strings.TrimSpace(stringArg(symbol, "id", "")),
		Name:      strings.TrimSpace(stringArg(symbol, "name", "")),
		Kind:      strings.TrimSpace(stringArg(symbol, "kind", "")),
		File:      filePath,
		Line:      intArg(symbol, "line"),
		EndLine:   intArg(symbol, "end_line"),
		Signature: stringArg(symbol, "signature", ""),
		Docstring: stringArg(symbol, "docstring", ""),
		Language:  language,
		Source:    source,
		Imports:   imports,
	}
	if includeCallers {
		entry.Callers = cloneStringSlice(callersByFile[filePath])
	}
	return entry
}

func loadContextBundleImports(
	index storage.RepoIndex,
	filePath, language string,
	contentCache *sync.Map,
	importCache *sync.Map,
) []string {
	if importCache != nil {
		if cached, ok := importCache.Load(filePath); ok {
			if imports, ok := cached.([]string); ok {
				return cloneStringSlice(imports)
			}
		}
	}

	content := ""
	contentLoaded := false
	if contentCache != nil {
		if cached, ok := contentCache.Load(filePath); ok {
			if text, ok := cached.(string); ok {
				content = text
				contentLoaded = true
			}
		}
	}
	if !contentLoaded {
		if loaded, ok := readIndexedFileContent(index, filePath); ok {
			content = loaded
		}
		if contentCache != nil {
			contentCache.Store(filePath, content)
		}
	}

	imports := extractContextBundleImports(content, language)
	if importCache != nil {
		if existing, loaded := importCache.LoadOrStore(filePath, imports); loaded {
			if cachedImports, ok := existing.([]string); ok {
				return cloneStringSlice(cachedImports)
			}
		}
	}
	return cloneStringSlice(imports)
}

func buildSingleOutline(repo string, index storage.RepoIndex, inputPath string, started time.Time) map[string]any {
	filePath := normalizeRepoFilePath(inputPath)
	if _, ok := index.Files[filePath]; !ok {
		return map[string]any{
			"repo":         repo,
			"file":         filePath,
			"language":     "",
			"file_summary": "",
			"symbols":      []any{},
		}
	}

	language, _ := classifyLanguage(filePath)
	return map[string]any{
		"repo":         repo,
		"file":         filePath,
		"language":     language,
		"file_summary": "",
		"symbols":      []any{},
		"_meta": map[string]any{
			"timing_ms":          roundMilliseconds(time.Since(started)),
			"symbol_count":       0,
			"tokens_saved":       0,
			"total_tokens_saved": 0,
			"tip":                "Tip: use file_paths=[...] to query multiple files in one call.",
		},
	}
}

func filterIndexedFiles(files map[string]string, pathPrefix string) []string {
	out := make([]string, 0, len(files))
	for file := range files {
		if strings.HasPrefix(file, pathPrefix) {
			out = append(out, file)
		}
	}
	sort.Strings(out)
	return out
}

func sortedIndexedFiles(files map[string]string) []string {
	out := make([]string, 0, len(files))
	for file := range files {
		out = append(out, file)
	}
	sort.Strings(out)
	return out
}

func matchesSearchTextPattern(filePath, filePattern string) bool {
	if filePattern == "" {
		return true
	}

	if matched, err := path.Match(filePattern, filePath); err == nil && matched {
		return true
	}
	if matched, err := path.Match("*/"+filePattern, filePath); err == nil && matched {
		return true
	}
	return false
}

func flattenSearchSymbolCandidates(rawSymbols map[string]any) []searchSymbolCandidate {
	if len(rawSymbols) == 0 {
		return []searchSymbolCandidate{}
	}

	keys := make([]string, 0, len(rawSymbols))
	for key := range rawSymbols {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]searchSymbolCandidate, 0, len(rawSymbols))
	seenIDs := map[string]struct{}{}

	appendCandidate := func(raw map[string]any, fallbackID string) {
		candidate, ok := toSearchSymbolCandidate(raw, fallbackID)
		if !ok {
			return
		}
		if _, exists := seenIDs[candidate.ID]; exists {
			return
		}
		seenIDs[candidate.ID] = struct{}{}
		out = append(out, candidate)
	}

	for _, key := range keys {
		switch typed := rawSymbols[key].(type) {
		case map[string]any:
			appendCandidate(typed, key)
		case []any:
			for index, item := range typed {
				symbol, ok := item.(map[string]any)
				if !ok {
					continue
				}
				appendCandidate(symbol, fmt.Sprintf("%s#%d", key, index))
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].ID < out[j].ID
	})

	return out
}

func toSearchSymbolCandidate(raw map[string]any, fallbackID string) (searchSymbolCandidate, bool) {
	id := strings.TrimSpace(stringArg(raw, "id", ""))
	if id == "" {
		id = strings.TrimSpace(fallbackID)
	}
	name := strings.TrimSpace(stringArg(raw, "name", ""))
	file := normalizeRepoFilePath(stringArg(raw, "file", ""))
	if id == "" || name == "" || file == "" {
		return searchSymbolCandidate{}, false
	}

	line := intArg(raw, "line")
	if line < 0 {
		line = 0
	}
	endLine := intArg(raw, "end_line")
	if endLine < line {
		endLine = line
	}

	language := strings.ToLower(strings.TrimSpace(stringArg(raw, "language", "")))
	if language == "" {
		if detected, ok := classifyLanguage(file); ok {
			language = detected
		}
	}

	byteLen := intArg(raw, "byte_length")
	if byteLen <= 0 {
		byteLen = len(name) +
			len(strings.TrimSpace(stringArg(raw, "signature", ""))) +
			len(strings.TrimSpace(stringArg(raw, "summary", ""))) +
			len(strings.TrimSpace(stringArg(raw, "docstring", "")))
	}
	if byteLen < searchSymbolsMinByteLength {
		byteLen = searchSymbolsMinByteLength
	}

	return searchSymbolCandidate{
		ID:        id,
		Name:      name,
		Kind:      strings.TrimSpace(stringArg(raw, "kind", "")),
		File:      file,
		Language:  language,
		Line:      line,
		EndLine:   endLine,
		Signature: strings.TrimSpace(stringArg(raw, "signature", "")),
		Summary:   strings.TrimSpace(stringArg(raw, "summary", "")),
		Docstring: strings.TrimSpace(stringArg(raw, "docstring", "")),
		Keywords:  normalizeRawStringSlice(raw["keywords"]),
		ByteLen:   byteLen,
	}, true
}

func normalizeRawStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			trimmed := strings.TrimSpace(item)
			if trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text := strings.TrimSpace(fmt.Sprintf("%v", item))
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return []string{}
	}
}

func computeSearchSymbolCentrality(candidates []searchSymbolCandidate) map[string]float64 {
	counts := map[string]int{}
	for _, candidate := range candidates {
		if candidate.File == "" {
			continue
		}
		counts[candidate.File]++
	}

	centrality := map[string]float64{}
	for file, count := range counts {
		if count <= 1 {
			continue
		}
		centrality[file] = math.Log(float64(count)) * 0.3
	}
	return centrality
}

func scoreSearchSymbolCandidate(
	candidate searchSymbolCandidate,
	queryTerms []string,
	centrality map[string]float64,
) (float64, float64, map[string]any) {
	terms := uniqueSearchTerms(queryTerms)
	nameTokens := tokenizeSearchSymbolText(candidate.Name)
	keywordTokens := []string{}
	for _, keyword := range candidate.Keywords {
		keywordTokens = append(keywordTokens, tokenizeSearchSymbolText(keyword)...)
	}
	signatureTokens := tokenizeSearchSymbolText(candidate.Signature)
	summaryTokens := tokenizeSearchSymbolText(candidate.Summary)
	docstringTokens := tokenizeSearchSymbolText(candidate.Docstring)

	nameScore := 0.0
	keywordScore := 0.0
	signatureScore := 0.0
	summaryScore := 0.0
	docstringScore := 0.0
	for _, term := range terms {
		nameScore += float64(tokenFrequency(nameTokens, term) * 3)
		keywordScore += float64(tokenFrequency(keywordTokens, term) * 2)
		signatureScore += float64(tokenFrequency(signatureTokens, term) * 2)
		summaryScore += float64(tokenFrequency(summaryTokens, term))
		docstringScore += float64(tokenFrequency(docstringTokens, term))
	}

	nameExactBonus := 0.0
	queryJoined := strings.TrimSpace(strings.ToLower(strings.Join(queryTerms, " ")))
	if queryJoined != "" && strings.ToLower(candidate.Name) == queryJoined {
		nameExactBonus = 50.0
	}

	lexical := nameScore + keywordScore + signatureScore + summaryScore + docstringScore + nameExactBonus
	centralityBonus := centrality[candidate.File]
	total := lexical + centralityBonus

	return total, lexical, map[string]any{
		"name":             roundSearchSymbolScore(nameScore),
		"keywords":         roundSearchSymbolScore(keywordScore),
		"signature":        roundSearchSymbolScore(signatureScore),
		"summary":          roundSearchSymbolScore(summaryScore),
		"docstring":        roundSearchSymbolScore(docstringScore),
		"name_exact_bonus": roundSearchSymbolScore(nameExactBonus),
		"centrality_bonus": roundSearchSymbolScore(centralityBonus),
	}
}

func uniqueSearchTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	seen := map[string]struct{}{}
	for _, term := range terms {
		normalized := strings.ToLower(strings.TrimSpace(term))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func tokenFrequency(tokens []string, term string) int {
	count := 0
	for _, token := range tokens {
		if token == term {
			count++
		}
	}
	return count
}

func tokenizeSearchSymbolText(text string) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return []string{}
	}

	expanded := searchSymbolCamelBoundaryPattern.ReplaceAllString(trimmed, "${1}_${2}")
	matches := searchSymbolTokenPattern.FindAllString(expanded, -1)
	out := make([]string, 0, len(matches))
	for _, token := range matches {
		out = append(out, strings.ToLower(token))
	}
	return out
}

func roundSearchSymbolScore(score float64) float64 {
	return math.Round(score*1000) / 1000
}

func trimAndClampSearchTextLine(line string) string {
	trimmed := strings.TrimRightFunc(line, unicode.IsSpace)
	runes := []rune(trimmed)
	if len(runes) <= searchTextMaxLineLength {
		return trimmed
	}
	return string(runes[:searchTextMaxLineLength])
}

func parseSearchTextRetrievalMode(arguments map[string]any) (searchTextRetrievalMode, error) {
	raw := strings.TrimSpace(stringArg(arguments, "retrieval_mode", ""))
	if raw == "" {
		raw = strings.TrimSpace(stringArg(arguments, "mode", ""))
	}
	if raw == "" {
		return searchTextRetrievalModeLexical, nil
	}

	switch normalized := strings.ToLower(raw); normalized {
	case string(searchTextRetrievalModeLexical):
		return searchTextRetrievalModeLexical, nil
	case string(searchTextRetrievalModeSemantic):
		return searchTextRetrievalModeSemantic, nil
	case string(searchTextRetrievalModeHybrid):
		return searchTextRetrievalModeHybrid, nil
	default:
		return "", fmt.Errorf(
			`Unsupported retrieval_mode %q. Use one of: "lexical", "semantic", "hybrid".`,
			raw,
		)
	}
}

func collectLexicalSearchTextCandidates(
	filteredFiles []string,
	sourceRoot string,
	queryLower string,
	pattern *regexp.Regexp,
	contextLines int,
	limit int,
) ([]searchTextMatchCandidate, int, bool) {
	if limit <= 0 {
		return []searchTextMatchCandidate{}, 0, false
	}

	candidates := make([]searchTextMatchCandidate, 0, limit)
	filesSearched := 0
	truncated := false
	for _, filePath := range filteredFiles {
		if sourceRoot == "" {
			continue
		}

		absoluteFile := filepath.Clean(filepath.Join(sourceRoot, filepath.FromSlash(filePath)))
		if !pathWithin(sourceRoot, absoluteFile) {
			continue
		}

		contentBytes, readErr := os.ReadFile(absoluteFile)
		if readErr != nil {
			continue
		}
		lines := splitContentLines(string(contentBytes))
		filesSearched++

		for lineIndex, line := range lines {
			matched := false
			if pattern != nil {
				matched = pattern.MatchString(line)
			} else {
				matched = strings.Contains(strings.ToLower(line), queryLower)
			}
			if !matched {
				continue
			}

			before, after := []string{}, []string{}
			if contextLines > 0 {
				before, after = buildSearchTextContextFromLines(lines, lineIndex+1, contextLines)
			}

			candidates = append(candidates, searchTextMatchCandidate{
				File:         filePath,
				Line:         lineIndex + 1,
				Text:         trimAndClampSearchTextLine(line),
				Before:       before,
				After:        after,
				LexicalScore: lexicalSearchTextScore(line, queryLower, pattern),
			})
			if len(candidates) >= limit {
				truncated = true
				break
			}
		}
		if truncated {
			break
		}
	}

	return candidates, filesSearched, truncated
}

func lexicalSearchTextScore(line, queryLower string, pattern *regexp.Regexp) float64 {
	if pattern != nil {
		matchCount := len(pattern.FindAllStringIndex(line, -1))
		if matchCount <= 0 {
			return 1
		}
		return float64(matchCount)
	}

	if queryLower == "" {
		return 1
	}

	matchCount := strings.Count(strings.ToLower(line), queryLower)
	if matchCount <= 0 {
		return 1
	}
	return float64(matchCount)
}

func collectSemanticSearchTextCandidates(
	ctx context.Context,
	embedder indexing.Embedder,
	vectorBackend indexing.VectorBackend,
	namespace string,
	query string,
	filePattern string,
	contextLines int,
	topK int,
	sourceRoot string,
) ([]searchTextMatchCandidate, error) {
	if topK <= 0 {
		return []searchTextMatchCandidate{}, nil
	}

	embeddings, err := embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("semantic retrieval: embed query: %w", err)
	}
	if len(embeddings) != 1 {
		return nil, fmt.Errorf("semantic retrieval: expected one query embedding, got %d", len(embeddings))
	}

	response, err := vectorBackend.Query(ctx, indexing.VectorQueryRequest{
		Namespace: namespace,
		Embedding: embeddings[0],
		TopK:      topK,
	})
	if err != nil {
		return nil, fmt.Errorf("semantic retrieval: query vectors for %s: %w", namespace, err)
	}

	linesCache := map[string][]string{}
	candidates := make([]searchTextMatchCandidate, 0, len(response.Matches))
	for _, match := range response.Matches {
		filePath := normalizeRepoFilePath(match.Record.Metadata.Path)
		if filePath == "" {
			continue
		}
		if !matchesSearchTextPattern(filePath, filePattern) {
			continue
		}

		line := match.Record.Metadata.StartLine
		if line <= 0 {
			line = 1
		}

		text := firstSearchTextSnippet(match.Record.Metadata.ChunkText)
		before, after := []string{}, []string{}
		if lines, ok := loadSearchTextFileLines(sourceRoot, filePath, linesCache); ok {
			if contextLines > 0 {
				before, after = buildSearchTextContextFromLines(lines, line, contextLines)
			}
			if text == "" && line >= 1 && line <= len(lines) {
				text = trimAndClampSearchTextLine(lines[line-1])
			}
		}

		candidates = append(candidates, searchTextMatchCandidate{
			File:          filePath,
			Line:          line,
			Text:          text,
			Before:        before,
			After:         after,
			SemanticScore: match.Score,
		})
	}

	return candidates, nil
}

func firstSearchTextSnippet(raw string) string {
	lines := splitContentLines(raw)
	for _, line := range lines {
		trimmed := trimAndClampSearchTextLine(line)
		if strings.TrimSpace(trimmed) != "" {
			return trimmed
		}
	}
	return ""
}

func loadSearchTextFileLines(
	sourceRoot string,
	filePath string,
	cache map[string][]string,
) ([]string, bool) {
	if sourceRoot == "" {
		return nil, false
	}

	if cached, ok := cache[filePath]; ok {
		return cached, true
	}

	absoluteFile := filepath.Clean(filepath.Join(sourceRoot, filepath.FromSlash(filePath)))
	if !pathWithin(sourceRoot, absoluteFile) {
		return nil, false
	}

	contentBytes, err := os.ReadFile(absoluteFile)
	if err != nil {
		return nil, false
	}

	lines := splitContentLines(string(contentBytes))
	cache[filePath] = lines
	return lines, true
}

func buildSearchTextContextFromLines(
	lines []string,
	line int,
	contextLines int,
) ([]string, []string) {
	if contextLines <= 0 || len(lines) == 0 {
		return []string{}, []string{}
	}

	actualLine := clampInt(line, 1, len(lines))
	lineIndex := actualLine - 1

	beforeStart := lineIndex - contextLines
	if beforeStart < 0 {
		beforeStart = 0
	}
	afterEnd := lineIndex + contextLines + 1
	if afterEnd > len(lines) {
		afterEnd = len(lines)
	}

	before := make([]string, 0, lineIndex-beforeStart)
	for _, item := range lines[beforeStart:lineIndex] {
		before = append(before, trimAndClampSearchTextLine(item))
	}

	after := make([]string, 0, afterEnd-lineIndex-1)
	for _, item := range lines[lineIndex+1 : afterEnd] {
		after = append(after, trimAndClampSearchTextLine(item))
	}

	return before, after
}

func (s *Service) resolveSearchTextHybridWeights(arguments map[string]any) (float64, float64) {
	lexicalWeight := s.cfg.VectorLexicalWeight
	semanticWeight := s.cfg.VectorSemanticWeight

	if override, ok := optionalFloatArg(arguments, "lexical_weight"); ok && override >= 0 {
		lexicalWeight = override
	}
	if override, ok := optionalFloatArg(arguments, "semantic_weight"); ok && override >= 0 {
		semanticWeight = override
	}

	if lexicalWeight == 0 && semanticWeight == 0 {
		lexicalWeight = 0.5
		semanticWeight = 0.5
	}

	total := lexicalWeight + semanticWeight
	if total <= 0 {
		return 0.5, 0.5
	}
	return lexicalWeight / total, semanticWeight / total
}

func rankHybridSearchTextCandidates(
	lexical []searchTextMatchCandidate,
	semantic []searchTextMatchCandidate,
	lexicalWeight float64,
	semanticWeight float64,
) []searchTextMatchCandidate {
	merged := map[string]searchTextMatchCandidate{}
	lexicalScores := map[string]float64{}
	semanticScores := map[string]float64{}

	for _, candidate := range lexical {
		key := searchTextCandidateKey(candidate)
		existing, ok := merged[key]
		if !ok {
			existing = candidate
		}
		if candidate.LexicalScore > lexicalScores[key] {
			lexicalScores[key] = candidate.LexicalScore
			existing.LexicalScore = candidate.LexicalScore
		}
		if existing.Text == "" {
			existing.Text = candidate.Text
		}
		if len(existing.Before) == 0 && len(candidate.Before) > 0 {
			existing.Before = append([]string(nil), candidate.Before...)
		}
		if len(existing.After) == 0 && len(candidate.After) > 0 {
			existing.After = append([]string(nil), candidate.After...)
		}
		merged[key] = existing
	}

	for _, candidate := range semantic {
		key := searchTextCandidateKey(candidate)
		existing, ok := merged[key]
		if !ok {
			existing = candidate
		}
		if candidate.SemanticScore > semanticScores[key] {
			semanticScores[key] = candidate.SemanticScore
			existing.SemanticScore = candidate.SemanticScore
		}
		if existing.Text == "" {
			existing.Text = candidate.Text
		}
		if len(existing.Before) == 0 && len(candidate.Before) > 0 {
			existing.Before = append([]string(nil), candidate.Before...)
		}
		if len(existing.After) == 0 && len(candidate.After) > 0 {
			existing.After = append([]string(nil), candidate.After...)
		}
		merged[key] = existing
	}

	normalizedLexical := normalizeSearchTextScores(lexicalScores)
	normalizedSemantic := normalizeSearchTextScores(semanticScores)

	out := make([]searchTextMatchCandidate, 0, len(merged))
	for key, candidate := range merged {
		candidate.LexicalScore = normalizedLexical[key]
		candidate.SemanticScore = normalizedSemantic[key]
		candidate.HybridScore = lexicalWeight*candidate.LexicalScore + semanticWeight*candidate.SemanticScore
		out = append(out, candidate)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].HybridScore != out[j].HybridScore {
			return out[i].HybridScore > out[j].HybridScore
		}
		if lexicalWeight > 0 && out[i].LexicalScore != out[j].LexicalScore {
			return out[i].LexicalScore > out[j].LexicalScore
		}
		if semanticWeight > 0 && out[i].SemanticScore != out[j].SemanticScore {
			return out[i].SemanticScore > out[j].SemanticScore
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Text < out[j].Text
	})

	return out
}

func searchTextCandidateKey(candidate searchTextMatchCandidate) string {
	return candidate.File + "\x00" + strconv.Itoa(candidate.Line)
}

func normalizeSearchTextScores(scores map[string]float64) map[string]float64 {
	if len(scores) == 0 {
		return map[string]float64{}
	}

	minScore := math.MaxFloat64
	maxScore := -math.MaxFloat64
	for _, score := range scores {
		if score < minScore {
			minScore = score
		}
		if score > maxScore {
			maxScore = score
		}
	}

	normalized := make(map[string]float64, len(scores))
	if maxScore == minScore {
		for key := range scores {
			normalized[key] = 1
		}
		return normalized
	}

	denominator := maxScore - minScore
	for key, score := range scores {
		normalized[key] = (score - minScore) / denominator
	}
	return normalized
}

func buildSearchTextGroupedResults(
	candidates []searchTextMatchCandidate,
	contextLines int,
	mode searchTextRetrievalMode,
) []map[string]any {
	results := make([]map[string]any, 0, len(candidates))
	if len(candidates) == 0 {
		return results
	}

	order := make([]string, 0, len(candidates))
	grouped := map[string][]map[string]any{}
	for _, candidate := range candidates {
		if _, seen := grouped[candidate.File]; !seen {
			order = append(order, candidate.File)
		}

		match := map[string]any{
			"line": candidate.Line,
			"text": candidate.Text,
		}
		if contextLines > 0 {
			match["before"] = append([]string(nil), candidate.Before...)
			match["after"] = append([]string(nil), candidate.After...)
		}

		switch mode {
		case searchTextRetrievalModeSemantic:
			match["score"] = candidate.SemanticScore
		case searchTextRetrievalModeHybrid:
			match["score"] = candidate.HybridScore
			match["lexical_score"] = candidate.LexicalScore
			match["vector_score"] = candidate.SemanticScore
		}

		grouped[candidate.File] = append(grouped[candidate.File], match)
	}

	for _, filePath := range order {
		results = append(results, map[string]any{
			"file":    filePath,
			"matches": grouped[filePath],
		})
	}

	return results
}

func buildFileTree(files []string, pathPrefix string, includeSummaries bool) []map[string]any {
	root := map[string]any{}

	for _, filePath := range files {
		relative := strings.TrimPrefix(filePath, pathPrefix)
		relative = strings.TrimPrefix(relative, "/")
		parts := strings.Split(relative, "/")

		current := root
		for i, part := range parts {
			if part == "" {
				continue
			}

			isLast := i == len(parts)-1
			if isLast {
				language, _ := classifyLanguage(filePath)
				node := map[string]any{
					"path":         filePath,
					"type":         "file",
					"language":     language,
					"symbol_count": 0,
				}
				if includeSummaries {
					node["summary"] = ""
				}
				current[part] = node
				continue
			}

			existing, ok := current[part].(map[string]any)
			if !ok || stringArg(existing, "type", "") != "dir" {
				existing = map[string]any{
					"type":     "dir",
					"children": map[string]any{},
				}
				current[part] = existing
			}
			children, _ := existing["children"].(map[string]any)
			if children == nil {
				children = map[string]any{}
				existing["children"] = children
			}
			current = children
		}
	}

	return treeDictToList(root)
}

func treeDictToList(nodeDict map[string]any) []map[string]any {
	keys := make([]string, 0, len(nodeDict))
	for key := range nodeDict {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]map[string]any, 0, len(nodeDict))
	for _, key := range keys {
		node, ok := nodeDict[key].(map[string]any)
		if !ok {
			continue
		}

		if stringArg(node, "type", "") == "file" {
			result = append(result, node)
			continue
		}

		children, _ := node["children"].(map[string]any)
		result = append(result, map[string]any{
			"path":     key + "/",
			"type":     "dir",
			"children": treeDictToList(children),
		})
	}
	return result
}

func normalizeRepoFilePath(filePath string) string {
	trimmed := strings.TrimSpace(filePath)
	if trimmed == "" {
		return ""
	}
	replaced := strings.ReplaceAll(trimmed, "\\", "/")
	cleaned := path.Clean(replaced)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func optionalStringArg(arguments map[string]any, key string) (string, bool) {
	value, ok := arguments[key]
	if !ok || value == nil {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return text, true
}

func stringArg(arguments map[string]any, key, fallback string) string {
	value, ok := arguments[key]
	if !ok || value == nil {
		return fallback
	}
	text, ok := value.(string)
	if !ok {
		return fallback
	}
	return text
}

func optionalStringSliceArg(arguments map[string]any, key string) ([]string, bool) {
	value, ok := arguments[key]
	if !ok || value == nil {
		return nil, false
	}

	switch typed := value.(type) {
	case []string:
		out := make([]string, len(typed))
		for i, item := range typed {
			out[i] = normalizeRepoFilePath(item)
		}
		return out, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			switch text := item.(type) {
			case string:
				out = append(out, normalizeRepoFilePath(text))
			default:
				out = append(out, normalizeRepoFilePath(fmt.Sprintf("%v", item)))
			}
		}
		return out, true
	default:
		return nil, false
	}
}

func optionalRawStringSliceArg(arguments map[string]any, key string) ([]string, bool) {
	value, ok := arguments[key]
	if !ok || value == nil {
		return nil, false
	}

	switch typed := value.(type) {
	case []string:
		out := make([]string, len(typed))
		for i, item := range typed {
			out[i] = strings.TrimSpace(item)
		}
		return out, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			switch text := item.(type) {
			case string:
				out = append(out, strings.TrimSpace(text))
			default:
				out = append(out, strings.TrimSpace(fmt.Sprintf("%v", item)))
			}
		}
		return out, true
	default:
		return nil, false
	}
}

func resolveRepoArgument(ctx context.Context, store storage.IndexStore, repo string) (string, bool, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", false, nil
	}
	if strings.Contains(repo, "/") {
		return repo, true, nil
	}

	repos, err := store.List(ctx)
	if err != nil {
		return "", false, err
	}

	candidates := make([]string, 0, 4)
	for _, entry := range repos {
		_, repoName, ok := strings.Cut(entry.Repo, "/")
		if !ok {
			continue
		}
		if repoName == repo || entry.DisplayName == repo {
			candidates = append(candidates, entry.Repo)
		}
	}
	if len(candidates) == 0 {
		return "", false, nil
	}

	sort.Strings(candidates)
	if len(candidates) > 1 {
		return "", false, fmt.Errorf("Ambiguous repository name: %s. Use one of: %s", repo, strings.Join(candidates, ", "))
	}
	return candidates[0], true, nil
}

func findIndexedSymbol(symbols map[string]any, symbolID string) (map[string]any, bool) {
	if symbols == nil {
		return nil, false
	}

	if direct, ok := symbols[symbolID].(map[string]any); ok {
		symbol := shallowCopyMap(direct)
		if stringArg(symbol, "id", "") == "" {
			symbol["id"] = symbolID
		}
		return symbol, true
	}

	for _, raw := range symbols {
		switch typed := raw.(type) {
		case map[string]any:
			if strings.TrimSpace(stringArg(typed, "id", "")) == symbolID {
				return shallowCopyMap(typed), true
			}
		case []any:
			for _, item := range typed {
				candidate, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if strings.TrimSpace(stringArg(candidate, "id", "")) == symbolID {
					return shallowCopyMap(candidate), true
				}
			}
		}
	}
	return nil, false
}

func normalizeDecorators(value any) []any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, len(typed))
		copy(out, typed)
		return out
	case []string:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
	default:
		return []any{}
	}
}

func intArg(arguments map[string]any, key string) int {
	value, ok := arguments[key]
	if !ok || value == nil {
		return 0
	}

	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func symbolSourceFromIndex(index storage.RepoIndex, symbol map[string]any, contextLines int) (string, string, string) {
	sourceRoot := strings.TrimSpace(index.SourceRoot)
	if sourceRoot == "" {
		return "", "", ""
	}

	filePath := normalizeRepoFilePath(stringArg(symbol, "file", ""))
	line := intArg(symbol, "line")
	endLine := intArg(symbol, "end_line")
	if filePath == "" || line <= 0 {
		return "", "", ""
	}

	sourceRoot = filepath.Clean(sourceRoot)
	absoluteFile := filepath.Clean(filepath.Join(sourceRoot, filepath.FromSlash(filePath)))
	if !pathWithin(sourceRoot, absoluteFile) {
		return "", "", ""
	}

	contentBytes, err := os.ReadFile(absoluteFile)
	if err != nil {
		return "", "", ""
	}
	lines := splitContentLines(string(contentBytes))
	if len(lines) == 0 || line > len(lines) {
		return "", "", ""
	}

	actualStart := clampInt(line, 1, len(lines))
	if endLine < actualStart {
		endLine = actualStart
	}
	actualEnd := clampInt(endLine, actualStart, len(lines))

	source := strings.Join(lines[actualStart-1:actualEnd], "\n")
	if contextLines <= 0 {
		return source, "", ""
	}

	beforeStart := actualStart - contextLines
	if beforeStart < 1 {
		beforeStart = 1
	}
	afterEnd := actualEnd + contextLines
	if afterEnd > len(lines) {
		afterEnd = len(lines)
	}

	contextBefore := ""
	contextAfter := ""
	if beforeStart < actualStart {
		contextBefore = strings.Join(lines[beforeStart-1:actualStart-1], "\n")
	}
	if actualEnd < afterEnd {
		contextAfter = strings.Join(lines[actualEnd:afterEnd], "\n")
	}
	return source, contextBefore, contextAfter
}

func shallowCopyMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func optionalIntArg(arguments map[string]any, key string) (int, bool) {
	value, ok := arguments[key]
	if !ok || value == nil {
		return 0, false
	}

	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func optionalFloatArg(arguments map[string]any, key string) (float64, bool) {
	value, ok := arguments[key]
	if !ok || value == nil {
		return 0, false
	}

	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func splitContentLines(content string) []string {
	if content == "" {
		return []string{}
	}

	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func zeroCostAvoidedMap() map[string]any {
	return map[string]any{
		"claude_opus_4_6":   0.0,
		"claude_sonnet_4_6": 0.0,
		"claude_haiku_4_5":  0.0,
		"gpt5_latest":       0.0,
	}
}

func buildDependencyAdjacency(records []importRecord) map[string][]string {
	adjacency := map[string][]string{}
	seen := map[string]map[string]struct{}{}
	for _, record := range records {
		if record.SourceFile == "" || record.Resolved == "" || record.SourceFile == record.Resolved {
			continue
		}

		rowSeen, ok := seen[record.SourceFile]
		if !ok {
			rowSeen = map[string]struct{}{}
			seen[record.SourceFile] = rowSeen
		}
		if _, exists := rowSeen[record.Resolved]; exists {
			continue
		}
		rowSeen[record.Resolved] = struct{}{}
		adjacency[record.SourceFile] = append(adjacency[record.SourceFile], record.Resolved)
	}
	return adjacency
}

func invertDependencyAdjacency(adjacency map[string][]string) map[string][]string {
	inverted := map[string][]string{}
	seen := map[string]map[string]struct{}{}
	for source, targets := range adjacency {
		for _, target := range targets {
			rowSeen, ok := seen[target]
			if !ok {
				rowSeen = map[string]struct{}{}
				seen[target] = rowSeen
			}
			if _, exists := rowSeen[source]; exists {
				continue
			}
			rowSeen[source] = struct{}{}
			inverted[target] = append(inverted[target], source)
		}
	}
	return inverted
}

func bfsDependencyGraph(
	start string,
	adjacency map[string][]string,
	depth int,
) (map[string]struct{}, []dependencyGraphEdge) {
	nodes := map[string]struct{}{start: {}}
	edges := make([]dependencyGraphEdge, 0, 8)
	visited := map[string]int{start: 0}

	queue := []string{start}
	levels := []int{0}
	for len(queue) > 0 {
		current := queue[0]
		level := levels[0]
		queue = queue[1:]
		levels = levels[1:]
		if level >= depth {
			continue
		}

		for _, neighbor := range adjacency[current] {
			nodes[neighbor] = struct{}{}
			edges = append(edges, dependencyGraphEdge{From: current, To: neighbor})
			if _, seen := visited[neighbor]; seen {
				continue
			}
			visited[neighbor] = level + 1
			queue = append(queue, neighbor)
			levels = append(levels, level+1)
		}
	}

	return nodes, edges
}

func mergeStringSet(dst, src map[string]struct{}) {
	for item := range src {
		dst[item] = struct{}{}
	}
}

func dedupeDependencyEdges(edges []dependencyGraphEdge) []dependencyGraphEdge {
	out := make([]dependencyGraphEdge, 0, len(edges))
	seen := map[string]struct{}{}
	for _, edge := range edges {
		key := edge.From + "\x00" + edge.To
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, edge)
	}
	return out
}

func filterDependencyNeighbors(in []string, allowed map[string]struct{}) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		if _, exists := allowed[item]; !exists {
			continue
		}
		if _, dup := seen[item]; dup {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func flattenSymbolsForDiff(rawSymbols map[string]any) []symbolDiffSymbol {
	keys := make([]string, 0, len(rawSymbols))
	for key := range rawSymbols {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]symbolDiffSymbol, 0, len(rawSymbols))
	appendSymbol := func(raw map[string]any, fallbackID string) {
		if raw == nil {
			return
		}
		id := strings.TrimSpace(stringArg(raw, "id", ""))
		if id == "" {
			id = strings.TrimSpace(fallbackID)
		}
		out = append(out, symbolDiffSymbol{
			ID:          id,
			Name:        strings.TrimSpace(stringArg(raw, "name", "")),
			Kind:        strings.TrimSpace(stringArg(raw, "kind", "")),
			File:        normalizeRepoFilePath(stringArg(raw, "file", "")),
			Line:        intArg(raw, "line"),
			Signature:   stringArg(raw, "signature", ""),
			ContentHash: stringArg(raw, "content_hash", ""),
		})
	}

	for _, key := range keys {
		switch typed := rawSymbols[key].(type) {
		case map[string]any:
			appendSymbol(typed, key)
		case []any:
			for index, item := range typed {
				raw, ok := item.(map[string]any)
				if !ok {
					continue
				}
				appendSymbol(raw, fmt.Sprintf("%s#%d", key, index))
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func buildSymbolDiffIdentityMap(symbols []symbolDiffSymbol) map[symbolDiffIdentityKey]symbolDiffSymbol {
	out := map[symbolDiffIdentityKey]symbolDiffSymbol{}
	for _, symbol := range symbols {
		key := symbolDiffIdentityKey{
			Name: symbol.Name,
			Kind: symbol.Kind,
		}
		if _, exists := out[key]; exists {
			continue
		}
		out[key] = symbol
	}
	return out
}

func parseClassHierarchyBases(signature string) []string {
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return []string{}
	}

	bases := make([]string, 0, 4)
	if match := classHierarchyExtendsPattern.FindStringSubmatch(signature); len(match) > 1 {
		for _, item := range splitAndTrimCSV(match[1]) {
			if normalized := normalizeClassHierarchyBase(item); normalized != "" {
				bases = append(bases, normalized)
			}
		}
	}
	if match := classHierarchyImplementsPattern.FindStringSubmatch(signature); len(match) > 1 {
		for _, item := range splitAndTrimCSV(match[1]) {
			if normalized := normalizeClassHierarchyBase(item); normalized != "" {
				bases = append(bases, normalized)
			}
		}
	}
	if len(bases) == 0 {
		if match := classHierarchyParenBasesPattern.FindStringSubmatch(signature); len(match) > 1 {
			for _, candidate := range splitAndTrimCSV(match[1]) {
				normalized := normalizeClassHierarchyBase(candidate)
				if classHierarchyExternalBasePattern.MatchString(normalized) {
					bases = append(bases, normalized)
				}
			}
		}
	}
	return bases
}

func splitAndTrimCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	segments := strings.Split(value, ",")
	out := make([]string, 0, len(segments))
	for _, segment := range segments {
		trimmed := strings.TrimSpace(segment)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func normalizeClassHierarchyBase(value string) string {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" {
		return ""
	}
	for _, delimiter := range []string{"{", "(", "<"} {
		index := strings.Index(cleaned, delimiter)
		if index < 0 {
			continue
		}
		cleaned = strings.TrimSpace(cleaned[:index])
	}
	return cleaned
}

func toTokenSet(tokens []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, token := range tokens {
		normalized := strings.ToLower(strings.TrimSpace(token))
		if normalized == "" {
			continue
		}
		out[normalized] = struct{}{}
	}
	return out
}

func tokenSetOverlapCount(left, right map[string]struct{}) int {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	count := 0
	for token := range left {
		if _, ok := right[token]; ok {
			count++
		}
	}
	return count
}

func buildFileImportersByTarget(records []importRecord) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	for _, record := range records {
		if record.Resolved == "" || record.SourceFile == "" || record.Resolved == record.SourceFile {
			continue
		}
		importers, ok := out[record.Resolved]
		if !ok {
			importers = map[string]struct{}{}
			out[record.Resolved] = importers
		}
		importers[record.SourceFile] = struct{}{}
	}
	return out
}

func hasSharedImporter(left, right map[string]struct{}) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	for importer := range left {
		if _, ok := right[importer]; ok {
			return true
		}
	}
	return false
}

func rankMapTopKeys(counts map[string]int, limit int) []string {
	ranked := make([]rankedIntKey, 0, len(counts))
	for key, count := range counts {
		ranked = append(ranked, rankedIntKey{Key: key, Count: count})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Count != ranked[j].Count {
			return ranked[i].Count > ranked[j].Count
		}
		return ranked[i].Key < ranked[j].Key
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]string, 0, len(ranked))
	for _, item := range ranked {
		out = append(out, item.Key)
	}
	return out
}

func rankMapToOrderedMap(counts map[string]int) map[string]any {
	ranked := make([]rankedIntKey, 0, len(counts))
	for key, count := range counts {
		ranked = append(ranked, rankedIntKey{Key: key, Count: count})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Count != ranked[j].Count {
			return ranked[i].Count > ranked[j].Count
		}
		return ranked[i].Key < ranked[j].Key
	})
	out := map[string]any{}
	for _, item := range ranked {
		out[item.Key] = item.Count
	}
	return out
}

func roundToPlaces(value float64, places int) float64 {
	if places < 0 {
		return value
	}
	factor := math.Pow10(places)
	return math.Round(value*factor) / factor
}
