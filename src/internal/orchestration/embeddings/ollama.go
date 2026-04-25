package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	defaultOllamaEmbeddingModel     = "bge-m3"
	defaultOllamaEmbeddingBatchSize = 32
	defaultOllamaResponseLimitBytes = 16 << 20
	bgeM3EmbeddingDimensions        = 1024
)

type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaEmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
	Embedding  []float32   `json:"embedding"`
	Error      string      `json:"error"`
}

type ollamaError struct {
	operation string
	err       error
	retryable bool
}

func (e *ollamaError) Error() string {
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

func (e *ollamaError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *ollamaError) Retryable() bool {
	if e == nil {
		return false
	}
	return e.retryable
}

func newOllamaError(operation string, err error, retryable bool) error {
	if err == nil {
		return nil
	}
	return &ollamaError{
		operation: operation,
		err:       err,
		retryable: retryable,
	}
}

// OllamaEmbedderOption customizes OllamaEmbedder construction.
type OllamaEmbedderOption func(*OllamaEmbedder)

// WithOllamaHTTPClient injects an HTTP client for Ollama transport.
func WithOllamaHTTPClient(client *http.Client) OllamaEmbedderOption {
	return func(embedder *OllamaEmbedder) {
		if client != nil {
			embedder.client = client
		}
	}
}

// WithOllamaBatchSize configures max inputs sent per embed request.
func WithOllamaBatchSize(size int) OllamaEmbedderOption {
	return func(embedder *OllamaEmbedder) {
		if size > 0 {
			embedder.batchSize = size
		}
	}
}

// OllamaEmbedder implements indexing.Embedder against the Ollama API.
type OllamaEmbedder struct {
	client         *http.Client
	embedEndpoint  string
	model          string
	requestTimeout time.Duration
	batchSize      int
}

// NewOllamaEmbedder builds a batched Ollama embedder targeting /api/embed.
func NewOllamaEmbedder(
	baseURL string,
	model string,
	requestTimeout time.Duration,
	optionFns ...OllamaEmbedderOption,
) (*OllamaEmbedder, error) {
	embedEndpoint, err := resolveOllamaEmbedEndpoint(baseURL)
	if err != nil {
		return nil, err
	}

	normalizedModel := strings.TrimSpace(model)
	if normalizedModel == "" {
		normalizedModel = defaultOllamaEmbeddingModel
	}

	embedder := &OllamaEmbedder{
		client:         &http.Client{},
		embedEndpoint:  embedEndpoint,
		model:          normalizedModel,
		requestTimeout: requestTimeout,
		batchSize:      defaultOllamaEmbeddingBatchSize,
	}

	for _, option := range optionFns {
		if option == nil {
			continue
		}
		option(embedder)
	}

	if embedder.batchSize <= 0 {
		embedder.batchSize = defaultOllamaEmbeddingBatchSize
	}
	if embedder.client == nil {
		embedder.client = &http.Client{}
	}

	return embedder, nil
}

// Embed generates embeddings for each input string in deterministic order.
func (e *OllamaEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if e == nil {
		return nil, errors.New("ollama embedder is nil")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return [][]float32{}, nil
	}

	embeddings := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += e.batchSize {
		end := start + e.batchSize
		if end > len(inputs) {
			end = len(inputs)
		}

		batchEmbeddings, err := e.embedBatch(ctx, inputs[start:end])
		if err != nil {
			return nil, err
		}
		embeddings = append(embeddings, batchEmbeddings...)
	}

	return embeddings, nil
}

func (e *OllamaEmbedder) embedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	requestPayload := ollamaEmbedRequest{
		Model: e.model,
		Input: inputs,
	}

	body, err := json.Marshal(requestPayload)
	if err != nil {
		return nil, newOllamaError("encode ollama embedding request", err, false)
	}

	requestCtx, cancel := e.newRequestContext(ctx)
	defer cancel()

	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		e.embedEndpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, newOllamaError("build ollama embedding request", err, false)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := e.client.Do(request)
	if err != nil {
		return nil, newOllamaError("execute ollama embedding request", err, isRetryableTransportError(err))
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		statusErr := parseOllamaStatusError(response)
		return nil, newOllamaError(
			"execute ollama embedding request",
			statusErr,
			isRetryableHTTPStatus(response.StatusCode),
		)
	}

	payload := ollamaEmbedResponse{}
	if err := json.NewDecoder(io.LimitReader(response.Body, defaultOllamaResponseLimitBytes)).Decode(&payload); err != nil {
		return nil, newOllamaError("decode ollama embedding response", err, false)
	}
	if strings.TrimSpace(payload.Error) != "" {
		return nil, newOllamaError(
			"decode ollama embedding response",
			errors.New(strings.TrimSpace(payload.Error)),
			false,
		)
	}

	batchEmbeddings := payload.Embeddings
	if len(batchEmbeddings) == 0 && len(payload.Embedding) > 0 {
		batchEmbeddings = [][]float32{payload.Embedding}
	}
	if len(batchEmbeddings) != len(inputs) {
		return nil, newOllamaError(
			"decode ollama embedding response",
			fmt.Errorf("embedding count mismatch: expected %d, got %d", len(inputs), len(batchEmbeddings)),
			false,
		)
	}

	if err := validateEmbeddingDimensions(e.model, batchEmbeddings); err != nil {
		return nil, newOllamaError("validate ollama embedding response", err, false)
	}
	return batchEmbeddings, nil
}

func (e *OllamaEmbedder) newRequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if e.requestTimeout <= 0 {
		return ctx, func() {}
	}

	timeout := e.requestTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ctx, func() {}
		}
		if remaining < timeout {
			timeout = remaining
		}
	}

	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func resolveOllamaEmbedEndpoint(baseURL string) (string, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "", errors.New("ollama base url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse ollama base url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("ollama base url must be absolute: %q", trimmed)
	}

	parsed.Path = path.Join("/", strings.TrimSuffix(parsed.Path, "/"), "api", "embed")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func parseOllamaStatusError(response *http.Response) error {
	statusCode := 0
	statusText := ""
	if response != nil {
		statusCode = response.StatusCode
		statusText = response.Status
	}

	bodyBytes, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	bodyText := strings.TrimSpace(string(bodyBytes))
	if bodyText == "" {
		return fmt.Errorf("ollama returned status %d (%s)", statusCode, statusText)
	}

	payload := ollamaEmbedResponse{}
	if err := json.Unmarshal(bodyBytes, &payload); err == nil && strings.TrimSpace(payload.Error) != "" {
		return fmt.Errorf(
			"ollama returned status %d (%s): %s",
			statusCode,
			statusText,
			strings.TrimSpace(payload.Error),
		)
	}
	return fmt.Errorf("ollama returned status %d (%s): %s", statusCode, statusText, bodyText)
}

func validateEmbeddingDimensions(model string, embeddings [][]float32) error {
	expectedDimensions, hasExpectedDimensions := embeddingDimensionsForModel(model)
	for i, embedding := range embeddings {
		if len(embedding) == 0 {
			return fmt.Errorf("embedding at index %d was empty", i)
		}
		if hasExpectedDimensions && len(embedding) != expectedDimensions {
			return fmt.Errorf(
				"embedding dimension mismatch for model %q at index %d: expected %d, got %d",
				model,
				i,
				expectedDimensions,
				len(embedding),
			)
		}
	}
	return nil
}

func embeddingDimensionsForModel(model string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "bge-m3":
		return bgeM3EmbeddingDimensions, true
	default:
		return 0, false
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
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true
		}
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
