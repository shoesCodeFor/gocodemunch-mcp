package storage

import (
	"context"
	"errors"
)

var (
	// ErrRepoNotFound indicates a requested repo has not been indexed yet.
	ErrRepoNotFound = errors.New("repo not found")
)

// ChangeSet captures incremental index changes.
type ChangeSet struct {
	Changed []string
	New     []string
	Deleted []string
}

// RepoIndex is the storage-facing representation of an indexed repository.
type RepoIndex struct {
	Repo            string
	IndexedAt       string
	SourceRoot      string
	DisplayName     string
	Languages       map[string]int
	ContextMetadata map[string]any
	IndexVersion    int
	GitHead         string
	Files           map[string]string
	FileBlobSHAs    map[string]string
	FileMTimes      map[string]int64
	Symbols         map[string]any
}

// RepoMetadata captures lightweight repo listing data.
type RepoMetadata struct {
	Repo         string
	IndexedAt    string
	SymbolCount  int
	FileCount    int
	Languages    map[string]int
	IndexVersion int
	GitHead      string
	DisplayName  string
	SourceRoot   string
	Indexed      bool
}

// IndexStore provides persisted index lifecycle operations.
type IndexStore interface {
	Load(ctx context.Context, repo string) (RepoIndex, error)
	Save(ctx context.Context, repo string, index RepoIndex) error
	IncrementalSave(ctx context.Context, repo string, index RepoIndex, changes ChangeSet) error
	Delete(ctx context.Context, repo string) error
	List(ctx context.Context) ([]RepoMetadata, error)
	DetectChanges(ctx context.Context, repo string, candidates []string) (ChangeSet, error)
}

// ContentCache provides content retrieval and safe path resolution.
type ContentCache interface {
	GetFileContent(ctx context.Context, repo, filePath string) (string, error)
	GetSymbolBytes(ctx context.Context, repo, symbolID string) ([]byte, error)
	ResolveSafePath(ctx context.Context, repo, filePath string) (string, error)
}
