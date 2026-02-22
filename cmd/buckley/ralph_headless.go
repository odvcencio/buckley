package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/headless"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/ralph"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
)

// ralphHeadlessRunner implements ralph.HeadlessRunner wrapping headless.Runner.
type ralphHeadlessRunner struct {
	runner    *headless.Runner
	store     *storage.Store
	sessionID string
}

type modelContextProvider struct {
	manager *model.Manager
}

func (p modelContextProvider) ContextLength(modelID string) int {
	if p.manager == nil {
		return 0
	}
	length, err := p.manager.GetContextLength(modelID)
	if err != nil {
		return 0
	}
	return length
}

func (r *ralphHeadlessRunner) ProcessInput(ctx context.Context, input string) error {
	if r == nil || r.runner == nil {
		return fmt.Errorf("runner not initialized")
	}
	return r.runner.HandleSessionCommand(command.SessionCommand{
		Type:    "input",
		Content: input,
	})
}

func (r *ralphHeadlessRunner) State() string {
	if r == nil || r.runner == nil {
		return "idle"
	}
	return string(r.runner.State())
}

func (r *ralphHeadlessRunner) SetModelOverride(modelID string) {
	if r == nil || r.runner == nil {
		return
	}
	r.runner.SetModelOverride(modelID)
}

func (r *ralphHeadlessRunner) WaitForIdle(ctx context.Context) error {
	if r == nil || r.runner == nil {
		return fmt.Errorf("runner not initialized")
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	// Wait for the runner to transition out of idle (start processing),
	// then wait for it to return to idle (finish processing).
	sawNonIdle := false
	maxWait := time.After(5 * time.Minute) // Safety timeout

	for {
		state := r.runner.State()
		switch state {
		case headless.StateIdle:
			if sawNonIdle {
				// Runner processed something and is now idle
				return nil
			}
			// Still waiting for processing to start
		case headless.StateProcessing:
			sawNonIdle = true
		case headless.StatePaused:
			return fmt.Errorf("runner paused")
		case headless.StateError:
			return fmt.Errorf("runner entered error state")
		case headless.StateStopped:
			return fmt.Errorf("runner stopped")
		default:
			sawNonIdle = true // Any other state counts as processing
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-maxWait:
			if sawNonIdle {
				return fmt.Errorf("waitForIdle: timed out after %v while processing", 5*time.Minute)
			}
			return fmt.Errorf("waitForIdle: timed out after %v waiting for processing to start", 5*time.Minute)
		case <-ticker.C:
		}
	}
}

func (r *ralphHeadlessRunner) LatestAssistantMessageID(ctx context.Context) (int64, error) {
	_ = ctx
	if r == nil || r.store == nil {
		return 0, fmt.Errorf("store not initialized")
	}
	msg, err := r.store.GetLatestMessageByRole(r.sessionID, "assistant")
	if err != nil {
		return 0, err
	}
	if msg == nil {
		return 0, nil
	}
	return msg.ID, nil
}

func (r *ralphHeadlessRunner) LatestAssistantMessage(ctx context.Context, afterID int64) (string, int, int64, error) {
	_ = ctx
	if r == nil || r.store == nil {
		return "", 0, 0, fmt.Errorf("store not initialized")
	}
	msg, err := r.store.GetLatestMessageByRole(r.sessionID, "assistant")
	if err != nil {
		return "", 0, 0, err
	}
	if msg == nil || msg.ID <= afterID {
		return "", 0, 0, nil
	}
	content := msg.Content
	if strings.TrimSpace(content) == "" && strings.TrimSpace(msg.ContentJSON) != "" {
		content = msg.ContentJSON
	}
	return content, msg.Tokens, msg.ID, nil
}

func (r *ralphHeadlessRunner) Stop() {
	if r != nil && r.runner != nil {
		r.runner.Stop()
	}
}

// ralphEventEmitter bridges headless.RunnerEvent to ralph.Logger.
type ralphEventEmitter struct {
	logger *ralph.Logger
}

func (e *ralphEventEmitter) Emit(event headless.RunnerEvent) {
	if e == nil || e.logger == nil {
		return
	}

	switch event.Type {
	case headless.EventToolCallStarted:
		toolName, _ := event.Data["toolName"].(string)
		argsRaw, _ := event.Data["arguments"].(string)
		var args map[string]any
		if err := json.Unmarshal([]byte(argsRaw), &args); err != nil {
			args = map[string]any{"raw": argsRaw}
		}
		e.logger.LogToolCall(toolName, args)

	case headless.EventToolCallComplete:
		toolName, _ := event.Data["toolName"].(string)
		success, _ := event.Data["success"].(bool)
		output, _ := event.Data["output"].(string)
		if errMsg, ok := event.Data["error"].(string); ok && errMsg != "" {
			output = errMsg
		}
		e.logger.LogToolResult(toolName, success, output)
	}
}

// newRalphHeadlessRunner creates a headless runner configured for Ralph mode.
func newRalphHeadlessRunner(
	cfg *config.Config,
	mgr *model.Manager,
	store *storage.Store,
	registry *tool.Registry,
	logger *ralph.Logger,
	sessionID string,
	sandboxPath string,
	timeout time.Duration,
) (*ralphHeadlessRunner, error) {
	// Create storage session for the headless runner
	now := time.Now()
	session := &storage.Session{
		ID:          sessionID,
		ProjectPath: sandboxPath,
		CreatedAt:   now,
		LastActive:  now,
		Status:      storage.SessionStatusActive,
	}

	if err := store.CreateSession(session); err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}

	// Create event emitter that logs to Ralph's logger
	emitter := &ralphEventEmitter{logger: logger}

	// Configure the runner
	runnerCfg := headless.RunnerConfig{
		Session:      session,
		ModelManager: mgr,
		Tools:        registry,
		Store:        store,
		Config:       cfg,
		Emitter:      emitter,
		MaxRuntime:   timeout,
	}

	runner, err := headless.NewRunner(runnerCfg)
	if err != nil {
		return nil, fmt.Errorf("creating headless runner: %w", err)
	}

	return &ralphHeadlessRunner{runner: runner, store: store, sessionID: sessionID}, nil
}
