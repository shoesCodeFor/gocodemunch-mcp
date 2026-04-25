package indexing

import (
	"context"
	"errors"
)

// VectorUpserter persists vector records for a namespace.
type VectorUpserter interface {
	Upsert(ctx context.Context, request VectorUpsertRequest) (VectorUpsertResponse, error)
}

// VectorQuerier executes nearest-neighbor lookups for a query embedding.
type VectorQuerier interface {
	Query(ctx context.Context, request VectorQueryRequest) (VectorQueryResponse, error)
}

// VectorDeleter removes explicit vector IDs from a namespace.
type VectorDeleter interface {
	Delete(ctx context.Context, request VectorDeleteRequest) (VectorDeleteResponse, error)
}

// VectorNamespaceDeleter removes all vector records for a namespace.
type VectorNamespaceDeleter interface {
	DeleteNamespace(
		ctx context.Context,
		request VectorDeleteNamespaceRequest,
	) (VectorDeleteNamespaceResponse, error)
}

// VectorHealthChecker reports backend readiness and diagnostics.
type VectorHealthChecker interface {
	Health(ctx context.Context) (VectorHealthResponse, error)
}

// VectorBackend captures full vector storage contract parity across backends.
type VectorBackend interface {
	VectorUpserter
	VectorQuerier
	VectorDeleter
	VectorNamespaceDeleter
	VectorHealthChecker
}

// Embedder generates embeddings for one or more text inputs.
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

// RetryableError allows backend/provider errors to classify retryability.
type RetryableError interface {
	Retryable() bool
}

// IsRetryableVectorError reports whether a vector/embedding error should be retried.
func IsRetryableVectorError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}

	var classified RetryableError
	if errors.As(err, &classified) {
		return classified.Retryable()
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var timeoutErr interface{ Timeout() bool }
	if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
		return true
	}

	var temporaryErr interface{ Temporary() bool }
	if errors.As(err, &temporaryErr) && temporaryErr.Temporary() {
		return true
	}

	return false
}
