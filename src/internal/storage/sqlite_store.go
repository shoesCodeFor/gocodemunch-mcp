package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const sqliteCurrentIndexVersion = currentIndexVersion

type SQLiteIndexStore struct {
	basePath string
	mu       sync.RWMutex
}

type sqlitePersistedRepo struct {
	Repo            string            `json:"repo"`
	IndexedAt       string            `json:"indexed_at"`
	SourceRoot      string            `json:"source_root"`
	DisplayName     string            `json:"display_name,omitempty"`
	SymbolCount     int               `json:"symbol_count"`
	FileCount       int               `json:"file_count"`
	Languages       map[string]int    `json:"languages"`
	ContextMetadata map[string]any    `json:"context_metadata,omitempty"`
	IndexVersion    int               `json:"index_version"`
	GitHead         string            `json:"git_head,omitempty"`
	Files           map[string]string `json:"files"`
	FileBlobSHAs    map[string]string `json:"file_blob_shas,omitempty"`
	FileMTimes      map[string]int64  `json:"file_mtimes,omitempty"`
	Symbols         map[string]any    `json:"symbols,omitempty"`
}

func NewSQLiteIndexStore(basePath string) (*SQLiteIndexStore, error) {
	resolved, err := resolveBasePath(basePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(resolved, 0o755); err != nil {
		return nil, fmt.Errorf("ensure storage path: %w", err)
	}
	return &SQLiteIndexStore{basePath: resolved}, nil
}

func (s *SQLiteIndexStore) BasePath() string {
	return s.basePath
}

func (s *SQLiteIndexStore) Load(_ context.Context, repo string) (RepoIndex, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := s.readPersisted(repo)
	if err != nil {
		return RepoIndex{}, err
	}
	return sqliteToRepoIndex(data), nil
}

func (s *SQLiteIndexStore) Save(_ context.Context, repo string, index RepoIndex) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalized := normalizeRepoIndex(repo, index)
	return s.writePersisted(sqliteFromRepoIndex(normalized))
}

func (s *SQLiteIndexStore) IncrementalSave(_ context.Context, repo string, index RepoIndex, changes ChangeSet) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalized := normalizeRepoIndex(repo, index)
	for _, deleted := range changes.Deleted {
		delete(normalized.Files, deleted)
		delete(normalized.FileBlobSHAs, deleted)
		delete(normalized.FileMTimes, deleted)
	}
	return s.writePersisted(sqliteFromRepoIndex(normalized))
}

func (s *SQLiteIndexStore) Delete(_ context.Context, repo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dbPath, err := s.repoDBPath(repo)
	if err != nil {
		return err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := dbPath + suffix
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete repo index: %w", err)
		}
	}
	owner, name, splitErr := splitRepoID(repo)
	if splitErr == nil {
		jsonPath := filepath.Join(s.basePath, repoSlug(owner, name)+".json")
		if err := os.Remove(jsonPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete repo sidecar snapshot: %w", err)
		}
	}
	return nil
}

func (s *SQLiteIndexStore) List(ctx context.Context) ([]RepoMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	matches, err := filepath.Glob(filepath.Join(s.basePath, "*.db"))
	if err != nil {
		return nil, fmt.Errorf("list repo indexes: %w", err)
	}

	repos := make([]RepoMetadata, 0, len(matches))
	seenRepos := make(map[string]struct{}, len(matches))
	for _, path := range matches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		data, err := s.readPersistedByPath(path)
		if err != nil {
			continue
		}
		if data.Repo == "" {
			continue
		}
		seenRepos[data.Repo] = struct{}{}
		repos = append(repos, RepoMetadata{
			Repo:         data.Repo,
			IndexedAt:    data.IndexedAt,
			SymbolCount:  data.SymbolCount,
			FileCount:    data.FileCount,
			Languages:    cloneLanguageCounts(data.Languages),
			IndexVersion: data.IndexVersion,
			GitHead:      data.GitHead,
			DisplayName:  data.DisplayName,
			SourceRoot:   data.SourceRoot,
			Indexed:      true,
		})
	}

	jsonMatches, err := filepath.Glob(filepath.Join(s.basePath, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("list legacy repo indexes: %w", err)
	}
	for _, path := range jsonMatches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := s.readLegacyJSONByPath(path)
		if err != nil || data.Repo == "" {
			continue
		}
		if _, exists := seenRepos[data.Repo]; exists {
			continue
		}
		seenRepos[data.Repo] = struct{}{}
		repos = append(repos, RepoMetadata{
			Repo:         data.Repo,
			IndexedAt:    data.IndexedAt,
			SymbolCount:  data.SymbolCount,
			FileCount:    data.FileCount,
			Languages:    cloneLanguageCounts(data.Languages),
			IndexVersion: data.IndexVersion,
			GitHead:      data.GitHead,
			DisplayName:  data.DisplayName,
			SourceRoot:   data.SourceRoot,
			Indexed:      true,
		})
	}

	sort.Slice(repos, func(i, j int) bool { return repos[i].Repo < repos[j].Repo })
	return repos, nil
}

func (s *SQLiteIndexStore) DetectChanges(_ context.Context, repo string, candidates []string) (ChangeSet, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	existing, err := s.readPersisted(repo)
	if err != nil {
		if errors.Is(err, ErrRepoNotFound) {
			newFiles := append([]string(nil), candidates...)
			sort.Strings(newFiles)
			return ChangeSet{New: newFiles}, nil
		}
		return ChangeSet{}, err
	}

	candidateSet := make(map[string]struct{}, len(candidates))
	newFiles := make([]string, 0)
	for _, candidate := range candidates {
		candidateSet[candidate] = struct{}{}
		if _, ok := existing.Files[candidate]; !ok {
			newFiles = append(newFiles, candidate)
		}
	}

	deleted := make([]string, 0)
	for file := range existing.Files {
		if _, ok := candidateSet[file]; !ok {
			deleted = append(deleted, file)
		}
	}

	sort.Strings(newFiles)
	sort.Strings(deleted)
	return ChangeSet{New: newFiles, Deleted: deleted}, nil
}

func (s *SQLiteIndexStore) readPersisted(repo string) (sqlitePersistedRepo, error) {
	path, err := s.repoDBPath(repo)
	if err != nil {
		return sqlitePersistedRepo{}, err
	}
	data, readErr := s.readPersistedByPath(path)
	if readErr == nil {
		return data, nil
	}
	if !errors.Is(readErr, ErrRepoNotFound) {
		return sqlitePersistedRepo{}, readErr
	}
	return s.readLegacyJSON(repo)
}

func (s *SQLiteIndexStore) readPersistedByPath(path string) (sqlitePersistedRepo, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return sqlitePersistedRepo{}, ErrRepoNotFound
		}
		return sqlitePersistedRepo{}, fmt.Errorf("stat sqlite db: %w", err)
	}
	db, err := s.openDB(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sqlitePersistedRepo{}, ErrRepoNotFound
		}
		return sqlitePersistedRepo{}, err
	}
	defer db.Close()

	data, err := loadSQLiteRepo(db)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sqlitePersistedRepo{}, ErrRepoNotFound
		}
		return sqlitePersistedRepo{}, err
	}
	return data, nil
}

func (s *SQLiteIndexStore) writePersisted(data sqlitePersistedRepo) error {
	path, err := s.repoDBPath(data.Repo)
	if err != nil {
		return err
	}
	db, err := s.openDB(path)
	if err != nil {
		return err
	}
	defer db.Close()

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := saveSQLiteRepo(db, data); err == nil {
			if sidecarErr := s.writeJSONSidecar(data); sidecarErr != nil {
				return sidecarErr
			}
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

func (s *SQLiteIndexStore) repoDBPath(repo string) (string, error) {
	owner, name, err := splitRepoID(repo)
	if err != nil {
		return "", err
	}
	slug := repoSlug(owner, name)
	return filepath.Join(s.basePath, slug+".db"), nil
}

func (s *SQLiteIndexStore) openDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("ensure storage path: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := initSQLiteSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func initSQLiteSchema(db *sql.DB) error {
	statements := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA wal_autocheckpoint = 1000",
		"PRAGMA busy_timeout = 5000",
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE IF NOT EXISTS repo_snapshot (repo TEXT PRIMARY KEY, payload TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("init sqlite schema: %w", err)
		}
	}
	return nil
}

func loadSQLiteRepo(db *sql.DB) (sqlitePersistedRepo, error) {
	var payload string
	if err := db.QueryRow(`SELECT payload FROM repo_snapshot LIMIT 1`).Scan(&payload); err != nil {
		return sqlitePersistedRepo{}, err
	}
	var data sqlitePersistedRepo
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		return sqlitePersistedRepo{}, fmt.Errorf("decode repo snapshot: %w", err)
	}
	if data.IndexVersion == 0 {
		data.IndexVersion = sqliteCurrentIndexVersion
	}
	if data.Files == nil {
		data.Files = map[string]string{}
	}
	if data.FileBlobSHAs == nil {
		data.FileBlobSHAs = map[string]string{}
	}
	if data.FileMTimes == nil {
		data.FileMTimes = map[string]int64{}
	}
	if data.Symbols == nil {
		data.Symbols = map[string]any{}
	}
	if data.ContextMetadata == nil {
		data.ContextMetadata = map[string]any{}
	}
	if data.Languages == nil {
		data.Languages = map[string]int{}
	}
	return data, nil
}

func saveSQLiteRepo(db *sql.DB, data sqlitePersistedRepo) error {
	if data.IndexVersion == 0 {
		data.IndexVersion = sqliteCurrentIndexVersion
	}
	if data.Files == nil {
		data.Files = map[string]string{}
	}
	if data.FileBlobSHAs == nil {
		data.FileBlobSHAs = map[string]string{}
	}
	if data.FileMTimes == nil {
		data.FileMTimes = map[string]int64{}
	}
	if data.Symbols == nil {
		data.Symbols = map[string]any{}
	}
	if data.ContextMetadata == nil {
		data.ContextMetadata = map[string]any{}
	}
	if data.Languages == nil {
		data.Languages = map[string]int{}
	}
	if data.FileCount == 0 && len(data.Files) > 0 {
		data.FileCount = len(data.Files)
	}
	if data.SymbolCount == 0 && len(data.Symbols) > 0 {
		data.SymbolCount = len(data.Symbols)
	}

	payloadBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode repo snapshot: %w", err)
	}
	payload := string(payloadBytes)

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin sqlite tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	metaValues := map[string]string{
		"repo":             data.Repo,
		"indexed_at":       data.IndexedAt,
		"index_version":    fmt.Sprintf("%d", data.IndexVersion),
		"git_head":         data.GitHead,
		"source_root":      data.SourceRoot,
		"display_name":     data.DisplayName,
		"languages":        mustJSONString(data.Languages),
		"context_metadata": mustJSONString(data.ContextMetadata),
	}
	owner, name, splitErr := splitRepoID(data.Repo)
	if splitErr == nil {
		metaValues["owner"] = owner
		metaValues["name"] = name
	}
	for key, value := range metaValues {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES(?, ?)`, key, value); err != nil {
			return fmt.Errorf("write sqlite meta: %w", err)
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO repo_snapshot(repo, payload) VALUES(?, ?)`, data.Repo, payload); err != nil {
		return fmt.Errorf("write sqlite snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite tx: %w", err)
	}
	return nil
}

func mustJSONString(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	trimmed := strings.TrimSpace(string(encoded))
	if trimmed == "null" {
		return "{}"
	}
	return trimmed
}

func isSQLiteBusyErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "database is locked") || strings.Contains(text, "sql statements in progress") || strings.Contains(text, "busy")
}

func (s *SQLiteIndexStore) writeJSONSidecar(data sqlitePersistedRepo) error {
	owner, name, err := splitRepoID(data.Repo)
	if err != nil {
		return err
	}
	slug := repoSlug(owner, name)
	path := filepath.Join(s.basePath, slug+".json")
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sqlite sidecar snapshot: %w", err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, encoded, 0o644); err != nil {
		return fmt.Errorf("write sqlite sidecar snapshot temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename sqlite sidecar snapshot temp file: %w", err)
	}
	return nil
}

func (s *SQLiteIndexStore) readLegacyJSON(repo string) (sqlitePersistedRepo, error) {
	owner, name, err := splitRepoID(repo)
	if err != nil {
		return sqlitePersistedRepo{}, err
	}
	path := filepath.Join(s.basePath, repoSlug(owner, name)+".json")
	return s.readLegacyJSONByPath(path)
}

func (s *SQLiteIndexStore) readLegacyJSONByPath(path string) (sqlitePersistedRepo, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sqlitePersistedRepo{}, ErrRepoNotFound
		}
		return sqlitePersistedRepo{}, fmt.Errorf("read legacy repo snapshot: %w", err)
	}
	var data sqlitePersistedRepo
	if err := json.Unmarshal(payload, &data); err != nil {
		return sqlitePersistedRepo{}, fmt.Errorf("decode legacy repo snapshot: %w", err)
	}
	if data.Repo == "" {
		return sqlitePersistedRepo{}, fmt.Errorf("decode legacy repo snapshot: missing repo field")
	}
	if data.IndexVersion == 0 {
		data.IndexVersion = sqliteCurrentIndexVersion
	}
	if data.Files == nil {
		data.Files = map[string]string{}
	}
	if data.FileBlobSHAs == nil {
		data.FileBlobSHAs = map[string]string{}
	}
	if data.FileMTimes == nil {
		data.FileMTimes = map[string]int64{}
	}
	if data.Symbols == nil {
		data.Symbols = map[string]any{}
	}
	if data.ContextMetadata == nil {
		data.ContextMetadata = map[string]any{}
	}
	if data.Languages == nil {
		data.Languages = map[string]int{}
	}
	if data.FileCount == 0 && len(data.Files) > 0 {
		data.FileCount = len(data.Files)
	}
	if data.SymbolCount == 0 && len(data.Symbols) > 0 {
		data.SymbolCount = len(data.Symbols)
	}
	if err := s.writePersisted(data); err != nil {
		return sqlitePersistedRepo{}, fmt.Errorf("migrate legacy repo snapshot: %w", err)
	}
	return data, nil
}

func sqliteFromRepoIndex(index RepoIndex) sqlitePersistedRepo {
	symbolCount := len(index.Symbols)
	return sqlitePersistedRepo{
		Repo:            index.Repo,
		IndexedAt:       index.IndexedAt,
		SourceRoot:      index.SourceRoot,
		DisplayName:     index.DisplayName,
		SymbolCount:     symbolCount,
		FileCount:       len(index.Files),
		Languages:       cloneLanguageCounts(index.Languages),
		ContextMetadata: cloneContextMetadataMap(index.ContextMetadata),
		IndexVersion:    index.IndexVersion,
		GitHead:         index.GitHead,
		Files:           cloneFileHashes(index.Files),
		FileBlobSHAs:    cloneFileHashes(index.FileBlobSHAs),
		FileMTimes:      cloneFileMTimes(index.FileMTimes),
		Symbols:         cloneSymbolMap(index.Symbols),
	}
}

func sqliteToRepoIndex(data sqlitePersistedRepo) RepoIndex {
	return RepoIndex{
		Repo:            data.Repo,
		IndexedAt:       data.IndexedAt,
		SourceRoot:      data.SourceRoot,
		DisplayName:     data.DisplayName,
		Languages:       cloneLanguageCounts(data.Languages),
		ContextMetadata: cloneContextMetadataMap(data.ContextMetadata),
		IndexVersion:    data.IndexVersion,
		GitHead:         data.GitHead,
		Files:           cloneFileHashes(data.Files),
		FileBlobSHAs:    cloneFileHashes(data.FileBlobSHAs),
		FileMTimes:      cloneFileMTimes(data.FileMTimes),
		Symbols:         cloneSymbolMap(data.Symbols),
	}
}
