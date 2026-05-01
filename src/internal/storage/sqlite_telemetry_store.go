package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/telemetry"
	_ "modernc.org/sqlite"
)

const telemetrySQLiteFileName = "savings-telemetry.db"

// SQLiteTelemetryStore persists cumulative telemetry snapshots as SQLite rows.
type SQLiteTelemetryStore struct {
	basePath string
	dbPath   string
	mu       sync.RWMutex
}

// NewSQLiteTelemetryStore creates a telemetry snapshot store rooted in basePath.
func NewSQLiteTelemetryStore(basePath string) (*SQLiteTelemetryStore, error) {
	resolved, err := resolveBasePath(basePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(resolved, 0o755); err != nil {
		return nil, fmt.Errorf("ensure storage path: %w", err)
	}
	return &SQLiteTelemetryStore{
		basePath: resolved,
		dbPath:   filepath.Join(resolved, telemetrySQLiteFileName),
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
		`CREATE TABLE IF NOT EXISTS cumulative_snapshots (
			snapshot_id INTEGER PRIMARY KEY AUTOINCREMENT,
			captured_at TEXT NOT NULL,
			payload TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cumulative_snapshots_captured_at
			ON cumulative_snapshots(captured_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("init telemetry sqlite schema: %w", err)
		}
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
