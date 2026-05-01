package orchestration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/savings"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
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

func TestCallToolRecordsValidationFailureAndInternalErrorTelemetry(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC)
	tracker := telemetry.NewTracker(testTelemetryPricing(), func() time.Time { return now })
	service := New(testTelemetryConfig(), Dependencies{Telemetry: tracker})

	service.tools["validation_probe"] = Tool{
		Name:        "validation_probe",
		Description: "test-only validation probe",
		InputSchema: objectSchema(map[string]any{
			"query": stringProp("Query text"),
		}, "query"),
		Handler: func(_ context.Context, _ map[string]any) (map[string]any, error) {
			t.Fatal("validation probe handler should not run when input validation fails")
			return nil, nil
		},
	}
	service.tools["error_probe"] = Tool{
		Name:        "error_probe",
		Description: "test-only failing probe",
		InputSchema: objectSchema(map[string]any{}),
		Handler: func(_ context.Context, _ map[string]any) (map[string]any, error) {
			return nil, errors.New("boom")
		},
	}

	validationPayload := service.CallTool(context.Background(), "validation_probe", map[string]any{
		"query": 42,
	})
	if got, _ := validationPayload["error"].(string); got == "" {
		t.Fatalf("expected validation error payload, got %#v", validationPayload)
	}
	validationMeta := mustMetaMap(t, validationPayload)
	if got := intFieldFromAny(t, validationMeta["tokens_saved"]); got <= 0 {
		t.Fatalf("expected validation failure telemetry to include positive tokens_saved, got %#v", validationMeta)
	}

	errorPayload := service.CallTool(context.Background(), "error_probe", map[string]any{})
	if got, _ := errorPayload["error"].(string); got != "Internal error processing error_probe" {
		t.Fatalf("unexpected internal error payload: %#v", errorPayload)
	}
	errorMeta := mustMetaMap(t, errorPayload)
	if got := intFieldFromAny(t, errorMeta["total_tokens_saved"]); got <= intFieldFromAny(t, validationMeta["total_tokens_saved"]) {
		t.Fatalf("expected cumulative telemetry to advance after internal error, validation=%#v internal=%#v", validationMeta, errorMeta)
	}

	session := tracker.SessionSnapshot()
	if session.CallCount != 2 {
		t.Fatalf("expected validation failure and internal error to both be recorded, got %#v", session)
	}
	if tool := session.ToolBreakdown["validation_probe"]; tool.CallCount != 1 {
		t.Fatalf("expected validation_probe call count to be recorded, got %#v", session.ToolBreakdown)
	}
	if tool := session.ToolBreakdown["error_probe"]; tool.CallCount != 1 {
		t.Fatalf("expected error_probe call count to be recorded, got %#v", session.ToolBreakdown)
	}
}

func TestCallToolRecordsCanceledTelemetryWithoutRunningHandler(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 45, 0, 0, time.UTC)
	tracker := telemetry.NewTracker(testTelemetryPricing(), func() time.Time { return now })
	service := New(config.Config{
		ServerName:       "gocodemunch-mcp",
		ServerVersion:    "test",
		RequestTimeoutMS: 50,
		Disabled:         map[string]struct{}{},
		SavingsCompetitorPricing: map[string]config.SavingsCompetitorPricing{
			"claude_code": {InputUSDPerMTok: 3.0, OutputUSDPerMTok: 15.0},
			"codex":       {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6.0},
			"amp":         {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6.0},
		},
	}, Dependencies{Telemetry: tracker})

	handlerCalled := false
	service.tools["cancel_probe"] = Tool{
		Name:        "cancel_probe",
		Description: "test-only cancel probe",
		InputSchema: objectSchema(map[string]any{}),
		Handler: func(_ context.Context, _ map[string]any) (map[string]any, error) {
			handlerCalled = true
			return map[string]any{"ok": true}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	payload := service.CallTool(ctx, "cancel_probe", map[string]any{})

	if handlerCalled {
		t.Fatal("expected canceled call to skip handler execution")
	}
	if got, _ := payload["error"].(string); got != "Internal error processing cancel_probe" {
		t.Fatalf("unexpected canceled payload: %#v", payload)
	}
	meta := mustMetaMap(t, payload)
	if got := intFieldFromAny(t, meta["tokens_saved"]); got <= 0 {
		t.Fatalf("expected canceled call to record telemetry, got %#v", meta)
	}
	if session := tracker.SessionSnapshot(); session.CallCount != 1 {
		t.Fatalf("expected one recorded canceled call, got %#v", session)
	}
}

func TestCallToolCountsSuccessfulBatchItemsInTelemetry(t *testing.T) {
	now := time.Date(2026, 5, 1, 13, 15, 0, 0, time.UTC)
	tracker := telemetry.NewTracker(testTelemetryPricing(), func() time.Time { return now })
	store := mustIndexStore(t)
	repoID := seedTelemetryBatchRepo(t, store)
	service := New(testTelemetryConfig(), Dependencies{
		IndexStore: store,
		Telemetry:  tracker,
	})

	payload := service.CallTool(context.Background(), "get_file_outline", map[string]any{
		"repo":       repoID,
		"file_paths": []string{"alpha.py", "beta.py"},
	})
	results, ok := payload["results"].([]map[string]any)
	if !ok {
		rawResults, ok := payload["results"].([]any)
		if !ok {
			t.Fatalf("expected batch outline results, got %#v", payload)
		}
		results = make([]map[string]any, 0, len(rawResults))
		for _, item := range rawResults {
			row, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("expected outline row map, got %#v", item)
			}
			results = append(results, row)
		}
	}
	if len(results) != 2 {
		t.Fatalf("expected two outline results, got %#v", payload)
	}

	session := tracker.SessionSnapshot()
	if session.CallCount != 2 {
		t.Fatalf("expected batch outline telemetry to count two logical calls, got %#v", session)
	}
	if tool := session.ToolBreakdown["get_file_outline"]; tool.CallCount != 2 {
		t.Fatalf("expected get_file_outline tool breakdown to count two logical calls, got %#v", session.ToolBreakdown)
	}
}

func TestCallToolIgnoresTelemetryCollectorPanics(t *testing.T) {
	service := New(testTelemetryConfig(), Dependencies{Telemetry: panicTelemetryCollector{}})
	service.tools["panic_safe_probe"] = Tool{
		Name:        "panic_safe_probe",
		Description: "test-only panic-safe probe",
		InputSchema: objectSchema(map[string]any{}),
		Handler: func(_ context.Context, _ map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	}

	payload := service.CallTool(context.Background(), "panic_safe_probe", map[string]any{})
	if ok, _ := payload["ok"].(bool); !ok {
		t.Fatalf("expected tool response to survive telemetry collector panic, got %#v", payload)
	}
	meta := mustMetaMap(t, payload)
	if got := intFieldFromAny(t, meta["tokens_saved"]); got != 0 {
		t.Fatalf("expected panicing telemetry collector to fall back to zero meta, got %#v", meta)
	}
	if got := intFieldFromAny(t, meta["total_tokens_saved"]); got != 0 {
		t.Fatalf("expected panicing telemetry collector to preserve tool response with zero cumulative meta, got %#v", meta)
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
	for _, competitor := range savings.DefaultCompetitors() {
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
	sessionRollups, ok := stats["session_rollups"].(telemetry.RollupSnapshot)
	if !ok {
		t.Fatalf("expected typed session_rollups on get_session_stats, got %#v", stats["session_rollups"])
	}
	if _, ok := sessionRollups.ToolBreakdown["telemetry_probe"]; !ok {
		t.Fatalf("expected telemetry_probe in session_rollups.tool_breakdown: %#v", sessionRollups)
	}
	if _, ok := sessionRollups.CompetitorBreakdown["codex"]; !ok {
		t.Fatalf("expected codex in session_rollups.competitor_breakdown: %#v", sessionRollups)
	}
	totalRollups, ok := stats["total_rollups"].(telemetry.RollupSnapshot)
	if !ok {
		t.Fatalf("expected typed total_rollups on get_session_stats, got %#v", stats["total_rollups"])
	}
	if _, ok := totalRollups.ToolBreakdown["seeded_tool"]; !ok {
		t.Fatalf("expected seeded_tool in total_rollups.tool_breakdown: %#v", totalRollups)
	}
	if _, ok := totalRollups.CompetitorBreakdown["claude_code"]; !ok {
		t.Fatalf("expected claude_code in total_rollups.competitor_breakdown: %#v", totalRollups)
	}
	if trends, ok := stats["trend_windows"].(map[string]telemetry.TrendWindowSnapshot); !ok || len(trends) != 0 {
		t.Fatalf("expected empty typed trend_windows without a request, got %#v", stats["trend_windows"])
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
	for _, competitor := range savings.DefaultCompetitors() {
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
	if sessionRollups, ok := stats["session_rollups"].(telemetry.RollupSnapshot); !ok || len(sessionRollups.ToolBreakdown) != 0 {
		t.Fatalf("expected empty typed session_rollups without telemetry, got %#v", stats["session_rollups"])
	}
	if totalRollups, ok := stats["total_rollups"].(telemetry.RollupSnapshot); !ok || len(totalRollups.ToolBreakdown) != 0 {
		t.Fatalf("expected empty typed total_rollups without telemetry, got %#v", stats["total_rollups"])
	}
	if trends, ok := stats["trend_windows"].(map[string]telemetry.TrendWindowSnapshot); !ok || len(trends) != 0 {
		t.Fatalf("expected empty typed trend_windows without telemetry, got %#v", stats["trend_windows"])
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

func TestGetSessionStatsReturnsRequestedTrendWindowsFromPersistedTelemetry(t *testing.T) {
	now := time.Date(2026, 5, 1, 20, 0, 0, 0, time.UTC)
	store, err := storage.NewSQLiteTelemetryStoreWithOptions(t.TempDir(), storage.SQLiteTelemetryStoreOptions{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create telemetry store: %v", err)
	}

	if err := store.SaveCallEvents(context.Background(), []telemetry.PersistedCallEvent{
		{
			CapturedAt: now.Add(-2 * time.Hour),
			Call: telemetry.CallSnapshot{
				ToolName:          "search_text",
				StartedAt:         now.Add(-2*time.Hour - time.Second),
				FinishedAt:        now.Add(-2 * time.Hour),
				RequestTokens:     24,
				ResponseTokens:    16,
				TotalTokens:       40,
				InputTokensSaved:  9,
				OutputTokensSaved: 6,
				TokensSaved:       15,
				LogicalCalls:      2,
				CostAvoidedUSD: map[string]float64{
					"claude_code": 0.000225,
					"codex":       0.0000855,
					"amp":         0.0000855,
				},
			},
		},
		{
			CapturedAt: now.Add(-48 * time.Hour),
			Call: telemetry.CallSnapshot{
				ToolName:          "get_context_bundle",
				StartedAt:         now.Add(-48*time.Hour - time.Second),
				FinishedAt:        now.Add(-48 * time.Hour),
				RequestTokens:     12,
				ResponseTokens:    8,
				TotalTokens:       20,
				InputTokensSaved:  4,
				OutputTokensSaved: 3,
				TokensSaved:       7,
				LogicalCalls:      1,
				CostAvoidedUSD: map[string]float64{
					"claude_code": 0.000087,
					"codex":       0.00003,
					"amp":         0.00003,
				},
			},
		},
	}); err != nil {
		t.Fatalf("seed persisted telemetry events: %v", err)
	}

	runtime, err := telemetry.NewRuntime(telemetry.RuntimeConfig{
		Pricing: testTelemetryPricing(),
		Store:   store,
		Now:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create telemetry runtime: %v", err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Fatalf("close telemetry runtime: %v", err)
		}
	}()

	service := New(testTelemetryConfig(), Dependencies{Telemetry: runtime})
	service.tools["telemetry_probe"] = Tool{
		Name:        "telemetry_probe",
		Description: "test-only telemetry probe",
		InputSchema: objectSchema(map[string]any{}),
		Handler: func(_ context.Context, _ map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	}

	_ = service.CallTool(context.Background(), "telemetry_probe", map[string]any{})
	stats := service.CallTool(context.Background(), "get_session_stats", map[string]any{
		"trend_windows": []any{"last_24h", "last_7d"},
	})

	trends, ok := stats["trend_windows"].(map[string]telemetry.TrendWindowSnapshot)
	if !ok {
		t.Fatalf("expected typed trend_windows payload, got %#v", stats["trend_windows"])
	}
	if len(trends) != 2 {
		t.Fatalf("expected two requested trend windows, got %#v", trends)
	}

	last24h := trends["last_24h"]
	if last24h.CallCount != 2 || last24h.TokensSaved != 15 {
		t.Fatalf("unexpected last_24h trend snapshot: %#v", last24h)
	}
	if tool := last24h.ToolBreakdown["search_text"]; tool.CallCount != 2 || tool.TokensSaved != 15 {
		t.Fatalf("expected persisted search_text rollup in last_24h trend snapshot, got %#v", last24h.ToolBreakdown)
	}
	if competitor := last24h.CompetitorBreakdown["claude_code"]; competitor.CostAvoidedUSD != 0.000225 {
		t.Fatalf("unexpected last_24h competitor breakdown: %#v", last24h.CompetitorBreakdown)
	}

	last7d := trends["last_7d"]
	if last7d.CallCount != 3 || last7d.TokensSaved != 22 {
		t.Fatalf("unexpected last_7d trend snapshot: %#v", last7d)
	}
	if tool := last7d.ToolBreakdown["get_context_bundle"]; tool.CallCount != 1 || tool.TokensSaved != 7 {
		t.Fatalf("expected retained get_context_bundle event in last_7d trend snapshot, got %#v", last7d.ToolBreakdown)
	}
	if len(last7d.Points) != 8 {
		t.Fatalf("expected calendar-aligned daily buckets for last_7d trend snapshot, got %#v", last7d.Points)
	}

	sessionRollups, ok := stats["session_rollups"].(telemetry.RollupSnapshot)
	if !ok {
		t.Fatalf("expected typed session_rollups in trend response, got %#v", stats["session_rollups"])
	}
	if _, ok := sessionRollups.ToolBreakdown["get_session_stats"]; !ok {
		t.Fatalf("expected live session rollups to include get_session_stats call, got %#v", sessionRollups)
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

func seedTelemetryBatchRepo(t *testing.T, store *storage.SQLiteIndexStore) string {
	t.Helper()

	sourceRoot := t.TempDir()
	files := map[string]string{
		"alpha.py": "def alpha():\n    return 'alpha'\n",
		"beta.py":  "def beta():\n    return 'beta'\n",
	}
	fileMTimes := make(map[string]int64, len(files))
	fileHashes := make(map[string]string, len(files))
	for relPath, content := range files {
		absolutePath := filepath.Join(sourceRoot, relPath)
		if err := os.WriteFile(absolutePath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
		fileMTimes[relPath] = time.Now().Unix()
		fileHashes[relPath] = relPath + "-hash"
	}

	repoID := "local/telemetry-batch"
	index := storage.RepoIndex{
		Repo:         repoID,
		IndexedAt:    time.Now().UTC().Format(time.RFC3339),
		SourceRoot:   sourceRoot,
		DisplayName:  "telemetry-batch",
		Languages:    map[string]int{"python": len(files)},
		IndexVersion: repoIndexVersion,
		Files:        fileHashes,
		FileMTimes:   fileMTimes,
		Symbols:      map[string]any{},
	}
	if err := store.Save(context.Background(), repoID, index); err != nil {
		t.Fatalf("seed telemetry batch repo: %v", err)
	}
	return repoID
}

type panicTelemetryCollector struct{}

func (panicTelemetryCollector) RecordCall(telemetry.CallRecord) telemetry.CallSnapshot {
	panic("record call panic")
}

func (panicTelemetryCollector) SessionSnapshot() telemetry.SessionSnapshot {
	panic("session snapshot panic")
}

func (panicTelemetryCollector) CumulativeSnapshot() telemetry.CumulativeSnapshot {
	panic("cumulative snapshot panic")
}
