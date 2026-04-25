package indexing

import "context"

// Symbol represents a parsed symbol in a source file.
type Symbol struct {
	ID        string
	Name      string
	Kind      string
	FilePath  string
	StartLine int
	EndLine   int
}

// ParserExtractor owns parse and import extraction behavior for source files.
type ParserExtractor interface {
	ParseFile(ctx context.Context, filePath string, content []byte) ([]Symbol, error)
	ExtractImports(ctx context.Context, filePath string, content []byte) ([]string, error)
	SupportsLanguage(language string) bool
}

// SummarizerProvider abstracts optional symbol summarization providers.
type SummarizerProvider interface {
	SummarizeSymbols(ctx context.Context, symbols []Symbol) (map[string]string, error)
	Capabilities(ctx context.Context) map[string]bool
}

// RemoteFileMetadata carries remote source-path metadata used by incremental indexing.
type RemoteFileMetadata struct {
	BlobSHA   string
	SizeBytes int64
}

// RepoTreeMetadata captures remote tree-level metadata used for no-download diffs.
type RepoTreeMetadata struct {
	TreeSHA string
	Files   map[string]RemoteFileMetadata
}

// RepoAcquirer abstracts remote repository acquisition for index operations.
type RepoAcquirer interface {
	AcquireTree(ctx context.Context, repoURL string) (map[string][]byte, error)
	AcquireTreeSubset(ctx context.Context, repoURL string, relPaths []string) (map[string][]byte, error)
	AcquireTreeMetadata(ctx context.Context, repoURL string) (RepoTreeMetadata, error)
	ReadGitignore(ctx context.Context, repoURL string) ([]byte, error)
}
