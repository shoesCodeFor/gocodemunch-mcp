package qdrant

import (
	"context"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
)

func TestQdrantQueryDeterministicTieOrdering(t *testing.T) {
	t.Parallel()

	var (
		callCount      int
		searchRequests []qdrantSearchRequest
	)

	adapter, err := NewAdapter(
		"http://qdrant.test",
		"",
		"test-collection",
		WithHTTPClient(&http.Client{
			Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				callCount++

				switch callCount {
				case 1:
					if request.Method != http.MethodGet || request.URL.Path != "/collections/test-collection" {
						t.Fatalf("unexpected bootstrap request: %s %s", request.Method, request.URL.Path)
					}
					return jsonResponse(http.StatusOK, `{
						"status":"ok",
						"result":{"config":{"params":{"vectors":{"size":2}}}}
					}`), nil
				case 2:
					if request.Method != http.MethodPost || request.URL.Path != "/collections/test-collection/points/search" {
						t.Fatalf("unexpected first search request: %s %s", request.Method, request.URL.Path)
					}
					payload := qdrantSearchRequest{}
					if err := decodeRequestBody(request, &payload); err != nil {
						t.Fatalf("decode first search request: %v", err)
					}
					searchRequests = append(searchRequests, payload)
					return jsonResponse(http.StatusOK, `{
						"status":"ok",
						"result":[
							{"id":"id-c","score":1,"vector":[1,0],"payload":{"namespace":"repo/main","metadata":{"repo":"repo-c","path":"z.go","chunk_id":"z"}}},
							{"id":"id-a","score":1,"vector":[1,0],"payload":{"namespace":"repo/main","metadata":{"repo":"repo-a","path":"x.go","chunk_id":"x"}}},
							{"id":"id-b","score":1,"vector":[1,0],"payload":{"namespace":"repo/main","metadata":{"repo":"repo-b","path":"y.go","chunk_id":"y"}}}
						]
					}`), nil
				case 3:
					if request.Method != http.MethodPost || request.URL.Path != "/collections/test-collection/points/search" {
						t.Fatalf("unexpected second search request: %s %s", request.Method, request.URL.Path)
					}
					payload := qdrantSearchRequest{}
					if err := decodeRequestBody(request, &payload); err != nil {
						t.Fatalf("decode second search request: %v", err)
					}
					searchRequests = append(searchRequests, payload)
					return jsonResponse(http.StatusOK, `{
						"status":"ok",
						"result":[
							{"id":"id-b","score":1,"vector":[1,0],"payload":{"namespace":"repo/main","metadata":{"repo":"repo-b","path":"y.go","chunk_id":"y"}}},
							{"id":"id-c","score":1,"vector":[1,0],"payload":{"namespace":"repo/main","metadata":{"repo":"repo-c","path":"z.go","chunk_id":"z"}}},
							{"id":"id-a","score":1,"vector":[1,0],"payload":{"namespace":"repo/main","metadata":{"repo":"repo-a","path":"x.go","chunk_id":"x"}}}
						]
					}`), nil
				default:
					t.Fatalf("unexpected extra qdrant request #%d: %s %s", callCount, request.Method, request.URL.Path)
					return nil, nil
				}
			}),
		}),
	)
	if err != nil {
		t.Fatalf("create qdrant adapter: %v", err)
	}

	queryRequest := indexing.VectorQueryRequest{
		Namespace: "repo/main",
		Embedding: []float32{1, 0},
		TopK:      3,
	}

	firstQuery, err := adapter.Query(context.Background(), queryRequest)
	if err != nil {
		t.Fatalf("first deterministic qdrant query: %v", err)
	}
	secondQuery, err := adapter.Query(context.Background(), queryRequest)
	if err != nil {
		t.Fatalf("second deterministic qdrant query: %v", err)
	}

	wantIDs := []string{"id-a", "id-b", "id-c"}
	if got := queryMatchIDs(firstQuery.Matches); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("unexpected deterministic ordering from first query: got %#v, want %#v", got, wantIDs)
	}
	if got := queryMatchIDs(secondQuery.Matches); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("unexpected deterministic ordering from second query: got %#v, want %#v", got, wantIDs)
	}

	for _, match := range firstQuery.Matches {
		if !floatAlmostEqual(match.Score, 1, 1e-6) {
			t.Fatalf("expected tie scores of 1 for deterministic order test, got %f", match.Score)
		}
		if !floatAlmostEqual(match.RawScore, match.Score, 1e-12) {
			t.Fatalf("expected raw score and score to match, got raw=%f score=%f", match.RawScore, match.Score)
		}
	}

	if callCount != 3 {
		t.Fatalf("unexpected qdrant request count: got %d, want 3", callCount)
	}
	if len(searchRequests) != 2 {
		t.Fatalf("expected two qdrant search requests, got %d", len(searchRequests))
	}
	for index, searchRequest := range searchRequests {
		if searchRequest.Limit != 3 {
			t.Fatalf("unexpected limit for search request #%d: got %d, want 3", index+1, searchRequest.Limit)
		}
		if !searchRequest.WithPayload || !searchRequest.WithVector {
			t.Fatalf("unexpected payload/vector flags for search request #%d: %+v", index+1, searchRequest)
		}
		assertNamespaceFilter(t, searchRequest.Filter, "repo/main")
	}
}

func TestQdrantQueryErrorTranslationFromTransport(t *testing.T) {
	t.Parallel()

	adapter, err := NewAdapter(
		"http://qdrant.test",
		"",
		"test-collection",
		WithHTTPClient(&http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, timeoutRoundTripError{}
			}),
		}),
	)
	if err != nil {
		t.Fatalf("create qdrant adapter: %v", err)
	}

	_, err = adapter.Query(context.Background(), indexing.VectorQueryRequest{
		Namespace: "repo/main",
		Embedding: []float32{1, 0},
		TopK:      1,
	})
	if err == nil {
		t.Fatal("expected qdrant query transport error")
	}
	if !indexing.IsRetryableVectorError(err) {
		t.Fatalf("expected transport query error to be retryable, got %T", err)
	}
}

func TestQdrantQueryErrorTranslationFromHTTPStatus(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		status    int
		body      string
		retryable bool
	}{
		{
			name:      "retryable status",
			status:    http.StatusServiceUnavailable,
			body:      `{"status":{"error":"temporarily unavailable"}}`,
			retryable: true,
		},
		{
			name:      "non-retryable status",
			status:    http.StatusBadRequest,
			body:      `{"status":{"error":"invalid request"}}`,
			retryable: false,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			callCount := 0
			adapter, err := NewAdapter(
				"http://qdrant.test",
				"",
				"test-collection",
				WithHTTPClient(&http.Client{
					Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
						callCount++
						switch callCount {
						case 1:
							if request.Method != http.MethodGet || request.URL.Path != "/collections/test-collection" {
								t.Fatalf("unexpected bootstrap request: %s %s", request.Method, request.URL.Path)
							}
							return jsonResponse(http.StatusOK, `{
								"status":"ok",
								"result":{"config":{"params":{"vectors":{"size":2}}}}
							}`), nil
						case 2:
							if request.Method != http.MethodPost || request.URL.Path != "/collections/test-collection/points/search" {
								t.Fatalf("unexpected search request: %s %s", request.Method, request.URL.Path)
							}
							return jsonResponse(testCase.status, testCase.body), nil
						default:
							t.Fatalf("unexpected extra qdrant request #%d: %s %s", callCount, request.Method, request.URL.Path)
							return nil, nil
						}
					}),
				}),
			)
			if err != nil {
				t.Fatalf("create qdrant adapter: %v", err)
			}

			_, err = adapter.Query(context.Background(), indexing.VectorQueryRequest{
				Namespace: "repo/main",
				Embedding: []float32{1, 0},
				TopK:      1,
			})
			if err == nil {
				t.Fatalf("expected qdrant query error for status %d", testCase.status)
			}
			if got := indexing.IsRetryableVectorError(err); got != testCase.retryable {
				t.Fatalf(
					"unexpected retryable classification for status %d: got %t, want %t (error=%v)",
					testCase.status,
					got,
					testCase.retryable,
					err,
				)
			}
			if !strings.Contains(err.Error(), "execute search") {
				t.Fatalf("expected query error to include execute search context, got %q", err)
			}
			if callCount != 2 {
				t.Fatalf("unexpected qdrant request count: got %d, want 2", callCount)
			}
		})
	}
}

func queryMatchIDs(matches []indexing.VectorQueryMatch) []string {
	ids := make([]string, len(matches))
	for index, match := range matches {
		ids[index] = match.Record.ID
	}
	return ids
}

func floatAlmostEqual(left, right, epsilon float64) bool {
	return math.Abs(left-right) <= epsilon
}
