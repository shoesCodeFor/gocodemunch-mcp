package testsgo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestQdrantLiveLifecycleOperations(t *testing.T) {
	qdrantURL := strings.TrimSpace(os.Getenv("QDRANT_URL"))
	if qdrantURL == "" {
		t.Skip("QDRANT_URL is unset; skipping live Qdrant lifecycle integration test")
	}

	baseURL := strings.TrimRight(qdrantURL, "/")
	apiKey := strings.TrimSpace(os.Getenv("QDRANT_API_KEY"))
	collection := fmt.Sprintf("gocodemunch-tests-go-live-%d", time.Now().UnixNano())

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	httpClient := &http.Client{}
	collectionPath := "/collections/" + url.PathEscape(collection)

	// Create an isolated collection for this test run.
	qdrantJSONRequest(t, ctx, httpClient, baseURL, apiKey, http.MethodPut, collectionPath, map[string]any{
		"vectors": map[string]any{
			"size":     2,
			"distance": "Cosine",
		},
	})

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = qdrantJSONRequestNoFail(
			cleanupCtx,
			httpClient,
			baseURL,
			apiKey,
			http.MethodDelete,
			collectionPath,
			nil,
		)
	})

	const (
		primaryNamespace   = "tests-go/live-qdrant/primary"
		secondaryNamespace = "tests-go/live-qdrant/secondary"
	)

	qdrantJSONRequest(
		t,
		ctx,
		httpClient,
		baseURL,
		apiKey,
		http.MethodPut,
		collectionPath+"/points?wait=true",
		map[string]any{
			"points": []map[string]any{
				{
					"id":     "id-c",
					"vector": []float32{1, 0},
					"payload": map[string]any{
						"namespace": primaryNamespace,
						"metadata": map[string]any{
							"path": "z.go",
						},
					},
				},
				{
					"id":     "id-a",
					"vector": []float32{1, 0},
					"payload": map[string]any{
						"namespace": primaryNamespace,
						"metadata": map[string]any{
							"path": "x.go",
						},
					},
				},
				{
					"id":     "id-b",
					"vector": []float32{0, 1},
					"payload": map[string]any{
						"namespace": primaryNamespace,
						"metadata": map[string]any{
							"path": "y.go",
						},
					},
				},
				{
					"id":     "id-secondary",
					"vector": []float32{1, 0},
					"payload": map[string]any{
						"namespace": secondaryNamespace,
						"metadata": map[string]any{
							"path": "secondary.go",
						},
					},
				},
			},
		},
	)

	primaryQueryIDs := queryNamespaceIDs(t, ctx, httpClient, baseURL, apiKey, collectionPath, primaryNamespace, 3)
	assertContainsExactIDs(t, primaryQueryIDs, []string{"id-a", "id-b", "id-c"})

	qdrantJSONRequest(
		t,
		ctx,
		httpClient,
		baseURL,
		apiKey,
		http.MethodPost,
		collectionPath+"/points/delete?wait=true",
		map[string]any{
			"points": []string{"id-b", "missing", "id-b"},
		},
	)

	postDeletePrimaryIDs := queryNamespaceIDs(
		t,
		ctx,
		httpClient,
		baseURL,
		apiKey,
		collectionPath,
		primaryNamespace,
		3,
	)
	assertContainsExactIDs(t, postDeletePrimaryIDs, []string{"id-a", "id-c"})

	qdrantJSONRequest(
		t,
		ctx,
		httpClient,
		baseURL,
		apiKey,
		http.MethodPost,
		collectionPath+"/points/delete?wait=true",
		map[string]any{
			"filter": namespaceFilter(primaryNamespace),
		},
	)

	postNamespaceDeletePrimaryIDs := queryNamespaceIDs(
		t,
		ctx,
		httpClient,
		baseURL,
		apiKey,
		collectionPath,
		primaryNamespace,
		3,
	)
	if len(postNamespaceDeletePrimaryIDs) != 0 {
		t.Fatalf(
			"expected no primary namespace matches after namespace delete, got %#v",
			postNamespaceDeletePrimaryIDs,
		)
	}

	secondaryIDs := queryNamespaceIDs(
		t,
		ctx,
		httpClient,
		baseURL,
		apiKey,
		collectionPath,
		secondaryNamespace,
		3,
	)
	assertContainsExactIDs(t, secondaryIDs, []string{"id-secondary"})
}

func queryNamespaceIDs(
	t *testing.T,
	ctx context.Context,
	httpClient *http.Client,
	baseURL string,
	apiKey string,
	collectionPath string,
	namespace string,
	limit int,
) []string {
	t.Helper()

	result := qdrantJSONRequest(
		t,
		ctx,
		httpClient,
		baseURL,
		apiKey,
		http.MethodPost,
		collectionPath+"/points/search",
		map[string]any{
			"vector":       []float32{1, 0},
			"limit":        limit,
			"with_payload": true,
			"with_vector":  false,
			"filter":       namespaceFilter(namespace),
		},
	)

	var points []map[string]any
	if err := json.Unmarshal(result, &points); err != nil {
		t.Fatalf("decode qdrant search result: %v\nresult=%s", err, string(result))
	}

	ids := make([]string, 0, len(points))
	for _, point := range points {
		id := strings.TrimSpace(fmt.Sprint(point["id"]))
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func assertContainsExactIDs(t *testing.T, got []string, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("unexpected id count: got %d (%#v), want %d (%#v)", len(got), got, len(want), want)
	}

	seen := make(map[string]int, len(got))
	for _, id := range got {
		seen[id]++
	}
	for _, wantID := range want {
		if seen[wantID] == 0 {
			t.Fatalf("missing expected id %q in %#v", wantID, got)
		}
		seen[wantID]--
	}
	for id, count := range seen {
		if count > 0 {
			t.Fatalf("unexpected extra id %q in %#v", id, got)
		}
	}
}

func namespaceFilter(namespace string) map[string]any {
	return map[string]any{
		"must": []map[string]any{
			{
				"key": "namespace",
				"match": map[string]any{
					"value": namespace,
				},
			},
		},
	}
}

func qdrantJSONRequest(
	t *testing.T,
	ctx context.Context,
	httpClient *http.Client,
	baseURL string,
	apiKey string,
	method string,
	endpoint string,
	requestBody any,
) json.RawMessage {
	t.Helper()

	result, err := qdrantJSONRequestNoFail(
		ctx,
		httpClient,
		baseURL,
		apiKey,
		method,
		endpoint,
		requestBody,
	)
	if err != nil {
		t.Fatalf("qdrant %s %s failed: %v", method, endpoint, err)
	}
	return result
}

func qdrantJSONRequestNoFail(
	ctx context.Context,
	httpClient *http.Client,
	baseURL string,
	apiKey string,
	method string,
	endpoint string,
	requestBody any,
) (json.RawMessage, error) {
	var bodyReader io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	requestURL := baseURL + endpoint
	request, err := http.NewRequestWithContext(ctx, method, requestURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		request.Header.Set("api-key", apiKey)
	}

	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(responseBody)))
	}

	envelope := struct {
		Status any             `json:"status"`
		Result json.RawMessage `json:"result"`
	}{}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return nil, fmt.Errorf("decode qdrant envelope: %w", err)
	}

	if !qdrantStatusOK(envelope.Status) {
		return nil, fmt.Errorf("qdrant status not ok: %s", strings.TrimSpace(string(responseBody)))
	}

	return envelope.Result, nil
}

func qdrantStatusOK(status any) bool {
	switch typed := status.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "ok")
	case map[string]any:
		errorText, _ := typed["error"].(string)
		return strings.TrimSpace(errorText) == ""
	default:
		return false
	}
}
