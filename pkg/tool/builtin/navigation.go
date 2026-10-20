package builtin

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// FindReferencesTool finds where symbols are used
type FindReferencesTool struct{ workDirAware }

func (t *FindReferencesTool) Name() string {
	return "find_references"
}

func (t *FindReferencesTool) Description() string {
	return "Find all places where a symbol is referenced or called. Returns file and line for each usage (first 50 shown)."
}

func (t *FindReferencesTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"symbol": {
				Type:        "string",
				Description: "Symbol name to find references for",
			},
			"path": {
				Type:        "string",
				Description: "Optional: directory to search (default: current directory)",
				Default:     ".",
			},
			"include_definition": {
				Type:        "boolean",
				Description: "Include the definition in results (default: false)",
				Default:     false,
			},
		},
		Required: []string{"symbol"},
	}
}

func (t *FindReferencesTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *FindReferencesTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	symbol, ok := params["symbol"].(string)
	if !ok || symbol == "" {
		return &Result{
			Success: false,
			Error:   "symbol parameter must be a non-empty string",
		}, nil
	}

	searchPath := "."
	if p, ok := params["path"].(string); ok && p != "" {
		searchPath = p
	}
	if strings.TrimSpace(t.workDir) != "" {
		_, rel, err := resolveRelPath(t.workDir, searchPath)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		searchPath = rel
	}

	includeDef := false
	if id, ok := params["include_definition"].(bool); ok {
		includeDef = id
	}

	// Search for the symbol as a word boundary
	pattern := fmt.Sprintf(`\b%s\b`, regexp.QuoteMeta(symbol))

	ctx, cancel := t.execContextWithParent(ctx)
	defer cancel()

	var cmd *exec.Cmd
	if _, err := exec.LookPath("rg"); err == nil {
		cmd = exec.CommandContext(ctx, "rg", "-n", "--no-heading", pattern, searchPath)
	} else {
		cmd = exec.CommandContext(ctx, "grep", "-rn", "-w", symbol, searchPath)
	}
	if strings.TrimSpace(t.workDir) != "" {
		cmd.Dir = strings.TrimSpace(t.workDir)
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)

	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return &Result{
			Success: false,
			Error:   "search command timed out",
		}, nil
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return &Result{
				Success: true,
				Data: map[string]any{
					"symbol":     symbol,
					"references": []map[string]any{},
					"count":      0,
				},
			}, nil
		}
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("search failed: %v", err),
		}, nil
	}

	references := []map[string]any{}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}

		lineNum := 0
		fmt.Sscanf(parts[1], "%d", &lineNum)

		content := strings.TrimSpace(parts[2])

		// Skip definitions unless requested
		if !includeDef {
			if strings.Contains(content, "func "+symbol) ||
				strings.Contains(content, "type "+symbol) ||
				strings.Contains(content, "class "+symbol) ||
				strings.Contains(content, "def "+symbol) {
				continue
			}
		}

		references = append(references, map[string]any{
			"file":    parts[0],
			"line":    lineNum,
			"content": content,
		})
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"symbol":     symbol,
			"references": references,
			"count":      len(references),
		},
	}

	// Abridge if many references
	const maxDisplay = 50
	if len(references) > maxDisplay {
		result.ShouldAbridge = true
		result.DisplayData = map[string]any{
			"symbol":     symbol,
			"references": references[:maxDisplay],
			"count":      len(references),
			"summary":    fmt.Sprintf("Found %d references to '%s' (showing first %d)", len(references), symbol, maxDisplay),
		}
	}

	return result, nil
}
