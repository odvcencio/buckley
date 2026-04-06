package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/filewatch"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/touch"
)

// FileChangeTracking emits file change notifications for write/edit tools.
func FileChangeTracking(watcher *filewatch.FileWatcher) Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			res, err := next(ctx)
			if watcher == nil || ctx == nil {
				return res, err
			}
			if err != nil || res == nil || !res.Success || res.NeedsApproval {
				return res, err
			}

			changes := extractFileChanges(ctx, res)
			for _, change := range changes {
				watcher.Notify(change)
			}
			return res, err
		}
	}
}

func extractFileChanges(ctx *ExecutionContext, res *builtin.Result) []filewatch.FileChange {
	if ctx == nil {
		return nil
	}
	toolName := strings.TrimSpace(ctx.ToolName)
	if toolName == "" {
		return nil
	}

	switch toolName {
	case "write_file":
		path := filePathFromResult(res)
		if path == "" {
			path = stringFromParams(ctx.Params, "path")
		}
		if path == "" {
			return nil
		}
		changeType := filewatch.ChangeModified
		if isNew, ok := boolFromMap(res.DisplayData, "is_new"); ok && isNew {
			changeType = filewatch.ChangeCreated
		}
		return []filewatch.FileChange{buildChange(ctx, path, changeType)}
	case "edit_file":
		path := filePathFromResult(res)
		if path == "" {
			path = stringFromParams(ctx.Params, "path")
		}
		if path == "" {
			return nil
		}
		return []filewatch.FileChange{buildChange(ctx, path, filewatch.ChangeModified)}
	case "delete_file":
		path := filePathFromResult(res)
		if path == "" {
			path = stringFromParams(ctx.Params, "path")
		}
		if path == "" {
			return nil
		}
		change := buildChange(ctx, path, filewatch.ChangeDeleted)
		return []filewatch.FileChange{change}
	case "apply_patch":
		rich := touch.ExtractFromArgs(toolName, ctx.Params)
		path := strings.TrimSpace(rich.FilePath)
		if path == "" {
			return nil
		}
		return []filewatch.FileChange{buildChange(ctx, path, filewatch.ChangeModified)}
	default:
		return nil
	}
}

func buildChange(ctx *ExecutionContext, path string, changeType filewatch.ChangeType) filewatch.FileChange {
	path = strings.TrimSpace(path)
	size, modTime := statPath(path)
	return filewatch.FileChange{
		Path:     filepath.Clean(path),
		Type:     changeType,
		Size:     size,
		ModTime:  modTime,
		ToolName: strings.TrimSpace(ctx.ToolName),
		CallID:   strings.TrimSpace(ctx.CallID),
	}
}

func statPath(path string) (int64, time.Time) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, time.Time{}
	}
	return info.Size(), info.ModTime()
}

func filePathFromResult(res *builtin.Result) string {
	if res == nil {
		return ""
	}
	if res.DiffPreview != nil {
		if path := strings.TrimSpace(res.DiffPreview.FilePath); path != "" {
			return path
		}
	}
	if path := stringFromMap(res.Data, "path"); path != "" {
		return path
	}
	if path := stringFromMap(res.DisplayData, "path"); path != "" {
		return path
	}
	return ""
}

func stringFromParams(params map[string]any, key string) string {
	return stringFromMap(params, key)
}

func stringFromMap(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, ok := data[key]
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

func boolFromMap(data map[string]any, key string) (bool, bool) {
	if data == nil {
		return false, false
	}
	value, ok := data[key]
	if !ok {
		return false, false
	}
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		trimmed := strings.TrimSpace(strings.ToLower(v))
		if trimmed == "true" || trimmed == "1" || trimmed == "yes" {
			return true, true
		}
		if trimmed == "false" || trimmed == "0" || trimmed == "no" {
			return false, true
		}
	}
	return false, false
}
