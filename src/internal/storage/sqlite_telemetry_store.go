package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/telemetry"
	_ "modernc.org/sqlite"
)

const (
	telemetrySQLiteFileName             = "savings-telemetry.db"
	telemetrySQLiteLegacySchemaVersion  = 1
	telemetrySQLiteCurrentSchemaVersion = 2
	defaultTelemetryCallEventRetention  = 30 * 24 * time.Hour
)

// SQLiteTelemetryStoreOptions customizes telemetry persistence behavior.
type SQLiteTelemetryStoreOptions struct {
	CallEventRetention time.Duration
	Now                func() time.Time
}

// SQLiteTelemetryStore persists cumulative telemetry snapshots and retained
// per-call event history as SQLite rows.
type SQLiteTelemetryStore struct {
	basePath           string
	dbPath             string
	callEventRetention time.Duration
	now                func() time.Time
	mu                 sync.RWMutex
}

// NewSQLiteTelemetryStore creates a telemetry store rooted in basePath.
func NewSQLiteTelemetryStore(basePath string) (*SQLiteTelemetryStore, error) {
	return NewSQLiteTelemetryStoreWithOptions(basePath, SQLiteTelemetryStoreOptions{})
}

// NewSQLiteTelemetryStoreWithOptions creates a telemetry store with explicit
// retention and clock controls.
func NewSQLiteTelemetryStoreWithOptions(
	basePath string,
	opts SQLiteTelemetryStoreOptions,
) (*SQLiteTelemetryStore, error) {
	resolved, err := resolveBasePath(basePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(resolved, 0o755); err != nil {
		return nil, fmt.Errorf("ensure storage path: %w", err)
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	retention := opts.CallEventRetention
	if retention <= 0 {
		retention = defaultTelemetryCallEventRetention
	}

	return &SQLiteTelemetryStore{
		basePath:           resolved,
		dbPath:             filepath.Join(resolved, telemetrySQLiteFileName),
		callEventRetention: retention,
		now:                now,
	}, nil
}

// BasePath returns the resolved storage directory used by the telemetry DB.
func (s *SQLiteTelemetryStore) BasePath() string {
	return s.basePath
}

// DBPath returns the full path of the telemetry SQLite file.
func (s *SQLiteTelemetryStore) DBPath() string {
	return s.dbPath
}

// LoadLatestSnapshot returns the latest cumulative telemetry snapshot.
func (s *SQLiteTelemetryStore) LoadLatestSnapshot(
	ctx context.Context,
) (telemetry.PersistedCumulativeSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, err := os.Stat(s.dbPath); err != nil {
		if os.IsNotExist(err) {
			return telemetry.PersistedCumulativeSnapshot{}, telemetry.ErrSnapshotNotFound
		}
		return telemetry.PersistedCumulativeSnapshot{}, fmt.Errorf("stat telemetry db: %w", err)
	}

	db, err := s.openDB()
	if err != nil {
		return telemetry.PersistedCumulativeSnapshot{}, err
	}
	defer db.Close()

	snapshot, err := loadLatestSQLiteTelemetrySnapshot(ctx, db)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return telemetry.PersistedCumulativeSnapshot{}, telemetry.ErrSnapshotNotFound
		}
		return telemetry.PersistedCumulativeSnapshot{}, err
	}
	return snapshot, nil
}

// SaveSnapshot appends a cumulative telemetry snapshot row.
func (s *SQLiteTelemetryStore) SaveSnapshot(
	ctx context.Context,
	snapshot telemetry.PersistedCumulativeSnapshot,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := saveSQLiteTelemetrySnapshot(ctx, db, snapshot); err == nil {
			return nil
		} else {
			lastErr = err
			if !isSQLiteBusyErr(err) || attempt == 2 {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
		}
	}
	return lastErr
}

// SaveCallEvents appends per-call telemetry rows and prunes expired history.
func (s *SQLiteTelemetryStore) SaveCallEvents(
	ctx context.Context,
	events []telemetry.PersistedCallEvent,
) error {
	if len(events) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		err := saveSQLiteTelemetryCallEvents(ctx, db, events, s.callEventRetention, s.now)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isSQLiteBusyErr(err) || attempt == 2 {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return lastErr
}

// LoadCallEvents returns retained telemetry events captured at or after since.
func (s *SQLiteTelemetryStore) LoadCallEvents(
	ctx context.Context,
	since time.Time,
) ([]telemetry.PersistedCallEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, err := os.Stat(s.dbPath); err != nil {
		if os.IsNotExist(err) {
			return []telemetry.PersistedCallEvent{}, nil
		}
		return nil, fmt.Errorf("stat telemetry db: %w", err)
	}

	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	currentTime := time.Now().UTC()
	if s.now != nil {
		currentTime = s.now().UTC()
	}
	return loadSQLiteTelemetryCallEvents(ctx, db, since.UTC(), currentTime)
}

func (s *SQLiteTelemetryStore) openDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open telemetry sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := initSQLiteTelemetrySchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func initSQLiteTelemetrySchema(db *sql.DB) error {
	statements := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA wal_autocheckpoint = 1000",
		"PRAGMA busy_timeout = 5000",
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("init telemetry sqlite schema: %w", err)
		}
	}

	version, err := detectSQLiteTelemetrySchemaVersion(db)
	if err != nil {
		return err
	}
	if version > telemetrySQLiteCurrentSchemaVersion {
		return fmt.Errorf(
			"telemetry sqlite schema version %d is newer than supported version %d",
			version,
			telemetrySQLiteCurrentSchemaVersion,
		)
	}

	for next := version + 1; next <= telemetrySQLiteCurrentSchemaVersion; next++ {
		if err := applySQLiteTelemetryMigration(db, next); err != nil {
			return err
		}
	}

	return nil
}

func detectSQLiteTelemetrySchemaVersion(db *sql.DB) (int, error) {
	var raw string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&raw)
	switch {
	case err == nil:
		version, parseErr := strconv.Atoi(strings.TrimSpace(raw))
		if parseErr != nil {
			return 0, fmt.Errorf("parse telemetry sqlite schema version %q: %w", raw, parseErr)
		}
		return version, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("read telemetry sqlite schema version: %w", err)
	}

	hasCallEvents, err := sqliteTelemetryTableExists(db, "call_events")
	if err != nil {
		return 0, err
	}
	if hasCallEvents {
		return telemetrySQLiteCurrentSchemaVersion, nil
	}

	hasSnapshots, err := sqliteTelemetryTableExists(db, "cumulative_snapshots")
	if err != nil {
		return 0, err
	}
	if hasSnapshots {
		return telemetrySQLiteLegacySchemaVersion, nil
	}

	return 0, nil
}

func sqliteTelemetryTableExists(db *sql.DB, table string) (bool, error) {
	var exists int
	if err := db.QueryRow(
		`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		table,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("probe telemetry sqlite table %q: %w", table, err)
	}
	return exists > 0, nil
}

func applySQLiteTelemetryMigration(db *sql.DB, targetVersion int) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin telemetry sqlite schema migration %d: %w", targetVersion, err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var statements []string
	switch targetVersion {
	case 1:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS cumulative_snapshots (
				snapshot_id INTEGER PRIMARY KEY AUTOINCREMENT,
				captured_at TEXT NOT NULL,
				payload TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_cumulative_snapshots_captured_at
				ON cumulative_snapshots(captured_at DESC)`,
		}
	case 2:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS call_events (
				event_id INTEGER PRIMARY KEY AUTOINCREMENT,
				captured_at TEXT NOT NULL,
				tool_name TEXT NOT NULL,
				started_at TEXT NOT NULL,
				finished_at TEXT NOT NULL,
				logical_calls INTEGER NOT NULL,
				request_tokens INTEGER NOT NULL,
				response_tokens INTEGER NOT NULL,
				total_tokens INTEGER NOT NULL,
				input_tokens_saved INTEGER NOT NULL,
				output_tokens_saved INTEGER NOT NULL,
				tokens_saved INTEGER NOT NULL,
				cost_avoided_usd TEXT NOT NULL,
				payload TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_call_events_captured_at
				ON call_events(captured_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_call_events_tool_name_captured_at
				ON call_events(tool_name, captured_at DESC)`,
		}
	default:
		return fmt.Errorf("unsupported telemetry sqlite schema migration target %d", targetVersion)
	}

	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf(
				"apply telemetry sqlite schema migration %d: %w",
				targetVersion,
				err,
			)
		}
	}

	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO meta(key, value) VALUES('schema_version', ?)`,
		strconv.Itoa(targetVersion),
	); err != nil {
		return fmt.Errorf("write telemetry sqlite schema version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit telemetry sqlite schema migration %d: %w", targetVersion, err)
	}
	return nil
}

func loadLatestSQLiteTelemetrySnapshot(
	ctx context.Context,
	db *sql.DB,
) (telemetry.PersistedCumulativeSnapshot, error) {
	var payload string
	if err := db.QueryRowContext(
		ctx,
		`SELECT payload FROM cumulative_snapshots ORDER BY snapshot_id DESC LIMIT 1`,
	).Scan(&payload); err != nil {
		return telemetry.PersistedCumulativeSnapshot{}, err
	}

	var snapshot telemetry.PersistedCumulativeSnapshot
	if err := json.Unmarshal([]byte(payload), &snapshot); err != nil {
		return telemetry.PersistedCumulativeSnapshot{}, fmt.Errorf("decode telemetry snapshot: %w", err)
	}
	return snapshot, nil
}

func saveSQLiteTelemetrySnapshot(
	ctx context.Context,
	db *sql.DB,
	snapshot telemetry.PersistedCumulativeSnapshot,
) error {
	if snapshot.CapturedAt.IsZero() {
		snapshot.CapturedAt = time.Now().UTC()
	} else {
		snapshot.CapturedAt = snapshot.CapturedAt.UTC()
	}

	payloadBytes, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode telemetry snapshot: %w", err)
	}
	payload := string(payloadBytes)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin telemetry sqlite tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	metaValues := map[string]string{
		"last_captured_at": snapshot.CapturedAt.Format(time.RFC3339Nano),
		"session_count":    fmt.Sprintf("%d", snapshot.Cumulative.SessionCount),
		"call_count":       fmt.Sprintf("%d", snapshot.Cumulative.CallCount),
		"tokens_saved":     fmt.Sprintf("%d", snapshot.Cumulative.TokensSaved),
	}
	for key, value := range metaValues {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT OR REPLACE INTO meta(key, value) VALUES(?, ?)`,
			key,
			value,
		); err != nil {
			return fmt.Errorf("write telemetry sqlite meta: %w", err)
		}
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO cumulative_snapshots(captured_at, payload) VALUES(?, ?)`,
		snapshot.CapturedAt.Format(time.RFC3339Nano),
		payload,
	); err != nil {
		return fmt.Errorf("write telemetry sqlite snapshot: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit telemetry sqlite tx: %w", err)
	}
	return nil
}

func saveSQLiteTelemetryCallEvents(
	ctx context.Context,
	db *sql.DB,
	events []telemetry.PersistedCallEvent,
	retention time.Duration,
	now func() time.Time,
) error {
	if len(events) == 0 {
		return nil
	}

	currentTime := time.Now().UTC()
	if now != nil {
		currentTime = now().UTC()
	}

	normalized := make([]telemetry.PersistedCallEvent, 0, len(events))
	for _, event := range events {
		normalized = append(normalized, normalizePersistedCallEvent(event, currentTime))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin telemetry sqlite call-event tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	stmt, err := tx.PrepareContext(
		ctx,
		`INSERT INTO call_events(
			captured_at,
			tool_name,
			started_at,
			finished_at,
			logical_calls,
			request_tokens,
			response_tokens,
			total_tokens,
			input_tokens_saved,
			output_tokens_saved,
			tokens_saved,
			cost_avoided_usd,
			payload
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("prepare telemetry sqlite call-event insert: %w", err)
	}
	defer stmt.Close()

	for _, event := range normalized {
		payloadBytes, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("encode telemetry call event: %w", err)
		}

		if _, err := stmt.ExecContext(
			ctx,
			event.CapturedAt.Format(time.RFC3339Nano),
			event.Call.ToolName,
			event.Call.StartedAt.Format(time.RFC3339Nano),
			event.Call.FinishedAt.Format(time.RFC3339Nano),
			event.Call.LogicalCalls,
			event.Call.RequestTokens,
			event.Call.ResponseTokens,
			event.Call.TotalTokens,
			event.Call.InputTokensSaved,
			event.Call.OutputTokensSaved,
			event.Call.TokensSaved,
			mustJSONString(event.Call.CostAvoidedUSD),
			string(payloadBytes),
		); err != nil {
			return fmt.Errorf("write telemetry sqlite call event: %w", err)
		}
	}

	if err := compactSQLiteTelemetryCallEvents(ctx, tx, retention, currentTime); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit telemetry sqlite call-event tx: %w", err)
	}
	return nil
}

func loadSQLiteTelemetryCallEvents(
	ctx context.Context,
	db *sql.DB,
	since time.Time,
	fallback time.Time,
) ([]telemetry.PersistedCallEvent, error) {
	query := `SELECT
		captured_at,
		tool_name,
		started_at,
		finished_at,
		logical_calls,
		request_tokens,
		response_tokens,
		total_tokens,
		input_tokens_saved,
		output_tokens_saved,
		tokens_saved,
		cost_avoided_usd
	FROM call_events`
	args := []any{}
	if !since.IsZero() {
		query += ` WHERE captured_at >= ?`
		args = append(args, since.Format(time.RFC3339Nano))
	}
	query += ` ORDER BY captured_at ASC, event_id ASC`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load telemetry sqlite call events: %w", err)
	}
	defer rows.Close()

	events := make([]telemetry.PersistedCallEvent, 0, 32)
	for rows.Next() {
		var (
			capturedAtRaw      string
			toolName           string
			startedAtRaw       string
			finishedAtRaw      string
			logicalCalls       int
			requestTokens      int
			responseTokens     int
			totalTokens        int
			inputTokensSaved   int
			outputTokensSaved  int
			tokensSaved        int
			costAvoidedUSDJSON string
		)
		if err := rows.Scan(
			&capturedAtRaw,
			&toolName,
			&startedAtRaw,
			&finishedAtRaw,
			&logicalCalls,
			&requestTokens,
			&responseTokens,
			&totalTokens,
			&inputTokensSaved,
			&outputTokensSaved,
			&tokensSaved,
			&costAvoidedUSDJSON,
		); err != nil {
			return nil, fmt.Errorf("scan telemetry sqlite call event: %w", err)
		}

		capturedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(capturedAtRaw))
		if err != nil {
			return nil, fmt.Errorf("parse telemetry call event captured_at %q: %w", capturedAtRaw, err)
		}
		startedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(startedAtRaw))
		if err != nil {
			return nil, fmt.Errorf("parse telemetry call event started_at %q: %w", startedAtRaw, err)
		}
		finishedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(finishedAtRaw))
		if err != nil {
			return nil, fmt.Errorf("parse telemetry call event finished_at %q: %w", finishedAtRaw, err)
		}

		costAvoidedUSD := map[string]float64{}
		if strings.TrimSpace(costAvoidedUSDJSON) != "" {
			if err := json.Unmarshal([]byte(costAvoidedUSDJSON), &costAvoidedUSD); err != nil {
				return nil, fmt.Errorf("decode telemetry call event cost_avoided_usd: %w", err)
			}
		}

		events = append(events, normalizePersistedCallEvent(telemetry.PersistedCallEvent{
			CapturedAt: capturedAt,
			Call: telemetry.CallSnapshot{
				ToolName:          toolName,
				StartedAt:         startedAt,
				FinishedAt:        finishedAt,
				RequestTokens:     requestTokens,
				ResponseTokens:    responseTokens,
				TotalTokens:       totalTokens,
				InputTokensSaved:  inputTokensSaved,
				OutputTokensSaved: outputTokensSaved,
				TokensSaved:       tokensSaved,
				LogicalCalls:      logicalCalls,
				CostAvoidedUSD:    costAvoidedUSD,
			},
		}, fallback))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate telemetry sqlite call events: %w", err)
	}

	return events, nil
}

func compactSQLiteTelemetryCallEvents(
	ctx context.Context,
	tx *sql.Tx,
	retention time.Duration,
	now time.Time,
) error {
	if retention <= 0 {
		return nil
	}

	cutoff := now.UTC().Add(-retention)
	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM call_events WHERE captured_at < ?`,
		cutoff.Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("compact telemetry sqlite call events: %w", err)
	}

	metaValues := map[string]string{
		"call_event_retention_seconds": strconv.FormatInt(int64(retention/time.Second), 10),
		"last_call_event_compacted_at": now.UTC().Format(time.RFC3339Nano),
	}
	for key, value := range metaValues {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT OR REPLACE INTO meta(key, value) VALUES(?, ?)`,
			key,
			value,
		); err != nil {
			return fmt.Errorf("write telemetry sqlite call-event meta: %w", err)
		}
	}

	return nil
}

func normalizePersistedCallEvent(
	event telemetry.PersistedCallEvent,
	fallback time.Time,
) telemetry.PersistedCallEvent {
	call := event.Call

	event.CapturedAt = event.CapturedAt.UTC()
	if event.CapturedAt.IsZero() {
		if !call.FinishedAt.IsZero() {
			event.CapturedAt = call.FinishedAt.UTC()
		} else {
			event.CapturedAt = fallback.UTC()
		}
	}

	call.ToolName = strings.TrimSpace(call.ToolName)
	if call.ToolName == "" {
		call.ToolName = "unknown"
	}

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

	call.RequestTokens = maxTelemetryInt(call.RequestTokens)
	call.ResponseTokens = maxTelemetryInt(call.ResponseTokens)
	call.TotalTokens = maxTelemetryInt(call.TotalTokens)
	totalTokens := call.RequestTokens + call.ResponseTokens
	if call.TotalTokens < totalTokens {
		call.TotalTokens = totalTokens
	}
	call.InputTokensSaved = maxTelemetryInt(call.InputTokensSaved)
	call.OutputTokensSaved = maxTelemetryInt(call.OutputTokensSaved)
	call.TokensSaved = maxTelemetryInt(call.TokensSaved)
	totalSaved := call.InputTokensSaved + call.OutputTokensSaved
	if call.TokensSaved < totalSaved {
		call.TokensSaved = totalSaved
	}
	if call.LogicalCalls <= 0 {
		call.LogicalCalls = 1
	}
	if call.CostAvoidedUSD == nil {
		call.CostAvoidedUSD = map[string]float64{}
	}
	call.DurationMS = call.FinishedAt.Sub(call.StartedAt).Seconds() * 1000

	event.Call = call
	return event
}

func maxTelemetryInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
