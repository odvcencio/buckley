package ralph

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/taskstate"
)

// BackendTurnEngine adapts a Ralph execution backend to
// goalloop.TurnEngine (goal-loop design section 8): the goal loop
// delegates a whole task to an external CLI (claude, codex) as one turn
// per drive. Ralph stops being its own loop here — the goal loop owns
// scheduling, checkpoints, verification gates, and budget; the backend
// only executes.
//
// Outcome mapping keeps the G7 gate honest: the backend's raw output is
// stored as an evidence object; test counts become a required
// verification check referencing that evidence; a clean execution with
// no failing tests claims completion; an execution error parks the task
// with a retry timer so a rate-limited backend re-queues itself instead
// of failing the goal.
type BackendTurnEngine struct {
	backend  Backend
	evidence evidence.Store
	sandbox  string

	mu         sync.Mutex
	iterations map[string]int
}

// backendRetryDelay is how long an execution error parks a task before
// the queue retries it — Ralph's rate-limit parking, re-expressed as the
// goal loop's retry-after unparking.
const backendRetryDelay = 15 * time.Minute

// NewBackendTurnEngine wires a backend as a goal task executor.
func NewBackendTurnEngine(backend Backend, ev evidence.Store, sandbox string) (*BackendTurnEngine, error) {
	if backend == nil {
		return nil, fmt.Errorf("ralph: backend is required")
	}
	if ev == nil {
		return nil, fmt.Errorf("ralph: evidence store is required")
	}
	return &BackendTurnEngine{
		backend:    backend,
		evidence:   ev,
		sandbox:    sandbox,
		iterations: map[string]int{},
	}, nil
}

// RunTurn implements goalloop.TurnEngine.
func (e *BackendTurnEngine) RunTurn(ctx context.Context, task goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	if !e.backend.Available() {
		retryAt := time.Now().Add(backendRetryDelay)
		return goalloop.TurnOutcome{
			Summary: fmt.Sprintf("backend %s unavailable", e.backend.Name()),
			Blocker: &taskstate.Blocker{
				Reason:     fmt.Sprintf("backend %s is unavailable (rate limit or quota)", e.backend.Name()),
				RetryAfter: &retryAt,
			},
		}, nil
	}

	e.mu.Lock()
	e.iterations[task.TaskID]++
	iteration := e.iterations[task.TaskID]
	e.mu.Unlock()

	result, err := e.backend.Execute(ctx, BackendRequest{
		Prompt:      backendTaskPrompt(task),
		SandboxPath: e.sandbox,
		Iteration:   iteration,
		SessionID:   task.RunID,
	})
	if err != nil || result == nil || result.Error != nil {
		reason := "backend execution failed"
		if err != nil {
			reason = err.Error()
		} else if result != nil && result.Error != nil {
			reason = result.Error.Error()
		}
		if ctx.Err() != nil {
			return goalloop.TurnOutcome{}, ctx.Err()
		}
		retryAt := time.Now().Add(backendRetryDelay)
		return goalloop.TurnOutcome{
			Summary: "backend execution failed",
			Blocker: &taskstate.Blocker{
				Reason:     fmt.Sprintf("backend %s: %s", e.backend.Name(), reason),
				RetryAfter: &retryAt,
			},
		}, nil
	}

	evidenceID, err := e.storeExecutionEvidence(ctx, task, iteration, result)
	if err != nil {
		return goalloop.TurnOutcome{}, err
	}

	outcome := goalloop.TurnOutcome{
		Rounds:           1,
		PromptTokens:     result.TokensIn,
		CompletionTokens: result.TokensOut,
		SpentUSD:         result.Cost,
		StateChanged:     len(result.FilesChanged) > 0,
		Summary:          backendSummary(task, result),
	}
	if outcome.SpentUSD == 0 {
		outcome.SpentUSD = result.CostEstimate
	}

	if result.TestsPassed+result.TestsFailed > 0 {
		status := taskstate.VerificationPass
		if result.TestsFailed > 0 {
			status = taskstate.VerificationFail
		}
		outcome.Checks = []taskstate.VerificationEntry{{
			Check:      "backend test run",
			Scope:      fmt.Sprintf("%d passed, %d failed", result.TestsPassed, result.TestsFailed),
			Status:     status,
			Required:   true,
			EvidenceID: evidenceID,
		}}
	}

	// A clean execution claims completion with the stored output as
	// evidence; the G7 gate still rejects it if a required check failed.
	if result.TestsFailed == 0 {
		outcome.Completed = true
		outcome.CompletedEvidenceID = evidenceID
	}
	return outcome, nil
}

func (e *BackendTurnEngine) storeExecutionEvidence(ctx context.Context, task goalloop.TaskContext, iteration int, result *BackendResult) (string, error) {
	output := strings.TrimSpace(result.Output)
	if output == "" {
		output = "(backend produced no output)"
	}
	body := fmt.Sprintf("# Backend execution: %s (iteration %d)\n\nBackend: %s\nFiles changed: %d\nTests: %d passed, %d failed\n\n%s\n",
		task.Spec.Title, iteration, e.backend.Name(), len(result.FilesChanged), result.TestsPassed, result.TestsFailed, output)
	obj, err := e.evidence.Put(ctx, evidence.Object{
		Kind:       evidence.KindSubagentReport,
		MediaType:  "text/markdown",
		InlineBody: []byte(body),
		Metadata: map[string]any{
			evidence.MetaRunID:  task.RunID,
			evidence.MetaTaskID: task.TaskID,
			"backend":           e.backend.Name(),
			"iteration":         iteration,
		},
	})
	if err != nil {
		return "", fmt.Errorf("ralph: store execution evidence: %w", err)
	}
	return obj.ID, nil
}

// backendTaskPrompt renders the whole-task prompt an external CLI
// receives: the goal, the task, its criteria, and the checkpointed
// resume context when one exists.
func backendTaskPrompt(task goalloop.TaskContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n\nTask: %s\n", task.Goal.Statement, task.Spec.Title)
	if task.Spec.Description != "" {
		b.WriteString(task.Spec.Description + "\n")
	}
	if len(task.Spec.AcceptanceCriteria) > 0 {
		b.WriteString("\nAcceptance criteria:\n")
		for _, criterion := range task.Spec.AcceptanceCriteria {
			b.WriteString("- " + criterion + "\n")
		}
	}
	if task.Phase == goalloop.PhaseVerify {
		b.WriteString("\nThis attempt must VERIFY the prior work: run the relevant build and tests and report their results. Do not make further edits unless a fix is required to pass.\n")
	}
	if task.Resume != nil && strings.TrimSpace(task.Resume.Prompt) != "" {
		b.WriteString("\n" + task.Resume.Prompt)
	}
	return b.String()
}

func backendSummary(task goalloop.TaskContext, result *BackendResult) string {
	summary := fmt.Sprintf("%s via %s: %d file(s) changed", task.Spec.Title, result.Backend, len(result.FilesChanged))
	if result.TestsPassed+result.TestsFailed > 0 {
		summary += fmt.Sprintf(", tests %d/%d passing", result.TestsPassed, result.TestsPassed+result.TestsFailed)
	}
	return summary
}
