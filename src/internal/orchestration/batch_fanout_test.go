package orchestration

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
)

func TestRunBatchFanoutSerialModeRunsOneAtATime(t *testing.T) {
	service := New(config.Config{
		ServerName:          "gocodemunch-mcp",
		ServerVersion:       "test",
		FreshnessMode:       "relaxed",
		FanoutMode:          "serial",
		FanoutMaxWorkers:    8,
		FanoutMaxQueueDepth: 32,
		Disabled:            map[string]struct{}{},
	}, Dependencies{})

	var (
		active    int32
		maxActive int32
	)
	err := service.runBatchFanout(context.Background(), 6, func(_ context.Context, _ int) error {
		current := atomic.AddInt32(&active, 1)
		for {
			previous := atomic.LoadInt32(&maxActive)
			if current <= previous || atomic.CompareAndSwapInt32(&maxActive, previous, current) {
				break
			}
		}
		time.Sleep(8 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return nil
	})
	if err != nil {
		t.Fatalf("runBatchFanout returned error: %v", err)
	}
	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("expected serial mode maxActive=1, got %d", got)
	}
}

func TestRunBatchFanoutParallelModeAllowsConcurrentExecution(t *testing.T) {
	service := New(config.Config{
		ServerName:          "gocodemunch-mcp",
		ServerVersion:       "test",
		FreshnessMode:       "relaxed",
		FanoutMode:          "parallel",
		FanoutMaxWorkers:    2,
		FanoutMaxQueueDepth: 32,
		Disabled:            map[string]struct{}{},
	}, Dependencies{})

	workerStarted := make(chan struct{}, 2)
	secondStarted := make(chan struct{})
	done := make(chan error, 1)
	var maxActive int32
	var active int32

	go func() {
		err := service.runBatchFanout(context.Background(), 2, func(_ context.Context, i int) error {
			current := atomic.AddInt32(&active, 1)
			for {
				previous := atomic.LoadInt32(&maxActive)
				if current <= previous || atomic.CompareAndSwapInt32(&maxActive, previous, current) {
					break
				}
			}
			workerStarted <- struct{}{}

			if i == 0 {
				select {
				case <-secondStarted:
				case <-time.After(1 * time.Second):
					return context.DeadlineExceeded
				}
			}
			if i == 1 {
				close(secondStarted)
			}

			atomic.AddInt32(&active, -1)
			return nil
		})
		done <- err
	}()

	select {
	case <-workerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first worker did not start")
	}
	select {
	case <-workerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("second worker did not start")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runBatchFanout returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parallel runBatchFanout timed out")
	}

	if got := atomic.LoadInt32(&maxActive); got < 2 {
		t.Fatalf("expected parallel mode to reach maxActive>=2, got %d", got)
	}
}

func TestRunBatchFanoutQueueDepthLimit(t *testing.T) {
	service := New(config.Config{
		ServerName:          "gocodemunch-mcp",
		ServerVersion:       "test",
		FreshnessMode:       "relaxed",
		FanoutMode:          "parallel",
		FanoutMaxWorkers:    4,
		FanoutMaxQueueDepth: 2,
		Disabled:            map[string]struct{}{},
	}, Dependencies{})

	err := service.runBatchFanout(context.Background(), 3, func(_ context.Context, _ int) error { return nil })
	if err == nil {
		t.Fatal("expected queue depth validation error")
	}
	if got := err.Error(); got != "batch item count 3 exceeds fanout queue depth limit 2" {
		t.Fatalf("unexpected queue depth error: %q", got)
	}
	overloadErr, ok := asBatchOverloadError(err)
	if !ok {
		t.Fatalf("expected batch overload error type, got %T", err)
	}
	if got := overloadErr.Code(); got != fanoutOverloadCodeQueueDepth {
		t.Fatalf("unexpected overload error code: %q", got)
	}
	if !overloadErr.Retryable() {
		t.Fatal("expected overload error to be retryable")
	}
}

func TestRunBatchFanoutQueueDepthDegradePolicyFallsBackToSerial(t *testing.T) {
	service := New(config.Config{
		ServerName:           "gocodemunch-mcp",
		ServerVersion:        "test",
		FreshnessMode:        "relaxed",
		FanoutMode:           "parallel",
		FanoutOverloadPolicy: "degrade",
		FanoutMaxWorkers:     4,
		FanoutMaxQueueDepth:  2,
		RequestTimeoutMS:     0,
		FanoutItemTimeoutMS:  0,
		Disabled:             map[string]struct{}{},
	}, Dependencies{})

	var (
		active    int32
		maxActive int32
		seen      []int
		seenMu    sync.Mutex
	)

	err := service.runBatchFanout(context.Background(), 3, func(_ context.Context, i int) error {
		current := atomic.AddInt32(&active, 1)
		for {
			previous := atomic.LoadInt32(&maxActive)
			if current <= previous || atomic.CompareAndSwapInt32(&maxActive, previous, current) {
				break
			}
		}
		time.Sleep(8 * time.Millisecond)
		seenMu.Lock()
		seen = append(seen, i)
		seenMu.Unlock()
		atomic.AddInt32(&active, -1)
		return nil
	})
	if err != nil {
		t.Fatalf("runBatchFanout returned error: %v", err)
	}
	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("expected degraded serial fallback maxActive=1, got %d", got)
	}
	if !slices.Equal(seen, []int{0, 1, 2}) {
		t.Fatalf("expected degraded serial execution order [0 1 2], got %v", seen)
	}
}

func TestRunBatchFanoutItemTimeoutBudget(t *testing.T) {
	service := New(config.Config{
		ServerName:          "gocodemunch-mcp",
		ServerVersion:       "test",
		FreshnessMode:       "relaxed",
		FanoutMode:          "serial",
		FanoutMaxWorkers:    1,
		FanoutMaxQueueDepth: 16,
		FanoutItemTimeoutMS: 20,
		RequestTimeoutMS:    0,
		Disabled:            map[string]struct{}{},
	}, Dependencies{})

	err := service.runBatchFanout(context.Background(), 1, func(itemCtx context.Context, _ int) error {
		select {
		case <-itemCtx.Done():
			return itemCtx.Err()
		case <-time.After(100 * time.Millisecond):
			return nil
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded from item timeout, got %v", err)
	}
}

func TestRunBatchFanoutSerialModeReturnsParentCancellationFromFinalItem(t *testing.T) {
	service := New(config.Config{
		ServerName:          "gocodemunch-mcp",
		ServerVersion:       "test",
		FreshnessMode:       "relaxed",
		FanoutMode:          "serial",
		FanoutMaxWorkers:    1,
		FanoutMaxQueueDepth: 16,
		FanoutItemTimeoutMS: 0,
		RequestTimeoutMS:    0,
		Disabled:            map[string]struct{}{},
	}, Dependencies{})

	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	err := service.runBatchFanout(parentCtx, 1, func(_ context.Context, _ int) error {
		cancelParent()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected parent cancellation from final serial item, got %v", err)
	}
}

func TestRunBatchFanoutParallelModeEnforcesItemTimeoutWhenHandlerIgnoresContext(t *testing.T) {
	service := New(config.Config{
		ServerName:          "gocodemunch-mcp",
		ServerVersion:       "test",
		FreshnessMode:       "relaxed",
		FanoutMode:          "parallel",
		FanoutMaxWorkers:    2,
		FanoutMaxQueueDepth: 16,
		FanoutItemTimeoutMS: 10,
		RequestTimeoutMS:    0,
		Disabled:            map[string]struct{}{},
	}, Dependencies{})

	err := service.runBatchFanout(context.Background(), 2, func(_ context.Context, _ int) error {
		time.Sleep(40 * time.Millisecond)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded from ignored parallel item timeout, got %v", err)
	}
}
