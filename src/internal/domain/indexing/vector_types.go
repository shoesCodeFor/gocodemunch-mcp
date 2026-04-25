package indexing

// VectorMetadata captures shared source metadata persisted with each vector row.
// Fields can carry backend/provider-specific values when needed.
type VectorMetadata struct {
	Repo      string
	Path      string
	Language  string
	ChunkID   string
	ChunkText string
	StartLine int
	EndLine   int
	Fields    map[string]any
}

// VectorRecord is the backend-agnostic representation of a stored vector entry.
type VectorRecord struct {
	ID        string
	Namespace string
	Embedding []float32
	Metadata  VectorMetadata
}

// VectorUpsertRequest batches vector records for create/update operations.
type VectorUpsertRequest struct {
	Namespace string
	Records   []VectorRecord
}

// VectorUpsertResponse reports how many records were persisted.
type VectorUpsertResponse struct {
	Upserted int
}

// VectorQueryRequest captures retrieval inputs shared by all vector backends.
type VectorQueryRequest struct {
	Namespace string
	Embedding []float32
	TopK      int
}

// VectorQueryMatch captures a retrieved vector row and backend scoring details.
type VectorQueryMatch struct {
	Record   VectorRecord
	Score    float64
	RawScore float64
}

// VectorQueryResponse returns ranked matches for a query.
type VectorQueryResponse struct {
	Matches []VectorQueryMatch
}

// VectorDeleteRequest removes explicit vector IDs from a namespace.
type VectorDeleteRequest struct {
	Namespace string
	IDs       []string
}

// VectorDeleteResponse reports how many records were deleted.
type VectorDeleteResponse struct {
	Deleted int
}

// VectorDeleteNamespaceRequest deletes all vectors in a namespace.
type VectorDeleteNamespaceRequest struct {
	Namespace string
}

// VectorDeleteNamespaceResponse reports namespace delete cardinality.
type VectorDeleteNamespaceResponse struct {
	Deleted int
}

// VectorHealthResponse captures backend health status and diagnostics.
type VectorHealthResponse struct {
	Ready    bool
	Message  string
	Metadata map[string]any
}
