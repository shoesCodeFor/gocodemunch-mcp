package orchestration

import (
	"context"
	"testing"
)

func TestBatchFanoutErrorPayloadUsesStructuredOverloadEnvelope(t *testing.T) {
	payload := batchFanoutErrorPayload(&batchOverloadError{
		BatchItemCount:  9,
		QueueDepthLimit: 4,
		Policy:          fanoutOverloadPolicyReject,
	})

	if got, _ := payload["error"].(string); got != "batch item count 9 exceeds fanout queue depth limit 4" {
		t.Fatalf("unexpected overload error message: %#v", payload)
	}
	if got, _ := payload["error_code"].(string); got != fanoutOverloadCodeQueueDepth {
		t.Fatalf("unexpected overload error code: %#v", payload)
	}
	if got, _ := payload["retryable"].(bool); !got {
		t.Fatalf("expected overload retryable=true: %#v", payload)
	}
	if got, _ := payload["overload_policy"].(string); got != fanoutOverloadPolicyReject {
		t.Fatalf("unexpected overload policy: %#v", payload)
	}
	if got, _ := payload["batch_item_count"].(int); got != 9 {
		t.Fatalf("unexpected batch_item_count: %#v", payload)
	}
	if got, _ := payload["queue_depth_limit"].(int); got != 4 {
		t.Fatalf("unexpected queue_depth_limit: %#v", payload)
	}
}

func TestBatchFanoutErrorPayloadPreservesCancellationEnvelope(t *testing.T) {
	payload := batchFanoutErrorPayload(context.DeadlineExceeded)
	if got, _ := payload["error"].(string); got != "Batch execution canceled: context deadline exceeded" {
		t.Fatalf("unexpected cancellation payload: %#v", payload)
	}
}
