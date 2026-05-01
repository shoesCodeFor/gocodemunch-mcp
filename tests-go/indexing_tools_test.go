package testsgo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/server"
)

func TestIndexListResolveRoundTrip(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	writeFile(t, filepath.Join(repoRoot, "main.go"), "package main\n\nfunc main() {}\n")
	writeFile(t, filepath.Join(repoRoot, "pkg", "util.py"), "def util():\n    return 1\n")
	writeFile(t, filepath.Join(repoRoot, "README.md"), "# ignored extension\n")

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
		toolCallRequest(3, "list_repos", map[string]any{}),
		toolCallRequest(4, "resolve_repo", map[string]any{
			"path": filepath.Join(repoRoot, "main.go"),
		}),
		toolCallRequest(5, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      true,
			"use_ai_summaries": false,
		}),
	})

	indexPayload := toolPayload(t, responses[1])
	if ok := boolField(indexPayload, "success"); !ok {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")
	if !strings.HasPrefix(repoID, "local/") {
		t.Fatalf("expected local repo id, got %q", repoID)
	}

	listPayload := toolPayload(t, responses[2])
	if got := intField(listPayload, "count"); got != 1 {
		t.Fatalf("expected one repo, got %d (%#v)", got, listPayload)
	}
	repos := mapSliceField(listPayload, "repos")
	if len(repos) != 1 {
		t.Fatalf("expected one repo entry, got %d", len(repos))
	}
	if got := stringField(repos[0], "repo"); got != repoID {
		t.Fatalf("unexpected listed repo id %q (want %q)", got, repoID)
	}
	meta := mapField(listPayload, "_meta")
	if _, ok := meta["timing_ms"]; !ok {
		t.Fatalf("list_repos missing timing meta: %#v", listPayload)
	}

	resolvePayload := toolPayload(t, responses[3])
	if !boolField(resolvePayload, "found") || !boolField(resolvePayload, "indexed") {
		t.Fatalf("resolve_repo expected indexed=true, got %#v", resolvePayload)
	}
	if got := stringField(resolvePayload, "repo"); got != repoID {
		t.Fatalf("resolve_repo repo mismatch: got %q want %q", got, repoID)
	}

	noChangePayload := toolPayload(t, responses[4])
	if got := stringField(noChangePayload, "message"); got != "No changes detected" {
		t.Fatalf("unexpected no-change message: %#v", noChangePayload)
	}
	if intField(noChangePayload, "changed") != 0 || intField(noChangePayload, "new") != 0 || intField(noChangePayload, "deleted") != 0 {
		t.Fatalf("expected no deltas in incremental no-change path: %#v", noChangePayload)
	}
}

func TestResolveRepoMissingPath(t *testing.T) {
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	missingPath := filepath.Join(t.TempDir(), "does-not-exist")
	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "resolve_repo", map[string]any{
			"path": missingPath,
		}),
	})

	payload := toolPayload(t, responses[1])
	if boolField(payload, "found") || boolField(payload, "indexed") {
		t.Fatalf("expected missing path to be not found: %#v", payload)
	}
	if !strings.Contains(stringField(payload, "error"), "Path does not exist:") {
		t.Fatalf("expected path-not-found error, got %#v", payload)
	}
}

func TestListReposIsDeterministic(t *testing.T) {
	root := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	first := filepath.Join(root, "zeta")
	second := filepath.Join(root, "alpha")
	writeFile(t, filepath.Join(first, "main.go"), "package main\n")
	writeFile(t, filepath.Join(second, "main.go"), "package main\n")

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             first,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
		toolCallRequest(3, "index_folder", map[string]any{
			"path":             second,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
		toolCallRequest(4, "list_repos", map[string]any{}),
	})

	for i, response := range responses[1:3] {
		payload := toolPayload(t, response)
		if !boolField(payload, "success") {
			t.Fatalf("index_folder call %d failed: %#v", i+1, payload)
		}
	}

	listPayload := toolPayload(t, responses[3])
	repos := mapSliceField(listPayload, "repos")
	if len(repos) != 2 {
		t.Fatalf("expected two repos, got %d", len(repos))
	}
	firstRepo := stringField(repos[0], "repo")
	secondRepo := stringField(repos[1], "repo")
	if firstRepo > secondRepo {
		t.Fatalf("repos are not sorted: %q > %q", firstRepo, secondRepo)
	}
}

func TestIndexFileLifecycle(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	sourceFile := filepath.Join(repoRoot, "hello.py")
	writeFile(t, sourceFile, "def hello():\n    return 'hi'\n")

	writeFile(t, filepath.Join(repoRoot, "README.md"), "# ignored\n")
	writeFile(t, filepath.Join(repoRoot, "notes.txt"), "unsupported\n")

	writeFile(t, sourceFile, "def hello():\n    return 'hi'\n")
	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
		toolCallRequest(3, "index_file", map[string]any{
			"path": sourceFile,
		}),
	})

	indexPayload := toolPayload(t, responses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")

	unchangedPayload := toolPayload(t, responses[2])
	if !boolField(unchangedPayload, "success") {
		t.Fatalf("index_file unchanged failed: %#v", unchangedPayload)
	}
	if got := stringField(unchangedPayload, "message"); got != "File unchanged" {
		t.Fatalf("unexpected unchanged message: %#v", unchangedPayload)
	}
	if got := stringField(unchangedPayload, "repo"); got != repoID {
		t.Fatalf("unchanged repo mismatch: got %q want %q", got, repoID)
	}

	writeFile(t, sourceFile, "def hello():\n    return 'world'\n")
	newFile := filepath.Join(repoRoot, "world.py")
	writeFile(t, newFile, "def world():\n    return 'earth'\n")

	responses = runMCPRequests(t, []map[string]any{
		initializeRequest(4),
		toolCallRequest(5, "index_file", map[string]any{
			"path": sourceFile,
		}),
		toolCallRequest(6, "index_file", map[string]any{
			"path": newFile,
		}),
		toolCallRequest(7, "list_repos", map[string]any{}),
	})

	changedPayload := toolPayload(t, responses[1])
	if !boolField(changedPayload, "success") {
		t.Fatalf("index_file changed failed: %#v", changedPayload)
	}
	if got := boolField(changedPayload, "is_new"); got {
		t.Fatalf("expected changed file to have is_new=false: %#v", changedPayload)
	}
	if got := intField(changedPayload, "symbol_count"); got != 0 {
		t.Fatalf("expected symbol_count=0 in parity lane, got %d", got)
	}

	newPayload := toolPayload(t, responses[2])
	if !boolField(newPayload, "success") {
		t.Fatalf("index_file new failed: %#v", newPayload)
	}
	if got := boolField(newPayload, "is_new"); !got {
		t.Fatalf("expected new file to have is_new=true: %#v", newPayload)
	}
	if got := stringField(newPayload, "file"); got != "world.py" {
		t.Fatalf("unexpected indexed file path %q", got)
	}

	listPayload := toolPayload(t, responses[3])
	repos := mapSliceField(listPayload, "repos")
	if len(repos) != 1 {
		t.Fatalf("expected one repo after index_file lifecycle, got %d", len(repos))
	}
	if got := intField(repos[0], "file_count"); got != 2 {
		t.Fatalf("expected file_count=2 after new file index, got %d", got)
	}

	snapshot := readSingleRepoSnapshot(t, storageRoot)
	fileMTimes, ok := snapshot["file_mtimes"].(map[string]any)
	if !ok {
		t.Fatalf("repo snapshot missing file_mtimes: %#v", snapshot)
	}
	helloMTime := asInt64(fileMTimes["hello.py"])
	worldMTime := asInt64(fileMTimes["world.py"])
	if helloMTime <= 0 || worldMTime <= 0 {
		t.Fatalf("expected positive file mtimes, got hello=%d world=%d", helloMTime, worldMTime)
	}
}

func TestIndexFileNoMatchingIndex(t *testing.T) {
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	filePath := filepath.Join(t.TempDir(), "standalone.py")
	writeFile(t, filePath, "def standalone():\n    return 1\n")

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_file", map[string]any{
			"path": filePath,
		}),
	})

	payload := toolPayload(t, responses[1])
	if boolField(payload, "success") {
		t.Fatalf("expected no-matching-index failure, got %#v", payload)
	}
	if !strings.Contains(stringField(payload, "error"), "No indexed folder found") {
		t.Fatalf("unexpected no-matching-index error: %#v", payload)
	}
}

func TestIndexFileUnsupportedAndMissingPaths(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	writeFile(t, filepath.Join(repoRoot, "hello.py"), "def hello():\n    return 'hi'\n")
	textFile := filepath.Join(repoRoot, "readme.txt")
	writeFile(t, textFile, "plain text")
	missingFile := filepath.Join(repoRoot, "missing.py")

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
		toolCallRequest(3, "index_file", map[string]any{
			"path": textFile,
		}),
		toolCallRequest(4, "index_file", map[string]any{
			"path": missingFile,
		}),
		toolCallRequest(5, "index_file", map[string]any{
			"path": repoRoot,
		}),
	})

	indexPayload := toolPayload(t, responses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}

	unsupported := toolPayload(t, responses[2])
	if boolField(unsupported, "success") {
		t.Fatalf("expected unsupported extension failure: %#v", unsupported)
	}
	if !strings.Contains(stringField(unsupported, "error"), "Unsupported file type") {
		t.Fatalf("unexpected unsupported-file error: %#v", unsupported)
	}

	missing := toolPayload(t, responses[3])
	if boolField(missing, "success") {
		t.Fatalf("expected missing file failure: %#v", missing)
	}
	if !strings.Contains(stringField(missing, "error"), "File not found:") {
		t.Fatalf("unexpected missing-file error: %#v", missing)
	}

	notAFile := toolPayload(t, responses[4])
	if boolField(notAFile, "success") {
		t.Fatalf("expected not-a-file failure: %#v", notAFile)
	}
	if !strings.Contains(stringField(notAFile, "error"), "Path is not a file:") {
		t.Fatalf("unexpected path-not-file error: %#v", notAFile)
	}
}

func TestGetFileTreeAndOutlineContracts(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	writeFile(t, filepath.Join(repoRoot, "src", "main.go"), "package main\n\nfunc main() {}\n")
	writeFile(t, filepath.Join(repoRoot, "pkg", "util.py"), "def util():\n    return 1\n")
	writeFile(t, filepath.Join(repoRoot, "include", "no_symbols.h"), "int noop(void);\n")

	indexResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})

	indexPayload := toolPayload(t, indexResponses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(3),
		toolCallRequest(4, "get_file_tree", map[string]any{
			"repo":              repoID,
			"path_prefix":       "",
			"include_summaries": true,
		}),
		toolCallRequest(5, "get_file_tree", map[string]any{
			"repo":              repoID,
			"path_prefix":       "missing/",
			"include_summaries": false,
		}),
		toolCallRequest(6, "get_file_outline", map[string]any{
			"repo":      repoID,
			"file_path": "include/no_symbols.h",
		}),
		toolCallRequest(7, "get_file_outline", map[string]any{
			"repo":       repoID,
			"file_paths": []string{"src/main.go", "pkg/util.py"},
		}),
		toolCallRequest(8, "get_file_outline", map[string]any{
			"repo":       repoID,
			"file_path":  "src/main.go",
			"file_paths": []string{"src/main.go"},
		}),
	})

	treePayload := toolPayload(t, responses[1])
	if got := stringField(treePayload, "repo"); got != repoID {
		t.Fatalf("tree repo mismatch: got %q want %q", got, repoID)
	}
	files := flattenTreeFiles(mapSliceField(treePayload, "tree"))
	noSymbols, ok := files["include/no_symbols.h"]
	if !ok {
		t.Fatalf("tree payload missing include/no_symbols.h: %#v", treePayload)
	}
	if got := stringField(noSymbols, "language"); got != "c" {
		t.Fatalf("unexpected no-symbol language: %#v", noSymbols)
	}
	if got := intField(noSymbols, "symbol_count"); got != 0 {
		t.Fatalf("expected no symbols for include/no_symbols.h: %#v", noSymbols)
	}
	if _, ok := noSymbols["summary"]; !ok {
		t.Fatalf("expected include_summaries=true to include summary field: %#v", noSymbols)
	}
	treeMeta := mapField(treePayload, "_meta")
	if _, ok := treeMeta["timing_ms"]; !ok {
		t.Fatalf("tree payload missing timing meta: %#v", treePayload)
	}

	missingPrefix := toolPayload(t, responses[2])
	if got := len(mapSliceField(missingPrefix, "tree")); got != 0 {
		t.Fatalf("expected empty tree for unmatched prefix, got %d (%#v)", got, missingPrefix)
	}

	outlineSingle := toolPayload(t, responses[3])
	if got := stringField(outlineSingle, "file"); got != "include/no_symbols.h" {
		t.Fatalf("unexpected single outline file %q", got)
	}
	if got := stringField(outlineSingle, "language"); got != "c" {
		t.Fatalf("unexpected single outline language %#v", outlineSingle)
	}
	if got := len(mapSliceField(outlineSingle, "symbols")); got != 0 {
		t.Fatalf("expected zero symbols in parity lane: %#v", outlineSingle)
	}
	singleMeta := mapField(outlineSingle, "_meta")
	if got := intField(singleMeta, "symbol_count"); got != 0 {
		t.Fatalf("expected symbol_count=0 for no-symbol file: %#v", outlineSingle)
	}

	outlineBatch := toolPayload(t, responses[4])
	results := mapSliceField(outlineBatch, "results")
	if len(results) != 2 {
		t.Fatalf("expected two batch results, got %d (%#v)", len(results), outlineBatch)
	}
	if got := stringField(results[0], "file"); got != "src/main.go" {
		t.Fatalf("unexpected batch result order: %#v", results)
	}
	if got := stringField(results[1], "file"); got != "pkg/util.py" {
		t.Fatalf("unexpected batch result order: %#v", results)
	}
	if _, ok := mapField(results[0], "_meta")["tip"]; ok {
		t.Fatalf("expected tip stripped from batch result meta: %#v", results[0])
	}
	if _, ok := mapField(outlineBatch, "_meta")["timing_ms"]; !ok {
		t.Fatalf("expected batch timing meta: %#v", outlineBatch)
	}

	xorPayload := toolPayload(t, responses[5])
	if got := stringField(xorPayload, "error"); got != "Internal error processing get_file_outline" {
		t.Fatalf("expected XOR server envelope, got %#v", xorPayload)
	}
}

func TestGetFileContentContracts(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	crlfContent := "first\r\nsecond\r\n"
	writeFile(t, filepath.Join(repoRoot, "script.py"), crlfContent)
	writeFile(t, filepath.Join(repoRoot, "empty.py"), "")

	indexResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})

	indexPayload := toolPayload(t, indexResponses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(3),
		toolCallRequest(4, "get_file_content", map[string]any{
			"repo":      repoID,
			"file_path": "script.py",
		}),
		toolCallRequest(5, "get_file_content", map[string]any{
			"repo":       repoID,
			"file_path":  "script.py",
			"start_line": 2,
			"end_line":   99,
		}),
		toolCallRequest(6, "get_file_content", map[string]any{
			"repo":       repoID,
			"file_path":  "script.py",
			"start_line": 2,
			"end_line":   1,
		}),
		toolCallRequest(7, "get_file_content", map[string]any{
			"repo":      repoID,
			"file_path": "empty.py",
		}),
		toolCallRequest(8, "get_file_content", map[string]any{
			"repo":      repoID,
			"file_path": "missing.py",
		}),
	})

	fullContent := toolPayload(t, responses[1])
	if got := intField(fullContent, "line_count"); got != 2 {
		t.Fatalf("unexpected full content line_count: %#v", fullContent)
	}
	if got := intField(fullContent, "start_line"); got != 1 {
		t.Fatalf("unexpected full content start_line: %#v", fullContent)
	}
	if got := intField(fullContent, "end_line"); got != 2 {
		t.Fatalf("unexpected full content end_line: %#v", fullContent)
	}
	if got := stringField(fullContent, "content"); got != crlfContent {
		t.Fatalf("expected unsliced content to remain verbatim, got %#v", fullContent)
	}

	sliced := toolPayload(t, responses[2])
	if got := intField(sliced, "start_line"); got != 2 || intField(sliced, "end_line") != 2 {
		t.Fatalf("unexpected clamped slice bounds: %#v", sliced)
	}
	if got := stringField(sliced, "content"); got != "second" {
		t.Fatalf("unexpected clamped slice content: %#v", sliced)
	}

	reversed := toolPayload(t, responses[3])
	if got := intField(reversed, "start_line"); got != 2 || intField(reversed, "end_line") != 2 {
		t.Fatalf("unexpected reversed-range bounds: %#v", reversed)
	}
	if got := stringField(reversed, "content"); got != "second" {
		t.Fatalf("unexpected reversed-range content: %#v", reversed)
	}

	emptyPayload := toolPayload(t, responses[4])
	if got := intField(emptyPayload, "line_count"); got != 0 {
		t.Fatalf("expected line_count=0 for empty file: %#v", emptyPayload)
	}
	if intField(emptyPayload, "start_line") != 0 || intField(emptyPayload, "end_line") != 0 {
		t.Fatalf("expected stable empty slice bounds: %#v", emptyPayload)
	}
	if got := stringField(emptyPayload, "content"); got != "" {
		t.Fatalf("expected empty content for empty file: %#v", emptyPayload)
	}

	missingFile := toolPayload(t, responses[5])
	if got := stringField(missingFile, "error"); got != "File not found: missing.py" {
		t.Fatalf("unexpected missing file error: %#v", missingFile)
	}

	if err := os.Remove(filepath.Join(repoRoot, "script.py")); err != nil {
		t.Fatalf("remove indexed file: %v", err)
	}
	missingContentResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(9),
		toolCallRequest(10, "get_file_content", map[string]any{
			"repo":      repoID,
			"file_path": "script.py",
		}),
	})
	missingContent := toolPayload(t, missingContentResponses[1])
	if got := stringField(missingContent, "error"); got != "File content not found: script.py" {
		t.Fatalf("unexpected missing-content error: %#v", missingContent)
	}
}

func TestRetrievalToolsRepoNotIndexed(t *testing.T) {
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "get_file_tree", map[string]any{
			"repo": "local/missing-repo",
		}),
		toolCallRequest(3, "get_file_content", map[string]any{
			"repo":      "local/missing-repo",
			"file_path": "main.go",
		}),
	})

	treePayload := toolPayload(t, responses[1])
	if got := stringField(treePayload, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected get_file_tree repo-missing error: %#v", treePayload)
	}

	contentPayload := toolPayload(t, responses[2])
	if got := stringField(contentPayload, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected get_file_content repo-missing error: %#v", contentPayload)
	}
}

func TestInvalidateCacheContracts(t *testing.T) {
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	repoOne := filepath.Join(t.TempDir(), "project")
	repoTwo := filepath.Join(t.TempDir(), "project")
	writeFile(t, filepath.Join(repoOne, "main.go"), "package main\n")
	writeFile(t, filepath.Join(repoTwo, "main.go"), "package main\n")

	indexResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoOne,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
		toolCallRequest(3, "index_folder", map[string]any{
			"path":             repoTwo,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})

	indexOne := toolPayload(t, indexResponses[1])
	indexTwo := toolPayload(t, indexResponses[2])
	if !boolField(indexOne, "success") || !boolField(indexTwo, "success") {
		t.Fatalf("index setup failed: %#v %#v", indexOne, indexTwo)
	}
	repoOneID := stringField(indexOne, "repo")
	repoTwoID := stringField(indexTwo, "repo")

	repoChoices := []string{repoOneID, repoTwoID}
	sort.Strings(repoChoices)

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(4),
		toolCallRequest(5, "invalidate_cache", map[string]any{
			"repo": "project",
		}),
		toolCallRequest(6, "invalidate_cache", map[string]any{
			"repo": repoOneID,
		}),
		toolCallRequest(7, "get_file_tree", map[string]any{
			"repo": repoOneID,
		}),
		toolCallRequest(8, "invalidate_cache", map[string]any{
			"repo": repoOneID,
		}),
		toolCallRequest(9, "invalidate_cache", map[string]any{
			"repo": "missing-repo",
		}),
	})

	ambiguous := toolPayload(t, responses[1])
	expectedAmbiguous := fmt.Sprintf(
		"Ambiguous repository name: project. Use one of: %s, %s",
		repoChoices[0],
		repoChoices[1],
	)
	if got := stringField(ambiguous, "error"); got != expectedAmbiguous {
		t.Fatalf("unexpected ambiguous invalidate_cache error: %#v", ambiguous)
	}

	deleted := toolPayload(t, responses[2])
	if !boolField(deleted, "success") {
		t.Fatalf("expected delete success, got %#v", deleted)
	}
	if got := stringField(deleted, "repo"); got != repoOneID {
		t.Fatalf("unexpected deleted repo id %q", got)
	}
	if got := stringField(deleted, "message"); got != fmt.Sprintf("Index and cached files deleted for %s", repoOneID) {
		t.Fatalf("unexpected delete message: %#v", deleted)
	}

	deletedTree := toolPayload(t, responses[3])
	if got := stringField(deletedTree, "error"); got != fmt.Sprintf("Repository not indexed: %s", repoOneID) {
		t.Fatalf("expected deleted repo to be unavailable: %#v", deletedTree)
	}

	missingAfterDelete := toolPayload(t, responses[4])
	if boolField(missingAfterDelete, "success") {
		t.Fatalf("expected success=false after deleting missing index: %#v", missingAfterDelete)
	}
	if got := stringField(missingAfterDelete, "error"); got != fmt.Sprintf("No index found for %s", repoOneID) {
		t.Fatalf("unexpected missing-after-delete envelope: %#v", missingAfterDelete)
	}

	missingBare := toolPayload(t, responses[5])
	if got := stringField(missingBare, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing-bare invalidate_cache error: %#v", missingBare)
	}
}

func TestGetRepoOutlineContracts(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	writeFile(t, filepath.Join(repoRoot, "main.go"), "package main\n")
	writeFile(t, filepath.Join(repoRoot, "pkg", "util.py"), "def util():\n    return 1\n")

	indexResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})

	indexPayload := toolPayload(t, indexResponses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")

	snapshot := readSingleRepoSnapshot(t, storageRoot)
	snapshot["indexed_at"] = time.Now().UTC().AddDate(0, 0, -8).Format(time.RFC3339)
	snapshot["symbols"] = map[string]any{
		"pkg/util.py::util#function": map[string]any{
			"id":         "pkg/util.py::util#function",
			"kind":       "function",
			"name":       "util",
			"file":       "pkg/util.py",
			"line":       1,
			"end_line":   1,
			"signature":  "def util()",
			"docstring":  "",
			"decorators": []any{},
		},
		"main.go::main#function": map[string]any{
			"id":         "main.go::main#function",
			"kind":       "function",
			"name":       "main",
			"file":       "main.go",
			"line":       1,
			"end_line":   1,
			"signature":  "func main()",
			"docstring":  "",
			"decorators": []any{},
		},
	}
	writeSingleRepoSnapshot(t, storageRoot, snapshot)

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(3),
		toolCallRequest(4, "get_repo_outline", map[string]any{
			"repo": repoID,
		}),
		toolCallRequest(5, "get_repo_outline", map[string]any{
			"repo": "local/missing-repo",
		}),
	})

	outline := toolPayload(t, responses[1])
	if got := stringField(outline, "repo"); got != repoID {
		t.Fatalf("repo mismatch: got %q want %q", got, repoID)
	}
	if got := intField(outline, "file_count"); got != 2 {
		t.Fatalf("unexpected file_count in repo outline: %#v", outline)
	}
	if got := intField(outline, "symbol_count"); got != 2 {
		t.Fatalf("unexpected symbol_count in repo outline: %#v", outline)
	}

	directories := mapField(outline, "directories")
	if got := intField(directories, "(root)"); got != 1 {
		t.Fatalf("unexpected root directory count: %#v", directories)
	}
	if got := intField(directories, "pkg/"); got != 1 {
		t.Fatalf("unexpected pkg directory count: %#v", directories)
	}

	symbolKinds := mapField(outline, "symbol_kinds")
	if got := intField(symbolKinds, "function"); got != 2 {
		t.Fatalf("unexpected symbol kinds: %#v", symbolKinds)
	}

	meta := mapField(outline, "_meta")
	if _, ok := meta["timing_ms"]; !ok {
		t.Fatalf("expected timing meta on repo outline: %#v", outline)
	}
	if got := boolField(meta, "is_stale"); !got {
		t.Fatalf("expected stale repo meta for old snapshot: %#v", meta)
	}
	if got := stringField(outline, "staleness_warning"); !strings.Contains(got, "Run index_repo to refresh.") {
		t.Fatalf("expected staleness warning for stale repo outline: %#v", outline)
	}

	missing := toolPayload(t, responses[2])
	if got := stringField(missing, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing repo outline envelope: %#v", missing)
	}
}

func TestGetSymbolSourceContracts(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	sourceContent := "a = 1\nb = 2\ndef hello():\n    return a + b\n"
	writeFile(t, filepath.Join(repoRoot, "pkg", "util.py"), sourceContent)

	indexResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})

	indexPayload := toolPayload(t, indexResponses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")

	symbolID := "pkg/util.py::hello#function"
	sourceHash := sha256.Sum256([]byte("def hello():"))
	snapshot := readSingleRepoSnapshot(t, storageRoot)
	snapshot["symbols"] = map[string]any{
		symbolID: map[string]any{
			"id":           symbolID,
			"kind":         "function",
			"name":         "hello",
			"file":         "pkg/util.py",
			"line":         3,
			"end_line":     3,
			"signature":    "def hello()",
			"decorators":   []any{},
			"docstring":    "",
			"content_hash": hex.EncodeToString(sourceHash[:]),
		},
	}
	writeSingleRepoSnapshot(t, storageRoot, snapshot)

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(3),
		toolCallRequest(4, "get_symbol_source", map[string]any{
			"repo":          repoID,
			"symbol_id":     symbolID,
			"verify":        true,
			"context_lines": 1,
		}),
		toolCallRequest(5, "get_symbol_source", map[string]any{
			"repo":          repoID,
			"symbol_ids":    []string{symbolID, "missing::symbol"},
			"verify":        false,
			"context_lines": 500,
		}),
		toolCallRequest(6, "get_symbol_source", map[string]any{
			"repo":       repoID,
			"symbol_id":  symbolID,
			"symbol_ids": []string{symbolID},
		}),
		toolCallRequest(7, "get_symbol_source", map[string]any{
			"repo": repoID,
		}),
		toolCallRequest(8, "get_symbol_source", map[string]any{
			"repo":      "local/missing-repo",
			"symbol_id": symbolID,
		}),
		toolCallRequest(9, "get_symbol_source", map[string]any{
			"repo":      "missing-repo",
			"symbol_id": symbolID,
		}),
	})

	single := toolPayload(t, responses[1])
	if got := stringField(single, "id"); got != symbolID {
		t.Fatalf("unexpected symbol id: %#v", single)
	}
	if got := stringField(single, "source"); got != "def hello():" {
		t.Fatalf("unexpected single symbol source: %#v", single)
	}
	if got := stringField(single, "context_before"); got != "b = 2" {
		t.Fatalf("unexpected single symbol context_before: %#v", single)
	}
	if got := stringField(single, "context_after"); got != "    return a + b" {
		t.Fatalf("unexpected single symbol context_after: %#v", single)
	}
	if got := boolField(single, "content_verified"); !got {
		t.Fatalf("expected content hash verification success: %#v", single)
	}
	if got := stringField(mapField(single, "_meta"), "hint"); got == "" {
		t.Fatalf("expected single symbol hint in _meta: %#v", single)
	}

	batch := toolPayload(t, responses[2])
	batchSymbols := mapSliceField(batch, "symbols")
	if len(batchSymbols) != 1 {
		t.Fatalf("expected one batch symbol result: %#v", batch)
	}
	batchErrors := mapSliceField(batch, "errors")
	if len(batchErrors) != 1 {
		t.Fatalf("expected one batch error result: %#v", batch)
	}
	if got := stringField(batchErrors[0], "error"); got != "Symbol not found: missing::symbol" {
		t.Fatalf("unexpected missing batch symbol error: %#v", batchErrors[0])
	}
	if got := intField(mapField(batch, "_meta"), "symbol_count"); got != 1 {
		t.Fatalf("expected symbol_count=1 for batch response: %#v", batch)
	}

	bothIDFields := toolPayload(t, responses[3])
	if got := stringField(bothIDFields, "error"); got != "Provide symbol_id or symbol_ids, not both." {
		t.Fatalf("unexpected both-id-fields validation envelope: %#v", bothIDFields)
	}

	neitherField := toolPayload(t, responses[4])
	if got := stringField(neitherField, "error"); got != "Provide symbol_id (string) or symbol_ids (array)." {
		t.Fatalf("unexpected missing-id validation envelope: %#v", neitherField)
	}

	missingIndexedRepo := toolPayload(t, responses[5])
	if got := stringField(missingIndexedRepo, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing indexed repo envelope: %#v", missingIndexedRepo)
	}

	missingBareRepo := toolPayload(t, responses[6])
	if got := stringField(missingBareRepo, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing bare repo envelope: %#v", missingBareRepo)
	}
}

func TestSearchTextContracts(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	writeFile(t, filepath.Join(repoRoot, "include", "no_symbols.h"), "// TODO: wire header\n#define FLAG 1\n")
	writeFile(t, filepath.Join(repoRoot, "src", "main.py"), "def run():\n    # TODO: wire main\n    return FLAG\n")

	indexResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})

	indexPayload := toolPayload(t, indexResponses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(3),
		toolCallRequest(4, "search_text", map[string]any{
			"repo":          repoID,
			"query":         "TODO",
			"context_lines": 1,
		}),
		toolCallRequest(5, "search_text", map[string]any{
			"repo":          repoID,
			"query":         "TODO",
			"max_results":   1,
			"context_lines": 1,
		}),
		toolCallRequest(6, "search_text", map[string]any{
			"repo":          repoID,
			"query":         "TODO",
			"file_pattern":  "src/*.py",
			"context_lines": 1,
		}),
		toolCallRequest(7, "search_text", map[string]any{
			"repo":          repoID,
			"query":         "TODO",
			"context_lines": 999,
		}),
		toolCallRequest(8, "search_text", map[string]any{
			"repo":     repoID,
			"query":    "(a+)+b",
			"is_regex": true,
		}),
		toolCallRequest(9, "search_text", map[string]any{
			"repo":     repoID,
			"query":    strings.Repeat("a", 201),
			"is_regex": true,
		}),
		toolCallRequest(10, "search_text", map[string]any{
			"repo":     repoID,
			"query":    "[",
			"is_regex": true,
		}),
		toolCallRequest(11, "search_text", map[string]any{
			"repo":     repoID,
			"query":    "todo: wire (header|main)",
			"is_regex": true,
		}),
		toolCallRequest(12, "search_text", map[string]any{
			"repo":  "local/missing-repo",
			"query": "TODO",
		}),
		toolCallRequest(13, "search_text", map[string]any{
			"repo":  "missing-repo",
			"query": "TODO",
		}),
	})

	grouped := toolPayload(t, responses[1])
	if got := intField(grouped, "result_count"); got != 2 {
		t.Fatalf("expected grouped result_count=2: %#v", grouped)
	}
	groupedResults := mapSliceField(grouped, "results")
	if len(groupedResults) != 2 {
		t.Fatalf("expected two grouped file entries: %#v", grouped)
	}
	if got := stringField(groupedResults[0], "file"); got != "include/no_symbols.h" {
		t.Fatalf("unexpected first grouped file %q", got)
	}
	headerMatches := mapSliceField(groupedResults[0], "matches")
	if len(headerMatches) != 1 {
		t.Fatalf("expected one match in include/no_symbols.h: %#v", groupedResults[0])
	}
	if got := stringField(headerMatches[0], "text"); got != "// TODO: wire header" {
		t.Fatalf("unexpected header match text: %#v", headerMatches[0])
	}
	if got := stringSliceField(headerMatches[0], "before"); len(got) != 0 {
		t.Fatalf("expected empty before context for header match: %#v", headerMatches[0])
	}
	if got := stringSliceField(headerMatches[0], "after"); len(got) != 1 || got[0] != "#define FLAG 1" {
		t.Fatalf("unexpected after context for header match: %#v", headerMatches[0])
	}

	if got := stringField(groupedResults[1], "file"); got != "src/main.py" {
		t.Fatalf("unexpected second grouped file %q", got)
	}
	mainMatches := mapSliceField(groupedResults[1], "matches")
	if len(mainMatches) != 1 {
		t.Fatalf("expected one match in src/main.py: %#v", groupedResults[1])
	}
	if got := stringField(mainMatches[0], "text"); got != "    # TODO: wire main" {
		t.Fatalf("unexpected main match text: %#v", mainMatches[0])
	}
	if got := stringSliceField(mainMatches[0], "before"); len(got) != 1 || got[0] != "def run():" {
		t.Fatalf("unexpected main before context: %#v", mainMatches[0])
	}
	if got := stringSliceField(mainMatches[0], "after"); len(got) != 1 || got[0] != "    return FLAG" {
		t.Fatalf("unexpected main after context: %#v", mainMatches[0])
	}

	groupedMeta := mapField(grouped, "_meta")
	if got := intField(groupedMeta, "files_searched"); got != 2 {
		t.Fatalf("expected files_searched=2: %#v", groupedMeta)
	}
	if got := boolField(groupedMeta, "truncated"); got {
		t.Fatalf("expected grouped search to be non-truncated: %#v", groupedMeta)
	}

	truncated := toolPayload(t, responses[2])
	if got := intField(truncated, "result_count"); got != 1 {
		t.Fatalf("expected truncated result_count=1: %#v", truncated)
	}
	truncatedMeta := mapField(truncated, "_meta")
	if got := boolField(truncatedMeta, "truncated"); !got {
		t.Fatalf("expected truncation meta=true: %#v", truncatedMeta)
	}
	truncatedResults := mapSliceField(truncated, "results")
	if len(truncatedResults) != 1 || stringField(truncatedResults[0], "file") != "include/no_symbols.h" {
		t.Fatalf("unexpected truncated grouped result set: %#v", truncated)
	}

	filtered := toolPayload(t, responses[3])
	if got := intField(filtered, "result_count"); got != 1 {
		t.Fatalf("expected file-pattern filtered result_count=1: %#v", filtered)
	}
	filteredResults := mapSliceField(filtered, "results")
	if len(filteredResults) != 1 || stringField(filteredResults[0], "file") != "src/main.py" {
		t.Fatalf("unexpected file-pattern filtered results: %#v", filtered)
	}

	clampedContext := toolPayload(t, responses[4])
	clampedResults := mapSliceField(clampedContext, "results")
	if len(clampedResults) != 2 {
		t.Fatalf("expected context-clamped search to keep both grouped files: %#v", clampedContext)
	}
	mainClampedMatches := mapSliceField(clampedResults[1], "matches")
	if got := stringSliceField(mainClampedMatches[0], "before"); len(got) != 1 || got[0] != "def run():" {
		t.Fatalf("unexpected clamped before context: %#v", mainClampedMatches[0])
	}
	if got := stringSliceField(mainClampedMatches[0], "after"); len(got) != 1 || got[0] != "    return FLAG" {
		t.Fatalf("unexpected clamped after context: %#v", mainClampedMatches[0])
	}

	nestedQuantifier := toolPayload(t, responses[5])
	if got := strings.ToLower(stringField(nestedQuantifier, "error")); !strings.Contains(got, "nested quantifier") {
		t.Fatalf("expected nested quantifier rejection: %#v", nestedQuantifier)
	}

	longRegex := toolPayload(t, responses[6])
	if got := stringField(longRegex, "error"); !strings.Contains(got, "Regex too long") {
		t.Fatalf("expected regex length rejection: %#v", longRegex)
	}

	invalidRegex := toolPayload(t, responses[7])
	if got := stringField(invalidRegex, "error"); !strings.HasPrefix(got, "Invalid regex:") {
		t.Fatalf("expected invalid-regex envelope: %#v", invalidRegex)
	}

	safeRegex := toolPayload(t, responses[8])
	if got := intField(safeRegex, "result_count"); got != 2 {
		t.Fatalf("expected safe regex match count 2: %#v", safeRegex)
	}

	missingIndexedRepo := toolPayload(t, responses[9])
	if got := stringField(missingIndexedRepo, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing indexed repo envelope: %#v", missingIndexedRepo)
	}

	missingBareRepo := toolPayload(t, responses[10])
	if got := stringField(missingBareRepo, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing bare repo envelope: %#v", missingBareRepo)
	}

	if err := os.Remove(filepath.Join(repoRoot, "include", "no_symbols.h")); err != nil {
		t.Fatalf("remove indexed file for missing-cache coverage: %v", err)
	}
	missingFileResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(14),
		toolCallRequest(15, "search_text", map[string]any{
			"repo":  repoID,
			"query": "TODO",
		}),
	})
	missingFilePayload := toolPayload(t, missingFileResponses[1])
	if got := intField(missingFilePayload, "result_count"); got != 1 {
		t.Fatalf("expected one match after missing cached file skip: %#v", missingFilePayload)
	}
	missingFileResults := mapSliceField(missingFilePayload, "results")
	if len(missingFileResults) != 1 || stringField(missingFileResults[0], "file") != "src/main.py" {
		t.Fatalf("unexpected grouped results after missing cached file skip: %#v", missingFilePayload)
	}
}

func TestSearchSymbolsContracts(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	writeFile(
		t,
		filepath.Join(repoRoot, "src", "utils.py"),
		"def resolve_repo(repo):\n    return repo.strip()\n\ndef resolve_repo_path(repo):\n    return resolve_repo(repo)\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "src", "main.py"),
		"class Runner:\n    def run(self):\n        return True\n",
	)
	writeFile(t, filepath.Join(repoRoot, "include", "flags.h"), "#define FLAG 1\n")

	indexResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})
	indexPayload := toolPayload(t, indexResponses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")

	snapshot := readSingleRepoSnapshot(t, storageRoot)
	snapshot["symbols"] = map[string]any{
		"src/utils.py::resolve_repo::1": map[string]any{
			"id":          "src/utils.py::resolve_repo::1",
			"name":        "resolve_repo",
			"kind":        "function",
			"language":    "python",
			"file":        "src/utils.py",
			"line":        1,
			"end_line":    2,
			"signature":   "def resolve_repo(repo):",
			"summary":     "Resolve canonical repository identifier",
			"docstring":   "Normalize and return repository id.",
			"keywords":    []any{"resolve", "repo"},
			"byte_length": 120,
		},
		"src/utils.py::resolve_repo_path::4": map[string]any{
			"id":          "src/utils.py::resolve_repo_path::4",
			"name":        "resolve_repo_path",
			"kind":        "function",
			"language":    "python",
			"file":        "src/utils.py",
			"line":        4,
			"end_line":    5,
			"signature":   "def resolve_repo_path(repo):",
			"summary":     "Resolve repository and return path form",
			"docstring":   "",
			"keywords":    []any{"resolve", "repo", "path"},
			"byte_length": 80,
		},
		"src/main.py::Runner::1": map[string]any{
			"id":          "src/main.py::Runner::1",
			"name":        "Runner",
			"kind":        "class",
			"language":    "python",
			"file":        "src/main.py",
			"line":        1,
			"end_line":    3,
			"signature":   "class Runner:",
			"summary":     "Simple run loop holder",
			"docstring":   "",
			"keywords":    []any{"runner"},
			"byte_length": 70,
		},
		"include/flags.h::FLAG::1": map[string]any{
			"id":          "include/flags.h::FLAG::1",
			"name":        "FLAG",
			"kind":        "constant",
			"language":    "c",
			"file":        "include/flags.h",
			"line":        1,
			"end_line":    1,
			"signature":   "#define FLAG 1",
			"summary":     "Header flag definition",
			"docstring":   "",
			"keywords":    []any{"flag"},
			"byte_length": 32,
		},
	}
	writeSingleRepoSnapshot(t, storageRoot, snapshot)

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(3),
		toolCallRequest(4, "search_symbols", map[string]any{
			"repo":         repoID,
			"query":        "resolve_repo",
			"detail_level": "standard",
		}),
		toolCallRequest(5, "search_symbols", map[string]any{
			"repo":         repoID,
			"query":        "resolve",
			"detail_level": "compact",
			"debug":        true,
		}),
		toolCallRequest(6, "search_symbols", map[string]any{
			"repo":         repoID,
			"query":        "resolve_repo",
			"kind":         "function",
			"detail_level": "full",
			"max_results":  1,
		}),
		toolCallRequest(7, "search_symbols", map[string]any{
			"repo":         repoID,
			"query":        "flag",
			"file_pattern": "include/*.h",
		}),
		toolCallRequest(8, "search_symbols", map[string]any{
			"repo":        repoID,
			"query":       "resolve",
			"language":    "python",
			"max_results": 1,
		}),
		toolCallRequest(9, "search_symbols", map[string]any{
			"repo":         repoID,
			"query":        "resolve",
			"detail_level": "compact",
			"token_budget": 30,
		}),
		toolCallRequest(10, "search_symbols", map[string]any{
			"repo":        repoID,
			"query":       "resolve",
			"max_results": 0,
		}),
		toolCallRequest(11, "search_symbols", map[string]any{
			"repo":         repoID,
			"query":        "resolve_repo",
			"detail_level": "verbose",
		}),
		toolCallRequest(12, "search_symbols", map[string]any{
			"repo":  repoID,
			"query": strings.Repeat("a", 501),
		}),
		toolCallRequest(13, "search_symbols", map[string]any{
			"repo":  repoID,
			"query": "resolve_repo",
			"kind":  "banana",
		}),
		toolCallRequest(14, "search_symbols", map[string]any{
			"repo":  "local/missing-repo",
			"query": "resolve",
		}),
		toolCallRequest(15, "search_symbols", map[string]any{
			"repo":  "missing-repo",
			"query": "resolve",
		}),
	})

	standard := toolPayload(t, responses[1])
	if got := intField(standard, "result_count"); got != 2 {
		t.Fatalf("expected standard result_count=2: %#v", standard)
	}
	standardResults := mapSliceField(standard, "results")
	if len(standardResults) != 2 {
		t.Fatalf("expected two standard results: %#v", standard)
	}
	if got := stringField(standardResults[0], "id"); got != "src/utils.py::resolve_repo::1" {
		t.Fatalf("unexpected first standard result id: %#v", standardResults[0])
	}
	if got := stringField(standardResults[1], "id"); got != "src/utils.py::resolve_repo_path::4" {
		t.Fatalf("unexpected second standard result id: %#v", standardResults[1])
	}
	if got := stringField(standardResults[0], "signature"); got == "" {
		t.Fatalf("expected standard signature field: %#v", standardResults[0])
	}
	standardMeta := mapField(standard, "_meta")
	if got := intField(standardMeta, "total_symbols"); got != 4 {
		t.Fatalf("expected total_symbols=4: %#v", standardMeta)
	}
	if got := boolField(standardMeta, "truncated"); got {
		t.Fatalf("expected non-truncated standard response: %#v", standardMeta)
	}

	compactDebug := toolPayload(t, responses[2])
	compactResults := mapSliceField(compactDebug, "results")
	if len(compactResults) != 2 {
		t.Fatalf("expected compact response to include two results: %#v", compactDebug)
	}
	if _, ok := compactResults[0]["signature"]; ok {
		t.Fatalf("compact response should not include signature: %#v", compactResults[0])
	}
	if _, ok := compactResults[0]["score"]; !ok {
		t.Fatalf("debug compact response missing score: %#v", compactResults[0])
	}
	if _, ok := compactResults[0]["score_breakdown"]; !ok {
		t.Fatalf("debug compact response missing score_breakdown: %#v", compactResults[0])
	}
	compactMeta := mapField(compactDebug, "_meta")
	if got := intField(compactMeta, "candidates_scored"); got < 2 {
		t.Fatalf("expected candidates_scored metadata: %#v", compactMeta)
	}

	full := toolPayload(t, responses[3])
	if got := intField(full, "result_count"); got != 1 {
		t.Fatalf("expected one full-detail result: %#v", full)
	}
	fullResult := mapSliceField(full, "results")[0]
	if got := stringField(fullResult, "source"); got != "def resolve_repo(repo):\n    return repo.strip()" {
		t.Fatalf("unexpected full-detail source: %#v", fullResult)
	}
	if got := intField(fullResult, "end_line"); got != 2 {
		t.Fatalf("unexpected full-detail end_line: %#v", fullResult)
	}
	if got := stringField(fullResult, "docstring"); got == "" {
		t.Fatalf("expected full-detail docstring: %#v", fullResult)
	}

	filePattern := toolPayload(t, responses[4])
	if got := intField(filePattern, "result_count"); got != 1 {
		t.Fatalf("expected one file-pattern result: %#v", filePattern)
	}
	filePatternResult := mapSliceField(filePattern, "results")
	if len(filePatternResult) != 1 || stringField(filePatternResult[0], "file") != "include/flags.h" {
		t.Fatalf("unexpected file-pattern results: %#v", filePattern)
	}

	languageFiltered := toolPayload(t, responses[5])
	if got := intField(languageFiltered, "result_count"); got != 1 {
		t.Fatalf("expected one language-filtered result: %#v", languageFiltered)
	}
	if got := boolField(mapField(languageFiltered, "_meta"), "truncated"); !got {
		t.Fatalf("expected language-filtered truncation metadata: %#v", languageFiltered)
	}

	tokenBudget := toolPayload(t, responses[6])
	if got := intField(tokenBudget, "result_count"); got != 1 {
		t.Fatalf("expected one token-budget-packed result: %#v", tokenBudget)
	}
	tokenMeta := mapField(tokenBudget, "_meta")
	if got := intField(tokenMeta, "token_budget"); got != 30 {
		t.Fatalf("expected token_budget metadata: %#v", tokenMeta)
	}
	if got := intField(tokenMeta, "tokens_used"); got != 30 {
		t.Fatalf("expected tokens_used=30: %#v", tokenMeta)
	}
	if got := intField(tokenMeta, "tokens_remaining"); got != 0 {
		t.Fatalf("expected tokens_remaining=0: %#v", tokenMeta)
	}

	clampedMaxResults := toolPayload(t, responses[7])
	if got := intField(clampedMaxResults, "result_count"); got != 1 {
		t.Fatalf("expected clamped max_results response to contain one result: %#v", clampedMaxResults)
	}
	if got := boolField(mapField(clampedMaxResults, "_meta"), "truncated"); !got {
		t.Fatalf("expected truncation metadata for clamped max_results: %#v", clampedMaxResults)
	}

	invalidDetail := toolPayload(t, responses[8])
	if got := stringField(invalidDetail, "error"); got != "Invalid detail_level 'verbose'. Must be 'compact', 'standard', or 'full'." {
		t.Fatalf("unexpected invalid-detail envelope: %#v", invalidDetail)
	}

	longQuery := toolPayload(t, responses[9])
	if got := stringField(longQuery, "error"); !strings.Contains(got, "Query too long") {
		t.Fatalf("expected query-length error envelope: %#v", longQuery)
	}

	unknownKind := toolPayload(t, responses[10])
	if got := stringField(unknownKind, "error"); got != "Unknown kind: banana" {
		t.Fatalf("unexpected unknown-kind envelope: %#v", unknownKind)
	}

	missingIndexedRepo := toolPayload(t, responses[11])
	if got := stringField(missingIndexedRepo, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing indexed repo envelope: %#v", missingIndexedRepo)
	}

	missingBareRepo := toolPayload(t, responses[12])
	if got := stringField(missingBareRepo, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing bare repo envelope: %#v", missingBareRepo)
	}
}

func TestRelationshipToolContracts(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	writeFile(
		t,
		filepath.Join(repoRoot, "src", "utils", "helper.ts"),
		"export function helper() {\n    return true\n}\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "src", "utils", "runner.ts"),
		"export const runner = () => 'ok'\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "src", "app", "main.ts"),
		"import { helper } from \"../utils/helper\";\nimport { runner } from \"../utils/runner\";\nconst helperAlias = helper;\nexport const run = () => runner();\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "src", "app", "consumer.ts"),
		"import helperDefault from \"../utils/helper\";\nimport { helper as aliasHelper } from \"../utils/helper\";\nexport const use = () => helperDefault || aliasHelper;\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "src", "app", "unused.ts"),
		"const marker = 'no references here';\n",
	)

	indexResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})
	indexPayload := toolPayload(t, indexResponses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")

	snapshot := readSingleRepoSnapshot(t, storageRoot)
	snapshot["symbols"] = map[string]any{
		"src/utils/helper.ts::helper::1": map[string]any{
			"id":        "src/utils/helper.ts::helper::1",
			"name":      "helper",
			"kind":      "function",
			"language":  "typescript",
			"file":      "src/utils/helper.ts",
			"line":      1,
			"end_line":  3,
			"signature": "export function helper()",
		},
		"src/utils/runner.ts::runner::1": map[string]any{
			"id":        "src/utils/runner.ts::runner::1",
			"name":      "runner",
			"kind":      "constant",
			"language":  "typescript",
			"file":      "src/utils/runner.ts",
			"line":      1,
			"end_line":  1,
			"signature": "export const runner = () => 'ok'",
		},
	}
	writeSingleRepoSnapshot(t, storageRoot, snapshot)

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(3),
		toolCallRequest(4, "find_importers", map[string]any{
			"repo":        repoID,
			"file_path":   "src/utils/helper.ts",
			"max_results": 1,
		}),
		toolCallRequest(5, "find_importers", map[string]any{
			"repo":       repoID,
			"file_paths": []string{"src/utils/helper.ts", "src/utils/runner.ts"},
		}),
		toolCallRequest(6, "find_importers", map[string]any{
			"repo":       repoID,
			"file_path":  "src/utils/helper.ts",
			"file_paths": []string{"src/utils/helper.ts"},
		}),
		toolCallRequest(7, "find_references", map[string]any{
			"repo":        repoID,
			"identifier":  "helper",
			"max_results": 1,
		}),
		toolCallRequest(8, "find_references", map[string]any{
			"repo":        repoID,
			"identifiers": []string{"helper", "runner", "missing"},
		}),
		toolCallRequest(9, "find_references", map[string]any{
			"repo":        repoID,
			"identifier":  "helper",
			"identifiers": []string{"helper"},
		}),
		toolCallRequest(10, "check_references", map[string]any{
			"repo":                repoID,
			"identifier":          "helper",
			"search_content":      true,
			"max_content_results": 1,
		}),
		toolCallRequest(11, "check_references", map[string]any{
			"repo":           repoID,
			"identifier":     "runner",
			"search_content": false,
		}),
		toolCallRequest(12, "check_references", map[string]any{
			"repo":           repoID,
			"identifiers":    []string{"helper", "missing"},
			"search_content": false,
		}),
		toolCallRequest(13, "check_references", map[string]any{
			"repo":        repoID,
			"identifier":  "helper",
			"identifiers": []string{"helper"},
		}),
		toolCallRequest(14, "find_importers", map[string]any{
			"repo":      "local/missing-repo",
			"file_path": "src/utils/helper.ts",
		}),
		toolCallRequest(15, "find_references", map[string]any{
			"repo":       "missing-repo",
			"identifier": "helper",
		}),
		toolCallRequest(16, "check_references", map[string]any{
			"repo":       "local/missing-repo",
			"identifier": "helper",
		}),
		toolCallRequest(17, "check_references", map[string]any{
			"repo":       "missing-repo",
			"identifier": "helper",
		}),
	})

	findImportersSingle := toolPayload(t, responses[1])
	if got := stringField(findImportersSingle, "file_path"); got != "src/utils/helper.ts" {
		t.Fatalf("unexpected find_importers file_path: %#v", findImportersSingle)
	}
	if got := intField(findImportersSingle, "importer_count"); got != 2 {
		t.Fatalf("expected importer_count=2: %#v", findImportersSingle)
	}
	singleImporters := mapSliceField(findImportersSingle, "importers")
	if len(singleImporters) != 1 || stringField(singleImporters[0], "file") != "src/app/consumer.ts" {
		t.Fatalf("unexpected truncated importer rows: %#v", findImportersSingle)
	}
	findImportersMeta := mapField(findImportersSingle, "_meta")
	if got := boolField(findImportersMeta, "truncated"); !got {
		t.Fatalf("expected find_importers truncation metadata: %#v", findImportersSingle)
	}
	if got := stringField(findImportersMeta, "tip"); !strings.Contains(got, "file_paths") {
		t.Fatalf("expected find_importers tip metadata: %#v", findImportersSingle)
	}

	findImportersBatch := toolPayload(t, responses[2])
	importerBatchResults := mapSliceField(findImportersBatch, "results")
	if len(importerBatchResults) != 2 {
		t.Fatalf("expected two batch importer groups: %#v", findImportersBatch)
	}
	if got := intField(importerBatchResults[0], "importer_count"); got != 3 {
		t.Fatalf("unexpected helper importer_count in batch: %#v", importerBatchResults[0])
	}
	if got := intField(importerBatchResults[1], "importer_count"); got != 1 {
		t.Fatalf("unexpected runner importer_count in batch: %#v", importerBatchResults[1])
	}

	invalidFindImporters := toolPayload(t, responses[3])
	if got := stringField(invalidFindImporters, "error"); got != "Internal error processing find_importers" {
		t.Fatalf("unexpected invalid find_importers envelope: %#v", invalidFindImporters)
	}

	findReferencesSingle := toolPayload(t, responses[4])
	if got := intField(findReferencesSingle, "reference_count"); got != 2 {
		t.Fatalf("expected reference_count=2: %#v", findReferencesSingle)
	}
	singleReferences := mapSliceField(findReferencesSingle, "references")
	if len(singleReferences) != 1 || stringField(singleReferences[0], "file") != "src/app/consumer.ts" {
		t.Fatalf("unexpected truncated references rows: %#v", findReferencesSingle)
	}
	matches := mapSliceField(singleReferences[0], "matches")
	if len(matches) == 0 {
		t.Fatalf("expected at least one match in first reference row: %#v", findReferencesSingle)
	}
	if got := boolField(mapField(findReferencesSingle, "_meta"), "truncated"); !got {
		t.Fatalf("expected find_references truncation metadata: %#v", findReferencesSingle)
	}

	findReferencesBatch := toolPayload(t, responses[5])
	referenceBatchResults := mapSliceField(findReferencesBatch, "results")
	if len(referenceBatchResults) != 3 {
		t.Fatalf("expected three batch identifier groups: %#v", findReferencesBatch)
	}
	if got := intField(referenceBatchResults[0], "reference_count"); got != 2 {
		t.Fatalf("unexpected helper reference_count in batch: %#v", referenceBatchResults[0])
	}
	if got := intField(referenceBatchResults[1], "reference_count"); got != 1 {
		t.Fatalf("unexpected runner reference_count in batch: %#v", referenceBatchResults[1])
	}
	if got := intField(referenceBatchResults[2], "reference_count"); got != 0 {
		t.Fatalf("unexpected missing reference_count in batch: %#v", referenceBatchResults[2])
	}
	batchReferenceRows := mapSliceField(referenceBatchResults[0], "references")
	if len(batchReferenceRows) == 0 {
		t.Fatalf("expected helper batch references to be non-empty: %#v", referenceBatchResults[0])
	}
	if _, ok := batchReferenceRows[0]["specifier"]; !ok {
		t.Fatalf("expected batch references to include specifier field: %#v", batchReferenceRows[0])
	}
	if _, ok := batchReferenceRows[0]["matches"]; ok {
		t.Fatalf("batch reference entries should not include nested matches: %#v", batchReferenceRows[0])
	}

	invalidFindReferences := toolPayload(t, responses[6])
	if got := stringField(invalidFindReferences, "error"); got != "Internal error processing find_references" {
		t.Fatalf("unexpected invalid find_references envelope: %#v", invalidFindReferences)
	}

	checkSingle := toolPayload(t, responses[7])
	if got := boolField(checkSingle, "is_referenced"); !got {
		t.Fatalf("expected helper to be referenced: %#v", checkSingle)
	}
	if got := intField(checkSingle, "import_count"); got != 2 {
		t.Fatalf("expected helper import_count=2: %#v", checkSingle)
	}
	if got := intField(checkSingle, "content_count"); got != 1 {
		t.Fatalf("expected helper content_count=1 with max_content_results=1: %#v", checkSingle)
	}
	contentRefs := mapSliceField(checkSingle, "content_references")
	if len(contentRefs) != 1 || stringField(contentRefs[0], "file") != "src/app/consumer.ts" {
		t.Fatalf("unexpected helper content references: %#v", checkSingle)
	}

	checkImportOnly := toolPayload(t, responses[8])
	if got := boolField(checkImportOnly, "is_referenced"); !got {
		t.Fatalf("expected runner to be import-referenced: %#v", checkImportOnly)
	}
	if got := intField(checkImportOnly, "import_count"); got != 1 {
		t.Fatalf("expected runner import_count=1: %#v", checkImportOnly)
	}
	if got := intField(checkImportOnly, "content_count"); got != 0 {
		t.Fatalf("expected runner content_count=0 when search_content=false: %#v", checkImportOnly)
	}
	if _, ok := checkImportOnly["content_references"]; ok {
		t.Fatalf("did not expect content_references when search_content=false: %#v", checkImportOnly)
	}

	checkBatch := toolPayload(t, responses[9])
	checkBatchResults := mapSliceField(checkBatch, "results")
	if len(checkBatchResults) != 2 {
		t.Fatalf("expected two batch check_references rows: %#v", checkBatch)
	}
	if got := boolField(checkBatchResults[0], "is_referenced"); !got {
		t.Fatalf("expected helper batch row to be referenced: %#v", checkBatchResults[0])
	}
	if got := boolField(checkBatchResults[1], "is_referenced"); got {
		t.Fatalf("expected missing batch row to be unreferenced: %#v", checkBatchResults[1])
	}
	if got := intField(mapField(checkBatch, "_meta"), "identifiers_checked"); got != 2 {
		t.Fatalf("expected identifiers_checked=2: %#v", checkBatch)
	}

	invalidCheckReferences := toolPayload(t, responses[10])
	if got := stringField(invalidCheckReferences, "error"); got != "Internal error processing check_references" {
		t.Fatalf("unexpected invalid check_references envelope: %#v", invalidCheckReferences)
	}

	missingIndexedImporters := toolPayload(t, responses[11])
	if got := stringField(missingIndexedImporters, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing indexed repo for find_importers: %#v", missingIndexedImporters)
	}

	missingBareReferences := toolPayload(t, responses[12])
	if got := stringField(missingBareReferences, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing bare repo for find_references: %#v", missingBareReferences)
	}

	missingIndexedCheck := toolPayload(t, responses[13])
	if got := stringField(missingIndexedCheck, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing indexed repo for check_references: %#v", missingIndexedCheck)
	}

	missingBareCheck := toolPayload(t, responses[14])
	if got := stringField(missingBareCheck, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing bare repo for check_references: %#v", missingBareCheck)
	}
}

func TestRelationshipToolFanoutQueueDepthAndOrdering(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)
	t.Setenv("GOCODEMUNCH_FANOUT_MODE", "parallel")
	t.Setenv("GOCODEMUNCH_FANOUT_MAX_WORKERS", "4")
	t.Setenv("GOCODEMUNCH_FANOUT_MAX_QUEUE_DEPTH", "2")
	t.Setenv("GOCODEMUNCH_FANOUT_OVERLOAD_POLICY", "reject")

	writeFile(
		t,
		filepath.Join(repoRoot, "src", "mod.py"),
		"def alpha():\n    return 1\n\ndef beta():\n    return 2\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "src", "app", "consumer.py"),
		"from src.mod import alpha\nfrom src.mod import beta\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "src", "app", "extra.py"),
		"from src.mod import beta\n",
	)

	indexResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})
	indexPayload := toolPayload(t, indexResponses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(3),
		toolCallRequest(4, "find_references", map[string]any{
			"repo":        repoID,
			"identifiers": []string{"beta", "alpha"},
		}),
		toolCallRequest(5, "check_references", map[string]any{
			"repo":        repoID,
			"identifiers": []string{"alpha", "beta", "missing"},
		}),
		toolCallRequest(6, "find_importers", map[string]any{
			"repo":       repoID,
			"file_paths": []string{"src/mod.py", "src/app/consumer.py", "src/app/extra.py"},
		}),
	})

	findReferencesBatch := toolPayload(t, responses[1])
	referenceBatchResults := mapSliceField(findReferencesBatch, "results")
	if len(referenceBatchResults) != 2 {
		t.Fatalf("expected two find_references batch results: %#v", findReferencesBatch)
	}
	if got := stringField(referenceBatchResults[0], "identifier"); got != "beta" {
		t.Fatalf("expected first batch row to match input order beta: %#v", referenceBatchResults[0])
	}
	if got := stringField(referenceBatchResults[1], "identifier"); got != "alpha" {
		t.Fatalf("expected second batch row to match input order alpha: %#v", referenceBatchResults[1])
	}

	checkReferencesOverflow := toolPayload(t, responses[2])
	assertFanoutOverflowEnvelope(t, checkReferencesOverflow, 3, 2)

	findImportersOverflow := toolPayload(t, responses[3])
	assertFanoutOverflowEnvelope(t, findImportersOverflow, 3, 2)
}

func TestRetrievalToolFanoutQueueDepthAndOrdering(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)
	t.Setenv("GOCODEMUNCH_FANOUT_MODE", "parallel")
	t.Setenv("GOCODEMUNCH_FANOUT_MAX_WORKERS", "4")
	t.Setenv("GOCODEMUNCH_FANOUT_MAX_QUEUE_DEPTH", "2")
	t.Setenv("GOCODEMUNCH_FANOUT_OVERLOAD_POLICY", "reject")

	writeFile(
		t,
		filepath.Join(repoRoot, "src", "mod.py"),
		"import os\n\ndef alpha():\n    return os.getcwd()\n\ndef beta():\n    return 2\n\ndef gamma():\n    return 3\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "src", "app", "consumer.py"),
		"from src.mod import alpha\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "src", "app", "extra.py"),
		"from src.mod import gamma\n",
	)

	indexResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})
	indexPayload := toolPayload(t, indexResponses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")

	alphaID := "src/mod.py::alpha#function"
	betaID := "src/mod.py::beta#function"
	gammaID := "src/mod.py::gamma#function"
	snapshot := readSingleRepoSnapshot(t, storageRoot)
	snapshot["symbols"] = map[string]any{
		alphaID: map[string]any{
			"id":         alphaID,
			"kind":       "function",
			"name":       "alpha",
			"file":       "src/mod.py",
			"line":       3,
			"end_line":   3,
			"signature":  "def alpha()",
			"decorators": []any{},
			"docstring":  "",
		},
		betaID: map[string]any{
			"id":         betaID,
			"kind":       "function",
			"name":       "beta",
			"file":       "src/mod.py",
			"line":       6,
			"end_line":   6,
			"signature":  "def beta()",
			"decorators": []any{},
			"docstring":  "",
		},
		gammaID: map[string]any{
			"id":         gammaID,
			"kind":       "function",
			"name":       "gamma",
			"file":       "src/mod.py",
			"line":       9,
			"end_line":   9,
			"signature":  "def gamma()",
			"decorators": []any{},
			"docstring":  "",
		},
	}
	writeSingleRepoSnapshot(t, storageRoot, snapshot)

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(3),
		toolCallRequest(4, "get_file_outline", map[string]any{
			"repo":       repoID,
			"file_paths": []string{"src/app/consumer.py", "src/mod.py"},
		}),
		toolCallRequest(5, "get_symbol_source", map[string]any{
			"repo":       repoID,
			"symbol_ids": []string{betaID, alphaID},
		}),
		toolCallRequest(6, "get_context_bundle", map[string]any{
			"repo":       repoID,
			"symbol_ids": []string{gammaID, betaID},
		}),
		toolCallRequest(7, "get_file_outline", map[string]any{
			"repo":       repoID,
			"file_paths": []string{"src/mod.py", "src/app/consumer.py", "src/app/extra.py"},
		}),
		toolCallRequest(8, "get_symbol_source", map[string]any{
			"repo":       repoID,
			"symbol_ids": []string{alphaID, betaID, gammaID},
		}),
		toolCallRequest(9, "get_context_bundle", map[string]any{
			"repo":       repoID,
			"symbol_ids": []string{alphaID, betaID, gammaID},
		}),
	})

	fileOutlineBatch := toolPayload(t, responses[1])
	outlineBatchResults := mapSliceField(fileOutlineBatch, "results")
	if len(outlineBatchResults) != 2 {
		t.Fatalf("expected two get_file_outline batch results: %#v", fileOutlineBatch)
	}
	if got := stringField(outlineBatchResults[0], "file"); got != "src/app/consumer.py" {
		t.Fatalf("expected get_file_outline batch ordering to match input: %#v", outlineBatchResults)
	}
	if got := stringField(outlineBatchResults[1], "file"); got != "src/mod.py" {
		t.Fatalf("expected get_file_outline batch ordering to match input: %#v", outlineBatchResults)
	}

	symbolSourceBatch := toolPayload(t, responses[2])
	symbolBatchRows := mapSliceField(symbolSourceBatch, "symbols")
	if len(symbolBatchRows) != 2 {
		t.Fatalf("expected two get_symbol_source rows: %#v", symbolSourceBatch)
	}
	if got := stringField(symbolBatchRows[0], "id"); got != betaID {
		t.Fatalf("expected first get_symbol_source row to match input order: %#v", symbolBatchRows[0])
	}
	if got := stringField(symbolBatchRows[1], "id"); got != alphaID {
		t.Fatalf("expected second get_symbol_source row to match input order: %#v", symbolBatchRows[1])
	}
	if got := len(mapSliceField(symbolSourceBatch, "errors")); got != 0 {
		t.Fatalf("expected zero get_symbol_source batch errors: %#v", symbolSourceBatch)
	}

	contextBundleBatch := toolPayload(t, responses[3])
	contextSymbols := mapSliceField(contextBundleBatch, "symbols")
	if len(contextSymbols) != 2 {
		t.Fatalf("expected two get_context_bundle symbols: %#v", contextBundleBatch)
	}
	if got := stringField(contextSymbols[0], "symbol_id"); got != gammaID {
		t.Fatalf("expected first get_context_bundle row to match input order: %#v", contextSymbols[0])
	}
	if got := stringField(contextSymbols[1], "symbol_id"); got != betaID {
		t.Fatalf("expected second get_context_bundle row to match input order: %#v", contextSymbols[1])
	}

	fileOutlineOverflow := toolPayload(t, responses[4])
	assertFanoutOverflowEnvelope(t, fileOutlineOverflow, 3, 2)

	symbolSourceOverflow := toolPayload(t, responses[5])
	assertFanoutOverflowEnvelope(t, symbolSourceOverflow, 3, 2)

	contextBundleOverflow := toolPayload(t, responses[6])
	assertFanoutOverflowEnvelope(t, contextBundleOverflow, 3, 2)
}

func TestSearchColumnsAndContextBundleContracts(t *testing.T) {
	repoRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	writeFile(
		t,
		filepath.Join(repoRoot, "models", "orders.sql"),
		"select order_id, customer_id from raw_orders\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "models", "payments.sql"),
		"select payment_id from raw_payments\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "app", "helper.py"),
		"import os\nfrom pathlib import Path\n\ndef helper(value):\n    return str(value)\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "app", "main.py"),
		"from app.helper import helper\n\ndef run():\n    return helper(1)\n",
	)
	writeFile(
		t,
		filepath.Join(repoRoot, "app", "consumer.py"),
		"import app.helper as helper_mod\n\nvalue = helper_mod.helper(2)\n",
	)

	indexResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})
	indexPayload := toolPayload(t, indexResponses[1])
	if !boolField(indexPayload, "success") {
		t.Fatalf("index_folder failed: %#v", indexPayload)
	}
	repoID := stringField(indexPayload, "repo")
	helperSymbolID := "app/helper.py::helper::4"

	snapshot := readSingleRepoSnapshot(t, storageRoot)
	snapshot["symbols"] = map[string]any{
		helperSymbolID: map[string]any{
			"id":        helperSymbolID,
			"name":      "helper",
			"kind":      "function",
			"language":  "python",
			"file":      "app/helper.py",
			"line":      4,
			"end_line":  5,
			"signature": "def helper(value):",
		},
		"models/orders.sql::orders::1": map[string]any{
			"id":        "models/orders.sql::orders::1",
			"name":      "orders",
			"kind":      "table",
			"language":  "sql",
			"file":      "models/orders.sql",
			"line":      1,
			"end_line":  1,
			"signature": "select order_id, customer_id from raw_orders",
		},
		"models/payments.sql::payments::1": map[string]any{
			"id":        "models/payments.sql::payments::1",
			"name":      "payments",
			"kind":      "table",
			"language":  "sql",
			"file":      "models/payments.sql",
			"line":      1,
			"end_line":  1,
			"signature": "select payment_id from raw_payments",
		},
	}
	snapshot["context_metadata"] = map[string]any{
		"dbt_columns": map[string]any{
			"orders": map[string]any{
				"order_id":    "Primary key",
				"customer_id": "Foreign key to customers",
			},
			"payments": map[string]any{
				"payment_id": "Primary key",
			},
		},
		"sqlmesh_columns": map[string]any{
			"orders": map[string]any{
				"loaded_at": "Timestamp loaded marker",
			},
		},
	}
	writeSingleRepoSnapshot(t, storageRoot, snapshot)

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(3),
		toolCallRequest(4, "search_columns", map[string]any{
			"repo":  repoID,
			"query": "order_id",
		}),
		toolCallRequest(5, "search_columns", map[string]any{
			"repo":          repoID,
			"query":         "key",
			"model_pattern": "pay*",
		}),
		toolCallRequest(6, "search_columns", map[string]any{
			"repo":        repoID,
			"query":       "",
			"max_results": 1,
		}),
		toolCallRequest(7, "get_context_bundle", map[string]any{
			"repo":            repoID,
			"symbol_id":       helperSymbolID,
			"include_callers": true,
		}),
		toolCallRequest(8, "get_context_bundle", map[string]any{
			"repo":       repoID,
			"symbol_ids": []string{helperSymbolID, helperSymbolID},
		}),
		toolCallRequest(9, "get_context_bundle", map[string]any{
			"repo":          repoID,
			"symbol_id":     helperSymbolID,
			"output_format": "markdown",
		}),
		toolCallRequest(10, "get_context_bundle", map[string]any{
			"repo":      repoID,
			"symbol_id": "missing::symbol",
		}),
		toolCallRequest(11, "get_context_bundle", map[string]any{
			"repo": repoID,
		}),
		toolCallRequest(12, "get_context_bundle", map[string]any{
			"repo":          repoID,
			"symbol_id":     helperSymbolID,
			"output_format": "yaml",
		}),
		toolCallRequest(13, "search_columns", map[string]any{
			"repo":  "local/missing-repo",
			"query": "order",
		}),
		toolCallRequest(14, "search_columns", map[string]any{
			"repo":  "missing-repo",
			"query": "order",
		}),
		toolCallRequest(15, "get_context_bundle", map[string]any{
			"repo":      "local/missing-repo",
			"symbol_id": helperSymbolID,
		}),
		toolCallRequest(16, "get_context_bundle", map[string]any{
			"repo":      "missing-repo",
			"symbol_id": helperSymbolID,
		}),
	})

	exact := toolPayload(t, responses[1])
	if got := intField(exact, "result_count"); got != 1 {
		t.Fatalf("expected exact search_columns result_count=1: %#v", exact)
	}
	if got := intField(exact, "total_models"); got != 3 {
		t.Fatalf("expected total_models=3: %#v", exact)
	}
	if got := intField(exact, "total_columns"); got != 4 {
		t.Fatalf("expected total_columns=4: %#v", exact)
	}
	if got := len(stringSliceField(exact, "sources")); got != 2 {
		t.Fatalf("expected two metadata sources: %#v", exact)
	}
	exactResult := mapSliceField(exact, "results")[0]
	if got := stringField(exactResult, "column"); got != "order_id" {
		t.Fatalf("unexpected exact top column: %#v", exactResult)
	}
	if got := intField(exactResult, "score"); got != 30 {
		t.Fatalf("expected exact-match score=30: %#v", exactResult)
	}
	if got := stringField(exactResult, "file"); got != "models/orders.sql" {
		t.Fatalf("expected SQL model file lookup for orders: %#v", exactResult)
	}
	if got := stringField(exactResult, "source"); got == "" {
		t.Fatalf("expected source field for multi-provider metadata: %#v", exactResult)
	}
	if got := boolField(mapField(exact, "_meta"), "truncated"); got {
		t.Fatalf("expected non-truncated exact column search: %#v", exact)
	}

	patternFiltered := toolPayload(t, responses[2])
	if got := intField(patternFiltered, "result_count"); got != 1 {
		t.Fatalf("expected one model-pattern-filtered column result: %#v", patternFiltered)
	}
	patternRow := mapSliceField(patternFiltered, "results")[0]
	if got := stringField(patternRow, "model"); got != "payments" {
		t.Fatalf("unexpected model_pattern search row: %#v", patternRow)
	}

	truncated := toolPayload(t, responses[3])
	if got := intField(truncated, "result_count"); got != 1 {
		t.Fatalf("expected max_results truncation to one row: %#v", truncated)
	}
	if got := boolField(mapField(truncated, "_meta"), "truncated"); !got {
		t.Fatalf("expected truncation metadata for max_results=1: %#v", truncated)
	}

	contextSingle := toolPayload(t, responses[4])
	if got := stringField(contextSingle, "symbol_id"); got != helperSymbolID {
		t.Fatalf("unexpected context bundle symbol_id: %#v", contextSingle)
	}
	if got := stringField(contextSingle, "source"); got != "def helper(value):\n    return str(value)" {
		t.Fatalf("unexpected context bundle source: %#v", contextSingle)
	}
	imports := stringSliceField(contextSingle, "imports")
	if len(imports) != 2 || imports[0] != "import os" || imports[1] != "from pathlib import Path" {
		t.Fatalf("unexpected context bundle imports: %#v", contextSingle)
	}
	callers := stringSliceField(contextSingle, "callers")
	if len(callers) != 2 || callers[0] != "app/consumer.py" || callers[1] != "app/main.py" {
		t.Fatalf("unexpected context bundle callers: %#v", contextSingle)
	}

	contextBatch := toolPayload(t, responses[5])
	if got := intField(contextBatch, "symbol_count"); got != 1 {
		t.Fatalf("expected deduplicated symbol_count=1 in batch mode: %#v", contextBatch)
	}
	if got := len(mapSliceField(contextBatch, "symbols")); got != 1 {
		t.Fatalf("expected one deduplicated symbol entry in batch mode: %#v", contextBatch)
	}
	filesMap := mapField(contextBatch, "files")
	helperFileMeta := mapField(filesMap, "app/helper.py")
	if got := len(stringSliceField(helperFileMeta, "imports")); got != 2 {
		t.Fatalf("expected deduplicated files import map for helper.py: %#v", contextBatch)
	}

	markdown := toolPayload(t, responses[6])
	mdText := stringField(markdown, "markdown")
	if !strings.Contains(mdText, "# Context Bundle: ") {
		t.Fatalf("expected markdown heading in context bundle markdown output: %#v", markdown)
	}
	if !strings.Contains(mdText, "### Imports") || !strings.Contains(mdText, "```python") {
		t.Fatalf("expected import section and python fence in markdown output: %#v", markdown)
	}

	missingSymbol := toolPayload(t, responses[7])
	if got := stringField(missingSymbol, "error"); got != "Symbol(s) not found: missing::symbol" {
		t.Fatalf("unexpected missing-symbol context bundle envelope: %#v", missingSymbol)
	}

	missingArgs := toolPayload(t, responses[8])
	if got := stringField(missingArgs, "error"); got != "Provide either 'symbol_id' or 'symbol_ids'." {
		t.Fatalf("unexpected missing-args context bundle envelope: %#v", missingArgs)
	}

	invalidFormat := toolPayload(t, responses[9])
	if got := stringField(invalidFormat, "error"); got != "Invalid output_format 'yaml'. Must be 'json' or 'markdown'." {
		t.Fatalf("unexpected invalid-format context bundle envelope: %#v", invalidFormat)
	}

	missingIndexedColumns := toolPayload(t, responses[10])
	if got := stringField(missingIndexedColumns, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing indexed repo envelope for search_columns: %#v", missingIndexedColumns)
	}

	missingBareColumns := toolPayload(t, responses[11])
	if got := stringField(missingBareColumns, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing bare repo envelope for search_columns: %#v", missingBareColumns)
	}

	missingIndexedContext := toolPayload(t, responses[12])
	if got := stringField(missingIndexedContext, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing indexed repo envelope for get_context_bundle: %#v", missingIndexedContext)
	}

	missingBareContext := toolPayload(t, responses[13])
	if got := stringField(missingBareContext, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing bare repo envelope for get_context_bundle: %#v", missingBareContext)
	}

	snapshot = readSingleRepoSnapshot(t, storageRoot)
	snapshot["context_metadata"] = map[string]any{}
	writeSingleRepoSnapshot(t, storageRoot, snapshot)

	noMetadataResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(20),
		toolCallRequest(21, "search_columns", map[string]any{
			"repo":  repoID,
			"query": "order",
		}),
	})
	noMetadata := toolPayload(t, noMetadataResponses[1])
	if got := stringField(noMetadata, "error"); !strings.Contains(got, "No column metadata found") {
		t.Fatalf("unexpected no-column-metadata envelope: %#v", noMetadata)
	}
}

func TestAdvancedAnalysisToolContracts(t *testing.T) {
	repoARoot := t.TempDir()
	repoBRoot := t.TempDir()
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)

	writeFile(
		t,
		filepath.Join(repoARoot, "app", "base.py"),
		"class Base:\n    pass\n",
	)
	writeFile(
		t,
		filepath.Join(repoARoot, "app", "child.py"),
		"from app.base import Base\n\nclass Child(Base):\n    pass\n",
	)
	writeFile(
		t,
		filepath.Join(repoARoot, "app", "helper.py"),
		"from app.child import Child\n\ndef helper_value():\n    return Child()\n\ndef build_child():\n    return Child()\n",
	)
	writeFile(
		t,
		filepath.Join(repoARoot, "app", "main.py"),
		"from app.helper import helper_value\nfrom app.child import Child\n\ndef run():\n    return helper_value(), Child()\n",
	)
	writeFile(
		t,
		filepath.Join(repoARoot, "app", "consumer.py"),
		"from app.helper import helper_value\n\nvalue = helper_value()\n",
	)
	writeFile(
		t,
		filepath.Join(repoARoot, "app", "peer.py"),
		"from app.child import Child\n\ndef child_runner():\n    return Child()\n",
	)

	writeFile(
		t,
		filepath.Join(repoBRoot, "app", "base.py"),
		"class Base:\n    pass\n",
	)
	writeFile(
		t,
		filepath.Join(repoBRoot, "app", "child.py"),
		"from app.base import Base\n\nclass Child(Base):\n    pass\n",
	)
	writeFile(
		t,
		filepath.Join(repoBRoot, "app", "helper.py"),
		"from app.child import Child\n\ndef helper_value(mode='v2'):\n    return Child()\n\ndef build_child():\n    return Child()\n\ndef new_feature():\n    return 'ready'\n",
	)

	indexResponses := runMCPRequests(t, []map[string]any{
		initializeRequest(1),
		toolCallRequest(2, "index_folder", map[string]any{
			"path":             repoARoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
		toolCallRequest(3, "index_folder", map[string]any{
			"path":             repoBRoot,
			"incremental":      false,
			"use_ai_summaries": false,
		}),
	})

	repoA := toolPayload(t, indexResponses[1])
	if !boolField(repoA, "success") {
		t.Fatalf("index_folder repoA failed: %#v", repoA)
	}
	repoAID := stringField(repoA, "repo")
	repoB := toolPayload(t, indexResponses[2])
	if !boolField(repoB, "success") {
		t.Fatalf("index_folder repoB failed: %#v", repoB)
	}
	repoBID := stringField(repoB, "repo")

	findSnapshot := func(repoID string) (string, map[string]any) {
		t.Helper()
		matches, err := filepath.Glob(filepath.Join(storageRoot, "*.json"))
		if err != nil {
			t.Fatalf("glob snapshots: %v", err)
		}
		for _, match := range matches {
			payload, err := os.ReadFile(match)
			if err != nil {
				t.Fatalf("read snapshot %s: %v", match, err)
			}
			var snapshot map[string]any
			mustJSON(t, payload, &snapshot)
			if got := stringField(snapshot, "repo"); got == repoID {
				return match, snapshot
			}
		}
		t.Fatalf("snapshot not found for repo %s", repoID)
		return "", nil
	}

	repoASnapshotPath, repoASnapshot := findSnapshot(repoAID)
	repoASnapshot["symbols"] = map[string]any{
		"app/base.py::Base::1": map[string]any{
			"id":           "app/base.py::Base::1",
			"name":         "Base",
			"kind":         "class",
			"language":     "python",
			"file":         "app/base.py",
			"line":         1,
			"end_line":     2,
			"signature":    "class Base:",
			"content_hash": "hash-base-a",
			"keywords":     []string{"base", "model"},
		},
		"app/child.py::Child::3": map[string]any{
			"id":           "app/child.py::Child::3",
			"name":         "Child",
			"kind":         "class",
			"language":     "python",
			"file":         "app/child.py",
			"line":         3,
			"end_line":     4,
			"signature":    "class Child(Base):",
			"content_hash": "hash-child-a",
			"keywords":     []string{"child", "model"},
		},
		"app/helper.py::helper_value::3": map[string]any{
			"id":           "app/helper.py::helper_value::3",
			"name":         "helper_value",
			"kind":         "function",
			"language":     "python",
			"file":         "app/helper.py",
			"line":         3,
			"end_line":     4,
			"signature":    "def helper_value():",
			"content_hash": "hash-helper-a",
			"keywords":     []string{"helper", "value", "child"},
		},
		"app/helper.py::build_child::6": map[string]any{
			"id":           "app/helper.py::build_child::6",
			"name":         "build_child",
			"kind":         "function",
			"language":     "python",
			"file":         "app/helper.py",
			"line":         6,
			"end_line":     7,
			"signature":    "def build_child():",
			"content_hash": "hash-build-a",
			"keywords":     []string{"build", "child"},
		},
		"app/main.py::run::4": map[string]any{
			"id":           "app/main.py::run::4",
			"name":         "run",
			"kind":         "function",
			"language":     "python",
			"file":         "app/main.py",
			"line":         4,
			"end_line":     5,
			"signature":    "def run():",
			"content_hash": "hash-run-a",
			"keywords":     []string{"run", "entry"},
		},
	}
	encodedA, err := json.MarshalIndent(repoASnapshot, "", "  ")
	if err != nil {
		t.Fatalf("encode repoA snapshot: %v", err)
	}
	if err := os.WriteFile(repoASnapshotPath, encodedA, 0o644); err != nil {
		t.Fatalf("write repoA snapshot: %v", err)
	}

	repoBSnapshotPath, repoBSnapshot := findSnapshot(repoBID)
	repoBSnapshot["source_root"] = ""
	repoBSnapshot["symbols"] = map[string]any{
		"app/base.py::Base::1": map[string]any{
			"id":           "app/base.py::Base::1",
			"name":         "Base",
			"kind":         "class",
			"language":     "python",
			"file":         "app/base.py",
			"line":         1,
			"end_line":     2,
			"signature":    "class Base:",
			"content_hash": "hash-base-a",
		},
		"app/child.py::Child::3": map[string]any{
			"id":           "app/child.py::Child::3",
			"name":         "Child",
			"kind":         "class",
			"language":     "python",
			"file":         "app/child.py",
			"line":         3,
			"end_line":     4,
			"signature":    "class Child(Base):",
			"content_hash": "hash-child-a",
		},
		"app/helper.py::helper_value::3": map[string]any{
			"id":           "app/helper.py::helper_value::3",
			"name":         "helper_value",
			"kind":         "function",
			"language":     "python",
			"file":         "app/helper.py",
			"line":         3,
			"end_line":     4,
			"signature":    "def helper_value(mode='v2'):",
			"content_hash": "hash-helper-b",
		},
		"app/helper.py::build_child::6": map[string]any{
			"id":           "app/helper.py::build_child::6",
			"name":         "build_child",
			"kind":         "function",
			"language":     "python",
			"file":         "app/helper.py",
			"line":         6,
			"end_line":     7,
			"signature":    "def build_child():",
			"content_hash": "hash-build-a",
		},
		"app/helper.py::new_feature::9": map[string]any{
			"id":           "app/helper.py::new_feature::9",
			"name":         "new_feature",
			"kind":         "function",
			"language":     "python",
			"file":         "app/helper.py",
			"line":         9,
			"end_line":     10,
			"signature":    "def new_feature():",
			"content_hash": "hash-new-b",
		},
	}
	encodedB, err := json.MarshalIndent(repoBSnapshot, "", "  ")
	if err != nil {
		t.Fatalf("encode repoB snapshot: %v", err)
	}
	if err := os.WriteFile(repoBSnapshotPath, encodedB, 0o644); err != nil {
		t.Fatalf("write repoB snapshot: %v", err)
	}

	responses := runMCPRequests(t, []map[string]any{
		initializeRequest(20),
		toolCallRequest(21, "get_dependency_graph", map[string]any{
			"repo":      repoAID,
			"file":      "app/main.py",
			"direction": "both",
			"depth":     2,
		}),
		toolCallRequest(22, "get_dependency_graph", map[string]any{
			"repo":      repoAID,
			"file":      "app/main.py",
			"direction": "sideways",
		}),
		toolCallRequest(23, "get_dependency_graph", map[string]any{
			"repo": repoAID,
			"file": "app/missing.py",
		}),
		toolCallRequest(24, "get_dependency_graph", map[string]any{
			"repo": "local/missing-repo",
			"file": "app/main.py",
		}),
		toolCallRequest(25, "get_dependency_graph", map[string]any{
			"repo": "missing-repo",
			"file": "app/main.py",
		}),
		toolCallRequest(26, "get_class_hierarchy", map[string]any{
			"repo":       repoAID,
			"class_name": "Child",
		}),
		toolCallRequest(27, "get_class_hierarchy", map[string]any{
			"repo":       repoAID,
			"class_name": "UnknownClass",
		}),
		toolCallRequest(28, "get_related_symbols", map[string]any{
			"repo":        repoAID,
			"symbol_id":   "app/helper.py::helper_value::3",
			"max_results": 2,
		}),
		toolCallRequest(29, "get_related_symbols", map[string]any{
			"repo":      repoAID,
			"symbol_id": "missing::symbol",
		}),
		toolCallRequest(30, "suggest_queries", map[string]any{
			"repo": repoAID,
		}),
		toolCallRequest(31, "suggest_queries", map[string]any{
			"repo": "local/missing-repo",
		}),
		toolCallRequest(32, "suggest_queries", map[string]any{
			"repo": "missing-repo",
		}),
		toolCallRequest(33, "get_symbol_diff", map[string]any{
			"repo_a": repoAID,
			"repo_b": repoBID,
		}),
		toolCallRequest(34, "get_symbol_diff", map[string]any{
			"repo_a": repoAID,
			"repo_b": "missing-repo",
		}),
		toolCallRequest(35, "get_symbol_diff", map[string]any{
			"repo_a": repoAID,
			"repo_b": "local/missing-repo",
		}),
		toolCallRequest(36, "get_session_stats", map[string]any{}),
		toolCallRequest(37, "get_blast_radius", map[string]any{
			"repo":   repoAID,
			"symbol": "helper_value",
			"depth":  2,
		}),
		toolCallRequest(38, "get_blast_radius", map[string]any{
			"repo":   repoAID,
			"symbol": "missing_symbol",
		}),
		toolCallRequest(39, "get_blast_radius", map[string]any{
			"repo":   "local/missing-repo",
			"symbol": "helper_value",
		}),
		toolCallRequest(40, "get_blast_radius", map[string]any{
			"repo":   "missing-repo",
			"symbol": "helper_value",
		}),
		toolCallRequest(41, "wait_for_fresh", map[string]any{
			"repo":       repoAID,
			"timeout_ms": 500,
		}),
		toolCallRequest(42, "check_freshness", map[string]any{
			"repo": repoAID,
		}),
		toolCallRequest(43, "check_freshness", map[string]any{
			"repo": repoBID,
		}),
		toolCallRequest(44, "check_freshness", map[string]any{
			"repo": "local/missing-repo",
		}),
		toolCallRequest(45, "check_freshness", map[string]any{
			"repo": "missing-repo",
		}),
	})

	dependencyGraph := toolPayload(t, responses[1])
	if got := stringField(dependencyGraph, "repo"); got != repoAID {
		t.Fatalf("unexpected dependency graph repo: %#v", dependencyGraph)
	}
	if got := intField(dependencyGraph, "node_count"); got < 3 {
		t.Fatalf("expected dependency graph node_count >= 3: %#v", dependencyGraph)
	}
	if got := intField(dependencyGraph, "edge_count"); got < 2 {
		t.Fatalf("expected dependency graph edge_count >= 2: %#v", dependencyGraph)
	}
	neighbors := mapField(dependencyGraph, "neighbors")
	mainNeighbors := mapField(neighbors, "app/main.py")
	imports := stringSliceField(mainNeighbors, "imports")
	sort.Strings(imports)
	if len(imports) < 2 || imports[0] != "app/child.py" || imports[1] != "app/helper.py" {
		t.Fatalf("unexpected dependency graph imports for app/main.py: %#v", mainNeighbors)
	}

	invalidDirection := toolPayload(t, responses[2])
	if got := stringField(invalidDirection, "error"); got != "Invalid direction 'sideways'. Must be 'imports', 'importers', or 'both'." {
		t.Fatalf("unexpected invalid-direction envelope: %#v", invalidDirection)
	}

	missingFile := toolPayload(t, responses[3])
	if got := stringField(missingFile, "error"); got != "File not found in index: app/missing.py" {
		t.Fatalf("unexpected missing-file envelope: %#v", missingFile)
	}

	missingIndexedDependency := toolPayload(t, responses[4])
	if got := stringField(missingIndexedDependency, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing indexed repo for dependency graph: %#v", missingIndexedDependency)
	}

	missingBareDependency := toolPayload(t, responses[5])
	if got := stringField(missingBareDependency, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing bare repo for dependency graph: %#v", missingBareDependency)
	}

	classHierarchy := toolPayload(t, responses[6])
	classEntry := mapField(classHierarchy, "class")
	if got := stringField(classEntry, "name"); got != "Child" {
		t.Fatalf("unexpected class hierarchy class entry: %#v", classHierarchy)
	}
	if got := intField(classHierarchy, "ancestor_count"); got != 1 {
		t.Fatalf("expected ancestor_count=1 for Child hierarchy: %#v", classHierarchy)
	}
	ancestors := mapSliceField(classHierarchy, "ancestors")
	if len(ancestors) != 1 || stringField(ancestors[0], "name") != "Base" {
		t.Fatalf("unexpected class hierarchy ancestors: %#v", classHierarchy)
	}

	missingClass := toolPayload(t, responses[7])
	if got := stringField(missingClass, "error"); got != "Class 'UnknownClass' not found in index. Only 'class' and 'type' kinds are searched." {
		t.Fatalf("unexpected missing-class envelope: %#v", missingClass)
	}

	related := toolPayload(t, responses[8])
	if got := intField(related, "related_count"); got != 2 {
		t.Fatalf("expected related_count=2 with max_results=2: %#v", related)
	}
	relatedRows := mapSliceField(related, "related")
	if len(relatedRows) == 0 || stringField(relatedRows[0], "id") != "app/helper.py::build_child::6" {
		t.Fatalf("expected same-file related symbol first: %#v", related)
	}

	missingRelatedSymbol := toolPayload(t, responses[9])
	if got := stringField(missingRelatedSymbol, "error"); got != "Symbol not found: missing::symbol" {
		t.Fatalf("unexpected missing-symbol envelope for related symbols: %#v", missingRelatedSymbol)
	}

	suggest := toolPayload(t, responses[10])
	if got := intField(suggest, "symbol_count"); got != 5 {
		t.Fatalf("expected suggest_queries symbol_count=5: %#v", suggest)
	}
	if got := len(stringSliceField(suggest, "top_keywords")); got == 0 {
		t.Fatalf("expected suggest_queries top_keywords: %#v", suggest)
	}
	importedFiles := mapSliceField(suggest, "most_imported_files")
	if len(importedFiles) == 0 || stringField(importedFiles[0], "file") != "app/child.py" {
		t.Fatalf("expected app/child.py as most-imported file: %#v", suggest)
	}
	if got := len(mapSliceField(suggest, "example_queries")); got == 0 {
		t.Fatalf("expected suggest_queries example_queries: %#v", suggest)
	}

	missingIndexedSuggest := toolPayload(t, responses[11])
	if got := stringField(missingIndexedSuggest, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing indexed repo for suggest_queries: %#v", missingIndexedSuggest)
	}

	missingBareSuggest := toolPayload(t, responses[12])
	if got := stringField(missingBareSuggest, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing bare repo for suggest_queries: %#v", missingBareSuggest)
	}

	symbolDiff := toolPayload(t, responses[13])
	if got := intField(symbolDiff, "added_count"); got != 1 {
		t.Fatalf("expected added_count=1 in symbol diff: %#v", symbolDiff)
	}
	if got := intField(symbolDiff, "removed_count"); got != 1 {
		t.Fatalf("expected removed_count=1 in symbol diff: %#v", symbolDiff)
	}
	if got := intField(symbolDiff, "changed_count"); got != 1 {
		t.Fatalf("expected changed_count=1 in symbol diff: %#v", symbolDiff)
	}
	added := mapSliceField(symbolDiff, "added")
	if len(added) != 1 || stringField(added[0], "name") != "new_feature" {
		t.Fatalf("unexpected added symbols in symbol diff: %#v", symbolDiff)
	}
	removed := mapSliceField(symbolDiff, "removed")
	if len(removed) != 1 || stringField(removed[0], "name") != "run" {
		t.Fatalf("unexpected removed symbols in symbol diff: %#v", symbolDiff)
	}
	changed := mapSliceField(symbolDiff, "changed")
	if len(changed) != 1 || stringField(changed[0], "name") != "helper_value" || !boolField(changed[0], "hash_changed") {
		t.Fatalf("unexpected changed symbols in symbol diff: %#v", symbolDiff)
	}

	missingBareDiff := toolPayload(t, responses[14])
	if got := stringField(missingBareDiff, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing bare repo for symbol diff: %#v", missingBareDiff)
	}

	missingIndexedDiff := toolPayload(t, responses[15])
	if got := stringField(missingIndexedDiff, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing indexed repo for symbol diff: %#v", missingIndexedDiff)
	}

	sessionStats := toolPayload(t, responses[16])
	if got := intField(sessionStats, "session_tokens_saved"); got <= 0 {
		t.Fatalf("expected positive session_tokens_saved from live telemetry: %#v", sessionStats)
	}
	if got := intField(sessionStats, "total_tokens_saved"); got <= 0 {
		t.Fatalf("expected positive total_tokens_saved from live telemetry: %#v", sessionStats)
	}
	if got := intField(sessionStats, "session_calls"); got <= 0 {
		t.Fatalf("expected positive session_calls from live telemetry: %#v", sessionStats)
	}
	if got := intField(sessionStats, "session_input_tokens_saved") + intField(sessionStats, "session_output_tokens_saved"); got != intField(sessionStats, "session_tokens_saved") {
		t.Fatalf("expected session input/output token savings to sum to session_tokens_saved: %#v", sessionStats)
	}
	if got := intField(sessionStats, "total_input_tokens_saved") + intField(sessionStats, "total_output_tokens_saved"); got != intField(sessionStats, "total_tokens_saved") {
		t.Fatalf("expected total input/output token savings to sum to total_tokens_saved: %#v", sessionStats)
	}
	if got := intField(sessionStats, "total_calls"); got < intField(sessionStats, "session_calls") {
		t.Fatalf("expected total_calls to be at least session_calls: %#v", sessionStats)
	}
	if got := intField(sessionStats, "total_sessions"); got != 1 {
		t.Fatalf("expected one live telemetry session in clean integration run, got %#v", sessionStats)
	}
	sessionCost := mapField(sessionStats, "session_cost_avoided")
	for _, competitor := range []string{"claude_code", "codex", "amp"} {
		if _, ok := sessionCost[competitor]; !ok {
			t.Fatalf("expected %s in session_cost_avoided: %#v", competitor, sessionStats)
		}
		if numeric := intField(map[string]any{competitor: sessionCost[competitor]}, competitor); numeric <= 0 && sessionCost[competitor] == 0 {
			t.Fatalf("expected positive session cost avoided for %s: %#v", competitor, sessionStats)
		}
	}
	totalCost := mapField(sessionStats, "total_cost_avoided")
	for _, competitor := range []string{"claude_code", "codex", "amp"} {
		if _, ok := totalCost[competitor]; !ok {
			t.Fatalf("expected %s in total_cost_avoided: %#v", competitor, sessionStats)
		}
		if totalCost[competitor] != sessionCost[competitor] {
			t.Fatalf("expected total/session cost avoided to match in fresh run for %s: %#v", competitor, sessionStats)
		}
	}
	toolBreakdown := mapField(sessionStats, "tool_breakdown")
	if len(toolBreakdown) == 0 {
		t.Fatalf("expected non-empty tool_breakdown in live telemetry response: %#v", sessionStats)
	}
	sessionStatsTool := mapField(toolBreakdown, "get_session_stats")
	if got := intField(sessionStatsTool, "call_count"); got != 1 {
		t.Fatalf("expected get_session_stats tool breakdown entry with one call: %#v", sessionStats)
	}
	totalToolBreakdown := mapField(sessionStats, "total_tool_breakdown")
	if len(totalToolBreakdown) == 0 {
		t.Fatalf("expected non-empty total_tool_breakdown in live telemetry response: %#v", sessionStats)
	}
	meta, ok := sessionStats["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("expected server-level _meta envelope on get_session_stats: %#v", sessionStats)
	}
	if got := intField(meta, "tokens_saved"); got <= 0 {
		t.Fatalf("expected positive _meta.tokens_saved on get_session_stats: %#v", sessionStats)
	}
	if got := intField(meta, "total_tokens_saved"); got != intField(sessionStats, "total_tokens_saved") {
		t.Fatalf("expected _meta.total_tokens_saved to mirror total_tokens_saved: %#v", sessionStats)
	}

	blastRadius := toolPayload(t, responses[17])
	if got := stringField(blastRadius, "repo"); got != repoAID {
		t.Fatalf("unexpected blast radius repo: %#v", blastRadius)
	}
	if got := intField(blastRadius, "importer_count"); got != 2 {
		t.Fatalf("expected importer_count=2 for helper_value: %#v", blastRadius)
	}
	if got := intField(blastRadius, "confirmed_count"); got != 2 {
		t.Fatalf("expected confirmed_count=2 for helper_value: %#v", blastRadius)
	}
	if got := intField(blastRadius, "potential_count"); got != 0 {
		t.Fatalf("expected potential_count=0 for helper_value: %#v", blastRadius)
	}
	confirmed := mapSliceField(blastRadius, "confirmed")
	if len(confirmed) != 2 || stringField(confirmed[0], "file") != "app/consumer.py" || stringField(confirmed[1], "file") != "app/main.py" {
		t.Fatalf("unexpected confirmed blast radius files: %#v", blastRadius)
	}

	missingBlastSymbol := toolPayload(t, responses[18])
	if got := stringField(missingBlastSymbol, "error"); got != "Symbol not found: 'missing_symbol'. Try search_symbols first." {
		t.Fatalf("unexpected missing-symbol blast radius envelope: %#v", missingBlastSymbol)
	}

	missingIndexedBlast := toolPayload(t, responses[19])
	if got := stringField(missingIndexedBlast, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing indexed repo for blast radius: %#v", missingIndexedBlast)
	}

	missingBareBlast := toolPayload(t, responses[20])
	if got := stringField(missingBareBlast, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing bare repo for blast radius: %#v", missingBareBlast)
	}

	waitForFresh := toolPayload(t, responses[21])
	if got := boolField(waitForFresh, "fresh"); !got {
		t.Fatalf("expected wait_for_fresh fresh=true without watcher state: %#v", waitForFresh)
	}
	if got := intField(waitForFresh, "waited_ms"); got != 0 {
		t.Fatalf("expected wait_for_fresh waited_ms=0 without watcher state: %#v", waitForFresh)
	}

	localFreshness := toolPayload(t, responses[22])
	if got, ok := localFreshness["fresh"]; !ok || got != nil {
		t.Fatalf("expected check_freshness local fresh=nil when no git head stored: %#v", localFreshness)
	}
	if got := boolField(localFreshness, "is_local"); !got {
		t.Fatalf("expected local check_freshness is_local=true: %#v", localFreshness)
	}
	if got := stringField(localFreshness, "message"); !strings.Contains(got, "No SHA stored at index time") {
		t.Fatalf("expected no-stored-sha message for local freshness: %#v", localFreshness)
	}

	nonLocalFreshness := toolPayload(t, responses[23])
	if got, ok := nonLocalFreshness["fresh"]; !ok || got != nil {
		t.Fatalf("expected check_freshness non-local fresh=nil: %#v", nonLocalFreshness)
	}
	if got := boolField(nonLocalFreshness, "is_local"); got {
		t.Fatalf("expected non-local check_freshness is_local=false: %#v", nonLocalFreshness)
	}
	if got := stringField(nonLocalFreshness, "message"); !strings.Contains(got, "Freshness check requires a locally indexed repo") {
		t.Fatalf("unexpected non-local freshness message: %#v", nonLocalFreshness)
	}

	missingIndexedFreshness := toolPayload(t, responses[24])
	if got := stringField(missingIndexedFreshness, "error"); got != "Repository not indexed: local/missing-repo" {
		t.Fatalf("unexpected missing indexed repo for check_freshness: %#v", missingIndexedFreshness)
	}

	missingBareFreshness := toolPayload(t, responses[25])
	if got := stringField(missingBareFreshness, "error"); got != "Repository not found: missing-repo" {
		t.Fatalf("unexpected missing bare repo for check_freshness: %#v", missingBareFreshness)
	}
}

func flattenTreeFiles(nodes []map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	var walk func([]map[string]any)
	walk = func(current []map[string]any) {
		for _, node := range current {
			switch stringField(node, "type") {
			case "file":
				path := stringField(node, "path")
				if path != "" {
					out[path] = node
				}
			case "dir":
				walk(mapSliceField(node, "children"))
			}
		}
	}
	walk(nodes)
	return out
}

func runMCPRequests(t *testing.T, requests []map[string]any) []map[string]any {
	t.Helper()
	var in bytes.Buffer
	for _, request := range requests {
		writeFrame(t, &in, request)
	}

	var out bytes.Buffer
	mcpServer := server.New(&in, &out, server.WithServerInfo("gocodemunch-mcp", "test"))
	if err := mcpServer.Serve(context.Background()); err != nil {
		t.Fatalf("serve failed: %v", err)
	}

	frames := readFrames(t, &out)
	if len(frames) != len(requests) {
		t.Fatalf("expected %d responses, got %d", len(requests), len(frames))
	}

	decoded := make([]map[string]any, 0, len(frames))
	for _, frame := range frames {
		var payload map[string]any
		mustJSON(t, frame, &payload)
		decoded = append(decoded, payload)
	}
	return decoded
}

func initializeRequest(id int) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "initialize",
		"params":  map[string]any{},
	}
}

func toolCallRequest(id int, name string, arguments map[string]any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}
}

func toolPayload(t *testing.T, response map[string]any) map[string]any {
	t.Helper()

	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("response missing result: %#v", response)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("response missing content envelope: %#v", response)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("response content has invalid shape: %#v", response)
	}
	text, _ := first["text"].(string)

	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("decode tool payload: %v\ntext=%s", err, text)
	}
	return payload
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFanoutOverflowEnvelope(t *testing.T, payload map[string]any, expectedCount, expectedLimit int) {
	t.Helper()

	expectedMessage := fmt.Sprintf(
		"batch item count %d exceeds fanout queue depth limit %d",
		expectedCount,
		expectedLimit,
	)
	if got := stringField(payload, "error"); got != expectedMessage {
		t.Fatalf("unexpected fanout overflow error message: %#v", payload)
	}
	if got := stringField(payload, "error_code"); got != "fanout_queue_depth_exceeded" {
		t.Fatalf("unexpected fanout overflow error code: %#v", payload)
	}
	if got := boolField(payload, "retryable"); !got {
		t.Fatalf("expected fanout overflow retryable=true: %#v", payload)
	}
	if got := stringField(payload, "overload_policy"); got != "reject" {
		t.Fatalf("unexpected fanout overload policy: %#v", payload)
	}
	if got := intField(payload, "batch_item_count"); got != expectedCount {
		t.Fatalf("unexpected fanout batch_item_count: %#v", payload)
	}
	if got := intField(payload, "queue_depth_limit"); got != expectedLimit {
		t.Fatalf("unexpected fanout queue_depth_limit: %#v", payload)
	}
}

func boolField(m map[string]any, key string) bool {
	value, _ := m[key].(bool)
	return value
}

func stringField(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}

func intField(m map[string]any, key string) int {
	value := m[key]
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

func mapField(m map[string]any, key string) map[string]any {
	value, _ := m[key].(map[string]any)
	return value
}

func mapSliceField(m map[string]any, key string) []map[string]any {
	switch typed := m[key].(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if asMap, ok := item.(map[string]any); ok {
				out = append(out, asMap)
			}
		}
		return out
	default:
		return nil
	}
}

func stringSliceField(m map[string]any, key string) []string {
	switch typed := m[key].(type) {
	case []string:
		out := make([]string, len(typed))
		copy(out, typed)
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func readSingleRepoSnapshot(t *testing.T, storageRoot string) map[string]any {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(storageRoot, "*.json"))
	if err != nil {
		t.Fatalf("glob repo snapshots: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one repo snapshot, got %d (%v)", len(matches), matches)
	}
	payload, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read snapshot %s: %v", matches[0], err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		t.Fatalf("decode snapshot %s: %v", matches[0], err)
	}
	return snapshot
}

func writeSingleRepoSnapshot(t *testing.T, storageRoot string, snapshot map[string]any) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(storageRoot, "*.json"))
	if err != nil {
		t.Fatalf("glob repo snapshots for write: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one repo snapshot for write, got %d (%v)", len(matches), matches)
	}
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatalf("encode snapshot %s: %v", matches[0], err)
	}
	if err := os.WriteFile(matches[0], encoded, 0o644); err != nil {
		t.Fatalf("write snapshot %s: %v", matches[0], err)
	}
}

func asInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return parsed
		}
		return 0
	default:
		text := fmt.Sprintf("%v", typed)
		if text == "" {
			return 0
		}
		var parsed int64
		if _, err := fmt.Sscanf(text, "%d", &parsed); err != nil {
			return 0
		}
		return parsed
	}
}
