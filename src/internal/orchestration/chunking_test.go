package orchestration

import (
	"reflect"
	"testing"
)

func TestBuildDeterministicChunkMetadataDeterministicOrderingAndIDs(t *testing.T) {
	t.Parallel()

	inputA := []indexedFileContent{
		{
			Repo:    "repo-a",
			Path:    "z.go",
			Content: []byte("package z"),
		},
		{
			Repo:    "repo-a",
			Path:    "a.go",
			Content: []byte("package a\nfunc alpha() {}\nfunc beta() {}"),
		},
	}
	inputB := []indexedFileContent{
		inputA[1],
		inputA[0],
	}

	chunksA := buildDeterministicChunkMetadataWithLimits(inputA, 2, 500)
	chunksB := buildDeterministicChunkMetadataWithLimits(inputB, 2, 500)
	if !reflect.DeepEqual(chunksA, chunksB) {
		t.Fatalf("expected deterministic chunk output across input orderings:\na=%#v\nb=%#v", chunksA, chunksB)
	}

	if len(chunksA) != 3 {
		t.Fatalf("expected 3 chunks, got %d (%#v)", len(chunksA), chunksA)
	}

	if chunksA[0].Path != "a.go" || chunksA[0].StartLine != 1 || chunksA[0].EndLine != 2 {
		t.Fatalf("unexpected first chunk metadata: %#v", chunksA[0])
	}
	if chunksA[1].Path != "a.go" || chunksA[1].StartLine != 3 || chunksA[1].EndLine != 3 {
		t.Fatalf("unexpected second chunk metadata: %#v", chunksA[1])
	}
	if chunksA[2].Path != "z.go" || chunksA[2].StartLine != 1 || chunksA[2].EndLine != 1 {
		t.Fatalf("unexpected third chunk metadata: %#v", chunksA[2])
	}

	for index, chunk := range chunksA {
		if chunk.ChunkID == "" {
			t.Fatalf("expected non-empty chunk id at index %d", index)
		}
	}
}

func TestBuildDeterministicChunkMetadataNormalizesCRLFAndSkipsWhitespace(t *testing.T) {
	t.Parallel()

	lfChunks := buildDeterministicChunkMetadata([]indexedFileContent{
		{
			Repo:    "repo-a",
			Path:    "src/main.go",
			Content: []byte("line one\nline two\n"),
		},
	})
	crlfChunks := buildDeterministicChunkMetadata([]indexedFileContent{
		{
			Repo:    "repo-a",
			Path:    "src/main.go",
			Content: []byte("line one\r\nline two\r\n"),
		},
	})

	if !reflect.DeepEqual(lfChunks, crlfChunks) {
		t.Fatalf("expected LF/CRLF normalization parity:\nlf=%#v\ncrlf=%#v", lfChunks, crlfChunks)
	}

	chunks := buildDeterministicChunkMetadata([]indexedFileContent{
		{
			Repo:    "repo-a",
			Path:    "empty.go",
			Content: []byte(" \n\t\n"),
		},
		{
			Repo:    "repo-a",
			Path:    "valid.go",
			Content: []byte("package main\n"),
		},
	})
	if len(chunks) != 1 {
		t.Fatalf("expected only non-empty content chunks, got %d (%#v)", len(chunks), chunks)
	}
	if chunks[0].Path != "valid.go" {
		t.Fatalf("expected valid.go chunk, got %#v", chunks[0])
	}
}

func TestBuildDeterministicChunkMetadataPathLanguageAndFields(t *testing.T) {
	t.Parallel()

	sourceFields := map[string]any{
		"source_type": "local",
	}

	chunks := buildDeterministicChunkMetadata([]indexedFileContent{
		{
			Repo:     "repo-a",
			Path:     "./src\\main.go",
			Language: "",
			Content:  []byte("package main"),
			Fields:   sourceFields,
		},
		{
			Repo:    "repo-a",
			Path:    "../outside.go",
			Content: []byte("package outside"),
		},
		{
			Repo:    "repo-a",
			Path:    "",
			Content: []byte("package missing"),
		},
	})

	if len(chunks) != 1 {
		t.Fatalf("expected one valid chunk, got %d (%#v)", len(chunks), chunks)
	}
	if chunks[0].Path != "src/main.go" {
		t.Fatalf("expected normalized path src/main.go, got %#v", chunks[0].Path)
	}
	if chunks[0].Language != "go" {
		t.Fatalf("expected language fallback go, got %#v", chunks[0].Language)
	}
	if got := chunks[0].Fields["source_type"]; got != "local" {
		t.Fatalf("expected metadata field source_type=local, got %#v", got)
	}

	sourceFields["source_type"] = "mutated"
	if got := chunks[0].Fields["source_type"]; got != "local" {
		t.Fatalf("expected chunk fields to be cloned, got %#v", got)
	}
}

func TestBuildDeterministicChunkMetadataChunkIDTracksContentChanges(t *testing.T) {
	t.Parallel()

	original := buildDeterministicChunkMetadata([]indexedFileContent{
		{
			Repo:    "repo-a",
			Path:    "main.go",
			Content: []byte("package main\nfunc alpha() {}"),
		},
	})
	updated := buildDeterministicChunkMetadata([]indexedFileContent{
		{
			Repo:    "repo-a",
			Path:    "main.go",
			Content: []byte("package main\nfunc beta() {}"),
		},
	})

	if len(original) != 1 || len(updated) != 1 {
		t.Fatalf("expected one chunk per input: original=%#v updated=%#v", original, updated)
	}
	if original[0].ChunkID == updated[0].ChunkID {
		t.Fatalf("expected chunk id change when content changes: original=%q updated=%q", original[0].ChunkID, updated[0].ChunkID)
	}
}
