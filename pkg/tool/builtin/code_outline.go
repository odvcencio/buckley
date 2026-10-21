package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// CodeOutlineTool reads a live Canopy outline without indexing the repository.
type CodeOutlineTool struct{ canopyRunner }

func (t *CodeOutlineTool) Name() string { return "code_outline" }
func (t *CodeOutlineTool) Description() string {
	return "Inspect a live file's symbol outline before reading implementation bodies. Uses Canopy search outline without building or caching a repository index. Returns bounded preorder symbol rows with kind/name/owner, depth, global parent_index, and inclusive start_line/end_line for read_file. Check coverage.definition_kinds, tree_has_error, parse_stopped_early, decline_reason, query_truncated, and omissions: an absent symbol is not proof of absence. Follow next_cursor unchanged with the same path; edits invalidate cursors. Specialized tool available through discover_tools."
}
func (t *CodeOutlineTool) Parameters() ParameterSchema {
	return ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"path":   {Type: "string", Description: "Literal source file inside the active workspace."},
		"limit":  {Type: "integer", Description: "Symbols per page, maximum 100.", Default: 40},
		"cursor": {Type: "string", Description: "Returned next_cursor, unchanged; omit for the first page."},
	}, Required: []string{"path"}, AdditionalProperties: false}
}
func (t *CodeOutlineTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *CodeOutlineTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok || path == "" || len(path) > 4096 {
		return &Result{Error: "path must be a non-empty literal file path of at most 4096 bytes"}, nil
	}
	root := t.workDir
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
	}
	_, relative, err := resolveRelPath(root, path)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	limit := getIntParam(params, "limit", 40)
	if limit < 1 {
		limit = 40
	}
	limit = min(limit, 100)
	args := []string{"search", "outline", "--json", "--limit", strconv.Itoa(limit)}
	if cursor, ok := params["cursor"].(string); ok && cursor != "" {
		if len(cursor) > 128 {
			return &Result{Error: "invalid outline cursor"}, nil
		}
		args = append(args, "--cursor", cursor)
	}
	args = append(args, "--", relative)
	output, truncated, err := t.run(ctx, args...)
	if err != nil {
		if strings.Contains(err.Error(), "unknown command") {
			err = fmt.Errorf("code_outline requires Canopy with search outline support; update the Canopy CLI: %w", err)
		}
		return &Result{Error: err.Error()}, nil
	}
	if truncated {
		return &Result{Error: "Canopy outline exceeded the capture budget; reduce limit and retry"}, nil
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(output), &data); err != nil {
		return &Result{Error: "Canopy returned an invalid outline JSON response"}, nil
	}
	if data["format_version"] != float64(1) {
		return &Result{Error: "unsupported Canopy outline format; update the Canopy CLI"}, nil
	}
	if _, ok := data["symbols"].([]any); !ok {
		return &Result{Error: "Canopy outline response is missing symbol rows"}, nil
	}
	return &Result{Success: true, Data: data}, nil
}
