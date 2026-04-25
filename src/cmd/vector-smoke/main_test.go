package main

import (
	"testing"
)

func TestNormalizeTopK(t *testing.T) {
	testCases := []struct {
		name      string
		requested int
		corpus    int
		want      int
		wantErr   bool
	}{
		{name: "exact size", requested: 3, corpus: 6, want: 3},
		{name: "clamps to corpus", requested: 99, corpus: 6, want: 6},
		{name: "zero requested", requested: 0, corpus: 6, wantErr: true},
		{name: "negative requested", requested: -1, corpus: 6, wantErr: true},
		{name: "empty corpus", requested: 1, corpus: 0, wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeTopK(tc.requested, tc.corpus)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for requested=%d corpus=%d", tc.requested, tc.corpus)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeTopK returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("unexpected top-k: got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestBuildFixtureRecords(t *testing.T) {
	chunks := []fixtureChunk{
		{
			ID:        "fixture-1",
			Path:      "fixtures/go/example.go",
			Language:  "go",
			StartLine: 1,
			EndLine:   10,
			Text:      "decode json",
		},
	}
	embeddings := [][]float32{{1, 2, 3}}

	records, err := buildFixtureRecords("demo/ns", chunks, embeddings)
	if err != nil {
		t.Fatalf("buildFixtureRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one record, got %d", len(records))
	}
	if got := records[0].ID; got != "fixture-1" {
		t.Fatalf("unexpected record id: %q", got)
	}
	if got := records[0].Namespace; got != "demo/ns" {
		t.Fatalf("unexpected namespace: %q", got)
	}
	if got := records[0].Metadata.ChunkText; got != "decode json" {
		t.Fatalf("unexpected chunk text: %q", got)
	}
	if got := records[0].Metadata.Fields["fixture"]; got != true {
		t.Fatalf("expected fixture field true, got %#v", got)
	}

	// Verify embedding slices are cloned defensively.
	embeddings[0][0] = 99
	if records[0].Embedding[0] == 99 {
		t.Fatal("expected stored embedding to remain unchanged after input mutation")
	}
}

func TestBuildFixtureRecordsValidation(t *testing.T) {
	if _, err := buildFixtureRecords("", nil, nil); err == nil {
		t.Fatal("expected empty namespace validation error")
	}

	if _, err := buildFixtureRecords("ns", []fixtureChunk{{ID: "a"}}, nil); err == nil {
		t.Fatal("expected chunk/embedding mismatch error")
	}

	if _, err := buildFixtureRecords("ns", []fixtureChunk{{ID: ""}}, [][]float32{{1}}); err == nil {
		t.Fatal("expected missing fixture id validation error")
	}

	if _, err := buildFixtureRecords("ns", []fixtureChunk{{ID: "a"}}, [][]float32{{}}); err == nil {
		t.Fatal("expected empty embedding validation error")
	}
}

func TestCompactSnippet(t *testing.T) {
	if got := compactSnippet("a    b   c", 100); got != "a b c" {
		t.Fatalf("expected whitespace compaction, got %q", got)
	}

	if got := compactSnippet("0123456789", 6); got != "012..." {
		t.Fatalf("expected truncated snippet, got %q", got)
	}
}
