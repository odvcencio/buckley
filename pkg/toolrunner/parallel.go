package toolrunner

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/model"
)

type toolCallMeta struct {
	index int
	mode  string
	path  string
}

func buildToolCallBatches(calls []model.ToolCall) [][]toolCallMeta {
	if len(calls) == 0 {
		return nil
	}
	batches := make([][]toolCallMeta, 0)
	for i, call := range calls {
		meta := toolCallMeta{
			index: i,
			mode:  toolAccessMode(call.Function.Name),
			path:  normalizeToolPath(extractToolPath(call.Function.Arguments)),
		}

		minBatch := 0
		for batchIdx, batch := range batches {
			if toolCallConflicts(meta, batch) && batchIdx+1 > minBatch {
				minBatch = batchIdx + 1
			}
		}

		placed := false
		for batchIdx := minBatch; batchIdx < len(batches); batchIdx++ {
			if !toolCallConflicts(meta, batches[batchIdx]) {
				batches[batchIdx] = append(batches[batchIdx], meta)
				placed = true
				break
			}
		}
		if !placed {
			batches = append(batches, []toolCallMeta{meta})
		}
	}
	return batches
}

func toolCallConflicts(meta toolCallMeta, batch []toolCallMeta) bool {
	for _, existing := range batch {
		if toolCallsConflict(meta, existing) {
			return true
		}
	}
	return false
}

func toolCallsConflict(a, b toolCallMeta) bool {
	if a.path == "" || b.path == "" {
		return false
	}
	if a.path != b.path {
		return false
	}
	if a.mode == "read" && b.mode == "read" {
		return false
	}
	if a.mode == "" || b.mode == "" {
		return true
	}
	return true
}

func toolAccessMode(name string) string {
	switch name {
	case "read_file", "list_directory", "find_files", "file_exists", "search_text":
		return "read"
	case "write_file", "patch_file", "edit_file", "edit_file_terminal", "insert_text", "delete_lines", "search_replace", "rename_symbol", "extract_function", "mark_resolved":
		return "write"
	default:
		return ""
	}
}

func extractToolPath(args string) string {
	if strings.TrimSpace(args) == "" {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		return ""
	}
	if value, ok := parsed["path"].(string); ok {
		return value
	}
	if value, ok := parsed["file"].(string); ok {
		return value
	}
	return ""
}

func normalizeToolPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}
