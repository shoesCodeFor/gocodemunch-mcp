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
