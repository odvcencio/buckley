package artifact

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Chain represents a linked chain of artifacts (planning → execution → review → PR)
type Chain struct {
	Feature           string
	PlanningArtifact  string // Path to planning.md
	ExecutionArtifact string // Path to execution.md
	ReviewArtifact    string // Path to review.md
	PRDocument        string // Path to pr-{number}.md (if archived)
	IsArchived        bool
	ArchivePath       string // Path to archive directory if archived
}

// ChainManager handles artifact chain operations
type ChainManager struct {
	docsRoot string // Root docs directory
}

// NewChainManager creates a new chain manager
func NewChainManager(docsRoot string) *ChainManager {
	return &ChainManager{
		docsRoot: docsRoot,
	}
}

// FindChain discovers the artifact chain for a given feature
// Looks in both active directories and archives
func (m *ChainManager) FindChain(feature string) (*Chain, error) {
	chain := &Chain{
		Feature: feature,
	}

	// Try to find in active directories first
	planningPath := m.findArtifact("plans", feature, "planning")
	executionPath := m.findArtifact("execution", feature, "execution")
	reviewPath := m.findArtifact("reviews", feature, "review")

	if planningPath != "" || executionPath != "" || reviewPath != "" {
		chain.PlanningArtifact = planningPath
		chain.ExecutionArtifact = executionPath
		chain.ReviewArtifact = reviewPath
		chain.IsArchived = false
		return chain, nil
	}

	// Try to find in archives
	archivePath := m.findInArchive(feature)
	if archivePath != "" {
		chain.IsArchived = true
		chain.ArchivePath = archivePath
		chain.PlanningArtifact = filepath.Join(archivePath, "planning.md")
		chain.ExecutionArtifact = filepath.Join(archivePath, "execution.md")
		chain.ReviewArtifact = filepath.Join(archivePath, "review.md")

		// Check for PR document
		prFiles, _ := filepath.Glob(filepath.Join(archivePath, "pr-*.md"))
		if len(prFiles) > 0 {
			chain.PRDocument = prFiles[0]
		}

		return chain, nil
	}

	return nil, fmt.Errorf("no artifact chain found for feature: %s", feature)
}

// UpdateLinks updates the artifact chain links in all artifact files
// This is called after moving artifacts to archive or when creating new artifacts
func (m *ChainManager) UpdateLinks(chain *Chain) error {
	// Update planning artifact links
	if chain.PlanningArtifact != "" && fileExists(chain.PlanningArtifact) {
		if err := m.updatePlanningLinks(chain); err != nil {
			return fmt.Errorf("failed to update planning links: %w", err)
		}
	}

	// Update execution artifact links
	if chain.ExecutionArtifact != "" && fileExists(chain.ExecutionArtifact) {
		if err := m.updateExecutionLinks(chain); err != nil {
			return fmt.Errorf("failed to update execution links: %w", err)
		}
	}

	// Update review artifact links
	if chain.ReviewArtifact != "" && fileExists(chain.ReviewArtifact) {
		if err := m.updateReviewLinks(chain); err != nil {
			return fmt.Errorf("failed to update review links: %w", err)
		}
	}

	return nil
}

// updatePlanningLinks adds/updates chain navigation in planning artifact
func (m *ChainManager) updatePlanningLinks(chain *Chain) error {
	content, err := os.ReadFile(chain.PlanningArtifact)
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")

	// Find the header and add links after it
	var newLines []string
	headerFound := false
	linksAdded := false

	for _, line := range lines {
		// If we find the title line and haven't added links yet
		if strings.HasPrefix(line, "# Planning:") && !linksAdded {
			newLines = append(newLines, line)
			newLines = append(newLines, "")
			newLines = append(newLines, m.generatePlanningChainLinks(chain)...)
			headerFound = true
			linksAdded = true
			continue
		}

		// Skip old chain link lines
		if headerFound && (strings.HasPrefix(line, "**Chain:**") || strings.HasPrefix(line, "**Next:**")) {
			continue
		}

		newLines = append(newLines, line)
	}

	// Write updated content
	return os.WriteFile(chain.PlanningArtifact, []byte(strings.Join(newLines, "\n")), 0644)
}

// updateExecutionLinks adds/updates chain navigation in execution artifact
func (m *ChainManager) updateExecutionLinks(chain *Chain) error {
	// Execution artifacts are updated incrementally, so links are already maintained
	// This is a no-op for now, but kept for consistency
	return nil
}

// updateReviewLinks adds/updates chain navigation in review artifact
func (m *ChainManager) updateReviewLinks(chain *Chain) error {
	// Review artifacts already include chain links in their header
	// This is a no-op for now, but kept for consistency
	return nil
}

// generatePlanningChainLinks creates the chain navigation links for planning artifact
func (m *ChainManager) generatePlanningChainLinks(chain *Chain) []string {
	var links []string

	if chain.IsArchived {
		// Archived format: all artifacts in same directory
		parts := []string{}
		if chain.ExecutionArtifact != "" {
			parts = append(parts, "[Execution](execution.md)")
		}
		if chain.ReviewArtifact != "" {
			parts = append(parts, "[Review](review.md)")
		}
		if chain.PRDocument != "" {
			prBase := filepath.Base(chain.PRDocument)
			parts = append(parts, fmt.Sprintf("[PR](%s)", prBase))
		}

		if len(parts) > 0 {
			links = append(links, fmt.Sprintf("**Chain:** %s", strings.Join(parts, " | ")))
		}

		links = append(links, fmt.Sprintf("**Archived:** %s", "true"))
	} else {
		// Active format: link to other directories
		parts := []string{}
		if chain.ExecutionArtifact != "" {
			execBase := filepath.Base(chain.ExecutionArtifact)
			parts = append(parts, fmt.Sprintf("[Execution](../execution/%s)", execBase))
		}
		if chain.ReviewArtifact != "" {
			reviewBase := filepath.Base(chain.ReviewArtifact)
			parts = append(parts, fmt.Sprintf("[Review](../reviews/%s)", reviewBase))
		}

		if len(parts) > 0 {
			links = append(links, fmt.Sprintf("**Next:** %s", strings.Join(parts, " | ")))
		}
	}

	return links
}

// findArtifact searches for an artifact file in a specific directory
func (m *ChainManager) findArtifact(dir, feature, artifactType string) string {
	// Look for files matching: *-{feature}-{artifactType}.md
	searchPath := filepath.Join(m.docsRoot, dir, fmt.Sprintf("*-%s-%s.md", feature, artifactType))
	matches, err := filepath.Glob(searchPath)
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0] // Return most recent if multiple
}

// findInArchive searches for a feature in the archive directories
func (m *ChainManager) findInArchive(feature string) string {
	archiveRoot := filepath.Join(m.docsRoot, "archive")

	// Walk through archive/YYYY-MM directories
	entries, err := os.ReadDir(archiveRoot)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Look for feature directory within month
		featurePath := filepath.Join(archiveRoot, entry.Name(), feature)
		if _, err := os.Stat(featurePath); err == nil {
			return featurePath
		}
	}

	return ""
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
