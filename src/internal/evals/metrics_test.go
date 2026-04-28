package evals

import (
	"math"
	"testing"
	"time"
)

func TestRecallAtK(t *testing.T) {
	testCases := []struct {
		name            string
		rankedDocIDs    []string
		relevanceByDoc  map[string]int
		k               int
		want            float64
	}{
		{
			name: "returns zero for invalid inputs",
			rankedDocIDs: nil,
			relevanceByDoc: map[string]int{"doc-1": 1},
			k:    5,
			want: 0,
		},
		{
			name: "deduplicates repeated ranked ids",
			rankedDocIDs: []string{"doc-a", "doc-a", "doc-b", "doc-c"},
			relevanceByDoc: map[string]int{
				"doc-a": 1,
				"doc-b": 2,
				"doc-x": 1,
				"doc-c": 0,
			},
			k:    3,
			want: 2.0 / 3.0,
		},
		{
			name: "caps evaluation at k",
			rankedDocIDs: []string{"doc-a", "doc-c", "doc-b"},
			relevanceByDoc: map[string]int{
				"doc-a": 1,
				"doc-b": 1,
			},
			k:    1,
			want: 0.5,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := RecallAtK(testCase.rankedDocIDs, testCase.relevanceByDoc, testCase.k)
			if !almostEqual(got, testCase.want, 1e-12) {
				t.Fatalf("expected recall@k=%v, got=%v", testCase.want, got)
			}
		})
	}
}

func TestMRRAtK(t *testing.T) {
	testCases := []struct {
		name            string
		rankedDocIDs    []string
		relevanceByDoc  map[string]int
		k               int
		want            float64
	}{
		{
			name: "returns zero for invalid inputs",
			rankedDocIDs: nil,
			relevanceByDoc: map[string]int{"doc-1": 1},
			k:    5,
			want: 0,
		},
		{
			name: "first relevant at top rank",
			rankedDocIDs: []string{"doc-a", "doc-b", "doc-c"},
			relevanceByDoc: map[string]int{
				"doc-a": 1,
			},
			k:    3,
			want: 1,
		},
		{
			name: "first relevant at third rank",
			rankedDocIDs: []string{"doc-a", "doc-b", "doc-c"},
			relevanceByDoc: map[string]int{
				"doc-c": 1,
			},
			k:    3,
			want: 1.0 / 3.0,
		},
		{
			name: "relevant outside k is ignored",
			rankedDocIDs: []string{"doc-a", "doc-b", "doc-c"},
			relevanceByDoc: map[string]int{
				"doc-c": 1,
			},
			k:    2,
			want: 0,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := MRRAtK(testCase.rankedDocIDs, testCase.relevanceByDoc, testCase.k)
			if !almostEqual(got, testCase.want, 1e-12) {
				t.Fatalf("expected mrr@k=%v, got=%v", testCase.want, got)
			}
		})
	}
}

func TestComputeLatencyPercentiles(t *testing.T) {
	testCases := []struct {
		name      string
		latencies []time.Duration
		want      LatencyPercentiles
	}{
		{
			name:      "empty samples return zero values",
			latencies: nil,
			want:      LatencyPercentiles{},
		},
		{
			name:      "single sample returns same p50 and p95",
			latencies: []time.Duration{42 * time.Millisecond},
			want: LatencyPercentiles{
				P50MS: 42,
				P95MS: 42,
			},
		},
		{
			name: "negative samples clamp to zero and percentiles interpolate",
			latencies: []time.Duration{
				-5 * time.Millisecond,
				100 * time.Millisecond,
				30 * time.Millisecond,
				20 * time.Millisecond,
			},
			want: LatencyPercentiles{
				P50MS: 25,
				P95MS: 89.5,
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ComputeLatencyPercentiles(testCase.latencies)
			if !almostEqual(got.P50MS, testCase.want.P50MS, 1e-12) {
				t.Fatalf("expected p50_ms=%v, got=%v", testCase.want.P50MS, got.P50MS)
			}
			if !almostEqual(got.P95MS, testCase.want.P95MS, 1e-12) {
				t.Fatalf("expected p95_ms=%v, got=%v", testCase.want.P95MS, got.P95MS)
			}
		})
	}
}

func almostEqual(left, right, epsilon float64) bool {
	return math.Abs(left-right) <= epsilon
}
