package telemetry

import "github.com/jgravelle/gocodemunch-mcp/src/internal/savings"

// PricingFromSavings converts normalized savings pricing into telemetry pricing.
func PricingFromSavings(pricing map[string]savings.Pricing) map[string]Pricing {
	normalized, _ := savings.NormalizePricing(pricing)
	converted := make(map[string]Pricing, len(normalized))
	for competitor, value := range normalized {
		converted[competitor] = Pricing{
			InputUSDPerMTok:  value.InputUSDPerMTok,
			OutputUSDPerMTok: value.OutputUSDPerMTok,
		}
	}
	return converted
}
