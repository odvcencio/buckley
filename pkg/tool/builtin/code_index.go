package builtin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
)

// LookupContextTool exposes indexed project context.
type LookupContextTool struct {
	Store *storage.Store
}

func (t *LookupContextTool) Name() string {
	return "lookup_context"
}

func (t *LookupContextTool) Description() string {
	return "Query the project code index for relevant files or symbols. Returns indexed information about functions, types, and files matching your query. Use this to quickly find where code is defined without reading multiple files. Faster than grep for finding specific definitions."
}

func (t *LookupContextTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"query": {
				Type:        "string",
				Description: "General search query applied to file paths and summaries.",
			},
			"path": {
				Type:        "string",
				Description: "Optional glob (e.g. pkg/buckley/ui/*.go) to restrict results.",
			},
			"symbol": {
				Type:        "string",
				Description: "Filter for symbol names (functions/types).",
			},
			"limit": {
				Type:        "integer",
				Description: "Maximum number of records (default 20).",
			},
		},
	}
}

func (t *LookupContextTool) Execute(params map[string]any) (*Result, error) {
	if t.Store == nil {
		return &Result{Success: false, Error: "code index is not available"}, nil
	}

	query := strings.TrimSpace(getStringParam(params, "query"))
	pathGlob := strings.TrimSpace(getStringParam(params, "path"))
	symbol := strings.TrimSpace(getStringParam(params, "symbol"))
	limit := getIntParam(params, "limit", 20)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	files, err := t.Store.SearchFiles(ctx, query, pathGlob, limit)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("file search failed: %v", err)}, nil
	}

	symbols := []storage.SymbolRecord{}
	if symbol != "" {
		symbols, err = t.Store.SearchSymbols(ctx, symbol, pathGlob, limit)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("symbol search failed: %v", err)}, nil
		}
	}

	fileData := make([]map[string]any, 0, len(files))
	for _, f := range files {
		fileData = append(fileData, map[string]any{
			"path":       f.Path,
			"language":   f.Language,
			"size_bytes": f.SizeBytes,
			"summary":    f.Summary,
			"updated_at": f.UpdatedAt.Format(time.RFC3339),
		})
	}

	symbolData := make([]map[string]any, 0, len(symbols))
	for _, s := range symbols {
		symbolData = append(symbolData, map[string]any{
			"name":       s.Name,
			"kind":       s.Kind,
			"file_path":  s.FilePath,
			"signature":  s.Signature,
			"start_line": s.StartLine,
			"end_line":   s.EndLine,
		})
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"files":   fileData,
			"symbols": symbolData,
		},
		DisplayData: map[string]any{
			"summary": fmt.Sprintf("Found %d file(s), %d symbol(s)", len(fileData), len(symbolData)),
		},
	}, nil
}

func getStringParam(params map[string]any, key string) string {
	if val, ok := params[key]; ok {
		if s, ok := val.(string); ok {
			return s
		}
	}
	return ""
}

func getIntParam(params map[string]any, key string, def int) int {
	if val, ok := params[key]; ok {
		switch v := val.(type) {
		case int:
			return v
		case float64:
			return int(v)
		}
	}
	return def
}
