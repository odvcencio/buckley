package toolrunner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/tool"
)

const (
	defaultMaxIterations  = 25
	defaultMaxToolsPhase1 = 15
	defaultMaxParallel    = 5
)

// Runner executes a tool loop with optional tool selection.
type Runner struct {
	config         Config
	streamHandler  StreamHandler
	maxToolsPhase1 int
	selectionCache *toolSelectionCache
}

// New creates a tool runner with the provided config.
func New(cfg Config) (*Runner, error) {
	if cfg.Models == nil {
		return nil, fmt.Errorf("model manager required")
	}
	if cfg.Registry == nil {
		return nil, fmt.Errorf("tool registry required")
	}

	maxIter := cfg.DefaultMaxIterations
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}

	maxToolsPhase1 := cfg.MaxToolsPhase1
	if maxToolsPhase1 <= 0 {
		maxToolsPhase1 = defaultMaxToolsPhase1
	}

	cfg.DefaultMaxIterations = maxIter
	cfg.MaxToolsPhase1 = maxToolsPhase1

	// Set default model timeout if not specified
	if cfg.ModelTimeout <= 0 {
		cfg.ModelTimeout = 2 * time.Minute
	}

	return &Runner{
		config:         cfg,
		maxToolsPhase1: maxToolsPhase1,
		selectionCache: newToolSelectionCache(cfg.CacheSize, cfg.CacheTTL),
	}, nil
}

// SetStreamHandler configures streaming event handler.
func (r *Runner) SetStreamHandler(handler StreamHandler) {
	r.streamHandler = handler
}

// CacheStats returns current cache statistics.
func (r *Runner) CacheStats() CacheStats {
	if r.selectionCache == nil {
		return CacheStats{}
	}
	return r.selectionCache.Stats()
}

// WarmCache pre-populates the tool selection cache with common contexts.
func (r *Runner) WarmCache(commonContexts []string, tools []tool.Tool) {
	if r.selectionCache == nil {
		return
	}
	r.selectionCache.WarmCache(commonContexts, tools)
}

// WarmCacheAsync pre-populates the tool selection cache asynchronously without blocking.
func (r *Runner) WarmCacheAsync(commonContexts []string, tools []tool.Tool) {
	if r.selectionCache == nil {
		return
	}
	r.selectionCache.WarmCacheAsync(commonContexts, tools)
}

// ResetCacheStats resets cache statistics to zero.
func (r *Runner) ResetCacheStats() {
	if r.selectionCache == nil {
		return
	}
	r.selectionCache.ResetStats()
}

func (r *Runner) notifyStreamError(err error) {
	if r == nil || r.streamHandler == nil || err == nil {
		return
	}
	r.streamHandler.OnError(err)
}

// Run processes the request using automatic tool loop.
func (r *Runner) Run(ctx context.Context, req Request) (*Result, error) {
	result := &Result{}

	// Apply model timeout if context doesn't have a deadline
	ctx, cancel := r.withModelTimeout(ctx)
	defer cancel()

	availableTools := r.availableTools(req.AllowedTools)

	var selectedTools []tool.Tool
	if len(availableTools) > r.maxToolsPhase1 {
		var err error
		selectedTools, err = r.selectTools(ctx, req, availableTools)
		if err != nil {
			selectedTools = availableTools
		}
	} else {
		selectedTools = availableTools
	}

	return r.executeWithTools(ctx, req, selectedTools, result)
}

// withModelTimeout applies the configured model timeout if the context doesn't already have a deadline.
func (r *Runner) withModelTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && r.config.ModelTimeout > 0 {
		return context.WithTimeout(ctx, r.config.ModelTimeout)
	}
	return ctx, func() {}
}

func (r *Runner) requestModel(req Request) string {
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" && r.config.Models != nil {
		modelID = r.config.Models.GetExecutionModel()
	}
	return modelID
}
