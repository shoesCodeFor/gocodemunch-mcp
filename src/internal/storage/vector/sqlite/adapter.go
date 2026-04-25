package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	_ "modernc.org/sqlite"
)

const (
	defaultVectorDBFileName = "vectors.db"
	maxDeleteBatchSize      = 500
)

// Adapter implements indexing.VectorBackend with a local SQLite database.
type Adapter struct {
	basePath string
	dbPath   string
	db       *sql.DB
}

var _ indexing.VectorBackend = (*Adapter)(nil)

// NewAdapter opens (and bootstraps) the SQLite vector backend.
func NewAdapter(basePath string) (*Adapter, error) {
	resolvedBasePath, err := resolveBasePath(basePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(resolvedBasePath, 0o755); err != nil {
		return nil, fmt.Errorf("ensure vector storage path: %w", err)
	}

	dbPath := filepath.Join(resolvedBasePath, defaultVectorDBFileName)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite vector db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	adapter := &Adapter{
		basePath: resolvedBasePath,
		dbPath:   dbPath,
		db:       db,
	}
	if err := adapter.bootstrap(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return adapter, nil
}

// BasePath returns the resolved filesystem path for vector storage.
func (a *Adapter) BasePath() string {
	if a == nil {
		return ""
	}
	return a.basePath
}

// DBPath returns the SQLite file path used by this adapter.
func (a *Adapter) DBPath() string {
	if a == nil {
		return ""
	}
	return a.dbPath
}

// Close releases underlying database resources.
func (a *Adapter) Close() error {
	if a == nil || a.db == nil {
		return nil
	}
	return a.db.Close()
}

func (a *Adapter) bootstrap(ctx context.Context) error {
	if a == nil || a.db == nil {
		return errors.New("sqlite vector adapter is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	statements := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA wal_autocheckpoint = 1000",
		"PRAGMA busy_timeout = 5000",
		`CREATE TABLE IF NOT EXISTS vector_records (
			namespace TEXT NOT NULL,
			id TEXT NOT NULL,
			embedding_json TEXT NOT NULL,
			metadata_json TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			PRIMARY KEY (namespace, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_vector_records_namespace ON vector_records(namespace)`,
	}
	for _, statement := range statements {
		if _, err := a.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("bootstrap sqlite vector schema: %w", err)
		}
	}
	return nil
}

// Upsert inserts or updates vector records in a namespace.
func (a *Adapter) Upsert(
	ctx context.Context,
	request indexing.VectorUpsertRequest,
) (indexing.VectorUpsertResponse, error) {
	namespace, err := normalizeNamespace(request.Namespace)
	if err != nil {
		return indexing.VectorUpsertResponse{}, fmt.Errorf("upsert vectors: %w", err)
	}
	if len(request.Records) == 0 {
		return indexing.VectorUpsertResponse{Upserted: 0}, nil
	}
	if a == nil || a.db == nil {
		return indexing.VectorUpsertResponse{}, errors.New("upsert vectors: sqlite vector adapter is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return indexing.VectorUpsertResponse{}, err
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return indexing.VectorUpsertResponse{}, fmt.Errorf("upsert vectors: begin sqlite tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO vector_records(namespace, id, embedding_json, metadata_json, updated_at)
		VALUES(?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(namespace, id) DO UPDATE SET
			embedding_json = excluded.embedding_json,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at
	`)
	if err != nil {
		return indexing.VectorUpsertResponse{}, fmt.Errorf("upsert vectors: prepare sqlite statement: %w", err)
	}
	defer statement.Close()

	upserted := 0
	for index, record := range request.Records {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return indexing.VectorUpsertResponse{}, fmt.Errorf(
				"upsert vectors: record at index %d must include a non-empty id",
				index,
			)
		}

		recordNamespace := strings.TrimSpace(record.Namespace)
		if recordNamespace != "" && recordNamespace != namespace {
			return indexing.VectorUpsertResponse{}, fmt.Errorf(
				"upsert vectors: record %q namespace %q does not match request namespace %q",
				id,
				recordNamespace,
				namespace,
			)
		}

		if err := validateEmbedding(record.Embedding); err != nil {
			return indexing.VectorUpsertResponse{}, fmt.Errorf(
				"upsert vectors: record %q has invalid embedding: %w",
				id,
				err,
			)
		}

		embeddingJSON, err := encodeEmbedding(record.Embedding)
		if err != nil {
			return indexing.VectorUpsertResponse{}, fmt.Errorf(
				"upsert vectors: encode embedding for record %q: %w",
				id,
				err,
			)
		}
		metadataJSON, err := encodeMetadata(record.Metadata)
		if err != nil {
			return indexing.VectorUpsertResponse{}, fmt.Errorf(
				"upsert vectors: encode metadata for record %q: %w",
				id,
				err,
			)
		}

		if _, err := statement.ExecContext(ctx, namespace, id, embeddingJSON, metadataJSON); err != nil {
			return indexing.VectorUpsertResponse{}, fmt.Errorf(
				"upsert vectors: persist record %q: %w",
				id,
				err,
			)
		}
		upserted++
	}

	if err := tx.Commit(); err != nil {
		return indexing.VectorUpsertResponse{}, fmt.Errorf("upsert vectors: commit sqlite tx: %w", err)
	}
	return indexing.VectorUpsertResponse{Upserted: upserted}, nil
}

// Query searches vectors in a namespace and returns top-k ranked matches.
func (a *Adapter) Query(
	ctx context.Context,
	request indexing.VectorQueryRequest,
) (indexing.VectorQueryResponse, error) {
	namespace, err := normalizeNamespace(request.Namespace)
	if err != nil {
		return indexing.VectorQueryResponse{}, fmt.Errorf("query vectors: %w", err)
	}
	if request.TopK <= 0 {
		return indexing.VectorQueryResponse{}, fmt.Errorf(
			"query vectors: top_k must be positive (got %d)",
			request.TopK,
		)
	}
	if err := validateEmbedding(request.Embedding); err != nil {
		return indexing.VectorQueryResponse{}, fmt.Errorf("query vectors: invalid query embedding: %w", err)
	}
	if a == nil || a.db == nil {
		return indexing.VectorQueryResponse{}, errors.New("query vectors: sqlite vector adapter is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return indexing.VectorQueryResponse{}, err
	}

	rows, err := a.db.QueryContext(
		ctx,
		`SELECT id, embedding_json, metadata_json FROM vector_records WHERE namespace = ? ORDER BY id ASC`,
		namespace,
	)
	if err != nil {
		return indexing.VectorQueryResponse{}, fmt.Errorf("query vectors: load namespace rows: %w", err)
	}
	defer rows.Close()

	matches := make([]indexing.VectorQueryMatch, 0, request.TopK)
	for rows.Next() {
		var (
			id            string
			embeddingJSON string
			metadataJSON  string
		)
		if err := rows.Scan(&id, &embeddingJSON, &metadataJSON); err != nil {
			return indexing.VectorQueryResponse{}, fmt.Errorf("query vectors: scan namespace row: %w", err)
		}

		recordEmbedding, err := decodeEmbedding(embeddingJSON)
		if err != nil {
			return indexing.VectorQueryResponse{}, fmt.Errorf(
				"query vectors: decode embedding for record %q: %w",
				id,
				err,
			)
		}
		if len(recordEmbedding) != len(request.Embedding) {
			return indexing.VectorQueryResponse{}, fmt.Errorf(
				"query vectors: embedding dimension mismatch for record %q: expected %d, got %d",
				id,
				len(request.Embedding),
				len(recordEmbedding),
			)
		}

		score, err := cosineSimilarity(request.Embedding, recordEmbedding)
		if err != nil {
			return indexing.VectorQueryResponse{}, fmt.Errorf(
				"query vectors: compute score for record %q: %w",
				id,
				err,
			)
		}

		recordMetadata, err := decodeMetadata(metadataJSON)
		if err != nil {
			return indexing.VectorQueryResponse{}, fmt.Errorf(
				"query vectors: decode metadata for record %q: %w",
				id,
				err,
			)
		}

		matches = append(matches, indexing.VectorQueryMatch{
			Record: indexing.VectorRecord{
				ID:        id,
				Namespace: namespace,
				Embedding: cloneEmbedding(recordEmbedding),
				Metadata:  recordMetadata,
			},
			Score:    score,
			RawScore: score,
		})
	}
	if err := rows.Err(); err != nil {
		return indexing.VectorQueryResponse{}, fmt.Errorf("query vectors: iterate rows: %w", err)
	}

	sort.Slice(matches, func(i, j int) bool {
		left := matches[i]
		right := matches[j]
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		if left.Record.ID != right.Record.ID {
			return left.Record.ID < right.Record.ID
		}
		if left.Record.Metadata.Path != right.Record.Metadata.Path {
			return left.Record.Metadata.Path < right.Record.Metadata.Path
		}
		if left.Record.Metadata.ChunkID != right.Record.Metadata.ChunkID {
			return left.Record.Metadata.ChunkID < right.Record.Metadata.ChunkID
		}
		return left.Record.Metadata.Repo < right.Record.Metadata.Repo
	})

	if len(matches) > request.TopK {
		matches = matches[:request.TopK]
	}
	return indexing.VectorQueryResponse{Matches: matches}, nil
}

// Delete removes one or more explicit vector IDs from a namespace.
func (a *Adapter) Delete(
	ctx context.Context,
	request indexing.VectorDeleteRequest,
) (indexing.VectorDeleteResponse, error) {
	namespace, err := normalizeNamespace(request.Namespace)
	if err != nil {
		return indexing.VectorDeleteResponse{}, fmt.Errorf("delete vectors: %w", err)
	}
	if len(request.IDs) == 0 {
		return indexing.VectorDeleteResponse{Deleted: 0}, nil
	}
	if a == nil || a.db == nil {
		return indexing.VectorDeleteResponse{}, errors.New("delete vectors: sqlite vector adapter is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return indexing.VectorDeleteResponse{}, err
	}

	ids := normalizeIDs(request.IDs)
	if len(ids) == 0 {
		return indexing.VectorDeleteResponse{}, errors.New(
			"delete vectors: ids must include at least one non-empty value",
		)
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return indexing.VectorDeleteResponse{}, fmt.Errorf("delete vectors: begin sqlite tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	deleted := 0
	for start := 0; start < len(ids); start += maxDeleteBatchSize {
		end := start + maxDeleteBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]

		placeholders := make([]string, len(batch))
		args := make([]any, 0, len(batch)+1)
		args = append(args, namespace)
		for i, id := range batch {
			placeholders[i] = "?"
			args = append(args, id)
		}

		query := fmt.Sprintf(
			"DELETE FROM vector_records WHERE namespace = ? AND id IN (%s)",
			strings.Join(placeholders, ", "),
		)
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return indexing.VectorDeleteResponse{}, fmt.Errorf("delete vectors: execute delete batch: %w", err)
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return indexing.VectorDeleteResponse{}, fmt.Errorf("delete vectors: read rows affected: %w", err)
		}
		deleted += int(affected)
	}

	if err := tx.Commit(); err != nil {
		return indexing.VectorDeleteResponse{}, fmt.Errorf("delete vectors: commit sqlite tx: %w", err)
	}
	return indexing.VectorDeleteResponse{Deleted: deleted}, nil
}

// DeleteNamespace removes all records from one namespace.
func (a *Adapter) DeleteNamespace(
	ctx context.Context,
	request indexing.VectorDeleteNamespaceRequest,
) (indexing.VectorDeleteNamespaceResponse, error) {
	namespace, err := normalizeNamespace(request.Namespace)
	if err != nil {
		return indexing.VectorDeleteNamespaceResponse{}, fmt.Errorf("delete vector namespace: %w", err)
	}
	if a == nil || a.db == nil {
		return indexing.VectorDeleteNamespaceResponse{}, errors.New(
			"delete vector namespace: sqlite vector adapter is nil",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return indexing.VectorDeleteNamespaceResponse{}, err
	}

	result, err := a.db.ExecContext(ctx, "DELETE FROM vector_records WHERE namespace = ?", namespace)
	if err != nil {
		return indexing.VectorDeleteNamespaceResponse{}, fmt.Errorf(
			"delete vector namespace: execute namespace delete: %w",
			err,
		)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return indexing.VectorDeleteNamespaceResponse{}, fmt.Errorf(
			"delete vector namespace: read rows affected: %w",
			err,
		)
	}
	return indexing.VectorDeleteNamespaceResponse{Deleted: int(affected)}, nil
}

// Health reports backend readiness and diagnostics.
func (a *Adapter) Health(ctx context.Context) (indexing.VectorHealthResponse, error) {
	if a == nil || a.db == nil {
		return indexing.VectorHealthResponse{
			Ready:   false,
			Message: "sqlite vector adapter is nil",
		}, errors.New("sqlite vector adapter is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return indexing.VectorHealthResponse{}, err
	}

	metadata := map[string]any{
		"backend":   "sqlite",
		"base_path": a.basePath,
		"db_path":   a.dbPath,
	}

	if err := a.db.QueryRowContext(ctx, "SELECT 1").Scan(new(int)); err != nil {
		return indexing.VectorHealthResponse{
			Ready:    false,
			Message:  "sqlite ping failed",
			Metadata: metadata,
		}, fmt.Errorf("vector health check: sqlite ping failed: %w", err)
	}

	var tableCount int
	if err := a.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'vector_records'`,
	).Scan(&tableCount); err != nil {
		return indexing.VectorHealthResponse{
			Ready:    false,
			Message:  "schema check failed",
			Metadata: metadata,
		}, fmt.Errorf("vector health check: schema check failed: %w", err)
	}
	if tableCount == 0 {
		return indexing.VectorHealthResponse{
			Ready:    false,
			Message:  "vector_records table missing",
			Metadata: metadata,
		}, errors.New("vector health check: vector_records table missing")
	}

	return indexing.VectorHealthResponse{
		Ready:    true,
		Message:  "ok",
		Metadata: metadata,
	}, nil
}

func normalizeNamespace(namespace string) (string, error) {
	trimmed := strings.TrimSpace(namespace)
	if trimmed == "" {
		return "", errors.New("namespace must be non-empty")
	}
	return trimmed, nil
}

func normalizeIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func validateEmbedding(embedding []float32) error {
	if len(embedding) == 0 {
		return errors.New("embedding must be non-empty")
	}

	normSquared := 0.0
	for index, value := range embedding {
		value64 := float64(value)
		if math.IsNaN(value64) || math.IsInf(value64, 0) {
			return fmt.Errorf("embedding value at index %d must be finite", index)
		}
		normSquared += value64 * value64
	}
	if normSquared <= 0 {
		return errors.New("embedding magnitude must be greater than zero")
	}
	return nil
}

func cosineSimilarity(left, right []float32) (float64, error) {
	if len(left) != len(right) {
		return 0, fmt.Errorf("dimension mismatch: %d vs %d", len(left), len(right))
	}

	dot := 0.0
	leftNormSquared := 0.0
	rightNormSquared := 0.0
	for i := range left {
		leftValue := float64(left[i])
		rightValue := float64(right[i])
		dot += leftValue * rightValue
		leftNormSquared += leftValue * leftValue
		rightNormSquared += rightValue * rightValue
	}
	if leftNormSquared <= 0 || rightNormSquared <= 0 {
		return 0, errors.New("cannot compute cosine similarity with zero-magnitude embedding")
	}

	score := dot / (math.Sqrt(leftNormSquared) * math.Sqrt(rightNormSquared))
	if score > 1 {
		score = 1
	}
	if score < -1 {
		score = -1
	}
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0, errors.New("cosine similarity produced non-finite score")
	}
	return score, nil
}

func encodeEmbedding(embedding []float32) (string, error) {
	encoded, err := json.Marshal(embedding)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeEmbedding(raw string) ([]float32, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("embedding payload was empty")
	}
	values := []float32{}
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return nil, err
	}
	if err := validateEmbedding(values); err != nil {
		return nil, err
	}
	return values, nil
}

func encodeMetadata(metadata indexing.VectorMetadata) (string, error) {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeMetadata(raw string) (indexing.VectorMetadata, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return indexing.VectorMetadata{}, nil
	}
	metadata := indexing.VectorMetadata{}
	if err := json.Unmarshal([]byte(trimmed), &metadata); err != nil {
		return indexing.VectorMetadata{}, err
	}
	if metadata.Fields == nil {
		metadata.Fields = map[string]any{}
	}
	return metadata, nil
}

func cloneEmbedding(source []float32) []float32 {
	if len(source) == 0 {
		return []float32{}
	}
	target := make([]float32, len(source))
	copy(target, source)
	return target
}

func resolveBasePath(basePath string) (string, error) {
	trimmed := strings.TrimSpace(basePath)
	if trimmed == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		trimmed = filepath.Join(home, ".code-index")
	}

	if strings.HasPrefix(trimmed, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory for storage path: %w", err)
		}
		switch {
		case trimmed == "~":
			trimmed = home
		case strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "~\\"):
			trimmed = filepath.Join(home, trimmed[2:])
		default:
			return "", fmt.Errorf("unsupported tilde path: %s", trimmed)
		}
	}

	return filepath.Clean(trimmed), nil
}
