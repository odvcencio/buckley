package taskstate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
)

// CheckpointLedger is the slice of runledger.Store this package needs.
type CheckpointLedger interface {
	CreateTaskCheckpoint(ctx context.Context, checkpoint runledger.TaskCheckpoint) (runledger.TaskCheckpoint, error)
	LatestTaskCheckpoint(ctx context.Context, taskID string) (runledger.TaskCheckpoint, error)
}

// EvidenceWriter is the slice of evidence.Store this package needs: the
// rendered Markdown view persists as a checkpoint-kind evidence object,
// which the ledger row then references by ID.
type EvidenceWriter interface {
	Put(ctx context.Context, object evidence.Object) (evidence.Object, error)
	Get(ctx context.Context, id string) (evidence.Object, error)
}

// Manager persists checkpoints and compiles resume context. It composes
// the two durable stores; it owns no storage of its own.
type Manager struct {
	ledger   CheckpointLedger
	evidence EvidenceWriter
}

// NewManager wires a Manager. Both stores are required.
func NewManager(ledger CheckpointLedger, ev EvidenceWriter) (*Manager, error) {
	if ledger == nil {
		return nil, fmt.Errorf("taskstate: ledger is required")
	}
	if ev == nil {
		return nil, fmt.Errorf("taskstate: evidence store is required")
	}
	return &Manager{ledger: ledger, evidence: ev}, nil
}

// SaveInput carries the identifiers a checkpoint row needs beyond the
// state itself.
type SaveInput struct {
	State      CheckpointState
	Reason     TriggerReason
	SessionID  string
	RunID      string
	SnapshotID string
}

// Save validates the state, renders and stores the Markdown view as a
// checkpoint evidence object, and records the ledger row. The returned
// checkpoint carries the assigned version and checkpoint ID.
func (m *Manager) Save(ctx context.Context, in SaveInput) (runledger.TaskCheckpoint, error) {
	if in.State.UpdatedAt.IsZero() {
		in.State.UpdatedAt = time.Now().UTC()
	}
	stateJSON, err := in.State.Marshal()
	if err != nil {
		return runledger.TaskCheckpoint{}, err
	}
	reason := string(in.Reason)
	if strings.TrimSpace(reason) == "" {
		return runledger.TaskCheckpoint{}, fmt.Errorf("taskstate: a checkpoint reason is required")
	}

	rendered := RenderMarkdown(in.State)
	obj, err := m.evidence.Put(ctx, evidence.Object{
		Kind:       evidence.KindCheckpoint,
		MediaType:  "text/markdown",
		InlineBody: []byte(rendered),
		Metadata: map[string]any{
			evidence.MetaTaskID:    in.State.TaskID,
			evidence.MetaSessionID: in.SessionID,
			evidence.MetaRunID:     in.RunID,
			"schema":               SchemaVersion,
			"reason":               reason,
		},
	})
	if err != nil {
		return runledger.TaskCheckpoint{}, fmt.Errorf("taskstate: store checkpoint evidence: %w", err)
	}

	parentID := ""
	if latest, err := m.ledger.LatestTaskCheckpoint(ctx, in.State.TaskID); err == nil {
		parentID = latest.CheckpointID
	}

	saved, err := m.ledger.CreateTaskCheckpoint(ctx, runledger.TaskCheckpoint{
		ParentCheckpointID: parentID,
		TaskID:             in.State.TaskID,
		SessionID:          in.SessionID,
		RunID:              in.RunID,
		Status:             in.State.Status,
		SnapshotID:         in.SnapshotID,
		Reason:             reason,
		StateJSON:          stateJSON,
		MarkdownEvidenceID: obj.ID,
	})
	if err != nil {
		return runledger.TaskCheckpoint{}, fmt.Errorf("taskstate: record checkpoint: %w", err)
	}
	return saved, nil
}

// ErrNoCheckpoint is returned by Resume when a task has no checkpoint yet.
var ErrNoCheckpoint = errors.New("taskstate: no checkpoint for task")

// ResumeContext is the compiled restart package (section 15.5): everything
// the loop needs to reach a useful next action without transcript replay.
type ResumeContext struct {
	Checkpoint runledger.TaskCheckpoint
	State      CheckpointState
	// Prompt is the model-facing resume block: the checkpoint summary,
	// verification debt, blocker, and the ordered next actions. It is
	// built from conclusions only; no chain-of-thought exists to leak.
	Prompt string
}

// Resume loads the latest checkpoint for taskID and compiles the resume
// context. The state must parse and validate — a corrupt checkpoint is an
// error, not a silent fresh start; callers decide whether to fall back to
// an older version or replan.
func (m *Manager) Resume(ctx context.Context, taskID string) (ResumeContext, error) {
	cp, err := m.ledger.LatestTaskCheckpoint(ctx, taskID)
	if err != nil {
		return ResumeContext{}, fmt.Errorf("%w: %s", ErrNoCheckpoint, taskID)
	}
	state, err := Unmarshal(cp.StateJSON)
	if err != nil {
		return ResumeContext{}, fmt.Errorf("taskstate: checkpoint %s: %w", cp.CheckpointID, err)
	}
	return ResumeContext{
		Checkpoint: cp,
		State:      state,
		Prompt:     resumePrompt(state, cp),
	}, nil
}

// resumePrompt renders the compact model-facing resume block. Target per
// the design: a resumed task reaches a useful next action in at most two
// model requests, so the block leads with status and the action queue.
func resumePrompt(s CheckpointState, cp runledger.TaskCheckpoint) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Resuming task %s from checkpoint %s (version %d, reason: %s).\n", s.TaskID, cp.CheckpointID, cp.Version, cp.Reason)
	fmt.Fprintf(&b, "Status: %s.", s.Status)
	if debt := s.VerificationDebt(); debt > 0 {
		fmt.Fprintf(&b, " Verification debt: %d unresolved check(s).", debt)
	}
	b.WriteString("\n")
	if strings.TrimSpace(s.Summary) != "" {
		b.WriteString("\n" + strings.TrimSpace(s.Summary) + "\n")
	}
	if s.Blocker != nil {
		fmt.Fprintf(&b, "\nBlocked: %s", s.Blocker.Reason)
		if s.Blocker.Needs != "" {
			fmt.Fprintf(&b, " (needs: %s)", s.Blocker.Needs)
		}
		b.WriteString("\n")
	}
	if len(s.NextActions) > 0 {
		b.WriteString("\nNext actions, in order:\n")
		for i, a := range s.NextActions {
			fmt.Fprintf(&b, "%d. %s\n", i+1, a.Text)
		}
	}
	if len(s.Questions) > 0 {
		b.WriteString("\nOpen questions (deferred, do not block):\n")
		for i, q := range s.Questions {
			fmt.Fprintf(&b, "%d. %s\n", i+1, q.Text)
		}
	}
	return b.String()
}
