package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
)

func TestNormalizeTokenSavingsModesRequiresExplicitBothModes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		modes   []string
		wantErr string
	}{
		{
			name:    "empty",
			modes:   nil,
			wantErr: "modes must be non-empty",
		},
		{
			name:    "missing without mcp",
			modes:   []string{tokenSavingsModeWithMCP},
			wantErr: `must include "without_mcp"`,
		},
		{
			name:    "duplicate",
			modes:   []string{tokenSavingsModeWithMCP, tokenSavingsModeWithMCP, tokenSavingsModeWithoutMCP},
			wantErr: `duplicate mode "with_mcp"`,
		},
		{
			name:    "unsupported",
			modes:   []string{tokenSavingsModeWithMCP, "manual_review", tokenSavingsModeWithoutMCP},
			wantErr: `unsupported mode "manual_review"`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := normalizeTokenSavingsModes(tc.modes)
			if err == nil {
				t.Fatalf("expected error for modes %#v", tc.modes)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestNormalizeTokenSavingsModesCanonicalizesOrder(t *testing.T) {
	t.Parallel()

	modes, err := normalizeTokenSavingsModes([]string{tokenSavingsModeWithoutMCP, tokenSavingsModeWithMCP})
	if err != nil {
		t.Fatalf("normalize token savings modes: %v", err)
	}
	if !reflect.DeepEqual(modes, tokenSavingsRequiredModes) {
		t.Fatalf("expected canonical mode order %#v, got %#v", tokenSavingsRequiredModes, modes)
	}
}

func TestBuildTokenSavingsDeltaReportIncludesCompetitorScores(t *testing.T) {
	t.Parallel()

	pricing := map[string]config.SavingsCompetitorPricing{
		"claude_code": {InputUSDPerMTok: 3.0, OutputUSDPerMTok: 15.0},
		"codex":       {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6.0},
		"amp":         {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6.0},
	}

	report := buildTokenSavingsDeltaReport(
		80,
		map[string]float64{"claude_code": 0.00024, "codex": 0.00012, "amp": 0.00012},
		120,
		map[string]float64{"claude_code": 0.00036, "codex": 0.00018, "amp": 0.00018},
		pricing,
	)

	if report.TokensSaved != 40 {
		t.Fatalf("expected 40 tokens saved, got %#v", report)
	}
	if report.SavingsPct != 0.333333 {
		t.Fatalf("expected rounded savings pct, got %#v", report)
	}
	if got := report.CostSavedUSD["claude_code"]; got != 0.00012 {
		t.Fatalf("expected claude_code saved cost 0.00012, got %#v", report.CostSavedUSD)
	}
	if got := report.Scores["codex"]; got != (tokenSavingsCompetitorScore{
		TokensSaved:  40,
		CostSavedUSD: 0.00006,
		SavingsPct:   0.333333,
	}) {
		t.Fatalf("unexpected codex score %#v", got)
	}
}

func TestBuildTokenSavingsDistributionReportCalculatesSuiteStats(t *testing.T) {
	t.Parallel()

	pricing := map[string]config.SavingsCompetitorPricing{
		"claude_code": {InputUSDPerMTok: 3.0, OutputUSDPerMTok: 15.0},
		"codex":       {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6.0},
		"amp":         {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6.0},
	}

	distribution := buildTokenSavingsDistributionReport([]tokenSavingsCaseReport{
		{Savings: tokenSavingsDeltaReport{TokensSaved: 10, SavingsPct: 0.1, CostSavedUSD: map[string]float64{"claude_code": 0.001, "codex": 0.002, "amp": 0.002}}},
		{Savings: tokenSavingsDeltaReport{TokensSaved: 20, SavingsPct: 0.2, CostSavedUSD: map[string]float64{"claude_code": 0.002, "codex": 0.004, "amp": 0.004}}},
		{Savings: tokenSavingsDeltaReport{TokensSaved: 40, SavingsPct: 0.4, CostSavedUSD: map[string]float64{"claude_code": 0.004, "codex": 0.008, "amp": 0.008}}},
	}, pricing)

	if got := distribution.TokensSaved; got != (tokenSavingsDistributionMetric{
		Mean:   23.333333,
		Median: 20,
		P95:    38,
	}) {
		t.Fatalf("unexpected token distribution %#v", got)
	}
	if got := distribution.SavingsPct; got != (tokenSavingsDistributionMetric{
		Mean:   0.233333,
		Median: 0.2,
		P95:    0.38,
	}) {
		t.Fatalf("unexpected savings pct distribution %#v", got)
	}
	if got := distribution.CostSavedUSD["codex"]; got != (tokenSavingsDistributionMetric{
		Mean:   0.004667,
		Median: 0.004,
		P95:    0.0076,
	}) {
		t.Fatalf("unexpected codex cost distribution %#v", got)
	}
}

func TestBuildTokenSavingsTrendSeriesFromBenchmarkRuns(t *testing.T) {
	t.Parallel()

	pricing := map[string]config.SavingsCompetitorPricing{
		"claude_code": {InputUSDPerMTok: 3.0, OutputUSDPerMTok: 15.0},
		"codex":       {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6.0},
		"amp":         {InputUSDPerMTok: 1.5, OutputUSDPerMTok: 6.0},
	}

	trends := buildTokenSavingsTrendSeriesFromBenchmarkRuns(
		[]storage.SavingsBenchmarkRun{
			{
				CapturedAt:  time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
				TokensSaved: 20,
				SavingsPct:  0.166667,
				CompetitorScores: map[string]storage.SavingsBenchmarkCompetitorScore{
					"claude_code": {TokensSaved: 20, CostSavedUSD: 0.06, SavingsPct: 0.166667},
					"codex":       {TokensSaved: 20, CostSavedUSD: 0.03, SavingsPct: 0.166667},
					"amp":         {TokensSaved: 20, CostSavedUSD: 0.03, SavingsPct: 0.166667},
				},
			},
			{
				CapturedAt:  time.Date(2026, 5, 1, 12, 5, 0, 0, time.UTC),
				TokensSaved: 30,
				SavingsPct:  0.166667,
				CompetitorScores: map[string]storage.SavingsBenchmarkCompetitorScore{
					"claude_code": {TokensSaved: 30, CostSavedUSD: 0.045, SavingsPct: 0.166667},
					"codex":       {TokensSaved: 30, CostSavedUSD: 0.045, SavingsPct: 0.166667},
					"amp":         {TokensSaved: 30, CostSavedUSD: 0.045, SavingsPct: 0.166667},
				},
			},
		},
		time.Date(2026, 5, 1, 12, 10, 0, 0, time.UTC),
		tokenSavingsDeltaReport{
			TokensSaved:  40,
			SavingsPct:   0.333333,
			CostSavedUSD: map[string]float64{"claude_code": 0.024, "codex": 0.012, "amp": 0.012},
		},
		pricing,
	)

	wantCodex := []tokenSavingsTrendPoint{
		{
			CapturedAtUTC: "2026-05-01T12:00:00Z",
			TokensSaved:   20,
			CostSavedUSD:  0.03,
			SavingsPct:    0.166667,
		},
		{
			CapturedAtUTC: "2026-05-01T12:05:00Z",
			TokensSaved:   30,
			CostSavedUSD:  0.045,
			SavingsPct:    0.166667,
		},
		{
			CapturedAtUTC: "2026-05-01T12:10:00Z",
			TokensSaved:   40,
			CostSavedUSD:  0.012,
			SavingsPct:    0.333333,
		},
	}
	if !reflect.DeepEqual(trends["codex"], wantCodex) {
		t.Fatalf("unexpected codex trend series\nwant=%#v\ngot=%#v", wantCodex, trends["codex"])
	}
	if len(trends["claude_code"]) != 3 || len(trends["amp"]) != 3 {
		t.Fatalf("expected all competitors to receive the merged trend series, got %#v", trends)
	}
}
