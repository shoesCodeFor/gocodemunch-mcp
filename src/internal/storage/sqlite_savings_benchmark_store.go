package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/telemetry"
)

// SavingsBenchmarkRun captures one persisted token-savings benchmark aggregate.
type SavingsBenchmarkRun struct {
	RunID                  string
	CapturedAt             time.Time
	Dataset                string
	SuiteVersion           string
	Mode                   string
	Provider               string
	Backend                string
	Model                  string
	IndexedRepo            string
	FileCount              int
	CaseCount              int
	WithMCPInputTokens     int
	WithMCPOutputTokens    int
	WithMCPTotalTokens     int
	WithoutMCPInputTokens  int
	WithoutMCPOutputTokens int
	WithoutMCPTotalTokens  int
	TokensSaved            int
	SavingsPct             float64
	CompetitorScores       map[string]SavingsBenchmarkCompetitorScore
	TelemetrySnapshot      telemetry.PersistedCumulativeSnapshot
}

// SavingsBenchmarkCompetitorScore captures competitor-specific benchmark deltas.
type SavingsBenchmarkCompetitorScore struct {
	TokensSaved  int     `json:"tokens_saved"`
	CostSavedUSD float64 `json:"cost_saved_usd"`
	SavingsPct   float64 `json:"savings_pct"`
}

// SavingsBenchmarkRunFilter narrows benchmark-history reads.
type SavingsBenchmarkRunFilter struct {
	Dataset           string
	SuiteVersion      string
	Mode              string
	Provider          string
	Backend           string
	Model             string
	ExcludeRunID      string
	CapturedAtOrAfter time.Time
}

type savingsBenchmarkRunPayload struct {
	RunID                  string                                     `json:"run_id"`
	CapturedAt             time.Time                                  `json:"captured_at"`
	Dataset                string                                     `json:"dataset"`
	SuiteVersion           string                                     `json:"suite_version"`
	Mode                   string                                     `json:"mode"`
	Provider               string                                     `json:"provider"`
	Backend                string                                     `json:"backend"`
	Model                  string                                     `json:"model"`
	IndexedRepo            string                                     `json:"indexed_repo"`
	FileCount              int                                        `json:"file_count"`
	CaseCount              int                                        `json:"case_count"`
	WithMCPInputTokens     int                                        `json:"with_mcp_input_tokens"`
	WithMCPOutputTokens    int                                        `json:"with_mcp_output_tokens"`
	WithMCPTotalTokens     int                                        `json:"with_mcp_total_tokens"`
	WithoutMCPInputTokens  int                                        `json:"without_mcp_input_tokens"`
	WithoutMCPOutputTokens int                                        `json:"without_mcp_output_tokens"`
	WithoutMCPTotalTokens  int                                        `json:"without_mcp_total_tokens"`
	TokensSaved            int                                        `json:"tokens_saved"`
	SavingsPct             float64                                    `json:"savings_pct"`
	CompetitorScores       map[string]SavingsBenchmarkCompetitorScore `json:"competitor_scores"`
}

// SaveSavingsBenchmarkRun upserts one benchmark aggregate and its competitor rows.
func (s *SQLiteTelemetryStore) SaveSavingsBenchmarkRun(
	ctx context.Context,
	run SavingsBenchmarkRun,
) error {
	normalized := normalizeSavingsBenchmarkRun(run, s.now)
	if err := validateSavingsBenchmarkRun(normalized); err != nil {
		return err
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
		if err := saveSQLiteSavingsBenchmarkRun(ctx, db, normalized); err == nil {
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

// LoadSavingsBenchmarkRuns returns benchmark-history rows in ascending capture order.
func (s *SQLiteTelemetryStore) LoadSavingsBenchmarkRuns(
	ctx context.Context,
	filter SavingsBenchmarkRunFilter,
) ([]SavingsBenchmarkRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, err := os.Stat(s.dbPath); err != nil {
		if os.IsNotExist(err) {
			return []SavingsBenchmarkRun{}, nil
		}
		return nil, fmt.Errorf("stat telemetry db: %w", err)
	}

	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	return loadSQLiteSavingsBenchmarkRuns(ctx, db, filter)
}

func saveSQLiteSavingsBenchmarkRun(
	ctx context.Context,
	db *sql.DB,
	run SavingsBenchmarkRun,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin savings benchmark tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if shouldPersistSavingsBenchmarkSnapshot(run.TelemetrySnapshot) {
		snapshot := run.TelemetrySnapshot
		snapshot.BenchmarkRunID = run.RunID
		if snapshot.CapturedAt.IsZero() {
			snapshot.CapturedAt = run.CapturedAt
		}
		if err := saveSQLiteTelemetrySnapshotTx(ctx, tx, snapshot); err != nil {
			return fmt.Errorf("save linked benchmark telemetry snapshot: %w", err)
		}
	}

	payloadBytes, err := json.Marshal(marshalSavingsBenchmarkRunPayload(run))
	if err != nil {
		return fmt.Errorf("encode savings benchmark payload: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO savings_benchmark_runs(
			run_id,
			captured_at,
			dataset,
			suite_version,
			mode,
			provider,
			backend,
			model,
			indexed_repo,
			file_count,
			case_count,
			with_mcp_input_tokens,
			with_mcp_output_tokens,
			with_mcp_total_tokens,
			without_mcp_input_tokens,
			without_mcp_output_tokens,
			without_mcp_total_tokens,
			tokens_saved,
			savings_pct,
			payload
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
			captured_at = excluded.captured_at,
			dataset = excluded.dataset,
			suite_version = excluded.suite_version,
			mode = excluded.mode,
			provider = excluded.provider,
			backend = excluded.backend,
			model = excluded.model,
			indexed_repo = excluded.indexed_repo,
			file_count = excluded.file_count,
			case_count = excluded.case_count,
			with_mcp_input_tokens = excluded.with_mcp_input_tokens,
			with_mcp_output_tokens = excluded.with_mcp_output_tokens,
			with_mcp_total_tokens = excluded.with_mcp_total_tokens,
			without_mcp_input_tokens = excluded.without_mcp_input_tokens,
			without_mcp_output_tokens = excluded.without_mcp_output_tokens,
			without_mcp_total_tokens = excluded.without_mcp_total_tokens,
			tokens_saved = excluded.tokens_saved,
			savings_pct = excluded.savings_pct,
			payload = excluded.payload`,
		run.RunID,
		run.CapturedAt.Format(time.RFC3339Nano),
		run.Dataset,
		run.SuiteVersion,
		run.Mode,
		run.Provider,
		run.Backend,
		run.Model,
		run.IndexedRepo,
		run.FileCount,
		run.CaseCount,
		run.WithMCPInputTokens,
		run.WithMCPOutputTokens,
		run.WithMCPTotalTokens,
		run.WithoutMCPInputTokens,
		run.WithoutMCPOutputTokens,
		run.WithoutMCPTotalTokens,
		run.TokensSaved,
		run.SavingsPct,
		string(payloadBytes),
	); err != nil {
		return fmt.Errorf("upsert savings benchmark run: %w", err)
	}

	stmt, err := tx.PrepareContext(
		ctx,
		`INSERT INTO savings_benchmark_competitors(
			run_id,
			competitor,
			tokens_saved,
			cost_saved_usd,
			savings_pct,
			payload
		) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, competitor) DO UPDATE SET
			tokens_saved = excluded.tokens_saved,
			cost_saved_usd = excluded.cost_saved_usd,
			savings_pct = excluded.savings_pct,
			payload = excluded.payload`,
	)
	if err != nil {
		return fmt.Errorf("prepare savings benchmark competitor upsert: %w", err)
	}
	defer stmt.Close()

	competitors := make([]string, 0, len(run.CompetitorScores))
	for competitor := range run.CompetitorScores {
		competitors = append(competitors, competitor)
	}
	sort.Strings(competitors)

	for _, competitor := range competitors {
		score := run.CompetitorScores[competitor]
		scorePayload, err := json.Marshal(score)
		if err != nil {
			return fmt.Errorf("encode savings benchmark competitor %q payload: %w", competitor, err)
		}
		if _, err := stmt.ExecContext(
			ctx,
			run.RunID,
			competitor,
			score.TokensSaved,
			score.CostSavedUSD,
			score.SavingsPct,
			string(scorePayload),
		); err != nil {
			return fmt.Errorf("upsert savings benchmark competitor %q: %w", competitor, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit savings benchmark tx: %w", err)
	}
	return nil
}

func loadSQLiteSavingsBenchmarkRuns(
	ctx context.Context,
	db *sql.DB,
	filter SavingsBenchmarkRunFilter,
) ([]SavingsBenchmarkRun, error) {
	query := `SELECT payload FROM savings_benchmark_runs WHERE 1 = 1`
	args := make([]any, 0, 7)

	appendStringFilter := func(column string, value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		query += fmt.Sprintf(" AND %s = ?", column)
		args = append(args, trimmed)
	}

	appendStringFilter("dataset", filter.Dataset)
	appendStringFilter("suite_version", filter.SuiteVersion)
	appendStringFilter("mode", filter.Mode)
	appendStringFilter("provider", filter.Provider)
	appendStringFilter("backend", filter.Backend)
	appendStringFilter("model", filter.Model)
	if excluded := strings.TrimSpace(filter.ExcludeRunID); excluded != "" {
		query += ` AND run_id <> ?`
		args = append(args, excluded)
	}
	if !filter.CapturedAtOrAfter.IsZero() {
		query += ` AND unixepoch(captured_at) >= unixepoch(?)`
		args = append(args, filter.CapturedAtOrAfter.UTC().Format(time.RFC3339Nano))
	}
	query += ` ORDER BY captured_at ASC, run_id ASC`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load savings benchmark runs: %w", err)
	}
	defer rows.Close()

	runs := make([]SavingsBenchmarkRun, 0, 8)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan savings benchmark payload: %w", err)
		}

		var decoded savingsBenchmarkRunPayload
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			return nil, fmt.Errorf("decode savings benchmark payload: %w", err)
		}
		runs = append(runs, unmarshalSavingsBenchmarkRunPayload(decoded))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate savings benchmark runs: %w", err)
	}
	return runs, nil
}

func normalizeSavingsBenchmarkRun(
	run SavingsBenchmarkRun,
	now func() time.Time,
) SavingsBenchmarkRun {
	normalized := run
	if now == nil {
		now = time.Now
	}
	if normalized.CapturedAt.IsZero() {
		normalized.CapturedAt = now().UTC()
	} else {
		normalized.CapturedAt = normalized.CapturedAt.UTC()
	}
	normalized.RunID = strings.TrimSpace(normalized.RunID)
	normalized.Dataset = strings.TrimSpace(normalized.Dataset)
	normalized.SuiteVersion = strings.TrimSpace(normalized.SuiteVersion)
	normalized.Mode = strings.TrimSpace(normalized.Mode)
	normalized.Provider = strings.TrimSpace(normalized.Provider)
	normalized.Backend = strings.TrimSpace(normalized.Backend)
	normalized.Model = strings.TrimSpace(normalized.Model)
	normalized.IndexedRepo = strings.TrimSpace(normalized.IndexedRepo)
	normalized.CompetitorScores = cloneSavingsBenchmarkCompetitorScores(normalized.CompetitorScores)
	if !normalized.TelemetrySnapshot.CapturedAt.IsZero() {
		normalized.TelemetrySnapshot.CapturedAt = normalized.TelemetrySnapshot.CapturedAt.UTC()
	}
	normalized.TelemetrySnapshot.BenchmarkRunID = strings.TrimSpace(normalized.TelemetrySnapshot.BenchmarkRunID)
	return normalized
}

func validateSavingsBenchmarkRun(run SavingsBenchmarkRun) error {
	switch {
	case run.RunID == "":
		return fmt.Errorf("save savings benchmark run: run id must be non-empty")
	case run.Dataset == "":
		return fmt.Errorf("save savings benchmark run %q: dataset must be non-empty", run.RunID)
	case run.SuiteVersion == "":
		return fmt.Errorf("save savings benchmark run %q: suite version must be non-empty", run.RunID)
	case run.Mode == "":
		return fmt.Errorf("save savings benchmark run %q: mode must be non-empty", run.RunID)
	case run.Provider == "":
		return fmt.Errorf("save savings benchmark run %q: provider must be non-empty", run.RunID)
	case run.Backend == "":
		return fmt.Errorf("save savings benchmark run %q: backend must be non-empty", run.RunID)
	case len(run.CompetitorScores) == 0:
		return fmt.Errorf("save savings benchmark run %q: competitor scores must be non-empty", run.RunID)
	default:
		return nil
	}
}

func marshalSavingsBenchmarkRunPayload(run SavingsBenchmarkRun) savingsBenchmarkRunPayload {
	return savingsBenchmarkRunPayload{
		RunID:                  run.RunID,
		CapturedAt:             run.CapturedAt,
		Dataset:                run.Dataset,
		SuiteVersion:           run.SuiteVersion,
		Mode:                   run.Mode,
		Provider:               run.Provider,
		Backend:                run.Backend,
		Model:                  run.Model,
		IndexedRepo:            run.IndexedRepo,
		FileCount:              run.FileCount,
		CaseCount:              run.CaseCount,
		WithMCPInputTokens:     run.WithMCPInputTokens,
		WithMCPOutputTokens:    run.WithMCPOutputTokens,
		WithMCPTotalTokens:     run.WithMCPTotalTokens,
		WithoutMCPInputTokens:  run.WithoutMCPInputTokens,
		WithoutMCPOutputTokens: run.WithoutMCPOutputTokens,
		WithoutMCPTotalTokens:  run.WithoutMCPTotalTokens,
		TokensSaved:            run.TokensSaved,
		SavingsPct:             run.SavingsPct,
		CompetitorScores:       cloneSavingsBenchmarkCompetitorScores(run.CompetitorScores),
	}
}

func unmarshalSavingsBenchmarkRunPayload(payload savingsBenchmarkRunPayload) SavingsBenchmarkRun {
	return SavingsBenchmarkRun{
		RunID:                  strings.TrimSpace(payload.RunID),
		CapturedAt:             payload.CapturedAt.UTC(),
		Dataset:                strings.TrimSpace(payload.Dataset),
		SuiteVersion:           strings.TrimSpace(payload.SuiteVersion),
		Mode:                   strings.TrimSpace(payload.Mode),
		Provider:               strings.TrimSpace(payload.Provider),
		Backend:                strings.TrimSpace(payload.Backend),
		Model:                  strings.TrimSpace(payload.Model),
		IndexedRepo:            strings.TrimSpace(payload.IndexedRepo),
		FileCount:              payload.FileCount,
		CaseCount:              payload.CaseCount,
		WithMCPInputTokens:     payload.WithMCPInputTokens,
		WithMCPOutputTokens:    payload.WithMCPOutputTokens,
		WithMCPTotalTokens:     payload.WithMCPTotalTokens,
		WithoutMCPInputTokens:  payload.WithoutMCPInputTokens,
		WithoutMCPOutputTokens: payload.WithoutMCPOutputTokens,
		WithoutMCPTotalTokens:  payload.WithoutMCPTotalTokens,
		TokensSaved:            payload.TokensSaved,
		SavingsPct:             payload.SavingsPct,
		CompetitorScores:       cloneSavingsBenchmarkCompetitorScores(payload.CompetitorScores),
	}
}

func shouldPersistSavingsBenchmarkSnapshot(snapshot telemetry.PersistedCumulativeSnapshot) bool {
	if !snapshot.CapturedAt.IsZero() || strings.TrimSpace(snapshot.BenchmarkRunID) != "" {
		return true
	}
	if snapshot.Cumulative.CallCount > 0 ||
		snapshot.Cumulative.TotalTokens > 0 ||
		snapshot.Cumulative.TokensSaved > 0 ||
		snapshot.Cumulative.SessionCount > 0 {
		return true
	}
	return len(snapshot.Cumulative.ToolBreakdown) > 0
}

func cloneSavingsBenchmarkCompetitorScores(
	input map[string]SavingsBenchmarkCompetitorScore,
) map[string]SavingsBenchmarkCompetitorScore {
	if len(input) == 0 {
		return map[string]SavingsBenchmarkCompetitorScore{}
	}
	cloned := make(map[string]SavingsBenchmarkCompetitorScore, len(input))
	for competitor, score := range input {
		cloned[competitor] = score
	}
	return cloned
}
