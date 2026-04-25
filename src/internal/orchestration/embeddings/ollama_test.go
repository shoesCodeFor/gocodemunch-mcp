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

func TestOllamaEmbedderSuccessPath(t *testing.T) {
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
		if r.URL.Path != "/api/embed" {
			t.Errorf("expected /api/embed path, got %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}

		requestCount.Add(1)

		requestPayload := ollamaEmbedRequest{}
		if err := json.NewDecoder(r.Body).Decode(&requestPayload); err != nil {
			t.Errorf("decode request payload: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if requestPayload.Model != "bge-m3" {
			t.Errorf("expected model bge-m3, got %q", requestPayload.Model)
		}

		responseEmbeddings := make([][]float32, 0, len(requestPayload.Input))
		for _, input := range requestPayload.Input {
			seed, ok := inputSeed[input]
			if !ok {
				t.Errorf("received unexpected input %q", input)
				http.Error(w, "unexpected input", http.StatusBadRequest)
				return
			}
			responseEmbeddings = append(responseEmbeddings, buildEmbedding(seed, bgeM3EmbeddingDimensions))
		}

		if err := json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Model:      "bge-m3",
			Embeddings: responseEmbeddings,
		}); err != nil {
			t.Errorf("encode response payload: %v", err)
		}
	}))
	defer server.Close()

	embedder, err := NewOllamaEmbedder(
		server.URL,
		"bge-m3",
		time.Second,
		WithOllamaHTTPClient(server.Client()),
		WithOllamaBatchSize(2),
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

func TestOllamaEmbedderTimeoutBehavior(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Model:      "bge-m3",
			Embeddings: [][]float32{buildEmbedding(7, bgeM3EmbeddingDimensions)},
		})
	}))
	defer server.Close()

	embedder, err := NewOllamaEmbedder(
		server.URL,
		"bge-m3",
		15*time.Millisecond,
		WithOllamaHTTPClient(server.Client()),
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
	if !strings.Contains(err.Error(), "execute ollama embedding request") {
		t.Fatalf("expected execute request operation in error, got %q", err.Error())
	}
	assertRetryable(t, err, true)
}

func TestOllamaEmbedderMalformedPayloadHandling(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"bge-m3","embeddings":[123]}`))
	}))
	defer server.Close()

	embedder, err := NewOllamaEmbedder(
		server.URL,
		"bge-m3",
		time.Second,
		WithOllamaHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}

	_, err = embedder.Embed(context.Background(), []string{"alpha"})
	if err == nil {
		t.Fatal("expected malformed payload error, got nil")
	}
	if !strings.Contains(err.Error(), "decode ollama embedding response") {
		t.Fatalf("expected decode operation in error, got %q", err.Error())
	}
	assertRetryable(t, err, false)
}

func TestOllamaEmbedderDimensionMismatchHandling(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Model:      "bge-m3",
			Embeddings: [][]float32{{1, 2, 3}},
		})
	}))
	defer server.Close()

	embedder, err := NewOllamaEmbedder(
		server.URL,
		"bge-m3",
		time.Second,
		WithOllamaHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}

	_, err = embedder.Embed(context.Background(), []string{"alpha"})
	if err == nil {
		t.Fatal("expected dimension mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "validate ollama embedding response") {
		t.Fatalf("expected validation operation in error, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "embedding dimension mismatch for model \"bge-m3\"") {
		t.Fatalf("expected dimension mismatch message, got %q", err.Error())
	}
	assertRetryable(t, err, false)
}

func assertRetryable(t *testing.T, err error, want bool) {
	t.Helper()

	var classified interface{ Retryable() bool }
	if !errors.As(err, &classified) {
		t.Fatalf("expected retryable classification on error, got %T", err)
	}
	if got := classified.Retryable(); got != want {
		t.Fatalf("expected retryable=%v, got %v (error=%v)", want, got, err)
	}
}

func buildEmbedding(seed float32, dimensions int) []float32 {
	vector := make([]float32, dimensions)
	for i := range vector {
		vector[i] = seed
	}
	return vector
}
