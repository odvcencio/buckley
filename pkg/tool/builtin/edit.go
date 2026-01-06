package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EditFileTool performs targeted string replacement edits in a file
// Similar to Claude Code's Edit tool - makes exact replacements
type EditFileTool struct {
	workDirAware
	// ShowDiffPreview when true will return diff preview requiring approval
	ShowDiffPreview bool
}

func (t *EditFileTool) Name() string {
	return "edit_file"
}

func (t *EditFileTool) Description() string {
	return "Make targeted edits to a file by replacing exact text. The old_string must match exactly (including whitespace and indentation). Use this for precise code modifications. Shows a diff preview before applying changes."
}

func (t *EditFileTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the file to edit",
			},
			"old_string": {
				Type:        "string",
				Description: "Exact text to find and replace (must match exactly including whitespace)",
			},
			"new_string": {
				Type:        "string",
				Description: "Text to replace old_string with",
			},
			"replace_all": {
				Type:        "boolean",
				Description: "If true, replace all occurrences. If false (default), only replace the first occurrence",
				Default:     false,
			},
		},
		Required: []string{"path", "old_string", "new_string"},
	}
}

func (t *EditFileTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "path parameter must be a string",
		}, nil
	}

	oldString, ok := params["old_string"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "old_string parameter must be a string",
		}, nil
	}

	newString, ok := params["new_string"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "new_string parameter must be a string",
		}, nil
	}

	replaceAll := false
	if ra, ok := params["replace_all"].(bool); ok {
		replaceAll = ra
	}

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Read existing file
	content, err := os.ReadFile(absPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	oldContent := string(content)

	// Check if old_string exists in content
	if !strings.Contains(oldContent, oldString) {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("old_string not found in file. Make sure the text matches exactly including whitespace."),
		}, nil
	}

	// Check for uniqueness if not replacing all
	if !replaceAll && strings.Count(oldContent, oldString) > 1 {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("old_string appears %d times in the file. Either provide a more specific string or use replace_all=true", strings.Count(oldContent, oldString)),
		}, nil
	}

	// Perform replacement
	var newContent string
	if replaceAll {
		newContent = strings.ReplaceAll(oldContent, oldString, newString)
	} else {
		newContent = strings.Replace(oldContent, oldString, newString, 1)
	}

	// Generate diff preview
	diffPreview := generateDiff(absPath, oldContent, newContent)

	// If showing diff preview, return without writing
	if t.ShowDiffPreview {
		return &Result{
			Success:       true,
			NeedsApproval: true,
			DiffPreview:   diffPreview,
			Data: map[string]any{
				"path":        absPath,
				"old_content": oldContent,
				"new_content": newContent,
				"preview":     diffPreview.Preview,
			},
			ShouldAbridge: true,
			DisplayData: map[string]any{
				"path":      absPath,
				"summary":   diffPreview.Preview,
				"diff_only": true,
			},
		}, nil
	}

	// Write the new content
	if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to write file: %v", err),
		}, nil
	}

	replacements := 1
	if replaceAll {
		replacements = strings.Count(oldContent, oldString)
	}

	summary := fmt.Sprintf("✓ Edited %s (+%d/-%d lines, %d replacement%s)",
		filepath.Base(absPath),
		diffPreview.LinesAdded,
		diffPreview.LinesRemoved,
		replacements,
		pluralize(replacements))

	return &Result{
		Success:       true,
		ShouldAbridge: true,
		DiffPreview:   diffPreview,
		Data: map[string]any{
			"path":          absPath,
			"replacements":  replacements,
			"lines_added":   diffPreview.LinesAdded,
			"lines_removed": diffPreview.LinesRemoved,
		},
		DisplayData: map[string]any{
			"path":    absPath,
			"summary": summary,
			"diff":    diffPreview.Preview,
		},
	}, nil
}

// generateDiff creates a diff preview between old and new content
func generateDiff(path, oldContent, newContent string) *DiffInfo {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	// Calculate line changes
	linesAdded := 0
	linesRemoved := 0
	oldSet := make(map[string]int)
	newSet := make(map[string]int)

	for _, line := range oldLines {
		if strings.TrimSpace(line) != "" {
			oldSet[line]++
		}
	}
	for _, line := range newLines {
		if strings.TrimSpace(line) != "" {
			newSet[line]++
		}
	}

	for line, count := range newSet {
		if oldCount, exists := oldSet[line]; exists {
			if count > oldCount {
				linesAdded += count - oldCount
			}
		} else {
			linesAdded += count
		}
	}

	for line, count := range oldSet {
		if newCount, exists := newSet[line]; exists {
			if count > newCount {
				linesRemoved += count - newCount
			}
		} else {
			linesRemoved += count
		}
	}

	// Generate unified diff
	unifiedDiff := generateUnifiedDiff(path, oldLines, newLines)

	// Create preview (first 15 lines)
	previewLines := strings.Split(unifiedDiff, "\n")
	var preview string
	if len(previewLines) > 15 {
		preview = strings.Join(previewLines[:15], "\n")
		preview += fmt.Sprintf("\n... (%d more lines)", len(previewLines)-15)
	} else {
		preview = unifiedDiff
	}

	return &DiffInfo{
		FilePath:     path,
		IsNew:        oldContent == "",
		LinesAdded:   linesAdded,
		LinesRemoved: linesRemoved,
		OldContent:   oldContent,
		NewContent:   newContent,
		UnifiedDiff:  unifiedDiff,
		Preview:      preview,
	}
}

// generateUnifiedDiff creates a unified diff format output
func generateUnifiedDiff(path string, oldLines, newLines []string) string {
	var diff strings.Builder

	diff.WriteString(fmt.Sprintf("--- %s\n", path))
	diff.WriteString(fmt.Sprintf("+++ %s\n", path))

	// Find changed regions
	i, j := 0, 0
	for i < len(oldLines) || j < len(newLines) {
		// Skip matching lines
		for i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j] {
			i++
			j++
		}

		if i >= len(oldLines) && j >= len(newLines) {
			break
		}

		// Found a change region
		startI, startJ := i, j

		// Find extent of change
		for i < len(oldLines) && (j >= len(newLines) || oldLines[i] != newLines[j]) {
			if i < len(oldLines) && (j >= len(newLines) || !containsAt(newLines[j:], oldLines[i])) {
				i++
			} else {
				break
			}
		}

		for j < len(newLines) && (i >= len(oldLines) || newLines[j] != oldLines[i]) {
			if j < len(newLines) && (i >= len(oldLines) || !containsAt(oldLines[i:], newLines[j])) {
				j++
			} else {
				break
			}
		}

		// Output the hunk
		if startI < i || startJ < j {
			// Context lines are tracked but not used in minimal diff output
			// contextStart := startI - 3
			// contextEnd := i + 3

			diff.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n",
				startI+1, i-startI,
				startJ+1, j-startJ))

			// Show removed lines
			for k := startI; k < i; k++ {
				diff.WriteString(fmt.Sprintf("-%s\n", oldLines[k]))
			}

			// Show added lines
			for k := startJ; k < j; k++ {
				diff.WriteString(fmt.Sprintf("+%s\n", newLines[k]))
			}
		}
	}

	return diff.String()
}

func containsAt(lines []string, target string) bool {
	for _, line := range lines {
		if line == target {
			return true
		}
	}
	return false
}

func pluralize(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

// InsertTextTool inserts text at a specific position in a file
type InsertTextTool struct{ workDirAware }

func (t *InsertTextTool) Name() string {
	return "insert_text"
}

func (t *InsertTextTool) Description() string {
	return "Insert text at a specific line number in a file. Use this to add new code without replacing existing content. Line numbers are 1-indexed."
}

func (t *InsertTextTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the file to edit",
			},
			"line": {
				Type:        "integer",
				Description: "Line number to insert at (1-indexed). Text is inserted before this line.",
			},
			"text": {
				Type:        "string",
				Description: "Text to insert",
			},
		},
		Required: []string{"path", "line", "text"},
	}
}

func (t *InsertTextTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "path parameter must be a string",
		}, nil
	}

	lineNum := 0
	switch v := params["line"].(type) {
	case float64:
		lineNum = int(v)
	case int:
		lineNum = v
	default:
		return &Result{
			Success: false,
			Error:   "line parameter must be an integer",
		}, nil
	}

	text, ok := params["text"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "text parameter must be a string",
		}, nil
	}

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	oldContent := string(content)
	lines := strings.Split(oldContent, "\n")

	if lineNum < 1 {
		lineNum = 1
	}
	if lineNum > len(lines)+1 {
		lineNum = len(lines) + 1
	}

	// Insert the text
	insertLines := strings.Split(text, "\n")
	newLines := make([]string, 0, len(lines)+len(insertLines))
	newLines = append(newLines, lines[:lineNum-1]...)
	newLines = append(newLines, insertLines...)
	newLines = append(newLines, lines[lineNum-1:]...)

	newContent := strings.Join(newLines, "\n")

	// Generate diff
	diffPreview := generateDiff(absPath, oldContent, newContent)

	// Write file
	if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to write file: %v", err),
		}, nil
	}

	summary := fmt.Sprintf("✓ Inserted %d line%s at line %d in %s",
		len(insertLines), pluralize(len(insertLines)),
		lineNum, filepath.Base(absPath))

	return &Result{
		Success:       true,
		ShouldAbridge: true,
		DiffPreview:   diffPreview,
		Data: map[string]any{
			"path":           absPath,
			"line":           lineNum,
			"lines_inserted": len(insertLines),
		},
		DisplayData: map[string]any{
			"path":    absPath,
			"summary": summary,
			"diff":    diffPreview.Preview,
		},
	}, nil
}

// DeleteLinesool deletes a range of lines from a file
type DeleteLinesTool struct{ workDirAware }

func (t *DeleteLinesTool) Name() string {
	return "delete_lines"
}

func (t *DeleteLinesTool) Description() string {
	return "Delete a range of lines from a file. Line numbers are 1-indexed and inclusive."
}

func (t *DeleteLinesTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the file to edit",
			},
			"start_line": {
				Type:        "integer",
				Description: "First line to delete (1-indexed, inclusive)",
			},
			"end_line": {
				Type:        "integer",
				Description: "Last line to delete (1-indexed, inclusive)",
			},
		},
		Required: []string{"path", "start_line", "end_line"},
	}
}

func (t *DeleteLinesTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "path parameter must be a string",
		}, nil
	}

	startLine := 0
	switch v := params["start_line"].(type) {
	case float64:
		startLine = int(v)
	case int:
		startLine = v
	default:
		return &Result{
			Success: false,
			Error:   "start_line parameter must be an integer",
		}, nil
	}

	endLine := 0
	switch v := params["end_line"].(type) {
	case float64:
		endLine = int(v)
	case int:
		endLine = v
	default:
		return &Result{
			Success: false,
			Error:   "end_line parameter must be an integer",
		}, nil
	}

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	oldContent := string(content)
	lines := strings.Split(oldContent, "\n")

	if startLine < 1 {
		startLine = 1
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > endLine {
		return &Result{
			Success: false,
			Error:   "start_line must be less than or equal to end_line",
		}, nil
	}

	// Delete the lines
	newLines := make([]string, 0, len(lines)-(endLine-startLine+1))
	newLines = append(newLines, lines[:startLine-1]...)
	newLines = append(newLines, lines[endLine:]...)

	newContent := strings.Join(newLines, "\n")

	// Generate diff
	diffPreview := generateDiff(absPath, oldContent, newContent)

	// Write file
	if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to write file: %v", err),
		}, nil
	}

	linesDeleted := endLine - startLine + 1
	summary := fmt.Sprintf("✓ Deleted %d line%s (%d-%d) from %s",
		linesDeleted, pluralize(linesDeleted),
		startLine, endLine, filepath.Base(absPath))

	return &Result{
		Success:       true,
		ShouldAbridge: true,
		DiffPreview:   diffPreview,
		Data: map[string]any{
			"path":          absPath,
			"start_line":    startLine,
			"end_line":      endLine,
			"lines_deleted": linesDeleted,
		},
		DisplayData: map[string]any{
			"path":    absPath,
			"summary": summary,
			"diff":    diffPreview.Preview,
		},
	}, nil
}
