package artifact

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReviewGenerator creates review artifacts
type ReviewGenerator struct {
	outputDir string
}

// NewReviewGenerator creates a new review artifact generator
func NewReviewGenerator(outputDir string) *ReviewGenerator {
	return &ReviewGenerator{
		outputDir: outputDir,
	}
}

// Generate creates a review artifact and writes it to disk
func (g *ReviewGenerator) Generate(artifact *ReviewArtifact) (string, error) {
	// Ensure output directory exists
	if err := os.MkdirAll(g.outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}

	// Generate filename: YYYY-MM-DD-{feature}-review.md
	now := time.Now()
	filename := fmt.Sprintf("%s-%s-review.md", now.Format("2006-01-02"), artifact.Feature)
	filePath := filepath.Join(g.outputDir, filename)

	// Generate markdown content
	content := g.generateMarkdown(artifact)

	// Write to file
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write review artifact: %w", err)
	}

	// Update artifact metadata
	artifact.FilePath = filePath
	artifact.UpdatedAt = now

	return filePath, nil
}

// generateMarkdown converts a review artifact to markdown
func (g *ReviewGenerator) generateMarkdown(artifact *ReviewArtifact) string {
	var b strings.Builder

	// Header with chain links
	b.WriteString(fmt.Sprintf("# Review: %s\n\n", formatFeatureName(artifact.Feature)))
	b.WriteString(fmt.Sprintf("**Planning Artifact:** [%s](%s)\n",
		filepath.Base(artifact.PlanningArtifactPath), g.relativePath("reviews", "plans", artifact.PlanningArtifactPath)))
	b.WriteString(fmt.Sprintf("**Execution Artifact:** [%s](%s)\n",
		filepath.Base(artifact.ExecutionArtifactPath), g.relativePath("reviews", "execution", artifact.ExecutionArtifactPath)))
	b.WriteString(fmt.Sprintf("**Reviewed:** %s\n", artifact.ReviewedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("**Reviewer Model:** %s\n", artifact.ReviewerModel))
	b.WriteString(fmt.Sprintf("**Status:** %s\n\n", formatStatus(artifact.Status)))

	// Validation Strategy
	b.WriteString("## Validation Strategy\n\n")
	if len(artifact.ValidationStrategy.CriticalPath) > 0 {
		b.WriteString("### Critical Path Validation\n")
		for i, item := range artifact.ValidationStrategy.CriticalPath {
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, item))
		}
		b.WriteString("\n")
	}

	if len(artifact.ValidationStrategy.HighRiskAreas) > 0 {
		b.WriteString("### High-Risk Areas (from execution artifact)\n")
		for _, area := range artifact.ValidationStrategy.HighRiskAreas {
			b.WriteString(fmt.Sprintf("- %s\n", area))
		}
		b.WriteString("\n")
	}

	// Validation Results
	if len(artifact.ValidationResults) > 0 {
		b.WriteString("## Validation Results\n\n")
		for _, result := range artifact.ValidationResults {
			statusEmoji := categoryStatusEmoji(result.Status)
			b.WriteString(fmt.Sprintf("### %s %s\n\n", result.Category, statusEmoji))

			for _, check := range result.Checks {
				icon := statusIcon(check.Status)
				b.WriteString(fmt.Sprintf("- %s %s\n", icon, check.Name))
				if check.Description != "" {
					b.WriteString(fmt.Sprintf("  - %s\n", check.Description))
				}
				if check.Issue != nil {
					b.WriteString(fmt.Sprintf("  - **Issue:** %s\n", check.Issue.Description))
					if check.Issue.Fix != "" {
						b.WriteString(fmt.Sprintf("  - **Fix Required:** %s\n", check.Issue.Fix))
					}
				}
			}
			b.WriteString("\n")
		}
	}

	// Issues Found
	if len(artifact.IssuesFound) > 0 {
		criticalIssues := filterIssuesBySeverity(artifact.IssuesFound, "critical")
		qualityIssues := filterIssuesBySeverity(artifact.IssuesFound, "quality")
		nits := filterIssuesBySeverity(artifact.IssuesFound, "nit")

		b.WriteString("## Issues Found\n\n")

		if len(criticalIssues) > 0 {
			b.WriteString(fmt.Sprintf("### Critical Issues (Must Fix) - %d found\n\n", len(criticalIssues)))
			for _, issue := range criticalIssues {
				b.WriteString(fmt.Sprintf("%d. **%s** (`%s`)\n", issue.ID, issue.Title, issue.Location))
				b.WriteString(fmt.Sprintf("   - Severity: %s\n", issue.Severity))
				b.WriteString(fmt.Sprintf("   - %s\n", issue.Description))
				if issue.Fix != "" {
					b.WriteString(fmt.Sprintf("   - Fix: %s\n", issue.Fix))
				}
				b.WriteString("\n")
			}
		}

		if len(qualityIssues) > 0 {
			b.WriteString(fmt.Sprintf("### Quality Concerns (Should Fix) - %d found\n\n", len(qualityIssues)))
			for _, issue := range qualityIssues {
				b.WriteString(fmt.Sprintf("%d. **%s** (`%s`)\n", issue.ID, issue.Title, issue.Location))
				b.WriteString(fmt.Sprintf("   - %s\n", issue.Description))
				if issue.Fix != "" {
					b.WriteString(fmt.Sprintf("   - Fix: %s\n", issue.Fix))
				}
				b.WriteString("\n")
			}
		}

		if len(nits) > 0 {
			b.WriteString(fmt.Sprintf("### Nits (Future Work) - %d found\n\n", len(nits)))
			for _, issue := range nits {
				b.WriteString(fmt.Sprintf("%d. %s\n", issue.ID, issue.Description))
			}
			b.WriteString("\n")
		}
	}

	// Review Iterations
	if len(artifact.Iterations) > 0 {
		b.WriteString("## Review Iterations\n\n")
		for _, iteration := range artifact.Iterations {
			b.WriteString(fmt.Sprintf("### Iteration %d (%s)\n", iteration.Number, formatStatus(iteration.Status)))
			b.WriteString(fmt.Sprintf("- Found %d issues\n", iteration.IssuesFound))
			if iteration.Notes != "" {
				b.WriteString(fmt.Sprintf("- %s\n", iteration.Notes))
			}
			b.WriteString("\n")
		}
	}

	// Opportunistic Improvements
	if len(artifact.OpportunisticImprovements) > 0 {
		b.WriteString("## Opportunistic Improvements\n\n")
		b.WriteString("These are unrelated to the current feature but noticed during review. ")
		b.WriteString("Consider addressing in future work or separate PRs.\n\n")

		// Group by category
		categories := groupImprovementsByCategory(artifact.OpportunisticImprovements)
		for category, improvements := range categories {
			b.WriteString(fmt.Sprintf("### %s\n\n", category))
			for i, improvement := range improvements {
				b.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, improvement.Title))
				b.WriteString(fmt.Sprintf("   - **Observation:** %s\n", improvement.Observation))
				b.WriteString(fmt.Sprintf("   - **Suggestion:** %s\n", improvement.Suggestion))
				b.WriteString(fmt.Sprintf("   - **Impact:** %s\n", improvement.Impact))
				if len(improvement.Files) > 0 {
					b.WriteString(fmt.Sprintf("   - **Files:** %s\n", strings.Join(improvement.Files, ", ")))
				}
				b.WriteString("\n")
			}
		}
	}

	// Final Approval
	if artifact.Approval != nil {
		b.WriteString("## Approval\n\n")
		b.WriteString(fmt.Sprintf("**Status:** %s %s\n", approvalEmoji(artifact.Approval.Status), formatStatus(artifact.Approval.Status)))

		if len(artifact.Approval.RemainingWork) > 0 {
			b.WriteString(fmt.Sprintf("**Remaining Work:** %d nits logged as future enhancements\n", len(artifact.Approval.RemainingWork)))
		}

		b.WriteString(fmt.Sprintf("**Ready for PR:** %v\n\n", artifact.Approval.ReadyForPR))
		b.WriteString(fmt.Sprintf("**Summary:** %s\n", artifact.Approval.Summary))
	}

	return b.String()
}

// relativePath creates relative paths between artifact directories
func (g *ReviewGenerator) relativePath(fromDir, toDir, targetPath string) string {
	targetBase := filepath.Base(targetPath)
	if fromDir == toDir {
		return targetBase
	}
	return filepath.Join("..", toDir, targetBase)
}

// categoryStatusEmoji returns an emoji for category validation status
func categoryStatusEmoji(status string) string {
	switch strings.ToLower(status) {
	case "pass":
		return "✓ PASS"
	case "fail":
		return "✗ FAIL"
	case "concern":
		return "⚠ CONCERN"
	default:
		return status
	}
}

// approvalEmoji returns an emoji for approval status
func approvalEmoji(status string) string {
	switch strings.ToLower(status) {
	case "approved", "approved_with_nits":
		return "✅"
	case "changes_requested":
		return "🔄"
	default:
		return "⏳"
	}
}

// formatStatus formats status strings for display
func formatStatus(status string) string {
	// Replace underscores with spaces and title case
	formatted := strings.ReplaceAll(status, "_", " ")
	words := strings.Fields(formatted)
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}

// filterIssuesBySeverity filters issues by severity level
func filterIssuesBySeverity(issues []Issue, severity string) []Issue {
	var filtered []Issue
	for _, issue := range issues {
		if strings.ToLower(issue.Severity) == strings.ToLower(severity) {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

// groupImprovementsByCategory groups improvements by their category
func groupImprovementsByCategory(improvements []Improvement) map[string][]Improvement {
	groups := make(map[string][]Improvement)
	for _, improvement := range improvements {
		groups[improvement.Category] = append(groups[improvement.Category], improvement)
	}
	return groups
}
