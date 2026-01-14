package execution

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/rlm"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/ui/progress"
	"github.com/odvcencio/buckley/pkg/ui/toast"
)

// RLMStrategy uses the coordinator/sub-agent pattern for execution.
//
// The coordinator receives only meta-tools (delegate, delegate_batch, inspect,
// set_answer) and delegates actual work to sub-agents with filtered tool access.
// This prevents overwhelming models with too many tools while enabling complex
// multi-step workflows.
//
// Benefits:
//   - Coordinator sees only 4 tools, making tool selection reliable
//   - Sub-agents get task-specific tools based on delegation parameters
//   - Weight-based model routing optimizes cost (trivial→fast, heavy→quality)
//   - Scratchpad enables cross-task visibility without context pollution
//   - Supports 200+ sequential tool calls via Kimi K2's agentic capabilities
type RLMStrategy struct {
	runtime       *rlm.Runtime
	config        StrategyConfig
	telemetry     *telemetry.Hub
	streamMu      sync.Mutex
	streamAdapter *RLMStreamAdapter
}

// RLMStrategyConfig extends StrategyConfig with RLM-specific options.
type RLMStrategyConfig struct {
	StrategyConfig

	// ModelManager is the concrete manager needed by RLM runtime.
	// This is separate from StrategyConfig.Models because the RLM
	// runtime requires the full Manager, not just the ModelClient interface.
	ModelManager *model.Manager

	// Store provides persistence for scratchpad entries
	Store *storage.Store

	// Telemetry receives iteration events
	Telemetry *telemetry.Hub

	// CoordinatorModel overrides the model used for coordination (optional)
	CoordinatorModel string

	// MaxWallTime limits total execution time (default 30m)
	MaxWallTime time.Duration

	// MaxTokensBudget limits total tokens across all iterations
	MaxTokensBudget int

	// SubAgentMaxConcurrent overrides the default sub-agent parallelism
	SubAgentMaxConcurrent int
}

// NewRLMStrategy creates a strategy using the RLM coordinator pattern.
func NewRLMStrategy(cfg RLMStrategyConfig) (*RLMStrategy, error) {
	if cfg.ModelManager == nil {
		return nil, fmt.Errorf("model manager required for RLM strategy")
	}

	// Build RLM config
	rlmCfg := rlm.DefaultConfig()

	if cfg.ConfidenceThreshold > 0 {
		rlmCfg.Coordinator.ConfidenceThreshold = cfg.ConfidenceThreshold
	}
	if cfg.DefaultMaxIterations > 0 {
		rlmCfg.Coordinator.MaxIterations = cfg.DefaultMaxIterations
	}
	if cfg.MaxWallTime > 0 {
		rlmCfg.Coordinator.MaxWallTime = cfg.MaxWallTime
	}
	if cfg.MaxTokensBudget > 0 {
		rlmCfg.Coordinator.MaxTokensBudget = cfg.MaxTokensBudget
	}
	if cfg.CoordinatorModel != "" {
		rlmCfg.Coordinator.Model = cfg.CoordinatorModel
	}
	if cfg.SubAgentMaxConcurrent > 0 {
		rlmCfg.SubAgent.MaxConcurrent = cfg.SubAgentMaxConcurrent
	}

	// Create runtime - uses the concrete ModelManager for RLM's needs
	runtime, err := rlm.NewRuntime(rlmCfg, rlm.RuntimeDeps{
		Models:    cfg.ModelManager,
		Store:     cfg.Store,
		Registry:  cfg.Registry,
		Telemetry: cfg.Telemetry,
		UseToon:   cfg.UseTOON,
	})
	if err != nil {
		return nil, fmt.Errorf("create RLM runtime: %w", err)
	}

	return &RLMStrategy{
		runtime:   runtime,
		config:    cfg.StrategyConfig,
		telemetry: cfg.Telemetry,
	}, nil
}

// Name returns the strategy identifier.
func (s *RLMStrategy) Name() string {
	return "rlm"
}

// SupportsStreaming indicates RLM strategy supports iteration callbacks but not true streaming.
func (s *RLMStrategy) SupportsStreaming() bool {
	return false // RLM uses iteration hooks, not streaming
}

// Execute processes the request using the RLM coordinator.
func (s *RLMStrategy) Execute(ctx context.Context, req ExecutionRequest) (*ExecutionResult, error) {
	if s.runtime == nil {
		return nil, fmt.Errorf("RLM runtime not initialized")
	}

	// Build the task prompt with context
	task := s.buildTaskPrompt(req)

	// Execute via RLM coordinator
	answer, err := s.runtime.Execute(ctx, task)
	if err != nil {
		return nil, fmt.Errorf("RLM execution: %w", err)
	}

	// Convert RLM answer to ExecutionResult
	result := &ExecutionResult{
		Content:    answer.Content,
		Confidence: answer.Confidence,
		Iterations: answer.Iteration,
		Artifacts:  answer.Artifacts,
		Usage: model.Usage{
			TotalTokens: answer.TokensUsed,
		},
	}

	s.emitStreamComplete(result)
	return result, nil
}

// buildTaskPrompt constructs the coordinator task from the request.
func (s *RLMStrategy) buildTaskPrompt(req ExecutionRequest) string {
	var sb strings.Builder

	// Add user prompt
	sb.WriteString(req.Prompt)

	// Add conversation context if available
	if req.Conversation != nil && len(req.Conversation.Messages) > 0 {
		messages := req.Conversation.Messages
		if req.ContextBuilder != nil {
			budget := req.ContextBudget
			if budget <= 0 {
				budget = s.rlmContextBudget(req)
			}
			messages = req.ContextBuilder.BuildMessages(req.Conversation, budget, "rlm")
		}
		if len(messages) > 0 {
			sb.WriteString("\n\n## Conversation Context\n")
			for _, msg := range messages {
				content := contentToString(msg.Content)
				if strings.TrimSpace(content) == "" && strings.TrimSpace(msg.Reasoning) != "" {
					content = msg.Reasoning
				}
				if strings.TrimSpace(content) == "" {
					continue
				}
				sb.WriteString(fmt.Sprintf("\n**%s**: %s", msg.Role, content))
			}
		}
	}

	// Add tool filter guidance if specified
	if len(req.AllowedTools) > 0 {
		sb.WriteString("\n\n## Available Tools\n")
		sb.WriteString("When delegating, restrict sub-agents to these tools: ")
		sb.WriteString(strings.Join(req.AllowedTools, ", "))
	}

	return sb.String()
}

// contentToString converts message content to string.
func contentToString(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		if text := conversation.GetContentAsString(v); strings.TrimSpace(text) != "" {
			return text
		}
		return fmt.Sprintf("%v", content)
	}
}

func (s *RLMStrategy) rlmContextBudget(req ExecutionRequest) int {
	modelID := ""
	if s != nil && s.config.Models != nil {
		modelID = s.config.Models.GetExecutionModel()
	}
	contextWindow := contextWindowForModel(s.config.Models, modelID)
	if contextWindow <= 0 {
		return 0
	}

	var base strings.Builder
	base.WriteString(req.Prompt)
	if len(req.AllowedTools) > 0 {
		base.WriteString("\n\n## Available Tools\n")
		base.WriteString("When delegating, restrict sub-agents to these tools: ")
		base.WriteString(strings.Join(req.AllowedTools, ", "))
	}

	budget := contextWindow - conversation.CountTokens(base.String())
	if budget <= 0 {
		return 0
	}
	headerTokens := conversation.CountTokens("## Conversation Context")
	budget -= headerTokens
	if budget < 0 {
		budget = 0
	}
	return budget
}

// SetStreamHandler attaches a stream handler for iteration updates.
func (s *RLMStrategy) SetStreamHandler(handler StreamHandler) {
	if s == nil {
		return
	}
	adapter := s.ensureStreamAdapter()
	if adapter == nil {
		return
	}
	adapter.SetHandler(handler)
}

// SetProgressManager attaches a progress manager for iteration updates.
func (s *RLMStrategy) SetProgressManager(manager *progress.ProgressManager) {
	if s == nil {
		return
	}
	adapter := s.ensureStreamAdapter()
	if adapter == nil {
		return
	}
	adapter.SetProgressManager(manager)
}

// SetToastManager attaches a toast manager for budget warnings.
func (s *RLMStrategy) SetToastManager(manager *toast.ToastManager) {
	if s == nil {
		return
	}
	adapter := s.ensureStreamAdapter()
	if adapter == nil {
		return
	}
	adapter.SetToastManager(manager)
}

// OnIteration registers a callback for iteration events.
// This enables progress tracking in the TUI without true streaming.
func (s *RLMStrategy) OnIteration(hook rlm.IterationHook) {
	if s.runtime != nil {
		s.runtime.OnIteration(hook)
	}
}

func (s *RLMStrategy) ensureStreamAdapter() *RLMStreamAdapter {
	if s == nil {
		return nil
	}
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.streamAdapter == nil {
		s.streamAdapter = NewRLMStreamAdapter(nil, nil, nil)
		if s.runtime != nil {
			s.runtime.OnIteration(s.streamAdapter.OnRLMEvent)
		}
	}
	return s.streamAdapter
}

func (s *RLMStrategy) emitStreamComplete(result *ExecutionResult) {
	if s == nil {
		return
	}
	s.streamMu.Lock()
	adapter := s.streamAdapter
	s.streamMu.Unlock()
	if adapter != nil {
		adapter.OnComplete(result)
	}
}
