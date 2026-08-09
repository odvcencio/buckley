// Package goalloop is the durable goal loop (goal-loop design section 5,
// slice G6): goal intake, decomposition into a ledger-backed task tree,
// a next-action scheduler, and a turn driver that wraps every task's
// turns with the section-20 progress controller and checkpoints through
// pkg/taskstate. The package owns no model wiring: turns execute behind
// the TurnEngine port, and decomposition behind the Planner port, so the
// loop is testable and the goal tree is reconstructable from run events
// alone.
package goalloop

import (
	"context"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// Goal is one durable goal record: the statement, its acceptance
// criteria, and the envelope the loop runs under while unattended.
type Goal struct {
	Statement          string
	AcceptanceCriteria []string
	Constraints        []string
	Deadline           time.Time
	BudgetUSD          float64
	// Posture names the budget posture (interactive | frugal |
	// overnight). G8 turns postures into policy bundles; G6 records it.
	Posture string
	// ApprovalMode is the ADR 0006 tier that applies while unattended.
	ApprovalMode string
}

// Validate rejects a goal the loop could not run or resume meaningfully.
func (g Goal) Validate() error {
	if strings.TrimSpace(g.Statement) == "" {
		return errEmptyStatement
	}
	return nil
}

// TaskSpec is one decomposed unit of work.
type TaskSpec struct {
	Title              string
	Description        string
	AcceptanceCriteria []string
	// Priority orders the queue; lower runs first.
	Priority int
	// Claims lists the workspace paths this task intends to touch. A
	// durable scheduler fans out only tasks whose claims are disjoint;
	// a task with no claims implicitly claims the whole workspace and
	// never runs in parallel with another task.
	Claims []string
}

// Planner decomposes a goal into ordered task specs. The orchestrator
// planner (ADR 0004) adapts to this port at the planning model tier;
// a nil planner yields one task carrying the whole goal.
type Planner interface {
	Decompose(ctx context.Context, goal Goal) ([]TaskSpec, error)
}

// Turn phases. The loop sets Phase on the TaskContext so the engine can
// shape the turn: execute works the task; verify runs cheap checks and
// attaches evidence instead of exploring further (design 5.4).
const (
	PhaseExecute = "execute"
	PhaseVerify  = "verify"
)

// TaskContext is everything one turn needs to know about its task.
type TaskContext struct {
	RunID  string
	TaskID string
	// TurnID is stable for one drive turn and is derived from the latest
	// checkpoint generation. Retrying an interrupted turn reuses it, while a
	// checkpoint advance creates a new generation.
	TurnID string
	Goal   Goal
	Spec   TaskSpec
	// Phase is PhaseExecute or PhaseVerify; the controller's verify
	// routing flips it (design section 5.2 step 3).
	Phase string
	// Resume carries the compiled checkpoint context when the task has
	// one; nil on a fresh task.
	Resume *taskstate.ResumeContext
}

// TurnOutcome reports what one turn accomplished, in conclusions the
// loop can act on. SpentUSD and the counters feed the fuse counters;
// the rest feeds checkpoints and the progress controller.
type TurnOutcome struct {
	Rounds           int
	ToolCalls        int
	SpentUSD         float64
	PromptTokens     int
	CompletionTokens int
	StateChanged     bool
	Summary          string
	NextActions      []taskstate.NextAction
	Completed        bool
	// CompletedEvidenceID is required when Completed is set: the task's
	// completion claim must be evidence-linked (spec decision 9).
	CompletedEvidenceID string
	Blocker             *taskstate.Blocker
	// Checks reports verification entries the turn ran or discovered,
	// merged into the task's checkpoint by check name. A pass entry
	// must carry the evidence object ID of the check's output (machine
	// checks attach command output as evidence, design 5.4).
	Checks []taskstate.VerificationEntry
	// Questions are deferred user questions (design 5.5): the loop
	// never blocks on them — they land on the checkpoint for the
	// morning report, and only tasks a question names as blocking park.
	Questions []taskstate.Question
}

// TurnEngine executes one model turn for a task. Every migrated loop
// already sits on agentloop.Controller; this port lets the goal loop
// drive any of them without owning model wiring.
type TurnEngine interface {
	RunTurn(ctx context.Context, task TaskContext) (TurnOutcome, error)
}

// Config wires one Loop.
type Config struct {
	// Ledger is required: the goal tree and every decision live there.
	Ledger runledger.Store
	// Checkpoints is required: every state transition checkpoints.
	Checkpoints *taskstate.Manager
	// Engine is required by RunTask; Start works without it.
	Engine TurnEngine
	// Planner is optional; nil decomposes to a single task.
	Planner Planner
	// Progress is optional. When set, every drive uses it verbatim;
	// when nil, each goal's posture expands into its policy bundle
	// (see progressFor: interactive enforces user budgets without
	// fuses, frugal and overnight arm fuses with earlier parking).
	Progress *agentloop.ProgressController
	// SessionID labels the goal's runs.
	SessionID string
}

// Loop drives goals. Construct with New.
type Loop struct {
	ledger      runledger.Store
	checkpoints *taskstate.Manager
	engine      TurnEngine
	planner     Planner
	progress    *agentloop.ProgressController
	sessionID   string
}

// New wires a Loop.
func New(cfg Config) (*Loop, error) {
	if cfg.Ledger == nil {
		return nil, errNoLedger
	}
	if cfg.Checkpoints == nil {
		return nil, errNoCheckpoints
	}
	return &Loop{
		ledger:      cfg.Ledger,
		checkpoints: cfg.Checkpoints,
		engine:      cfg.Engine,
		planner:     cfg.Planner,
		progress:    cfg.Progress,
		sessionID:   cfg.SessionID,
	}, nil
}
