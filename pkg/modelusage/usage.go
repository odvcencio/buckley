package modelusage

import (
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

// FromUsage projects provider usage into Buckley's neutral transparency shape.
// Provider detail fields are retained as reported evidence, not additive token
// counts, so reasoning/cache subsets do not double-count.
func FromUsage(usage model.Usage) transparency.TokenUsage {
	tokens := transparency.TokenUsage{
		Input:              usage.PromptTokens,
		Output:             usage.CompletionTokens,
		ReportedTotal:      usage.TotalTokens,
		ReportedCacheWrite: usage.CacheWriteTokens,
		Estimated:          usage.Estimated,
	}
	if usage.CompletionTokenDetails != nil {
		value := usage.CompletionTokenDetails.ReasoningTokens
		tokens.ReportedReasoning = &value
	}
	if usage.PromptTokensDetails != nil {
		value := usage.PromptTokensDetails.CachedTokens
		tokens.ReportedCachedInput = &value
	}
	if total, known := usage.TotalCostUSD(); known {
		tokens.ProviderCostUSD = total
		tokens.ProviderCostKnown = true
		tokens.ProviderCostIsBYOK = usage.IsBYOK
	}
	if usage.TotalTokens > 0 {
		split := usage.PromptTokens + usage.CompletionTokens
		if extra := usage.TotalTokens - split; extra > 0 {
			tokens.Unclassified = extra
		} else if usage.TotalTokens < split {
			tokens.ReportedUsageInconsistent = true
		}
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 || usage.CacheWriteTokens < 0 {
		tokens.ReportedUsageInconsistent = true
	}
	if tokens.ReportedReasoning != nil && (*tokens.ReportedReasoning < 0 || *tokens.ReportedReasoning > usage.CompletionTokens) {
		tokens.ReportedUsageInconsistent = true
	}
	if tokens.ReportedCachedInput != nil && (*tokens.ReportedCachedInput < 0 || *tokens.ReportedCachedInput > usage.PromptTokens) {
		tokens.ReportedUsageInconsistent = true
	}
	return tokens
}

// FromResponse projects a model response's usage. If a real response omitted a
// usage object and also carries no populated usage fields, the returned usage
// records missing evidence without inventing token counts.
func FromResponse(resp *model.ChatResponse) transparency.TokenUsage {
	if resp == nil {
		return transparency.TokenUsage{}
	}
	tokens := FromUsage(resp.Usage)
	if resp.UsagePresent {
		tokens.UsageEvidencePresent = true
	} else if HasEvidence(tokens) {
		tokens.UsageEvidencePresent = true
	} else {
		tokens.UsageEvidenceMissing = true
	}
	return tokens
}

// HasEvidence reports whether usage carries any provider or host usage
// evidence, including an explicit missing-usage marker.
func HasEvidence(tokens transparency.TokenUsage) bool {
	return tokens.Input != 0 ||
		tokens.Output != 0 ||
		tokens.Reasoning != 0 ||
		tokens.Unclassified != 0 ||
		tokens.CachedInput != 0 ||
		tokens.ReportedTotal != 0 ||
		tokens.ReportedReasoning != nil ||
		tokens.ReportedCachedInput != nil ||
		tokens.ReportedCacheWrite != 0 ||
		tokens.ReportedUsageInconsistent ||
		tokens.Estimated ||
		tokens.UsageEvidencePresent ||
		tokens.UsageEvidenceMissing
}
