package builtin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ReadFileTool reads a file from disk
type ReadFileTool struct{ workDirAware }

func (t *ReadFileTool) Name() string {
	return "read_file"
}

func (t *ReadFileTool) Description() string {
	return "Read file contents. Large files (>100 lines) are automatically summarized in conversation while full content remains available for analysis. Use this to examine code, configuration, or documentation files."
}

func (t *ReadFileTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the file to read",
			},
		},
		Required: []string{"path"},
	}
}

func (t *ReadFileTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "path parameter must be a string",
		}, nil
	}

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	if t.maxFileSizeBytes > 0 {
		if info, err := os.Stat(absPath); err == nil && info.Size() > t.maxFileSizeBytes {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("file too large: %d bytes (max %d)", info.Size(), t.maxFileSizeBytes),
			}, nil
		}
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	contentStr := string(content)

	// Abridge large files (> 100 lines) for display
	shouldAbridge := false
	displayContent := contentStr
	const maxDisplayLines = 100

	lines := strings.Split(contentStr, "\n")
	if len(lines) > maxDisplayLines {
		shouldAbridge = true
		displayContent = strings.Join(lines[:maxDisplayLines], "\n")
		displayContent += fmt.Sprintf("\n... (%d more lines, %d total)", len(lines)-maxDisplayLines, len(lines))
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"path":    absPath,
			"content": contentStr, // Full content always in Data
			"size":    len(content),
		},
		ShouldAbridge: shouldAbridge,
	}

	if shouldAbridge {
		result.DisplayData = map[string]any{
			"path":    absPath,
			"content": displayContent, // Abridged content for display
			"size":    len(content),
			"preview": fmt.Sprintf("Read %s (%d lines, %d bytes)", filepath.Base(absPath), len(lines), len(content)),
		}
	}

	return result, nil
}

// WriteFileTool writes content to a file
type WriteFileTool struct{ workDirAware }

func (t *WriteFileTool) Name() string {
	return "write_file"
}

func (t *WriteFileTool) Description() string {
	return "Write or create a file with specified content. Creates parent directories automatically. Shows a compact summary instead of echoing content back. Use this to create new files or overwrite existing ones."
}

func (t *WriteFileTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the file to write",
			},
			"content": {
				Type:        "string",
				Description: "Content to write to the file",
			},
		},
		Required: []string{"path", "content"},
	}
}

func (t *WriteFileTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "path parameter must be a string",
		}, nil
	}

	content, ok := params["content"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "content parameter must be a string",
		}, nil
	}

	if t.maxFileSizeBytes > 0 && int64(len(content)) > t.maxFileSizeBytes {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("content too large: %d bytes (max %d)", len(content), t.maxFileSizeBytes),
		}, nil
	}

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	// Create parent directory if it doesn't exist
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to create directory: %v", err),
		}, nil
	}

	// Check if file exists for diff summary
	oldContent := ""
	existingFile, err := os.ReadFile(absPath)
	if err == nil {
		oldContent = string(existingFile)
	}

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to write file: %v", err),
		}, nil
	}

	lines := strings.Split(content, "\n")
	isNew := oldContent == ""

	result := &Result{
		Success: true,
		Data: map[string]any{
			"path":    absPath,
			"size":    len(content),
			"content": content, // Full content
		},
		ShouldAbridge: true,
	}

	// Show compact summary in conversation
	summary := fmt.Sprintf("✓ Wrote %s (%d lines, %d bytes)", filepath.Base(absPath), len(lines), len(content))
	if isNew {
		summary = fmt.Sprintf("✓ Created %s (%d lines, %d bytes)", filepath.Base(absPath), len(lines), len(content))
	}

	result.DisplayData = map[string]any{
		"path":    absPath,
		"size":    len(content),
		"summary": summary,
		"lines":   len(lines),
		"is_new":  isNew,
	}

	return result, nil
}

// ListDirectoryTool lists files in a directory
type ListDirectoryTool struct{ workDirAware }

func (t *ListDirectoryTool) Name() string {
	return "list_directory"
}

func (t *ListDirectoryTool) Description() string {
	return "List files and directories at a path. Returns name, type (file/directory), and size for each entry. Use this to explore directory structure or find files in a specific location."
}

func (t *ListDirectoryTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the directory to list",
				Default:     ".",
			},
		},
		Required: []string{},
	}
}

func (t *ListDirectoryTool) Execute(params map[string]any) (*Result, error) {
	path := "."
	if p, ok := params["path"].(string); ok {
		path = p
	}

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to read directory: %v", err),
		}, nil
	}

	files := []map[string]any{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, map[string]any{
			"name":   entry.Name(),
			"is_dir": entry.IsDir(),
			"size":   info.Size(),
		})
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"path":  absPath,
			"files": files,
			"count": len(files),
		},
	}, nil
}

// PatchFileTool applies a unified diff patch to the repository
type PatchFileTool struct{ workDirAware }

func (t *PatchFileTool) Name() string {
	return "apply_patch"
}

func (t *PatchFileTool) Description() string {
	return "Apply a unified diff patch to modify files. Supports standard patch format with configurable path stripping (-pN). Use this to apply code changes from diffs, pull requests, or version control systems."
}

func (t *PatchFileTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"patch": {
				Type:        "string",
				Description: "Unified diff/patch content to apply",
			},
			"strip": {
				Type:        "integer",
				Description: "Number of leading path components to strip when applying (patch -pN). Defaults to 0.",
				Default:     0,
			},
		},
		Required: []string{"patch"},
	}
}

func (t *PatchFileTool) Execute(params map[string]any) (*Result, error) {
	rawPatch, ok := params["patch"].(string)
	if !ok || strings.TrimSpace(rawPatch) == "" {
		return &Result{
			Success: false,
			Error:   "patch parameter must be a non-empty string",
		}, nil
	}

	strip := 0
	if v, exists := params["strip"]; exists {
		var parsedStrip int
		var err error
		switch value := v.(type) {
		case float64:
			parsedStrip = int(value)
		case int:
			parsedStrip = value
		case string:
			if strings.TrimSpace(value) == "" {
				parsedStrip = 0
			} else {
				parsedStrip, err = strconv.Atoi(value)
				if err != nil {
					return &Result{
						Success: false,
						Error:   fmt.Sprintf("strip parameter must be an integer: %v", err),
					}, nil
				}
			}
		default:
			return &Result{
				Success: false,
				Error:   "strip parameter must be an integer",
			}, nil
		}

		if parsedStrip < 0 {
			return &Result{
				Success: false,
				Error:   "strip parameter cannot be negative",
			}, nil
		}
		strip = parsedStrip
	}

	ctx, cancel := t.execContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, "patch", fmt.Sprintf("-p%d", strip), "-N", "-s")
	if strings.TrimSpace(t.workDir) != "" {
		cmd.Dir = strings.TrimSpace(t.workDir)
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)
	cmd.Stdin = strings.NewReader(rawPatch)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return &Result{
			Success: false,
			Error:   "patch command timed out",
		}, nil
	}
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("patch command failed: %v\n%s", err, strings.TrimSpace(string(output))),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"strip":   strip,
			"message": strings.TrimSpace(string(output)),
		},
	}, nil
}

// FindFilesTool finds files matching a pattern
type FindFilesTool struct{ workDirAware }

func (t *FindFilesTool) Name() string {
	return "find_files"
}

func (t *FindFilesTool) Description() string {
	return "Find files matching a glob pattern. Searches recursively from base directory. Patterns like '*.go' match files by extension, 'test_*.py' by prefix. Use this to locate specific files or file types across the codebase."
}

func (t *FindFilesTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"pattern": {
				Type:        "string",
				Description: "Glob pattern to match files (e.g., '*.go', 'src/**/*.ts')",
			},
			"base_path": {
				Type:        "string",
				Description: "Base directory to search from (default: current directory)",
				Default:     ".",
			},
		},
		Required: []string{"pattern"},
	}
}

func (t *FindFilesTool) Execute(params map[string]any) (*Result, error) {
	pattern, ok := params["pattern"].(string)
	if !ok || pattern == "" {
		return &Result{
			Success: false,
			Error:   "pattern parameter must be a non-empty string",
		}, nil
	}

	basePath := "."
	if bp, ok := params["base_path"].(string); ok && bp != "" {
		basePath = bp
	}

	absBasePath, err := resolvePath(t.workDir, basePath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	var matches []string
	err = filepath.Walk(absBasePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(absBasePath, path)
		if err != nil {
			return nil
		}

		matched, err := filepath.Match(pattern, filepath.Base(path))
		if err != nil {
			return nil
		}

		if matched {
			matches = append(matches, relPath)
		}

		return nil
	})

	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to search files: %v", err),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"pattern": pattern,
			"matches": matches,
			"count":   len(matches),
		},
	}, nil
}

// FileExistsTool checks if a file exists
type FileExistsTool struct{ workDirAware }

func (t *FileExistsTool) Name() string {
	return "file_exists"
}

func (t *FileExistsTool) Description() string {
	return "Check if a file or directory exists and get basic metadata. Returns existence status, type, size, permissions, and modification time. Use this before reading/writing files or to verify paths."
}

func (t *FileExistsTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to check for existence",
			},
		},
		Required: []string{"path"},
	}
}

func (t *FileExistsTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return &Result{
			Success: false,
			Error:   "path parameter must be a non-empty string",
		}, nil
	}

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	info, err := os.Stat(absPath)
	exists := err == nil

	result := map[string]any{
		"path":   absPath,
		"exists": exists,
	}

	if exists {
		result["is_dir"] = info.IsDir()
		result["size"] = info.Size()
		result["mode"] = info.Mode().String()
		result["modified"] = info.ModTime().Format("2006-01-02 15:04:05")
	}

	return &Result{
		Success: true,
		Data:    result,
	}, nil
}

// GetFileInfoTool gets metadata about a file
type GetFileInfoTool struct{ workDirAware }

func (t *GetFileInfoTool) Name() string {
	return "get_file_info"
}

func (t *GetFileInfoTool) Description() string {
	return "Get detailed metadata about a file or directory including name, size, type, permissions, and last modification time. More detailed than file_exists. Use this for file statistics and metadata inspection."
}

func (t *GetFileInfoTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the file or directory",
			},
		},
		Required: []string{"path"},
	}
}

func (t *GetFileInfoTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return &Result{
			Success: false,
			Error:   "path parameter must be a non-empty string",
		}, nil
	}

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to get file info: %v", err),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"path":     absPath,
			"name":     info.Name(),
			"size":     info.Size(),
			"is_dir":   info.IsDir(),
			"mode":     info.Mode().String(),
			"modified": info.ModTime().Format("2006-01-02 15:04:05"),
		},
	}, nil
}
