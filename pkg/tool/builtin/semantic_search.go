package builtin

import (
	"context"
	"fmt"

	"github.com/odvcencio/buckley/pkg/embeddings"
)

// SemanticSearchTool provides semantic code search using embeddings
type SemanticSearchTool struct {
	searcher *embeddings.Searcher
}

// NewSemanticSearchTool creates a new semantic search tool
func NewSemanticSearchTool(searcher *embeddings.Searcher) *SemanticSearchTool {
	return &SemanticSearchTool{
		searcher: searcher,
	}
}

// Name returns the tool name
func (t *SemanticSearchTool) Name() string {
	return "semantic_search"
}

// Description returns the tool description
func (t *SemanticSearchTool) Description() string {
	return "Search codebase using semantic similarity. Finds code based on meaning rather than exact text matches. Use when looking for implementations of concepts, similar patterns, or related functionality. Requires index to be built first using manage_embeddings_index."
}

// Parameters returns the tool parameter schema
func (t *SemanticSearchTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"query": {
				Type:        "string",
				Description: "Natural language description of what you're looking for (e.g., 'functions that handle HTTP requests', 'error handling patterns', 'database connection code')",
			},
			"limit": {
				Type:        "string",
				Description: "Maximum number of results to return (default: 5, max: 20)",
				Default:     "5",
			},
		},
		Required: []string{"query"},
	}
}

// Execute runs the semantic search
func (t *SemanticSearchTool) Execute(params map[string]any) (*Result, error) {
	if t.searcher == nil {
		return &Result{
			Success: false,
			Error:   "Semantic search not available - embeddings index not initialized. Use manage_embeddings_index to build an index first.",
		}, nil
	}

	// Parse query
	query, ok := params["query"].(string)
	if !ok || query == "" {
		return &Result{
			Success: false,
			Error:   "query parameter is required and must be a string",
		}, nil
	}

	// Parse limit
	limit := 5
	if limitStr, ok := params["limit"].(string); ok {
		if parsed, err := fmt.Sscanf(limitStr, "%d", &limit); err == nil && parsed == 1 {
			if limit < 1 {
				limit = 1
			}
			if limit > 20 {
				limit = 20
			}
		}
	}

	// Perform search
	ctx := context.Background()
	results, err := t.searcher.Search(ctx, query, limit)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("Search failed: %v", err),
		}, nil
	}

	if len(results) == 0 {
		return &Result{
			Success: true,
			Data: map[string]any{
				"query":   query,
				"count":   0,
				"message": "No results found. Index may be empty or query too specific.",
			},
		}, nil
	}

	// Format results
	formattedResults := make([]map[string]any, 0, len(results))
	for i, result := range results {
		// Truncate content for display
		content := result.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}

		formattedResults = append(formattedResults, map[string]any{
			"rank":       i + 1,
			"file":       result.Metadata["file"],
			"similarity": fmt.Sprintf("%.3f", result.Similarity),
			"preview":    content,
		})
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"query":   query,
			"count":   len(results),
			"results": formattedResults,
		},
	}, nil
}

// IndexManagementTool provides index building and management
type IndexManagementTool struct {
	searcher *embeddings.Searcher
}

// NewIndexManagementTool creates a new index management tool
func NewIndexManagementTool(searcher *embeddings.Searcher) *IndexManagementTool {
	return &IndexManagementTool{
		searcher: searcher,
	}
}

// Name returns the tool name
func (t *IndexManagementTool) Name() string {
	return "manage_embeddings_index"
}

// Description returns the tool description
func (t *IndexManagementTool) Description() string {
	return "Manage the semantic search index. Build index for a directory to enable semantic_search, clear the index, or check status. Building an index generates embeddings for all code files and may take time depending on codebase size."
}

// Parameters returns the tool parameter schema
func (t *IndexManagementTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"action": {
				Type:        "string",
				Description: "Action to perform: 'build' to index files, 'clear' to remove all embeddings, 'status' to check index state",
			},
			"path": {
				Type:        "string",
				Description: "Directory path to index (required for 'build' action, typically '.' for current directory)",
			},
		},
		Required: []string{"action"},
	}
}

// Execute runs the index management command
func (t *IndexManagementTool) Execute(params map[string]any) (*Result, error) {
	if t.searcher == nil {
		return &Result{
			Success: false,
			Error:   "Embeddings service not available - check OPENAI_API_KEY is set",
		}, nil
	}

	action, ok := params["action"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "action parameter is required",
		}, nil
	}

	ctx := context.Background()

	switch action {
	case "build":
		path, ok := params["path"].(string)
		if !ok || path == "" {
			return &Result{
				Success: false,
				Error:   "path parameter is required for build action",
			}, nil
		}

		report, err := t.searcher.IndexDirectory(ctx, path)
		if err != nil {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("Failed to build index: %v", err),
			}, nil
		}

		count, _ := t.searcher.GetIndexCount(ctx)
		return &Result{
			Success: true,
			Data: map[string]any{
				"message":       fmt.Sprintf("Indexed directory: %s", path),
				"indexed_items": count,
				"embedded":      report.Embedded,
				"skipped":       report.Skipped,
				"errors":        report.Errors,
			},
		}, nil

	case "clear":
		err := t.searcher.ClearIndex(ctx)
		if err != nil {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("Failed to clear index: %v", err),
			}, nil
		}

		return &Result{
			Success: true,
			Data: map[string]any{
				"message": "Index cleared successfully",
			},
		}, nil

	case "status":
		count, err := t.searcher.GetIndexCount(ctx)
		if err != nil {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("Failed to get status: %v", err),
			}, nil
		}

		return &Result{
			Success: true,
			Data: map[string]any{
				"indexed_items":   count,
				"ready_to_search": count > 0,
				"status":          fmt.Sprintf("%d files indexed", count),
			},
		}, nil

	default:
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("Unknown action: %s. Valid actions are: build, clear, status", action),
		}, nil
	}
}
