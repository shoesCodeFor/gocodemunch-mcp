package vector_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
	vectorqdrant "github.com/jgravelle/gocodemunch-mcp/src/internal/storage/vector/qdrant"
	vectorsqlite "github.com/jgravelle/gocodemunch-mcp/src/internal/storage/vector/sqlite"
)

type backendFactory struct {
	name string
	new  func(t *testing.T) indexing.VectorBackend
}

var vectorBackendFactories = []backendFactory{
	{
		name: "sqlite",
		new:  newSQLiteVectorBackend,
	},
	{
		name: "qdrant",
		new:  newQdrantVectorBackend,
	},
}

func TestVectorBackendContractParityCRUDLifecycle(t *testing.T) {
	for _, backendFactory := range vectorBackendFactories {
		backendFactory := backendFactory
		t.Run(backendFactory.name, func(t *testing.T) {
			backend := backendFactory.new(t)
			runCRUDLifecycleContractSuite(t, backend)
		})
	}
}

func TestVectorBackendContractParityValidation(t *testing.T) {
	for _, backendFactory := range vectorBackendFactories {
		backendFactory := backendFactory
		t.Run(backendFactory.name, func(t *testing.T) {
			backend := backendFactory.new(t)
			runValidationContractSuite(t, backend)
		})
	}
}

func newSQLiteVectorBackend(t *testing.T) indexing.VectorBackend {
	t.Helper()

	adapter, err := vectorsqlite.NewAdapter(t.TempDir())
	if err != nil {
		t.Fatalf("create sqlite vector adapter: %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(); err != nil {
			t.Errorf("close sqlite vector adapter: %v", err)
		}
	})

	return adapter
}

func newQdrantVectorBackend(t *testing.T) indexing.VectorBackend {
	t.Helper()

	const collectionName = "contract-parity-vectors"

	fakeQdrant := newFakeQdrantServer(t, collectionName)
	adapter, err := vectorqdrant.NewAdapter(fakeQdrant.baseURL(), "", collectionName)
	if err != nil {
		t.Fatalf("create qdrant vector adapter: %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(); err != nil {
			t.Errorf("close qdrant vector adapter: %v", err)
		}
	})

	return adapter
}

func runCRUDLifecycleContractSuite(t *testing.T, backend indexing.VectorBackend) {
	t.Helper()

	ctx := context.Background()

	const (
		primaryNamespace   = "repo/main"
		secondaryNamespace = "repo/secondary"
	)

	_, err := backend.DeleteNamespace(
		ctx,
		indexing.VectorDeleteNamespaceRequest{Namespace: primaryNamespace},
	)
	if err != nil {
		t.Fatalf("reset primary namespace: %v", err)
	}
	_, err = backend.DeleteNamespace(
		ctx,
		indexing.VectorDeleteNamespaceRequest{Namespace: secondaryNamespace},
	)
	if err != nil {
		t.Fatalf("reset secondary namespace: %v", err)
	}

	upsertPrimaryResponse, err := backend.Upsert(ctx, indexing.VectorUpsertRequest{
		Namespace: primaryNamespace,
		Records: []indexing.VectorRecord{
			{
				ID:        "id-c",
				Namespace: primaryNamespace,
				Embedding: []float32{1, 0},
				Metadata: indexing.VectorMetadata{
					Repo:      "repo-c",
					Path:      "z.go",
					Language:  "go",
					ChunkID:   "chunk-z",
					ChunkText: "func zed() {}",
					StartLine: 1,
					EndLine:   4,
				},
			},
			{
				ID:        "id-a",
				Namespace: primaryNamespace,
				Embedding: []float32{1, 0},
				Metadata: indexing.VectorMetadata{
					Repo:      "repo-a",
					Path:      "x.go",
					Language:  "go",
					ChunkID:   "chunk-x",
					ChunkText: "func alpha() {}",
					StartLine: 10,
					EndLine:   14,
					Fields: map[string]any{
						"section": "alpha",
					},
				},
			},
			{
				ID:        "id-b",
				Namespace: primaryNamespace,
				Embedding: []float32{1, 0},
				Metadata: indexing.VectorMetadata{
					Repo:      "repo-b",
					Path:      "y.go",
					Language:  "go",
					ChunkID:   "chunk-y",
					ChunkText: "func beta() {}",
					StartLine: 20,
					EndLine:   26,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("upsert primary namespace records: %v", err)
	}
	if upsertPrimaryResponse.Upserted != 3 {
		t.Fatalf("unexpected primary upsert count: got %d, want 3", upsertPrimaryResponse.Upserted)
	}

	upsertSecondaryResponse, err := backend.Upsert(ctx, indexing.VectorUpsertRequest{
		Namespace: secondaryNamespace,
		Records: []indexing.VectorRecord{
			{
				ID:        "id-secondary",
				Namespace: secondaryNamespace,
				Embedding: []float32{0, 1},
				Metadata: indexing.VectorMetadata{
					Repo:      "repo-secondary",
					Path:      "secondary.go",
					Language:  "go",
					ChunkID:   "chunk-secondary",
					ChunkText: "func secondary() {}",
					StartLine: 1,
					EndLine:   3,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("upsert secondary namespace records: %v", err)
	}
	if upsertSecondaryResponse.Upserted != 1 {
		t.Fatalf("unexpected secondary upsert count: got %d, want 1", upsertSecondaryResponse.Upserted)
	}

	initialPrimaryQuery, err := backend.Query(ctx, indexing.VectorQueryRequest{
		Namespace: primaryNamespace,
		Embedding: []float32{1, 0},
		TopK:      3,
	})
	if err != nil {
		t.Fatalf("query primary namespace records: %v", err)
	}
	assertMatchIDs(t, initialPrimaryQuery.Matches, []string{"id-a", "id-b", "id-c"})
	for _, match := range initialPrimaryQuery.Matches {
		if !almostEqual(match.Score, 1, 1e-6) {
			t.Fatalf("expected initial score 1 for %q, got %f", match.Record.ID, match.Score)
		}
		if !almostEqual(match.RawScore, match.Score, 1e-12) {
			t.Fatalf("expected raw score parity for %q, got raw=%f score=%f", match.Record.ID, match.RawScore, match.Score)
		}
		if match.Record.Namespace != primaryNamespace {
			t.Fatalf(
				"expected primary namespace %q for %q, got %q",
				primaryNamespace,
				match.Record.ID,
				match.Record.Namespace,
			)
		}
	}

	alphaMatch := mustFindMatchByID(t, initialPrimaryQuery.Matches, "id-a")
	if got := alphaMatch.Record.Metadata.Fields["section"]; got != "alpha" {
		t.Fatalf("expected id-a metadata field section=alpha, got %#v", got)
	}

	updateResponse, err := backend.Upsert(ctx, indexing.VectorUpsertRequest{
		Namespace: primaryNamespace,
		Records: []indexing.VectorRecord{
			{
				ID:        "id-b",
				Namespace: primaryNamespace,
				Embedding: []float32{0, 1},
				Metadata: indexing.VectorMetadata{
					Repo:      "repo-b",
					Path:      "b-updated.go",
					Language:  "go",
					ChunkID:   "chunk-b-updated",
					ChunkText: "func betaUpdated() {}",
					StartLine: 40,
					EndLine:   46,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("update existing primary record: %v", err)
	}
	if updateResponse.Upserted != 1 {
		t.Fatalf("unexpected update upsert count: got %d, want 1", updateResponse.Upserted)
	}

	updatedPrimaryQuery, err := backend.Query(ctx, indexing.VectorQueryRequest{
		Namespace: primaryNamespace,
		Embedding: []float32{1, 0},
		TopK:      3,
	})
	if err != nil {
		t.Fatalf("query primary namespace after update: %v", err)
	}
	assertMatchIDs(t, updatedPrimaryQuery.Matches, []string{"id-a", "id-c", "id-b"})

	updatedBMatch := mustFindMatchByID(t, updatedPrimaryQuery.Matches, "id-b")
	if got := updatedBMatch.Record.Metadata.Path; got != "b-updated.go" {
		t.Fatalf("expected updated metadata path for id-b, got %q", got)
	}
	if !almostEqual(updatedBMatch.Score, 0, 1e-6) {
		t.Fatalf("expected id-b score to be 0 after embedding update, got %f", updatedBMatch.Score)
	}

	deleteResponse, err := backend.Delete(ctx, indexing.VectorDeleteRequest{
		Namespace: primaryNamespace,
		IDs:       []string{" id-b ", "", "missing", "id-b"},
	})
	if err != nil {
		t.Fatalf("delete explicit ids from primary namespace: %v", err)
	}
	if deleteResponse.Deleted != 1 {
		t.Fatalf("unexpected delete count: got %d, want 1", deleteResponse.Deleted)
	}

	afterDeletePrimaryQuery, err := backend.Query(ctx, indexing.VectorQueryRequest{
		Namespace: primaryNamespace,
		Embedding: []float32{1, 0},
		TopK:      3,
	})
	if err != nil {
		t.Fatalf("query primary namespace after explicit delete: %v", err)
	}
	assertMatchIDs(t, afterDeletePrimaryQuery.Matches, []string{"id-a", "id-c"})

	secondaryQuery, err := backend.Query(ctx, indexing.VectorQueryRequest{
		Namespace: secondaryNamespace,
		Embedding: []float32{0, 1},
		TopK:      3,
	})
	if err != nil {
		t.Fatalf("query secondary namespace records: %v", err)
	}
	assertMatchIDs(t, secondaryQuery.Matches, []string{"id-secondary"})
	if got := secondaryQuery.Matches[0].Record.Namespace; got != secondaryNamespace {
		t.Fatalf("expected secondary namespace %q, got %q", secondaryNamespace, got)
	}

	deletePrimaryNamespaceResponse, err := backend.DeleteNamespace(
		ctx,
		indexing.VectorDeleteNamespaceRequest{Namespace: primaryNamespace},
	)
	if err != nil {
		t.Fatalf("delete primary namespace: %v", err)
	}
	if deletePrimaryNamespaceResponse.Deleted != 2 {
		t.Fatalf("unexpected primary namespace delete count: got %d, want 2", deletePrimaryNamespaceResponse.Deleted)
	}

	afterNamespaceDeletePrimaryQuery, err := backend.Query(ctx, indexing.VectorQueryRequest{
		Namespace: primaryNamespace,
		Embedding: []float32{1, 0},
		TopK:      3,
	})
	if err != nil {
		t.Fatalf("query primary namespace after namespace delete: %v", err)
	}
	if len(afterNamespaceDeletePrimaryQuery.Matches) != 0 {
		t.Fatalf("expected no primary namespace matches after delete, got %d", len(afterNamespaceDeletePrimaryQuery.Matches))
	}

	secondaryStillExistsQuery, err := backend.Query(ctx, indexing.VectorQueryRequest{
		Namespace: secondaryNamespace,
		Embedding: []float32{0, 1},
		TopK:      3,
	})
	if err != nil {
		t.Fatalf("query secondary namespace after primary namespace delete: %v", err)
	}
	assertMatchIDs(t, secondaryStillExistsQuery.Matches, []string{"id-secondary"})

	health, err := backend.Health(ctx)
	if err != nil {
		t.Fatalf("backend health should be ready after contract lifecycle: %v", err)
	}
	if !health.Ready {
		t.Fatalf("expected backend health ready=true, got %#v", health)
	}
	if health.Message != "ok" {
		t.Fatalf("expected health message ok, got %q", health.Message)
	}
	if health.Metadata == nil {
		t.Fatalf("expected health metadata to be populated")
	}
	backendName, ok := health.Metadata["backend"].(string)
	if !ok || strings.TrimSpace(backendName) == "" {
		t.Fatalf("expected non-empty backend metadata, got %#v", health.Metadata["backend"])
	}
}

func runValidationContractSuite(t *testing.T, backend indexing.VectorBackend) {
	t.Helper()

	ctx := context.Background()

	_, err := backend.Upsert(ctx, indexing.VectorUpsertRequest{
		Namespace: " ",
		Records: []indexing.VectorRecord{
			{ID: "id-a", Embedding: []float32{1, 0}},
		},
	})
	assertErrorContains(t, err, "namespace must be non-empty")

	_, err = backend.Query(ctx, indexing.VectorQueryRequest{
		Namespace: "repo/main",
		Embedding: []float32{1, 0},
		TopK:      0,
	})
	assertErrorContains(t, err, "top_k must be positive")

	_, err = backend.Delete(ctx, indexing.VectorDeleteRequest{
		Namespace: "repo/main",
		IDs:       []string{"", "  "},
	})
	assertErrorContains(t, err, "ids must include at least one non-empty value")

	_, err = backend.DeleteNamespace(ctx, indexing.VectorDeleteNamespaceRequest{
		Namespace: " ",
	})
	assertErrorContains(t, err, "namespace must be non-empty")
}

func assertMatchIDs(t *testing.T, matches []indexing.VectorQueryMatch, want []string) {
	t.Helper()

	got := make([]string, len(matches))
	for index, match := range matches {
		got[index] = match.Record.ID
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected match count: got %d (%#v), want %d (%#v)", len(got), got, len(want), want)
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("unexpected match ordering: got %#v, want %#v", got, want)
		}
	}
}

func mustFindMatchByID(
	t *testing.T,
	matches []indexing.VectorQueryMatch,
	id string,
) indexing.VectorQueryMatch {
	t.Helper()

	for _, match := range matches {
		if match.Record.ID == id {
			return match
		}
	}
	t.Fatalf("match id %q not found in %#v", id, matches)
	return indexing.VectorQueryMatch{}
}

func assertErrorContains(t *testing.T, err error, wantSubstring string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error containing %q, got nil", wantSubstring)
	}
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("expected error containing %q, got %q", wantSubstring, err)
	}
}

func almostEqual(left, right, epsilon float64) bool {
	return math.Abs(left-right) <= epsilon
}

type fakeQdrantServer struct {
	collectionName string

	server *httptest.Server

	mu sync.Mutex

	collectionExists bool
	vectorDimension  int
	points           map[string]fakeQdrantPoint
}

type fakeQdrantPoint struct {
	ID      string
	Vector  []float32
	Payload map[string]any
}

func newFakeQdrantServer(t *testing.T, collectionName string) *fakeQdrantServer {
	t.Helper()

	server := &fakeQdrantServer{
		collectionName: collectionName,
		points:         make(map[string]fakeQdrantPoint),
	}
	server.server = httptest.NewServer(http.HandlerFunc(server.handle))

	t.Cleanup(func() {
		server.server.Close()
	})

	return server
}

func (s *fakeQdrantServer) baseURL() string {
	if s == nil || s.server == nil {
		return ""
	}
	return s.server.URL
}

func (s *fakeQdrantServer) handle(w http.ResponseWriter, request *http.Request) {
	if request == nil {
		s.writeError(w, http.StatusBadRequest, "request was nil")
		return
	}

	if request.Method == http.MethodGet && request.URL.Path == "/collections" {
		s.handleCollectionsList(w)
		return
	}

	segments := splitPath(request.URL.Path)
	if len(segments) < 2 || segments[0] != "collections" {
		s.writeError(w, http.StatusNotFound, "not found")
		return
	}

	collectionName, err := url.PathUnescape(segments[1])
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode collection name: %v", err))
		return
	}
	if collectionName != s.collectionName {
		s.writeError(w, http.StatusNotFound, "collection not found")
		return
	}

	if len(segments) == 2 {
		s.handleCollectionRoot(w, request)
		return
	}
	if segments[2] != "points" {
		s.writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.handleCollectionPoints(w, request, segments[3:])
}

func (s *fakeQdrantServer) handleCollectionsList(w http.ResponseWriter) {
	s.mu.Lock()
	exists := s.collectionExists
	s.mu.Unlock()

	collections := []map[string]any{}
	if exists {
		collections = append(collections, map[string]any{
			"name": s.collectionName,
		})
	}

	s.writeOK(w, map[string]any{
		"collections": collections,
	})
}

func (s *fakeQdrantServer) handleCollectionRoot(w http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		s.mu.Lock()
		exists := s.collectionExists
		vectorDimension := s.vectorDimension
		s.mu.Unlock()
		if !exists {
			s.writeError(w, http.StatusNotFound, "collection not found")
			return
		}

		s.writeOK(w, map[string]any{
			"config": map[string]any{
				"params": map[string]any{
					"vectors": map[string]any{
						"size": vectorDimension,
					},
				},
			},
		})
	case http.MethodPut:
		payload, err := decodeJSONRequest(request)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode collection payload: %v", err))
			return
		}

		vectorsPayload, ok := payload["vectors"].(map[string]any)
		if !ok {
			s.writeError(w, http.StatusBadRequest, "vectors config missing")
			return
		}
		vectorDimension, err := decodePositiveInt(vectorsPayload["size"])
		if err != nil {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode vectors size: %v", err))
			return
		}

		s.mu.Lock()
		s.collectionExists = true
		s.vectorDimension = vectorDimension
		s.points = make(map[string]fakeQdrantPoint)
		s.mu.Unlock()

		s.writeOK(w, map[string]any{
			"operation_id": 1,
		})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *fakeQdrantServer) handleCollectionPoints(
	w http.ResponseWriter,
	request *http.Request,
	remainingPath []string,
) {
	s.mu.Lock()
	exists := s.collectionExists
	s.mu.Unlock()
	if !exists {
		s.writeError(w, http.StatusNotFound, "collection not found")
		return
	}

	switch {
	case len(remainingPath) == 0 && request.Method == http.MethodPut:
		s.handlePointsUpsert(w, request)
	case len(remainingPath) == 0 && request.Method == http.MethodPost:
		s.handlePointsLookup(w, request)
	case len(remainingPath) == 1 && request.Method == http.MethodPost && remainingPath[0] == "search":
		s.handlePointsSearch(w, request)
	case len(remainingPath) == 1 && request.Method == http.MethodPost && remainingPath[0] == "delete":
		s.handlePointsDelete(w, request)
	case len(remainingPath) == 1 && request.Method == http.MethodPost && remainingPath[0] == "count":
		s.handlePointsCount(w, request)
	default:
		s.writeError(w, http.StatusNotFound, "not found")
	}
}

func (s *fakeQdrantServer) handlePointsUpsert(w http.ResponseWriter, request *http.Request) {
	payload, err := decodeJSONRequest(request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode points upsert payload: %v", err))
		return
	}

	rawPoints, ok := payload["points"].([]any)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "points payload missing")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, rawPoint := range rawPoints {
		pointMap, ok := rawPoint.(map[string]any)
		if !ok {
			s.writeError(w, http.StatusBadRequest, "point payload had unexpected shape")
			return
		}

		id, err := normalizePointID(pointMap["id"])
		if err != nil {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode point id: %v", err))
			return
		}
		vector, err := decodeEmbedding(pointMap["vector"])
		if err != nil {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode point vector for %q: %v", id, err))
			return
		}
		if len(vector) != s.vectorDimension {
			s.writeError(
				w,
				http.StatusBadRequest,
				fmt.Sprintf(
					"embedding dimension mismatch for point %q: expected %d, got %d",
					id,
					s.vectorDimension,
					len(vector),
				),
			)
			return
		}

		payloadMap, ok := pointMap["payload"].(map[string]any)
		if !ok {
			payloadMap = map[string]any{}
		}

		s.points[id] = fakeQdrantPoint{
			ID:      id,
			Vector:  cloneFloat32Slice(vector),
			Payload: deepCloneMap(payloadMap),
		}
	}

	s.writeOK(w, map[string]any{
		"operation_id": 2,
	})
}

func (s *fakeQdrantServer) handlePointsLookup(w http.ResponseWriter, request *http.Request) {
	payload, err := decodeJSONRequest(request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode points lookup payload: %v", err))
		return
	}

	rawIDs, ok := payload["ids"].([]any)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "ids payload missing")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	results := make([]map[string]any, 0, len(rawIDs))
	for _, rawID := range rawIDs {
		id, err := normalizePointID(rawID)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode lookup id: %v", err))
			return
		}
		point, ok := s.points[id]
		if !ok {
			continue
		}
		results = append(results, map[string]any{
			"id":      point.ID,
			"payload": deepCloneMap(point.Payload),
		})
	}

	s.writeOK(w, results)
}

func (s *fakeQdrantServer) handlePointsSearch(w http.ResponseWriter, request *http.Request) {
	payload, err := decodeJSONRequest(request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode points search payload: %v", err))
		return
	}

	embedding, err := decodeEmbedding(payload["vector"])
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode query vector: %v", err))
		return
	}
	limit, err := decodePositiveInt(payload["limit"])
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode query limit: %v", err))
		return
	}
	namespace := extractNamespaceFilter(payload["filter"])

	s.mu.Lock()
	defer s.mu.Unlock()

	type scoredPoint struct {
		ID      string
		Score   float64
		Vector  []float32
		Payload map[string]any
	}

	results := make([]scoredPoint, 0, len(s.points))
	for _, point := range s.points {
		if namespace != "" {
			recordNamespace := strings.TrimSpace(anyToString(point.Payload["namespace"]))
			if recordNamespace != namespace {
				continue
			}
		}

		score, err := cosineSimilarity(embedding, point.Vector)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("compute cosine similarity for %q: %v", point.ID, err))
			return
		}

		results = append(results, scoredPoint{
			ID:      point.ID,
			Score:   score,
			Vector:  cloneFloat32Slice(point.Vector),
			Payload: deepCloneMap(point.Payload),
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].ID > results[j].ID
	})
	if len(results) > limit {
		results = results[:limit]
	}

	response := make([]map[string]any, 0, len(results))
	for _, result := range results {
		response = append(response, map[string]any{
			"id":      result.ID,
			"score":   result.Score,
			"vector":  result.Vector,
			"payload": result.Payload,
		})
	}

	s.writeOK(w, response)
}

func (s *fakeQdrantServer) handlePointsDelete(w http.ResponseWriter, request *http.Request) {
	payload, err := decodeJSONRequest(request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode points delete payload: %v", err))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if rawIDs, ok := payload["points"]; ok {
		ids, ok := rawIDs.([]any)
		if !ok {
			s.writeError(w, http.StatusBadRequest, "delete points payload had unexpected shape")
			return
		}
		for _, rawID := range ids {
			id, err := normalizePointID(rawID)
			if err != nil {
				s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode delete id: %v", err))
				return
			}
			delete(s.points, id)
		}
		s.writeOK(w, map[string]any{"operation_id": 3})
		return
	}

	if rawFilter, ok := payload["filter"]; ok {
		namespace := extractNamespaceFilter(rawFilter)
		for id, point := range s.points {
			recordNamespace := strings.TrimSpace(anyToString(point.Payload["namespace"]))
			if recordNamespace == namespace {
				delete(s.points, id)
			}
		}
		s.writeOK(w, map[string]any{"operation_id": 4})
		return
	}

	s.writeError(w, http.StatusBadRequest, "delete payload must include points or filter")
}

func (s *fakeQdrantServer) handlePointsCount(w http.ResponseWriter, request *http.Request) {
	payload, err := decodeJSONRequest(request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode points count payload: %v", err))
		return
	}

	namespace := extractNamespaceFilter(payload["filter"])

	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for _, point := range s.points {
		recordNamespace := strings.TrimSpace(anyToString(point.Payload["namespace"]))
		if namespace == "" || recordNamespace == namespace {
			count++
		}
	}

	s.writeOK(w, map[string]any{
		"count": count,
	})
}

func (s *fakeQdrantServer) writeOK(w http.ResponseWriter, result any) {
	s.writeEnvelope(w, http.StatusOK, "ok", result)
}

func (s *fakeQdrantServer) writeError(w http.ResponseWriter, statusCode int, message string) {
	s.writeEnvelope(w, statusCode, map[string]any{"error": strings.TrimSpace(message)}, nil)
}

func (s *fakeQdrantServer) writeEnvelope(
	w http.ResponseWriter,
	statusCode int,
	status any,
	result any,
) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": status,
		"result": result,
	})
}

func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func decodeJSONRequest(request *http.Request) (map[string]any, error) {
	if request == nil {
		return nil, errors.New("request was nil")
	}
	if request.Body == nil {
		return map[string]any{}, nil
	}
	defer request.Body.Close()

	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.UseNumber()

	payload := map[string]any{}
	if err := decoder.Decode(&payload); err != nil {
		if errors.Is(err, io.EOF) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	return payload, nil
}

func deepCloneMap(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		cloned := make(map[string]any, len(source))
		for key, value := range source {
			cloned[key] = value
		}
		return cloned
	}

	clone := map[string]any{}
	if err := json.Unmarshal(encoded, &clone); err != nil {
		cloned := make(map[string]any, len(source))
		for key, value := range source {
			cloned[key] = value
		}
		return cloned
	}
	return clone
}

func cloneFloat32Slice(source []float32) []float32 {
	if len(source) == 0 {
		return []float32{}
	}
	target := make([]float32, len(source))
	copy(target, source)
	return target
}

func normalizePointID(raw any) (string, error) {
	switch typed := raw.(type) {
	case string:
		id := strings.TrimSpace(typed)
		if id == "" {
			return "", errors.New("id was empty")
		}
		return id, nil
	case json.Number:
		id := strings.TrimSpace(typed.String())
		if id == "" {
			return "", errors.New("id was empty")
		}
		return id, nil
	case float64:
		if math.Trunc(typed) == typed {
			return strconv.FormatInt(int64(typed), 10), nil
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	default:
		text := strings.TrimSpace(fmt.Sprintf("%v", raw))
		if text == "" || text == "<nil>" {
			return "", fmt.Errorf("unsupported id value %T", raw)
		}
		return text, nil
	}
}

func decodeEmbedding(raw any) ([]float32, error) {
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("unsupported vector type %T", raw)
	}
	if len(values) == 0 {
		return nil, errors.New("embedding must be non-empty")
	}

	embedding := make([]float32, 0, len(values))
	normSquared := 0.0
	for index, rawValue := range values {
		value, err := decodeFloat32(rawValue)
		if err != nil {
			return nil, fmt.Errorf("decode value at index %d: %w", index, err)
		}
		value64 := float64(value)
		if math.IsNaN(value64) || math.IsInf(value64, 0) {
			return nil, fmt.Errorf("embedding value at index %d must be finite", index)
		}
		normSquared += value64 * value64
		embedding = append(embedding, value)
	}
	if normSquared <= 0 {
		return nil, errors.New("embedding magnitude must be greater than zero")
	}

	return embedding, nil
}

func decodeFloat32(raw any) (float32, error) {
	switch typed := raw.(type) {
	case float32:
		return typed, nil
	case float64:
		return float32(typed), nil
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, err
		}
		return float32(parsed), nil
	case int:
		return float32(typed), nil
	case int64:
		return float32(typed), nil
	default:
		return 0, fmt.Errorf("unsupported numeric value type %T", raw)
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
		return 0, fmt.Errorf("unsupported positive integer type %T", raw)
	}
}

func extractNamespaceFilter(raw any) string {
	filter, ok := raw.(map[string]any)
	if !ok || len(filter) == 0 {
		return ""
	}
	rawMustClauses, ok := filter["must"].([]any)
	if !ok {
		return ""
	}
	for _, rawMustClause := range rawMustClauses {
		mustClause, ok := rawMustClause.(map[string]any)
		if !ok {
			continue
		}
		matchClause, ok := mustClause["match"].(map[string]any)
		if !ok {
			continue
		}
		namespace := strings.TrimSpace(anyToString(matchClause["value"]))
		if namespace != "" {
			return namespace
		}
	}
	return ""
}

func anyToString(raw any) string {
	switch typed := raw.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprintf("%v", raw)
	}
}

func cosineSimilarity(left, right []float32) (float64, error) {
	if len(left) != len(right) {
		return 0, fmt.Errorf("dimension mismatch: %d vs %d", len(left), len(right))
	}

	dot := 0.0
	leftNormSquared := 0.0
	rightNormSquared := 0.0
	for index := range left {
		leftValue := float64(left[index])
		rightValue := float64(right[index])
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
