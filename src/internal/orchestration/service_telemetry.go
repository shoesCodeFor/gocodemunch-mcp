package orchestration

import (
	"encoding/json"
	"math"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/telemetry"
)

const estimatedSerializedBytesPerToken = 4.0

var defaultSavingsCompetitors = []string{
	"claude_code",
	"codex",
	"amp",
}

func (s *Service) recordTelemetry(
	name string,
	arguments map[string]any,
	payload map[string]any,
	startedAt time.Time,
) (telemetry.CallSnapshot, telemetry.SessionSnapshot, telemetry.CumulativeSnapshot) {
	collector := s.deps.Telemetry
	if collector == nil {
		return telemetry.CallSnapshot{}, s.zeroSessionSnapshot(), s.zeroCumulativeSnapshot()
	}

	if payload == nil {
		payload = map[string]any{}
	}

	call := collector.RecordCall(telemetry.CallRecord{
		ToolName:          name,
		StartedAt:         startedAt.UTC(),
		FinishedAt:        time.Now().UTC(),
		RequestTokens:     estimateSerializedTokens(map[string]any{"name": name, "arguments": arguments}),
		ResponseTokens:    estimateSerializedTokens(payload),
		InputTokensSaved:  estimateSerializedTokens(map[string]any{"name": name, "arguments": arguments}),
		OutputTokensSaved: estimateSerializedTokens(payload),
	})

	return call, s.normalizeSessionSnapshot(collector.SessionSnapshot()), s.normalizeCumulativeSnapshot(collector.CumulativeSnapshot())
}

func (s *Service) applySavingsMeta(
	payload map[string]any,
	call telemetry.CallSnapshot,
	cumulative telemetry.CumulativeSnapshot,
) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}

	meta, _ := payload["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		payload["_meta"] = meta
	}

	meta["tokens_saved"] = call.TokensSaved
	meta["total_tokens_saved"] = cumulative.TokensSaved
	return payload
}

func (s *Service) applySessionStatsPayload(
	payload map[string]any,
	session telemetry.SessionSnapshot,
	cumulative telemetry.CumulativeSnapshot,
) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}

	session = s.normalizeSessionSnapshot(session)
	cumulative = s.normalizeCumulativeSnapshot(cumulative)

	payload["session_tokens_saved"] = session.TokensSaved
	payload["session_calls"] = session.CallCount
	payload["session_duration_s"] = session.DurationS
	payload["session_request_tokens"] = session.RequestTokens
	payload["session_response_tokens"] = session.ResponseTokens
	payload["session_input_tokens_saved"] = session.InputTokensSaved
	payload["session_output_tokens_saved"] = session.OutputTokensSaved
	payload["session_cost_avoided"] = session.CostAvoidedUSD
	payload["tool_breakdown"] = session.ToolBreakdown
	payload["total_tokens_saved"] = cumulative.TokensSaved
	payload["total_calls"] = cumulative.CallCount
	payload["total_sessions"] = cumulative.SessionCount
	payload["total_request_tokens"] = cumulative.RequestTokens
	payload["total_response_tokens"] = cumulative.ResponseTokens
	payload["total_input_tokens_saved"] = cumulative.InputTokensSaved
	payload["total_output_tokens_saved"] = cumulative.OutputTokensSaved
	payload["total_cost_avoided"] = cumulative.CostAvoidedUSD
	payload["total_tool_breakdown"] = cumulative.ToolBreakdown
	return payload
}

func (s *Service) normalizeSessionSnapshot(snapshot telemetry.SessionSnapshot) telemetry.SessionSnapshot {
	if snapshot.CostAvoidedUSD == nil {
		snapshot.CostAvoidedUSD = s.zeroCostAvoidedUSD()
	} else {
		snapshot.CostAvoidedUSD = s.normalizeCostAvoidedUSD(snapshot.CostAvoidedUSD)
	}
	if snapshot.ToolBreakdown == nil {
		snapshot.ToolBreakdown = map[string]telemetry.ToolSnapshot{}
	}
	return snapshot
}

func (s *Service) normalizeCumulativeSnapshot(
	snapshot telemetry.CumulativeSnapshot,
) telemetry.CumulativeSnapshot {
	if snapshot.CostAvoidedUSD == nil {
		snapshot.CostAvoidedUSD = s.zeroCostAvoidedUSD()
	} else {
		snapshot.CostAvoidedUSD = s.normalizeCostAvoidedUSD(snapshot.CostAvoidedUSD)
	}
	if snapshot.ToolBreakdown == nil {
		snapshot.ToolBreakdown = map[string]telemetry.ToolSnapshot{}
	}
	return snapshot
}

func (s *Service) zeroSessionSnapshot() telemetry.SessionSnapshot {
	return telemetry.SessionSnapshot{
		CostAvoidedUSD: s.zeroCostAvoidedUSD(),
		ToolBreakdown:  map[string]telemetry.ToolSnapshot{},
	}
}

func (s *Service) zeroCumulativeSnapshot() telemetry.CumulativeSnapshot {
	return telemetry.CumulativeSnapshot{
		CostAvoidedUSD: s.zeroCostAvoidedUSD(),
		ToolBreakdown:  map[string]telemetry.ToolSnapshot{},
	}
}

func (s *Service) zeroCostAvoidedUSD() map[string]float64 {
	pricing := s.cfg.SavingsCompetitorPricing
	if len(pricing) == 0 {
		zeroes := make(map[string]float64, len(defaultSavingsCompetitors))
		for _, competitor := range defaultSavingsCompetitors {
			zeroes[competitor] = 0
		}
		return zeroes
	}

	zeroes := make(map[string]float64, len(pricing))
	for competitor := range pricing {
		zeroes[competitor] = 0
	}
	return zeroes
}

func (s *Service) normalizeCostAvoidedUSD(costs map[string]float64) map[string]float64 {
	normalized := s.zeroCostAvoidedUSD()
	for competitor, value := range costs {
		normalized[competitor] = value
	}
	return normalized
}

func estimateSerializedTokens(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 {
		return 0
	}
	return int(math.Ceil(float64(len(encoded)) / estimatedSerializedBytesPerToken))
}
