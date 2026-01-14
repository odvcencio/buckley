package tool

import (
	"strings"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/ui/toast"
)

// ToastNotifications emits toast messages for tool failures.
func ToastNotifications(manager *toast.ToastManager) Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			res, err := next(ctx)
			if manager == nil {
				return res, err
			}
			if err == nil && (res == nil || res.Success) {
				return res, err
			}

			name := "Tool"
			if ctx != nil && strings.TrimSpace(ctx.ToolName) != "" {
				name = strings.TrimSpace(ctx.ToolName)
			}
			title := name + " failed"
			message := ""
			if err != nil {
				message = err.Error()
			}
			if message == "" && res != nil {
				message = res.Error
			}
			if strings.TrimSpace(message) == "" {
				message = "tool execution failed"
			}

			manager.Error(title, message)
			return res, err
		}
	}
}
