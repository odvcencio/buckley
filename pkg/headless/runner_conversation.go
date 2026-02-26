package headless

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/toolrunner"
)

func (r *Runner) processUserInput(content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("empty input")
	}

	r.setState(StateProcessing)
	defer func() {
		if r.State() == StateProcessing {
			r.setState(StateIdle)
		}
	}()

	// Add user message to conversation
	r.conv.AddUserMessage(content)

	// Save to storage
	userMsg := r.conv.Messages[len(r.conv.Messages)-1]
	if err := r.conv.SaveMessage(r.store, userMsg); err != nil {
		r.emitError("failed to save user message", err)
	}

	// Run the conversation loop
	return r.runConversationLoop()
}

func (r *Runner) runConversationLoop() error {
	ctx, cancel := context.WithCancel(r.baseContext())
	r.mu.Lock()
	r.cancelFunc = cancel
	r.mu.Unlock()
	defer cancel()

	if r.State() == StateStopped || r.State() == StatePaused {
		return nil
	}

	if r.tools == nil {
		return fmt.Errorf("tool registry required")
	}

	maxIterations := 50 // Prevent runaway loops
	modelID := r.executionModelID()
	runner, err := toolrunner.New(toolrunner.Config{
		Models:               &headlessModelClient{runner: r},
		Registry:             r.tools,
		DefaultMaxIterations: maxIterations,
		MaxToolsPhase1:       len(r.tools.List()),
		EnableReasoning:      true,
		ToolExecutor:         r.executeToolCall,
	})
	if err != nil {
		r.emitError("tool runner init failed", err)
		return fmt.Errorf("initializing tool runner: %w", err)
	}

	stopWatcher := make(chan struct{})
	go r.watchRunState(ctx, cancel, stopWatcher)
	defer close(stopWatcher)

	result, err := runner.Run(ctx, toolrunner.Request{
		Messages:      buildHeadlessMessages(r, modelID),
		MaxIterations: maxIterations,
		Model:         modelID,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		var toolErr toolExecutionError
		if errors.As(err, &toolErr) {
			return fmt.Errorf("tool execution: %w", err)
		}
		r.emitError("model call failed", err)
		return fmt.Errorf("running conversation loop: %w", err)
	}

	if result == nil || strings.TrimSpace(result.Content) == "" {
		r.emit(RunnerEvent{
			Type:      EventWarning,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data:      map[string]any{"message": "Model returned no content and no tool calls - ending conversation"},
		})
		return nil
	}

	r.conv.AddAssistantMessageWithReasoning(result.Content, result.Reasoning)
	assistantMsg := r.conv.Messages[len(r.conv.Messages)-1]
	if err := r.conv.SaveMessage(r.store, assistantMsg); err != nil {
		r.emitError("failed to save assistant message", err)
	}

	return nil
}

func (r *Runner) executionModelID() string {
	r.mu.RLock()
	override := r.modelOverride
	cfg := r.config
	r.mu.RUnlock()

	modelID := cfg.Models.Execution
	if modelID == "" {
		modelID = cfg.Models.Planning
	}
	if override != "" {
		modelID = override
	}
	return modelID
}

func (r *Runner) watchRunState(ctx context.Context, cancel context.CancelFunc, stop <-chan struct{}) {
	if r == nil {
		return
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			state := r.State()
			if state == StatePaused || state == StateStopped {
				cancel()
				return
			}
		}
	}
}

func (r *Runner) buildChatRequest() model.ChatRequest {
	return model.ChatRequest{
		Model:    r.executionModelID(),
		Messages: buildHeadlessMessages(r, r.executionModelID()),
		Tools:    r.tools.ToOpenAIFunctions(),
	}
}

func (r *Runner) callModel(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	startTime := time.Now()

	if r.telemetry != nil {
		r.telemetry.Publish(telemetry.Event{
			Type:      telemetry.EventBuilderStarted,
			SessionID: r.sessionID,
			Timestamp: startTime,
			Data: map[string]any{
				"model":  req.Model,
				"source": "headless",
			},
		})
	}

	resp, err := r.modelManager.ChatCompletion(ctx, req)

	if r.telemetry != nil {
		duration := time.Since(startTime)
		eventType := telemetry.EventBuilderCompleted
		data := map[string]any{
			"model":       req.Model,
			"duration_ms": duration.Milliseconds(),
			"source":      "headless",
		}
		if err != nil {
			eventType = telemetry.EventBuilderFailed
			data["error"] = err.Error()
		} else if resp != nil {
			data["input_tokens"] = resp.Usage.PromptTokens
			data["output_tokens"] = resp.Usage.CompletionTokens
		}
		r.telemetry.Publish(telemetry.Event{
			Type:      eventType,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data:      data,
		})
	}

	return resp, err
}
