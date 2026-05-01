package savings

import "testing"

func TestNormalizePricingFillsDefaultsAndWarnsOnInvalidValues(t *testing.T) {
	normalized, warnings := NormalizePricing(map[string]Pricing{
		" codex ": {InputUSDPerMTok: 2.25, OutputUSDPerMTok: 7.5},
		"claude_code": {
			InputUSDPerMTok:  -1,
			OutputUSDPerMTok: 18,
		},
		"amp": {
			InputUSDPerMTok:  1.75,
			OutputUSDPerMTok: 0,
		},
		"unsupported": {InputUSDPerMTok: 9, OutputUSDPerMTok: 9},
	})

	if len(normalized) != 3 {
		t.Fatalf("expected normalized pricing to keep only supported competitors, got %#v", normalized)
	}
	if got := normalized[CompetitorCodex]; got.InputUSDPerMTok != 2.25 || got.OutputUSDPerMTok != 7.5 {
		t.Fatalf("expected normalized codex pricing override, got %#v", normalized)
	}
	claudeDefaults, _ := DefaultPricingForCompetitor(CompetitorClaudeCode)
	if got := normalized[CompetitorClaudeCode]; got.InputUSDPerMTok != claudeDefaults.InputUSDPerMTok || got.OutputUSDPerMTok != 18 {
		t.Fatalf("expected claude_code invalid input to fall back while preserving valid output, got %#v", normalized)
	}
	if got := normalized[CompetitorAmp]; got.InputUSDPerMTok != 1.75 || got.OutputUSDPerMTok != 0 {
		t.Fatalf("expected amp zero output to remain valid, got %#v", normalized)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one normalization warning, got %#v", warnings)
	}
}

func TestCostsForTokensAndDiffCostMapUseNormalizedProfiles(t *testing.T) {
	pricing := map[string]Pricing{
		CompetitorClaudeCode: {InputUSDPerMTok: 1.0, OutputUSDPerMTok: 9.0},
		CompetitorCodex:      {InputUSDPerMTok: 2.0, OutputUSDPerMTok: 4.0},
	}

	costs := CostsForTokens(pricing, 100, 50)
	if got := costs[CompetitorClaudeCode]; got != 0.00055 {
		t.Fatalf("expected claude_code blended cost 0.00055, got %#v", costs)
	}
	if got := costs[CompetitorCodex]; got != 0.0004 {
		t.Fatalf("expected codex blended cost 0.0004, got %#v", costs)
	}
	if _, ok := costs[CompetitorAmp]; !ok {
		t.Fatalf("expected normalized cost map to include amp default profile, got %#v", costs)
	}

	diff := DiffCostMap(
		map[string]float64{CompetitorClaudeCode: 0.00055, CompetitorCodex: 0.0004},
		map[string]float64{CompetitorClaudeCode: 0.0006, CompetitorCodex: 0.0001},
		pricing,
	)
	if got := diff[CompetitorClaudeCode]; got != -0.00005 {
		t.Fatalf("expected claude_code diff to preserve negative delta, got %#v", diff)
	}
	if got := diff[CompetitorCodex]; got != 0.0003 {
		t.Fatalf("expected codex diff 0.0003, got %#v", diff)
	}
}

func TestOrderedCompetitorsReturnsDeterministicSupportedSet(t *testing.T) {
	ordered := OrderedCompetitors(map[string]Pricing{
		CompetitorCodex:      {InputUSDPerMTok: 2.0, OutputUSDPerMTok: 4.0},
		CompetitorClaudeCode: {InputUSDPerMTok: 1.0, OutputUSDPerMTok: 9.0},
	})

	expected := []string{CompetitorAmp, CompetitorClaudeCode, CompetitorCodex}
	if len(ordered) != len(expected) {
		t.Fatalf("expected ordered competitors %#v, got %#v", expected, ordered)
	}
	for i := range expected {
		if ordered[i] != expected[i] {
			t.Fatalf("unexpected ordered competitors %#v, got %#v", expected, ordered)
		}
	}
}
