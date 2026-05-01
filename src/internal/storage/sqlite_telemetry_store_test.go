package storage

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/telemetry"
)

func TestSQLiteTelemetryStoreSaveAndLoadLatestSnapshot(t *testing.T) {
	store, err := NewSQLiteTelemetryStore(t.TempDir())
	if err != nil {
		t.Fatalf("create telemetry store: %v", err)
	}

	first := telemetry.PersistedCumulativeSnapshot{
		CapturedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		Cumulative: telemetry.CumulativeSnapshot{
			SessionCount:      1,
			CallCount:         2,
			RequestTokens:     100,
			ResponseTokens:    50,
			TotalTokens:       150,
			InputTokensSaved:  30,
			OutputTokensSaved: 10,
			TokensSaved:       40,
			CostAvoidedUSD:    map[string]float64{"codex": 0.000105},
			ToolBreakdown: map[string]telemetry.ToolSnapshot{
				"search_text": {
					CallCount:      2,
					RequestTokens:  100,
					ResponseTokens: 50,
					TotalTokens:    150,
					TokensSaved:    40,
					CostAvoidedUSD: map[string]float64{"codex": 0.000105},
				},
			},
		},
	}
	second := telemetry.PersistedCumulativeSnapshot{
		CapturedAt: time.Date(2026, 5, 1, 12, 5, 0, 0, time.UTC),
		Cumulative: telemetry.CumulativeSnapshot{
			SessionCount:      2,
			CallCount:         5,
			RequestTokens:     250,
			ResponseTokens:    90,
			TotalTokens:       340,
			InputTokensSaved:  55,
			OutputTokensSaved: 15,
			TokensSaved:       70,
			CostAvoidedUSD:    map[string]float64{"codex": 0.0001725},
			ToolBreakdown: map[string]telemetry.ToolSnapshot{
				"search_text": {
					CallCount:      4,
					RequestTokens:  200,
					ResponseTokens: 70,
					TotalTokens:    270,
					TokensSaved:    60,
					CostAvoidedUSD: map[string]float64{"codex": 0.00015},
				},
				"get_context_bundle": {
					CallCount:      1,
					RequestTokens:  50,
					ResponseTokens: 20,
					TotalTokens:    70,
					TokensSaved:    10,
					CostAvoidedUSD: map[string]float64{"codex": 0.0000225},
				},
			},
		},
	}

	if err := store.SaveSnapshot(context.Background(), first); err != nil {
		t.Fatalf("save first telemetry snapshot: %v", err)
	}
	if err := store.SaveSnapshot(context.Background(), second); err != nil {
		t.Fatalf("save second telemetry snapshot: %v", err)
	}

	loaded, err := store.LoadLatestSnapshot(context.Background())
	if err != nil {
		t.Fatalf("load latest telemetry snapshot: %v", err)
	}
	if loaded.CapturedAt != second.CapturedAt {
		t.Fatalf("expected second snapshot timestamp, got %#v", loaded)
	}
	if loaded.Cumulative.CallCount != second.Cumulative.CallCount ||
		loaded.Cumulative.TokensSaved != second.Cumulative.TokensSaved {
		t.Fatalf("unexpected latest cumulative snapshot: %#v", loaded.Cumulative)
	}
	if _, err := os.Stat(store.DBPath()); err != nil {
		t.Fatalf("expected telemetry db file to exist: %v", err)
	}
}

func TestSQLiteTelemetryStoreReturnsNotFoundWhenEmpty(t *testing.T) {
	store, err := NewSQLiteTelemetryStore(t.TempDir())
	if err != nil {
		t.Fatalf("create telemetry store: %v", err)
	}

	_, err = store.LoadLatestSnapshot(context.Background())
	if !errors.Is(err, telemetry.ErrSnapshotNotFound) {
		t.Fatalf("expected ErrSnapshotNotFound, got %v", err)
	}
}

func TestSQLiteTelemetryStoreReloadsLatestSnapshotAcrossStoreInstances(t *testing.T) {
	root := t.TempDir()
	writer, err := NewSQLiteTelemetryStore(root)
	if err != nil {
		t.Fatalf("create writer telemetry store: %v", err)
	}

	expected := telemetry.PersistedCumulativeSnapshot{
		CapturedAt: time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC),
		Cumulative: telemetry.CumulativeSnapshot{
			FirstRecordedAt:   time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
			LastRecordedAt:    time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC),
			SessionCount:      3,
			CallCount:         7,
			RequestTokens:     320,
			ResponseTokens:    150,
			TotalTokens:       470,
			InputTokensSaved:  70,
			OutputTokensSaved: 25,
			TokensSaved:       95,
			CostAvoidedUSD: map[string]float64{
				"claude_code": 0.000585,
				"codex":       0.000255,
			},
			ToolBreakdown: map[string]telemetry.ToolSnapshot{
				"search_text": {
					CallCount:         5,
					RequestTokens:     220,
					ResponseTokens:    90,
					TotalTokens:       310,
					InputTokensSaved:  45,
					OutputTokensSaved: 15,
					TokensSaved:       60,
					CostAvoidedUSD: map[string]float64{
						"claude_code": 0.00036,
						"codex":       0.0001575,
					},
				},
			},
		},
	}

	if err := writer.SaveSnapshot(context.Background(), expected); err != nil {
		t.Fatalf("save telemetry snapshot: %v", err)
	}

	reader, err := NewSQLiteTelemetryStore(root)
	if err != nil {
		t.Fatalf("create reader telemetry store: %v", err)
	}

	loaded, err := reader.LoadLatestSnapshot(context.Background())
	if err != nil {
		t.Fatalf("reload latest telemetry snapshot: %v", err)
	}

	if loaded.CapturedAt != expected.CapturedAt {
		t.Fatalf("expected captured_at %s, got %#v", expected.CapturedAt, loaded)
	}
	if loaded.Cumulative.SessionCount != expected.Cumulative.SessionCount ||
		loaded.Cumulative.CallCount != expected.Cumulative.CallCount ||
		loaded.Cumulative.TokensSaved != expected.Cumulative.TokensSaved {
		t.Fatalf("unexpected reloaded cumulative snapshot: %#v", loaded.Cumulative)
	}
	if tool := loaded.Cumulative.ToolBreakdown["search_text"]; tool.CallCount != 5 || tool.TokensSaved != 60 {
		t.Fatalf("unexpected reloaded tool breakdown: %#v", loaded.Cumulative.ToolBreakdown)
	}
	if got := loaded.Cumulative.CostAvoidedUSD["claude_code"]; got != 0.000585 {
		t.Fatalf("expected claude_code avoided cost to survive reload, got %#v", loaded.Cumulative.CostAvoidedUSD)
	}
}

func TestSQLiteTelemetryStorePersistsPeriodicRuntimeSnapshots(t *testing.T) {
	store, err := NewSQLiteTelemetryStore(t.TempDir())
	if err != nil {
		t.Fatalf("create telemetry store: %v", err)
	}

	now := time.Date(2026, 5, 1, 14, 0, 0, 0, time.UTC)
	runtime, err := telemetry.NewRuntime(telemetry.RuntimeConfig{
		Pricing: map[string]telemetry.Pricing{
			"codex": {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6},
		},
		Store:            store,
		SnapshotInterval: 10 * time.Millisecond,
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Fatalf("close runtime: %v", err)
		}
	}()

	runtime.RecordCall(telemetry.CallRecord{
		ToolName:          "get_context_bundle",
		StartedAt:         now.Add(-20 * time.Millisecond),
		FinishedAt:        now,
		RequestTokens:     12,
		ResponseTokens:    8,
		InputTokensSaved:  4,
		OutputTokensSaved: 3,
	})

	var loaded telemetry.PersistedCumulativeSnapshot
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		loaded, err = store.LoadLatestSnapshot(context.Background())
		if err == nil && loaded.Cumulative.CallCount == 1 && loaded.Cumulative.TokensSaved == 7 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err != nil {
		t.Fatalf("load periodic runtime snapshot: %v", err)
	}
	if loaded.Cumulative.CallCount != 1 || loaded.Cumulative.SessionCount != 1 || loaded.Cumulative.TokensSaved != 7 {
		t.Fatalf("expected periodic runtime snapshot to be persisted, got %#v", loaded)
	}
	if loaded.CapturedAt.IsZero() {
		t.Fatalf("expected persisted periodic snapshot captured_at to be populated, got %#v", loaded)
	}
}
