package builtin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var whitespaceNormalizer = regexp.MustCompile(`\s+`)

// FindDuplicatesTool finds duplicate code blocks
type FindDuplicatesTool struct{ workDirAware }

func (t *FindDuplicatesTool) Name() string {
	return "find_duplicates"
}

func (t *FindDuplicatesTool) Description() string {
	return "Find duplicate code blocks in a file or directory. Returns groups of duplicate code with locations."
}

func (t *FindDuplicatesTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "File or directory to analyze",
			},
			"min_lines": {
				Type:        "integer",
				Description: "Minimum number of consecutive lines to consider as duplicate (default: 5)",
				Default:     5,
			},
			"language": {
				Type:        "string",
				Description: "Optional: language hint for better detection",
			},
		},
		Required: []string{"path"},
	}
}

func (t *FindDuplicatesTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return &Result{
			Success: false,
			Error:   "path parameter must be a non-empty string",
		}, nil
	}
	if strings.TrimSpace(t.workDir) != "" {
		abs, err := resolvePath(t.workDir, path)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		path = abs
	}

	minLines := 5
	if ml, ok := params["min_lines"].(float64); ok {
		minLines = int(ml)
	}

	// Try using external tools first
	duplicates := []map[string]any{}
	var err error

	// Try jscpd (copy-paste detector)
	if _, execErr := exec.LookPath("jscpd"); execErr == nil {
		duplicates, err = t.runJSCPD(path, minLines)
	} else {
		// Fallback to simple duplicate detection
		duplicates, err = t.findSimpleDuplicates(path, minLines)
	}

	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("duplicate detection failed: %v", err),
		}, nil
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"path":       path,
			"min_lines":  minLines,
			"duplicates": duplicates,
			"count":      len(duplicates),
		},
	}

	// Abridge if many duplicates
	if len(duplicates) > 10 {
		result.ShouldAbridge = true
		result.DisplayData = map[string]any{
			"path":       path,
			"min_lines":  minLines,
			"duplicates": duplicates[:10],
			"count":      len(duplicates),
			"summary":    fmt.Sprintf("Found %d duplicate code blocks (showing first 10)", len(duplicates)),
		}
	}

	return result, nil
}

func (t *FindDuplicatesTool) runJSCPD(path string, minLines int) ([]map[string]any, error) {
	// jscpd can be complex to parse, for now return empty
	// This would need proper implementation with JSON output parsing
	return []map[string]any{}, nil
}

func (t *FindDuplicatesTool) findSimpleDuplicates(path string, minLines int) ([]map[string]any, error) {
	// Simple hash-based duplicate detection
	// This is a basic implementation - production would use more sophisticated algorithms

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	var files []string
	if info.IsDir() {
		_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && t.isCodeFile(p) {
				files = append(files, p)
			}
			return nil
		})
	} else {
		files = []string{path}
	}

	// Map of code hash -> locations
	codeBlocks := make(map[string][]map[string]any)

	for _, file := range files {
		blocks, err := t.extractCodeBlocks(file, minLines)
		if err != nil {
			continue
		}

		for _, block := range blocks {
			hash := t.hashCode(block["code"].(string))
			codeBlocks[hash] = append(codeBlocks[hash], block)
		}
	}

	// Find duplicates (blocks that appear more than once)
	duplicates := []map[string]any{}
	for _, locations := range codeBlocks {
		if len(locations) > 1 {
			duplicates = append(duplicates, map[string]any{
				"locations": locations,
				"count":     len(locations),
			})
		}
	}

	return duplicates, nil
}

func (t *FindDuplicatesTool) extractCodeBlocks(filepath string, minLines int) ([]map[string]any, error) {
	content, err := os.ReadFile(filepath)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(content), "\n")
	blocks := []map[string]any{}

	// Extract all possible blocks of minLines length
	for i := 0; i <= len(lines)-minLines; i++ {
		blockLines := lines[i : i+minLines]
		// Skip blocks that are mostly whitespace or comments
		if t.isSignificantBlock(blockLines) {
			blocks = append(blocks, map[string]any{
				"file":       filepath,
				"start_line": i + 1,
				"end_line":   i + minLines,
				"code":       strings.Join(blockLines, "\n"),
			})
		}
	}

	return blocks, nil
}

func (t *FindDuplicatesTool) isSignificantBlock(lines []string) bool {
	significantLines := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "//") && !strings.HasPrefix(trimmed, "#") {
			significantLines++
		}
	}
	return significantLines >= len(lines)/2
}

func (t *FindDuplicatesTool) hashCode(code string) string {
	// Normalize code for comparison (remove whitespace variations)
	normalized := whitespaceNormalizer.ReplaceAllString(code, " ")
	return fmt.Sprintf("%x", normalized)
}

func (t *FindDuplicatesTool) isCodeFile(path string) bool {
	exts := []string{".go", ".js", ".ts", ".py", ".java", ".c", ".cpp", ".rs"}
	for _, ext := range exts {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}
