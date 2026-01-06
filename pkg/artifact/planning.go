package artifact

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PlanningGenerator creates planning artifacts from structured data
type PlanningGenerator struct {
	outputDir string // Directory where artifacts are saved (e.g., "docs/plans")
}

// NewPlanningGenerator creates a new planning artifact generator
func NewPlanningGenerator(outputDir string) *PlanningGenerator {
	return &PlanningGenerator{
		outputDir: outputDir,
	}
}

// Generate creates a planning artifact and writes it to disk
// Returns the file path and any error
func (g *PlanningGenerator) Generate(artifact *PlanningArtifact) (string, error) {
	// Ensure output directory exists
	if err := os.MkdirAll(g.outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}

	// Generate filename: YYYY-MM-DD-{feature}-planning.md
	now := time.Now()
	filename := fmt.Sprintf("%s-%s-planning.md", now.Format("2006-01-02"), artifact.Feature)
	filePath := filepath.Join(g.outputDir, filename)

	// Generate markdown content
	content := g.generateMarkdown(artifact)

	// Write to file
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write planning artifact: %w", err)
	}

	// Update artifact metadata
	artifact.FilePath = filePath
	artifact.CreatedAt = now
	artifact.UpdatedAt = now
	artifact.Status = "completed"

	return filePath, nil
}

// generateMarkdown converts a planning artifact to markdown format
func (g *PlanningGenerator) generateMarkdown(artifact *PlanningArtifact) string {
	var b strings.Builder

	// Header
	b.WriteString(fmt.Sprintf("# Planning: %s\n\n", formatFeatureName(artifact.Feature)))
	b.WriteString(fmt.Sprintf("**Date:** %s\n", artifact.CreatedAt.Format("2006-01-02")))
	b.WriteString(fmt.Sprintf("**Status:** %s\n", artifact.Status))
	b.WriteString("**Type:** Planning Artifact\n\n")

	// Context Section
	b.WriteString("## 1. Context\n\n")
	b.WriteString(fmt.Sprintf("**User Goal:** %s\n\n", artifact.Context.UserGoal))
	b.WriteString(fmt.Sprintf("**Architecture Style:** %s\n\n", artifact.Context.ArchitectureStyle))

	if len(artifact.Context.ExistingPatterns) > 0 {
		b.WriteString("**Existing Patterns Detected:**\n")
		for _, pattern := range artifact.Context.ExistingPatterns {
			b.WriteString(fmt.Sprintf("- %s\n", pattern))
		}
		b.WriteString("\n")
	}

	if len(artifact.Context.RelevantFiles) > 0 {
		b.WriteString("**Relevant Files Analyzed:**\n")
		for _, file := range artifact.Context.RelevantFiles {
			b.WriteString(fmt.Sprintf("- `%s`\n", file))
		}
		b.WriteString("\n")
	}

	if artifact.Context.ResearchSummary != "" {
		b.WriteString("**Research Summary:**\n")
		b.WriteString(fmt.Sprintf("%s\n\n", artifact.Context.ResearchSummary))
	}

	if len(artifact.Context.ResearchRisks) > 0 {
		b.WriteString("**Top Research Risks:**\n")
		for _, risk := range artifact.Context.ResearchRisks {
			b.WriteString(fmt.Sprintf("- %s\n", risk))
		}
		b.WriteString("\n")
	}

	// Architecture Decisions
	if len(artifact.Decisions) > 0 {
		b.WriteString("## 2. Architecture Decisions\n\n")
		for i, decision := range artifact.Decisions {
			b.WriteString(fmt.Sprintf("### Decision %d: %s\n\n", i+1, decision.Title))

			if len(decision.Alternatives) > 0 {
				b.WriteString("**Alternatives Considered:**\n")
				for _, alt := range decision.Alternatives {
					b.WriteString(fmt.Sprintf("- %s\n", alt))
				}
				b.WriteString("\n")
			}

			b.WriteString(fmt.Sprintf("**Rationale:**\n%s\n\n", decision.Rationale))

			if len(decision.TradeOffs) > 0 {
				b.WriteString("**Trade-offs:**\n")
				for _, tradeoff := range decision.TradeOffs {
					b.WriteString(fmt.Sprintf("- %s\n", tradeoff))
				}
				b.WriteString("\n")
			}

			if len(decision.LayerImpact) > 0 {
				b.WriteString(fmt.Sprintf("**Layer Impact:** %s\n\n", strings.Join(decision.LayerImpact, ", ")))
			}
		}
	}

	// Code Contracts
	if len(artifact.CodeContracts) > 0 {
		b.WriteString("## 3. Code Contracts\n\n")
		for _, contract := range artifact.CodeContracts {
			b.WriteString(fmt.Sprintf("### %s Layer - `%s`\n\n", contract.Layer, contract.FilePath))
			if contract.Description != "" {
				b.WriteString(fmt.Sprintf("%s\n\n", contract.Description))
			}
			b.WriteString("```go\n")
			b.WriteString(contract.Code)
			b.WriteString("\n```\n\n")
		}
	}

	// Layer Map
	if len(artifact.LayerMap.Layers) > 0 {
		b.WriteString("## 4. Layer Map\n\n")
		for _, layer := range artifact.LayerMap.Layers {
			b.WriteString(fmt.Sprintf("### %s Layer\n\n", layer.Name))

			if len(layer.Files) > 0 {
				b.WriteString("**Files:**\n")
				for _, file := range layer.Files {
					b.WriteString(fmt.Sprintf("- `%s`\n", file))
				}
				b.WriteString("\n")
			}

			if len(layer.Dependencies) > 0 {
				b.WriteString(fmt.Sprintf("**Dependencies:** %s\n\n", strings.Join(layer.Dependencies, ", ")))
			}
		}
	}

	// Task Breakdown
	if len(artifact.Tasks) > 0 {
		b.WriteString("## 5. Task Breakdown\n\n")
		for _, task := range artifact.Tasks {
			b.WriteString(fmt.Sprintf("### Task %d: %s\n\n", task.ID, task.Description))
			b.WriteString(fmt.Sprintf("**File:** `%s`\n\n", task.FilePath))

			if task.Pseudocode != "" {
				b.WriteString("**Pseudocode:**\n```\n")
				b.WriteString(task.Pseudocode)
				b.WriteString("\n```\n\n")
			}

			if task.Complexity != "" {
				b.WriteString(fmt.Sprintf("**Complexity:** %s\n", task.Complexity))
			}
			if task.Maintainability != "" {
				b.WriteString(fmt.Sprintf("**Maintainability:** %s\n", task.Maintainability))
			}

			if len(task.Dependencies) > 0 {
				deps := make([]string, len(task.Dependencies))
				for i, dep := range task.Dependencies {
					deps[i] = fmt.Sprintf("Task %d", dep)
				}
				b.WriteString(fmt.Sprintf("**Dependencies:** %s\n", strings.Join(deps, ", ")))
			}

			if len(task.Verification) > 0 {
				b.WriteString("\n**Verification:**\n")
				for _, verify := range task.Verification {
					b.WriteString(fmt.Sprintf("- %s\n", verify))
				}
			}

			b.WriteString("\n")
		}
	}

	// Cross-Cutting Concerns
	b.WriteString("## 6. Cross-Cutting Concerns\n\n")

	if artifact.CrossCuttingScope.ErrorHandling != "" {
		b.WriteString(fmt.Sprintf("**Error Handling:** %s\n\n", artifact.CrossCuttingScope.ErrorHandling))
	}
	if artifact.CrossCuttingScope.Logging != "" {
		b.WriteString(fmt.Sprintf("**Logging:** %s\n\n", artifact.CrossCuttingScope.Logging))
	}
	if artifact.CrossCuttingScope.Testing != "" {
		b.WriteString(fmt.Sprintf("**Testing:** %s\n\n", artifact.CrossCuttingScope.Testing))
	}
	if len(artifact.CrossCuttingScope.Security) > 0 {
		b.WriteString("**Security Considerations:**\n")
		for _, sec := range artifact.CrossCuttingScope.Security {
			b.WriteString(fmt.Sprintf("- %s\n", sec))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// formatFeatureName converts a feature slug to a human-readable title
// Example: "user-auth" -> "User Authentication"
func formatFeatureName(feature string) string {
	words := strings.Split(feature, "-")
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}
