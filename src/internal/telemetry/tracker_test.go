package telemetry

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"
)

func TestTrackerRecordCallAggregatesSessionAndCumulative(t *testing.T) {
	now := time.Date(2026, 4, 30, 18, 0, 0, 0, time.UTC)
	tracker := NewTracker(
		map[string]Pricing{
			"claude_code": {InputUSDPerMTok: 3, OutputUSDPerMTok: 15},
			"codex":       {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6},
			"amp":         {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6},
		},
		func() time.Time { return now },
	)

	call := tracker.RecordCall(CallRecord{
		ToolName:          "search_text",
		StartedAt:         now.Add(-250 * time.Millisecond),
		FinishedAt:        now,
		RequestTokens:     120,
		ResponseTokens:    80,
		InputTokensSaved:  40,
		OutputTokensSaved: 10,
	})

	if call.TotalTokens != 200 {
		t.Fatalf("expected total tokens 200, got %d", call.TotalTokens)
	}
	if call.TokensSaved != 50 {
		t.Fatalf("expected tokens saved 50, got %d", call.TokensSaved)
	}
	if !almostEqual(call.CostAvoidedUSD["claude_code"], 0.00027) {
		t.Fatalf("unexpected claude_code avoided cost: %#v", call.CostAvoidedUSD)
	}

	session := tracker.SessionSnapshot()
	if session.CallCount != 1 || session.TotalTokens != 200 || session.TokensSaved != 50 {
		t.Fatalf("unexpected session totals: %#v", session)
	}
	tool := session.ToolBreakdown["search_text"]
	if tool.CallCount != 1 || tool.TotalTokens != 200 || tool.TokensSaved != 50 {
		t.Fatalf("unexpected tool breakdown: %#v", tool)
	}

	cumulative := tracker.CumulativeSnapshot()
	if cumulative.SessionCount != 1 || cumulative.CallCount != 1 {
		t.Fatalf("unexpected cumulative counts: %#v", cumulative)
	}
	if cumulative.FirstRecordedAt != now.Add(-250*time.Millisecond) || cumulative.LastRecordedAt != now {
		t.Fatalf("unexpected cumulative timing: %#v", cumulative)
	}
}

func TestTrackerRestoreCumulativeAddsNewSessionWork(t *testing.T) {
	startedAt := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	currentNow := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	tracker := NewTracker(
		map[string]Pricing{
			"codex": {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6},
		},
		func() time.Time { return currentNow },
	)

	tracker.RestoreCumulative(CumulativeSnapshot{
		FirstRecordedAt:   startedAt,
		LastRecordedAt:    startedAt.Add(2 * time.Hour),
		SessionCount:      2,
		CallCount:         5,
		RequestTokens:     500,
		ResponseTokens:    100,
		TotalTokens:       600,
		InputTokensSaved:  120,
		OutputTokensSaved: 30,
		TokensSaved:       150,
		CostAvoidedUSD:    map[string]float64{"codex": 0.00036},
		ToolBreakdown: map[string]ToolSnapshot{
			"index_repo": {
				CallCount:      5,
				RequestTokens:  500,
				ResponseTokens: 100,
				TotalTokens:    600,
				TokensSaved:    150,
				CostAvoidedUSD: map[string]float64{"codex": 0.00036},
			},
		},
	})

	tracker.RecordCall(CallRecord{
		ToolName:          "search_text",
		StartedAt:         currentNow.Add(-100 * time.Millisecond),
		FinishedAt:        currentNow,
		RequestTokens:     25,
		ResponseTokens:    15,
		InputTokensSaved:  10,
		OutputTokensSaved: 5,
	})

	cumulative := tracker.CumulativeSnapshot()
	if cumulative.SessionCount != 3 || cumulative.CallCount != 6 {
		t.Fatalf("expected restored totals plus one session/call, got %#v", cumulative)
	}
	if cumulative.TotalTokens != 640 || cumulative.TokensSaved != 165 {
		t.Fatalf("unexpected cumulative totals after restore: %#v", cumulative)
	}
	if _, ok := cumulative.ToolBreakdown["search_text"]; !ok {
		t.Fatalf("expected new tool breakdown entry after restore: %#v", cumulative.ToolBreakdown)
	}
}

func TestRuntimePersistsPeriodicallyAndOnClose(t *testing.T) {
	store := &runtimeStoreStub{
		saveCh: make(chan PersistedCumulativeSnapshot, 4),
	}
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	runtime, err := NewRuntime(RuntimeConfig{
		Pricing: map[string]Pricing{
			"codex": {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6},
		},
		Store:            store,
		SnapshotInterval: 10 * time.Millisecond,
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	select {
	case snapshot := <-store.saveCh:
		if snapshot.Cumulative.CallCount != 0 {
			t.Fatalf("expected initial periodic snapshot to be empty, got %#v", snapshot)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected periodic snapshot flush")
	}

	runtime.RecordCall(CallRecord{
		ToolName:          "get_context_bundle",
		StartedAt:         now.Add(-5 * time.Millisecond),
		FinishedAt:        now,
		RequestTokens:     10,
		ResponseTokens:    15,
		InputTokensSaved:  3,
		OutputTokensSaved: 2,
	})

	select {
	case snapshot := <-store.saveCh:
		if snapshot.Cumulative.CallCount != 1 || snapshot.Cumulative.TokensSaved != 5 {
			t.Fatalf("expected periodic snapshot with recorded call, got %#v", snapshot)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected periodic snapshot after call")
	}

	if err := runtime.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	if saves := store.saveCount(); saves < 2 {
		t.Fatalf("expected at least two persisted snapshots, got %d", saves)
	}
}

type runtimeStoreStub struct {
	mu      sync.Mutex
	load    PersistedCumulativeSnapshot
	loadErr error
	saves   []PersistedCumulativeSnapshot
	saveCh  chan PersistedCumulativeSnapshot
}

func (s *runtimeStoreStub) LoadLatestSnapshot(_ context.Context) (PersistedCumulativeSnapshot, error) {
	if s.loadErr != nil {
		return PersistedCumulativeSnapshot{}, s.loadErr
	}
	if s.load.CapturedAt.IsZero() && s.load.Cumulative.CallCount == 0 && s.load.Cumulative.SessionCount == 0 {
		return PersistedCumulativeSnapshot{}, ErrSnapshotNotFound
	}
	return s.load, nil
}

func (s *runtimeStoreStub) SaveSnapshot(
	_ context.Context,
	snapshot PersistedCumulativeSnapshot,
) error {
	s.mu.Lock()
	s.saves = append(s.saves, snapshot)
	s.mu.Unlock()

	if s.saveCh != nil {
		s.saveCh <- snapshot
	}
	return nil
}

func (s *runtimeStoreStub) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saves)
}

func almostEqual(left, right float64) bool {
	return math.Abs(left-right) < 1e-12
}
