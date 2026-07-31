package commands

import (
	"fmt"
	"strings"
)

func promptPRContext(ctx *PRContext) *PRContext {
	if ctx != nil && ctx.promptContext != nil {
		return ctx.promptContext
	}
	return ctx
}

func appendPRContextCurationNotice(sb *strings.Builder, curation PRContextCuration) {
	if sb == nil || curation.SavedTokens <= 0 {
		return
	}
	sb.WriteString("## Context Projection\n\n")
	fmt.Fprintf(sb, "- Supporting evidence: ~%d of ~%d estimated tokens included; ~%d saved.\n",
		curation.IncludedTokens, curation.OriginalTokens, curation.SavedTokens)
	sb.WriteString("- Diff, immutable PR facts, CI state, changed-file coverage, and feedback IDs are protected outside this budget.\n")
	sb.WriteString("- Ellipses or blank feedback bodies indicate budget projection, not missing feedback identity.\n\n")
}

func appendPRContextProviderEvidence(sb *strings.Builder, evidence []PRContextEvidence) {
	if sb == nil {
		return
	}
	for _, item := range evidence {
		if strings.TrimSpace(item.Body) == "" {
			continue
		}
		fmt.Fprintf(sb, "## Deterministic Evidence: %s\n\n", item.Title)
		fmt.Fprintf(sb, "- **Provider**: %s\n", item.Provider)
		if len(item.Files) > 0 {
			fmt.Fprintf(sb, "- **Files**: %s\n", strings.Join(item.Files, ", "))
		}
		sb.WriteString("\n")
		sb.WriteString(item.Body)
		sb.WriteString("\n\n")
	}
}

func projectPRContextEvidenceBodies(evidence []PRContextEvidence, budgetTokens int) {
	remaining := budgetTokens
	for i := range evidence {
		itemsLeft := len(evidence) - i
		share := 0
		if remaining > 0 && itemsLeft > 0 {
			share = remaining / itemsLeft
		}
		evidence[i].Body = projectPRSupportingText(evidence[i].Body, share)
		remaining -= reviewEstimateTokens(evidence[i].Body)
		if remaining < 0 {
			remaining = 0
		}
	}
}
