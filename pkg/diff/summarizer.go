package diff

import (
	"fmt"
	"strings"
)

// Summary represents a concise diff summary
type Summary struct {
	FilePath      string
	LinesAdded    int
	LinesRemoved  int
	LinesModified int
	Functions     []string // Names of modified functions/methods
	IsNew         bool
	IsDeleted     bool
	TotalLines    int
}

// DiffSummarizer creates concise summaries of code changes
type DiffSummarizer struct {
	maxContextLines int // How many lines of context to show
	maxFunctions    int // Max functions to list
}

// NewDiffSummarizer creates a new diff summarizer
func NewDiffSummarizer() *DiffSummarizer {
	return &DiffSummarizer{
		maxContextLines: 3,
		maxFunctions:    5,
	}
}

// SummarizeFileDiff creates a concise summary of file changes
func (ds *DiffSummarizer) SummarizeFileDiff(oldContent, newContent, filepath string) *Summary {
	summary := &Summary{
		FilePath: filepath,
	}

	// Check if new or deleted file
	if oldContent == "" && newContent != "" {
		summary.IsNew = true
		summary.LinesAdded = len(strings.Split(newContent, "\n"))
		summary.TotalLines = summary.LinesAdded
		return summary
	}
	if oldContent != "" && newContent == "" {
		summary.IsDeleted = true
		summary.LinesRemoved = len(strings.Split(oldContent, "\n"))
		return summary
	}

	// Calculate line changes
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	summary.TotalLines = len(newLines)

	// Simple diff algorithm
	added, removed := ds.calculateLineChanges(oldLines, newLines)
	summary.LinesAdded = added
	summary.LinesRemoved = removed

	// Detect modified functions
	summary.Functions = ds.detectModifiedFunctions(oldContent, newContent, filepath)

	return summary
}

// calculateLineChanges counts added and removed lines
func (ds *DiffSummarizer) calculateLineChanges(oldLines, newLines []string) (added, removed int) {
	// Create maps for quick lookup
	oldMap := make(map[string]int)
	newMap := make(map[string]int)

	for _, line := range oldLines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			oldMap[trimmed]++
		}
	}

	for _, line := range newLines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			newMap[trimmed]++
		}
	}

	// Count additions
	for line, count := range newMap {
		if oldCount, exists := oldMap[line]; exists {
			if count > oldCount {
				added += count - oldCount
			}
		} else {
			added += count
		}
	}

	// Count removals
	for line, count := range oldMap {
		if newCount, exists := newMap[line]; exists {
			if count > newCount {
				removed += count - newCount
			}
		} else {
			removed += count
		}
	}

	return added, removed
}

// detectModifiedFunctions finds function/method names that were changed
func (ds *DiffSummarizer) detectModifiedFunctions(oldContent, newContent, filepath string) []string {
	var functions []string

	// Detect language from extension
	ext := ""
	if idx := strings.LastIndex(filepath, "."); idx >= 0 {
		ext = filepath[idx:]
	}

	switch ext {
	case ".go":
		functions = ds.detectGoFunctions(oldContent, newContent)
	case ".js", ".ts", ".jsx", ".tsx":
		functions = ds.detectJSFunctions(oldContent, newContent)
	case ".py":
		functions = ds.detectPythonFunctions(oldContent, newContent)
	}

	// Limit to max functions
	if len(functions) > ds.maxFunctions {
		functions = functions[:ds.maxFunctions]
	}

	return functions
}

// detectGoFunctions finds modified Go functions
func (ds *DiffSummarizer) detectGoFunctions(oldContent, newContent string) []string {
	var functions []string
	seen := make(map[string]bool)

	// Find functions in new content
	for _, line := range strings.Split(newContent, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func ") {
			// Extract function name
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				name := parts[1]
				// Remove receiver if present
				if strings.HasPrefix(name, "(") {
					if len(parts) >= 3 {
						name = parts[2]
					}
				}
				// Clean up
				name = strings.TrimSuffix(name, "(")
				name = strings.TrimSuffix(name, "{")

				// Check if this function changed
				if !strings.Contains(oldContent, "func "+name) ||
					ds.functionBodyChanged(oldContent, newContent, name) {
					if !seen[name] {
						functions = append(functions, name)
						seen[name] = true
					}
				}
			}
		}
	}

	return functions
}

// detectJSFunctions finds modified JavaScript/TypeScript functions
func (ds *DiffSummarizer) detectJSFunctions(oldContent, newContent string) []string {
	var functions []string
	seen := make(map[string]bool)

	patterns := []string{
		"function ",
		"const ",
		"let ",
		"var ",
	}

	for _, line := range strings.Split(newContent, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, pattern := range patterns {
			if strings.HasPrefix(trimmed, pattern) {
				// Extract function name
				parts := strings.Fields(trimmed)
				if len(parts) >= 2 {
					name := parts[1]
					name = strings.TrimSuffix(name, "=")
					name = strings.TrimSuffix(name, "(")

					if !seen[name] && name != "" {
						functions = append(functions, name)
						seen[name] = true
					}
				}
			}
		}
	}

	return functions
}

// detectPythonFunctions finds modified Python functions
func (ds *DiffSummarizer) detectPythonFunctions(oldContent, newContent string) []string {
	var functions []string
	seen := make(map[string]bool)

	for _, line := range strings.Split(newContent, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "def ") {
			// Extract function name
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				name := parts[1]
				name = strings.TrimSuffix(name, "(")
				name = strings.TrimSuffix(name, ":")

				if !seen[name] {
					functions = append(functions, name)
					seen[name] = true
				}
			}
		}
	}

	return functions
}

// functionBodyChanged checks if a function's implementation changed
func (ds *DiffSummarizer) functionBodyChanged(oldContent, newContent, funcName string) bool {
	// Simple heuristic: if the function exists in both but the content differs
	oldHas := strings.Contains(oldContent, funcName)
	newHas := strings.Contains(newContent, funcName)

	if !oldHas || !newHas {
		return true
	}

	// Extract function context
	oldContext := ds.extractFunctionContext(oldContent, funcName)
	newContext := ds.extractFunctionContext(newContent, funcName)

	return oldContext != newContext
}

// extractFunctionContext gets a few lines around the function
func (ds *DiffSummarizer) extractFunctionContext(content, funcName string) string {
	lines := strings.Split(content, "\n")
	var context []string

	for i, line := range lines {
		if strings.Contains(line, funcName) {
			start := i
			end := i + 5
			if end > len(lines) {
				end = len(lines)
			}
			context = lines[start:end]
			break
		}
	}

	return strings.Join(context, "\n")
}

// Format returns a human-readable summary
func (s *Summary) Format() string {
	if s.IsNew {
		return fmt.Sprintf("✓ %s (new file, %d lines)", s.FilePath, s.LinesAdded)
	}

	if s.IsDeleted {
		return fmt.Sprintf("✗ %s (deleted, %d lines)", s.FilePath, s.LinesRemoved)
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("✎ %s", s.FilePath))

	if s.LinesAdded > 0 || s.LinesRemoved > 0 {
		parts = append(parts, fmt.Sprintf("+%d/-%d lines", s.LinesAdded, s.LinesRemoved))
	}

	if len(s.Functions) > 0 {
		funcList := strings.Join(s.Functions, ", ")
		parts = append(parts, fmt.Sprintf("(%s)", funcList))
	}

	return strings.Join(parts, " ")
}

// FormatCompact returns an even more compact summary
func (s *Summary) FormatCompact() string {
	icon := "✎"
	if s.IsNew {
		icon = "+"
	} else if s.IsDeleted {
		icon = "-"
	}

	fileName := s.FilePath
	if idx := strings.LastIndex(fileName, "/"); idx >= 0 {
		fileName = fileName[idx+1:]
	}

	return fmt.Sprintf("%s %s (+%d/-%d)", icon, fileName, s.LinesAdded, s.LinesRemoved)
}

// AbridgedDiff creates an abridged version of a full diff for display
type AbridgedDiff struct {
	Summary     *Summary
	Preview     string // First few lines of changes
	FullDiff    string // Complete diff (not shown in chat)
	IsTruncated bool
}

// CreateAbridgedDiff generates a compact diff preview
func (ds *DiffSummarizer) CreateAbridgedDiff(oldContent, newContent, filepath string) *AbridgedDiff {
	summary := ds.SummarizeFileDiff(oldContent, newContent, filepath)

	// Generate full diff
	fullDiff := ds.generateUnifiedDiff(oldContent, newContent, filepath)

	// Create preview (first N lines)
	preview := ds.createPreview(fullDiff)

	return &AbridgedDiff{
		Summary:     summary,
		Preview:     preview,
		FullDiff:    fullDiff,
		IsTruncated: len(fullDiff) > len(preview),
	}
}

// generateUnifiedDiff creates a unified diff format
func (ds *DiffSummarizer) generateUnifiedDiff(oldContent, newContent, filepath string) string {
	var diff strings.Builder

	diff.WriteString(fmt.Sprintf("--- %s\n", filepath))
	diff.WriteString(fmt.Sprintf("+++ %s\n", filepath))

	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	// Simple line-by-line diff
	maxLen := len(oldLines)
	if len(newLines) > maxLen {
		maxLen = len(newLines)
	}

	for i := 0; i < maxLen; i++ {
		oldLine := ""
		newLine := ""

		if i < len(oldLines) {
			oldLine = oldLines[i]
		}
		if i < len(newLines) {
			newLine = newLines[i]
		}

		if oldLine != newLine {
			if oldLine != "" {
				diff.WriteString(fmt.Sprintf("-%s\n", oldLine))
			}
			if newLine != "" {
				diff.WriteString(fmt.Sprintf("+%s\n", newLine))
			}
		}
	}

	return diff.String()
}

// createPreview generates a compact preview of changes
func (ds *DiffSummarizer) createPreview(fullDiff string) string {
	lines := strings.Split(fullDiff, "\n")

	maxLines := 10 // Show first 10 lines of diff
	if len(lines) <= maxLines {
		return fullDiff
	}

	preview := strings.Join(lines[:maxLines], "\n")
	preview += fmt.Sprintf("\n... (%d more lines)", len(lines)-maxLines)

	return preview
}

// BatchSummary summarizes multiple file changes
type BatchSummary struct {
	Summaries    []*Summary
	TotalAdded   int
	TotalRemoved int
	FilesChanged int
	FilesNew     int
	FilesDeleted int
}

// SummarizeBatch creates a summary of multiple file changes
func (ds *DiffSummarizer) SummarizeBatch(changes map[string][2]string) *BatchSummary {
	batch := &BatchSummary{}

	for filepath, contents := range changes {
		oldContent := contents[0]
		newContent := contents[1]

		summary := ds.SummarizeFileDiff(oldContent, newContent, filepath)
		batch.Summaries = append(batch.Summaries, summary)

		batch.TotalAdded += summary.LinesAdded
		batch.TotalRemoved += summary.LinesRemoved

		if summary.IsNew {
			batch.FilesNew++
		} else if summary.IsDeleted {
			batch.FilesDeleted++
		} else {
			batch.FilesChanged++
		}
	}

	return batch
}

// Format returns a human-readable batch summary
func (bs *BatchSummary) Format() string {
	var parts []string

	if bs.FilesNew > 0 {
		parts = append(parts, fmt.Sprintf("%d new", bs.FilesNew))
	}
	if bs.FilesChanged > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", bs.FilesChanged))
	}
	if bs.FilesDeleted > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", bs.FilesDeleted))
	}

	summary := fmt.Sprintf("📝 Changed %s files (+%d/-%d lines)",
		strings.Join(parts, ", "), bs.TotalAdded, bs.TotalRemoved)

	return summary
}

// FormatDetailed returns a detailed batch summary
func (bs *BatchSummary) FormatDetailed() string {
	var output strings.Builder

	output.WriteString(bs.Format() + "\n")

	for _, s := range bs.Summaries {
		output.WriteString("  " + s.Format() + "\n")
	}

	return output.String()
}
