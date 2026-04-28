package evals

import (
	"math"
	"slices"
	"time"
)

// LatencyPercentiles stores latency percentile metrics in milliseconds.
type LatencyPercentiles struct {
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
}

// RecallAtK returns recall@k for one ranked result set.
// Any relevance score greater than zero is considered relevant.
func RecallAtK(rankedDocIDs []string, relevanceByDocID map[string]int, k int) float64 {
	if k <= 0 || len(rankedDocIDs) == 0 || len(relevanceByDocID) == 0 {
		return 0
	}

	relevantTotal := countRelevant(relevanceByDocID)
	if relevantTotal == 0 {
		return 0
	}

	limit := min(k, len(rankedDocIDs))
	relevantRetrieved := 0
	seen := make(map[string]struct{}, limit)
	for i := 0; i < limit; i++ {
		docID := rankedDocIDs[i]
		if _, exists := seen[docID]; exists {
			continue
		}
		seen[docID] = struct{}{}
		if relevanceByDocID[docID] > 0 {
			relevantRetrieved++
		}
	}

	return float64(relevantRetrieved) / float64(relevantTotal)
}

// MRRAtK returns reciprocal rank@k for one ranked result set.
// Any relevance score greater than zero is considered relevant.
func MRRAtK(rankedDocIDs []string, relevanceByDocID map[string]int, k int) float64 {
	if k <= 0 || len(rankedDocIDs) == 0 || len(relevanceByDocID) == 0 {
		return 0
	}

	limit := min(k, len(rankedDocIDs))
	for i := 0; i < limit; i++ {
		if relevanceByDocID[rankedDocIDs[i]] > 0 {
			return 1 / float64(i+1)
		}
	}

	return 0
}

// ComputeLatencyPercentiles calculates p50 and p95 latency in milliseconds.
func ComputeLatencyPercentiles(latencies []time.Duration) LatencyPercentiles {
	if len(latencies) == 0 {
		return LatencyPercentiles{}
	}

	samplesMS := make([]float64, 0, len(latencies))
	for _, sample := range latencies {
		if sample < 0 {
			sample = 0
		}
		samplesMS = append(samplesMS, float64(sample)/float64(time.Millisecond))
	}

	return LatencyPercentiles{
		P50MS: percentile(samplesMS, 50),
		P95MS: percentile(samplesMS, 95),
	}
}

func countRelevant(relevanceByDocID map[string]int) int {
	total := 0
	for _, relevance := range relevanceByDocID {
		if relevance > 0 {
			total++
		}
	}
	return total
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if len(values) == 1 {
		return values[0]
	}

	clampedP := math.Max(0, math.Min(100, p))
	sorted := slices.Clone(values)
	slices.Sort(sorted)

	position := (clampedP / 100.0) * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}

	weight := position - float64(lower)
	return sorted[lower] + (sorted[upper]-sorted[lower])*weight
}
