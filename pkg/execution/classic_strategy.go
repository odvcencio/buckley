package execution

import (
	"context"
	"fmt"

	"github.com/odvcencio/buckley/pkg/toolrunner"
)

// ClassicStrategy implements single-agent execution using the ToolRunner loop.
type ClassicStrategy struct {
	config StrategyConfig
	runner *toolrunner.Runner
}

// NewClassicStrategy creates a single-agent strategy backed by ToolRunner.
func NewClassicStrategy(cfg StrategyConfig) (*ClassicStrategy, error) {
	if cfg.Models == nil {
		return nil, fmt.Errorf("model manager required")
	}
	if cfg.Registry == nil {
		return nil, fmt.Errorf("tool registry required")
	}

	runner, err := toolrunner.New(toolrunner.Config{
		Models:               cfg.Models,
		Registry:             cfg.Registry,
		DefaultMaxIterations: cfg.DefaultMaxIterations,
		EnableReasoning:      cfg.EnableReasoning,
	})
	if err != nil {
		return nil, err
	}

	return &ClassicStrategy{
		config: cfg,
		runner: runner,
	}, nil
}

// Name returns the strategy identifier.
func (s *ClassicStrategy) Name() string {
	return "classic"
}

// SupportsStreaming indicates this strategy supports streaming.
func (s *ClassicStrategy) SupportsStreaming() bool {
	return true
}

// SetStreamHandler configures streaming event handler.
func (s *ClassicStrategy) SetStreamHandler(handler StreamHandler) {
	if s == nil || s.runner == nil {
		return
	}
	s.runner.SetStreamHandler(&toolrunnerStreamAdapter{handler: handler})
}

// Execute processes the request using the ToolRunner loop.
func (s *ClassicStrategy) Execute(ctx context.Context, req ExecutionRequest) (*ExecutionResult, error) {
	if s == nil || s.runner == nil {
		return nil, fmt.Errorf("tool runner not initialized")
	}

	messages := buildMessages(req)
	result, err := s.runner.Run(ctx, toolrunner.Request{
		Messages:        messages,
		SelectionPrompt: req.Prompt,
		AllowedTools:    req.AllowedTools,
		MaxIterations:   req.MaxIterations,
		Model:           s.config.Models.GetExecutionModel(),
	})
	if err != nil {
		return nil, err
	}

	return toExecutionResult(result), nil
}
