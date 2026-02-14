package tui

import (
	"context"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/filewatch"
	"github.com/odvcencio/buckley/pkg/mcp"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/toast"
)

// defaultTUIMaxOutputBytes limits tool output in TUI mode.
const defaultTUIMaxOutputBytes = 100_000
const defaultTUIAttachmentMaxBytes = 50_000

// progressTrackerAdapter wraps *progress.ProgressManager to satisfy tool.ProgressTracker.
type progressTrackerAdapter struct {
	mgr *progress.ProgressManager
}

func (a *progressTrackerAdapter) Start(id, label string, mode string, total int) {
	a.mgr.Start(id, label, progress.ProgressType(mode), total)
}

func (a *progressTrackerAdapter) Done(id string) {
	a.mgr.Done(id)
}

// buildRegistry creates the tool registry with all available tools.
func buildRegistry(ctx context.Context, cfg *config.Config, store *storage.Store, workDir string, hub *telemetry.Hub, sessionID string, progressMgr *progress.ProgressManager, toastMgr *toast.ToastManager) *tool.Registry {
	registry := tool.NewRegistry()

	registryCfg := tool.DefaultRegistryConfig()
	registryCfg.MaxOutputBytes = defaultTUIMaxOutputBytes
	if cfg != nil {
		if cfg.ToolMiddleware.MaxResultBytes > 0 {
			registryCfg.MaxOutputBytes = cfg.ToolMiddleware.MaxResultBytes
		}
		registryCfg.Middleware.DefaultTimeout = cfg.ToolMiddleware.DefaultTimeout
		registryCfg.Middleware.PerToolTimeouts = copyDurationMap(cfg.ToolMiddleware.PerToolTimeouts)
		registryCfg.Middleware.MaxResultBytes = cfg.ToolMiddleware.MaxResultBytes
		registryCfg.Middleware.RetryConfig = tool.RetryConfig{
			MaxAttempts:  cfg.ToolMiddleware.Retry.MaxAttempts,
			InitialDelay: cfg.ToolMiddleware.Retry.InitialDelay,
			MaxDelay:     cfg.ToolMiddleware.Retry.MaxDelay,
			Multiplier:   cfg.ToolMiddleware.Retry.Multiplier,
			Jitter:       cfg.ToolMiddleware.Retry.Jitter,
		}
	}
	registryCfg.TelemetryHub = hub
	registryCfg.TelemetrySessionID = sessionID
	registryCfg.Middleware.ProgressManager = &progressTrackerAdapter{mgr: progressMgr}
	registryCfg.Middleware.ToastManager = toastMgr
	registryCfg.Middleware.FileWatcher = filewatch.NewFileWatcher(100)
	tool.ApplyRegistryConfig(registry, registryCfg)
	if cfg != nil {
		registry.SetSandboxConfig(cfg.Sandbox.ToSandboxConfig(workDir))
	}

	// Configure container execution if enabled
	if cfg != nil && workDir != "" {
		registry.ConfigureContainers(cfg, workDir)
		registry.ConfigureDockerSandbox(cfg, workDir)
	}

	// Enable todo tracking
	if store != nil {
		registry.SetTodoStore(&todoStoreAdapter{store: store})
		registry.EnableCodeIndex(store)
	}

	// Load user plugins from ~/.buckley/plugins/ and ./.buckley/plugins/
	if err := registry.LoadDefaultPlugins(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load some plugins: %v\n", err)
	}

	if cfg != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		manager, err := mcp.ManagerFromConfig(ctx, cfg.MCP)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: MCP setup failed: %v\n", err)
		}
		if manager != nil {
			mcp.RegisterMCPTools(manager, func(_ string, toolAny any) {
				toolAdapter, ok := toolAny.(tool.Tool)
				if !ok {
					return
				}
				registry.Register(toolAdapter)
			})
		}
	}

	// Set working directory for file tools
	if workDir != "" {
		registry.SetWorkDir(workDir)
	}

	return registry
}
func copyDurationMap(src map[string]time.Duration) map[string]time.Duration {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(src))
	maps.Copy(out, src)
	return out
}
