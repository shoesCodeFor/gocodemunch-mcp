package indexing

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsRetryableVectorError(t *testing.T) {
	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error is not retryable",
			err:  nil,
			want: false,
		},
		{
			name: "retryable marker true",
			err:  retryableTestError{retryable: true},
			want: true,
		},
		{
			name: "retryable marker false",
			err:  retryableTestError{retryable: false},
			want: false,
		},
		{
			name: "wrapped retryable marker true",
			err:  fmt.Errorf("wrapped: %w", retryableTestError{retryable: true}),
			want: true,
		},
		{
			name: "explicit marker false overrides deadline signal",
			err: wrappedRetryableTestError{
				cause:     context.DeadlineExceeded,
				retryable: false,
			},
			want: false,
		},
		{
			name: "deadline exceeded is retryable",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "canceled is not retryable",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "timeout method is retryable",
			err:  timeoutTestError{},
			want: true,
		},
		{
			name: "temporary method is retryable",
			err:  temporaryTestError{},
			want: true,
		},
		{
			name: "plain error is not retryable",
			err:  errors.New("plain"),
			want: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := IsRetryableVectorError(testCase.err)
			if got != testCase.want {
				t.Fatalf("expected retryable=%v, got=%v", testCase.want, got)
			}
		})
	}
}

type retryableTestError struct {
	retryable bool
}

func (e retryableTestError) Error() string {
	return "retryable-test-error"
}

func (e retryableTestError) Retryable() bool {
	return e.retryable
}

type wrappedRetryableTestError struct {
	cause     error
	retryable bool
}

func (e wrappedRetryableTestError) Error() string {
	return "wrapped-retryable-test-error"
}

func (e wrappedRetryableTestError) Unwrap() error {
	return e.cause
}

func (e wrappedRetryableTestError) Retryable() bool {
	return e.retryable
}

type timeoutTestError struct{}

func (e timeoutTestError) Error() string {
	return "timeout-test-error"
}

func (e timeoutTestError) Timeout() bool {
	return true
}

type temporaryTestError struct{}

func (e temporaryTestError) Error() string {
	return "temporary-test-error"
}

func (e temporaryTestError) Temporary() bool {
	return true
}
