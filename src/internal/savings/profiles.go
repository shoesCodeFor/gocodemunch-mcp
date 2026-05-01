package savings

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

const (
	DefaultPricingProfileVersion = "2026-05-01"

	CompetitorClaudeCode = "claude_code"
	CompetitorCodex      = "codex"
	CompetitorAmp        = "amp"
)

// Pricing captures per-competitor token pricing in USD per million tokens.
type Pricing struct {
	InputUSDPerMTok  float64
	OutputUSDPerMTok float64
}

// Profile defines one supported competitor pricing profile.
type Profile struct {
	ID          string
	DisplayName string
	Pricing     Pricing
}

var defaultProfiles = []Profile{
	{
		ID:          CompetitorClaudeCode,
		DisplayName: "Claude Code",
		Pricing: Pricing{
			InputUSDPerMTok:  3.00,
			OutputUSDPerMTok: 15.00,
		},
	},
	{
		ID:          CompetitorCodex,
		DisplayName: "Codex",
		Pricing: Pricing{
			InputUSDPerMTok:  1.50,
			OutputUSDPerMTok: 6.00,
		},
	},
	{
		ID:          CompetitorAmp,
		DisplayName: "Amp",
		Pricing: Pricing{
			InputUSDPerMTok:  1.50,
			OutputUSDPerMTok: 6.00,
		},
	},
}

// DefaultProfiles returns the built-in supported pricing profiles.
func DefaultProfiles() []Profile {
	out := make([]Profile, len(defaultProfiles))
	copy(out, defaultProfiles)
	return out
}

// DefaultCompetitors returns supported competitor identifiers in stable order.
func DefaultCompetitors() []string {
	out := make([]string, 0, len(defaultProfiles))
	for _, profile := range defaultProfiles {
		out = append(out, profile.ID)
	}
	return out
}

// DefaultPricing returns the built-in pricing keyed by competitor id.
func DefaultPricing() map[string]Pricing {
	out := make(map[string]Pricing, len(defaultProfiles))
	for _, profile := range defaultProfiles {
		out[profile.ID] = profile.Pricing
	}
	return out
}

// DefaultPricingForCompetitor returns the built-in pricing for one competitor.
func DefaultPricingForCompetitor(competitor string) (Pricing, bool) {
	normalized := normalizeCompetitorID(competitor)
	for _, profile := range defaultProfiles {
		if profile.ID == normalized {
			return profile.Pricing, true
		}
	}
	return Pricing{}, false
}

// ClonePricing returns a shallow copy of the provided pricing map.
func ClonePricing(pricing map[string]Pricing) map[string]Pricing {
	if len(pricing) == 0 {
		return map[string]Pricing{}
	}

	cloned := make(map[string]Pricing, len(pricing))
	for competitor, value := range pricing {
		cloned[competitor] = value
	}
	return cloned
}

// NormalizePricing fills missing supported competitors with defaults and
// replaces malformed or negative values with the built-in fallback rates.
func NormalizePricing(pricing map[string]Pricing) (map[string]Pricing, []string) {
	normalizedInput := make(map[string]Pricing, len(pricing))
	for rawCompetitor, candidate := range pricing {
		competitor := normalizeCompetitorID(rawCompetitor)
		if competitor == "" {
			continue
		}
		if _, supported := DefaultPricingForCompetitor(competitor); !supported {
			continue
		}
		normalizedInput[competitor] = candidate
	}

	out := make(map[string]Pricing, len(defaultProfiles))
	warnings := make([]string, 0, len(defaultProfiles)*2)
	for _, profile := range defaultProfiles {
		current := profile.Pricing
		candidate, ok := normalizedInput[profile.ID]
		if ok {
			if isFiniteNonNegative(candidate.InputUSDPerMTok) {
				current.InputUSDPerMTok = candidate.InputUSDPerMTok
			} else {
				warnings = append(
					warnings,
					fmt.Sprintf(
						"%s input pricing must be finite and non-negative; using default %g USD/MTok",
						profile.ID,
						profile.Pricing.InputUSDPerMTok,
					),
				)
			}
			if isFiniteNonNegative(candidate.OutputUSDPerMTok) {
				current.OutputUSDPerMTok = candidate.OutputUSDPerMTok
			} else {
				warnings = append(
					warnings,
					fmt.Sprintf(
						"%s output pricing must be finite and non-negative; using default %g USD/MTok",
						profile.ID,
						profile.Pricing.OutputUSDPerMTok,
					),
				)
			}
		}
		out[profile.ID] = current
	}

	return out, warnings
}

// ZeroCostMap returns a zeroed avoided-cost map for the normalized pricing set.
func ZeroCostMap(pricing map[string]Pricing) map[string]float64 {
	normalized, _ := NormalizePricing(pricing)
	zeroes := make(map[string]float64, len(normalized))
	for competitor := range normalized {
		zeroes[competitor] = 0
	}
	return zeroes
}

// CostsForTokens calculates avoided cost per competitor for the provided token counts.
func CostsForTokens(pricing map[string]Pricing, inputTokens int, outputTokens int) map[string]float64 {
	normalized, _ := NormalizePricing(pricing)
	inputTokens = sanitizeTokenCount(inputTokens)
	outputTokens = sanitizeTokenCount(outputTokens)

	costs := make(map[string]float64, len(normalized))
	for competitor, rate := range normalized {
		inputCost := float64(inputTokens) * rate.InputUSDPerMTok / 1_000_000.0
		outputCost := float64(outputTokens) * rate.OutputUSDPerMTok / 1_000_000.0
		costs[competitor] = roundUSD(inputCost + outputCost)
	}
	return costs
}

// DiffCostMap calculates per-competitor cost deltas using the normalized pricing key set.
func DiffCostMap(left map[string]float64, right map[string]float64, pricing map[string]Pricing) map[string]float64 {
	normalized, _ := NormalizePricing(pricing)
	diff := make(map[string]float64, len(normalized))
	for competitor := range normalized {
		diff[competitor] = roundUSD(left[competitor] - right[competitor])
	}
	return diff
}

// OrderedCompetitors returns normalized competitor ids in deterministic order.
func OrderedCompetitors(pricing map[string]Pricing) []string {
	normalized, _ := NormalizePricing(pricing)
	competitors := make([]string, 0, len(normalized))
	for competitor := range normalized {
		competitors = append(competitors, competitor)
	}
	slices.Sort(competitors)
	return competitors
}

func normalizeCompetitorID(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func isFiniteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func sanitizeTokenCount(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func roundUSD(value float64) float64 {
	return math.Round(value*1_000_000_000_000) / 1_000_000_000_000
}
