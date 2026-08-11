package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/execprogram"
)

// codeModeRuntime owns the durable stores and one audited run per session for
// an explicit --code-mode launch.
type codeModeRuntime struct {
	mu            sync.Mutex
	workspaceRoot string
	stores        *goalStores
	cleanup       func()
	tools         map[string]tool.Tool
	runs          map[string]string
	closed        bool
}

func openCodeModeRuntime(workspaceRoot string) (*codeModeRuntime, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return nil, fmt.Errorf("code mode requires a workspace root")
	}
	if execmode.DetectIsolation() != execmode.IsolationBwrap {
		return nil, fmt.Errorf("code mode requires OS isolation (install bubblewrap); exec_program never runs unsandboxed")
	}
	stores, cleanup, err := openGoalStores()
	if err != nil {
		return nil, err
	}
	return &codeModeRuntime{
		workspaceRoot: workspaceRoot,
		stores:        stores,
		cleanup:       cleanup,
		tools:         make(map[string]tool.Tool),
		runs:          make(map[string]string),
	}, nil
}

func (r *codeModeRuntime) Tool(sessionID string) (tool.Tool, error) {
	if r == nil {
		return nil, fmt.Errorf("code mode runtime unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("code mode requires a session id")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, fmt.Errorf("code mode runtime is closed")
	}
	if existing := r.tools[sessionID]; existing != nil {
		return existing, nil
	}

	run, err := r.stores.ledger.StartRun(context.Background(), runledger.AgentRun{
		SessionID: sessionID,
		AgentID:   "buckley",
		Backend:   "code-mode",
	})
	if err != nil {
		return nil, fmt.Errorf("start code-mode run: %w", err)
	}
	programTool, err := execprogram.NewProgramTool(
		r.workspaceRoot,
		r.stores.ledger,
		r.stores.evidence,
		run.RunID,
		sessionID,
		execmode.ReadOnlySet,
	)
	if err != nil {
		_ = r.stores.ledger.EndRun(context.Background(), run.RunID, "failed", time.Now().UTC(), map[string]any{"error": err.Error()})
		return nil, err
	}
	r.tools[sessionID] = programTool
	r.runs[sessionID] = run.RunID
	return programTool, nil
}

func (r *codeModeRuntime) RunLedger() runledger.Store {
	if r == nil || r.stores == nil {
		return nil
	}
	return r.stores.ledger
}

// EvidenceStore exposes the code-mode runtime's shared evidence adapter for
// sibling durable surfaces such as AgentCoordinator. Both stores use the
// same SQLite file, so subagent evidence can be replayed beside code-mode
// operation evidence.
func (r *codeModeRuntime) EvidenceStore() evidence.Store {
	if r == nil || r.stores == nil {
		return nil
	}
	return r.stores.evidence
}

func (r *codeModeRuntime) Close() error {
	return r.closeRuns("completed", map[string]any{"surface": "exec_program"})
}

func (r *codeModeRuntime) Fail(cause error) error {
	outcome := map[string]any{"surface": "exec_program"}
	if cause != nil {
		outcome["error"] = cause.Error()
	}
	return r.closeRuns("failed", outcome)
}

func (r *codeModeRuntime) closeRuns(status string, outcome map[string]any) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	runs := make(map[string]string, len(r.runs))
	for sessionID, runID := range r.runs {
		runs[sessionID] = runID
	}
	cleanup := r.cleanup
	r.mu.Unlock()

	var closeErrs []error
	for sessionID, runID := range runs {
		runOutcome := make(map[string]any, len(outcome)+1)
		for key, value := range outcome {
			runOutcome[key] = value
		}
		runOutcome["session_id"] = sessionID
		if err := r.stores.ledger.EndRun(context.Background(), runID, status, time.Now().UTC(), runOutcome); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("end code-mode run %s: %w", runID, err))
		}
	}
	if cleanup != nil {
		cleanup()
	}
	return errors.Join(closeErrs...)
}
