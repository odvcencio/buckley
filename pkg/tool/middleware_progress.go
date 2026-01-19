package tool

import (
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/fluffy-ui/progress"
)

// DefaultLongRunningTools lists tools that should trigger progress indicators.
var DefaultLongRunningTools = map[string]string{
	"run_shell":          "Shell command",
	"search_text":        "Searching",
	"search_replace":     "Replacing",
	"find_files":         "Scanning files",
	"git_diff":           "Diff",
	"git_log":            "Git log",
	"analyze_complexity": "Analyzing complexity",
	"find_duplicates":    "Finding duplicates",
	"generate_test":      "Generating tests",
}

// Progress reports tool execution progress to the manager.
func Progress(manager *progress.ProgressManager, longRunning map[string]string) Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if manager == nil || ctx == nil {
				return next(ctx)
			}
			tools := longRunning
			if tools == nil {
				tools = DefaultLongRunningTools
			}
			name := strings.TrimSpace(ctx.ToolName)
			label, ok := tools[name]
			if !ok {
				return next(ctx)
			}
			if strings.TrimSpace(label) == "" {
				label = name
			}
			if strings.TrimSpace(ctx.CallID) == "" {
				ctx.CallID = ulid.Make().String()
			}
			manager.Start(ctx.CallID, label, progress.ProgressIndeterminate, 0)
			defer manager.Done(ctx.CallID)
			return next(ctx)
		}
	}
}
