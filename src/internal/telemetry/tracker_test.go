package telemetry

import (
	"context"
	"errors"
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

func TestTrackerRecordCallSupportsLogicalCallWeights(t *testing.T) {
	now := time.Date(2026, 5, 1, 14, 0, 0, 0, time.UTC)
	tracker := NewTracker(
		map[string]Pricing{
			"codex": {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6},
		},
		func() time.Time { return now },
	)

	call := tracker.RecordCall(CallRecord{
		ToolName:          "get_file_outline",
		StartedAt:         now.Add(-40 * time.Millisecond),
		FinishedAt:        now,
		RequestTokens:     20,
		ResponseTokens:    30,
		InputTokensSaved:  8,
		OutputTokensSaved: 4,
		LogicalCalls:      3,
	})

	if call.LogicalCalls != 3 {
		t.Fatalf("expected logical calls to round-trip in call snapshot, got %#v", call)
	}

	session := tracker.SessionSnapshot()
	if session.CallCount != 3 {
		t.Fatalf("expected weighted logical calls in session snapshot, got %#v", session)
	}
	if tool := session.ToolBreakdown["get_file_outline"]; tool.CallCount != 3 {
		t.Fatalf("expected weighted logical calls in tool breakdown, got %#v", session.ToolBreakdown)
	}

	cumulative := tracker.CumulativeSnapshot()
	if cumulative.CallCount != 3 || cumulative.SessionCount != 1 {
		t.Fatalf("expected weighted logical calls in cumulative snapshot, got %#v", cumulative)
	}
	if cumulative.TokensSaved != 12 {
		t.Fatalf("expected token totals to remain based on serialized payload size, got %#v", cumulative)
	}
}

func TestTrackerRecordCallNormalizesNegativeAndBlankValues(t *testing.T) {
	now := time.Date(2026, 5, 1, 15, 0, 0, 0, time.UTC)
	tracker := NewTracker(
		map[string]Pricing{
			"codex": {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6},
		},
		func() time.Time { return now },
	)

	call := tracker.RecordCall(CallRecord{
		ToolName:          "   ",
		RequestTokens:     -10,
		ResponseTokens:    -5,
		InputTokensSaved:  -3,
		OutputTokensSaved: -1,
	})

	if call.ToolName != "unknown" {
		t.Fatalf("expected blank tool name to normalize to unknown, got %#v", call)
	}
	if call.StartedAt != now || call.FinishedAt != now {
		t.Fatalf("expected zero timestamps to normalize to now, got %#v", call)
	}
	if call.DurationMS != 0 {
		t.Fatalf("expected zero duration for normalized instant call, got %#v", call)
	}
	if call.RequestTokens != 0 || call.ResponseTokens != 0 || call.TotalTokens != 0 {
		t.Fatalf("expected negative token counts to clamp to zero, got %#v", call)
	}
	if call.InputTokensSaved != 0 || call.OutputTokensSaved != 0 || call.TokensSaved != 0 {
		t.Fatalf("expected negative saved token counts to clamp to zero, got %#v", call)
	}
	if !almostEqual(call.CostAvoidedUSD["codex"], 0) {
		t.Fatalf("expected zero avoided cost after clamping negatives, got %#v", call.CostAvoidedUSD)
	}

	session := tracker.SessionSnapshot()
	if tool, ok := session.ToolBreakdown["unknown"]; !ok {
		t.Fatalf("expected normalized unknown tool breakdown entry, got %#v", session.ToolBreakdown)
	} else if tool.CallCount != 1 || tool.TokensSaved != 0 {
		t.Fatalf("unexpected normalized tool breakdown: %#v", tool)
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
		PricingProfileVersion: "pricing-v2026-05-01",
		Store:                 store,
		SnapshotInterval:      10 * time.Millisecond,
		Now:                   func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	select {
	case snapshot := <-store.saveCh:
		if snapshot.Cumulative.CallCount != 0 {
			t.Fatalf("expected initial periodic snapshot to be empty, got %#v", snapshot)
		}
		if snapshot.PricingProfileVersion != "pricing-v2026-05-01" {
			t.Fatalf("expected pricing profile version on persisted snapshot, got %#v", snapshot)
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
		if snapshot.PricingProfileVersion != "pricing-v2026-05-01" {
			t.Fatalf("expected pricing profile version on updated persisted snapshot, got %#v", snapshot)
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

func TestRuntimeRestoresPersistedCumulativeAndSkipsRedundantFlushes(t *testing.T) {
	now := time.Date(2026, 5, 1, 16, 0, 0, 0, time.UTC)
	store := &runtimeStoreStub{
		load: PersistedCumulativeSnapshot{
			CapturedAt: now.Add(-time.Hour),
			Cumulative: CumulativeSnapshot{
				FirstRecordedAt:   now.Add(-48 * time.Hour),
				LastRecordedAt:    now.Add(-time.Hour),
				SessionCount:      2,
				CallCount:         4,
				RequestTokens:     90,
				ResponseTokens:    30,
				TotalTokens:       120,
				InputTokensSaved:  30,
				OutputTokensSaved: 10,
				TokensSaved:       40,
				CostAvoidedUSD:    map[string]float64{"codex": 0.000105},
				ToolBreakdown: map[string]ToolSnapshot{
					"seeded_tool": {
						CallCount:      4,
						RequestTokens:  90,
						ResponseTokens: 30,
						TotalTokens:    120,
						TokensSaved:    40,
						CostAvoidedUSD: map[string]float64{"codex": 0.000105},
					},
				},
			},
		},
	}

	runtime, err := NewRuntime(RuntimeConfig{
		Pricing: map[string]Pricing{
			"codex": {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6},
		},
		PricingProfileVersion: "pricing-v2026-05-01",
		Store:                 store,
		Now:                   func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	restored := runtime.CumulativeSnapshot()
	if restored.SessionCount != 2 || restored.CallCount != 4 || restored.TokensSaved != 40 {
		t.Fatalf("expected restored cumulative snapshot, got %#v", restored)
	}

	if err := runtime.Flush(context.Background()); err != nil {
		t.Fatalf("flush restored runtime: %v", err)
	}
	if saves := store.saveCount(); saves != 1 {
		t.Fatalf("expected first flush to persist restored snapshot once, got %d", saves)
	}

	if err := runtime.Flush(context.Background()); err != nil {
		t.Fatalf("flush without changes: %v", err)
	}
	if saves := store.saveCount(); saves != 1 {
		t.Fatalf("expected redundant flush to be skipped, got %d saves", saves)
	}

	runtime.RecordCall(CallRecord{
		ToolName:          "search_text",
		StartedAt:         now.Add(-250 * time.Millisecond),
		FinishedAt:        now,
		RequestTokens:     20,
		ResponseTokens:    10,
		InputTokensSaved:  5,
		OutputTokensSaved: 2,
	})
	if err := runtime.Flush(context.Background()); err != nil {
		t.Fatalf("flush after new call: %v", err)
	}
	if saves := store.saveCount(); saves != 2 {
		t.Fatalf("expected changed revision to persist a second snapshot, got %d", saves)
	}
	if got := store.saves[len(store.saves)-1].PricingProfileVersion; got != "pricing-v2026-05-01" {
		t.Fatalf("expected restored runtime flush to preserve pricing profile version, got %#v", store.saves)
	}

	updated := runtime.CumulativeSnapshot()
	if updated.SessionCount != 3 || updated.CallCount != 5 || updated.TokensSaved != 47 {
		t.Fatalf("expected restored totals plus new session call, got %#v", updated)
	}
}

func TestRuntimeFlushPersistsCallEventsAndRequeuesOnFailure(t *testing.T) {
	now := time.Date(2026, 5, 1, 18, 0, 0, 0, time.UTC)
	store := &runtimeStoreStub{
		eventErrs: []error{errors.New("sqlite unavailable")},
	}

	runtime, err := NewRuntime(RuntimeConfig{
		Pricing: map[string]Pricing{
			"codex": {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6},
		},
		PricingProfileVersion: "pricing-v2026-05-01",
		Store:                 store,
		Now:                   func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	runtime.RecordCall(CallRecord{
		ToolName:          "get_context_bundle",
		StartedAt:         now.Add(-20 * time.Millisecond),
		FinishedAt:        now,
		RequestTokens:     12,
		ResponseTokens:    8,
		InputTokensSaved:  4,
		OutputTokensSaved: 3,
	})

	if err := runtime.Flush(context.Background()); err == nil {
		t.Fatal("expected first flush to fail while call-event persistence is unavailable")
	}
	if saves := store.saveCount(); saves != 0 {
		t.Fatalf("expected snapshot save to be skipped on call-event failure, got %d saves", saves)
	}
	if batches := store.eventBatchCount(); batches != 0 {
		t.Fatalf("expected failed call-event batch not to be recorded, got %d batches", batches)
	}

	if err := runtime.Flush(context.Background()); err != nil {
		t.Fatalf("retry flush after call-event failure: %v", err)
	}
	if saves := store.saveCount(); saves != 1 {
		t.Fatalf("expected snapshot save after successful retry, got %d", saves)
	}
	if batches := store.eventBatchCount(); batches != 1 {
		t.Fatalf("expected one persisted call-event batch after retry, got %d", batches)
	}
	if len(store.events[0]) != 1 || store.events[0][0].Call.ToolName != "get_context_bundle" {
		t.Fatalf("unexpected persisted call-event batch after retry: %#v", store.events)
	}
	if got := store.events[0][0].PricingProfileVersion; got != "pricing-v2026-05-01" {
		t.Fatalf("expected persisted call event to include pricing profile version, got %#v", store.events)
	}
}

func TestRuntimeQueryTrendsAggregatesPersistedCallEvents(t *testing.T) {
	now := time.Date(2026, 5, 1, 18, 0, 0, 0, time.UTC)
	store := &runtimeStoreStub{
		loadedEvents: []PersistedCallEvent{
			{
				CapturedAt: now.Add(-2 * time.Hour),
				Call: CallSnapshot{
					ToolName:          "search_text",
					StartedAt:         now.Add(-2*time.Hour - 2*time.Second),
					FinishedAt:        now.Add(-2 * time.Hour),
					RequestTokens:     12,
					ResponseTokens:    8,
					TotalTokens:       20,
					InputTokensSaved:  5,
					OutputTokensSaved: 3,
					TokensSaved:       8,
					LogicalCalls:      2,
					CostAvoidedUSD: map[string]float64{
						"codex": 0.0000255,
					},
				},
			},
			{
				CapturedAt: now.Add(-36 * time.Hour),
				Call: CallSnapshot{
					ToolName:          "get_context_bundle",
					StartedAt:         now.Add(-36*time.Hour - 3*time.Second),
					FinishedAt:        now.Add(-36 * time.Hour),
					RequestTokens:     20,
					ResponseTokens:    10,
					TotalTokens:       30,
					InputTokensSaved:  7,
					OutputTokensSaved: 4,
					TokensSaved:       11,
					LogicalCalls:      1,
					CostAvoidedUSD: map[string]float64{
						"codex": 0.0000345,
					},
				},
			},
		},
	}

	runtime, err := NewRuntime(RuntimeConfig{
		Pricing: map[string]Pricing{
			"codex": {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6},
		},
		PricingProfileVersion: "pricing-v2026-05-01",
		Store:                 store,
		Now:                   func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	trends, err := runtime.QueryTrends(context.Background(), TrendQuery{
		Windows: []TrendWindow{TrendWindowLast24H, TrendWindowLast7D},
	})
	if err != nil {
		t.Fatalf("query trends: %v", err)
	}

	last24h := trends[string(TrendWindowLast24H)]
	if last24h.CallCount != 2 || last24h.TokensSaved != 8 {
		t.Fatalf("unexpected last_24h trend snapshot: %#v", last24h)
	}
	if tool := last24h.ToolBreakdown["search_text"]; tool.CallCount != 2 || tool.TokensSaved != 8 {
		t.Fatalf("expected last_24h per-tool rollup for search_text, got %#v", last24h.ToolBreakdown)
	}
	if competitor := last24h.CompetitorBreakdown["codex"]; competitor.CostAvoidedUSD != 0.0000255 {
		t.Fatalf("unexpected last_24h competitor rollup: %#v", last24h.CompetitorBreakdown)
	}

	last7d := trends[string(TrendWindowLast7D)]
	if last7d.CallCount != 3 || last7d.TokensSaved != 19 {
		t.Fatalf("unexpected last_7d trend snapshot: %#v", last7d)
	}
	if tool := last7d.ToolBreakdown["get_context_bundle"]; tool.CallCount != 1 || tool.TokensSaved != 11 {
		t.Fatalf("expected last_7d rollup to include persisted get_context_bundle event, got %#v", last7d.ToolBreakdown)
	}
	if len(last7d.Points) != 8 {
		t.Fatalf("expected calendar-aligned daily buckets for last_7d window, got %#v", last7d.Points)
	}
	if loadedSince := store.lastLoadedSince(); !loadedSince.Equal(now.Add(-7 * 24 * time.Hour)) {
		t.Fatalf("expected loader to read from earliest requested window, got %s", loadedSince)
	}
}

type runtimeStoreStub struct {
	mu           sync.Mutex
	load         PersistedCumulativeSnapshot
	loadErr      error
	saves        []PersistedCumulativeSnapshot
	saveCh       chan PersistedCumulativeSnapshot
	events       [][]PersistedCallEvent
	eventErrs    []error
	loadedEvents []PersistedCallEvent
	loadedSince  []time.Time
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

func (s *runtimeStoreStub) SaveCallEvents(
	_ context.Context,
	events []PersistedCallEvent,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.eventErrs) > 0 {
		err := s.eventErrs[0]
		s.eventErrs = s.eventErrs[1:]
		if err != nil {
			return err
		}
	}

	batch := append([]PersistedCallEvent(nil), events...)
	s.events = append(s.events, batch)
	return nil
}

func (s *runtimeStoreStub) LoadCallEvents(
	_ context.Context,
	since time.Time,
) ([]PersistedCallEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.loadedSince = append(s.loadedSince, since)
	events := make([]PersistedCallEvent, 0, len(s.loadedEvents))
	for _, event := range s.loadedEvents {
		if event.CapturedAt.Before(since) {
			continue
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *runtimeStoreStub) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saves)
}

func (s *runtimeStoreStub) eventBatchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func (s *runtimeStoreStub) lastLoadedSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.loadedSince) == 0 {
		return time.Time{}
	}
	return s.loadedSince[len(s.loadedSince)-1]
}

func almostEqual(left, right float64) bool {
	return math.Abs(left-right) < 1e-12
}
