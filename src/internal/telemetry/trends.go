package telemetry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// TrendWindow identifies a supported persisted telemetry lookback.
type TrendWindow string

const (
	TrendWindowLast24H TrendWindow = "last_24h"
	TrendWindowLast7D  TrendWindow = "last_7d"
	TrendWindowLast30D TrendWindow = "last_30d"
)

// TrendBucket identifies the aggregation cadence for a trend window.
type TrendBucket string

const (
	TrendBucketHour TrendBucket = "hour"
	TrendBucketDay  TrendBucket = "day"
)

var supportedTrendWindows = []TrendWindow{
	TrendWindowLast24H,
	TrendWindowLast7D,
	TrendWindowLast30D,
}

type trendWindowSpec struct {
	window   TrendWindow
	duration time.Duration
	step     time.Duration
	bucket   TrendBucket
}

var trendWindowSpecs = map[TrendWindow]trendWindowSpec{
	TrendWindowLast24H: {
		window:   TrendWindowLast24H,
		duration: 24 * time.Hour,
		step:     time.Hour,
		bucket:   TrendBucketHour,
	},
	TrendWindowLast7D: {
		window:   TrendWindowLast7D,
		duration: 7 * 24 * time.Hour,
		step:     24 * time.Hour,
		bucket:   TrendBucketDay,
	},
	TrendWindowLast30D: {
		window:   TrendWindowLast30D,
		duration: 30 * 24 * time.Hour,
		step:     24 * time.Hour,
		bucket:   TrendBucketDay,
	},
}

// CompetitorSnapshot captures rollup totals for one competitor profile.
type CompetitorSnapshot struct {
	InputTokensSaved  int     `json:"input_tokens_saved"`
	OutputTokensSaved int     `json:"output_tokens_saved"`
	TokensSaved       int     `json:"tokens_saved"`
	CostAvoidedUSD    float64 `json:"cost_avoided_usd"`
}

// RollupSnapshot exposes explicit per-tool and per-competitor rollups.
type RollupSnapshot struct {
	ToolBreakdown       map[string]ToolSnapshot       `json:"tool_breakdown"`
	CompetitorBreakdown map[string]CompetitorSnapshot `json:"competitor_breakdown"`
}

// TrendPoint captures one aggregated persisted trend bucket.
type TrendPoint struct {
	BucketStart       time.Time               `json:"bucket_start"`
	BucketEnd         time.Time               `json:"bucket_end"`
	CallCount         int                     `json:"call_count"`
	RequestTokens     int                     `json:"request_tokens"`
	ResponseTokens    int                     `json:"response_tokens"`
	TotalTokens       int                     `json:"total_tokens"`
	InputTokensSaved  int                     `json:"input_tokens_saved"`
	OutputTokensSaved int                     `json:"output_tokens_saved"`
	TokensSaved       int                     `json:"tokens_saved"`
	CostAvoidedUSD    map[string]float64      `json:"cost_avoided_usd"`
	ToolBreakdown     map[string]ToolSnapshot `json:"tool_breakdown"`
}

// TrendWindowSnapshot captures a persisted lookback summary and its points.
type TrendWindowSnapshot struct {
	Window              string                        `json:"window"`
	StartAt             time.Time                     `json:"start_at"`
	EndAt               time.Time                     `json:"end_at"`
	Bucket              string                        `json:"bucket"`
	CallCount           int                           `json:"call_count"`
	RequestTokens       int                           `json:"request_tokens"`
	ResponseTokens      int                           `json:"response_tokens"`
	TotalTokens         int                           `json:"total_tokens"`
	InputTokensSaved    int                           `json:"input_tokens_saved"`
	OutputTokensSaved   int                           `json:"output_tokens_saved"`
	TokensSaved         int                           `json:"tokens_saved"`
	CostAvoidedUSD      map[string]float64            `json:"cost_avoided_usd"`
	ToolBreakdown       map[string]ToolSnapshot       `json:"tool_breakdown"`
	CompetitorBreakdown map[string]CompetitorSnapshot `json:"competitor_breakdown"`
	Points              []TrendPoint                  `json:"points"`
}

// TrendQuery defines which persisted windows to aggregate.
type TrendQuery struct {
	Windows []TrendWindow
	Now     time.Time
}

// TrendReader exposes persisted telemetry trend aggregation.
type TrendReader interface {
	QueryTrends(ctx context.Context, query TrendQuery) (map[string]TrendWindowSnapshot, error)
}

// CallEventLoader loads persisted per-call telemetry rows.
type CallEventLoader interface {
	LoadCallEvents(ctx context.Context, since time.Time) ([]PersistedCallEvent, error)
}

// SupportedTrendWindows returns the accepted string values in stable order.
func SupportedTrendWindows() []string {
	out := make([]string, 0, len(supportedTrendWindows))
	for _, window := range supportedTrendWindows {
		out = append(out, string(window))
	}
	return out
}

// ParseTrendWindows validates, normalizes, and deduplicates trend windows.
func ParseTrendWindows(raw []string) ([]TrendWindow, error) {
	windows := make([]TrendWindow, 0, len(raw))
	for _, item := range raw {
		normalized := TrendWindow(strings.ToLower(strings.TrimSpace(item)))
		if normalized == "" {
			continue
		}
		if _, ok := trendWindowSpecs[normalized]; !ok {
			return nil, fmt.Errorf(
				"invalid trend window %q (supported: %s)",
				item,
				strings.Join(SupportedTrendWindows(), ", "),
			)
		}
		windows = append(windows, normalized)
	}
	return normalizeTrendWindows(windows)
}

// BuildRollupSnapshot produces explicit rollups from aggregate scope totals.
func BuildRollupSnapshot(
	inputTokensSaved int,
	outputTokensSaved int,
	toolBreakdown map[string]ToolSnapshot,
	costAvoided map[string]float64,
) RollupSnapshot {
	return RollupSnapshot{
		ToolBreakdown:       cloneToolBreakdown(toolBreakdown),
		CompetitorBreakdown: buildCompetitorBreakdown(inputTokensSaved, outputTokensSaved, costAvoided),
	}
}

func normalizeTrendWindows(windows []TrendWindow) ([]TrendWindow, error) {
	if len(windows) == 0 {
		return []TrendWindow{}, nil
	}

	out := make([]TrendWindow, 0, len(windows))
	seen := map[TrendWindow]struct{}{}
	for _, window := range windows {
		spec, ok := trendWindowSpecs[window]
		if !ok {
			return nil, fmt.Errorf(
				"invalid trend window %q (supported: %s)",
				window,
				strings.Join(SupportedTrendWindows(), ", "),
			)
		}
		if _, ok := seen[spec.window]; ok {
			continue
		}
		seen[spec.window] = struct{}{}
		out = append(out, spec.window)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return trendWindowSpecs[out[i]].duration < trendWindowSpecs[out[j]].duration
	})
	return out, nil
}

func earliestTrendWindowStart(query TrendQuery) time.Time {
	now := query.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	windows, err := normalizeTrendWindows(query.Windows)
	if err != nil || len(windows) == 0 {
		return time.Time{}
	}

	earliest := now
	for _, window := range windows {
		startAt := now.Add(-trendWindowSpecs[window].duration)
		if startAt.Before(earliest) {
			earliest = startAt
		}
	}
	return earliest
}

func aggregateTrendWindows(
	events []PersistedCallEvent,
	query TrendQuery,
	pricing map[string]Pricing,
) (map[string]TrendWindowSnapshot, error) {
	windows, err := normalizeTrendWindows(query.Windows)
	if err != nil {
		return nil, err
	}
	if len(windows) == 0 {
		return map[string]TrendWindowSnapshot{}, nil
	}

	now := query.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	accumulators := make(map[TrendWindow]*trendAccumulator, len(windows))
	for _, window := range windows {
		spec := trendWindowSpecs[window]
		accumulators[window] = newTrendAccumulator(spec, now, pricing)
	}

	for _, rawEvent := range events {
		event := normalizeTrendEvent(rawEvent, now, pricing)
		capturedAt := event.CapturedAt.UTC()
		for _, window := range windows {
			accumulator := accumulators[window]
			if capturedAt.Before(accumulator.startAt) || !capturedAt.Before(accumulator.endAt) {
				continue
			}
			accumulator.add(event)
		}
	}

	out := make(map[string]TrendWindowSnapshot, len(windows))
	for _, window := range windows {
		out[string(window)] = accumulators[window].snapshot()
	}
	return out, nil
}

type trendAccumulator struct {
	spec       trendWindowSpec
	startAt    time.Time
	endAt      time.Time
	window     TrendWindowSnapshot
	pointIndex map[int64]int
}

func newTrendAccumulator(spec trendWindowSpec, now time.Time, pricing map[string]Pricing) *trendAccumulator {
	startAt := now.Add(-spec.duration).UTC()
	endAt := now.UTC()
	points := make([]TrendPoint, 0, 32)
	pointIndex := map[int64]int{}
	for cursor := alignTrendBucket(startAt, spec.step); cursor.Before(endAt); cursor = cursor.Add(spec.step) {
		pointIndex[cursor.UnixNano()] = len(points)
		points = append(points, TrendPoint{
			BucketStart:    cursor,
			BucketEnd:      cursor.Add(spec.step),
			CostAvoidedUSD: zeroCostAvoided(pricing),
			ToolBreakdown:  map[string]ToolSnapshot{},
		})
	}

	return &trendAccumulator{
		spec:    spec,
		startAt: startAt,
		endAt:   endAt,
		window: TrendWindowSnapshot{
			Window:              string(spec.window),
			StartAt:             startAt,
			EndAt:               endAt,
			Bucket:              string(spec.bucket),
			CostAvoidedUSD:      zeroCostAvoided(pricing),
			ToolBreakdown:       map[string]ToolSnapshot{},
			CompetitorBreakdown: map[string]CompetitorSnapshot{},
			Points:              points,
		},
		pointIndex: pointIndex,
	}
}

func (a *trendAccumulator) add(event PersistedCallEvent) {
	mergeTrendSnapshot(&a.window, event.Call)

	bucketStart := alignTrendBucket(event.CapturedAt.UTC(), a.spec.step)
	index, ok := a.pointIndex[bucketStart.UnixNano()]
	if !ok {
		return
	}

	point := a.window.Points[index]
	mergeTrendPoint(&point, event.Call)
	a.window.Points[index] = point
}

func (a *trendAccumulator) snapshot() TrendWindowSnapshot {
	snapshot := a.window
	snapshot.CostAvoidedUSD = cloneCostAvoided(snapshot.CostAvoidedUSD)
	snapshot.ToolBreakdown = cloneToolBreakdown(snapshot.ToolBreakdown)
	snapshot.CompetitorBreakdown = buildCompetitorBreakdown(
		snapshot.InputTokensSaved,
		snapshot.OutputTokensSaved,
		snapshot.CostAvoidedUSD,
	)

	points := make([]TrendPoint, 0, len(snapshot.Points))
	for _, point := range snapshot.Points {
		point.CostAvoidedUSD = cloneCostAvoided(point.CostAvoidedUSD)
		point.ToolBreakdown = cloneToolBreakdown(point.ToolBreakdown)
		points = append(points, point)
	}
	snapshot.Points = points
	return snapshot
}

func mergeTrendSnapshot(snapshot *TrendWindowSnapshot, call CallSnapshot) {
	snapshot.CallCount += sanitizeLogicalCalls(call.LogicalCalls)
	snapshot.RequestTokens += call.RequestTokens
	snapshot.ResponseTokens += call.ResponseTokens
	snapshot.TotalTokens += call.TotalTokens
	snapshot.InputTokensSaved += call.InputTokensSaved
	snapshot.OutputTokensSaved += call.OutputTokensSaved
	snapshot.TokensSaved += call.TokensSaved
	mergeCostAvoided(snapshot.CostAvoidedUSD, call.CostAvoidedUSD)
	mergeToolSnapshot(snapshot.ToolBreakdown, call.ToolName, call, call.CostAvoidedUSD)
}

func mergeTrendPoint(point *TrendPoint, call CallSnapshot) {
	point.CallCount += sanitizeLogicalCalls(call.LogicalCalls)
	point.RequestTokens += call.RequestTokens
	point.ResponseTokens += call.ResponseTokens
	point.TotalTokens += call.TotalTokens
	point.InputTokensSaved += call.InputTokensSaved
	point.OutputTokensSaved += call.OutputTokensSaved
	point.TokensSaved += call.TokensSaved
	mergeCostAvoided(point.CostAvoidedUSD, call.CostAvoidedUSD)
	mergeToolSnapshot(point.ToolBreakdown, call.ToolName, call, call.CostAvoidedUSD)
}

func cloneToolBreakdown(tools map[string]ToolSnapshot) map[string]ToolSnapshot {
	if len(tools) == 0 {
		return map[string]ToolSnapshot{}
	}
	cloned := make(map[string]ToolSnapshot, len(tools))
	for toolName, snapshot := range tools {
		snapshot.CostAvoidedUSD = cloneCostAvoided(snapshot.CostAvoidedUSD)
		cloned[toolName] = snapshot
	}
	return cloned
}

func buildCompetitorBreakdown(
	inputTokensSaved int,
	outputTokensSaved int,
	costAvoided map[string]float64,
) map[string]CompetitorSnapshot {
	if len(costAvoided) == 0 {
		return map[string]CompetitorSnapshot{}
	}

	inputTokensSaved = sanitizeNonNegative(inputTokensSaved)
	outputTokensSaved = sanitizeNonNegative(outputTokensSaved)
	tokensSaved := inputTokensSaved + outputTokensSaved

	out := make(map[string]CompetitorSnapshot, len(costAvoided))
	for competitor, cost := range costAvoided {
		out[competitor] = CompetitorSnapshot{
			InputTokensSaved:  inputTokensSaved,
			OutputTokensSaved: outputTokensSaved,
			TokensSaved:       tokensSaved,
			CostAvoidedUSD:    roundUSD(cost),
		}
	}
	return out
}

func normalizeTrendEvent(
	event PersistedCallEvent,
	fallback time.Time,
	pricing map[string]Pricing,
) PersistedCallEvent {
	call := event.Call

	event.CapturedAt = event.CapturedAt.UTC()
	if event.CapturedAt.IsZero() {
		if !call.FinishedAt.IsZero() {
			event.CapturedAt = call.FinishedAt.UTC()
		} else {
			event.CapturedAt = fallback.UTC()
		}
	}

	call.ToolName = normalizeToolName(call.ToolName)
	call.StartedAt = call.StartedAt.UTC()
	if call.StartedAt.IsZero() {
		call.StartedAt = event.CapturedAt
	}

	call.FinishedAt = call.FinishedAt.UTC()
	if call.FinishedAt.IsZero() {
		call.FinishedAt = event.CapturedAt
	}
	if call.FinishedAt.Before(call.StartedAt) {
		call.FinishedAt = call.StartedAt
	}

	call.RequestTokens = sanitizeNonNegative(call.RequestTokens)
	call.ResponseTokens = sanitizeNonNegative(call.ResponseTokens)
	call.TotalTokens = sanitizeNonNegative(call.TotalTokens)
	if minimumTotalTokens := call.RequestTokens + call.ResponseTokens; call.TotalTokens < minimumTotalTokens {
		call.TotalTokens = minimumTotalTokens
	}

	call.InputTokensSaved = sanitizeNonNegative(call.InputTokensSaved)
	call.OutputTokensSaved = sanitizeNonNegative(call.OutputTokensSaved)
	call.TokensSaved = sanitizeNonNegative(call.TokensSaved)
	if minimumTokensSaved := call.InputTokensSaved + call.OutputTokensSaved; call.TokensSaved < minimumTokensSaved {
		call.TokensSaved = minimumTokensSaved
	}

	call.LogicalCalls = sanitizeLogicalCalls(call.LogicalCalls)
	call.DurationMS = durationMS(call.StartedAt, call.FinishedAt)
	call.CostAvoidedUSD = normalizeCostAvoided(call.CostAvoidedUSD, pricing)

	event.Call = call
	return event
}

func alignTrendBucket(timestamp time.Time, step time.Duration) time.Time {
	if step <= 0 {
		return timestamp.UTC()
	}
	return timestamp.UTC().Truncate(step)
}
