package experiment

import (
	"fmt"
	"math"

	"m31labs.dev/buckley/pkg/modelusage"
	"m31labs.dev/buckley/pkg/transparency"
)

const (
	CostEvidenceKnown         = "known"
	CostEvidenceUnknown       = "unknown"
	CostEvidenceLegacyKnown   = "legacy_known"
	CostEvidenceLegacyUnknown = "legacy_unknown"
)

// CostEvidence describes whether a run's scalar TotalCost can truthfully be
// used as a ranking tie-break. Unknown cost evidence never disqualifies an
// otherwise verified result.
type CostEvidence struct {
	Status     string `json:"status"`
	Comparable bool   `json:"comparable"`
	Label      string `json:"label"`
}

func cloneTokenUsagePtr(input *transparency.TokenUsage) *transparency.TokenUsage {
	if input == nil {
		return nil
	}
	value := transparency.CloneTokenUsage(*input)
	return &value
}

func cloneRunMetrics(metrics RunMetrics) RunMetrics {
	metrics.Usage = cloneTokenUsagePtr(metrics.Usage)
	return metrics
}

func CostEvidenceForMetrics(metrics RunMetrics) CostEvidence {
	return runCostEvidence(metrics)
}

func runCostEvidence(metrics RunMetrics) CostEvidence {
	if metrics.Usage != nil {
		if metrics.CostUnknown || transparency.CostUnknownForUsage(*metrics.Usage, transparency.ModelPricing{InputPerMillion: 1, OutputPerMillion: 1}) {
			return CostEvidence{
				Status:     CostEvidenceUnknown,
				Comparable: false,
				Label:      fmt.Sprintf("$%.4f known subtotal; full cost unknown", metrics.TotalCost),
			}
		}
		if modelusage.HasEvidence(*metrics.Usage) {
			return CostEvidence{
				Status:     CostEvidenceKnown,
				Comparable: true,
				Label:      fmt.Sprintf("$%.4f known", metrics.TotalCost),
			}
		}
	}
	if metrics.TotalCost > 0 && finiteNonNegativeFloat(metrics.TotalCost) {
		return CostEvidence{
			Status:     CostEvidenceLegacyKnown,
			Comparable: true,
			Label:      fmt.Sprintf("$%.4f legacy scalar", metrics.TotalCost),
		}
	}
	return CostEvidence{
		Status:     CostEvidenceLegacyUnknown,
		Comparable: false,
		Label:      "unknown (legacy run without retained cost evidence)",
	}
}

func finiteNonNegativeFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}
