package commands

import (
	"strings"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/transparency"
)

const (
	// DefaultPRSupportingContextTokens bounds optional prose and structural
	// summaries around the separately budgeted diff.
	DefaultPRSupportingContextTokens = 12_000

	prFeedbackBudgetParts    = 5
	prProviderBudgetParts    = 3
	prDescriptionBudgetParts = 2
	prCanopyBudgetParts      = 2
	prAgentsBudgetParts      = 3
)

// PRContextCurationSource accounts for one projectable prompt source.
type PRContextCurationSource struct {
	Name           string
	OriginalTokens int
	IncludedTokens int
	Projected      bool
}

// PRContextCuration describes the token-conscious projection used to build a
// PR review prompt. Diff, identity, CI, changed files, and feedback identifiers
// are protected separately and are not included in this supporting budget.
type PRContextCuration struct {
	BudgetTokens   int
	OriginalTokens int
	IncludedTokens int
	SavedTokens    int
	Sources        []PRContextCurationSource
}

// CuratePRContext produces a prompt-only projection without mutating captured
// evidence. A non-positive budget uses DefaultPRSupportingContextTokens.
func CuratePRContext(ctx *PRContext, maxSupportingTokens int) (*PRContext, PRContextCuration) {
	if maxSupportingTokens <= 0 {
		maxSupportingTokens = DefaultPRSupportingContextTokens
	}
	accounting := PRContextCuration{BudgetTokens: maxSupportingTokens}
	if ctx == nil {
		return nil, accounting
	}

	curated := *ctx
	curated.Comments = append([]PRComment(nil), ctx.Comments...)
	curated.Reviews = append([]PRReview(nil), ctx.Reviews...)
	curated.InlineComments = append([]PRComment(nil), ctx.InlineComments...)
	curated.ProviderEvidence = append([]PRContextEvidence(nil), ctx.ProviderEvidence...)
	for i := range curated.ProviderEvidence {
		curated.ProviderEvidence[i].Files = append([]string(nil), ctx.ProviderEvidence[i].Files...)
	}
	if ctx.PR != nil {
		pr := *ctx.PR
		curated.PR = &pr
	}
	ids := ctx.feedbackIDs()
	curated.feedbackIDCache = &ids
	curated.promptContext = nil

	feedbackOriginal := prFeedbackSupportingTokens(ctx)
	providerOriginal := prContextEvidenceSupportingTokens(ctx.ProviderEvidence)
	descriptionOriginal := 0
	if ctx.PR != nil {
		descriptionOriginal = reviewEstimateTokens(ctx.PR.Body)
	}
	canopyOriginal := reviewEstimateTokens(ctx.CanopyReview)
	agentsOriginal := reviewEstimateTokens(ctx.AgentsMD)
	budgets := allocatePRSupportingBudgets(
		maxSupportingTokens,
		[]int{feedbackOriginal, providerOriginal, descriptionOriginal, canopyOriginal, agentsOriginal},
		[]int{prFeedbackBudgetParts, prProviderBudgetParts, prDescriptionBudgetParts, prCanopyBudgetParts, prAgentsBudgetParts},
	)

	feedbackBudget := budgets[0]
	projectPRFeedbackBodies(&curated, feedbackBudget)
	feedbackIncluded := prFeedbackSupportingTokens(&curated)
	accounting.add("prior review feedback", feedbackOriginal, feedbackIncluded)

	projectPRContextEvidenceBodies(curated.ProviderEvidence, budgets[1])
	providerIncluded := prContextEvidenceSupportingTokens(curated.ProviderEvidence)
	accounting.add("deterministic provider evidence", providerOriginal, providerIncluded)

	descriptionIncluded := 0
	if ctx.PR != nil {
		curated.PR.Body = projectPRSupportingText(ctx.PR.Body, budgets[2])
		descriptionIncluded = reviewEstimateTokens(curated.PR.Body)
	}
	accounting.add("PR description", descriptionOriginal, descriptionIncluded)

	curated.CanopyReview = projectPRSupportingText(ctx.CanopyReview, budgets[3])
	canopyIncluded := reviewEstimateTokens(curated.CanopyReview)
	accounting.add("Canopy structural review", canopyOriginal, canopyIncluded)

	curated.AgentsMD = projectPRSupportingText(ctx.AgentsMD, budgets[4])
	agentsIncluded := reviewEstimateTokens(curated.AgentsMD)
	accounting.add("AGENTS.md chain", agentsOriginal, agentsIncluded)

	accounting.SavedTokens = accounting.OriginalTokens - accounting.IncludedTokens
	curated.ContextCuration = accounting
	return &curated, accounting
}

func allocatePRSupportingBudgets(total int, demand, weight []int) []int {
	allocated := make([]int, len(demand))
	if total <= 0 || len(demand) == 0 || len(demand) != len(weight) {
		return allocated
	}

	remaining := total
	for remaining > 0 {
		totalWeight := 0
		for i := range demand {
			if allocated[i] < demand[i] && weight[i] > 0 {
				totalWeight += weight[i]
			}
		}
		if totalWeight == 0 {
			break
		}

		startRemaining := remaining
		distributed := 0
		for i := range demand {
			if allocated[i] >= demand[i] || weight[i] <= 0 {
				continue
			}
			share := startRemaining * weight[i] / totalWeight
			if share == 0 {
				share = 1
			}
			if share > remaining {
				share = remaining
			}
			if unmet := demand[i] - allocated[i]; share > unmet {
				share = unmet
			}
			allocated[i] += share
			remaining -= share
			distributed += share
			if remaining == 0 {
				break
			}
		}
		if distributed == 0 {
			break
		}
	}
	return allocated
}

func prFeedbackSupportingTokens(ctx *PRContext) int {
	if ctx == nil {
		return 0
	}
	total := 0
	for _, comment := range ctx.Comments {
		total += reviewEstimateTokens(comment.Body)
	}
	for _, review := range ctx.Reviews {
		total += reviewEstimateTokens(review.Body)
	}
	for _, comment := range ctx.InlineComments {
		total += reviewEstimateTokens(comment.Body)
	}
	return total
}

func prContextEvidenceSupportingTokens(evidence []PRContextEvidence) int {
	total := 0
	for _, item := range evidence {
		total += reviewEstimateTokens(item.Body)
	}
	return total
}

func (c *PRContextCuration) add(name string, original, included int) {
	if original == 0 {
		return
	}
	c.Sources = append(c.Sources, PRContextCurationSource{
		Name:           name,
		OriginalTokens: original,
		IncludedTokens: included,
		Projected:      included < original,
	})
	c.OriginalTokens += original
	c.IncludedTokens += included
}

func projectPRFeedbackBodies(ctx *PRContext, budgetTokens int) {
	if ctx == nil {
		return
	}
	type feedbackBody struct {
		get func() string
		set func(string)
	}
	bodies := make([]feedbackBody, 0, len(ctx.Comments)+len(ctx.Reviews)+len(ctx.InlineComments))
	for i := range ctx.Comments {
		index := i
		bodies = append(bodies, feedbackBody{
			get: func() string { return ctx.Comments[index].Body },
			set: func(body string) { ctx.Comments[index].Body = body },
		})
	}
	for i := range ctx.Reviews {
		index := i
		bodies = append(bodies, feedbackBody{
			get: func() string { return ctx.Reviews[index].Body },
			set: func(body string) { ctx.Reviews[index].Body = body },
		})
	}
	for i := range ctx.InlineComments {
		index := i
		bodies = append(bodies, feedbackBody{
			get: func() string { return ctx.InlineComments[index].Body },
			set: func(body string) { ctx.InlineComments[index].Body = body },
		})
	}

	remaining := budgetTokens
	for i, body := range bodies {
		itemsLeft := len(bodies) - i
		share := 0
		if remaining > 0 && itemsLeft > 0 {
			share = remaining / itemsLeft
		}
		projected := projectPRSupportingText(body.get(), share)
		body.set(projected)
		remaining -= reviewEstimateTokens(projected)
		if remaining < 0 {
			remaining = 0
		}
	}
}

func projectPRSupportingText(value string, maxTokens int) string {
	value = strings.TrimSpace(value)
	if value == "" || maxTokens <= 0 {
		return ""
	}
	if reviewEstimateTokens(value) <= maxTokens {
		return value
	}

	const marker = "\n\n... [middle omitted for review-context budget] ...\n\n"
	maxBytes := maxTokens * 4
	if maxBytes <= len(marker) {
		return utf8Prefix("…", maxBytes)
	}
	contentBytes := maxBytes - len(marker)
	headBytes := (contentBytes * 2) / 3
	tailBytes := contentBytes - headBytes
	return strings.TrimSpace(utf8Prefix(value, headBytes)) +
		marker +
		strings.TrimSpace(utf8Suffix(value, tailBytes))
}

func utf8Prefix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.ValidString(value[:maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}

func utf8Suffix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}

func recordPRContextCuration(audit *transparency.ContextAudit, accounting PRContextCuration) {
	if audit == nil {
		return
	}
	for _, source := range accounting.Sources {
		if source.Projected {
			audit.AddTruncated(source.Name+" (curated)", source.IncludedTokens, source.OriginalTokens)
			continue
		}
		audit.Add(source.Name, source.IncludedTokens)
	}
}
