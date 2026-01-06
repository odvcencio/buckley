package experiment

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/parallel"
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
	b.WriteString("| Model | Success | Duration | Tokens | Files |\n")
	b.WriteString("|-------|---------|----------|--------|-------|\n")

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
		}

		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", modelID, success, duration, tokens, files)
	}

	return b.String()
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
	fmt.Fprintf(&b, "**Task:** %s\n\n", exp.Task.Prompt)
	if report.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", report.Summary)
	}

	b.WriteString("## Rankings\n\n")
	b.WriteString("| Rank | Variant | Model | Score | Cost | Duration |\n")
	b.WriteString("|------|---------|-------|-------|------|----------|\n")
	for _, ranking := range report.Rankings {
		v := findVariantReport(report.Variants, ranking.VariantID)
		if v == nil {
			continue
		}
		cost := "-"
		if v.Metrics.TotalCost > 0 {
			cost = fmt.Sprintf("$%.4f", v.Metrics.TotalCost)
		}
		duration := formatDurationMs(v.Metrics.DurationMs)
		fmt.Fprintf(&b, "| %d | %s | %s | %.1f%% | %s | %s |\n",
			ranking.Rank, v.VariantName, v.ModelID, ranking.Score*100, cost, duration)
	}

	b.WriteString("\n## Variant Details\n\n")
	for _, v := range report.Variants {
		fmt.Fprintf(&b, "### %s (%s)\n\n", v.VariantName, v.ModelID)
		fmt.Fprintf(&b, "- **Status:** %s\n", v.Status)
		fmt.Fprintf(&b, "- **Score:** %.1f%%\n", v.CriteriaScore*100)
		fmt.Fprintf(&b, "- **Tokens:** %d prompt + %d completion\n",
			v.Metrics.PromptTokens, v.Metrics.CompletionTokens)
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

func findVariantReport(reports []VariantReport, id string) *VariantReport {
	for i := range reports {
		if reports[i].VariantID == id {
			return &reports[i]
		}
	}
	return nil
}
