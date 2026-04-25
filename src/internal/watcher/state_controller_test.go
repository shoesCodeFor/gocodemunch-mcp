package watcher

import (
	"context"
	"testing"
	"time"
)

func TestWaitForFreshFailureEscalation(t *testing.T) {
	controller := NewStateController()

	unknown, err := controller.WaitForFresh(context.Background(), "local/unknown", 50)
	if err != nil {
		t.Fatalf("unexpected wait error for unknown repo: %v", err)
	}
	if !unknown.Fresh {
		t.Fatalf("expected unknown repo to be fresh: %#v", unknown)
	}

	repo := "local/repo-a"
	controller.MarkReindexStart(repo)
	controller.MarkReindexFailed(repo, "failure-1")

	transient, err := controller.WaitForFresh(context.Background(), repo, 50)
	if err != nil {
		t.Fatalf("unexpected wait error after first failure: %v", err)
	}
	if !transient.Fresh {
		t.Fatalf("expected first failure to remain tolerant: %#v", transient)
	}
	if transient.ReindexFailures != 0 || transient.LastError != "" {
		t.Fatalf("expected first failure to hide escalation details: %#v", transient)
	}

	queryTransient, err := controller.Query(context.Background(), repo)
	if err != nil {
		t.Fatalf("query after first failure: %v", err)
	}
	if !queryTransient.IndexStale || queryTransient.ReindexInProgress {
		t.Fatalf("unexpected stale status after first failure: %#v", queryTransient)
	}

	controller.MarkReindexStart(repo)
	controller.MarkReindexFailed(repo, "failure-2")

	escalated, err := controller.WaitForFresh(context.Background(), repo, 50)
	if err != nil {
		t.Fatalf("unexpected wait error after second failure: %v", err)
	}
	if escalated.Fresh {
		t.Fatalf("expected second failure to escalate as not-fresh: %#v", escalated)
	}
	if escalated.ReindexFailures != 2 || escalated.LastError != "failure-2" {
		t.Fatalf("unexpected escalation details: %#v", escalated)
	}

	queryEscalated, err := controller.Query(context.Background(), repo)
	if err != nil {
		t.Fatalf("query after second failure: %v", err)
	}
	if queryEscalated.ReindexFailures != 2 || queryEscalated.LastError != "failure-2" {
		t.Fatalf("expected query to expose persistent failure details: %#v", queryEscalated)
	}
}

func TestWaitForFreshWaitsForAllOverlappingReindexRuns(t *testing.T) {
	controller := NewStateController()
	repo := "local/repo-overlap"

	controller.MarkReindexStart(repo)
	controller.MarkReindexStart(repo)

	waitDone := make(chan struct{})
	var (
		waitStatus Status
		waitErr    error
	)
	go func() {
		waitStatus, waitErr = controller.WaitForFresh(context.Background(), repo, 250)
		close(waitDone)
	}()

	controller.MarkReindexDone(repo)

	select {
	case <-waitDone:
		t.Fatalf("wait_for_fresh returned before final overlapping run finished: status=%#v err=%v", waitStatus, waitErr)
	case <-time.After(20 * time.Millisecond):
	}

	controller.MarkReindexDone(repo)

	select {
	case <-waitDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("wait_for_fresh did not unblock after final overlapping run completed")
	}

	if waitErr != nil {
		t.Fatalf("unexpected wait error after final completion: %v", waitErr)
	}
	if !waitStatus.Fresh || waitStatus.ReindexInProgress {
		t.Fatalf("expected fresh status after final completion: %#v", waitStatus)
	}
}

func TestQueryStaysInProgressWhenOneOverlappingRunFails(t *testing.T) {
	controller := NewStateController()
	repo := "local/repo-overlap-failure"

	controller.MarkReindexStart(repo)
	controller.MarkReindexStart(repo)
	controller.MarkReindexFailed(repo, "first-run-failed")

	duringOverlap, err := controller.Query(context.Background(), repo)
	if err != nil {
		t.Fatalf("query during overlap: %v", err)
	}
	if !duringOverlap.ReindexInProgress {
		t.Fatalf("expected reindex to remain in progress while one run is still active: %#v", duringOverlap)
	}
	if !duringOverlap.IndexStale {
		t.Fatalf("expected index to remain stale during overlap: %#v", duringOverlap)
	}

	controller.MarkReindexDone(repo)

	afterCompletion, err := controller.Query(context.Background(), repo)
	if err != nil {
		t.Fatalf("query after overlap completion: %v", err)
	}
	if afterCompletion.ReindexInProgress {
		t.Fatalf("expected reindex to settle after final completion: %#v", afterCompletion)
	}
}
