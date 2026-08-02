// Package taskstate owns the task checkpoint schema (Context Fabric
// section 15): the typed CheckpointState JSON that is canonical, the
// Markdown++ rendering that is the human view, the event-driven triggers
// that decide when to checkpoint, and the resume compiler that turns the
// latest checkpoint into a useful next action without transcript replay.
//
// The run ledger stores checkpoints (runledger.TaskCheckpoint) but treats
// StateJSON as opaque; this package is the schema owner. Neither the state
// nor the rendered view ever carries private chain-of-thought.
package taskstate

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SchemaVersion identifies the canonical checkpoint schema this package
// reads and writes (section 15.1).
const SchemaVersion = "m31.task-checkpoint.v1"

// Task status values a checkpoint can carry.
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusBlocked    = "blocked"
	StatusParked     = "parked"
	StatusCompleted  = "completed"
)

// Verification status values (section 15.2).
const (
	VerificationPending      = "pending"
	VerificationPass         = "pass"
	VerificationFail         = "fail"
	VerificationInconclusive = "inconclusive"
)

// CheckpointState is the canonical, typed checkpoint payload. JSON is the
// storage form (runledger.TaskCheckpoint.StateJSON); RenderMarkdown is the
// human view. Summary and every other field are agent-facing conclusions,
// never private chain-of-thought.
type CheckpointState struct {
	Schema      string              `json:"schema"`
	TaskID      string              `json:"task_id"`
	GoalID      string              `json:"goal_id,omitempty"`
	Status      string              `json:"status"`
	Summary     string              `json:"summary,omitempty"`
	Completed   []CompletedItem     `json:"completed,omitempty"`
	Checks      []VerificationEntry `json:"verification,omitempty"`
	NextActions []NextAction        `json:"next_actions,omitempty"`
	Blocker     *Blocker            `json:"blocker,omitempty"`
	Questions   []Question          `json:"questions,omitempty"`
	Spend       Spend               `json:"spend"`
	Files       []string            `json:"files,omitempty"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

// CompletedItem is one evidence-linked completed claim. Claims require
// evidence (spec decision 9); EvidenceID may be empty only for items still
// awaiting verification, which then must appear in Checks as debt.
type CompletedItem struct {
	Text       string `json:"text"`
	EvidenceID string `json:"evidence_id,omitempty"`
}

// VerificationEntry is one verification check (section 15.2). Required
// entries gate task completion.
type VerificationEntry struct {
	Check      string `json:"check"`
	Scope      string `json:"scope,omitempty"`
	Status     string `json:"status"`
	Required   bool   `json:"required,omitempty"`
	EvidenceID string `json:"evidence_id,omitempty"`
}

// NextAction is one ordered entry of the next-action queue. Kind lets the
// scheduler prefer cheap verification before expensive exploration.
type NextAction struct {
	Text    string `json:"text"`
	Kind    string `json:"kind,omitempty"` // verify | edit | explore | report
	TaskRef string `json:"task_ref,omitempty"`
}

// Blocker records why a task left the queue and what un-parks it
// (section 5.5 of the goal-loop design).
type Blocker struct {
	Reason     string     `json:"reason"`
	Needs      string     `json:"needs,omitempty"`
	RetryAfter *time.Time `json:"retry_after,omitempty"`
}

// Question is a deferred user question. In overnight posture the loop
// never blocks on ask_user; it records the question and parks only the
// dependent tasks.
type Question struct {
	Text          string   `json:"text"`
	Context       string   `json:"context,omitempty"`
	BlockingTasks []string `json:"blocking_tasks,omitempty"`
}

// Spend is the checkpoint's cost roll-up.
type Spend struct {
	USD              float64 `json:"usd,omitempty"`
	BudgetUSD        float64 `json:"budget_usd,omitempty"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
}

var validStatuses = map[string]bool{
	StatusPending:    true,
	StatusInProgress: true,
	StatusBlocked:    true,
	StatusParked:     true,
	StatusCompleted:  true,
}

var validVerificationStatuses = map[string]bool{
	VerificationPending:      true,
	VerificationPass:         true,
	VerificationFail:         true,
	VerificationInconclusive: true,
}

// Validate enforces the schema's type-level rules (sections 15.1, 15.2):
//
//   - schema and task_id are required; status must be a known value;
//   - a verification "pass" without an evidence ID is invalid — the store
//     must be unable to record an unevidenced pass;
//   - a completed task may not carry a required verification entry that
//     is not an evidenced pass, and may not carry a blocker.
func (s CheckpointState) Validate() error {
	if s.Schema != SchemaVersion {
		return fmt.Errorf("taskstate: schema %q, want %q", s.Schema, SchemaVersion)
	}
	if strings.TrimSpace(s.TaskID) == "" {
		return fmt.Errorf("taskstate: task_id is required")
	}
	if !validStatuses[s.Status] {
		return fmt.Errorf("taskstate: unknown status %q", s.Status)
	}
	for i, v := range s.Checks {
		if strings.TrimSpace(v.Check) == "" {
			return fmt.Errorf("taskstate: verification %d: check name is required", i)
		}
		if !validVerificationStatuses[v.Status] {
			return fmt.Errorf("taskstate: verification %q: unknown status %q", v.Check, v.Status)
		}
		if v.Status == VerificationPass && strings.TrimSpace(v.EvidenceID) == "" {
			return fmt.Errorf("taskstate: verification %q: pass requires an evidence id", v.Check)
		}
	}
	if s.Status == StatusCompleted {
		for _, v := range s.Checks {
			if v.Required && v.Status != VerificationPass {
				return fmt.Errorf("taskstate: task cannot complete: required verification %q is %s", v.Check, v.Status)
			}
		}
		if s.Blocker != nil {
			return fmt.Errorf("taskstate: task cannot complete with an active blocker")
		}
	}
	if s.Status == StatusBlocked && s.Blocker == nil {
		return fmt.Errorf("taskstate: blocked status requires a blocker record")
	}
	return nil
}

// VerificationDebt is the count of verification entries that are not an
// evidenced pass. The controller's risk-weighted debt scalar builds on
// this; the raw count is the schema-level building block.
func (s CheckpointState) VerificationDebt() int {
	debt := 0
	for _, v := range s.Checks {
		if v.Status != VerificationPass {
			debt++
		}
	}
	return debt
}

// Marshal serializes the state as canonical JSON after validating it.
func (s CheckpointState) Marshal() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("taskstate: marshal: %w", err)
	}
	return string(raw), nil
}

// Unmarshal parses and validates a canonical JSON checkpoint state.
func Unmarshal(raw string) (CheckpointState, error) {
	var s CheckpointState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return CheckpointState{}, fmt.Errorf("taskstate: unmarshal: %w", err)
	}
	if err := s.Validate(); err != nil {
		return CheckpointState{}, err
	}
	return s, nil
}
