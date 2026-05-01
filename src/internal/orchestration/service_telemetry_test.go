package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/telemetry"
)

func TestCallToolRecordsTelemetryAndOverridesSavingsMeta(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	tracker := telemetry.NewTracker(testTelemetryPricing(), func() time.Time { return now })
	service := New(testTelemetryConfig(), Dependencies{Telemetry: tracker})

	service.tools["telemetry_probe"] = Tool{
		Name:        "telemetry_probe",
		Description: "test-only telemetry probe",
		InputSchema: objectSchema(map[string]any{
			"query": stringProp("Query text"),
		}, "query"),
		Handler: func(_ context.Context, arguments map[string]any) (map[string]any, error) {
			return map[string]any{
				"echo": arguments["query"],
				"_meta": map[string]any{
					"hint":               "preserved",
					"tokens_saved":       0,
					"total_tokens_saved": 0,
				},
			}, nil
		},
	}

	payload := service.CallTool(context.Background(), "telemetry_probe", map[string]any{
		"query": "hello world",
	})

	meta, ok := payload["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("expected telemetry_probe payload to include _meta: %#v", payload)
	}
	if got, _ := meta["hint"].(string); got != "preserved" {
		t.Fatalf("expected existing _meta hint to be preserved, got %#v", meta)
	}

	perCall := intFieldFromAny(t, meta["tokens_saved"])
	if perCall <= 0 {
		t.Fatalf("expected positive per-call tokens_saved, got %#v", meta)
	}

	cumulative := tracker.CumulativeSnapshot()
	if got := intFieldFromAny(t, meta["total_tokens_saved"]); got != cumulative.TokensSaved {
		t.Fatalf("expected total_tokens_saved=%d, got %#v", cumulative.TokensSaved, meta)
	}
	if cumulative.CallCount != 1 {
		t.Fatalf("expected one recorded telemetry call, got %#v", cumulative)
	}
	if tool := cumulative.ToolBreakdown["telemetry_probe"]; tool.CallCount != 1 || tool.TokensSaved != perCall {
		t.Fatalf("unexpected telemetry tool breakdown: %#v", cumulative.ToolBreakdown)
	}
}

func TestGetSessionStatsReturnsLiveSessionAndCumulativeTelemetry(t *testing.T) {
	now := time.Date(2026, 5, 1, 13, 0, 0, 0, time.UTC)
	tracker := telemetry.NewTracker(testTelemetryPricing(), func() time.Time { return now })
	tracker.RestoreCumulative(telemetry.CumulativeSnapshot{
		FirstRecordedAt:   now.Add(-48 * time.Hour),
		LastRecordedAt:    now.Add(-24 * time.Hour),
		SessionCount:      2,
		CallCount:         5,
		RequestTokens:     150,
		ResponseTokens:    60,
		TotalTokens:       210,
		InputTokensSaved:  150,
		OutputTokensSaved: 60,
		TokensSaved:       210,
		CostAvoidedUSD: map[string]float64{
			"claude_code": 0.00135,
			"codex":       0.000585,
			"amp":         0.000585,
		},
		ToolBreakdown: map[string]telemetry.ToolSnapshot{
			"seeded_tool": {
				CallCount:      5,
				RequestTokens:  150,
				ResponseTokens: 60,
				TotalTokens:    210,
				TokensSaved:    210,
				CostAvoidedUSD: map[string]float64{
					"claude_code": 0.00135,
					"codex":       0.000585,
					"amp":         0.000585,
				},
			},
		},
	})

	service := New(testTelemetryConfig(), Dependencies{Telemetry: tracker})
	service.tools["telemetry_probe"] = Tool{
		Name:        "telemetry_probe",
		Description: "test-only telemetry probe",
		InputSchema: objectSchema(map[string]any{
			"query": stringProp("Query text"),
		}, "query"),
		Handler: func(_ context.Context, arguments map[string]any) (map[string]any, error) {
			return map[string]any{"echo": arguments["query"]}, nil
		},
	}

	_ = service.CallTool(context.Background(), "telemetry_probe", map[string]any{
		"query": "search text",
	})
	stats := service.CallTool(context.Background(), "get_session_stats", map[string]any{})

	session := tracker.SessionSnapshot()
	cumulative := tracker.CumulativeSnapshot()

	if got := intFieldFromAny(t, stats["session_calls"]); got != session.CallCount {
		t.Fatalf("expected session_calls=%d, got %#v", session.CallCount, stats)
	}
	if got := intFieldFromAny(t, stats["session_tokens_saved"]); got != session.TokensSaved {
		t.Fatalf("expected session_tokens_saved=%d, got %#v", session.TokensSaved, stats)
	}
	if got := intFieldFromAny(t, stats["total_tokens_saved"]); got != cumulative.TokensSaved {
		t.Fatalf("expected total_tokens_saved=%d, got %#v", cumulative.TokensSaved, stats)
	}
	if got := intFieldFromAny(t, stats["total_calls"]); got != cumulative.CallCount {
		t.Fatalf("expected total_calls=%d, got %#v", cumulative.CallCount, stats)
	}
	if got := intFieldFromAny(t, stats["total_sessions"]); got != cumulative.SessionCount {
		t.Fatalf("expected total_sessions=%d, got %#v", cumulative.SessionCount, stats)
	}

	sessionCost := floatMapFromAny(t, stats["session_cost_avoided"])
	totalCost := floatMapFromAny(t, stats["total_cost_avoided"])
	for _, competitor := range defaultSavingsCompetitors {
		if _, ok := sessionCost[competitor]; !ok {
			t.Fatalf("expected %q in session_cost_avoided: %#v", competitor, sessionCost)
		}
		if _, ok := totalCost[competitor]; !ok {
			t.Fatalf("expected %q in total_cost_avoided: %#v", competitor, totalCost)
		}
	}

	toolBreakdown, ok := stats["tool_breakdown"].(map[string]telemetry.ToolSnapshot)
	if !ok {
		t.Fatalf("expected typed tool_breakdown map, got %#v", stats["tool_breakdown"])
	}
	if _, ok := toolBreakdown["telemetry_probe"]; !ok {
		t.Fatalf("expected telemetry_probe in session tool breakdown: %#v", toolBreakdown)
	}
	if _, ok := toolBreakdown["get_session_stats"]; !ok {
		t.Fatalf("expected get_session_stats in session tool breakdown after stats call: %#v", toolBreakdown)
	}

	meta, ok := stats["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("expected _meta envelope on get_session_stats: %#v", stats)
	}
	if got := intFieldFromAny(t, meta["total_tokens_saved"]); got != cumulative.TokensSaved {
		t.Fatalf("expected _meta.total_tokens_saved=%d, got %#v", cumulative.TokensSaved, meta)
	}
	if got := intFieldFromAny(t, meta["tokens_saved"]); got <= 0 {
		t.Fatalf("expected positive _meta.tokens_saved for get_session_stats, got %#v", meta)
	}
}

func TestGetSessionStatsReturnsStableZeroContractWithoutTelemetry(t *testing.T) {
	service := New(testTelemetryConfig(), Dependencies{})

	stats := service.CallTool(context.Background(), "get_session_stats", map[string]any{})

	if got := intFieldFromAny(t, stats["session_calls"]); got != 0 {
		t.Fatalf("expected zero session_calls without telemetry, got %#v", stats)
	}
	if got := intFieldFromAny(t, stats["session_tokens_saved"]); got != 0 {
		t.Fatalf("expected zero session_tokens_saved without telemetry, got %#v", stats)
	}
	if got := intFieldFromAny(t, stats["total_calls"]); got != 0 {
		t.Fatalf("expected zero total_calls without telemetry, got %#v", stats)
	}
	if got := intFieldFromAny(t, stats["total_sessions"]); got != 0 {
		t.Fatalf("expected zero total_sessions without telemetry, got %#v", stats)
	}
	if got := intFieldFromAny(t, stats["total_tokens_saved"]); got != 0 {
		t.Fatalf("expected zero total_tokens_saved without telemetry, got %#v", stats)
	}

	sessionCost := floatMapFromAny(t, stats["session_cost_avoided"])
	totalCost := floatMapFromAny(t, stats["total_cost_avoided"])
	for _, competitor := range defaultSavingsCompetitors {
		if sessionCost[competitor] != 0 {
			t.Fatalf("expected zero session cost for %q without telemetry, got %#v", competitor, sessionCost)
		}
		if totalCost[competitor] != 0 {
			t.Fatalf("expected zero total cost for %q without telemetry, got %#v", competitor, totalCost)
		}
	}

	if toolBreakdown, ok := stats["tool_breakdown"].(map[string]telemetry.ToolSnapshot); !ok || len(toolBreakdown) != 0 {
		t.Fatalf("expected empty typed session tool_breakdown without telemetry, got %#v", stats["tool_breakdown"])
	}
	if totalBreakdown, ok := stats["total_tool_breakdown"].(map[string]telemetry.ToolSnapshot); !ok || len(totalBreakdown) != 0 {
		t.Fatalf("expected empty typed total_tool_breakdown without telemetry, got %#v", stats["total_tool_breakdown"])
	}

	meta, ok := stats["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("expected _meta envelope without telemetry, got %#v", stats)
	}
	if got := intFieldFromAny(t, meta["tokens_saved"]); got != 0 {
		t.Fatalf("expected zero _meta.tokens_saved without telemetry, got %#v", meta)
	}
	if got := intFieldFromAny(t, meta["total_tokens_saved"]); got != 0 {
		t.Fatalf("expected zero _meta.total_tokens_saved without telemetry, got %#v", meta)
	}
}

func TestEstimateSerializedTokensHandlesMarshalErrorsAndRoundsUp(t *testing.T) {
	if got := estimateSerializedTokens("abcd"); got != 2 {
		t.Fatalf("expected quoted string to round up to 2 tokens, got %d", got)
	}
	if got := estimateSerializedTokens(make(chan int)); got != 0 {
		t.Fatalf("expected marshal error to return zero tokens, got %d", got)
	}
}

func testTelemetryConfig() config.Config {
	return config.Config{
		ServerName:    "gocodemunch-mcp",
		ServerVersion: "test",
		Disabled:      map[string]struct{}{},
		SavingsCompetitorPricing: map[string]config.SavingsCompetitorPricing{
			"claude_code": {InputUSDPerMTok: 3.0, OutputUSDPerMTok: 15.0},
			"codex":       {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6.0},
			"amp":         {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6.0},
		},
	}
}

func testTelemetryPricing() map[string]telemetry.Pricing {
	return map[string]telemetry.Pricing{
		"claude_code": {InputUSDPerMTok: 3.0, OutputUSDPerMTok: 15.0},
		"codex":       {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6.0},
		"amp":         {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6.0},
	}
}

func intFieldFromAny(t *testing.T, value any) int {
	t.Helper()

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
		t.Fatalf("expected numeric value, got %#v", value)
		return 0
	}
}

func floatMapFromAny(t *testing.T, value any) map[string]float64 {
	t.Helper()

	switch typed := value.(type) {
	case map[string]float64:
		return typed
	case map[string]any:
		converted := make(map[string]float64, len(typed))
		for key, raw := range typed {
			switch numeric := raw.(type) {
			case float64:
				converted[key] = numeric
			case int:
				converted[key] = float64(numeric)
			default:
				t.Fatalf("expected numeric cost for %q, got %#v", key, raw)
			}
		}
		return converted
	default:
		t.Fatalf("expected map value, got %#v", value)
		return nil
	}
}
