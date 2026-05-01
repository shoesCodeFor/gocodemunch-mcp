package orchestration

import (
	"encoding/json"
	"math"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/savings"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/telemetry"
)

const estimatedSerializedBytesPerToken = 4.0

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

	record := telemetry.CallRecord{
		ToolName:          name,
		StartedAt:         startedAt.UTC(),
		FinishedAt:        time.Now().UTC(),
		RequestTokens:     estimateSerializedTokens(map[string]any{"name": name, "arguments": arguments}),
		ResponseTokens:    estimateSerializedTokens(payload),
		InputTokensSaved:  estimateSerializedTokens(map[string]any{"name": name, "arguments": arguments}),
		OutputTokensSaved: estimateSerializedTokens(payload),
		LogicalCalls:      s.telemetryLogicalCalls(name, arguments, payload),
	}

	call, ok := recoverTelemetryValue(func() telemetry.CallSnapshot {
		return collector.RecordCall(record)
	})
	if !ok {
		return telemetry.CallSnapshot{}, s.zeroSessionSnapshot(), s.zeroCumulativeSnapshot()
	}

	session := s.zeroSessionSnapshot()
	if snapshot, ok := recoverTelemetryValue(func() telemetry.SessionSnapshot {
		return collector.SessionSnapshot()
	}); ok {
		session = s.normalizeSessionSnapshot(snapshot)
	}

	cumulative := s.zeroCumulativeSnapshot()
	if snapshot, ok := recoverTelemetryValue(func() telemetry.CumulativeSnapshot {
		return collector.CumulativeSnapshot()
	}); ok {
		cumulative = s.normalizeCumulativeSnapshot(snapshot)
	}

	return call, session, cumulative
}

func (s *Service) finalizeToolPayload(
	name string,
	arguments map[string]any,
	payload map[string]any,
	startedAt time.Time,
) map[string]any {
	callSnapshot, sessionSnapshot, cumulativeSnapshot := s.recordTelemetry(name, arguments, payload, startedAt)
	payload = s.applySavingsMeta(payload, callSnapshot, cumulativeSnapshot)
	if name == "get_session_stats" && toolSucceeded(payload) {
		payload = s.applySessionStatsPayload(payload, sessionSnapshot, cumulativeSnapshot)
	}
	return s.applyMetaPolicy(payload)
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
	payload["session_rollups"] = telemetry.BuildRollupSnapshot(
		session.InputTokensSaved,
		session.OutputTokensSaved,
		session.ToolBreakdown,
		session.CostAvoidedUSD,
	)
	payload["total_tokens_saved"] = cumulative.TokensSaved
	payload["total_calls"] = cumulative.CallCount
	payload["total_sessions"] = cumulative.SessionCount
	payload["total_request_tokens"] = cumulative.RequestTokens
	payload["total_response_tokens"] = cumulative.ResponseTokens
	payload["total_input_tokens_saved"] = cumulative.InputTokensSaved
	payload["total_output_tokens_saved"] = cumulative.OutputTokensSaved
	payload["total_cost_avoided"] = cumulative.CostAvoidedUSD
	payload["total_tool_breakdown"] = cumulative.ToolBreakdown
	payload["total_rollups"] = telemetry.BuildRollupSnapshot(
		cumulative.InputTokensSaved,
		cumulative.OutputTokensSaved,
		cumulative.ToolBreakdown,
		cumulative.CostAvoidedUSD,
	)

	if payload["trend_windows"] == nil {
		payload["trend_windows"] = map[string]telemetry.TrendWindowSnapshot{}
	}
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
	return savings.ZeroCostMap(s.cfg.SavingsCompetitorPricing)
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

func (s *Service) telemetryLogicalCalls(name string, arguments map[string]any, payload map[string]any) int {
	if !toolSucceeded(payload) {
		return 1
	}

	switch name {
	case "get_file_outline", "find_importers":
		if paths, ok := optionalStringSliceArg(arguments, "file_paths"); ok && len(paths) > 1 {
			return len(paths)
		}
	case "find_references", "check_references":
		if identifiers, ok := optionalRawStringSliceArg(arguments, "identifiers"); ok && len(identifiers) > 1 {
			return len(identifiers)
		}
	case "get_symbol_source":
		if symbolIDs, ok := optionalRawStringSliceArg(arguments, "symbol_ids"); ok && len(symbolIDs) > 1 {
			return len(symbolIDs)
		}
	case "get_context_bundle":
		if symbolIDs, ok := optionalRawStringSliceArg(arguments, "symbol_ids"); ok {
			deduped := dedupePreservingOrder(symbolIDs)
			if len(deduped) > 1 {
				return len(deduped)
			}
		}
	case "index_folder":
		switch typed := arguments["changed_paths"].(type) {
		case []any:
			if len(typed) > 1 {
				return len(typed)
			}
		case []map[string]any:
			if len(typed) > 1 {
				return len(typed)
			}
		}
	}

	return 1
}

func recoverTelemetryValue[T any](fn func() T) (value T, ok bool) {
	ok = true
	defer func() {
		if recover() != nil {
			var zero T
			value = zero
			ok = false
		}
	}()

	value = fn()
	return value, true
}

func recoverTelemetryResult[T any](fn func() (T, error)) (value T, err error, ok bool) {
	ok = true
	defer func() {
		if recover() != nil {
			var zero T
			value = zero
			err = nil
			ok = false
		}
	}()

	value, err = fn()
	return value, err, true
}
