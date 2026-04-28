package orchestration

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
)

const (
	repoIndexVersion            = 6
	defaultMaxDiscoveryFileSize = 500 * 1024
	defaultMaxFolderFiles       = 2000
	defaultMaxIndexFiles        = 10000
	vectorChunkIDsContextKey    = "_vector_chunk_ids_by_file"
)

var sourceExtensions = map[string]string{
	".al":        "al",
	".blade.php": "blade",
	".c":         "c",
	".cc":        "cpp",
	".cpp":       "cpp",
	".cs":        "csharp",
	".cshtml":    "razor",
	".cxx":       "cpp",
	".dart":      "dart",
	".ex":        "elixir",
	".go":        "go",
	".h":         "c",
	".hpp":       "cpp",
	".java":      "java",
	".js":        "javascript",
	".jsx":       "javascript",
	".php":       "php",
	".pl":        "perl",
	".py":        "python",
	".rb":        "ruby",
	".rs":        "rust",
	".sql":       "sql",
	".swift":     "swift",
	".ts":        "typescript",
	".tsx":       "typescript",
	".vue":       "vue",
	".xml":       "xml",
	".xul":       "xml",
}

var skippedDirectories = map[string]struct{}{
	".git":         {},
	".hg":          {},
	".svn":         {},
	".venv":        {},
	"venv":         {},
	"node_modules": {},
	"dist":         {},
	"build":        {},
	"target":       {},
	"coverage":     {},
	"__pycache__":  {},
}

var githubSlugPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

var allowedGitHubHosts = map[string]struct{}{
	"github.com": {},
}

var secretPathHints = []string{
	".env",
	"api_key",
	"apikey",
	"credential",
	"credentials",
	"id_rsa",
	"passwd",
	"password",
	"private_key",
	"secret",
	"token",
}

var documentationExtensions = map[string]struct{}{
	".adoc":     {},
	".markdown": {},
	".md":       {},
	".mdown":    {},
	".mkd":      {},
	".mkdn":     {},
	".rst":      {},
	".text":     {},
	".txt":      {},
	".wiki":     {},
}

func (s *Service) bindImplementedHandlers() {
	s.setHandler("index_repo", s.handleIndexRepo)
	s.setHandler("index_folder", s.handleIndexFolder)
	s.setHandler("index_file", s.handleIndexFile)
	s.setHandler("list_repos", s.handleListRepos)
	s.setHandler("resolve_repo", s.handleResolveRepo)
	s.setHandler("get_file_tree", s.handleGetFileTree)
	s.setHandler("get_file_outline", s.handleGetFileOutline)
	s.setHandler("get_file_content", s.handleGetFileContent)
	s.setHandler("get_symbol_source", s.handleGetSymbolSource)
	s.setHandler("search_symbols", s.handleSearchSymbols)
	s.setHandler("invalidate_cache", s.handleInvalidateCache)
	s.setHandler("search_text", s.handleSearchText)
	s.setHandler("get_repo_outline", s.handleGetRepoOutline)
	s.setHandler("find_importers", s.handleFindImporters)
	s.setHandler("find_references", s.handleFindReferences)
	s.setHandler("check_references", s.handleCheckReferences)
	s.setHandler("search_columns", s.handleSearchColumns)
	s.setHandler("get_context_bundle", s.handleGetContextBundle)
	s.setHandler("get_session_stats", s.handleGetSessionStats)
	s.setHandler("get_dependency_graph", s.handleGetDependencyGraph)
	s.setHandler("get_symbol_diff", s.handleGetSymbolDiff)
	s.setHandler("get_class_hierarchy", s.handleGetClassHierarchy)
	s.setHandler("get_related_symbols", s.handleGetRelatedSymbols)
	s.setHandler("suggest_queries", s.handleSuggestQueries)
	s.setHandler("get_blast_radius", s.handleGetBlastRadius)
	s.setHandler("wait_for_fresh", s.handleWaitForFresh)
	s.setHandler("check_freshness", s.handleCheckFreshness)
}

func (s *Service) setHandler(name string, handler ToolHandler) {
	tool, ok := s.tools[name]
	if !ok {
		return
	}
	tool.Handler = handler
	s.tools[name] = tool
}

func (s *Service) handleIndexRepo(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	rawURL := strings.TrimSpace(stringArg(arguments, "url", ""))
	owner, repo, err := parseGitHubRepoURL(rawURL)
	if err != nil {
		return map[string]any{
			"success": false,
			"error":   err.Error(),
		}, nil
	}

	repoID := owner + "/" + repo
	canonicalURL := canonicalGitHubRepoURL(owner, repo)
	acquirer := s.deps.RepoAcquirer
	if acquirer == nil {
		return map[string]any{
			"success": false,
			"repo":    repoID,
			"error":   "index_repo is not implemented yet in the Go migration runtime. Use index_folder for local repositories.",
		}, nil
	}

	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	incremental := boolArg(arguments, "incremental", true)
	extraIgnorePatternsArg, _ := optionalRawStringSliceArg(arguments, "extra_ignore_patterns")
	extraIgnorePatterns := append(
		append([]string(nil), s.cfg.ExtraIgnorePatterns...),
		extraIgnorePatternsArg...,
	)
	excludeSecretPatterns := append([]string(nil), s.cfg.ExcludeSecretPatterns...)
	maxIndexFiles := s.cfg.MaxIndexFiles
	if maxIndexFiles <= 0 {
		maxIndexFiles = defaultMaxIndexFiles
	}
	started := time.Now()

	return s.runWithReindexLifecycle(ctx, repoID, "", func() (map[string]any, error) {
		existing := storage.RepoIndex{}
		loadErr := storage.ErrRepoNotFound
		if incremental {
			existing, loadErr = store.Load(ctx, repoID)
			if loadErr != nil && !errors.Is(loadErr, storage.ErrRepoNotFound) {
				return nil, loadErr
			}
		}

		prefetchWarnings := make([]string, 0, 1)
		gitignoreContent, gitignoreErr := acquirer.ReadGitignore(ctx, canonicalURL)
		if gitignoreErr != nil {
			prefetchWarnings = append(prefetchWarnings, fmt.Sprintf("Failed to read .gitignore: %v", gitignoreErr))
		}

		useMetadataPrefetch := incremental && loadErr == nil
		metadataSourceFiles := []string{}
		metadataBlobSHAs := map[string]string{}
		metadataSkipCounts := map[string]int{}
		currentTreeSHA := ""
		if useMetadataPrefetch {
			metadata, metadataErr := acquirer.AcquireTreeMetadata(ctx, canonicalURL)
			if metadataErr != nil {
				return map[string]any{
					"success": false,
					"repo":    repoID,
					"error":   fmt.Sprintf("Failed to fetch repository tree metadata: %v", metadataErr),
				}, nil
			}

			var metadataDiscoverErr error
			metadataSourceFiles, metadataBlobSHAs, metadataSkipCounts, metadataDiscoverErr = discoverRemoteSourceMetadata(
				ctx,
				metadata.Files,
				gitignoreContent,
				extraIgnorePatterns,
				excludeSecretPatterns,
				maxIndexFiles,
			)
			if metadataDiscoverErr != nil {
				return nil, metadataDiscoverErr
			}

			currentTreeSHA = strings.ToLower(strings.TrimSpace(metadata.TreeSHA))
			if currentTreeSHA == "" {
				currentTreeSHA = remoteTreeSHA(metadataBlobSHAs)
			}
		}

		warnings := append([]string(nil), prefetchWarnings...)
		if useMetadataPrefetch && len(metadataSourceFiles) == 0 {
			return buildIndexRepoNoSourceFilesResult(indexRepoNoSourceFilesResultInput{
				repoID:       repoID,
				canonicalURL: canonicalURL,
				warnings:     warnings,
			}), nil
		}

		if useMetadataPrefetch {
			if currentTreeSHA != "" && strings.TrimSpace(existing.GitHead) == currentTreeSHA {
				return buildIndexRepoNoChangeResult(indexRepoNoChangeResultInput{
					message:        "No changes detected (tree SHA unchanged)",
					repoID:         repoID,
					canonicalURL:   canonicalURL,
					gitHead:        currentTreeSHA,
					includeGitHead: true,
					warnings:       warnings,
					duration:       time.Since(started),
				}), nil
			}

			if len(existing.FileBlobSHAs) > 0 {
				changed, created, deleted := diffFileHashes(existing.FileBlobSHAs, metadataBlobSHAs)
				if len(changed) == 0 && len(created) == 0 && len(deleted) == 0 {
					return buildIndexRepoNoChangeResult(indexRepoNoChangeResultInput{
						message:        "No changes detected",
						repoID:         repoID,
						canonicalURL:   canonicalURL,
						gitHead:        currentTreeSHA,
						includeGitHead: currentTreeSHA != "",
						warnings:       warnings,
						duration:       time.Since(started),
					}), nil
				}
			}
		}

		fetchPlan := planRemoteTreeFetch(useMetadataPrefetch, existing, metadataSourceFiles, metadataBlobSHAs)
		useIncrementalBlobDiff := fetchPlan.useIncrementalBlobDiff
		fetchPaths := fetchPlan.fetchPaths
		tree, acquireErr := acquireRemoteTreeForIndex(ctx, acquirer, canonicalURL, fetchPlan)
		if acquireErr != nil {
			return map[string]any{
				"success": false,
				"repo":    repoID,
				"error":   fmt.Sprintf("Failed to fetch repository tree: %v", acquireErr),
			}, nil
		}

		prefetchedBlobSHAs := map[string]string{}
		if useMetadataPrefetch {
			prefetchedBlobSHAs = metadataBlobSHAs
		}
		fetchedSourceFiles, fetchedFileHashes, fetchedFileBlobSHAs, fetchedFileMTimes, fetchedLanguageCounts, discoverWarnings, contentSkipCounts, discoverErr := discoverRemoteSourceFiles(
			ctx,
			tree,
			gitignoreContent,
			extraIgnorePatterns,
			excludeSecretPatterns,
			maxIndexFiles,
			prefetchedBlobSHAs,
		)
		if discoverErr != nil {
			return nil, discoverErr
		}
		if len(discoverWarnings) > 0 {
			warnings = append(warnings, discoverWarnings...)
		}
		skipCounts := contentSkipCounts
		if useMetadataPrefetch {
			skipCounts = mergeDiscoverySkipCounts(metadataSkipCounts, contentSkipCounts)
		}
		sourceFiles := fetchedSourceFiles
		fileHashes := fetchedFileHashes
		fileBlobSHAs := fetchedFileBlobSHAs
		fileMTimes := fetchedFileMTimes
		languageCounts := fetchedLanguageCounts
		if useIncrementalBlobDiff {
			sourceFiles, fileHashes, fileBlobSHAs, fileMTimes, languageCounts = mergeRemoteIncrementalIndexState(
				existing,
				metadataSourceFiles,
				metadataBlobSHAs,
				fetchedFileHashes,
				fetchedFileBlobSHAs,
				fetchedFileMTimes,
				fetchPaths,
			)
		}

		// Preserve metadata-only skip-limit observations when content fetch yields no additional skips.
		if skipCounts["file_limit"] == 0 && metadataSkipCounts["file_limit"] > 0 {
			skipCounts["file_limit"] = metadataSkipCounts["file_limit"]
		}
		if len(sourceFiles) == 0 {
			return buildIndexRepoNoSourceFilesResult(indexRepoNoSourceFilesResultInput{
				repoID:       repoID,
				canonicalURL: canonicalURL,
				warnings:     warnings,
			}), nil
		}
		if currentTreeSHA == "" {
			currentTreeSHA = remoteTreeSHA(fileBlobSHAs)
		}

		nowRFC3339 := time.Now().UTC().Format(time.RFC3339)
		if incremental && loadErr == nil {
			changed := []string{}
			created := []string{}
			deleted := []string{}
			usedBlobDiff := false
			if len(existing.FileBlobSHAs) > 0 {
				changed, created, deleted = diffFileHashes(existing.FileBlobSHAs, fileBlobSHAs)
				usedBlobDiff = true
			} else {
				changed, created, deleted = diffFileHashes(existing.Files, fileHashes)
			}

			if len(changed) == 0 && len(created) == 0 && len(deleted) == 0 {
				return buildIndexRepoNoChangeResult(indexRepoNoChangeResultInput{
					message:        "No changes detected",
					repoID:         repoID,
					canonicalURL:   canonicalURL,
					gitHead:        currentTreeSHA,
					includeGitHead: usedBlobDiff && currentTreeSHA != "",
					warnings:       warnings,
					duration:       time.Since(started),
				}), nil
			}

			existing.Repo = repoID
			existing.IndexedAt = nowRFC3339
			existing.SourceRoot = canonicalURL
			existing.DisplayName = repo
			existing.Languages = cloneLanguageCounts(languageCounts)
			existing.IndexVersion = repoIndexVersion
			existing.GitHead = currentTreeSHA
			existing.Files = cloneFileHashes(fileHashes)
			existing.FileBlobSHAs = cloneFileHashes(fileBlobSHAs)
			existing.FileMTimes = cloneFileMTimes(fileMTimes)
			if existing.Symbols == nil {
				existing.Symbols = map[string]any{}
			}

			changeSet := storage.ChangeSet{
				Changed: changed,
				New:     created,
				Deleted: deleted,
			}
			if err := store.IncrementalSave(ctx, repoID, existing, changeSet); err != nil {
				return nil, err
			}
			if err := s.syncRemoteIndexVectors(
				ctx,
				&existing,
				repoID,
				tree,
				changed,
				created,
				deleted,
			); err != nil {
				warnings = append(warnings, fmt.Sprintf("Vector sync skipped: %v", err))
			} else if err := store.IncrementalSave(ctx, repoID, existing, storage.ChangeSet{}); err != nil {
				return nil, err
			}

			return buildIndexRepoIncrementalSuccessResult(indexRepoIncrementalSuccessResultInput{
				repoID:       repoID,
				canonicalURL: canonicalURL,
				changedCount: len(changed),
				newCount:     len(created),
				deletedCount: len(deleted),
				symbolCount:  len(existing.Symbols),
				indexedAt:    nowRFC3339,
				duration:     time.Since(started),
				skipCounts:   skipCounts,
				warnings:     warnings,
			}), nil
		}

		index := storage.RepoIndex{
			Repo:         repoID,
			IndexedAt:    nowRFC3339,
			SourceRoot:   canonicalURL,
			DisplayName:  repo,
			Languages:    cloneLanguageCounts(languageCounts),
			IndexVersion: repoIndexVersion,
			GitHead:      currentTreeSHA,
			Files:        cloneFileHashes(fileHashes),
			FileBlobSHAs: cloneFileHashes(fileBlobSHAs),
			FileMTimes:   cloneFileMTimes(fileMTimes),
			Symbols:      map[string]any{},
		}
		if err := store.Save(ctx, repoID, index); err != nil {
			return nil, err
		}
		if err := s.syncRemoteIndexVectors(ctx, &index, repoID, tree, sourceFiles, nil, nil); err != nil {
			warnings = append(warnings, fmt.Sprintf("Vector sync skipped: %v", err))
		} else if err := store.Save(ctx, repoID, index); err != nil {
			return nil, err
		}

		return buildIndexRepoFullSuccessResult(indexRepoFullSuccessResultInput{
			repoID:         repoID,
			canonicalURL:   canonicalURL,
			indexedAt:      nowRFC3339,
			fileCount:      len(sourceFiles),
			languageCounts: languageCounts,
			sourceFiles:    sourceFiles,
			duration:       time.Since(started),
			skipCounts:     skipCounts,
			warnings:       warnings,
			maxIndexFiles:  maxIndexFiles,
		}), nil
	})
}

func parseGitHubRepoURL(raw string) (string, string, error) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(raw, ".git"))
	if trimmed == "" {
		return "", "", fmt.Errorf("Could not parse GitHub URL: %s", raw)
	}

	if strings.Contains(trimmed, "/") && !strings.Contains(trimmed, "://") {
		parts := strings.Split(trimmed, "/")
		if len(parts) >= 2 {
			return parseGitHubOwnerRepoParts(parts[0], parts[1], trimmed)
		}
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", "", fmt.Errorf("Could not parse GitHub URL: %s", raw)
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if _, ok := allowedGitHubHosts[host]; !ok {
		return "", "", fmt.Errorf("Unsupported host %q. Only github.com URLs are accepted.", host)
	}

	pathParts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(pathParts) < 2 {
		return "", "", fmt.Errorf("Could not parse GitHub URL: %s", raw)
	}

	return parseGitHubOwnerRepoParts(pathParts[0], pathParts[1], raw)
}

type indexRepoNoChangeResultInput struct {
	message        string
	repoID         string
	canonicalURL   string
	gitHead        string
	includeGitHead bool
	warnings       []string
	duration       time.Duration
}

func buildIndexRepoNoChangeResult(input indexRepoNoChangeResultInput) map[string]any {
	result := map[string]any{
		"success":          true,
		"message":          input.message,
		"repo":             input.repoID,
		"url":              input.canonicalURL,
		"changed":          0,
		"new":              0,
		"deleted":          0,
		"duration_seconds": roundSeconds(input.duration),
	}
	if input.includeGitHead && strings.TrimSpace(input.gitHead) != "" {
		result["git_head"] = input.gitHead
	}
	if len(input.warnings) > 0 {
		result["warnings"] = append([]string(nil), input.warnings...)
	}
	return result
}

type indexRepoNoSourceFilesResultInput struct {
	repoID       string
	canonicalURL string
	warnings     []string
}

func buildIndexRepoNoSourceFilesResult(input indexRepoNoSourceFilesResultInput) map[string]any {
	result := map[string]any{
		"success": false,
		"repo":    input.repoID,
		"url":     input.canonicalURL,
		"error":   "No source files found",
	}
	appendWarnings(result, input.warnings)
	return result
}

type indexRepoIncrementalSuccessResultInput struct {
	repoID       string
	canonicalURL string
	changedCount int
	newCount     int
	deletedCount int
	symbolCount  int
	indexedAt    string
	duration     time.Duration
	skipCounts   map[string]int
	warnings     []string
}

func buildIndexRepoIncrementalSuccessResult(input indexRepoIncrementalSuccessResultInput) map[string]any {
	result := map[string]any{
		"success":               true,
		"repo":                  input.repoID,
		"url":                   input.canonicalURL,
		"incremental":           true,
		"changed":               input.changedCount,
		"new":                   input.newCount,
		"deleted":               input.deletedCount,
		"symbol_count":          input.symbolCount,
		"indexed_at":            input.indexedAt,
		"duration_seconds":      roundSeconds(input.duration),
		"discovery_skip_counts": input.skipCounts,
		"no_symbols_count":      0,
		"no_symbols_files":      []string{},
	}
	appendWarnings(result, input.warnings)
	return result
}

type indexRepoFullSuccessResultInput struct {
	repoID         string
	canonicalURL   string
	indexedAt      string
	fileCount      int
	languageCounts map[string]int
	sourceFiles    []string
	duration       time.Duration
	skipCounts     map[string]int
	warnings       []string
	maxIndexFiles  int
}

func buildIndexRepoFullSuccessResult(input indexRepoFullSuccessResultInput) map[string]any {
	result := map[string]any{
		"success":               true,
		"repo":                  input.repoID,
		"url":                   input.canonicalURL,
		"indexed_at":            input.indexedAt,
		"file_count":            input.fileCount,
		"symbol_count":          0,
		"file_summary_count":    0,
		"languages":             input.languageCounts,
		"files":                 sampleFiles(input.sourceFiles, 20),
		"duration_seconds":      roundSeconds(input.duration),
		"discovery_skip_counts": input.skipCounts,
		"no_symbols_count":      0,
		"no_symbols_files":      []string{},
	}

	resultWarnings := cloneWarnings(input.warnings)
	filesSkippedCap := input.skipCounts["file_limit"]
	if filesSkippedCap > 0 {
		filesDiscovered := input.maxIndexFiles + filesSkippedCap
		result["files_discovered"] = filesDiscovered
		result["files_indexed"] = input.maxIndexFiles
		result["files_skipped_cap"] = filesSkippedCap
		resultWarnings = append(
			resultWarnings,
			fmt.Sprintf(
				"File cap reached: %d files discovered, %d indexed, %d dropped. Raise JCODEMUNCH_MAX_INDEX_FILES or narrow the path.",
				filesDiscovered,
				input.maxIndexFiles,
				filesSkippedCap,
			),
		)
	}

	appendWarnings(result, resultWarnings)
	return result
}

type indexFolderNoSourceFilesResultInput struct {
	warnings []string
}

func buildIndexFolderNoSourceFilesResult(input indexFolderNoSourceFilesResultInput) map[string]any {
	result := map[string]any{
		"success": false,
		"error":   "No source files found",
	}
	appendWarnings(result, input.warnings)
	return result
}

type indexFolderNoChangeResultInput struct {
	repoID       string
	resolvedPath string
	warnings     []string
	duration     time.Duration
}

func buildIndexFolderNoChangeResult(input indexFolderNoChangeResultInput) map[string]any {
	result := map[string]any{
		"success":          true,
		"message":          "No changes detected",
		"repo":             input.repoID,
		"folder_path":      input.resolvedPath,
		"changed":          0,
		"new":              0,
		"deleted":          0,
		"duration_seconds": roundSeconds(input.duration),
	}
	appendWarnings(result, input.warnings)
	return result
}

type indexFolderIncrementalSuccessResultInput struct {
	repoID            string
	resolvedPath      string
	changedCount      int
	newCount          int
	deletedCount      int
	symbolCount       int
	indexedAt         string
	duration          time.Duration
	includeSkipCounts bool
	skipCounts        map[string]int
	warnings          []string
}

func buildIndexFolderIncrementalSuccessResult(input indexFolderIncrementalSuccessResultInput) map[string]any {
	result := map[string]any{
		"success":          true,
		"repo":             input.repoID,
		"folder_path":      input.resolvedPath,
		"incremental":      true,
		"changed":          input.changedCount,
		"new":              input.newCount,
		"deleted":          input.deletedCount,
		"symbol_count":     input.symbolCount,
		"indexed_at":       input.indexedAt,
		"duration_seconds": roundSeconds(input.duration),
		"no_symbols_count": 0,
		"no_symbols_files": []string{},
	}
	if input.includeSkipCounts {
		result["discovery_skip_counts"] = input.skipCounts
	}
	appendWarnings(result, input.warnings)
	return result
}

type indexFolderFullSuccessResultInput struct {
	repoID         string
	resolvedPath   string
	indexedAt      string
	fileCount      int
	languageCounts map[string]int
	sourceFiles    []string
	duration       time.Duration
	skipCounts     map[string]int
	warnings       []string
	maxFolderFiles int
}

func buildIndexFolderFullSuccessResult(input indexFolderFullSuccessResultInput) map[string]any {
	result := map[string]any{
		"success":               true,
		"repo":                  input.repoID,
		"folder_path":           input.resolvedPath,
		"indexed_at":            input.indexedAt,
		"file_count":            input.fileCount,
		"symbol_count":          0,
		"file_summary_count":    0,
		"languages":             input.languageCounts,
		"files":                 sampleFiles(input.sourceFiles, 20),
		"duration_seconds":      roundSeconds(input.duration),
		"discovery_skip_counts": input.skipCounts,
		"no_symbols_count":      0,
		"no_symbols_files":      []string{},
	}

	resultWarnings := cloneWarnings(input.warnings)
	filesSkippedCap := input.skipCounts["file_limit"]
	if filesSkippedCap > 0 {
		filesDiscovered := input.maxFolderFiles + filesSkippedCap
		result["files_discovered"] = filesDiscovered
		result["files_indexed"] = input.maxFolderFiles
		result["files_skipped_cap"] = filesSkippedCap
		resultWarnings = append(
			resultWarnings,
			fmt.Sprintf(
				"File cap reached: %d files discovered, %d indexed, %d dropped. Raise JCODEMUNCH_MAX_FOLDER_FILES or narrow the path.",
				filesDiscovered,
				input.maxFolderFiles,
				filesSkippedCap,
			),
		)
	}

	appendWarnings(result, resultWarnings)
	return result
}

func buildIndexFileFileNotFoundResult(requestedPath string) map[string]any {
	return map[string]any{
		"success": false,
		"error":   fmt.Sprintf("File not found: %s", requestedPath),
	}
}

func buildIndexFilePathNotFileResult(requestedPath string) map[string]any {
	return map[string]any{
		"success": false,
		"error":   fmt.Sprintf("Path is not a file: %s", requestedPath),
	}
}

func buildIndexFileNoIndexedFolderResult(requestedPath string) map[string]any {
	return map[string]any{
		"success": false,
		"error": fmt.Sprintf(
			"No indexed folder found that contains %s. Run index_folder on the parent directory first.",
			requestedPath,
		),
	}
}

func buildIndexFileSecurityValidationFailureResult(requestedPath string) map[string]any {
	return map[string]any{
		"success": false,
		"error":   fmt.Sprintf("File path failed security validation: %s", requestedPath),
	}
}

func buildIndexFileUnsupportedFileTypeResult(extension string) map[string]any {
	return map[string]any{
		"success": false,
		"error": fmt.Sprintf(
			"Unsupported file type: %s. File not recognized as a supported language.",
			extension,
		),
	}
}

func buildIndexFileReadFailureResult(err error) map[string]any {
	return map[string]any{
		"success": false,
		"error":   fmt.Sprintf("Failed to read file: %v", err),
	}
}

func buildIndexFileLoadFailureResult(repoID string) map[string]any {
	return map[string]any{
		"success": false,
		"error":   fmt.Sprintf("Failed to load index for %s", repoID),
	}
}

type indexFileUnchangedSuccessResultInput struct {
	repoID   string
	relPath  string
	duration time.Duration
}

func buildIndexFileUnchangedSuccessResult(input indexFileUnchangedSuccessResultInput) map[string]any {
	return map[string]any{
		"success":          true,
		"message":          "File unchanged",
		"repo":             input.repoID,
		"file":             input.relPath,
		"duration_seconds": roundSeconds(input.duration),
	}
}

type indexFileIncrementalSuccessResultInput struct {
	repoID    string
	relPath   string
	isNew     bool
	indexedAt string
	duration  time.Duration
}

func buildIndexFileIncrementalSuccessResult(input indexFileIncrementalSuccessResultInput) map[string]any {
	return map[string]any{
		"success":          true,
		"repo":             input.repoID,
		"file":             input.relPath,
		"is_new":           input.isNew,
		"symbol_count":     0,
		"indexed_at":       input.indexedAt,
		"duration_seconds": roundSeconds(input.duration),
	}
}

func parseGitHubOwnerRepoParts(owner, repo, input string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("Could not parse GitHub URL: %s", input)
	}
	if !githubSlugPattern.MatchString(owner) || !githubSlugPattern.MatchString(repo) {
		return "", "", fmt.Errorf("Invalid owner/repo format: %q", input)
	}
	return owner, repo, nil
}

func canonicalGitHubRepoURL(owner, repo string) string {
	return fmt.Sprintf("https://github.com/%s/%s", strings.TrimSpace(owner), strings.TrimSpace(repo))
}

func discoverRemoteSourceMetadata(
	ctx context.Context,
	files map[string]indexing.RemoteFileMetadata,
	gitignoreContent []byte,
	extraIgnorePatterns []string,
	excludedSecretPatterns []string,
	maxFiles int,
) ([]string, map[string]string, map[string]int, error) {
	sourceFiles := make([]string, 0, len(files))
	fileBlobSHAs := map[string]string{}
	skipCounts := map[string]int{
		"skip_dir":        0,
		"skip_file":       0,
		"symlink":         0,
		"symlink_escape":  0,
		"path_traversal":  0,
		"gitignore":       0,
		"extra_ignore":    0,
		"secret":          0,
		"wrong_extension": 0,
		"too_large":       0,
		"unreadable":      0,
		"binary":          0,
		"file_limit":      0,
	}

	effectiveIgnorePatterns := normalizeIgnorePatterns(extraIgnorePatterns)
	gitignorePatterns := parseGitignorePatterns(gitignoreContent)
	paths := make([]string, 0, len(files))
	for rawPath := range files {
		paths = append(paths, rawPath)
	}
	sort.Strings(paths)

	for _, rawPath := range paths {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}

		relPath, reason := normalizeRemoteTreePath(rawPath)
		if relPath == "" {
			if reason != "" {
				skipCounts[reason]++
			}
			continue
		}
		if shouldSkipRemotePath(relPath) {
			skipCounts["skip_dir"]++
			continue
		}
		if matchesIgnorePattern(relPath, gitignorePatterns) {
			skipCounts["gitignore"]++
			continue
		}
		if matchesIgnorePattern(relPath, effectiveIgnorePatterns) {
			skipCounts["extra_ignore"]++
			continue
		}
		if _, ok := classifyLanguage(relPath); !ok {
			skipCounts["wrong_extension"]++
			continue
		}
		if isLikelySecretPathWithExclusions(relPath, excludedSecretPatterns) {
			skipCounts["secret"]++
			continue
		}

		meta := files[rawPath]
		if meta.SizeBytes > defaultMaxDiscoveryFileSize {
			skipCounts["too_large"]++
			continue
		}

		blobSHA := normalizeRemoteBlobSHA(meta.BlobSHA)
		if blobSHA == "" {
			sum := sha256.Sum256([]byte(relPath))
			blobSHA = hex.EncodeToString(sum[:])
		}
		fileBlobSHAs[relPath] = blobSHA
		sourceFiles = append(sourceFiles, relPath)
	}

	sort.Strings(sourceFiles)
	if maxFiles <= 0 {
		maxFiles = defaultMaxIndexFiles
	}
	if len(sourceFiles) > maxFiles {
		skipCounts["file_limit"] = len(sourceFiles) - maxFiles
		sort.SliceStable(sourceFiles, func(i, j int) bool {
			return compareFolderDiscoveryPriority(sourceFiles[i], sourceFiles[j])
		})
		sourceFiles = append([]string(nil), sourceFiles[:maxFiles]...)

		truncatedBlobSHAs := make(map[string]string, len(sourceFiles))
		for _, relPath := range sourceFiles {
			if blobSHA, ok := fileBlobSHAs[relPath]; ok {
				truncatedBlobSHAs[relPath] = blobSHA
			}
		}
		fileBlobSHAs = truncatedBlobSHAs
	}

	return sourceFiles, fileBlobSHAs, skipCounts, nil
}

func discoverRemoteSourceFiles(
	ctx context.Context,
	tree map[string][]byte,
	gitignoreContent []byte,
	extraIgnorePatterns []string,
	excludedSecretPatterns []string,
	maxFiles int,
	prefetchedBlobSHAs map[string]string,
) ([]string, map[string]string, map[string]string, map[string]int64, map[string]int, []string, map[string]int, error) {
	sourceFiles := make([]string, 0, len(tree))
	fileHashes := map[string]string{}
	fileBlobSHAs := map[string]string{}
	fileMTimes := map[string]int64{}
	languageCounts := map[string]int{}
	warnings := make([]string, 0, 4)
	skipCounts := map[string]int{
		"skip_dir":        0,
		"skip_file":       0,
		"symlink":         0,
		"symlink_escape":  0,
		"path_traversal":  0,
		"gitignore":       0,
		"extra_ignore":    0,
		"secret":          0,
		"wrong_extension": 0,
		"too_large":       0,
		"unreadable":      0,
		"binary":          0,
		"file_limit":      0,
	}

	effectiveIgnorePatterns := normalizeIgnorePatterns(extraIgnorePatterns)
	gitignorePatterns := parseGitignorePatterns(gitignoreContent)
	paths := make([]string, 0, len(tree))
	for rawPath := range tree {
		paths = append(paths, rawPath)
	}
	sort.Strings(paths)

	for _, rawPath := range paths {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, nil, nil, nil, nil, err
		}

		relPath, reason := normalizeRemoteTreePath(rawPath)
		if relPath == "" {
			if reason != "" {
				skipCounts[reason]++
			}
			continue
		}
		if shouldSkipRemotePath(relPath) {
			skipCounts["skip_dir"]++
			continue
		}
		if matchesIgnorePattern(relPath, gitignorePatterns) {
			skipCounts["gitignore"]++
			continue
		}
		if matchesIgnorePattern(relPath, effectiveIgnorePatterns) {
			skipCounts["extra_ignore"]++
			continue
		}

		language, ok := classifyLanguage(relPath)
		if !ok {
			skipCounts["wrong_extension"]++
			continue
		}

		content := tree[rawPath]
		if isLikelySecretPathWithExclusions(relPath, excludedSecretPatterns) {
			skipCounts["secret"]++
			continue
		}
		if len(content) > 500*1024 {
			skipCounts["too_large"]++
			continue
		}
		if isLikelyBinaryContent(content) {
			skipCounts["binary"]++
			continue
		}

		sum := sha256.Sum256(content)
		hashText := hex.EncodeToString(sum[:])
		fileHashes[relPath] = hashText
		if blobSHA := normalizeRemoteBlobSHA(prefetchedBlobSHAs[relPath]); blobSHA != "" {
			fileBlobSHAs[relPath] = blobSHA
		} else {
			fileBlobSHAs[relPath] = hashText
		}
		fileMTimes[relPath] = 0
		languageCounts[language]++
		sourceFiles = append(sourceFiles, relPath)
	}

	sort.Strings(sourceFiles)
	if maxFiles <= 0 {
		maxFiles = defaultMaxIndexFiles
	}
	if len(sourceFiles) > maxFiles {
		skipCounts["file_limit"] = len(sourceFiles) - maxFiles
		sort.SliceStable(sourceFiles, func(i, j int) bool {
			return compareFolderDiscoveryPriority(sourceFiles[i], sourceFiles[j])
		})
		sourceFiles = append([]string(nil), sourceFiles[:maxFiles]...)

		truncatedHashes := make(map[string]string, len(sourceFiles))
		truncatedBlobSHAs := make(map[string]string, len(sourceFiles))
		truncatedMTimes := make(map[string]int64, len(sourceFiles))
		truncatedLanguageCounts := map[string]int{}
		for _, relPath := range sourceFiles {
			if hash, ok := fileHashes[relPath]; ok {
				truncatedHashes[relPath] = hash
			}
			if blobSHA, ok := fileBlobSHAs[relPath]; ok {
				truncatedBlobSHAs[relPath] = blobSHA
			}
			if mtime, ok := fileMTimes[relPath]; ok {
				truncatedMTimes[relPath] = mtime
			}
			if language, ok := classifyLanguage(relPath); ok {
				truncatedLanguageCounts[language]++
			}
		}
		fileHashes = truncatedHashes
		fileBlobSHAs = truncatedBlobSHAs
		fileMTimes = truncatedMTimes
		languageCounts = truncatedLanguageCounts
	}
	return sourceFiles, fileHashes, fileBlobSHAs, fileMTimes, languageCounts, warnings, skipCounts, nil
}

func buildRemoteIncrementalFetchPaths(
	sourceFiles []string,
	existingFileHashes map[string]string,
	existingBlobSHAs map[string]string,
	metadataBlobSHAs map[string]string,
) []string {
	changed, created, _ := diffFileHashes(existingBlobSHAs, metadataBlobSHAs)
	fetchSet := map[string]struct{}{}
	for _, relPath := range changed {
		fetchSet[relPath] = struct{}{}
	}
	for _, relPath := range created {
		fetchSet[relPath] = struct{}{}
	}
	for _, relPath := range sourceFiles {
		hash, ok := existingFileHashes[relPath]
		if !ok || strings.TrimSpace(hash) == "" {
			fetchSet[relPath] = struct{}{}
		}
	}

	fetchPaths := make([]string, 0, len(fetchSet))
	for relPath := range fetchSet {
		fetchPaths = append(fetchPaths, relPath)
	}
	sort.Strings(fetchPaths)
	return fetchPaths
}

type remoteTreeFetchPlan struct {
	useMetadataPrefetch    bool
	useIncrementalBlobDiff bool
	fetchPaths             []string
}

func planRemoteTreeFetch(
	useMetadataPrefetch bool,
	existing storage.RepoIndex,
	metadataSourceFiles []string,
	metadataBlobSHAs map[string]string,
) remoteTreeFetchPlan {
	plan := remoteTreeFetchPlan{
		useMetadataPrefetch: useMetadataPrefetch,
		fetchPaths:          nil,
	}
	if !useMetadataPrefetch {
		return plan
	}

	if len(existing.FileBlobSHAs) > 0 {
		plan.useIncrementalBlobDiff = true
		plan.fetchPaths = buildRemoteIncrementalFetchPaths(
			metadataSourceFiles,
			existing.Files,
			existing.FileBlobSHAs,
			metadataBlobSHAs,
		)
		return plan
	}

	plan.fetchPaths = append([]string(nil), metadataSourceFiles...)
	return plan
}

func acquireRemoteTreeForIndex(
	ctx context.Context,
	acquirer indexing.RepoAcquirer,
	canonicalURL string,
	plan remoteTreeFetchPlan,
) (map[string][]byte, error) {
	if !plan.useMetadataPrefetch {
		return acquirer.AcquireTree(ctx, canonicalURL)
	}
	if len(plan.fetchPaths) == 0 {
		return map[string][]byte{}, nil
	}
	return acquirer.AcquireTreeSubset(ctx, canonicalURL, plan.fetchPaths)
}

func mergeDiscoverySkipCounts(metadataCounts, contentCounts map[string]int) map[string]int {
	merged := map[string]int{}
	for reason, count := range metadataCounts {
		merged[reason] = count
	}
	for reason, count := range contentCounts {
		merged[reason] += count
	}
	return merged
}

func mergeRemoteIncrementalIndexState(
	existing storage.RepoIndex,
	metadataSourceFiles []string,
	metadataBlobSHAs map[string]string,
	fetchedFileHashes map[string]string,
	fetchedFileBlobSHAs map[string]string,
	fetchedFileMTimes map[string]int64,
	fetchedPaths []string,
) ([]string, map[string]string, map[string]string, map[string]int64, map[string]int) {
	fetchedSet := map[string]struct{}{}
	for _, relPath := range fetchedPaths {
		fetchedSet[relPath] = struct{}{}
	}

	mergedFileHashes := map[string]string{}
	mergedFileBlobSHAs := map[string]string{}
	mergedFileMTimes := map[string]int64{}
	for _, relPath := range metadataSourceFiles {
		if blobSHA := normalizeRemoteBlobSHA(metadataBlobSHAs[relPath]); blobSHA != "" {
			mergedFileBlobSHAs[relPath] = blobSHA
		}
		if _, fetched := fetchedSet[relPath]; fetched {
			continue
		}
		if hash, ok := existing.Files[relPath]; ok && strings.TrimSpace(hash) != "" {
			mergedFileHashes[relPath] = hash
		}
		if mtime, ok := existing.FileMTimes[relPath]; ok {
			mergedFileMTimes[relPath] = mtime
		}
	}

	for relPath, hash := range fetchedFileHashes {
		if strings.TrimSpace(hash) == "" {
			continue
		}
		mergedFileHashes[relPath] = hash
	}
	for relPath, blobSHA := range fetchedFileBlobSHAs {
		if normalized := normalizeRemoteBlobSHA(blobSHA); normalized != "" {
			mergedFileBlobSHAs[relPath] = normalized
		}
	}
	for relPath, mtime := range fetchedFileMTimes {
		mergedFileMTimes[relPath] = mtime
	}

	for relPath := range mergedFileBlobSHAs {
		if _, ok := mergedFileHashes[relPath]; !ok {
			delete(mergedFileBlobSHAs, relPath)
			delete(mergedFileMTimes, relPath)
		}
	}

	for relPath, hash := range mergedFileHashes {
		if blobSHA := normalizeRemoteBlobSHA(mergedFileBlobSHAs[relPath]); blobSHA == "" {
			mergedFileBlobSHAs[relPath] = hash
		}
		if _, ok := mergedFileMTimes[relPath]; !ok {
			mergedFileMTimes[relPath] = 0
		}
	}

	sourceFiles := make([]string, 0, len(mergedFileHashes))
	for relPath := range mergedFileHashes {
		sourceFiles = append(sourceFiles, relPath)
	}
	sort.Strings(sourceFiles)

	return sourceFiles, mergedFileHashes, mergedFileBlobSHAs, mergedFileMTimes, languageCountsFromIndexedFiles(mergedFileHashes)
}

func languageCountsFromIndexedFiles(fileHashes map[string]string) map[string]int {
	languageCounts := map[string]int{}
	for relPath := range fileHashes {
		language, ok := classifyLanguage(relPath)
		if !ok {
			continue
		}
		languageCounts[language]++
	}
	return languageCounts
}

func normalizeRemoteBlobSHA(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func remoteTreeSHA(fileBlobSHAs map[string]string) string {
	if len(fileBlobSHAs) == 0 {
		return ""
	}

	paths := make([]string, 0, len(fileBlobSHAs))
	for relPath := range fileBlobSHAs {
		paths = append(paths, relPath)
	}
	sort.Strings(paths)

	hash := sha256.New()
	for _, relPath := range paths {
		_, _ = hash.Write([]byte(relPath))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(fileBlobSHAs[relPath]))
		_, _ = hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func normalizeRemoteTreePath(rawPath string) (string, string) {
	trimmed := strings.TrimSpace(strings.ReplaceAll(rawPath, "\\", "/"))
	trimmed = strings.TrimPrefix(trimmed, "./")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return "", "skip_file"
	}

	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", "path_traversal"
	}
	if strings.Contains(cleaned, "/../") {
		return "", "path_traversal"
	}
	return cleaned, ""
}

func shouldSkipRemotePath(relPath string) bool {
	for _, segment := range strings.Split(relPath, "/") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return true
		}
		if _, skip := skippedDirectories[segment]; skip {
			return true
		}
	}
	return false
}

func normalizeIgnorePatterns(patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
		if pattern == "" {
			continue
		}
		normalized = append(normalized, pattern)
	}
	sort.Strings(normalized)
	return normalized
}

func parseGitignorePatterns(content []byte) []string {
	if len(content) == 0 {
		return nil
	}

	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	patterns := make([]string, 0, len(lines))
	for _, line := range lines {
		pattern := strings.TrimSpace(strings.ReplaceAll(line, "\\", "/"))
		if pattern == "" {
			continue
		}
		if strings.HasPrefix(pattern, "#") {
			continue
		}
		if strings.HasPrefix(pattern, "!") {
			// Negation support remains a post-parity improvement.
			continue
		}
		pattern = strings.TrimPrefix(pattern, "/")
		if pattern == "" {
			continue
		}
		patterns = append(patterns, pattern)
	}

	if len(patterns) == 0 {
		return nil
	}
	sort.Strings(patterns)
	return patterns
}

func matchesIgnorePattern(relPath string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	for _, pattern := range patterns {
		if strings.HasSuffix(pattern, "/") {
			prefix := strings.TrimSuffix(pattern, "/")
			if prefix != "" && (relPath == prefix || strings.HasPrefix(relPath, prefix+"/")) {
				return true
			}
			continue
		}
		if ok, err := path.Match(pattern, relPath); err == nil && ok {
			return true
		}
		if !strings.Contains(pattern, "/") {
			if ok, err := path.Match(pattern, path.Base(relPath)); err == nil && ok {
				return true
			}
		}
		if !strings.ContainsAny(pattern, "*?[]") && (relPath == pattern || strings.HasPrefix(relPath, pattern+"/")) {
			return true
		}
	}
	return false
}

func isLikelySecretPath(relPath string) bool {
	return isLikelySecretPathWithExclusions(relPath, nil)
}

func isLikelySecretPathWithExclusions(relPath string, excludedPatterns []string) bool {
	lower := strings.ToLower(filepath.ToSlash(strings.TrimSpace(relPath)))
	if lower == "" {
		return false
	}

	if strings.Contains(lower, "secret") && isDocumentationPath(lower) {
		return false
	}

	base := path.Base(lower)
	for _, hint := range secretPathHints {
		if strings.Contains(base, hint) {
			if isExcludedSecretMatch(lower, base, hint, excludedPatterns) {
				continue
			}
			return true
		}
	}
	return false
}

func isExcludedSecretMatch(fullPath, base, hint string, excludedPatterns []string) bool {
	if len(excludedPatterns) == 0 {
		return false
	}

	for _, rawPattern := range excludedPatterns {
		pattern := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(rawPattern, "\\", "/")))
		if pattern == "" {
			continue
		}
		if pattern == hint {
			return true
		}
		if ok, err := path.Match(pattern, base); err == nil && ok {
			return true
		}
		if ok, err := path.Match(pattern, fullPath); err == nil && ok {
			return true
		}
	}
	return false
}

func isDocumentationPath(relPath string) bool {
	ext := strings.ToLower(filepath.Ext(relPath))
	_, ok := documentationExtensions[ext]
	return ok
}

func isLikelyBinaryContent(content []byte) bool {
	if len(content) == 0 {
		return false
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return true
	}

	sample := content
	if len(sample) > 4096 {
		sample = sample[:4096]
	}

	nonText := 0
	for _, b := range sample {
		switch {
		case b == 0x09 || b == 0x0A || b == 0x0D:
			continue
		case b >= 0x20 && b <= 0x7E:
			continue
		}
		nonText++
	}

	return float64(nonText)/float64(len(sample)) > 0.30
}

func (s *Service) handleIndexFolder(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	rawPath, _ := arguments["path"].(string)
	requestedPath := strings.TrimSpace(rawPath)
	if requestedPath == "" {
		return map[string]any{
			"success": false,
			"error":   "Folder not found: ",
		}, nil
	}

	expandedPath, err := expandUserHomePath(requestedPath)
	if err != nil {
		return map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Folder not found: %s", requestedPath),
		}, nil
	}

	resolvedPath, err := filepath.Abs(expandedPath)
	if err != nil {
		return map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Folder not found: %s", requestedPath),
		}, nil
	}
	absPath := resolvedPath
	evaluatedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil && evaluatedPath != "" {
		resolvedPath = evaluatedPath
	} else {
		resolvedPath = absPath
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{
				"success": false,
				"error":   fmt.Sprintf("Folder not found: %s", requestedPath),
			}, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Path is not a directory: %s", requestedPath),
		}, nil
	}

	repoScopedCfg := s.cfg.ForProjectRoot(resolvedPath)
	preflightWarnings := make([]string, 0, 2)
	trustedFolders := repoScopedCfg.TrustedFolders
	whitelistMode := repoScopedCfg.TrustedFoldersWhitelistEnabled()

	if !whitelistMode && len(trustedFolders) == 0 {
		return map[string]any{
			"success": false,
			"error": "trusted_folders_whitelist_mode is False (blacklist mode) but trusted_folders is empty. " +
				"No folders would be trusted. Add entries to trusted_folders to specify which folders should be untrusted.",
		}, nil
	}

	isTrusted := isTrustedFolderPath(resolvedPath, trustedFolders, whitelistMode)
	if len(trustedFolders) > 0 && !isTrusted {
		return map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Resolved path '%s' is not under trusted_folders.", resolvedPath),
		}, nil
	}

	const minSafePathComponents = 3
	if pathComponentCount(resolvedPath) < minSafePathComponents {
		if !isTrusted {
			return map[string]any{
				"success": false,
				"error": fmt.Sprintf(
					"Resolved path '%s' is too broad to index safely (fewer than %d path components). "+
						"Pass an absolute path to the specific project directory instead of a relative path like '.' "+
						"- relative paths resolve against the MCP server's working directory, which may not be your project root.",
					resolvedPath,
					minSafePathComponents,
				),
			}, nil
		}
		preflightWarnings = append(
			preflightWarnings,
			fmt.Sprintf(
				"Resolved path '%s' would normally be rejected as too broad, but it matched trusted_folders and was allowed.",
				resolvedPath,
			),
		)
	}

	if !isAbsoluteWithUserHome(requestedPath) {
		preflightWarnings = append(
			preflightWarnings,
			fmt.Sprintf(
				"Relative path '%s' resolved to '%s' (MCP server CWD). Prefer passing an absolute path to avoid unexpected behaviour.",
				requestedPath,
				resolvedPath,
			),
		)
	}

	followSymlinks := boolArg(arguments, "follow_symlinks", false)
	extraIgnorePatternsArg, _ := optionalRawStringSliceArg(arguments, "extra_ignore_patterns")
	extraIgnorePatterns := append(
		append([]string(nil), repoScopedCfg.ExtraIgnorePatterns...),
		extraIgnorePatternsArg...,
	)
	excludeSecretPatterns := append([]string(nil), repoScopedCfg.ExcludeSecretPatterns...)
	maxFolderFiles := repoScopedCfg.MaxFolderFiles
	if maxFolderFiles <= 0 {
		maxFolderFiles = defaultMaxFolderFiles
	}
	incremental := boolArg(arguments, "incremental", true)
	started := time.Now()

	repoID := localRepoID(resolvedPath)
	return s.runWithReindexLifecycle(ctx, repoID, resolvedPath, func() (map[string]any, error) {
		existing, loadErr := store.Load(ctx, repoID)
		if loadErr != nil && !errors.Is(loadErr, storage.ErrRepoNotFound) {
			return nil, loadErr
		}

		nowRFC3339 := time.Now().UTC().Format(time.RFC3339)
		indexedGitHead := ""
		if head, ok := gitHeadAtPath(ctx, resolvedPath); ok {
			indexedGitHead = head
		}

		if incremental && loadErr == nil {
			fastPathResult, fastPathChangeSet, handled, fastErr := indexFolderFromChangedPaths(
				ctx,
				repoID,
				resolvedPath,
				existing,
				arguments["changed_paths"],
				nowRFC3339,
				indexedGitHead,
				started,
				store,
			)
			if fastErr != nil {
				return nil, fastErr
			}
			if handled {
				if err := s.syncLocalIndexVectors(
					ctx,
					&existing,
					repoID,
					resolvedPath,
					fastPathChangeSet.Changed,
					fastPathChangeSet.New,
					fastPathChangeSet.Deleted,
				); err != nil {
					appendWarnings(fastPathResult, []string{fmt.Sprintf("Vector sync skipped: %v", err)})
				} else if err := store.IncrementalSave(ctx, repoID, existing, storage.ChangeSet{}); err != nil {
					return nil, err
				}
				appendWarnings(fastPathResult, preflightWarnings)
				return fastPathResult, nil
			}
		}

		sourceFiles, fileHashes, fileMTimes, languageCounts, warnings, skipCounts, err := discoverFolderFiles(
			ctx,
			resolvedPath,
			followSymlinks,
			extraIgnorePatterns,
			excludeSecretPatterns,
			maxFolderFiles,
		)
		if err != nil {
			return nil, err
		}
		if len(preflightWarnings) > 0 {
			warnings = append(cloneWarnings(preflightWarnings), warnings...)
		}
		if len(sourceFiles) == 0 {
			return buildIndexFolderNoSourceFilesResult(indexFolderNoSourceFilesResultInput{
				warnings: warnings,
			}), nil
		}

		if incremental && loadErr == nil {
			changed, created, deleted := diffFileHashes(existing.Files, fileHashes)
			if len(changed) == 0 && len(created) == 0 && len(deleted) == 0 {
				return buildIndexFolderNoChangeResult(indexFolderNoChangeResultInput{
					repoID:       repoID,
					resolvedPath: resolvedPath,
					warnings:     warnings,
					duration:     time.Since(started),
				}), nil
			}

			existing.Repo = repoID
			existing.IndexedAt = nowRFC3339
			existing.SourceRoot = resolvedPath
			existing.DisplayName = filepath.Base(resolvedPath)
			existing.Languages = languageCounts
			existing.IndexVersion = repoIndexVersion
			existing.GitHead = indexedGitHead
			existing.Files = cloneFileHashes(fileHashes)
			existing.FileMTimes = cloneFileMTimes(fileMTimes)
			if existing.Symbols == nil {
				existing.Symbols = map[string]any{}
			}

			changeSet := storage.ChangeSet{
				Changed: changed,
				New:     created,
				Deleted: deleted,
			}
			if err := store.IncrementalSave(ctx, repoID, existing, changeSet); err != nil {
				return nil, err
			}
			if err := s.syncLocalIndexVectors(
				ctx,
				&existing,
				repoID,
				resolvedPath,
				changed,
				created,
				deleted,
			); err != nil {
				warnings = append(warnings, fmt.Sprintf("Vector sync skipped: %v", err))
			} else if err := store.IncrementalSave(ctx, repoID, existing, storage.ChangeSet{}); err != nil {
				return nil, err
			}

			return buildIndexFolderIncrementalSuccessResult(indexFolderIncrementalSuccessResultInput{
				repoID:            repoID,
				resolvedPath:      resolvedPath,
				changedCount:      len(changed),
				newCount:          len(created),
				deletedCount:      len(deleted),
				symbolCount:       len(existing.Symbols),
				indexedAt:         nowRFC3339,
				duration:          time.Since(started),
				includeSkipCounts: true,
				skipCounts:        skipCounts,
				warnings:          warnings,
			}), nil
		}

		index := storage.RepoIndex{
			Repo:         repoID,
			IndexedAt:    nowRFC3339,
			SourceRoot:   resolvedPath,
			DisplayName:  filepath.Base(resolvedPath),
			Languages:    cloneLanguageCounts(languageCounts),
			IndexVersion: repoIndexVersion,
			GitHead:      indexedGitHead,
			Files:        cloneFileHashes(fileHashes),
			FileMTimes:   cloneFileMTimes(fileMTimes),
			Symbols:      map[string]any{},
		}
		if err := store.Save(ctx, repoID, index); err != nil {
			return nil, err
		}
		if err := s.syncLocalIndexVectors(ctx, &index, repoID, resolvedPath, sourceFiles, nil, nil); err != nil {
			warnings = append(warnings, fmt.Sprintf("Vector sync skipped: %v", err))
		} else if err := store.Save(ctx, repoID, index); err != nil {
			return nil, err
		}

		return buildIndexFolderFullSuccessResult(indexFolderFullSuccessResultInput{
			repoID:         repoID,
			resolvedPath:   resolvedPath,
			indexedAt:      nowRFC3339,
			fileCount:      len(sourceFiles),
			languageCounts: languageCounts,
			sourceFiles:    sourceFiles,
			duration:       time.Since(started),
			skipCounts:     skipCounts,
			warnings:       warnings,
			maxFolderFiles: maxFolderFiles,
		}), nil
	})
}

func (s *Service) handleIndexFile(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	rawPath, _ := arguments["path"].(string)
	requestedPath := strings.TrimSpace(rawPath)
	if requestedPath == "" {
		return buildIndexFileFileNotFoundResult(requestedPath), nil
	}

	expandedPath, err := expandUserHomePath(requestedPath)
	if err != nil {
		return buildIndexFileFileNotFoundResult(requestedPath), nil
	}

	absPath, err := filepath.Abs(expandedPath)
	if err != nil {
		return buildIndexFileFileNotFoundResult(requestedPath), nil
	}
	resolvedPath := absPath
	if evaluated, evalErr := filepath.EvalSymlinks(absPath); evalErr == nil && evaluated != "" {
		resolvedPath = evaluated
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return buildIndexFileFileNotFoundResult(requestedPath), nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return buildIndexFilePathNotFileResult(requestedPath), nil
	}

	repoEntries, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	bestMatch, found := bestContainingRepo(resolvedPath, repoEntries)
	if !found {
		return buildIndexFileNoIndexedFolderResult(requestedPath), nil
	}

	sourceRoot := filepath.Clean(bestMatch.SourceRoot)
	relPath, err := filepath.Rel(sourceRoot, resolvedPath)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(relPath, ".."+string(filepath.Separator)) || relPath == ".." {
		return buildIndexFileSecurityValidationFailureResult(requestedPath), nil
	}
	relPath = filepath.ToSlash(relPath)

	ext := strings.ToLower(filepath.Ext(relPath))
	if _, ok := classifyLanguage(relPath); !ok {
		return buildIndexFileUnsupportedFileTypeResult(ext), nil
	}

	return s.runWithReindexLifecycle(ctx, bestMatch.Repo, sourceRoot, func() (map[string]any, error) {
		content, err := os.ReadFile(resolvedPath)
		if err != nil {
			return buildIndexFileReadFailureResult(err), nil
		}
		fileHash := sha256.Sum256(content)
		hashText := hex.EncodeToString(fileHash[:])
		fileMTime := info.ModTime().UnixNano()

		index, err := store.Load(ctx, bestMatch.Repo)
		if err != nil {
			if errors.Is(err, storage.ErrRepoNotFound) {
				return buildIndexFileLoadFailureResult(bestMatch.Repo), nil
			}
			return nil, err
		}

		if index.Files == nil {
			index.Files = map[string]string{}
		}
		if index.FileMTimes == nil {
			index.FileMTimes = map[string]int64{}
		}
		if head, ok := gitHeadAtPath(ctx, sourceRoot); ok {
			index.GitHead = head
		}

		storedHash, exists := index.Files[relPath]
		if exists && storedHash == hashText {
			if index.FileMTimes[relPath] != fileMTime {
				index.FileMTimes[relPath] = fileMTime
				if err := store.IncrementalSave(ctx, bestMatch.Repo, index, storage.ChangeSet{}); err != nil {
					return nil, err
				}
			}

			return buildIndexFileUnchangedSuccessResult(indexFileUnchangedSuccessResultInput{
				repoID:   bestMatch.Repo,
				relPath:  relPath,
				duration: time.Since(started),
			}), nil
		}

		previousLanguage := ""
		if exists {
			if language, ok := classifyLanguage(relPath); ok {
				previousLanguage = language
			}
		}
		newLanguage, _ := classifyLanguage(relPath)

		if index.Languages == nil {
			index.Languages = map[string]int{}
		}
		if exists {
			if previousLanguage != "" && previousLanguage != newLanguage && index.Languages[previousLanguage] > 0 {
				index.Languages[previousLanguage]--
			}
		} else if newLanguage != "" {
			index.Languages[newLanguage]++
		}

		index.Repo = bestMatch.Repo
		index.SourceRoot = sourceRoot
		if index.DisplayName == "" {
			index.DisplayName = filepath.Base(sourceRoot)
		}
		index.IndexVersion = repoIndexVersion
		index.IndexedAt = time.Now().UTC().Format(time.RFC3339)
		index.Files[relPath] = hashText
		index.FileMTimes[relPath] = fileMTime

		changes := storage.ChangeSet{}
		isNew := !exists
		if isNew {
			changes.New = []string{relPath}
		} else {
			changes.Changed = []string{relPath}
		}
		if err := store.IncrementalSave(ctx, bestMatch.Repo, index, changes); err != nil {
			return nil, err
		}
		if err := s.syncInlineIndexedVectorFiles(
			ctx,
			&index,
			bestMatch.Repo,
			[]indexedFileContent{
				{
					Repo:     bestMatch.Repo,
					Path:     relPath,
					Language: newLanguage,
					Content:  content,
					Fields: map[string]any{
						"source_type": "local",
					},
				},
			},
			changes.Changed,
			changes.New,
			nil,
		); err != nil {
			_ = err // Single-file indexing remains successful even when vector ingestion is unavailable.
		} else if err := store.IncrementalSave(ctx, bestMatch.Repo, index, storage.ChangeSet{}); err != nil {
			return nil, err
		}

		return buildIndexFileIncrementalSuccessResult(indexFileIncrementalSuccessResultInput{
			repoID:    bestMatch.Repo,
			relPath:   relPath,
			isNew:     isNew,
			indexedAt: index.IndexedAt,
			duration:  time.Since(started),
		}), nil
	})
}

func (s *Service) handleListRepos(ctx context.Context, _ map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	repos, err := store.List(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]map[string]any, 0, len(repos))
	for _, repo := range repos {
		entry := map[string]any{
			"repo":          repo.Repo,
			"indexed_at":    repo.IndexedAt,
			"symbol_count":  repo.SymbolCount,
			"file_count":    repo.FileCount,
			"languages":     repo.Languages,
			"index_version": repo.IndexVersion,
		}
		if repo.GitHead != "" {
			entry["git_head"] = repo.GitHead
		}
		if repo.DisplayName != "" {
			entry["display_name"] = repo.DisplayName
		}
		if repo.SourceRoot != "" {
			entry["source_root"] = repo.SourceRoot
		}
		items = append(items, entry)
	}

	return map[string]any{
		"count": len(items),
		"repos": items,
		"_meta": map[string]any{
			"timing_ms": roundMilliseconds(time.Since(started)),
		},
	}, nil
}

func (s *Service) handleResolveRepo(ctx context.Context, arguments map[string]any) (map[string]any, error) {
	store := s.deps.IndexStore
	if store == nil {
		return nil, errors.New("index store dependency is not configured")
	}

	started := time.Now()
	inputPath, _ := arguments["path"].(string)
	inputPath = strings.TrimSpace(inputPath)

	absInput, err := filepath.Abs(inputPath)
	if err != nil {
		absInput = inputPath
	}
	info, statErr := os.Stat(absInput)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return map[string]any{
				"found":   false,
				"indexed": false,
				"error":   fmt.Sprintf("Path does not exist: %s", inputPath),
				"_meta": map[string]any{
					"timing_ms": roundMilliseconds(time.Since(started)),
				},
			}, nil
		}
		return nil, statErr
	}

	candidate := absInput
	if !info.IsDir() {
		candidate = filepath.Dir(candidate)
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err == nil && resolvedCandidate != "" {
		candidate = resolvedCandidate
	}
	candidate = filepath.Clean(candidate)

	candidates := []string{candidate}
	if gitRoot, ok := gitTopLevel(ctx, candidate); ok && !samePath(gitRoot, candidate) {
		candidates = append(candidates, gitRoot)
	}

	for _, current := range candidates {
		repoID := localRepoID(current)
		index, err := store.Load(ctx, repoID)
		if err != nil {
			if errors.Is(err, storage.ErrRepoNotFound) {
				continue
			}
			return nil, err
		}

		return map[string]any{
			"found":        true,
			"indexed":      true,
			"repo":         repoID,
			"source_root":  index.SourceRoot,
			"display_name": index.DisplayName,
			"symbol_count": len(index.Symbols),
			"file_count":   len(index.Files),
			"languages":    index.Languages,
			"indexed_at":   index.IndexedAt,
			"_meta": map[string]any{
				"timing_ms": roundMilliseconds(time.Since(started)),
			},
		}, nil
	}

	bestRepoID := localRepoID(candidates[0])
	return map[string]any{
		"found":   true,
		"indexed": false,
		"repo":    bestRepoID,
		"hint":    "call index_folder to index this path",
		"_meta": map[string]any{
			"timing_ms": roundMilliseconds(time.Since(started)),
		},
	}, nil
}

func indexFolderFromChangedPaths(
	ctx context.Context,
	repoID, resolvedPath string,
	existing storage.RepoIndex,
	rawChangedPaths any,
	nowRFC3339, indexedGitHead string,
	started time.Time,
	store storage.IndexStore,
) (map[string]any, storage.ChangeSet, bool, error) {
	changedPaths, provided := parseIndexFolderChangedPaths(rawChangedPaths, resolvedPath)
	if !provided || len(changedPaths) == 0 {
		return nil, storage.ChangeSet{}, false, nil
	}

	if existing.Files == nil {
		existing.Files = map[string]string{}
	}
	if existing.FileMTimes == nil {
		existing.FileMTimes = map[string]int64{}
	}
	if existing.Languages == nil {
		existing.Languages = map[string]int{}
	}
	if existing.Symbols == nil {
		existing.Symbols = map[string]any{}
	}

	changedSet := map[string]struct{}{}
	newSet := map[string]struct{}{}
	deletedSet := map[string]struct{}{}
	warnings := make([]string, 0, 4)
	mtimeOnlyUpdate := false

	markDeleted := func(relPath string) {
		if _, existed := existing.Files[relPath]; !existed {
			return
		}
		deletedSet[relPath] = struct{}{}
		delete(changedSet, relPath)
		delete(newSet, relPath)
		delete(existing.Files, relPath)
		delete(existing.FileMTimes, relPath)
		decrementLanguageCount(existing.Languages, relPath)
	}

	for _, relPath := range changedPaths {
		if err := ctx.Err(); err != nil {
			return nil, storage.ChangeSet{}, false, err
		}

		absolutePath := filepath.Join(resolvedPath, filepath.FromSlash(relPath))
		info, statErr := os.Stat(absolutePath)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				markDeleted(relPath)
				continue
			}
			warnings = append(warnings, fmt.Sprintf("Failed to stat %s: %v", absolutePath, statErr))
			continue
		}
		if !info.Mode().IsRegular() {
			markDeleted(relPath)
			continue
		}
		if _, ok := classifyLanguage(relPath); !ok {
			markDeleted(relPath)
			continue
		}

		hash, hashErr := hashFile(absolutePath)
		if hashErr != nil {
			warnings = append(warnings, fmt.Sprintf("Failed to read %s: %v", absolutePath, hashErr))
			continue
		}
		mtime := info.ModTime().UnixNano()

		storedHash, existed := existing.Files[relPath]
		if !existed {
			newSet[relPath] = struct{}{}
			delete(changedSet, relPath)
			delete(deletedSet, relPath)
			existing.Files[relPath] = hash
			existing.FileMTimes[relPath] = mtime
			incrementLanguageCount(existing.Languages, relPath)
			continue
		}

		if storedHash == hash {
			if existing.FileMTimes[relPath] != mtime {
				existing.FileMTimes[relPath] = mtime
				mtimeOnlyUpdate = true
			}
			continue
		}

		changedSet[relPath] = struct{}{}
		delete(newSet, relPath)
		delete(deletedSet, relPath)
		existing.Files[relPath] = hash
		existing.FileMTimes[relPath] = mtime
	}

	changed := sortedSetMembers(changedSet)
	created := sortedSetMembers(newSet)
	deleted := sortedSetMembers(deletedSet)

	if len(changed) == 0 && len(created) == 0 && len(deleted) == 0 && !mtimeOnlyUpdate {
		return buildIndexFolderNoChangeResult(indexFolderNoChangeResultInput{
			repoID:       repoID,
			resolvedPath: resolvedPath,
			warnings:     warnings,
			duration:     time.Since(started),
		}), storage.ChangeSet{}, true, nil
	}

	existing.Repo = repoID
	existing.IndexedAt = nowRFC3339
	existing.SourceRoot = resolvedPath
	existing.DisplayName = filepath.Base(resolvedPath)
	existing.IndexVersion = repoIndexVersion
	existing.GitHead = indexedGitHead

	changeSet := storage.ChangeSet{
		Changed: changed,
		New:     created,
		Deleted: deleted,
	}
	if err := store.IncrementalSave(ctx, repoID, existing, changeSet); err != nil {
		return nil, storage.ChangeSet{}, true, err
	}

	if len(changed) == 0 && len(created) == 0 && len(deleted) == 0 {
		return buildIndexFolderNoChangeResult(indexFolderNoChangeResultInput{
			repoID:       repoID,
			resolvedPath: resolvedPath,
			warnings:     warnings,
			duration:     time.Since(started),
		}), storage.ChangeSet{}, true, nil
	}

	return buildIndexFolderIncrementalSuccessResult(indexFolderIncrementalSuccessResultInput{
		repoID:            repoID,
		resolvedPath:      resolvedPath,
		changedCount:      len(changed),
		newCount:          len(created),
		deletedCount:      len(deleted),
		symbolCount:       len(existing.Symbols),
		indexedAt:         nowRFC3339,
		duration:          time.Since(started),
		includeSkipCounts: false,
		warnings:          warnings,
	}), changeSet, true, nil
}

func parseIndexFolderChangedPaths(rawChangedPaths any, root string) ([]string, bool) {
	if rawChangedPaths == nil {
		return nil, false
	}

	items := make([]any, 0)
	switch typed := rawChangedPaths.(type) {
	case []any:
		items = append(items, typed...)
	case []map[string]any:
		for _, item := range typed {
			items = append(items, item)
		}
	default:
		return nil, false
	}

	root = filepath.Clean(root)
	unique := map[string]struct{}{}
	for _, item := range items {
		changeType, pathText, ok := parseChangedPathItem(item)
		if !ok {
			continue
		}
		if changeType != "added" && changeType != "modified" && changeType != "deleted" {
			continue
		}

		absolutePath := pathText
		if !filepath.IsAbs(absolutePath) {
			absolutePath = filepath.Join(root, absolutePath)
		}
		absolutePath, err := filepath.Abs(absolutePath)
		if err != nil {
			continue
		}

		relPath, err := filepath.Rel(root, absolutePath)
		if err != nil {
			continue
		}
		if relPath == "." || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
			continue
		}
		relPath = filepath.ToSlash(filepath.Clean(relPath))
		if relPath == "." || relPath == "" || hasHiddenPathSegment(relPath) {
			continue
		}
		unique[relPath] = struct{}{}
	}

	paths := sortedSetMembers(unique)
	return paths, true
}

func parseChangedPathItem(item any) (changeType string, path string, ok bool) {
	switch typed := item.(type) {
	case map[string]any:
		changeType, _ = typed["change_type"].(string)
		if strings.TrimSpace(changeType) == "" {
			changeType, _ = typed["type"].(string)
		}
		path, ok = typed["path"].(string)
		if !ok {
			return "", "", false
		}
		return strings.ToLower(strings.TrimSpace(changeType)), strings.TrimSpace(path), strings.TrimSpace(path) != ""
	case map[string]string:
		changeType = typed["change_type"]
		if strings.TrimSpace(changeType) == "" {
			changeType = typed["type"]
		}
		path = typed["path"]
		path = strings.TrimSpace(path)
		return strings.ToLower(strings.TrimSpace(changeType)), path, path != ""
	case []any:
		if len(typed) < 2 {
			return "", "", false
		}
		typeText, _ := typed[0].(string)
		pathText, _ := typed[1].(string)
		pathText = strings.TrimSpace(pathText)
		if pathText == "" {
			return "", "", false
		}
		return strings.ToLower(strings.TrimSpace(typeText)), pathText, true
	default:
		return "", "", false
	}
}

func vectorUpsertCandidatePaths(pathSets ...[]string) []string {
	if len(pathSets) == 0 {
		return nil
	}

	combined := make([]string, 0)
	for _, paths := range pathSets {
		combined = append(combined, paths...)
	}
	return normalizeUniqueRepoPaths(combined)
}

func normalizeUniqueRepoPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}

	unique := map[string]struct{}{}
	for _, rawPath := range paths {
		normalized := normalizeChunkPath(rawPath)
		if normalized == "" {
			continue
		}
		unique[normalized] = struct{}{}
	}
	if len(unique) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(unique))
	for relPath := range unique {
		normalized = append(normalized, relPath)
	}
	sort.Strings(normalized)
	return normalized
}

func collectIndexableRemoteFiles(
	repoID string,
	tree map[string][]byte,
	relPaths []string,
) ([]indexedFileContent, error) {
	paths := normalizeUniqueRepoPaths(relPaths)
	if len(paths) == 0 {
		return nil, nil
	}

	normalizedTree := map[string][]byte{}
	rawPaths := make([]string, 0, len(tree))
	for rawPath := range tree {
		rawPaths = append(rawPaths, rawPath)
	}
	sort.Strings(rawPaths)
	for _, rawPath := range rawPaths {
		normalizedPath, _ := normalizeRemoteTreePath(rawPath)
		if normalizedPath == "" {
			continue
		}
		if _, exists := normalizedTree[normalizedPath]; exists {
			continue
		}
		normalizedTree[normalizedPath] = tree[rawPath]
	}

	files := make([]indexedFileContent, 0, len(paths))
	for _, relPath := range paths {
		language, ok := classifyLanguage(relPath)
		if !ok {
			continue
		}
		content, ok := normalizedTree[relPath]
		if !ok {
			return nil, fmt.Errorf("missing remote file content for %q", relPath)
		}
		files = append(files, indexedFileContent{
			Repo:     repoID,
			Path:     relPath,
			Language: language,
			Content:  content,
			Fields: map[string]any{
				"source_type": "remote",
			},
		})
	}

	return files, nil
}

func collectIndexableLocalFiles(
	ctx context.Context,
	repoID string,
	root string,
	relPaths []string,
) ([]indexedFileContent, error) {
	paths := normalizeUniqueRepoPaths(relPaths)
	if len(paths) == 0 {
		return nil, nil
	}

	root = filepath.Clean(root)
	files := make([]indexedFileContent, 0, len(paths))
	for _, relPath := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		language, ok := classifyLanguage(relPath)
		if !ok {
			continue
		}
		absolutePath := filepath.Clean(filepath.Join(root, filepath.FromSlash(relPath)))
		if !pathWithin(root, absolutePath) {
			continue
		}

		info, err := os.Stat(absolutePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat local indexed file %q: %w", absolutePath, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}

		content, err := os.ReadFile(absolutePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read local indexed file %q: %w", absolutePath, err)
		}

		files = append(files, indexedFileContent{
			Repo:     repoID,
			Path:     relPath,
			Language: language,
			Content:  content,
			Fields: map[string]any{
				"source_type": "local",
			},
		})
	}

	return files, nil
}

func (s *Service) upsertVectorsFromRemoteTree(
	ctx context.Context,
	namespace string,
	tree map[string][]byte,
	relPaths []string,
) error {
	_, err := s.upsertVectorsFromRemoteTreeWithChunkIDs(ctx, namespace, tree, relPaths)
	return err
}

func (s *Service) upsertVectorsFromRemoteTreeWithChunkIDs(
	ctx context.Context,
	namespace string,
	tree map[string][]byte,
	relPaths []string,
) (map[string][]string, error) {
	files, err := collectIndexableRemoteFiles(namespace, tree, relPaths)
	if err != nil {
		return nil, fmt.Errorf("prepare remote chunk vectors for %s: %w", namespace, err)
	}
	return s.upsertIndexedVectorFilesWithChunkIDs(ctx, namespace, files)
}

func (s *Service) upsertVectorsFromLocalFilesystem(
	ctx context.Context,
	namespace string,
	root string,
	relPaths []string,
) error {
	_, err := s.upsertVectorsFromLocalFilesystemWithChunkIDs(ctx, namespace, root, relPaths)
	return err
}

func (s *Service) upsertVectorsFromLocalFilesystemWithChunkIDs(
	ctx context.Context,
	namespace string,
	root string,
	relPaths []string,
) (map[string][]string, error) {
	files, err := collectIndexableLocalFiles(ctx, namespace, root, relPaths)
	if err != nil {
		return nil, fmt.Errorf("prepare local chunk vectors for %s: %w", namespace, err)
	}
	return s.upsertIndexedVectorFilesWithChunkIDs(ctx, namespace, files)
}

func (s *Service) upsertIndexedVectorFiles(
	ctx context.Context,
	namespace string,
	files []indexedFileContent,
) error {
	_, err := s.upsertIndexedVectorFilesWithChunkIDs(ctx, namespace, files)
	return err
}

func (s *Service) upsertIndexedVectorFilesWithChunkIDs(
	ctx context.Context,
	namespace string,
	files []indexedFileContent,
) (map[string][]string, error) {
	if len(files) == 0 {
		return map[string][]string{}, nil
	}

	embedder := s.deps.Embedder
	vectorBackend := s.deps.VectorBackend
	if embedder == nil || vectorBackend == nil {
		return map[string][]string{}, nil
	}

	chunks := buildDeterministicChunkMetadata(files)
	if len(chunks) == 0 {
		return map[string][]string{}, nil
	}
	chunkIDsByPath := collectChunkIDsByPath(chunks)

	texts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		texts = append(texts, chunk.ChunkText)
	}

	embeddings, err := embedder.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embed indexed chunks for %s: %w", namespace, err)
	}
	if len(embeddings) != len(chunks) {
		return nil, fmt.Errorf(
			"embed indexed chunks for %s: embedding count mismatch (expected %d, got %d)",
			namespace,
			len(chunks),
			len(embeddings),
		)
	}

	records, err := buildVectorUpsertRecords(namespace, chunks, embeddings)
	if err != nil {
		return nil, fmt.Errorf("prepare vector records for %s: %w", namespace, err)
	}
	if len(records) == 0 {
		return map[string][]string{}, nil
	}

	if _, err := vectorBackend.Upsert(ctx, indexing.VectorUpsertRequest{
		Namespace: namespace,
		Records:   records,
	}); err != nil {
		return nil, fmt.Errorf("upsert chunk vectors for %s: %w", namespace, err)
	}

	return chunkIDsByPath, nil
}

func (s *Service) syncRemoteIndexVectors(
	ctx context.Context,
	index *storage.RepoIndex,
	namespace string,
	tree map[string][]byte,
	changed []string,
	created []string,
	deleted []string,
) error {
	upsertCandidates := vectorUpsertCandidatePaths(changed, created)
	if len(upsertCandidates) > 0 && s.deps.Embedder == nil {
		upsertCandidates = nil
	}
	deletedCandidates := normalizeUniqueRepoPaths(deleted)

	chunkIDsByPath := map[string][]string{}
	if len(upsertCandidates) > 0 {
		var err error
		chunkIDsByPath, err = s.upsertVectorsFromRemoteTreeWithChunkIDs(ctx, namespace, tree, upsertCandidates)
		if err != nil {
			return err
		}
	}
	return s.syncVectorIndexMetadata(ctx, index, namespace, upsertCandidates, deletedCandidates, chunkIDsByPath)
}

func (s *Service) syncLocalIndexVectors(
	ctx context.Context,
	index *storage.RepoIndex,
	namespace string,
	root string,
	changed []string,
	created []string,
	deleted []string,
) error {
	upsertCandidates := vectorUpsertCandidatePaths(changed, created)
	if len(upsertCandidates) > 0 && s.deps.Embedder == nil {
		upsertCandidates = nil
	}
	deletedCandidates := normalizeUniqueRepoPaths(deleted)

	chunkIDsByPath := map[string][]string{}
	if len(upsertCandidates) > 0 {
		var err error
		chunkIDsByPath, err = s.upsertVectorsFromLocalFilesystemWithChunkIDs(ctx, namespace, root, upsertCandidates)
		if err != nil {
			return err
		}
	}
	return s.syncVectorIndexMetadata(ctx, index, namespace, upsertCandidates, deletedCandidates, chunkIDsByPath)
}

func (s *Service) syncInlineIndexedVectorFiles(
	ctx context.Context,
	index *storage.RepoIndex,
	namespace string,
	files []indexedFileContent,
	changed []string,
	created []string,
	deleted []string,
) error {
	upsertCandidates := vectorUpsertCandidatePaths(changed, created)
	if len(upsertCandidates) > 0 && s.deps.Embedder == nil {
		upsertCandidates = nil
	}
	deletedCandidates := normalizeUniqueRepoPaths(deleted)

	chunkIDsByPath := map[string][]string{}
	if len(upsertCandidates) > 0 {
		var err error
		chunkIDsByPath, err = s.upsertIndexedVectorFilesWithChunkIDs(ctx, namespace, files)
		if err != nil {
			return err
		}
	}
	return s.syncVectorIndexMetadata(ctx, index, namespace, upsertCandidates, deletedCandidates, chunkIDsByPath)
}

func (s *Service) syncVectorIndexMetadata(
	ctx context.Context,
	index *storage.RepoIndex,
	namespace string,
	upsertedPaths []string,
	deletedPaths []string,
	upsertedChunkIDs map[string][]string,
) error {
	vectorBackend := s.deps.VectorBackend
	if vectorBackend == nil || index == nil {
		return nil
	}

	existingChunkIDs := loadVectorChunkIDsByPath(index.ContextMetadata)
	if len(existingChunkIDs) == 0 && len(upsertedPaths) == 0 && len(deletedPaths) == 0 {
		return nil
	}

	staleChunkIDs := make([]string, 0)
	for _, path := range deletedPaths {
		staleChunkIDs = append(staleChunkIDs, existingChunkIDs[path]...)
	}
	for _, path := range upsertedPaths {
		previous := existingChunkIDs[path]
		next := upsertedChunkIDs[path]
		staleChunkIDs = append(staleChunkIDs, diffVectorIDs(previous, next)...)
	}
	staleChunkIDs = normalizeUniqueVectorIDs(staleChunkIDs)
	if len(staleChunkIDs) > 0 {
		if err := s.deleteVectorRecordIDs(ctx, namespace, staleChunkIDs); err != nil {
			return err
		}
	}

	for _, path := range deletedPaths {
		delete(existingChunkIDs, path)
	}
	for _, path := range upsertedPaths {
		next := normalizeUniqueVectorIDs(upsertedChunkIDs[path])
		if len(next) == 0 {
			delete(existingChunkIDs, path)
			continue
		}
		existingChunkIDs[path] = next
	}
	writeVectorChunkIDsByPath(index, existingChunkIDs)
	return nil
}

func (s *Service) deleteVectorRecordIDs(
	ctx context.Context,
	namespace string,
	recordIDs []string,
) error {
	vectorBackend := s.deps.VectorBackend
	if vectorBackend == nil {
		return nil
	}

	namespace = strings.TrimSpace(namespace)
	recordIDs = normalizeUniqueVectorIDs(recordIDs)
	if namespace == "" || len(recordIDs) == 0 {
		return nil
	}

	if _, err := vectorBackend.Delete(ctx, indexing.VectorDeleteRequest{
		Namespace: namespace,
		IDs:       recordIDs,
	}); err != nil {
		return fmt.Errorf("delete chunk vectors for %s: %w", namespace, err)
	}
	return nil
}

func (s *Service) deleteVectorNamespace(ctx context.Context, namespace string) error {
	vectorBackend := s.deps.VectorBackend
	if vectorBackend == nil {
		return nil
	}

	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return nil
	}

	if _, err := vectorBackend.DeleteNamespace(ctx, indexing.VectorDeleteNamespaceRequest{
		Namespace: namespace,
	}); err != nil {
		return fmt.Errorf("delete vector namespace for %s: %w", namespace, err)
	}
	return nil
}

func buildVectorUpsertRecords(
	namespace string,
	chunks []indexing.VectorMetadata,
	embeddings [][]float32,
) ([]indexing.VectorRecord, error) {
	if len(chunks) != len(embeddings) {
		return nil, fmt.Errorf("chunk/embedding count mismatch: chunks=%d embeddings=%d", len(chunks), len(embeddings))
	}

	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return nil, errors.New("vector namespace must be non-empty")
	}

	records := make([]indexing.VectorRecord, 0, len(chunks))
	for i, chunk := range chunks {
		recordID := strings.TrimSpace(chunk.ChunkID)
		if recordID == "" {
			return nil, fmt.Errorf("chunk at index %d has empty chunk id", i)
		}
		records = append(records, indexing.VectorRecord{
			ID:        recordID,
			Namespace: namespace,
			Embedding: cloneEmbeddingVector(embeddings[i]),
			Metadata:  cloneVectorMetadata(chunk),
		})
	}

	return records, nil
}

func cloneVectorMetadata(metadata indexing.VectorMetadata) indexing.VectorMetadata {
	cloned := metadata
	cloned.Fields = cloneChunkFields(metadata.Fields)
	return cloned
}

func cloneEmbeddingVector(values []float32) []float32 {
	cloned := make([]float32, len(values))
	copy(cloned, values)
	return cloned
}

func collectChunkIDsByPath(chunks []indexing.VectorMetadata) map[string][]string {
	if len(chunks) == 0 {
		return map[string][]string{}
	}

	byPath := map[string][]string{}
	for _, chunk := range chunks {
		relPath := normalizeChunkPath(chunk.Path)
		chunkID := strings.TrimSpace(chunk.ChunkID)
		if relPath == "" || chunkID == "" {
			continue
		}
		byPath[relPath] = append(byPath[relPath], chunkID)
	}
	for relPath, ids := range byPath {
		byPath[relPath] = normalizeUniqueVectorIDs(ids)
	}
	return byPath
}

func loadVectorChunkIDsByPath(contextMetadata map[string]any) map[string][]string {
	if len(contextMetadata) == 0 {
		return map[string][]string{}
	}

	raw, ok := contextMetadata[vectorChunkIDsContextKey]
	if !ok {
		return map[string][]string{}
	}

	parsed := map[string][]string{}
	switch typed := raw.(type) {
	case map[string][]string:
		for rawPath, rawIDs := range typed {
			relPath := normalizeChunkPath(rawPath)
			if relPath == "" {
				continue
			}
			ids := normalizeUniqueVectorIDs(rawIDs)
			if len(ids) == 0 {
				continue
			}
			parsed[relPath] = ids
		}
	case map[string]any:
		for rawPath, value := range typed {
			relPath := normalizeChunkPath(rawPath)
			if relPath == "" {
				continue
			}
			ids := parseVectorIDList(value)
			if len(ids) == 0 {
				continue
			}
			parsed[relPath] = ids
		}
	}
	return parsed
}

func parseVectorIDList(raw any) []string {
	switch typed := raw.(type) {
	case []string:
		return normalizeUniqueVectorIDs(typed)
	case []any:
		ids := make([]string, 0, len(typed))
		for _, value := range typed {
			text := strings.TrimSpace(fmt.Sprintf("%v", value))
			if text == "" || text == "<nil>" {
				continue
			}
			ids = append(ids, text)
		}
		return normalizeUniqueVectorIDs(ids)
	default:
		return nil
	}
}

func writeVectorChunkIDsByPath(index *storage.RepoIndex, byPath map[string][]string) {
	if index == nil {
		return
	}
	if index.ContextMetadata == nil {
		index.ContextMetadata = map[string]any{}
	}

	cleaned := map[string][]string{}
	for rawPath, rawIDs := range byPath {
		relPath := normalizeChunkPath(rawPath)
		if relPath == "" {
			continue
		}
		ids := normalizeUniqueVectorIDs(rawIDs)
		if len(ids) == 0 {
			continue
		}
		cleaned[relPath] = ids
	}
	if len(cleaned) == 0 {
		delete(index.ContextMetadata, vectorChunkIDsContextKey)
		return
	}
	index.ContextMetadata[vectorChunkIDsContextKey] = cleaned
}

func diffVectorIDs(previous []string, next []string) []string {
	if len(previous) == 0 {
		return nil
	}
	nextSet := map[string]struct{}{}
	for _, id := range normalizeUniqueVectorIDs(next) {
		nextSet[id] = struct{}{}
	}

	stale := make([]string, 0, len(previous))
	for _, id := range normalizeUniqueVectorIDs(previous) {
		if _, ok := nextSet[id]; ok {
			continue
		}
		stale = append(stale, id)
	}
	return stale
}

func normalizeUniqueVectorIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}

	unique := map[string]struct{}{}
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		unique[id] = struct{}{}
	}
	if len(unique) == 0 {
		return nil
	}

	out := make([]string, 0, len(unique))
	for id := range unique {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func sortedSetMembers(values map[string]struct{}) []string {
	if len(values) == 0 {
		return []string{}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func incrementLanguageCount(languages map[string]int, relPath string) {
	language, ok := classifyLanguage(relPath)
	if !ok {
		return
	}
	languages[language]++
}

func decrementLanguageCount(languages map[string]int, relPath string) {
	language, ok := classifyLanguage(relPath)
	if !ok {
		return
	}
	count := languages[language]
	if count <= 1 {
		delete(languages, language)
		return
	}
	languages[language] = count - 1
}

func hasHiddenPathSegment(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func appendWarnings(payload map[string]any, warnings []string) {
	if payload == nil || len(warnings) == 0 {
		return
	}
	existing, _ := payload["warnings"].([]string)
	if len(existing) == 0 {
		payload["warnings"] = cloneWarnings(warnings)
		return
	}
	merged := append(cloneWarnings(existing), warnings...)
	payload["warnings"] = merged
}

func cloneWarnings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func isTrustedFolderPath(folderPath string, trustedFolders []string, whitelistMode bool) bool {
	if len(trustedFolders) == 0 {
		return false
	}

	folderPath = filepath.Clean(folderPath)
	inList := false
	for _, entry := range trustedFolders {
		trustedPath := filepath.Clean(strings.TrimSpace(entry))
		if trustedPath == "" {
			continue
		}
		if samePath(folderPath, trustedPath) || pathWithin(trustedPath, folderPath) {
			inList = true
			break
		}
	}
	if whitelistMode {
		return inList
	}
	return !inList
}

func pathComponentCount(path string) int {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" {
		return 0
	}

	count := 0
	if filepath.IsAbs(cleaned) {
		count++
	}
	volume := filepath.VolumeName(cleaned)
	if volume != "" {
		count++
		cleaned = strings.TrimPrefix(cleaned, volume)
	}

	cleaned = strings.Trim(cleaned, string(filepath.Separator))
	if cleaned == "" {
		return count
	}
	return count + len(strings.Split(cleaned, string(filepath.Separator)))
}

func isAbsoluteWithUserHome(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	if filepath.IsAbs(trimmed) {
		return true
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "~\\") {
		return true
	}
	return false
}

func expandUserHomePath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "~\\") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if trimmed == "~" {
			return homeDir, nil
		}
		suffix := strings.TrimLeft(trimmed[1:], "/\\")
		if suffix == "" {
			return homeDir, nil
		}
		return filepath.Join(homeDir, suffix), nil
	}
	return trimmed, nil
}

type gitignoreScope struct {
	root     string
	patterns []string
}

func discoverFolderFiles(
	ctx context.Context,
	root string,
	followSymlinks bool,
	extraIgnorePatterns []string,
	excludedSecretPatterns []string,
	maxFiles int,
) ([]string, map[string]string, map[string]int64, map[string]int, []string, map[string]int, error) {
	root = filepath.Clean(root)
	if evaluatedRoot, err := filepath.EvalSymlinks(root); err == nil && strings.TrimSpace(evaluatedRoot) != "" {
		root = filepath.Clean(evaluatedRoot)
	}

	sourceFiles := make([]string, 0, 256)
	fileHashes := map[string]string{}
	fileMTimes := map[string]int64{}
	languageCounts := map[string]int{}
	warnings := make([]string, 0, 8)
	skipCounts := map[string]int{
		"skip_dir":        0,
		"skip_file":       0,
		"symlink":         0,
		"symlink_escape":  0,
		"path_traversal":  0,
		"gitignore":       0,
		"extra_ignore":    0,
		"secret":          0,
		"wrong_extension": 0,
		"too_large":       0,
		"unreadable":      0,
		"binary":          0,
		"file_limit":      0,
	}

	gitignoreScopes := make([]gitignoreScope, 0, 8)
	loadGitignore := func(directory string) {
		content, err := os.ReadFile(filepath.Join(directory, ".gitignore"))
		if err != nil {
			return
		}
		patterns := parseGitignorePatterns(content)
		if len(patterns) == 0 {
			return
		}
		gitignoreScopes = append(gitignoreScopes, gitignoreScope{
			root:     filepath.Clean(directory),
			patterns: patterns,
		})
	}
	loadGitignore(root)
	effectiveIgnorePatterns := normalizeIgnorePatterns(extraIgnorePatterns)

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			warnings = append(warnings, fmt.Sprintf("Failed to walk %s: %v", path, walkErr))
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}

		name := entry.Name()
		if entry.IsDir() {
			if _, skip := skippedDirectories[name]; skip {
				skipCounts["skip_dir"]++
				return filepath.SkipDir
			}
			if path != root {
				loadGitignore(path)
			}
			return nil
		}

		mode := entry.Type()
		resolvedPath := filepath.Clean(path)
		readPath := path
		if mode&os.ModeSymlink != 0 {
			if !followSymlinks {
				skipCounts["symlink"]++
				return nil
			}
			evaluatedPath, err := filepath.EvalSymlinks(path)
			if err != nil || strings.TrimSpace(evaluatedPath) == "" {
				skipCounts["unreadable"]++
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("Failed to resolve %s: %v", path, err))
				}
				return nil
			}
			resolvedPath = filepath.Clean(evaluatedPath)
			readPath = resolvedPath
			if !pathWithin(root, resolvedPath) {
				skipCounts["symlink_escape"]++
				warnings = append(warnings, fmt.Sprintf("Skipped symlink escape: %s", path))
				return nil
			}
		}
		if !pathWithin(root, resolvedPath) {
			skipCounts["path_traversal"]++
			warnings = append(warnings, fmt.Sprintf("Skipped path traversal: %s", path))
			return nil
		}

		isRegular := mode.IsRegular()
		if !isRegular && mode&os.ModeSymlink != 0 {
			info, err := os.Stat(readPath)
			if err == nil {
				isRegular = info.Mode().IsRegular()
			}
		}
		if !isRegular && mode&os.ModeSymlink == 0 {
			info, err := entry.Info()
			if err == nil {
				isRegular = info.Mode().IsRegular()
			}
		}
		if !isRegular {
			skipCounts["skip_file"]++
			return nil
		}

		relative, err := filepath.Rel(root, resolvedPath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Failed to compute relative path for %s: %v", resolvedPath, err))
			return nil
		}
		relative = filepath.ToSlash(relative)

		if matchesScopedGitignorePattern(resolvedPath, gitignoreScopes) {
			skipCounts["gitignore"]++
			return nil
		}
		if matchesIgnorePattern(relative, effectiveIgnorePatterns) {
			skipCounts["extra_ignore"]++
			return nil
		}
		if isLikelySecretPathWithExclusions(relative, excludedSecretPatterns) {
			skipCounts["secret"]++
			warnings = append(warnings, fmt.Sprintf("Skipped secret file: %s", relative))
			return nil
		}

		if _, exists := fileHashes[relative]; exists {
			return nil
		}

		language, ok := classifyLanguage(relative)
		if !ok {
			skipCounts["wrong_extension"]++
			return nil
		}

		fileInfo, err := os.Stat(readPath)
		if err != nil {
			skipCounts["unreadable"]++
			warnings = append(warnings, fmt.Sprintf("Failed to stat %s: %v", readPath, err))
			return nil
		}
		if fileInfo.Size() > defaultMaxDiscoveryFileSize {
			skipCounts["too_large"]++
			return nil
		}

		content, err := os.ReadFile(readPath)
		if err != nil {
			skipCounts["unreadable"]++
			warnings = append(warnings, fmt.Sprintf("Failed to read %s: %v", readPath, err))
			return nil
		}
		if isLikelyBinaryContent(content) {
			skipCounts["binary"]++
			warnings = append(warnings, fmt.Sprintf("Skipped binary file: %s", relative))
			return nil
		}
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])

		sourceFiles = append(sourceFiles, relative)
		fileHashes[relative] = hash
		fileMTimes[relative] = fileInfo.ModTime().UnixNano()
		languageCounts[language]++
		return nil
	})
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}

	sort.Strings(sourceFiles)
	if maxFiles <= 0 {
		maxFiles = defaultMaxFolderFiles
	}
	if len(sourceFiles) > maxFiles {
		skipCounts["file_limit"] = len(sourceFiles) - maxFiles
		sort.SliceStable(sourceFiles, func(i, j int) bool {
			return compareFolderDiscoveryPriority(sourceFiles[i], sourceFiles[j])
		})
		sourceFiles = append([]string(nil), sourceFiles[:maxFiles]...)

		truncatedHashes := make(map[string]string, len(sourceFiles))
		truncatedMTimes := make(map[string]int64, len(sourceFiles))
		truncatedLanguageCounts := map[string]int{}
		for _, relPath := range sourceFiles {
			if hash, ok := fileHashes[relPath]; ok {
				truncatedHashes[relPath] = hash
			}
			if mtime, ok := fileMTimes[relPath]; ok {
				truncatedMTimes[relPath] = mtime
			}
			if language, ok := classifyLanguage(relPath); ok {
				truncatedLanguageCounts[language]++
			}
		}
		fileHashes = truncatedHashes
		fileMTimes = truncatedMTimes
		languageCounts = truncatedLanguageCounts
	}
	return sourceFiles, fileHashes, fileMTimes, languageCounts, warnings, skipCounts, nil
}

func compareFolderDiscoveryPriority(left, right string) bool {
	leftRank, leftDepth, leftPath := folderDiscoveryPriority(left)
	rightRank, rightDepth, rightPath := folderDiscoveryPriority(right)
	if leftRank != rightRank {
		return leftRank < rightRank
	}
	if leftDepth != rightDepth {
		return leftDepth < rightDepth
	}
	return leftPath < rightPath
}

func folderDiscoveryPriority(relPath string) (rank int, depth int, normalizedPath string) {
	normalized := filepath.ToSlash(filepath.Clean(strings.TrimSpace(relPath)))
	priorityDirs := []string{"src/", "lib/", "pkg/", "cmd/", "internal/"}
	rank = len(priorityDirs)
	for index, prefix := range priorityDirs {
		if strings.HasPrefix(normalized, prefix) {
			rank = index
			break
		}
	}
	return rank, strings.Count(normalized, "/"), normalized
}

func matchesScopedGitignorePattern(path string, scopes []gitignoreScope) bool {
	if len(scopes) == 0 {
		return false
	}
	for _, scope := range scopes {
		if !pathWithin(scope.root, path) {
			continue
		}
		relPath, err := filepath.Rel(scope.root, path)
		if err != nil {
			continue
		}
		relPath = filepath.ToSlash(filepath.Clean(relPath))
		if relPath == "." || relPath == "" {
			continue
		}
		if matchesIgnorePattern(relPath, scope.patterns) {
			return true
		}
	}
	return false
}

func classifyLanguage(relPath string) (string, bool) {
	lower := strings.ToLower(relPath)
	if strings.HasSuffix(lower, ".blade.php") {
		return sourceExtensions[".blade.php"], true
	}
	ext := strings.ToLower(filepath.Ext(lower))
	language, ok := sourceExtensions[ext]
	return language, ok
}

func hashFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func diffFileHashes(existing, current map[string]string) ([]string, []string, []string) {
	changed := make([]string, 0)
	created := make([]string, 0)
	deleted := make([]string, 0)

	for file, hash := range current {
		existingHash, ok := existing[file]
		if !ok {
			created = append(created, file)
			continue
		}
		if existingHash != hash {
			changed = append(changed, file)
		}
	}
	for file := range existing {
		if _, ok := current[file]; !ok {
			deleted = append(deleted, file)
		}
	}

	sort.Strings(changed)
	sort.Strings(created)
	sort.Strings(deleted)
	return changed, created, deleted
}

func sampleFiles(files []string, limit int) []string {
	if limit <= 0 || len(files) <= limit {
		return append([]string(nil), files...)
	}
	return append([]string(nil), files[:limit]...)
}

func localRepoID(path string) string {
	resolved := filepath.Clean(path)
	base := filepath.Base(resolved)
	digest := sha1.Sum([]byte(resolved))
	hashPrefix := hex.EncodeToString(digest[:])[:8]
	return fmt.Sprintf("local/%s-%s", base, hashPrefix)
}

func gitTopLevel(ctx context.Context, path string) (string, bool) {
	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	command := exec.CommandContext(gitCtx, "git", "rev-parse", "--show-toplevel")
	command.Dir = path
	output, err := command.Output()
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", false
	}

	resolved, err := filepath.EvalSymlinks(root)
	if err == nil && resolved != "" {
		root = resolved
	}
	return filepath.Clean(root), true
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func bestContainingRepo(filePath string, repos []storage.RepoMetadata) (storage.RepoMetadata, bool) {
	best := storage.RepoMetadata{}
	bestLen := -1
	for _, repo := range repos {
		root := strings.TrimSpace(repo.SourceRoot)
		if root == "" {
			continue
		}
		resolvedRoot := filepath.Clean(root)
		if evaluated, err := filepath.EvalSymlinks(resolvedRoot); err == nil && evaluated != "" {
			resolvedRoot = evaluated
		}
		if !pathWithin(resolvedRoot, filePath) {
			continue
		}
		if len(resolvedRoot) > bestLen {
			best = repo
			best.SourceRoot = resolvedRoot
			bestLen = len(resolvedRoot)
		}
	}
	return best, bestLen >= 0
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func boolArg(arguments map[string]any, key string, fallback bool) bool {
	value, ok := arguments[key]
	if !ok {
		return fallback
	}
	typed, ok := value.(bool)
	if !ok {
		return fallback
	}
	return typed
}

func roundSeconds(duration time.Duration) float64 {
	return math.Round(duration.Seconds()*100) / 100
}

func roundMilliseconds(duration time.Duration) float64 {
	return math.Round(duration.Seconds()*10000) / 10
}

func cloneFileHashes(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneFileMTimes(in map[string]int64) map[string]int64 {
	if in == nil {
		return map[string]int64{}
	}
	out := make(map[string]int64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneLanguageCounts(in map[string]int) map[string]int {
	if in == nil {
		return map[string]int{}
	}
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
