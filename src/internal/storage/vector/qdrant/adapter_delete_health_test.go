package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
)

func TestQdrantDeleteFiltersByNamespace(t *testing.T) {
	t.Parallel()

	var (
		callCount       int
		deletedPointIDs []string
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
						"result":{"config":{"params":{"vectors":{"size":3}}}}
					}`), nil
				case 2:
					if request.Method != http.MethodPost || request.URL.Path != "/collections/test-collection/points" {
						t.Fatalf("unexpected point lookup request: %s %s", request.Method, request.URL.Path)
					}

					payload := qdrantPointLookupRequest{}
					if err := decodeRequestBody(request, &payload); err != nil {
						t.Fatalf("decode point lookup payload: %v", err)
					}
					if got, want := payload.IDs, []string{
						qdrantStoredPointID("id-a"),
						qdrantStoredPointID("id-b"),
						qdrantStoredPointID("id-c"),
					}; !reflect.DeepEqual(got, want) {
						t.Fatalf("unexpected normalized ids: got %#v, want %#v", got, want)
					}
					if !payload.WithPayload || payload.WithVector {
						t.Fatalf("unexpected lookup flags: %+v", payload)
					}

					return jsonResponse(http.StatusOK, `{
						"status":"ok",
						"result":[
							{"id":"`+qdrantStoredPointID("id-a")+`","payload":{"namespace":"repo/main","record_id":"id-a"}},
							{"id":"`+qdrantStoredPointID("id-b")+`","payload":{"namespace":"repo/other","record_id":"id-b"}},
							{"id":"`+qdrantStoredPointID("id-c")+`","payload":{"namespace":"repo/main","record_id":"id-c"}}
						]
					}`), nil
				case 3:
					if request.Method != http.MethodPost || request.URL.Path != "/collections/test-collection/points/delete" {
						t.Fatalf("unexpected delete request: %s %s", request.Method, request.URL.Path)
					}
					if got := request.URL.Query().Get("wait"); got != "true" {
						t.Fatalf("expected wait=true on delete request, got %q", got)
					}

					payload := qdrantDeleteByIDsRequest{}
					if err := decodeRequestBody(request, &payload); err != nil {
						t.Fatalf("decode delete payload: %v", err)
					}
					deletedPointIDs = append([]string{}, payload.Points...)

					return jsonResponse(http.StatusOK, `{"status":"ok","result":{"operation_id":42}}`), nil
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

	response, err := adapter.Delete(context.Background(), indexing.VectorDeleteRequest{
		Namespace: "repo/main",
		IDs:       []string{" id-c ", "", "id-b", "id-a", "id-a"},
	})
	if err != nil {
		t.Fatalf("delete namespace-scoped ids: %v", err)
	}
	if response.Deleted != 2 {
		t.Fatalf("unexpected delete count: got %d, want 2", response.Deleted)
	}
	wantDeletedIDs := []string{
		qdrantStoredPointID("id-a"),
		qdrantStoredPointID("id-c"),
	}
	sort.Strings(wantDeletedIDs)
	if got, want := deletedPointIDs, wantDeletedIDs; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected ids sent to qdrant delete: got %#v, want %#v", got, want)
	}
	if callCount != 3 {
		t.Fatalf("unexpected qdrant request count: got %d, want 3", callCount)
	}
}

func TestQdrantDeleteNamespaceUsesCountAndFilter(t *testing.T) {
	t.Parallel()

	var (
		callCount            int
		countRequestPayload  qdrantCountRequest
		deleteRequestPayload qdrantDeleteByFilterRequest
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
						t.Fatalf("unexpected collection probe request: %s %s", request.Method, request.URL.Path)
					}
					return jsonResponse(http.StatusOK, `{
						"status":"ok",
						"result":{"config":{"params":{"vectors":{"size":3}}}}
					}`), nil
				case 2:
					if request.Method != http.MethodPost || request.URL.Path != "/collections/test-collection/points/count" {
						t.Fatalf("unexpected count request: %s %s", request.Method, request.URL.Path)
					}
					if err := decodeRequestBody(request, &countRequestPayload); err != nil {
						t.Fatalf("decode count request: %v", err)
					}

					return jsonResponse(http.StatusOK, `{
						"status":"ok",
						"result":{"count":4}
					}`), nil
				case 3:
					if request.Method != http.MethodPost || request.URL.Path != "/collections/test-collection/points/delete" {
						t.Fatalf("unexpected delete request: %s %s", request.Method, request.URL.Path)
					}
					if got := request.URL.Query().Get("wait"); got != "true" {
						t.Fatalf("expected wait=true on namespace delete request, got %q", got)
					}
					if err := decodeRequestBody(request, &deleteRequestPayload); err != nil {
						t.Fatalf("decode namespace delete request: %v", err)
					}

					return jsonResponse(http.StatusOK, `{"status":"ok","result":{"operation_id":7}}`), nil
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

	response, err := adapter.DeleteNamespace(
		context.Background(),
		indexing.VectorDeleteNamespaceRequest{Namespace: "repo/main"},
	)
	if err != nil {
		t.Fatalf("delete namespace: %v", err)
	}
	if response.Deleted != 4 {
		t.Fatalf("unexpected namespace delete count: got %d, want 4", response.Deleted)
	}
	if !countRequestPayload.Exact {
		t.Fatalf("expected exact namespace count request")
	}
	assertNamespaceFilter(t, countRequestPayload.Filter, "repo/main")
	assertNamespaceFilter(t, deleteRequestPayload.Filter, "repo/main")
	if callCount != 3 {
		t.Fatalf("unexpected qdrant request count: got %d, want 3", callCount)
	}
}

func TestQdrantHealthReportsReadyWhenCollectionMissing(t *testing.T) {
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
					if request.Method != http.MethodGet || request.URL.Path != "/collections" {
						t.Fatalf("unexpected qdrant api health request: %s %s", request.Method, request.URL.Path)
					}
					return jsonResponse(http.StatusOK, `{"status":"ok","result":{"collections":[]}}`), nil
				case 2:
					if request.Method != http.MethodGet || request.URL.Path != "/collections/test-collection" {
						t.Fatalf("unexpected collection probe request: %s %s", request.Method, request.URL.Path)
					}
					return jsonResponse(http.StatusNotFound, `{"status":{"error":"not found"},"result":null}`), nil
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

	health, err := adapter.Health(context.Background())
	if err != nil {
		t.Fatalf("health check should succeed when collection is missing: %v", err)
	}
	if !health.Ready {
		t.Fatalf("expected health ready=true, got %#v", health)
	}
	if health.Message != "ok" {
		t.Fatalf("unexpected health message: %q", health.Message)
	}
	if got := health.Metadata["backend"]; got != "qdrant" {
		t.Fatalf("unexpected backend metadata: %#v", got)
	}
	if got := health.Metadata["collection_exists"]; got != false {
		t.Fatalf("expected collection_exists=false, got %#v", got)
	}
	if callCount != 2 {
		t.Fatalf("unexpected qdrant request count: got %d, want 2", callCount)
	}
}

func TestQdrantTransportErrorIsRetryable(t *testing.T) {
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

	_, _, err = adapter.doJSONRequest(context.Background(), http.MethodGet, "/collections", nil)
	if err == nil {
		t.Fatal("expected transport error from qdrant request")
	}
	if !indexing.IsRetryableVectorError(err) {
		t.Fatalf("expected retryable classification for transport error, got %T", err)
	}
}

func TestEnsureQdrantSuccessMapsRetryableHTTPStatus(t *testing.T) {
	t.Parallel()

	retryableErr := ensureQdrantSuccess(
		http.StatusServiceUnavailable,
		[]byte(`{"status":{"error":"temporarily unavailable"}}`),
	)
	if retryableErr == nil {
		t.Fatal("expected qdrant status error for 503")
	}
	if !indexing.IsRetryableVectorError(retryableErr) {
		t.Fatalf("expected 503 to be retryable, got error %v", retryableErr)
	}

	nonRetryableErr := ensureQdrantSuccess(
		http.StatusBadRequest,
		[]byte(`{"status":{"error":"bad request"}}`),
	)
	if nonRetryableErr == nil {
		t.Fatal("expected qdrant status error for 400")
	}
	if indexing.IsRetryableVectorError(nonRetryableErr) {
		t.Fatalf("expected 400 to be non-retryable, got error %v", nonRetryableErr)
	}
}

func assertNamespaceFilter(t *testing.T, filter qdrantFilter, namespace string) {
	t.Helper()

	if len(filter.Must) != 1 {
		t.Fatalf("expected one namespace filter clause, got %d", len(filter.Must))
	}
	clause := filter.Must[0]
	if clause.Key != defaultNamespacePayloadField {
		t.Fatalf("unexpected filter key: got %q, want %q", clause.Key, defaultNamespacePayloadField)
	}
	if got := strings.TrimSpace(fmt.Sprintf("%v", clause.Match.Value)); got != namespace {
		t.Fatalf("unexpected namespace filter value: got %q, want %q", got, namespace)
	}
}

func decodeRequestBody(request *http.Request, target any) error {
	if request == nil {
		return errors.New("request was nil")
	}
	if request.Body == nil {
		return errors.New("request body was nil")
	}
	defer request.Body.Close()

	payload, err := io.ReadAll(request.Body)
	if err != nil {
		return err
	}
	if len(payload) == 0 {
		return errors.New("request body was empty")
	}
	return json.Unmarshal(payload, target)
}

func jsonResponse(statusCode int, body string) *http.Response {
	if strings.TrimSpace(body) == "" {
		body = `{}`
	}

	return &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

type roundTripFunc func(request *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type timeoutRoundTripError struct{}

func (timeoutRoundTripError) Error() string {
	return "qdrant transport timeout"
}

func (timeoutRoundTripError) Timeout() bool {
	return true
}
