package storage

import (
	"context"
	"database/sql"
	"encoding/json"
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
		PricingProfileVersion: "pricing-v2026-05-01",
		Store:                 store,
		SnapshotInterval:      10 * time.Millisecond,
		Now:                   func() time.Time { return now },
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
	if loaded.PricingProfileVersion != "pricing-v2026-05-01" {
		t.Fatalf("expected persisted periodic snapshot pricing profile version, got %#v", loaded)
	}
}

func TestSQLiteTelemetryStoreMigratesLegacySnapshotOnlySchema(t *testing.T) {
	root := t.TempDir()
	store, err := NewSQLiteTelemetryStore(root)
	if err != nil {
		t.Fatalf("create telemetry store: %v", err)
	}

	legacyDB, err := sql.Open("sqlite", store.DBPath())
	if err != nil {
		t.Fatalf("open legacy telemetry db: %v", err)
	}

	legacyStatements := []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE cumulative_snapshots (
			snapshot_id INTEGER PRIMARY KEY AUTOINCREMENT,
			captured_at TEXT NOT NULL,
			payload TEXT NOT NULL
		)`,
	}
	for _, statement := range legacyStatements {
		if _, err := legacyDB.Exec(statement); err != nil {
			t.Fatalf("seed legacy telemetry schema: %v", err)
		}
	}

	expected := telemetry.PersistedCumulativeSnapshot{
		CapturedAt: time.Date(2026, 5, 1, 15, 0, 0, 0, time.UTC),
		Cumulative: telemetry.CumulativeSnapshot{
			FirstRecordedAt:   time.Date(2026, 5, 1, 14, 0, 0, 0, time.UTC),
			LastRecordedAt:    time.Date(2026, 5, 1, 15, 0, 0, 0, time.UTC),
			SessionCount:      2,
			CallCount:         4,
			RequestTokens:     120,
			ResponseTokens:    30,
			TotalTokens:       150,
			InputTokensSaved:  20,
			OutputTokensSaved: 10,
			TokensSaved:       30,
			CostAvoidedUSD:    map[string]float64{"codex": 0.00009},
			ToolBreakdown: map[string]telemetry.ToolSnapshot{
				"search_text": {
					CallCount:      4,
					RequestTokens:  120,
					ResponseTokens: 30,
					TotalTokens:    150,
					TokensSaved:    30,
					CostAvoidedUSD: map[string]float64{"codex": 0.00009},
				},
			},
		},
	}
	payload, err := json.Marshal(expected)
	if err != nil {
		t.Fatalf("marshal legacy telemetry payload: %v", err)
	}
	if _, err := legacyDB.Exec(
		`INSERT INTO cumulative_snapshots(captured_at, payload) VALUES(?, ?)`,
		expected.CapturedAt.Format(time.RFC3339Nano),
		string(payload),
	); err != nil {
		t.Fatalf("insert legacy telemetry snapshot: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close legacy telemetry db: %v", err)
	}

	loaded, err := store.LoadLatestSnapshot(context.Background())
	if err != nil {
		t.Fatalf("load migrated telemetry snapshot: %v", err)
	}
	if loaded.CapturedAt != expected.CapturedAt {
		t.Fatalf("expected migrated captured_at %s, got %#v", expected.CapturedAt, loaded)
	}
	if loaded.Cumulative.CallCount != expected.Cumulative.CallCount ||
		loaded.Cumulative.TokensSaved != expected.Cumulative.TokensSaved {
		t.Fatalf("unexpected migrated cumulative snapshot: %#v", loaded.Cumulative)
	}

	migratedDB, err := store.openDB()
	if err != nil {
		t.Fatalf("reopen migrated telemetry db: %v", err)
	}
	defer migratedDB.Close()

	var schemaVersion string
	if err := migratedDB.QueryRow(
		`SELECT value FROM meta WHERE key = 'schema_version'`,
	).Scan(&schemaVersion); err != nil {
		t.Fatalf("read migrated schema version: %v", err)
	}
	if schemaVersion != "2" {
		t.Fatalf("expected schema_version 2 after migration, got %q", schemaVersion)
	}

	var callEventTableCount int
	if err := migratedDB.QueryRow(
		`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'call_events'`,
	).Scan(&callEventTableCount); err != nil {
		t.Fatalf("probe migrated call_events table: %v", err)
	}
	if callEventTableCount != 1 {
		t.Fatalf("expected migrated call_events table to exist, got count %d", callEventTableCount)
	}
}

func TestSQLiteTelemetryStoreMigratesExplicitSchemaVersionOne(t *testing.T) {
	root := t.TempDir()
	store, err := NewSQLiteTelemetryStore(root)
	if err != nil {
		t.Fatalf("create telemetry store: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	legacyDB, err := sql.Open("sqlite", store.DBPath())
	if err != nil {
		t.Fatalf("open versioned legacy telemetry db: %v", err)
	}

	legacyStatements := []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO meta(key, value) VALUES('schema_version', '1')`,
		`CREATE TABLE cumulative_snapshots (
			snapshot_id INTEGER PRIMARY KEY AUTOINCREMENT,
			captured_at TEXT NOT NULL,
			payload TEXT NOT NULL
		)`,
	}
	for _, statement := range legacyStatements {
		if _, err := legacyDB.Exec(statement); err != nil {
			t.Fatalf("seed versioned legacy telemetry schema: %v", err)
		}
	}

	expected := telemetry.PersistedCumulativeSnapshot{
		CapturedAt: now.Add(-time.Minute),
		Cumulative: telemetry.CumulativeSnapshot{
			FirstRecordedAt:   now.Add(-2 * time.Hour),
			LastRecordedAt:    now.Add(-time.Minute),
			SessionCount:      3,
			CallCount:         6,
			RequestTokens:     180,
			ResponseTokens:    60,
			TotalTokens:       240,
			InputTokensSaved:  40,
			OutputTokensSaved: 20,
			TokensSaved:       60,
			CostAvoidedUSD:    map[string]float64{"codex": 0.00018},
			ToolBreakdown: map[string]telemetry.ToolSnapshot{
				"seeded_tool": {
					CallCount:      6,
					RequestTokens:  180,
					ResponseTokens: 60,
					TotalTokens:    240,
					TokensSaved:    60,
					CostAvoidedUSD: map[string]float64{"codex": 0.00018},
				},
			},
		},
	}
	payload, err := json.Marshal(expected)
	if err != nil {
		t.Fatalf("marshal versioned legacy payload: %v", err)
	}
	if _, err := legacyDB.Exec(
		`INSERT INTO cumulative_snapshots(captured_at, payload) VALUES(?, ?)`,
		expected.CapturedAt.Format(time.RFC3339Nano),
		string(payload),
	); err != nil {
		t.Fatalf("insert versioned legacy telemetry snapshot: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close versioned legacy telemetry db: %v", err)
	}

	loaded, err := store.LoadLatestSnapshot(context.Background())
	if err != nil {
		t.Fatalf("load migrated versioned telemetry snapshot: %v", err)
	}
	if loaded.CapturedAt != expected.CapturedAt {
		t.Fatalf("expected migrated captured_at %s, got %#v", expected.CapturedAt, loaded)
	}
	if loaded.Cumulative.CallCount != expected.Cumulative.CallCount ||
		loaded.Cumulative.TokensSaved != expected.Cumulative.TokensSaved {
		t.Fatalf("unexpected migrated versioned cumulative snapshot: %#v", loaded.Cumulative)
	}

	migratedDB, err := store.openDB()
	if err != nil {
		t.Fatalf("reopen versioned migrated telemetry db: %v", err)
	}
	defer migratedDB.Close()

	var schemaVersion string
	if err := migratedDB.QueryRow(
		`SELECT value FROM meta WHERE key = 'schema_version'`,
	).Scan(&schemaVersion); err != nil {
		t.Fatalf("read versioned migrated schema version: %v", err)
	}
	if schemaVersion != "2" {
		t.Fatalf("expected schema_version 2 after versioned migration, got %q", schemaVersion)
	}

	if err := store.SaveCallEvents(context.Background(), []telemetry.PersistedCallEvent{
		{
			CapturedAt:            now,
			PricingProfileVersion: "pricing-v2026-05-01",
			Call: telemetry.CallSnapshot{
				ToolName:          "search_text",
				StartedAt:         now.Add(-2 * time.Second),
				FinishedAt:        now,
				RequestTokens:     18,
				ResponseTokens:    12,
				TotalTokens:       30,
				InputTokensSaved:  9,
				OutputTokensSaved: 6,
				TokensSaved:       15,
				LogicalCalls:      2,
				CostAvoidedUSD:    map[string]float64{"codex": 0.0000495},
			},
		},
	}); err != nil {
		t.Fatalf("save call events after versioned migration: %v", err)
	}

	events, err := store.LoadCallEvents(context.Background(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("load call events after versioned migration: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one migrated call event row, got %#v", events)
	}
	if events[0].Call.ToolName != "search_text" || events[0].Call.LogicalCalls != 2 {
		t.Fatalf("unexpected migrated call event payload: %#v", events)
	}
	if events[0].PricingProfileVersion != "pricing-v2026-05-01" {
		t.Fatalf("expected pricing profile version to round-trip after migration, got %#v", events)
	}
}

func TestSQLiteTelemetryStoreCallEventRetentionPreservesSnapshots(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	store, err := NewSQLiteTelemetryStoreWithOptions(t.TempDir(), SQLiteTelemetryStoreOptions{
		CallEventRetention: 24 * time.Hour,
		Now:                func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create telemetry store: %v", err)
	}

	oldSnapshot := telemetry.PersistedCumulativeSnapshot{
		CapturedAt: now.Add(-72 * time.Hour),
		Cumulative: telemetry.CumulativeSnapshot{
			SessionCount: 1,
			CallCount:    1,
			TokensSaved:  6,
			ToolBreakdown: map[string]telemetry.ToolSnapshot{
				"search_text": {CallCount: 1, TokensSaved: 6},
			},
		},
	}
	latestSnapshot := telemetry.PersistedCumulativeSnapshot{
		CapturedAt: now.Add(-time.Hour),
		Cumulative: telemetry.CumulativeSnapshot{
			SessionCount: 2,
			CallCount:    2,
			TokensSaved:  14,
			ToolBreakdown: map[string]telemetry.ToolSnapshot{
				"search_text": {CallCount: 2, TokensSaved: 14},
			},
		},
	}
	if err := store.SaveSnapshot(context.Background(), oldSnapshot); err != nil {
		t.Fatalf("save old snapshot: %v", err)
	}
	if err := store.SaveSnapshot(context.Background(), latestSnapshot); err != nil {
		t.Fatalf("save latest snapshot: %v", err)
	}

	if err := store.SaveCallEvents(context.Background(), []telemetry.PersistedCallEvent{
		{
			CapturedAt: now.Add(-72 * time.Hour),
			Call: telemetry.CallSnapshot{
				ToolName:          "stale_tool",
				StartedAt:         now.Add(-72*time.Hour - time.Second),
				FinishedAt:        now.Add(-72 * time.Hour),
				RequestTokens:     10,
				ResponseTokens:    5,
				TotalTokens:       15,
				InputTokensSaved:  4,
				OutputTokensSaved: 2,
				TokensSaved:       6,
				CostAvoidedUSD:    map[string]float64{"codex": 0.000015},
			},
		},
		{
			CapturedAt: now.Add(-time.Hour),
			Call: telemetry.CallSnapshot{
				ToolName:          "fresh_tool",
				StartedAt:         now.Add(-time.Hour - time.Second),
				FinishedAt:        now.Add(-time.Hour),
				RequestTokens:     15,
				ResponseTokens:    8,
				TotalTokens:       23,
				InputTokensSaved:  6,
				OutputTokensSaved: 3,
				TokensSaved:       9,
				CostAvoidedUSD:    map[string]float64{"codex": 0.0000225},
			},
		},
	}); err != nil {
		t.Fatalf("save retained telemetry call events: %v", err)
	}

	loaded, err := store.LoadLatestSnapshot(context.Background())
	if err != nil {
		t.Fatalf("load latest snapshot after retention compaction: %v", err)
	}
	if loaded.CapturedAt != latestSnapshot.CapturedAt || loaded.Cumulative.CallCount != latestSnapshot.Cumulative.CallCount {
		t.Fatalf("expected latest snapshot to survive retention compaction, got %#v", loaded)
	}

	db, err := store.openDB()
	if err != nil {
		t.Fatalf("open telemetry db for retention assertions: %v", err)
	}
	defer db.Close()

	var snapshotCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM cumulative_snapshots`).Scan(&snapshotCount); err != nil {
		t.Fatalf("count cumulative snapshots: %v", err)
	}
	if snapshotCount != 2 {
		t.Fatalf("expected cumulative snapshot history to be preserved, got %d rows", snapshotCount)
	}

	var eventCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM call_events`).Scan(&eventCount); err != nil {
		t.Fatalf("count retained call events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("expected exactly one retained call event, got %d", eventCount)
	}

	var retainedTool string
	if err := db.QueryRow(`SELECT tool_name FROM call_events LIMIT 1`).Scan(&retainedTool); err != nil {
		t.Fatalf("load retained call event tool: %v", err)
	}
	if retainedTool != "fresh_tool" {
		t.Fatalf("expected stale event to be compacted away, retained tool=%q", retainedTool)
	}
}

func TestSQLiteTelemetryStoreLoadCallEventsSince(t *testing.T) {
	now := time.Date(2026, 5, 3, 9, 0, 0, 0, time.UTC)
	store, err := NewSQLiteTelemetryStoreWithOptions(t.TempDir(), SQLiteTelemetryStoreOptions{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create telemetry store: %v", err)
	}

	if err := store.SaveCallEvents(context.Background(), []telemetry.PersistedCallEvent{
		{
			CapturedAt: now.Add(-48 * time.Hour),
			Call: telemetry.CallSnapshot{
				ToolName:          "stale_tool",
				StartedAt:         now.Add(-48*time.Hour - time.Second),
				FinishedAt:        now.Add(-48 * time.Hour),
				RequestTokens:     10,
				ResponseTokens:    6,
				TotalTokens:       16,
				InputTokensSaved:  4,
				OutputTokensSaved: 2,
				TokensSaved:       6,
				CostAvoidedUSD:    map[string]float64{"codex": 0.000018},
			},
		},
		{
			CapturedAt: now.Add(-3 * time.Hour),
			Call: telemetry.CallSnapshot{
				ToolName:          "fresh_tool",
				StartedAt:         now.Add(-3*time.Hour - time.Second),
				FinishedAt:        now.Add(-3 * time.Hour),
				RequestTokens:     14,
				ResponseTokens:    9,
				TotalTokens:       23,
				InputTokensSaved:  6,
				OutputTokensSaved: 3,
				TokensSaved:       9,
				LogicalCalls:      2,
				CostAvoidedUSD:    map[string]float64{"codex": 0.000027},
			},
		},
	}); err != nil {
		t.Fatalf("save telemetry call events: %v", err)
	}

	events, err := store.LoadCallEvents(context.Background(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("load retained telemetry call events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected exactly one retained call event in 24h window, got %#v", events)
	}
	if events[0].Call.ToolName != "fresh_tool" || events[0].Call.LogicalCalls != 2 {
		t.Fatalf("unexpected filtered telemetry call event: %#v", events)
	}
	if got := events[0].Call.CostAvoidedUSD["codex"]; got != 0.000027 {
		t.Fatalf("expected decoded cost_avoided_usd for fresh_tool event, got %#v", events[0].Call.CostAvoidedUSD)
	}
}

func TestSQLiteTelemetryStorePreservesPricingProfileVersionOnSnapshotAndCallEvents(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	store, err := NewSQLiteTelemetryStoreWithOptions(t.TempDir(), SQLiteTelemetryStoreOptions{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create telemetry store: %v", err)
	}

	snapshot := telemetry.PersistedCumulativeSnapshot{
		CapturedAt:            now,
		PricingProfileVersion: "pricing-v2026-05-01",
		Cumulative: telemetry.CumulativeSnapshot{
			SessionCount: 1,
			CallCount:    2,
			TokensSaved:  10,
		},
	}
	if err := store.SaveSnapshot(context.Background(), snapshot); err != nil {
		t.Fatalf("save telemetry snapshot with pricing profile version: %v", err)
	}

	loadedSnapshot, err := store.LoadLatestSnapshot(context.Background())
	if err != nil {
		t.Fatalf("load telemetry snapshot with pricing profile version: %v", err)
	}
	if loadedSnapshot.PricingProfileVersion != snapshot.PricingProfileVersion {
		t.Fatalf("expected snapshot pricing profile version %q, got %#v", snapshot.PricingProfileVersion, loadedSnapshot)
	}

	eventsToSave := []telemetry.PersistedCallEvent{
		{
			CapturedAt:            now,
			PricingProfileVersion: "pricing-v2026-05-01",
			Call: telemetry.CallSnapshot{
				ToolName:          "search_text",
				StartedAt:         now.Add(-time.Second),
				FinishedAt:        now,
				RequestTokens:     10,
				ResponseTokens:    5,
				TotalTokens:       15,
				InputTokensSaved:  4,
				OutputTokensSaved: 2,
				TokensSaved:       6,
				CostAvoidedUSD:    map[string]float64{"codex": 0.000018},
			},
		},
	}
	if err := store.SaveCallEvents(context.Background(), eventsToSave); err != nil {
		t.Fatalf("save telemetry call event with pricing profile version: %v", err)
	}

	loadedEvents, err := store.LoadCallEvents(context.Background(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("load telemetry call events with pricing profile version: %v", err)
	}
	if len(loadedEvents) != 1 {
		t.Fatalf("expected one loaded event, got %#v", loadedEvents)
	}
	if loadedEvents[0].PricingProfileVersion != "pricing-v2026-05-01" {
		t.Fatalf("expected call event pricing profile version to round-trip, got %#v", loadedEvents)
	}
}
