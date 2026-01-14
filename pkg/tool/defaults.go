package tool

import (
	"time"

	"github.com/odvcencio/buckley/pkg/filewatch"
	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/ui/progress"
	"github.com/odvcencio/buckley/pkg/ui/toast"
)

const (
	DefaultToolTimeout      = 2 * time.Minute
	DefaultToolMaxResult    = 100_000
	DefaultRetryMaxAttempts = 2
	DefaultRetryInitial     = 200 * time.Millisecond
	DefaultRetryMax         = 2 * time.Second
	DefaultRetryMultiplier  = 2
	DefaultRetryJitter      = 0.2
)

// MiddlewareConfig configures the default middleware stack.
type MiddlewareConfig struct {
	ToastManager    *toast.ToastManager
	ProgressManager *progress.ProgressManager
	FileWatcher     *filewatch.FileWatcher

	DefaultTimeout  time.Duration
	PerToolTimeouts map[string]time.Duration
	RetryConfig     RetryConfig
	MaxResultBytes  int
	LongRunningTools map[string]string

	ValidationConfig ValidationConfig
	OnValidationError func(tool, param, msg string)
}

// RegistryConfig configures registry defaults and middleware options.
type RegistryConfig struct {
	TelemetryHub       *telemetry.Hub
	TelemetrySessionID string
	HookRegistry       *HookRegistry

	MissionStore           *mission.Store
	MissionSessionID       string
	MissionAgentID         string
	MissionTimeout         time.Duration
	RequireMissionApproval bool

	MaxOutputBytes int
	Middleware     MiddlewareConfig
}

// DefaultMiddlewareStack returns the default middleware chain.
func DefaultMiddlewareStack(cfg MiddlewareConfig) []Middleware {
	longRunning := cfg.LongRunningTools
	if longRunning == nil {
		longRunning = DefaultLongRunningTools
	}

	chain := []Middleware{
		PanicRecovery(),
		ToastNotifications(cfg.ToastManager),
		Validation(cfg.ValidationConfig, cfg.OnValidationError),
		ResultSizeLimit(cfg.MaxResultBytes, "\n...[truncated]"),
		Retry(cfg.RetryConfig),
		Timeout(cfg.DefaultTimeout, cfg.PerToolTimeouts),
		Progress(cfg.ProgressManager, longRunning),
		FileChangeTracking(cfg.FileWatcher),
	}
	return chain
}

// DefaultRegistryConfig returns baseline defaults for registry setup.
func DefaultRegistryConfig() RegistryConfig {
	return RegistryConfig{
		MaxOutputBytes: DefaultToolMaxResult,
		Middleware: MiddlewareConfig{
			DefaultTimeout: DefaultToolTimeout,
			RetryConfig: RetryConfig{
				MaxAttempts:  DefaultRetryMaxAttempts,
				InitialDelay: DefaultRetryInitial,
				MaxDelay:     DefaultRetryMax,
				Multiplier:   DefaultRetryMultiplier,
				Jitter:       DefaultRetryJitter,
			},
			MaxResultBytes:  DefaultToolMaxResult,
			LongRunningTools: DefaultLongRunningTools,
		},
	}
}

// ApplyRegistryConfig applies registry defaults and middleware settings.
func ApplyRegistryConfig(registry *Registry, cfg RegistryConfig) {
	if registry == nil {
		return
	}
	if cfg.HookRegistry != nil {
		registry.mu.Lock()
		registry.hooks = cfg.HookRegistry
		registry.mu.Unlock()
	}
	if cfg.MaxOutputBytes > 0 {
		registry.SetMaxOutputBytes(cfg.MaxOutputBytes)
	}
	if cfg.TelemetryHub != nil {
		registry.EnableTelemetry(cfg.TelemetryHub, cfg.TelemetrySessionID)
	}
	if cfg.MissionStore != nil {
		registry.EnableMissionControl(cfg.MissionStore, cfg.MissionAgentID, cfg.RequireMissionApproval, cfg.MissionTimeout)
		if cfg.MissionSessionID != "" {
			registry.UpdateMissionSession(cfg.MissionSessionID)
		}
	}

	for _, mw := range DefaultMiddlewareStack(cfg.Middleware) {
		registry.Use(mw)
	}
}
