package telemetry

import (
	"math"
	"strings"
	"sync"
	"time"
)

// Pricing captures competitor token pricing in USD per million tokens.
type Pricing struct {
	InputUSDPerMTok  float64
	OutputUSDPerMTok float64
}

// CallRecord captures one completed tool call's token estimates.
type CallRecord struct {
	ToolName          string
	StartedAt         time.Time
	FinishedAt        time.Time
	RequestTokens     int
	ResponseTokens    int
	InputTokensSaved  int
	OutputTokensSaved int
	LogicalCalls      int
}

// CallSnapshot captures normalized per-call telemetry.
type CallSnapshot struct {
	ToolName          string             `json:"tool_name"`
	StartedAt         time.Time          `json:"started_at"`
	FinishedAt        time.Time          `json:"finished_at"`
	DurationMS        float64            `json:"duration_ms"`
	RequestTokens     int                `json:"request_tokens"`
	ResponseTokens    int                `json:"response_tokens"`
	TotalTokens       int                `json:"total_tokens"`
	InputTokensSaved  int                `json:"input_tokens_saved"`
	OutputTokensSaved int                `json:"output_tokens_saved"`
	TokensSaved       int                `json:"tokens_saved"`
	LogicalCalls      int                `json:"logical_calls,omitempty"`
	CostAvoidedUSD    map[string]float64 `json:"cost_avoided_usd"`
}

// ToolSnapshot captures per-tool cumulative telemetry.
type ToolSnapshot struct {
	CallCount         int                `json:"call_count"`
	RequestTokens     int                `json:"request_tokens"`
	ResponseTokens    int                `json:"response_tokens"`
	TotalTokens       int                `json:"total_tokens"`
	InputTokensSaved  int                `json:"input_tokens_saved"`
	OutputTokensSaved int                `json:"output_tokens_saved"`
	TokensSaved       int                `json:"tokens_saved"`
	CostAvoidedUSD    map[string]float64 `json:"cost_avoided_usd"`
	LastCallAt        time.Time          `json:"last_call_at"`
}

// SessionSnapshot captures current-process totals.
type SessionSnapshot struct {
	StartedAt         time.Time               `json:"started_at"`
	LastUpdatedAt     time.Time               `json:"last_updated_at"`
	DurationS         float64                 `json:"duration_s"`
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

// CumulativeSnapshot captures persisted multi-session totals.
type CumulativeSnapshot struct {
	FirstRecordedAt   time.Time               `json:"first_recorded_at"`
	LastRecordedAt    time.Time               `json:"last_recorded_at"`
	SessionCount      int                     `json:"session_count"`
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

// PersistedCumulativeSnapshot captures a point-in-time cumulative snapshot row.
type PersistedCumulativeSnapshot struct {
	CapturedAt time.Time          `json:"captured_at"`
	Cumulative CumulativeSnapshot `json:"cumulative"`
}

// PersistedCallEvent captures one persisted per-call telemetry event row.
type PersistedCallEvent struct {
	CapturedAt time.Time    `json:"captured_at"`
	Call       CallSnapshot `json:"call"`
}

// Collector is the runtime-facing telemetry contract.
type Collector interface {
	RecordCall(record CallRecord) CallSnapshot
	SessionSnapshot() SessionSnapshot
	CumulativeSnapshot() CumulativeSnapshot
}

// Tracker maintains in-memory telemetry aggregates.
type Tracker struct {
	mu         sync.RWMutex
	now        func() time.Time
	pricing    map[string]Pricing
	session    SessionSnapshot
	cumulative CumulativeSnapshot
	revision   uint64
}

// NewTracker builds a tracker with deterministic competitor keys.
func NewTracker(pricing map[string]Pricing, now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}

	startedAt := now().UTC()
	return &Tracker{
		now:     now,
		pricing: clonePricingMap(pricing),
		session: SessionSnapshot{
			StartedAt:      startedAt,
			CostAvoidedUSD: zeroCostAvoided(pricing),
			ToolBreakdown:  map[string]ToolSnapshot{},
		},
		cumulative: CumulativeSnapshot{
			CostAvoidedUSD: zeroCostAvoided(pricing),
			ToolBreakdown:  map[string]ToolSnapshot{},
		},
	}
}

// RestoreCumulative loads persisted totals into the current tracker.
func (t *Tracker) RestoreCumulative(snapshot CumulativeSnapshot) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.cumulative = normalizeCumulativeSnapshot(snapshot, t.pricing)
}

// RecordCall updates per-call, per-tool, per-session, and cumulative telemetry.
func (t *Tracker) RecordCall(record CallRecord) CallSnapshot {
	if t == nil {
		return CallSnapshot{}
	}

	normalized := t.normalizeCallRecord(record)
	costAvoided := t.costAvoidedFor(normalized.InputTokensSaved, normalized.OutputTokensSaved)

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.session.CallCount == 0 {
		t.cumulative.SessionCount++
	}

	t.session.LastUpdatedAt = normalized.FinishedAt
	t.session.CallCount += normalized.LogicalCalls
	t.session.RequestTokens += normalized.RequestTokens
	t.session.ResponseTokens += normalized.ResponseTokens
	t.session.TotalTokens += normalized.TotalTokens
	t.session.InputTokensSaved += normalized.InputTokensSaved
	t.session.OutputTokensSaved += normalized.OutputTokensSaved
	t.session.TokensSaved += normalized.TokensSaved
	mergeCostAvoided(t.session.CostAvoidedUSD, costAvoided)
	mergeToolSnapshot(t.session.ToolBreakdown, normalized.ToolName, normalized, costAvoided)

	if t.cumulative.FirstRecordedAt.IsZero() || normalized.StartedAt.Before(t.cumulative.FirstRecordedAt) {
		t.cumulative.FirstRecordedAt = normalized.StartedAt
	}
	if t.cumulative.LastRecordedAt.IsZero() || normalized.FinishedAt.After(t.cumulative.LastRecordedAt) {
		t.cumulative.LastRecordedAt = normalized.FinishedAt
	}
	t.cumulative.CallCount += normalized.LogicalCalls
	t.cumulative.RequestTokens += normalized.RequestTokens
	t.cumulative.ResponseTokens += normalized.ResponseTokens
	t.cumulative.TotalTokens += normalized.TotalTokens
	t.cumulative.InputTokensSaved += normalized.InputTokensSaved
	t.cumulative.OutputTokensSaved += normalized.OutputTokensSaved
	t.cumulative.TokensSaved += normalized.TokensSaved
	mergeCostAvoided(t.cumulative.CostAvoidedUSD, costAvoided)
	mergeToolSnapshot(t.cumulative.ToolBreakdown, normalized.ToolName, normalized, costAvoided)

	t.revision++

	return CallSnapshot{
		ToolName:          normalized.ToolName,
		StartedAt:         normalized.StartedAt,
		FinishedAt:        normalized.FinishedAt,
		DurationMS:        durationMS(normalized.StartedAt, normalized.FinishedAt),
		RequestTokens:     normalized.RequestTokens,
		ResponseTokens:    normalized.ResponseTokens,
		TotalTokens:       normalized.TotalTokens,
		InputTokensSaved:  normalized.InputTokensSaved,
		OutputTokensSaved: normalized.OutputTokensSaved,
		TokensSaved:       normalized.TokensSaved,
		LogicalCalls:      normalized.LogicalCalls,
		CostAvoidedUSD:    cloneCostAvoided(costAvoided),
	}
}

// SessionSnapshot returns a stable copy of current session metrics.
func (t *Tracker) SessionSnapshot() SessionSnapshot {
	if t == nil {
		return SessionSnapshot{}
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	snapshot := cloneSessionSnapshot(t.session, t.pricing)
	snapshot.DurationS = durationSeconds(snapshot.StartedAt, t.now().UTC())
	return snapshot
}

// CumulativeSnapshot returns a stable copy of current cumulative totals.
func (t *Tracker) CumulativeSnapshot() CumulativeSnapshot {
	if t == nil {
		return CumulativeSnapshot{}
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	return cloneCumulativeSnapshot(t.cumulative, t.pricing)
}

func (t *Tracker) normalizeCallRecord(record CallRecord) CallSnapshot {
	now := t.now().UTC()
	startedAt := record.StartedAt.UTC()
	if startedAt.IsZero() {
		startedAt = now
	}
	finishedAt := record.FinishedAt.UTC()
	if finishedAt.IsZero() {
		finishedAt = startedAt
	}
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}

	requestTokens := sanitizeNonNegative(record.RequestTokens)
	responseTokens := sanitizeNonNegative(record.ResponseTokens)
	inputTokensSaved := sanitizeNonNegative(record.InputTokensSaved)
	outputTokensSaved := sanitizeNonNegative(record.OutputTokensSaved)
	logicalCalls := sanitizeLogicalCalls(record.LogicalCalls)

	return CallSnapshot{
		ToolName:          normalizeToolName(record.ToolName),
		StartedAt:         startedAt,
		FinishedAt:        finishedAt,
		RequestTokens:     requestTokens,
		ResponseTokens:    responseTokens,
		TotalTokens:       requestTokens + responseTokens,
		InputTokensSaved:  inputTokensSaved,
		OutputTokensSaved: outputTokensSaved,
		TokensSaved:       inputTokensSaved + outputTokensSaved,
		LogicalCalls:      logicalCalls,
	}
}

func (t *Tracker) costAvoidedFor(inputTokensSaved, outputTokensSaved int) map[string]float64 {
	costs := zeroCostAvoided(t.pricing)
	for competitor, pricing := range t.pricing {
		inputCost := float64(inputTokensSaved) * pricing.InputUSDPerMTok / 1_000_000.0
		outputCost := float64(outputTokensSaved) * pricing.OutputUSDPerMTok / 1_000_000.0
		costs[competitor] = roundUSD(inputCost + outputCost)
	}
	return costs
}

func (t *Tracker) currentRevision() uint64 {
	if t == nil {
		return 0
	}

	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.revision
}

func normalizeToolName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func sanitizeNonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func sanitizeLogicalCalls(value int) int {
	if value <= 0 {
		return 1
	}
	return value
}

func zeroCostAvoided(pricing map[string]Pricing) map[string]float64 {
	zeroes := make(map[string]float64, len(pricing))
	for competitor := range pricing {
		zeroes[competitor] = 0
	}
	return zeroes
}

func clonePricingMap(pricing map[string]Pricing) map[string]Pricing {
	if len(pricing) == 0 {
		return map[string]Pricing{}
	}
	cloned := make(map[string]Pricing, len(pricing))
	for competitor, price := range pricing {
		cloned[competitor] = price
	}
	return cloned
}

func cloneCostAvoided(costs map[string]float64) map[string]float64 {
	if len(costs) == 0 {
		return map[string]float64{}
	}
	cloned := make(map[string]float64, len(costs))
	for competitor, cost := range costs {
		cloned[competitor] = roundUSD(cost)
	}
	return cloned
}

func mergeCostAvoided(target map[string]float64, delta map[string]float64) {
	for competitor, cost := range delta {
		target[competitor] = roundUSD(target[competitor] + cost)
	}
}

func mergeToolSnapshot(
	target map[string]ToolSnapshot,
	toolName string,
	call CallSnapshot,
	costAvoided map[string]float64,
) {
	tool := target[toolName]
	if tool.CostAvoidedUSD == nil {
		tool.CostAvoidedUSD = map[string]float64{}
	}
	tool.CallCount += sanitizeLogicalCalls(call.LogicalCalls)
	tool.RequestTokens += call.RequestTokens
	tool.ResponseTokens += call.ResponseTokens
	tool.TotalTokens += call.TotalTokens
	tool.InputTokensSaved += call.InputTokensSaved
	tool.OutputTokensSaved += call.OutputTokensSaved
	tool.TokensSaved += call.TokensSaved
	mergeCostAvoided(tool.CostAvoidedUSD, costAvoided)
	if tool.LastCallAt.IsZero() || call.FinishedAt.After(tool.LastCallAt) {
		tool.LastCallAt = call.FinishedAt
	}
	target[toolName] = tool
}

func normalizeCumulativeSnapshot(
	snapshot CumulativeSnapshot,
	pricing map[string]Pricing,
) CumulativeSnapshot {
	snapshot.SessionCount = sanitizeNonNegative(snapshot.SessionCount)
	snapshot.CallCount = sanitizeNonNegative(snapshot.CallCount)
	snapshot.RequestTokens = sanitizeNonNegative(snapshot.RequestTokens)
	snapshot.ResponseTokens = sanitizeNonNegative(snapshot.ResponseTokens)
	if snapshot.TotalTokens == 0 && (snapshot.RequestTokens > 0 || snapshot.ResponseTokens > 0) {
		snapshot.TotalTokens = snapshot.RequestTokens + snapshot.ResponseTokens
	}
	snapshot.TotalTokens = sanitizeNonNegative(snapshot.TotalTokens)
	snapshot.InputTokensSaved = sanitizeNonNegative(snapshot.InputTokensSaved)
	snapshot.OutputTokensSaved = sanitizeNonNegative(snapshot.OutputTokensSaved)
	if snapshot.TokensSaved == 0 && (snapshot.InputTokensSaved > 0 || snapshot.OutputTokensSaved > 0) {
		snapshot.TokensSaved = snapshot.InputTokensSaved + snapshot.OutputTokensSaved
	}
	snapshot.TokensSaved = sanitizeNonNegative(snapshot.TokensSaved)
	snapshot.CostAvoidedUSD = normalizeCostAvoided(snapshot.CostAvoidedUSD, pricing)
	snapshot.ToolBreakdown = normalizeToolBreakdown(snapshot.ToolBreakdown, pricing)
	return snapshot
}

func cloneSessionSnapshot(snapshot SessionSnapshot, pricing map[string]Pricing) SessionSnapshot {
	clone := snapshot
	clone.CostAvoidedUSD = normalizeCostAvoided(snapshot.CostAvoidedUSD, pricing)
	clone.ToolBreakdown = normalizeToolBreakdown(snapshot.ToolBreakdown, pricing)
	return clone
}

func cloneCumulativeSnapshot(snapshot CumulativeSnapshot, pricing map[string]Pricing) CumulativeSnapshot {
	clone := snapshot
	clone.CostAvoidedUSD = normalizeCostAvoided(snapshot.CostAvoidedUSD, pricing)
	clone.ToolBreakdown = normalizeToolBreakdown(snapshot.ToolBreakdown, pricing)
	return clone
}

func normalizeToolBreakdown(
	tools map[string]ToolSnapshot,
	pricing map[string]Pricing,
) map[string]ToolSnapshot {
	if len(tools) == 0 {
		return map[string]ToolSnapshot{}
	}
	cloned := make(map[string]ToolSnapshot, len(tools))
	for toolName, snapshot := range tools {
		snapshot.CallCount = sanitizeNonNegative(snapshot.CallCount)
		snapshot.RequestTokens = sanitizeNonNegative(snapshot.RequestTokens)
		snapshot.ResponseTokens = sanitizeNonNegative(snapshot.ResponseTokens)
		if snapshot.TotalTokens == 0 && (snapshot.RequestTokens > 0 || snapshot.ResponseTokens > 0) {
			snapshot.TotalTokens = snapshot.RequestTokens + snapshot.ResponseTokens
		}
		snapshot.TotalTokens = sanitizeNonNegative(snapshot.TotalTokens)
		snapshot.InputTokensSaved = sanitizeNonNegative(snapshot.InputTokensSaved)
		snapshot.OutputTokensSaved = sanitizeNonNegative(snapshot.OutputTokensSaved)
		if snapshot.TokensSaved == 0 && (snapshot.InputTokensSaved > 0 || snapshot.OutputTokensSaved > 0) {
			snapshot.TokensSaved = snapshot.InputTokensSaved + snapshot.OutputTokensSaved
		}
		snapshot.TokensSaved = sanitizeNonNegative(snapshot.TokensSaved)
		snapshot.CostAvoidedUSD = normalizeCostAvoided(snapshot.CostAvoidedUSD, pricing)
		cloned[toolName] = snapshot
	}
	return cloned
}

func normalizeCostAvoided(
	costs map[string]float64,
	pricing map[string]Pricing,
) map[string]float64 {
	normalized := zeroCostAvoided(pricing)
	for competitor, cost := range costs {
		normalized[competitor] = roundUSD(cost)
	}
	return normalized
}

func durationMS(startedAt, finishedAt time.Time) float64 {
	if startedAt.IsZero() || finishedAt.IsZero() || finishedAt.Before(startedAt) {
		return 0
	}
	return float64(finishedAt.Sub(startedAt).Microseconds()) / 1000.0
}

func durationSeconds(startedAt, finishedAt time.Time) float64 {
	if startedAt.IsZero() || finishedAt.IsZero() || finishedAt.Before(startedAt) {
		return 0
	}
	return math.Round(finishedAt.Sub(startedAt).Seconds()*1000) / 1000
}

func roundUSD(value float64) float64 {
	return math.Round(value*1_000_000_000_000) / 1_000_000_000_000
}
