package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const currentIndexVersion = 6

var invalidSlugChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// FSIndexStore persists repo indexes as JSON documents in a base directory.
// It is intentionally simple for parity-lane bring-up and keeps a stable on-disk
// shape that can be replaced by SQLite-backed storage in later slices.
type FSIndexStore struct {
	basePath string
	mu       sync.RWMutex
}

type persistedRepo struct {
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

// NewFSIndexStore creates a filesystem-backed index store.
func NewFSIndexStore(basePath string) (*FSIndexStore, error) {
	resolved, err := resolveBasePath(basePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(resolved, 0o755); err != nil {
		return nil, fmt.Errorf("ensure storage path: %w", err)
	}
	return &FSIndexStore{basePath: resolved}, nil
}

// BasePath returns the resolved filesystem root used for persisted indexes.
func (s *FSIndexStore) BasePath() string {
	return s.basePath
}

// Load returns the current persisted index for a repo.
func (s *FSIndexStore) Load(_ context.Context, repo string) (RepoIndex, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := s.readPersisted(repo)
	if err != nil {
		return RepoIndex{}, err
	}
	return toRepoIndex(data), nil
}

// Save writes a full repo snapshot.
func (s *FSIndexStore) Save(_ context.Context, repo string, index RepoIndex) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalized := normalizeRepoIndex(repo, index)
	return s.writePersisted(fromRepoIndex(normalized))
}

// IncrementalSave writes an updated snapshot after applying a delta.
func (s *FSIndexStore) IncrementalSave(_ context.Context, repo string, index RepoIndex, changes ChangeSet) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalized := normalizeRepoIndex(repo, index)
	for _, deleted := range changes.Deleted {
		delete(normalized.Files, deleted)
		delete(normalized.FileBlobSHAs, deleted)
	}
	return s.writePersisted(fromRepoIndex(normalized))
}

// Delete removes a persisted repo snapshot.
func (s *FSIndexStore) Delete(_ context.Context, repo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := s.repoFilePath(repo)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete repo index: %w", err)
	}
	return nil
}

// List returns all persisted repos in deterministic repo-id order.
func (s *FSIndexStore) List(ctx context.Context) ([]RepoMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	matches, err := filepath.Glob(filepath.Join(s.basePath, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("list repo indexes: %w", err)
	}

	repos := make([]RepoMetadata, 0, len(matches))
	for _, path := range matches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		payload, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var data persistedRepo
		if err := json.Unmarshal(payload, &data); err != nil {
			continue
		}
		if data.Repo == "" {
			continue
		}
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

	sort.Slice(repos, func(i, j int) bool {
		return repos[i].Repo < repos[j].Repo
	})
	return repos, nil
}

// DetectChanges returns inventory-based change classification from candidate file paths.
func (s *FSIndexStore) DetectChanges(_ context.Context, repo string, candidates []string) (ChangeSet, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	existing, err := s.readPersisted(repo)
	if err != nil {
		if err == ErrRepoNotFound {
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
	return ChangeSet{
		New:     newFiles,
		Deleted: deleted,
	}, nil
}

func (s *FSIndexStore) readPersisted(repo string) (persistedRepo, error) {
	path, err := s.repoFilePath(repo)
	if err != nil {
		return persistedRepo{}, err
	}

	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return persistedRepo{}, ErrRepoNotFound
		}
		return persistedRepo{}, fmt.Errorf("read repo index: %w", err)
	}

	var data persistedRepo
	if err := json.Unmarshal(payload, &data); err != nil {
		return persistedRepo{}, fmt.Errorf("decode repo index: %w", err)
	}
	if data.Repo == "" {
		data.Repo = repo
	}
	if data.IndexVersion == 0 {
		data.IndexVersion = currentIndexVersion
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

func (s *FSIndexStore) writePersisted(data persistedRepo) error {
	path, err := s.repoFilePath(data.Repo)
	if err != nil {
		return err
	}

	if data.IndexVersion == 0 {
		data.IndexVersion = currentIndexVersion
	}
	if data.IndexedAt == "" {
		data.IndexedAt = time.Now().UTC().Format(time.RFC3339)
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

	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode repo index: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, encoded, 0o644); err != nil {
		return fmt.Errorf("write repo index temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename repo index temp file: %w", err)
	}
	return nil
}

func (s *FSIndexStore) repoFilePath(repo string) (string, error) {
	owner, name, err := splitRepoID(repo)
	if err != nil {
		return "", err
	}
	slug := repoSlug(owner, name)
	return filepath.Join(s.basePath, slug+".json"), nil
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

func splitRepoID(repo string) (string, string, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("repo must be owner/name: %q", repo)
	}
	owner := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	if owner == "" || name == "" {
		return "", "", fmt.Errorf("repo must be owner/name: %q", repo)
	}
	return owner, name, nil
}

func repoSlug(owner, name string) string {
	return sanitizeSlugComponent(owner) + "-" + sanitizeSlugComponent(name)
}

func sanitizeSlugComponent(value string) string {
	sanitized := invalidSlugChars.ReplaceAllString(value, "-")
	sanitized = strings.Trim(sanitized, "-")
	if sanitized == "" {
		return "repo"
	}
	return sanitized
}

func normalizeRepoIndex(repo string, index RepoIndex) RepoIndex {
	if index.Repo == "" {
		index.Repo = repo
	}
	if index.IndexedAt == "" {
		index.IndexedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if index.IndexVersion == 0 {
		index.IndexVersion = currentIndexVersion
	}
	if index.Files == nil {
		index.Files = map[string]string{}
	}
	if index.FileBlobSHAs == nil {
		index.FileBlobSHAs = map[string]string{}
	}
	if index.FileMTimes == nil {
		index.FileMTimes = map[string]int64{}
	}
	if index.Symbols == nil {
		index.Symbols = map[string]any{}
	}
	if index.ContextMetadata == nil {
		index.ContextMetadata = map[string]any{}
	}
	if index.Languages == nil {
		index.Languages = map[string]int{}
	}
	return index
}

func fromRepoIndex(index RepoIndex) persistedRepo {
	symbolCount := len(index.Symbols)
	return persistedRepo{
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

func toRepoIndex(data persistedRepo) RepoIndex {
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

func cloneLanguageCounts(in map[string]int) map[string]int {
	if in == nil {
		return map[string]int{}
	}
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneFileHashes(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneFileMTimes(in map[string]int64) map[string]int64 {
	if in == nil {
		return map[string]int64{}
	}
	out := make(map[string]int64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneSymbolMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneContextMetadataMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
