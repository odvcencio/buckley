package experiment

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/parallel"
)

// Reporter formats experiment results for humans.
type Reporter struct {
	comparator *Comparator
}

// NewReporter creates a Reporter instance.
func NewReporter() *Reporter {
	return &Reporter{}
}

// NewReporterWithComparator creates a reporter that can compare stored runs.
func NewReporterWithComparator(comparator *Comparator) *Reporter {
	return &Reporter{comparator: comparator}
}

// MarkdownTable renders a markdown summary table for the experiment results.
func (r *Reporter) MarkdownTable(exp *Experiment, results []*parallel.AgentResult) string {
	if exp == nil {
		return ""
	}

	resultsByID := make(map[string]*parallel.AgentResult, len(results))
	for _, result := range results {
		if result != nil {
			resultsByID[result.TaskID] = result
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Experiment: %s\n\n", exp.Name)
	b.WriteString("| Model | Success | Duration | Tokens | Files | Error |\n")
	b.WriteString("|-------|---------|----------|--------|-------|-------|\n")

	for _, variant := range exp.Variants {
		result := resultsByID[variant.ID]

		modelID := strings.TrimSpace(variant.ModelID)
		if modelID == "" {
			modelID = strings.TrimSpace(variant.Name)
		}

		success := "no"
		duration := "-"
		tokens := "-"
		files := "-"
		errText := "-"
		if result != nil {
			if result.Success {
				success = "yes"
			}
			duration = formatDuration(result.Duration)
			promptTokens := result.Metrics["prompt_tokens"]
			completionTokens := result.Metrics["completion_tokens"]
			if promptTokens+completionTokens > 0 {
				tokens = fmt.Sprintf("%d", promptTokens+completionTokens)
			}
			files = fmt.Sprintf("%d", len(result.Files))
			if result.Error != nil {
				errText = markdownTableCellEscape(result.Error.Error())
			}
		}

		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", modelID, success, duration, tokens, files, errText)
	}

	return b.String()
}

// markdownTableCellEscape neutralizes characters that would otherwise break
// out of a markdown table cell or run the visible text onto another line.
func markdownTableCellEscape(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// ComparisonMarkdown renders a markdown report from persisted experiment runs.
func (r *Reporter) ComparisonMarkdown(exp *Experiment) (string, error) {
	if exp == nil {
		return "", errors.New("experiment is nil")
	}
	if r.comparator == nil {
		return "", errors.New("comparator unavailable")
	}

	report, err := r.comparator.Compare(exp)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Experiment: %s\n\n", exp.Name)
	if strings.TrimSpace(exp.Description) != "" {
		fmt.Fprintf(&b, "**Description:** %s\n\n", exp.Description)
	}
	if strings.TrimSpace(exp.Hypothesis) != "" {
		fmt.Fprintf(&b, "**Hypothesis:** %s\n\n", exp.Hypothesis)
	}
	fmt.Fprintf(&b, "**Current experiment task:** %s\n\n", exp.Task.Prompt)
	if report.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", report.Summary)
	}

	b.WriteString("## Rankings\n\n")
	b.WriteString("| Rank | Run | Variant | Requested model | Execution identity | Status | Evidence | Score | Cost | Duration | Tokens |\n")
	b.WriteString("|------|-----|---------|-----------------|--------------------|--------|----------|-------|------|----------|--------|\n")
	reportIndex := newVariantReportIndex(report.Variants)
	for _, ranking := range report.Rankings {
		v := reportIndex.find(ranking.RunID)
		if v == nil {
			continue
		}
		rank := "-"
		if ranking.Rank > 0 {
			rank = fmt.Sprintf("%d", ranking.Rank)
		}
		cost := formatCostEvidence(v.CostEvidence, v.Metrics)
		duration := formatDurationMs(v.Metrics.DurationMs)
		tokens := formatRunTokens(v.Metrics)
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %d |\n",
			rank, markdownCell(v.RunID), markdownCell(v.VariantName), markdownCell(v.ModelID), markdownCell(formatExecutionIdentitySummary(v.ModelExecutions)), v.Status, markdownCell(v.VerificationStatus), formatComparisonScore(v), markdownCell(cost), duration, tokens)
	}

	b.WriteString("\n## Variant Details\n\n")
	for _, v := range report.Variants {
		fmt.Fprintf(&b, "### %s / %s (%s)\n\n", v.VariantName, v.RunID, v.ModelID)
		fmt.Fprintf(&b, "- **Run ID:** %s\n", v.RunID)
		fmt.Fprintf(&b, "- **Variant ID:** %s\n", v.VariantID)
		if v.ProviderID != "" {
			fmt.Fprintf(&b, "- **Provider:** %s\n", v.ProviderID)
		}
		if v.SessionID != "" {
			fmt.Fprintf(&b, "- **Session:** %s\n", v.SessionID)
		}
		if v.Branch != "" {
			fmt.Fprintf(&b, "- **Branch:** %s\n", v.Branch)
		}
		fmt.Fprintf(&b, "- **Status:** %s\n", v.Status)
		fmt.Fprintf(&b, "- **Evidence:** %s\n", v.VerificationStatus)
		fmt.Fprintf(&b, "- **Score:** %s\n", formatComparisonScore(&v))
		fmt.Fprintf(&b, "- **Input digest:** %s\n", formatDigestOrUnknown(v.InputDigest))
		fmt.Fprintf(&b, "- **Workload digest:** %s\n", formatDigestOrUnknown(v.WorkloadDigest))
		fmt.Fprintf(&b, "- **Provenance:** %s\n", v.ProvenanceStatus)
		fmt.Fprintf(&b, "- **Execution identity:** %s\n", formatExecutionIdentityDetail(v.ModelExecutions))
		fmt.Fprintf(&b, "- **Execution identity limit:** observed response identities only; not a complete attempt audit or backend revision proof\n")
		fmt.Fprintf(&b, "- **Tokens:** %s\n", formatRunTokensDetail(v.Metrics))
		fmt.Fprintf(&b, "- **Cost:** %s\n", formatCostEvidence(v.CostEvidence, v.Metrics))
		fmt.Fprintf(&b, "- **Tool calls:** %d (%d success, %d failed)\n",
			v.Metrics.ToolCalls, v.Metrics.ToolSuccesses, v.Metrics.ToolFailures)
		fmt.Fprintf(&b, "- **Files modified:** %d (%d lines)\n",
			v.Metrics.FilesModified, v.Metrics.LinesChanged)

		if len(v.CriteriaPassed) > 0 {
			fmt.Fprintf(&b, "- **Passed:** %s\n", strings.Join(v.CriteriaPassed, ", "))
		}
		if len(v.CriteriaFailed) > 0 {
			fmt.Fprintf(&b, "- **Failed:** %s\n", strings.Join(v.CriteriaFailed, ", "))
		}
		if len(v.CriteriaPending) > 0 {
			fmt.Fprintf(&b, "- **Pending review:** %s\n", strings.Join(v.CriteriaPending, ", "))
		}
		if v.Error != "" {
			fmt.Fprintf(&b, "- **Error:** %s\n", v.Error)
		}
		if v.OutputPreview != "" {
			b.WriteString("\n```text\n")
			b.WriteString(v.OutputPreview)
			b.WriteString("\n```\n")
		}
		b.WriteString("\n")
	}

	return b.String(), nil
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

func formatDurationMs(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return formatDuration(time.Duration(ms) * time.Millisecond)
}

func formatComparisonScore(v *VariantReport) string {
	if v == nil || !v.RankEligible {
		return "not evaluated"
	}
	return fmt.Sprintf("%.1f%%", v.CriteriaScore*100)
}

func formatCostEvidence(evidence CostEvidence, metrics RunMetrics) string {
	if strings.TrimSpace(evidence.Label) != "" {
		return evidence.Label
	}
	return runCostEvidence(metrics).Label
}

func formatRunTokens(metrics RunMetrics) int {
	if metrics.Usage != nil {
		return metrics.Usage.Total()
	}
	return metrics.PromptTokens + metrics.CompletionTokens
}

func formatRunTokensDetail(metrics RunMetrics) string {
	if metrics.Usage == nil {
		return fmt.Sprintf("%d prompt + %d completion (legacy scalar)", metrics.PromptTokens, metrics.CompletionTokens)
	}
	parts := []string{fmt.Sprintf("%d input", metrics.Usage.Input), fmt.Sprintf("%d output", metrics.Usage.Output)}
	if metrics.Usage.Unclassified != 0 {
		parts = append(parts, fmt.Sprintf("%d unclassified", metrics.Usage.Unclassified))
	}
	if metrics.Usage.Estimated {
		parts = append(parts, "estimated")
	}
	if metrics.Usage.UsageEvidenceMissing {
		parts = append(parts, "missing usage evidence observed")
	}
	if metrics.Usage.ReportedUsageInconsistent {
		parts = append(parts, "reported usage inconsistent")
	}
	return strings.Join(parts, "; ")
}

func formatDigestOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown (legacy run or unavailable input manifest)"
	}
	return value
}

func formatExecutionIdentitySummary(identities []model.ExecutionIdentity) string {
	if len(identities) == 0 {
		return "unknown (no model response identity evidence)"
	}
	unknown := 0
	conflicted := false
	selectedModels := make(map[string]struct{})
	providers := make(map[string]struct{})
	responseModels := make(map[string]struct{})
	for _, identity := range identities {
		if identity == (model.ExecutionIdentity{}) {
			unknown++
			continue
		}
		if identity.Conflicted {
			conflicted = true
		}
		if identity.SelectedModel != "" {
			selectedModels[identity.SelectedModel] = struct{}{}
		}
		if identity.ProviderID != "" {
			providers[identity.ProviderID] = struct{}{}
		}
		if identity.ResponseModel != "" {
			responseModels[identity.ResponseModel] = struct{}{}
		}
	}
	parts := []string{fmt.Sprintf("%d observed", len(identities))}
	if unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d unknown", unknown))
	}
	if conflicted {
		parts = append(parts, "conflicted")
	}
	parts = appendIdentitySetSummary(parts, "selected", selectedModels)
	parts = appendIdentitySetSummary(parts, "provider", providers)
	parts = appendIdentitySetSummary(parts, "reported", responseModels)
	return strings.Join(parts, "; ")
}

func appendIdentitySetSummary(parts []string, label string, values map[string]struct{}) []string {
	switch len(values) {
	case 0:
		return parts
	case 1:
		for value := range values {
			return append(parts, label+"="+value)
		}
	}
	return append(parts, fmt.Sprintf("mixed %s (%d)", label, len(values)))
}

func formatExecutionIdentityDetail(identities []model.ExecutionIdentity) string {
	if len(identities) == 0 {
		return "unknown (no model response identity evidence)"
	}
	parts := make([]string, 0, len(identities))
	for i, identity := range identities {
		fields := make([]string, 0, 6)
		if identity.RequestedModel != "" {
			fields = append(fields, "requested="+identity.RequestedModel)
		}
		if identity.SelectedModel != "" {
			fields = append(fields, "selected="+identity.SelectedModel)
		}
		if identity.ProviderID != "" {
			fields = append(fields, "provider="+identity.ProviderID)
		}
		if identity.ResponseModel != "" {
			fields = append(fields, "response_model="+identity.ResponseModel)
		}
		if identity.ResponseID != "" {
			fields = append(fields, "response_id="+identity.ResponseID)
		}
		if identity.Conflicted {
			fields = append(fields, "conflicted=true")
		}
		if len(fields) == 0 {
			fields = append(fields, "unknown")
		}
		parts = append(parts, fmt.Sprintf("%d:%s", i+1, strings.Join(fields, " ")))
	}
	return fmt.Sprintf("%d observed response(s): %s", len(identities), strings.Join(parts, "; "))
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", "\\|")
	return value
}

func findVariantReport(reports []VariantReport, id string) *VariantReport {
	for i := range reports {
		if reports[i].RunID == id {
			return &reports[i]
		}
	}
	for i := range reports {
		if reports[i].VariantID == id {
			return &reports[i]
		}
	}
	return nil
}

type variantReportIndex struct {
	byRunID     map[string]*VariantReport
	byVariantID map[string]*VariantReport
}

func newVariantReportIndex(reports []VariantReport) variantReportIndex {
	index := variantReportIndex{
		byRunID:     make(map[string]*VariantReport, len(reports)),
		byVariantID: make(map[string]*VariantReport, len(reports)),
	}
	for i := range reports {
		if _, exists := index.byRunID[reports[i].RunID]; !exists {
			index.byRunID[reports[i].RunID] = &reports[i]
		}
		if _, exists := index.byVariantID[reports[i].VariantID]; !exists {
			index.byVariantID[reports[i].VariantID] = &reports[i]
		}
	}
	return index
}

func (i variantReportIndex) find(id string) *VariantReport {
	if report := i.byRunID[id]; report != nil {
		return report
	}
	return i.byVariantID[id]
}
