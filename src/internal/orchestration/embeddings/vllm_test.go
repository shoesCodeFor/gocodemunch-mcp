package embeddings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestVLLMEmbedderRequestMappingAndNormalization(t *testing.T) {
	t.Parallel()

	inputs := []string{"alpha", "beta", "gamma"}
	inputSeed := map[string]float32{
		"alpha": 11,
		"beta":  22,
		"gamma": 33,
	}

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected method POST, got %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("expected /v1/embeddings path, got %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("expected Authorization bearer token, got %q", got)
		}

		requestCount.Add(1)

		requestPayload := vllmEmbedRequest{}
		if err := json.NewDecoder(r.Body).Decode(&requestPayload); err != nil {
			t.Errorf("decode request payload: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if requestPayload.Model != "bge-m3" {
			t.Errorf("expected model bge-m3, got %q", requestPayload.Model)
		}
		if requestPayload.EncodingFormat != defaultVLLMEncodingFormat {
			t.Errorf("expected encoding_format %q, got %q", defaultVLLMEncodingFormat, requestPayload.EncodingFormat)
		}

		data := make([]vllmEmbedDatum, 0, len(requestPayload.Input))
		for i, input := range requestPayload.Input {
			seed, ok := inputSeed[input]
			if !ok {
				t.Errorf("received unexpected input %q", input)
				http.Error(w, "unexpected input", http.StatusBadRequest)
				return
			}
			data = append(data, vllmEmbedDatum{
				Index:     i,
				Embedding: buildEmbedding(seed, bgeM3EmbeddingDimensions),
			})
		}

		for i, j := 0, len(data)-1; i < j; i, j = i+1, j-1 {
			data[i], data[j] = data[j], data[i]
		}

		if err := json.NewEncoder(w).Encode(map[string]any{
			"model": "bge-m3",
			"data":  data,
		}); err != nil {
			t.Errorf("encode response payload: %v", err)
		}
	}))
	defer server.Close()

	embedder, err := NewVLLMEmbedder(
		server.URL+"/v1/",
		"bge-m3",
		"test-key",
		time.Second,
		WithVLLMHTTPClient(server.Client()),
		WithVLLMBatchSize(2),
	)
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}

	embeddings, err := embedder.Embed(context.Background(), inputs)
	if err != nil {
		t.Fatalf("embed inputs: %v", err)
	}
	if got := len(embeddings); got != len(inputs) {
		t.Fatalf("expected %d embeddings, got %d", len(inputs), got)
	}
	for i, input := range inputs {
		if got := len(embeddings[i]); got != bgeM3EmbeddingDimensions {
			t.Fatalf("expected embedding %d length %d, got %d", i, bgeM3EmbeddingDimensions, got)
		}
		if got := embeddings[i][0]; got != inputSeed[input] {
			t.Fatalf("expected embedding %d seed %v, got %v", i, inputSeed[input], got)
		}
	}

	if got := requestCount.Load(); got != 2 {
		t.Fatalf("expected 2 batched requests, got %d", got)
	}
}

func TestVLLMEmbedderTimeoutBehavior(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "bge-m3",
			"data": []vllmEmbedDatum{{
				Index:     0,
				Embedding: buildEmbedding(7, bgeM3EmbeddingDimensions),
			}},
		})
	}))
	defer server.Close()

	embedder, err := NewVLLMEmbedder(
		server.URL,
		"bge-m3",
		"",
		15*time.Millisecond,
		WithVLLMHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}

	_, err = embedder.Embed(context.Background(), []string{"alpha"})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded error, got %v", err)
	}
	if !strings.Contains(err.Error(), "execute vllm embedding request") {
		t.Fatalf("expected execute request operation in error, got %q", err.Error())
	}
	assertRetryable(t, err, true)
}

func TestVLLMEmbedderHTTPErrorMapping(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		statusCode    int
		body          string
		wantRetryable bool
		wantContains  []string
	}{
		{
			name:          "retryable rate limit",
			statusCode:    http.StatusTooManyRequests,
			body:          `{"error":{"message":"slow down"}}`,
			wantRetryable: true,
			wantContains: []string{
				"execute vllm embedding request",
				"vllm returned status 429",
				"slow down",
			},
		},
		{
			name:          "non-retryable bad request",
			statusCode:    http.StatusBadRequest,
			body:          `{"error":"invalid input"}`,
			wantRetryable: false,
			wantContains: []string{
				"execute vllm embedding request",
				"vllm returned status 400",
				"invalid input",
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(testCase.statusCode)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()

			embedder, err := NewVLLMEmbedder(
				server.URL,
				"bge-m3",
				"",
				time.Second,
				WithVLLMHTTPClient(server.Client()),
			)
			if err != nil {
				t.Fatalf("new embedder: %v", err)
			}

			_, err = embedder.Embed(context.Background(), []string{"alpha"})
			if err == nil {
				t.Fatal("expected HTTP status error, got nil")
			}

			for _, fragment := range testCase.wantContains {
				if !strings.Contains(err.Error(), fragment) {
					t.Fatalf("expected error to contain %q, got %q", fragment, err.Error())
				}
			}

			assertRetryable(t, err, testCase.wantRetryable)
		})
	}
}

func TestVLLMEmbedderDuplicateIndexHandling(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "bge-m3",
			"data": []vllmEmbedDatum{
				{Index: 0, Embedding: buildEmbedding(1, bgeM3EmbeddingDimensions)},
				{Index: 0, Embedding: buildEmbedding(2, bgeM3EmbeddingDimensions)},
			},
		})
	}))
	defer server.Close()

	embedder, err := NewVLLMEmbedder(
		server.URL,
		"bge-m3",
		"",
		time.Second,
		WithVLLMHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}

	_, err = embedder.Embed(context.Background(), []string{"alpha", "beta"})
	if err == nil {
		t.Fatal("expected duplicate index error, got nil")
	}
	if !strings.Contains(err.Error(), "decode vllm embedding response") {
		t.Fatalf("expected decode operation in error, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "duplicate embedding index: 0") {
		t.Fatalf("expected duplicate index message, got %q", err.Error())
	}
	assertRetryable(t, err, false)
}
