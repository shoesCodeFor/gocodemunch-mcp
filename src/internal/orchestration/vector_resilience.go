package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
)

const (
	defaultVectorOperationMaxAttempts = 3
	defaultVectorUpsertBatchSize      = 128
	defaultVectorDeleteBatchSize      = 256
	vectorRetryBackoffStep            = 75 * time.Millisecond
	vectorRetryBackoffMax             = 300 * time.Millisecond
)

func (s *Service) runVectorOperationWithRetry(
	ctx context.Context,
	operation string,
	namespace string,
	details map[string]any,
	runner func(context.Context) error,
) error {
	if runner == nil {
		return errors.New("vector operation runner is required")
	}

	maxAttempts := defaultVectorOperationMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		opCtx, cancel := s.newVectorOperationContext(ctx)
		err := runner(opCtx)
		cancel()
		if err == nil {
			if attempt > 1 {
				s.logVectorOperation(
					"vector_operation_recovered",
					operation,
					namespace,
					attempt,
					maxAttempts,
					true,
					nil,
					details,
				)
			}
			return nil
		}

		lastErr = err
		retryable := indexing.IsRetryableVectorError(err)
		s.logVectorOperation(
			"vector_operation_failure",
			operation,
			namespace,
			attempt,
			maxAttempts,
			retryable,
			err,
			details,
		)
		if !retryable || attempt == maxAttempts {
			break
		}

		backoff := vectorRetryDelay(attempt)
		if waitErr := waitVectorRetryBackoff(ctx, backoff); waitErr != nil {
			return err
		}
	}

	return lastErr
}

func (s *Service) newVectorOperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeoutMS := 0
	if s != nil {
		timeoutMS = s.cfg.VectorQueryTimeoutMS
	}
	if timeoutMS <= 0 {
		return ctx, func() {}
	}

	timeout := time.Duration(timeoutMS) * time.Millisecond
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

func (s *Service) vectorUpsertBatchSize() int {
	if defaultVectorUpsertBatchSize <= 0 {
		return 1
	}
	return defaultVectorUpsertBatchSize
}

func (s *Service) vectorDeleteBatchSize() int {
	if defaultVectorDeleteBatchSize <= 0 {
		return 1
	}
	return defaultVectorDeleteBatchSize
}

func vectorRetryDelay(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	delay := time.Duration(attempt) * vectorRetryBackoffStep
	if delay > vectorRetryBackoffMax {
		delay = vectorRetryBackoffMax
	}
	if delay <= 0 {
		delay = vectorRetryBackoffStep
	}
	return delay
}

func waitVectorRetryBackoff(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func splitVectorRecordBatches(records []indexing.VectorRecord, batchSize int) [][]indexing.VectorRecord {
	if len(records) == 0 {
		return nil
	}
	if batchSize <= 0 {
		batchSize = len(records)
	}

	batches := make([][]indexing.VectorRecord, 0, (len(records)+batchSize-1)/batchSize)
	for start := 0; start < len(records); start += batchSize {
		end := start + batchSize
		if end > len(records) {
			end = len(records)
		}
		batches = append(batches, records[start:end])
	}
	return batches
}

func splitVectorIDBatches(ids []string, batchSize int) [][]string {
	if len(ids) == 0 {
		return nil
	}
	if batchSize <= 0 {
		batchSize = len(ids)
	}

	batches := make([][]string, 0, (len(ids)+batchSize-1)/batchSize)
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		batches = append(batches, ids[start:end])
	}
	return batches
}

func (s *Service) logVectorOperation(
	event string,
	operation string,
	namespace string,
	attempt int,
	maxAttempts int,
	retryable bool,
	err error,
	details map[string]any,
) {
	payload := map[string]any{
		"event":              strings.TrimSpace(event),
		"component":          "vector_resilience",
		"operation":          strings.TrimSpace(operation),
		"namespace":          strings.TrimSpace(namespace),
		"attempt":            attempt,
		"max_attempts":       maxAttempts,
		"retryable":          retryable,
		"vector_backend":     strings.ToLower(strings.TrimSpace(s.cfg.VectorBackend)),
		"embedding_provider": strings.ToLower(strings.TrimSpace(s.cfg.EmbeddingProvider)),
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	for key, value := range details {
		normalizedKey := strings.TrimSpace(key)
		if normalizedKey == "" {
			continue
		}
		payload[normalizedKey] = value
	}

	body, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		log.Printf(
			`{"event":"vector_resilience_log_marshal_error","component":"vector_resilience","operation":"%s","namespace":"%s","attempt":%d,"max_attempts":%d,"retryable":%t,"error":"%v","marshal_error":"%v"}`,
			strings.TrimSpace(operation),
			strings.TrimSpace(namespace),
			attempt,
			maxAttempts,
			retryable,
			err,
			marshalErr,
		)
		return
	}
	log.Printf("%s", body)
}
