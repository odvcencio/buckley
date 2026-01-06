package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ArchiveManager handles monthly archival of artifacts
type ArchiveManager struct {
	docsRoot    string
	archiveRoot string
	readmePath  string
}

// ArchivedFeature represents a feature that has been archived
type ArchivedFeature struct {
	Feature       string
	Month         string // YYYY-MM format
	PRNumber      int
	MergedDate    time.Time
	Summary       string
	PlanningPath  string
	ExecutionPath string
	ReviewPath    string
	PRPath        string
}

// NewArchiveManager creates a new archive manager
func NewArchiveManager(docsRoot string) *ArchiveManager {
	archiveRoot := filepath.Join(docsRoot, "archive")
	return &ArchiveManager{
		docsRoot:    docsRoot,
		archiveRoot: archiveRoot,
		readmePath:  filepath.Join(archiveRoot, "README.md"),
	}
}

// Archive moves a completed artifact chain to the monthly archive
func (m *ArchiveManager) Archive(feature *ArchivedFeature) error {
	// Use current date if not specified
	if feature.MergedDate.IsZero() {
		feature.MergedDate = time.Now()
	}

	// Generate month string: YYYY-MM
	if feature.Month == "" {
		feature.Month = feature.MergedDate.Format("2006-01")
	}

	// Create archive directory structure
	monthDir := filepath.Join(m.archiveRoot, feature.Month)
	featureDir := filepath.Join(monthDir, feature.Feature)

	if err := os.MkdirAll(featureDir, 0755); err != nil {
		return fmt.Errorf("failed to create archive directory: %w", err)
	}

	// Move artifacts
	artifacts := map[string]*string{
		"planning":  &feature.PlanningPath,
		"execution": &feature.ExecutionPath,
		"review":    &feature.ReviewPath,
	}

	for artifactType, pathPtr := range artifacts {
		if *pathPtr == "" {
			// Try to find the artifact
			found := m.findArtifact(artifactType, feature.Feature)
			if found != "" {
				*pathPtr = found
			} else {
				continue // Skip if not found
			}
		}

		// Move to archive
		destPath := filepath.Join(featureDir, fmt.Sprintf("%s.md", artifactType))
		if err := m.moveFile(*pathPtr, destPath); err != nil {
			return fmt.Errorf("failed to move %s artifact: %w", artifactType, err)
		}

		// Update path to archived location
		*pathPtr = destPath
	}

	// Create PR document if PR number provided
	if feature.PRNumber > 0 && feature.PRPath == "" {
		prPath := filepath.Join(featureDir, fmt.Sprintf("pr-%d.md", feature.PRNumber))
		if err := m.createPRDocument(prPath, feature); err != nil {
			return fmt.Errorf("failed to create PR document: %w", err)
		}
		feature.PRPath = prPath
	}

	// Update artifact chain links for archived format
	if err := m.updateArchivedLinks(featureDir); err != nil {
		return fmt.Errorf("failed to update archived links: %w", err)
	}

	// Update archive index
	if err := m.updateIndex(feature); err != nil {
		return fmt.Errorf("failed to update archive index: %w", err)
	}

	return nil
}

// findArtifact searches for an artifact in active directories
func (m *ArchiveManager) findArtifact(artifactType, feature string) string {
	var searchDir string
	switch artifactType {
	case "planning":
		searchDir = "plans"
	case "execution":
		searchDir = "execution"
	case "review":
		searchDir = "reviews"
	default:
		return ""
	}

	pattern := filepath.Join(m.docsRoot, searchDir, fmt.Sprintf("*-%s-%s.md", feature, artifactType))
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}

	return matches[0]
}

// moveFile moves a file from src to dest
func (m *ArchiveManager) moveFile(src, dest string) error {
	// Read source
	content, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	// Write destination
	if err := os.WriteFile(dest, content, 0644); err != nil {
		return err
	}

	// Remove source
	return os.Remove(src)
}

// createPRDocument creates a document with PR metadata
func (m *ArchiveManager) createPRDocument(path string, feature *ArchivedFeature) error {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("# PR #%d: %s\n\n", feature.PRNumber, formatFeatureName(feature.Feature)))
	b.WriteString(fmt.Sprintf("**Merged:** %s\n\n", feature.MergedDate.Format("2006-01-02")))

	if feature.Summary != "" {
		b.WriteString(fmt.Sprintf("**Summary:** %s\n\n", feature.Summary))
	}

	b.WriteString("## Artifact Chain\n\n")
	b.WriteString("- [Planning](planning.md)\n")
	b.WriteString("- [Execution](execution.md)\n")
	b.WriteString("- [Review](review.md)\n\n")

	b.WriteString("## Links\n\n")
	b.WriteString(fmt.Sprintf("- GitHub PR: #%d\n", feature.PRNumber))

	return os.WriteFile(path, []byte(b.String()), 0644)
}

// updateArchivedLinks updates links in artifacts to use relative archive paths
func (m *ArchiveManager) updateArchivedLinks(featureDir string) error {
	// Update planning artifact
	planningPath := filepath.Join(featureDir, "planning.md")
	if fileExists(planningPath) {
		if err := m.updatePlanningArchivedLinks(planningPath); err != nil {
			return err
		}
	}

	// Execution and review artifacts already have correct relative links
	return nil
}

// updatePlanningArchivedLinks updates the planning artifact header for archived format
func (m *ArchiveManager) updatePlanningArchivedLinks(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string

	headerFound := false
	linksUpdated := false

	for _, line := range lines {
		// Find title line
		if strings.HasPrefix(line, "# Planning:") && !linksUpdated {
			newLines = append(newLines, line)
			newLines = append(newLines, "")
			// Add archived links
			newLines = append(newLines, "**Chain:** [Execution](execution.md) | [Review](review.md)")
			newLines = append(newLines, "**Archived:** true")
			headerFound = true
			linksUpdated = true
			continue
		}

		// Skip old "Next:" or "Chain:" lines
		if headerFound && (strings.HasPrefix(line, "**Next:**") || strings.HasPrefix(line, "**Chain:**") || strings.HasPrefix(line, "**Archived:**")) {
			continue
		}

		if headerFound && strings.HasPrefix(line, "**Date:**") {
			// Reset headerFound after date line
			headerFound = false
		}

		newLines = append(newLines, line)
	}

	return os.WriteFile(path, []byte(strings.Join(newLines, "\n")), 0644)
}

// updateIndex adds the archived feature to the archive README
func (m *ArchiveManager) updateIndex(feature *ArchivedFeature) error {
	// Ensure README exists
	if !fileExists(m.readmePath) {
		// Create initial README
		initialContent := "# Documentation Archive\n\nThis directory contains historical artifact chains organized by month.\n\n## Index\n\n"
		if err := os.WriteFile(m.readmePath, []byte(initialContent), 0644); err != nil {
			return err
		}
	}

	// Read current README
	content, err := os.ReadFile(m.readmePath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string

	indexFound := false
	monthSectionExists := false
	insertionPoint := -1

	for i, line := range lines {
		newLines = append(newLines, line)

		// Find index section
		if strings.HasPrefix(line, "## Index") {
			indexFound = true
			continue
		}

		// Look for month heading
		if indexFound && strings.HasPrefix(line, fmt.Sprintf("## %s", feature.Month)) {
			monthSectionExists = true
		}

		// If we find a different month heading after our month, mark insertion point
		if indexFound && monthSectionExists && strings.HasPrefix(line, "## ") && !strings.Contains(line, feature.Month) {
			insertionPoint = i
		}
	}

	// If month section doesn't exist, add it
	if !monthSectionExists {
		newLines = append(newLines, "")
		newLines = append(newLines, fmt.Sprintf("## %s", feature.Month))
		newLines = append(newLines, "")
	}

	// Add feature entry
	entry := m.formatArchiveEntry(feature)

	if insertionPoint > 0 {
		// Insert before next month section
		newLines = append(newLines[:insertionPoint], append([]string{entry, ""}, newLines[insertionPoint:]...)...)
	} else {
		// Append at end
		newLines = append(newLines, entry, "")
	}

	return os.WriteFile(m.readmePath, []byte(strings.Join(newLines, "\n")), 0644)
}

// formatArchiveEntry formats a feature entry for the archive index
func (m *ArchiveManager) formatArchiveEntry(feature *ArchivedFeature) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("### [%s](%s/%s/)\n", formatFeatureName(feature.Feature), feature.Month, feature.Feature))

	if feature.PRNumber > 0 {
		b.WriteString(fmt.Sprintf("- **PR:** #%d\n", feature.PRNumber))
	}

	b.WriteString(fmt.Sprintf("- **Merged:** %s\n", feature.MergedDate.Format("2006-01-02")))

	if feature.Summary != "" {
		b.WriteString(fmt.Sprintf("- **Summary:** %s\n", feature.Summary))
	}

	// Artifact links
	b.WriteString(fmt.Sprintf("- **Artifacts:** [Planning](%s/%s/planning.md)", feature.Month, feature.Feature))
	b.WriteString(fmt.Sprintf(" | [Execution](%s/%s/execution.md)", feature.Month, feature.Feature))
	b.WriteString(fmt.Sprintf(" | [Review](%s/%s/review.md)", feature.Month, feature.Feature))

	if feature.PRPath != "" {
		prFilename := filepath.Base(feature.PRPath)
		b.WriteString(fmt.Sprintf(" | [PR](%s/%s/%s)", feature.Month, feature.Feature, prFilename))
	}

	return b.String()
}

// formatFeatureName converts a feature slug to title case
func formatFeatureName(feature string) string {
	words := strings.Split(feature, "-")
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}
