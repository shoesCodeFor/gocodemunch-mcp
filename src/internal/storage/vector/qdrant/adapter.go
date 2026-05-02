package qdrant

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
)

const (
	defaultUpsertBatchSize        = 256
	defaultResponseLimitBytes     = 32 << 20
	defaultCollectionDistance     = "Cosine"
	defaultNamespacePayloadField  = "namespace"
	defaultMetadataPayloadField   = "metadata"
	defaultOriginalIDPayloadField = "record_id"
)

// Adapter implements Qdrant-backed vector storage operations.
type Adapter struct {
	baseURL    string
	apiKey     string
	collection string
	client     *http.Client

	bootstrapMu         sync.Mutex
	bootstrapped        bool
	collectionVectorDim int
}

var _ indexing.VectorBackend = (*Adapter)(nil)

// AdapterOption customizes adapter construction.
type AdapterOption func(*Adapter)

// WithHTTPClient injects a custom HTTP client.
func WithHTTPClient(client *http.Client) AdapterOption {
	return func(adapter *Adapter) {
		if client != nil {
			adapter.client = client
		}
	}
}

type qdrantEnvelope struct {
	Status any             `json:"status"`
	Result json.RawMessage `json:"result"`
	Time   float64         `json:"time"`
}

type qdrantCollectionInfo struct {
	Config struct {
		Params struct {
			Vectors any `json:"vectors"`
		} `json:"params"`
	} `json:"config"`
}

type qdrantPoint struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload,omitempty"`
}

type qdrantSearchRequest struct {
	Vector      []float32    `json:"vector"`
	Limit       int          `json:"limit"`
	WithPayload bool         `json:"with_payload"`
	WithVector  bool         `json:"with_vector"`
	Filter      qdrantFilter `json:"filter"`
}

type qdrantFilter struct {
	Must []qdrantFilterMatch `json:"must,omitempty"`
}

type qdrantFilterMatch struct {
	Key   string            `json:"key"`
	Match qdrantMatchClause `json:"match"`
}

type qdrantMatchClause struct {
	Value any `json:"value"`
}

type qdrantSearchPoint struct {
	ID      any             `json:"id"`
	Score   float64         `json:"score"`
	Payload map[string]any  `json:"payload"`
	Vector  any             `json:"vector"`
	Version json.RawMessage `json:"version"`
}

type qdrantSearchResponse struct {
	Status any                 `json:"status"`
	Result []qdrantSearchPoint `json:"result"`
	Time   float64             `json:"time"`
}

type qdrantPointLookupRequest struct {
	IDs         []string `json:"ids"`
	WithPayload bool     `json:"with_payload"`
	WithVector  bool     `json:"with_vector"`
}

type qdrantPointLookupPoint struct {
	ID      any             `json:"id"`
	Payload map[string]any  `json:"payload"`
	Vector  json.RawMessage `json:"vector"`
}

type qdrantPointLookupResponse struct {
	Status any                      `json:"status"`
	Result []qdrantPointLookupPoint `json:"result"`
	Time   float64                  `json:"time"`
}

type qdrantDeleteByIDsRequest struct {
	Points []string `json:"points"`
}

type qdrantDeleteByFilterRequest struct {
	Filter qdrantFilter `json:"filter"`
}

type qdrantCountRequest struct {
	Filter qdrantFilter `json:"filter,omitempty"`
	Exact  bool         `json:"exact"`
}

type qdrantCountResult struct {
	Count any `json:"count"`
}

type qdrantCountResponse struct {
	Status any               `json:"status"`
	Result qdrantCountResult `json:"result"`
	Time   float64           `json:"time"`
}

type qdrantError struct {
	operation  string
	err        error
	retryable  bool
	statusCode int
}

func (e *qdrantError) Error() string {
	if e == nil {
		return ""
	}
	if e.err == nil {
		return e.operation
	}
	if e.operation == "" {
		return e.err.Error()
	}
	return fmt.Sprintf("%s: %v", e.operation, e.err)
}

func (e *qdrantError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *qdrantError) Retryable() bool {
	if e == nil {
		return false
	}
	return e.retryable
}

func (e *qdrantError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.statusCode
}

func newQdrantError(operation string, err error, retryable bool) error {
	if err == nil {
		return nil
	}
	return &qdrantError{
		operation: operation,
		err:       err,
		retryable: retryable,
	}
}

// NewAdapter builds a Qdrant vector adapter.
func NewAdapter(
	baseURL string,
	apiKey string,
	collection string,
	optionFns ...AdapterOption,
) (*Adapter, error) {
	normalizedBaseURL, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	normalizedCollection := strings.TrimSpace(collection)
	if normalizedCollection == "" {
		return nil, errors.New("qdrant collection is required")
	}

	adapter := &Adapter{
		baseURL:    normalizedBaseURL,
		apiKey:     strings.TrimSpace(apiKey),
		collection: normalizedCollection,
		client:     &http.Client{},
	}
	for _, optionFn := range optionFns {
		if optionFn == nil {
			continue
		}
		optionFn(adapter)
	}
	if adapter.client == nil {
		adapter.client = &http.Client{}
	}

	return adapter, nil
}

// BaseURL returns the normalized Qdrant base URL.
func (a *Adapter) BaseURL() string {
	if a == nil {
		return ""
	}
	return a.baseURL
}

// Collection returns the configured Qdrant collection name.
func (a *Adapter) Collection() string {
	if a == nil {
		return ""
	}
	return a.collection
}

// Close is a no-op for the HTTP-backed adapter.
func (a *Adapter) Close() error {
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
	if a == nil {
		return indexing.VectorUpsertResponse{}, errors.New("upsert vectors: qdrant adapter is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return indexing.VectorUpsertResponse{}, err
	}

	points := make([]qdrantPoint, 0, len(request.Records))
	vectorDimension := 0
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

		if vectorDimension == 0 {
			vectorDimension = len(record.Embedding)
		}
		if len(record.Embedding) != vectorDimension {
			return indexing.VectorUpsertResponse{}, fmt.Errorf(
				"upsert vectors: record %q embedding dimension mismatch: expected %d, got %d",
				id,
				vectorDimension,
				len(record.Embedding),
			)
		}

		storedID := qdrantStoredPointID(id)
		payload := map[string]any{
			defaultNamespacePayloadField: namespace,
			defaultMetadataPayloadField:  normalizeMetadata(record.Metadata),
		}
		if storedID != id {
			payload[defaultOriginalIDPayloadField] = id
		}

		points = append(points, qdrantPoint{
			ID:      storedID,
			Vector:  cloneEmbedding(record.Embedding),
			Payload: payload,
		})
	}

	if err := a.ensureCollection(ctx, vectorDimension); err != nil {
		return indexing.VectorUpsertResponse{}, fmt.Errorf("upsert vectors: %w", err)
	}

	upserted := 0
	for start := 0; start < len(points); start += defaultUpsertBatchSize {
		end := start + defaultUpsertBatchSize
		if end > len(points) {
			end = len(points)
		}
		batch := points[start:end]

		requestBody := map[string]any{
			"points": batch,
		}

		statusCode, responseBody, err := a.doJSONRequest(
			ctx,
			http.MethodPut,
			a.collectionEndpoint("points")+"?wait=true",
			requestBody,
		)
		if err != nil {
			return indexing.VectorUpsertResponse{}, fmt.Errorf("upsert vectors: %w", err)
		}
		if err := ensureQdrantSuccess(statusCode, responseBody); err != nil {
			return indexing.VectorUpsertResponse{}, fmt.Errorf(
				"upsert vectors: persist batch starting at index %d: %w",
				start,
				err,
			)
		}
		upserted += len(batch)
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
	if a == nil {
		return indexing.VectorQueryResponse{}, errors.New("query vectors: qdrant adapter is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return indexing.VectorQueryResponse{}, err
	}

	if err := a.ensureCollection(ctx, len(request.Embedding)); err != nil {
		return indexing.VectorQueryResponse{}, fmt.Errorf("query vectors: %w", err)
	}

	searchRequest := qdrantSearchRequest{
		Vector:      cloneEmbedding(request.Embedding),
		Limit:       request.TopK,
		WithPayload: true,
		WithVector:  true,
		Filter:      namespaceFilter(namespace),
	}

	statusCode, responseBody, err := a.doJSONRequest(
		ctx,
		http.MethodPost,
		a.collectionEndpoint("points", "search"),
		searchRequest,
	)
	if err != nil {
		return indexing.VectorQueryResponse{}, fmt.Errorf("query vectors: %w", err)
	}
	if err := ensureQdrantSuccess(statusCode, responseBody); err != nil {
		return indexing.VectorQueryResponse{}, fmt.Errorf("query vectors: execute search: %w", err)
	}

	searchResponse := qdrantSearchResponse{}
	if err := json.Unmarshal(responseBody, &searchResponse); err != nil {
		return indexing.VectorQueryResponse{}, fmt.Errorf("query vectors: decode search response: %w", err)
	}
	if !qdrantStatusOK(searchResponse.Status) {
		return indexing.VectorQueryResponse{}, fmt.Errorf(
			"query vectors: execute search: %s",
			qdrantStatusMessage(searchResponse.Status),
		)
	}

	matches := make([]indexing.VectorQueryMatch, 0, len(searchResponse.Result))
	for _, point := range searchResponse.Result {
		id, err := normalizePointID(point.ID)
		if err != nil {
			return indexing.VectorQueryResponse{}, fmt.Errorf("query vectors: %w", err)
		}
		embedding, err := decodePointVector(point.Vector)
		if err != nil {
			return indexing.VectorQueryResponse{}, fmt.Errorf(
				"query vectors: decode embedding for record %q: %w",
				id,
				err,
			)
		}
		if len(embedding) != len(request.Embedding) {
			return indexing.VectorQueryResponse{}, fmt.Errorf(
				"query vectors: embedding dimension mismatch for record %q: expected %d, got %d",
				id,
				len(request.Embedding),
				len(embedding),
			)
		}

		metadata := decodeMetadataFromPayload(point.Payload)
		recordNamespace := namespace
		if payloadNamespace := strings.TrimSpace(readStringValue(point.Payload, defaultNamespacePayloadField)); payloadNamespace != "" {
			recordNamespace = payloadNamespace
		}
		if originalID := strings.TrimSpace(readStringValue(point.Payload, defaultOriginalIDPayloadField)); originalID != "" {
			id = originalID
		}

		score := point.Score
		if math.IsNaN(score) || math.IsInf(score, 0) {
			return indexing.VectorQueryResponse{}, fmt.Errorf(
				"query vectors: non-finite score for record %q",
				id,
			)
		}

		matches = append(matches, indexing.VectorQueryMatch{
			Record: indexing.VectorRecord{
				ID:        id,
				Namespace: recordNamespace,
				Embedding: embedding,
				Metadata:  metadata,
			},
			Score:    score,
			RawScore: score,
		})
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
	if a == nil {
		return indexing.VectorDeleteResponse{}, errors.New("delete vectors: qdrant adapter is nil")
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
	lookupIDs := normalizeStoredPointIDs(ids)

	_, exists, err := a.readCollectionVectorDimension(ctx)
	if err != nil {
		return indexing.VectorDeleteResponse{}, fmt.Errorf("delete vectors: %w", err)
	}
	if !exists {
		return indexing.VectorDeleteResponse{Deleted: 0}, nil
	}

	lookupRequest := qdrantPointLookupRequest{
		IDs:         lookupIDs,
		WithPayload: true,
		WithVector:  false,
	}
	statusCode, responseBody, err := a.doJSONRequest(
		ctx,
		http.MethodPost,
		a.collectionEndpoint("points"),
		lookupRequest,
	)
	if err != nil {
		return indexing.VectorDeleteResponse{}, fmt.Errorf("delete vectors: %w", err)
	}
	if statusCode == http.StatusNotFound {
		return indexing.VectorDeleteResponse{Deleted: 0}, nil
	}
	if err := ensureQdrantSuccess(statusCode, responseBody); err != nil {
		return indexing.VectorDeleteResponse{}, fmt.Errorf(
			"delete vectors: load records for namespace filter: %w",
			err,
		)
	}

	lookupResponse := qdrantPointLookupResponse{}
	if err := json.Unmarshal(responseBody, &lookupResponse); err != nil {
		return indexing.VectorDeleteResponse{}, fmt.Errorf(
			"delete vectors: decode point lookup response: %w",
			err,
		)
	}
	if !qdrantStatusOK(lookupResponse.Status) {
		return indexing.VectorDeleteResponse{}, fmt.Errorf(
			"delete vectors: load records for namespace filter: %s",
			qdrantStatusMessage(lookupResponse.Status),
		)
	}

	filteredIDs := make([]string, 0, len(lookupResponse.Result))
	seen := make(map[string]struct{}, len(lookupResponse.Result))
	for _, point := range lookupResponse.Result {
		id, err := normalizePointID(point.ID)
		if err != nil {
			return indexing.VectorDeleteResponse{}, fmt.Errorf("delete vectors: %w", err)
		}
		if recordNamespace := strings.TrimSpace(readStringValue(point.Payload, defaultNamespacePayloadField)); recordNamespace != namespace {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		filteredIDs = append(filteredIDs, id)
	}
	if len(filteredIDs) == 0 {
		return indexing.VectorDeleteResponse{Deleted: 0}, nil
	}
	sort.Strings(filteredIDs)

	statusCode, responseBody, err = a.doJSONRequest(
		ctx,
		http.MethodPost,
		a.collectionEndpoint("points", "delete")+"?wait=true",
		qdrantDeleteByIDsRequest{Points: filteredIDs},
	)
	if err != nil {
		return indexing.VectorDeleteResponse{}, fmt.Errorf("delete vectors: %w", err)
	}
	if statusCode == http.StatusNotFound {
		return indexing.VectorDeleteResponse{Deleted: 0}, nil
	}
	if err := ensureQdrantSuccess(statusCode, responseBody); err != nil {
		return indexing.VectorDeleteResponse{}, fmt.Errorf("delete vectors: execute delete request: %w", err)
	}

	return indexing.VectorDeleteResponse{Deleted: len(filteredIDs)}, nil
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
	if a == nil {
		return indexing.VectorDeleteNamespaceResponse{}, errors.New(
			"delete vector namespace: qdrant adapter is nil",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return indexing.VectorDeleteNamespaceResponse{}, err
	}

	_, exists, err := a.readCollectionVectorDimension(ctx)
	if err != nil {
		return indexing.VectorDeleteNamespaceResponse{}, fmt.Errorf(
			"delete vector namespace: %w",
			err,
		)
	}
	if !exists {
		return indexing.VectorDeleteNamespaceResponse{Deleted: 0}, nil
	}

	deleted, err := a.countNamespacePoints(ctx, namespace)
	if err != nil {
		return indexing.VectorDeleteNamespaceResponse{}, fmt.Errorf(
			"delete vector namespace: %w",
			err,
		)
	}
	if deleted == 0 {
		return indexing.VectorDeleteNamespaceResponse{Deleted: 0}, nil
	}

	statusCode, responseBody, err := a.doJSONRequest(
		ctx,
		http.MethodPost,
		a.collectionEndpoint("points", "delete")+"?wait=true",
		qdrantDeleteByFilterRequest{
			Filter: namespaceFilter(namespace),
		},
	)
	if err != nil {
		return indexing.VectorDeleteNamespaceResponse{}, fmt.Errorf("delete vector namespace: %w", err)
	}
	if statusCode == http.StatusNotFound {
		return indexing.VectorDeleteNamespaceResponse{Deleted: 0}, nil
	}
	if err := ensureQdrantSuccess(statusCode, responseBody); err != nil {
		return indexing.VectorDeleteNamespaceResponse{}, fmt.Errorf(
			"delete vector namespace: execute namespace delete: %w",
			err,
		)
	}

	return indexing.VectorDeleteNamespaceResponse{Deleted: deleted}, nil
}

// Health reports backend readiness and diagnostics.
func (a *Adapter) Health(ctx context.Context) (indexing.VectorHealthResponse, error) {
	if a == nil {
		return indexing.VectorHealthResponse{
			Ready:   false,
			Message: "qdrant adapter is nil",
		}, errors.New("qdrant adapter is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return indexing.VectorHealthResponse{}, err
	}

	metadata := map[string]any{
		"backend":    "qdrant",
		"base_url":   a.baseURL,
		"collection": a.collection,
	}

	statusCode, responseBody, err := a.doJSONRequest(ctx, http.MethodGet, "/collections", nil)
	if err != nil {
		return indexing.VectorHealthResponse{
			Ready:    false,
			Message:  "qdrant api ping failed",
			Metadata: metadata,
		}, fmt.Errorf("vector health check: qdrant api ping failed: %w", err)
	}
	if err := ensureQdrantSuccess(statusCode, responseBody); err != nil {
		return indexing.VectorHealthResponse{
			Ready:    false,
			Message:  "qdrant api ping failed",
			Metadata: metadata,
		}, fmt.Errorf("vector health check: qdrant api ping failed: %w", err)
	}

	vectorDimension, collectionExists, err := a.readCollectionVectorDimension(ctx)
	if err != nil {
		return indexing.VectorHealthResponse{
			Ready:    false,
			Message:  "qdrant collection check failed",
			Metadata: metadata,
		}, fmt.Errorf("vector health check: qdrant collection check failed: %w", err)
	}

	metadata["collection_exists"] = collectionExists
	if collectionExists {
		metadata["vector_dimension"] = vectorDimension
	}

	return indexing.VectorHealthResponse{
		Ready:    true,
		Message:  "ok",
		Metadata: metadata,
	}, nil
}

func (a *Adapter) ensureCollection(ctx context.Context, vectorDimension int) error {
	if vectorDimension <= 0 {
		return errors.New("vector dimension must be positive")
	}
	if a == nil {
		return errors.New("qdrant adapter is nil")
	}

	a.bootstrapMu.Lock()
	defer a.bootstrapMu.Unlock()

	if a.bootstrapped {
		if a.collectionVectorDim != vectorDimension {
			return fmt.Errorf(
				"qdrant collection %q embedding dimension mismatch: expected %d, got %d",
				a.collection,
				a.collectionVectorDim,
				vectorDimension,
			)
		}
		return nil
	}

	currentVectorDim, exists, err := a.readCollectionVectorDimension(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap collection %q: %w", a.collection, err)
	}

	if !exists {
		if err := a.createCollection(ctx, vectorDimension); err != nil {
			return fmt.Errorf("bootstrap collection %q: %w", a.collection, err)
		}
		currentVectorDim = vectorDimension
	}

	if currentVectorDim != vectorDimension {
		return fmt.Errorf(
			"qdrant collection %q embedding dimension mismatch: expected %d, got %d",
			a.collection,
			currentVectorDim,
			vectorDimension,
		)
	}

	a.collectionVectorDim = currentVectorDim
	a.bootstrapped = true
	return nil
}

func (a *Adapter) createCollection(ctx context.Context, vectorDimension int) error {
	requestBody := map[string]any{
		"vectors": map[string]any{
			"size":     vectorDimension,
			"distance": defaultCollectionDistance,
		},
	}

	statusCode, responseBody, err := a.doJSONRequest(
		ctx,
		http.MethodPut,
		a.collectionEndpoint(),
		requestBody,
	)
	if err != nil {
		return err
	}
	if err := ensureQdrantSuccess(statusCode, responseBody); err != nil {
		return fmt.Errorf("create collection: %w", err)
	}
	return nil
}

func (a *Adapter) readCollectionVectorDimension(ctx context.Context) (int, bool, error) {
	statusCode, responseBody, err := a.doJSONRequest(ctx, http.MethodGet, a.collectionEndpoint(), nil)
	if err != nil {
		return 0, false, err
	}
	if statusCode == http.StatusNotFound {
		return 0, false, nil
	}
	if err := ensureQdrantSuccess(statusCode, responseBody); err != nil {
		return 0, false, fmt.Errorf("read collection: %w", err)
	}

	envelope := qdrantEnvelope{}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return 0, false, fmt.Errorf("decode collection response: %w", err)
	}

	collectionInfo := qdrantCollectionInfo{}
	if err := json.Unmarshal(envelope.Result, &collectionInfo); err != nil {
		return 0, false, fmt.Errorf("decode collection metadata: %w", err)
	}

	vectorDimension, err := decodeVectorDimension(collectionInfo.Config.Params.Vectors)
	if err != nil {
		return 0, false, fmt.Errorf("decode collection vector configuration: %w", err)
	}
	return vectorDimension, true, nil
}

func (a *Adapter) countNamespacePoints(ctx context.Context, namespace string) (int, error) {
	statusCode, responseBody, err := a.doJSONRequest(
		ctx,
		http.MethodPost,
		a.collectionEndpoint("points", "count"),
		qdrantCountRequest{
			Filter: namespaceFilter(namespace),
			Exact:  true,
		},
	)
	if err != nil {
		return 0, err
	}
	if statusCode == http.StatusNotFound {
		return 0, nil
	}
	if err := ensureQdrantSuccess(statusCode, responseBody); err != nil {
		return 0, fmt.Errorf("count namespace points: %w", err)
	}

	countResponse := qdrantCountResponse{}
	if err := json.Unmarshal(responseBody, &countResponse); err != nil {
		return 0, fmt.Errorf("count namespace points: decode count response: %w", err)
	}
	if !qdrantStatusOK(countResponse.Status) {
		return 0, fmt.Errorf(
			"count namespace points: qdrant response status not ok: %s",
			qdrantStatusMessage(countResponse.Status),
		)
	}

	count, err := decodeNonNegativeInt(countResponse.Result.Count)
	if err != nil {
		return 0, fmt.Errorf("count namespace points: decode count: %w", err)
	}
	return count, nil
}

func (a *Adapter) doJSONRequest(
	ctx context.Context,
	method string,
	endpoint string,
	requestBody any,
) (int, []byte, error) {
	if a == nil {
		return 0, nil, errors.New("qdrant adapter is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}

	var bodyReader io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return 0, nil, newQdrantError("encode request body", err, false)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	endpointURL := a.resolveEndpoint(endpoint)
	request, err := http.NewRequestWithContext(ctx, method, endpointURL, bodyReader)
	if err != nil {
		return 0, nil, newQdrantError("build request", err, false)
	}
	request.Header.Set("Accept", "application/json")
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if a.apiKey != "" {
		request.Header.Set("api-key", a.apiKey)
	}

	response, err := a.client.Do(request)
	if err != nil {
		return 0, nil, newQdrantError("execute request", err, isRetryableTransportError(err))
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, defaultResponseLimitBytes))
	if err != nil {
		return response.StatusCode, nil, newQdrantError(
			"read response body",
			err,
			isRetryableTransportError(err),
		)
	}

	return response.StatusCode, responseBody, nil
}

func (a *Adapter) resolveEndpoint(endpoint string) string {
	base := strings.TrimRight(a.baseURL, "/")
	trimmedEndpoint := strings.TrimSpace(endpoint)
	if trimmedEndpoint == "" {
		return base
	}
	if strings.HasPrefix(trimmedEndpoint, "http://") || strings.HasPrefix(trimmedEndpoint, "https://") {
		return trimmedEndpoint
	}
	if strings.HasPrefix(trimmedEndpoint, "/") {
		return base + trimmedEndpoint
	}
	return base + "/" + trimmedEndpoint
}

func (a *Adapter) collectionEndpoint(parts ...string) string {
	segments := []string{"/collections", url.PathEscape(a.collection)}
	for _, part := range parts {
		trimmed := strings.Trim(part, "/")
		if trimmed == "" {
			continue
		}
		segments = append(segments, url.PathEscape(trimmed))
	}
	return strings.Join(segments, "/")
}

func namespaceFilter(namespace string) qdrantFilter {
	return qdrantFilter{
		Must: []qdrantFilterMatch{
			{
				Key: defaultNamespacePayloadField,
				Match: qdrantMatchClause{
					Value: namespace,
				},
			},
		},
	}
}

func ensureQdrantSuccess(statusCode int, responseBody []byte) error {
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return &qdrantError{
			err: fmt.Errorf(
				"qdrant returned status %d: %s",
				statusCode,
				summarizeQdrantStatusBody(responseBody),
			),
			retryable:  isRetryableHTTPStatus(statusCode),
			statusCode: statusCode,
		}
	}

	envelope := qdrantEnvelope{}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return newQdrantError("decode qdrant response", err, false)
	}
	if !qdrantStatusOK(envelope.Status) {
		return newQdrantError(
			"qdrant response status not ok",
			errors.New(qdrantStatusMessage(envelope.Status)),
			false,
		)
	}

	return nil
}

func summarizeQdrantStatusBody(responseBody []byte) string {
	envelope := qdrantEnvelope{}
	if err := json.Unmarshal(responseBody, &envelope); err == nil {
		if message := strings.TrimSpace(qdrantStatusMessage(envelope.Status)); message != "" &&
			!strings.EqualFold(message, "ok") {
			return message
		}
	}
	return summarizeResponseBody(responseBody)
}

func qdrantStatusOK(status any) bool {
	switch typed := status.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "ok")
	case map[string]any:
		errorText := strings.TrimSpace(readStringValue(typed, "error"))
		return errorText == ""
	default:
		return false
	}
}

func qdrantStatusMessage(status any) string {
	switch typed := status.(type) {
	case string:
		message := strings.TrimSpace(typed)
		if message == "" {
			return "unknown status"
		}
		return message
	case map[string]any:
		if errorText := strings.TrimSpace(readStringValue(typed, "error")); errorText != "" {
			return errorText
		}
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "unrecognized status payload"
		}
		return string(encoded)
	default:
		if status == nil {
			return "missing status"
		}
		return fmt.Sprintf("%v", status)
	}
}

func summarizeResponseBody(responseBody []byte) string {
	trimmed := strings.TrimSpace(string(responseBody))
	if trimmed == "" {
		return "<empty response>"
	}
	if len(trimmed) <= 300 {
		return trimmed
	}
	return trimmed[:300] + "...(truncated)"
}

func decodeVectorDimension(raw any) (int, error) {
	switch typed := raw.(type) {
	case map[string]any:
		if sizeValue, ok := typed["size"]; ok {
			return decodePositiveInt(sizeValue)
		}
		if len(typed) == 0 {
			return 0, errors.New("vectors configuration was empty")
		}

		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		firstVector := typed[names[0]]

		vectorConfig, ok := firstVector.(map[string]any)
		if !ok {
			return 0, errors.New("named vector configuration had unexpected shape")
		}
		sizeValue, ok := vectorConfig["size"]
		if !ok {
			return 0, errors.New("named vector size was missing")
		}
		return decodePositiveInt(sizeValue)
	default:
		return 0, fmt.Errorf("unsupported vectors configuration type %T", raw)
	}
}

func decodePositiveInt(raw any) (int, error) {
	switch typed := raw.(type) {
	case int:
		if typed <= 0 {
			return 0, fmt.Errorf("value must be positive (got %d)", typed)
		}
		return typed, nil
	case int64:
		if typed <= 0 {
			return 0, fmt.Errorf("value must be positive (got %d)", typed)
		}
		return int(typed), nil
	case float64:
		if typed <= 0 {
			return 0, fmt.Errorf("value must be positive (got %f)", typed)
		}
		if math.Trunc(typed) != typed {
			return 0, fmt.Errorf("value must be an integer (got %f)", typed)
		}
		return int(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("parse integer: %w", err)
		}
		if parsed <= 0 {
			return 0, fmt.Errorf("value must be positive (got %d)", parsed)
		}
		return int(parsed), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, fmt.Errorf("parse integer: %w", err)
		}
		if parsed <= 0 {
			return 0, fmt.Errorf("value must be positive (got %d)", parsed)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported integer type %T", raw)
	}
}

func decodeNonNegativeInt(raw any) (int, error) {
	switch typed := raw.(type) {
	case int:
		if typed < 0 {
			return 0, fmt.Errorf("value must be non-negative (got %d)", typed)
		}
		return typed, nil
	case int64:
		if typed < 0 {
			return 0, fmt.Errorf("value must be non-negative (got %d)", typed)
		}
		return int(typed), nil
	case float64:
		if typed < 0 {
			return 0, fmt.Errorf("value must be non-negative (got %f)", typed)
		}
		if math.Trunc(typed) != typed {
			return 0, fmt.Errorf("value must be an integer (got %f)", typed)
		}
		return int(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("parse integer: %w", err)
		}
		if parsed < 0 {
			return 0, fmt.Errorf("value must be non-negative (got %d)", parsed)
		}
		return int(parsed), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, fmt.Errorf("parse integer: %w", err)
		}
		if parsed < 0 {
			return 0, fmt.Errorf("value must be non-negative (got %d)", parsed)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported integer type %T", raw)
	}
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
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	sort.Strings(normalized)
	return normalized
}

func normalizeStoredPointIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(ids))
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		storedID := qdrantStoredPointID(id)
		if storedID == "" {
			continue
		}
		if _, ok := seen[storedID]; ok {
			continue
		}
		seen[storedID] = struct{}{}
		normalized = append(normalized, storedID)
	}
	sort.Strings(normalized)
	return normalized
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

func cloneEmbedding(source []float32) []float32 {
	if len(source) == 0 {
		return []float32{}
	}
	target := make([]float32, len(source))
	copy(target, source)
	return target
}

func normalizeMetadata(metadata indexing.VectorMetadata) indexing.VectorMetadata {
	normalized := metadata
	if normalized.Fields == nil {
		normalized.Fields = map[string]any{}
	}
	return normalized
}

func readStringValue(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	raw, ok := values[key]
	if !ok {
		return ""
	}
	text, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func decodeMetadataFromPayload(payload map[string]any) indexing.VectorMetadata {
	empty := indexing.VectorMetadata{Fields: map[string]any{}}
	if len(payload) == 0 {
		return empty
	}

	rawMetadata, ok := payload[defaultMetadataPayloadField]
	if !ok {
		return empty
	}

	encodedMetadata, err := json.Marshal(rawMetadata)
	if err != nil {
		return empty
	}

	metadata := indexing.VectorMetadata{}
	if err := json.Unmarshal(encodedMetadata, &metadata); err != nil {
		return empty
	}
	if metadata.Fields == nil {
		metadata.Fields = map[string]any{}
	}
	return metadata
}

func normalizePointID(raw any) (string, error) {
	switch typed := raw.(type) {
	case string:
		id := strings.TrimSpace(typed)
		if id == "" {
			return "", errors.New("query vectors: record id was empty")
		}
		return id, nil
	case float64:
		if math.Trunc(typed) == typed {
			return strconv.FormatInt(int64(typed), 10), nil
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case json.Number:
		return typed.String(), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	default:
		return "", fmt.Errorf("query vectors: unsupported record id type %T", raw)
	}
}

func qdrantStoredPointID(raw string) string {
	id := strings.TrimSpace(raw)
	if id == "" {
		return ""
	}
	if _, err := strconv.ParseUint(id, 10, 64); err == nil {
		return id
	}
	if isUUIDLike(id) {
		return strings.ToLower(id)
	}

	sum := sha1.Sum([]byte(id))
	uuid := sum[:16]

	// RFC 4122 variant/version bits for deterministic UUID-like ids.
	uuid[6] = (uuid[6] & 0x0f) | 0x50
	uuid[8] = (uuid[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

func isUUIDLike(raw string) bool {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(raw)), "-")
	if len(parts) != 5 {
		return false
	}
	expectedLengths := []int{8, 4, 4, 4, 12}
	for index, part := range parts {
		if len(part) != expectedLengths[index] {
			return false
		}
		for _, ch := range part {
			switch {
			case ch >= '0' && ch <= '9':
			case ch >= 'a' && ch <= 'f':
			default:
				return false
			}
		}
	}
	return true
}

func decodePointVector(raw any) ([]float32, error) {
	if raw == nil {
		return nil, errors.New("vector was missing in query response")
	}

	switch typed := raw.(type) {
	case []any:
		embedding := make([]float32, 0, len(typed))
		for index, value := range typed {
			parsed, err := decodeFloat32(value)
			if err != nil {
				return nil, fmt.Errorf("invalid vector value at index %d: %w", index, err)
			}
			embedding = append(embedding, parsed)
		}
		if err := validateEmbedding(embedding); err != nil {
			return nil, err
		}
		return embedding, nil
	case map[string]any:
		if len(typed) == 0 {
			return nil, errors.New("named vector payload was empty")
		}
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		return decodePointVector(typed[names[0]])
	default:
		return nil, fmt.Errorf("unsupported vector payload type %T", raw)
	}
}

func decodeFloat32(raw any) (float32, error) {
	switch typed := raw.(type) {
	case float32:
		value := float64(typed)
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, errors.New("value must be finite")
		}
		return typed, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, errors.New("value must be finite")
		}
		return float32(typed), nil
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, err
		}
		if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, errors.New("value must be finite")
		}
		return float32(parsed), nil
	case int:
		return float32(typed), nil
	case int64:
		return float32(typed), nil
	default:
		return 0, fmt.Errorf("unsupported numeric type %T", raw)
	}
}

func isRetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
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

func isRetryableHTTPStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	default:
		return statusCode >= http.StatusInternalServerError
	}
}

func normalizeBaseURL(rawBaseURL string) (string, error) {
	trimmed := strings.TrimSpace(rawBaseURL)
	if trimmed == "" {
		return "", errors.New("qdrant base url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse qdrant base url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("qdrant base url must be absolute: %q", trimmed)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("qdrant base url must use http or https: %q", trimmed)
	}

	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}
