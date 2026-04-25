package orchestration

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/watcher"
)

func TestNewRequestContextDisabled(t *testing.T) {
	service := New(config.Config{
		ServerName:       "gocodemunch-mcp",
		ServerVersion:    "test",
		FreshnessMode:    "relaxed",
		RequestTimeoutMS: 0,
		Disabled:         map[string]struct{}{},
	}, Dependencies{})

	base := context.Background()
	derived, cancel := service.newRequestContext(base)
	defer cancel()

	if derived != base {
		t.Fatal("expected disabled request timeout to keep original context")
	}
	if _, ok := derived.Deadline(); ok {
		t.Fatal("expected no derived deadline when timeout is disabled")
	}
}

func TestNewRequestContextAppliesConfiguredBudget(t *testing.T) {
	service := New(config.Config{
		ServerName:       "gocodemunch-mcp",
		ServerVersion:    "test",
		FreshnessMode:    "relaxed",
		RequestTimeoutMS: 30,
		Disabled:         map[string]struct{}{},
	}, Dependencies{})

	derived, cancel := service.newRequestContext(context.Background())
	defer cancel()

	deadline, ok := derived.Deadline()
	if !ok {
		t.Fatal("expected derived context deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		t.Fatalf("expected positive remaining timeout, got %s", remaining)
	}
	if remaining > 60*time.Millisecond {
		t.Fatalf("expected derived timeout budget near configured window, got %s", remaining)
	}
}

func TestNewRequestContextRespectsParentDeadline(t *testing.T) {
	service := New(config.Config{
		ServerName:       "gocodemunch-mcp",
		ServerVersion:    "test",
		FreshnessMode:    "relaxed",
		RequestTimeoutMS: 200,
		Disabled:         map[string]struct{}{},
	}, Dependencies{})

	parent, parentCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer parentCancel()
	parentDeadline, _ := parent.Deadline()

	derived, cancel := service.newRequestContext(parent)
	defer cancel()
	derivedDeadline, ok := derived.Deadline()
	if !ok {
		t.Fatal("expected derived context deadline")
	}
	if derivedDeadline.After(parentDeadline.Add(5 * time.Millisecond)) {
		t.Fatalf("expected derived deadline to stay within parent deadline, parent=%s derived=%s", parentDeadline, derivedDeadline)
	}
}

func TestCallToolUsesRequestTimeoutBudget(t *testing.T) {
	service := New(config.Config{
		ServerName:       "gocodemunch-mcp",
		ServerVersion:    "test",
		FreshnessMode:    "relaxed",
		RequestTimeoutMS: 20,
		Disabled:         map[string]struct{}{},
	}, Dependencies{})

	service.tools["slow_tool"] = Tool{
		Name:        "slow_tool",
		Description: "test-only slow tool",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, _ map[string]any) (map[string]any, error) {
			select {
			case <-ctx.Done():
				return map[string]any{
					"canceled": true,
					"reason":   ctx.Err().Error(),
				}, nil
			case <-time.After(100 * time.Millisecond):
				return map[string]any{"canceled": false}, nil
			}
		},
	}

	payload := service.CallTool(context.Background(), "slow_tool", map[string]any{})
	if canceled, _ := payload["canceled"].(bool); !canceled {
		t.Fatalf("expected request timeout cancellation payload, got %#v", payload)
	}
	if reason, _ := payload["reason"].(string); reason != context.DeadlineExceeded.Error() {
		t.Fatalf("expected deadline exceeded reason, got %#v", payload)
	}
}

func TestCallToolStrictFreshnessTimeoutSkipsNonBatchHandler(t *testing.T) {
	store := mustIndexStore(t)
	repoID := seedRepoIndex(t, store)
	controller := watcher.NewStateController()

	service := New(config.Config{
		ServerName:       "gocodemunch-mcp",
		ServerVersion:    "test",
		FreshnessMode:    "strict",
		RequestTimeoutMS: 30,
		Disabled:         map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
		Watcher:    controller,
	})

	var handlerCalls int32
	service.tools["strict_timeout_probe"] = Tool{
		Name:        "strict_timeout_probe",
		Description: "test-only strict timeout probe",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo": map[string]any{"type": "string"},
			},
		},
		Handler: func(_ context.Context, _ map[string]any) (map[string]any, error) {
			atomic.AddInt32(&handlerCalls, 1)
			return map[string]any{"ok": true}, nil
		},
	}

	controller.MarkReindexStart(repoID)
	started := time.Now()
	payload := service.CallTool(context.Background(), "strict_timeout_probe", map[string]any{
		"repo": repoID,
	})
	elapsed := time.Since(started)

	if got := atomic.LoadInt32(&handlerCalls); got != 0 {
		t.Fatalf("expected strict freshness timeout to skip handler call, got %d (%#v)", got, payload)
	}
	if got, _ := payload["error"].(string); got != "Internal error processing strict_timeout_probe" {
		t.Fatalf("unexpected strict freshness timeout payload: %#v", payload)
	}
	if elapsed < 20*time.Millisecond {
		t.Fatalf("expected strict freshness wait before timeout, elapsed=%s payload=%#v", elapsed, payload)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("expected strict freshness timeout to respect request budget, elapsed=%s payload=%#v", elapsed, payload)
	}
}

func TestCallToolStrictFreshnessTimeoutSkipsBatchFanoutHandler(t *testing.T) {
	store := mustIndexStore(t)
	repoID := seedRepoIndex(t, store)
	controller := watcher.NewStateController()

	service := New(config.Config{
		ServerName:          "gocodemunch-mcp",
		ServerVersion:       "test",
		FreshnessMode:       "strict",
		RequestTimeoutMS:    30,
		FanoutMode:          "parallel",
		FanoutMaxWorkers:    4,
		FanoutMaxQueueDepth: 32,
		Disabled:            map[string]struct{}{},
	}, Dependencies{
		IndexStore: store,
		Watcher:    controller,
	})

	var (
		handlerCalls int32
		itemCalls    int32
	)
	service.tools["strict_batch_timeout_probe"] = Tool{
		Name:        "strict_batch_timeout_probe",
		Description: "test-only strict batch timeout probe",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo": map[string]any{"type": "string"},
			},
		},
		Handler: func(ctx context.Context, _ map[string]any) (map[string]any, error) {
			atomic.AddInt32(&handlerCalls, 1)
			err := service.runBatchFanout(ctx, 6, func(_ context.Context, _ int) error {
				atomic.AddInt32(&itemCalls, 1)
				return nil
			})
			if err != nil {
				return batchFanoutErrorPayload(err), nil
			}
			return map[string]any{"ok": true}, nil
		},
	}

	controller.MarkReindexStart(repoID)
	started := time.Now()
	payload := service.CallTool(context.Background(), "strict_batch_timeout_probe", map[string]any{
		"repo": repoID,
	})
	elapsed := time.Since(started)

	if got := atomic.LoadInt32(&handlerCalls); got != 0 {
		t.Fatalf("expected strict freshness timeout to skip batch handler call, got %d (%#v)", got, payload)
	}
	if got := atomic.LoadInt32(&itemCalls); got != 0 {
		t.Fatalf("expected strict freshness timeout to skip batch item fanout, got %d (%#v)", got, payload)
	}
	if got, _ := payload["error"].(string); got != "Internal error processing strict_batch_timeout_probe" {
		t.Fatalf("unexpected strict freshness batch timeout payload: %#v", payload)
	}
	if elapsed < 20*time.Millisecond {
		t.Fatalf("expected strict freshness wait before timeout, elapsed=%s payload=%#v", elapsed, payload)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("expected strict freshness timeout to respect request budget, elapsed=%s payload=%#v", elapsed, payload)
	}
}
