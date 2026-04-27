package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	defaultVLLMEmbeddingModel     = "bge-m3"
	defaultVLLMEmbeddingBatchSize = 32
	defaultVLLMResponseLimitBytes = 16 << 20
	defaultVLLMEncodingFormat     = "float"
)

type vllmEmbedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format,omitempty"`
}

type vllmEmbedResponse struct {
	Data  []vllmEmbedDatum `json:"data"`
	Model string           `json:"model"`
	Error json.RawMessage  `json:"error"`
}

type vllmEmbedDatum struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

type vllmErrorPayload struct {
	Message string `json:"message"`
}

type vllmError struct {
	operation string
	err       error
	retryable bool
}

func (e *vllmError) Error() string {
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

func (e *vllmError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *vllmError) Retryable() bool {
	if e == nil {
		return false
	}
	return e.retryable
}

func newVLLMError(operation string, err error, retryable bool) error {
	if err == nil {
		return nil
	}
	return &vllmError{
		operation: operation,
		err:       err,
		retryable: retryable,
	}
}

// VLLMEmbedderOption customizes VLLMEmbedder construction.
type VLLMEmbedderOption func(*VLLMEmbedder)

// WithVLLMHTTPClient injects an HTTP client for vLLM transport.
func WithVLLMHTTPClient(client *http.Client) VLLMEmbedderOption {
	return func(embedder *VLLMEmbedder) {
		if client != nil {
			embedder.client = client
		}
	}
}

// WithVLLMBatchSize configures max inputs sent per embeddings request.
func WithVLLMBatchSize(size int) VLLMEmbedderOption {
	return func(embedder *VLLMEmbedder) {
		if size > 0 {
			embedder.batchSize = size
		}
	}
}

// VLLMEmbedder implements indexing.Embedder against OpenAI-compatible APIs.
type VLLMEmbedder struct {
	client         *http.Client
	embedEndpoint  string
	model          string
	apiKey         string
	requestTimeout time.Duration
	batchSize      int
}

// NewVLLMEmbedder builds a batched vLLM embedder targeting /embeddings.
func NewVLLMEmbedder(
	baseURL string,
	model string,
	apiKey string,
	requestTimeout time.Duration,
	optionFns ...VLLMEmbedderOption,
) (*VLLMEmbedder, error) {
	embedEndpoint, err := resolveVLLMEmbedEndpoint(baseURL)
	if err != nil {
		return nil, err
	}

	normalizedModel := strings.TrimSpace(model)
	if normalizedModel == "" {
		normalizedModel = defaultVLLMEmbeddingModel
	}

	embedder := &VLLMEmbedder{
		client:         &http.Client{},
		embedEndpoint:  embedEndpoint,
		model:          normalizedModel,
		apiKey:         strings.TrimSpace(apiKey),
		requestTimeout: requestTimeout,
		batchSize:      defaultVLLMEmbeddingBatchSize,
	}

	for _, option := range optionFns {
		if option == nil {
			continue
		}
		option(embedder)
	}

	if embedder.batchSize <= 0 {
		embedder.batchSize = defaultVLLMEmbeddingBatchSize
	}
	if embedder.client == nil {
		embedder.client = &http.Client{}
	}

	return embedder, nil
}

// Embed generates embeddings for each input string in deterministic order.
func (e *VLLMEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if e == nil {
		return nil, errors.New("vllm embedder is nil")
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

func (e *VLLMEmbedder) embedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	requestPayload := vllmEmbedRequest{
		Model:          e.model,
		Input:          inputs,
		EncodingFormat: defaultVLLMEncodingFormat,
	}

	body, err := json.Marshal(requestPayload)
	if err != nil {
		return nil, newVLLMError("encode vllm embedding request", err, false)
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
		return nil, newVLLMError("build vllm embedding request", err, false)
	}
	request.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	response, err := e.client.Do(request)
	if err != nil {
		return nil, newVLLMError("execute vllm embedding request", err, isRetryableTransportError(err))
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		statusErr := parseVLLMStatusError(response)
		return nil, newVLLMError(
			"execute vllm embedding request",
			statusErr,
			isRetryableHTTPStatus(response.StatusCode),
		)
	}

	payload := vllmEmbedResponse{}
	if err := json.NewDecoder(io.LimitReader(response.Body, defaultVLLMResponseLimitBytes)).Decode(&payload); err != nil {
		return nil, newVLLMError("decode vllm embedding response", err, false)
	}

	if len(payload.Error) > 0 {
		message := extractVLLMErrorMessage(payload.Error)
		if message == "" {
			message = strings.TrimSpace(string(payload.Error))
		}
		if message == "" {
			message = "vllm returned an unknown error payload"
		}
		return nil, newVLLMError(
			"decode vllm embedding response",
			errors.New(message),
			false,
		)
	}

	batchEmbeddings, err := normalizeVLLMEmbeddingData(payload.Data, len(inputs))
	if err != nil {
		return nil, newVLLMError("decode vllm embedding response", err, false)
	}

	if err := validateEmbeddingDimensions(e.model, batchEmbeddings); err != nil {
		return nil, newVLLMError("validate vllm embedding response", err, false)
	}

	return batchEmbeddings, nil
}

func (e *VLLMEmbedder) newRequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
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

func resolveVLLMEmbedEndpoint(baseURL string) (string, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "", errors.New("vllm base url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse vllm base url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("vllm base url must be absolute: %q", trimmed)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("vllm base url must use http or https: %q", trimmed)
	}

	parsed.Path = path.Join("/", strings.TrimSuffix(parsed.Path, "/"), "embeddings")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func normalizeVLLMEmbeddingData(data []vllmEmbedDatum, expected int) ([][]float32, error) {
	if len(data) != expected {
		return nil, fmt.Errorf("embedding count mismatch: expected %d, got %d", expected, len(data))
	}

	embeddings := make([][]float32, expected)
	seen := make([]bool, expected)
	for _, row := range data {
		if row.Index < 0 || row.Index >= expected {
			return nil, fmt.Errorf("embedding index out of range: %d", row.Index)
		}
		if seen[row.Index] {
			return nil, fmt.Errorf("duplicate embedding index: %d", row.Index)
		}
		embeddings[row.Index] = row.Embedding
		seen[row.Index] = true
	}

	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("missing embedding index: %d", i)
		}
	}

	return embeddings, nil
}

func parseVLLMStatusError(response *http.Response) error {
	statusCode := 0
	statusText := ""
	if response != nil {
		statusCode = response.StatusCode
		statusText = response.Status
	}

	bodyBytes, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	bodyText := strings.TrimSpace(string(bodyBytes))
	if bodyText == "" {
		return fmt.Errorf("vllm returned status %d (%s)", statusCode, statusText)
	}

	payload := vllmEmbedResponse{}
	if err := json.Unmarshal(bodyBytes, &payload); err == nil && len(payload.Error) > 0 {
		if message := extractVLLMErrorMessage(payload.Error); message != "" {
			return fmt.Errorf(
				"vllm returned status %d (%s): %s",
				statusCode,
				statusText,
				message,
			)
		}
	}

	return fmt.Errorf("vllm returned status %d (%s): %s", statusCode, statusText, bodyText)
}

func extractVLLMErrorMessage(raw json.RawMessage) string {
	payload := vllmErrorPayload{}
	if err := json.Unmarshal(raw, &payload); err == nil {
		return strings.TrimSpace(payload.Message)
	}

	var message string
	if err := json.Unmarshal(raw, &message); err == nil {
		return strings.TrimSpace(message)
	}

	return ""
}
