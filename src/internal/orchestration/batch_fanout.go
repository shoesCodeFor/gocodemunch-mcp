package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	fanoutModeSerial             = "serial"
	fanoutModeParallel           = "parallel"
	fanoutOverloadPolicyReject   = "reject"
	fanoutOverloadPolicyDegrade  = "degrade"
	fanoutOverloadCodeQueueDepth = "fanout_queue_depth_exceeded"
)

func (s *Service) runBatchFanout(
	ctx context.Context,
	total int,
	run func(itemCtx context.Context, index int) error,
) error {
	if total <= 0 {
		return nil
	}
	if run == nil {
		return nil
	}

	degradedToSerial, err := s.shouldDegradeBatchFanout(total)
	if err != nil {
		return err
	}

	mode, maxWorkers := s.batchFanoutPolicy()
	if degradedToSerial {
		mode = fanoutModeSerial
	}
	if mode != fanoutModeParallel || total == 1 || maxWorkers <= 1 {
		for i := 0; i < total; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			itemCtx, cancel := s.newBatchItemContext(ctx)
			err := runBatchFanoutItem(itemCtx, i, run)
			cancel()
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}

	if maxWorkers > total {
		maxWorkers = total
	}

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan int, total)
	for i := 0; i < total; i++ {
		jobs <- i
	}
	close(jobs)

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	setErr := func(err error) {
		if err == nil {
			return
		}
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	for workerID := 0; workerID < maxWorkers; workerID++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if err := workerCtx.Err(); err != nil {
					// If parent context was canceled, propagate that reason.
					if ctxErr := ctx.Err(); ctxErr != nil {
						setErr(ctxErr)
					}
					return
				}
				itemCtx, cancel := s.newBatchItemContext(workerCtx)
				err := runBatchFanoutItem(itemCtx, index, run)
				cancel()
				if err != nil {
					setErr(err)
					return
				}
			}
		}()
	}

	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s *Service) batchFanoutPolicy() (mode string, maxWorkers int) {
	mode = strings.ToLower(strings.TrimSpace(s.cfg.FanoutMode))
	if mode != fanoutModeParallel {
		mode = fanoutModeSerial
	}

	maxWorkers = s.cfg.FanoutMaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = 1
	}
	return mode, maxWorkers
}

func (s *Service) shouldDegradeBatchFanout(total int) (bool, error) {
	maxDepth := s.cfg.FanoutMaxQueueDepth
	if maxDepth <= 0 {
		return false, nil
	}
	if total <= maxDepth {
		return false, nil
	}

	if s.batchFanoutOverloadPolicy() == fanoutOverloadPolicyDegrade {
		return true, nil
	}

	return false, &batchOverloadError{
		BatchItemCount:  total,
		QueueDepthLimit: maxDepth,
		Policy:          fanoutOverloadPolicyReject,
	}
}

func (s *Service) batchFanoutOverloadPolicy() string {
	policy := strings.ToLower(strings.TrimSpace(s.cfg.FanoutOverloadPolicy))
	if policy != fanoutOverloadPolicyDegrade {
		return fanoutOverloadPolicyReject
	}
	return fanoutOverloadPolicyDegrade
}

func (s *Service) newBatchItemContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeoutMS := s.cfg.FanoutItemTimeoutMS
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

func runBatchFanoutItem(
	itemCtx context.Context,
	index int,
	run func(itemCtx context.Context, index int) error,
) error {
	if err := itemCtx.Err(); err != nil {
		return err
	}
	if err := run(itemCtx, index); err != nil {
		return err
	}
	if err := itemCtx.Err(); err != nil {
		return err
	}
	return nil
}

func isContextCancellationError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

type batchOverloadError struct {
	BatchItemCount  int
	QueueDepthLimit int
	Policy          string
}

func (e *batchOverloadError) Error() string {
	return fmt.Sprintf(
		"batch item count %d exceeds fanout queue depth limit %d",
		e.BatchItemCount,
		e.QueueDepthLimit,
	)
}

func (e *batchOverloadError) Code() string {
	return fanoutOverloadCodeQueueDepth
}

func (e *batchOverloadError) Retryable() bool {
	return true
}

func asBatchOverloadError(err error) (*batchOverloadError, bool) {
	var overloadErr *batchOverloadError
	if !errors.As(err, &overloadErr) {
		return nil, false
	}
	return overloadErr, true
}
