package orchestration

import "fmt"

func batchFanoutErrorPayload(err error) map[string]any {
	if err == nil {
		return map[string]any{}
	}

	if isContextCancellationError(err) {
		return map[string]any{
			"error": fmt.Sprintf("Batch execution canceled: %v", err),
		}
	}

	if overloadErr, ok := asBatchOverloadError(err); ok {
		return map[string]any{
			"error":             overloadErr.Error(),
			"error_code":        overloadErr.Code(),
			"retryable":         overloadErr.Retryable(),
			"overload_policy":   overloadErr.Policy,
			"batch_item_count":  overloadErr.BatchItemCount,
			"queue_depth_limit": overloadErr.QueueDepthLimit,
		}
	}

	return map[string]any{
		"error": err.Error(),
	}
}
