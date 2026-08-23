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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	// ModelRequest is the compact, immutable model contract captured at
	// intake. Keeping it on the goal makes local resumes and standalone
	// durable workers issue the same request instead of inheriting whatever
	// model/privacy defaults happen to be present after a restart.
	ModelRequest GoalModelRequest
	// WorkspaceRoot is the canonical directory this goal is allowed to
	// execute against. Durable workers compare it with their configured
	// work directory before accepting any activity for the goal.
	WorkspaceRoot string
	// Posture names the budget posture (interactive | frugal |
	// overnight). G8 turns postures into policy bundles; G6 records it.
	Posture string
	// ApprovalMode is the ADR 0006 tier that applies while unattended.
	ApprovalMode string
}

// GoalModelRequest is the transport-neutral portion of a goal's model
// request. It intentionally contains only bounded routing controls; prompts,
// tool results, provider responses, and credentials never enter goal history.
type GoalModelRequest struct {
	PolicyVersion    string
	Policy           string
	PolicyAction     string
	PolicyReasonCode string
	Model            string
	ReasoningEffort  string
	RetentionMode    string
	// OpenRouterZDR is retained as a compact compatibility projection for
	// histories written before RetentionMode became explicit. New intake writes
	// both fields consistently; loading a legacy true value promotes it to zdr.
	OpenRouterZDR            bool
	OpenRouterDataCollection string
	WorkspaceLicense         WorkspaceLicenseEvidence
}

const (
	MaxGoalModelIDBytes = 256

	GoalModelPolicyVersionV1 = "buckley.model-data-policy.v1"
	GoalRetentionLegacy      = "legacy"
	GoalRetentionZDR         = "zdr"
	GoalRetentionNonZDR      = "non_zdr"
)

// NormalizeWorkspaceRoot resolves a workspace directory to one stable local
// identity. Symlinks are resolved so a worker cannot appear to match a goal
// while addressing a different directory through an alias.
func NormalizeWorkspaceRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("goalloop: workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("goalloop: resolve workspace root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("goalloop: resolve workspace root %s: %w", abs, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("goalloop: inspect workspace root %s: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("goalloop: workspace root is not a directory: %s", resolved)
	}
	return filepath.Clean(resolved), nil
}

// Validate rejects a goal the loop could not run or resume meaningfully.
func (g Goal) Validate() error {
	if strings.TrimSpace(g.Statement) == "" {
		return errEmptyStatement
	}
	if err := g.ModelRequest.Validate(); err != nil {
		return err
	}
	if g.ModelRequest.PolicyVersion == GoalModelPolicyVersionV1 {
		workspaceRoot := strings.TrimSpace(g.WorkspaceRoot)
		if workspaceRoot == "" || workspaceRoot != g.WorkspaceRoot {
			return fmt.Errorf("goalloop: v1 goal model policy requires a canonical workspace root")
		}
		canonical, err := NormalizeWorkspaceRoot(workspaceRoot)
		if err != nil || canonical != workspaceRoot {
			return fmt.Errorf("goalloop: v1 goal model policy requires a canonical workspace root")
		}
	}
	return nil
}

// Validate rejects malformed or weakening model contracts before they are
// written to the durable ledger.
func (r GoalModelRequest) Validate() error {
	switch r.PolicyVersion {
	case "", GoalModelPolicyVersionV1:
	default:
		return fmt.Errorf("goalloop: unsupported goal model policy version")
	}
	retention := r.EffectiveRetentionMode()
	switch retention {
	case GoalRetentionLegacy, GoalRetentionZDR, GoalRetentionNonZDR:
	default:
		return fmt.Errorf("goalloop: unsupported goal retention mode %q", r.RetentionMode)
	}
	if r.RetentionMode != "" && r.RetentionMode != retention {
		return fmt.Errorf("goalloop: goal retention mode must be canonical lowercase")
	}
	if r.OpenRouterZDR && retention != GoalRetentionZDR {
		return fmt.Errorf("goalloop: OpenRouter ZDR projection conflicts with retention mode")
	}

	modelID := strings.TrimSpace(r.Model)
	if modelID != r.Model {
		return fmt.Errorf("goalloop: goal model must not have surrounding whitespace")
	}
	if len(modelID) > MaxGoalModelIDBytes || !utf8.ValidString(modelID) || containsGoalControl(modelID) {
		return fmt.Errorf("goalloop: goal model is invalid")
	}

	effort := strings.ToLower(strings.TrimSpace(r.ReasoningEffort))
	if effort != r.ReasoningEffort {
		return fmt.Errorf("goalloop: goal reasoning effort must be canonical lowercase")
	}
	switch effort {
	case "", "auto", "off", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("goalloop: unsupported goal reasoning effort %q", r.ReasoningEffort)
	}

	collection := strings.ToLower(strings.TrimSpace(r.OpenRouterDataCollection))
	if collection != r.OpenRouterDataCollection {
		return fmt.Errorf("goalloop: OpenRouter data collection policy must be canonical lowercase")
	}
	if collection != "" && collection != "deny" {
		return fmt.Errorf("goalloop: unsupported OpenRouter data collection policy %q", r.OpenRouterDataCollection)
	}
	if (retention == GoalRetentionZDR || retention == GoalRetentionNonZDR || collection != "") && modelID == "" {
		return fmt.Errorf("goalloop: an explicit OpenRouter privacy policy requires an exact goal model")
	}
	if retention == GoalRetentionZDR || retention == GoalRetentionNonZDR || collection != "" {
		if err := ValidateOpenRouterModelID(modelID); err != nil {
			return err
		}
	}
	if retention == GoalRetentionNonZDR && collection != "deny" {
		return fmt.Errorf("goalloop: explicit OpenRouter non-ZDR requires data_collection=deny")
	}
	if err := r.WorkspaceLicense.Validate(); err != nil {
		return err
	}
	if r.PolicyVersion == GoalModelPolicyVersionV1 {
		if r.RetentionMode == "" {
			return fmt.Errorf("goalloop: v1 goal model policy requires an explicit retention mode")
		}
		if retention == GoalRetentionZDR && !r.WorkspaceLicense.IsZero() {
			return fmt.Errorf("goalloop: strict ZDR policy must not bind workspace license evidence")
		}
		if retention != GoalRetentionZDR && r.WorkspaceLicense.IsZero() {
			return fmt.Errorf("goalloop: non-ZDR goal policy requires bound workspace license evidence")
		}
		if r.PolicyAction != "allow" || !validGoalPolicyLabel(r.Policy) || !validGoalPolicyCode(r.PolicyReasonCode) {
			return fmt.Errorf("goalloop: v1 goal model policy decision is invalid")
		}
	} else if r.RetentionMode != "" || r.Policy != "" || r.PolicyAction != "" || r.PolicyReasonCode != "" || !r.WorkspaceLicense.IsZero() {
		return fmt.Errorf("goalloop: explicit model policy metadata requires a policy version")
	}
	return nil
}

// ValidateOpenRouterModelID requires the canonical provider/model identity
// OpenRouter uses on the wire and in responses. Persisting an unqualified ID
// would make an otherwise successful exact-model request appear to drift after
// the provider normalizes it.
func ValidateOpenRouterModelID(modelID string) error {
	provider, slug, qualified := strings.Cut(strings.TrimSpace(modelID), "/")
	if !qualified || provider == "" || slug == "" || strings.Contains(slug, "/") {
		return fmt.Errorf("goalloop: an explicit OpenRouter privacy policy requires a canonical provider/model identifier")
	}
	return nil
}

func validGoalPolicyLabel(value string) bool {
	switch value {
	case "strict_zdr", "oss_non_zdr", "oss_legacy":
		return true
	default:
		return false
	}
}

func validGoalPolicyCode(value string) bool {
	if value == "" || len(value) > 64 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r != '_' && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

// EffectiveRetentionMode maps receipt-less legacy histories onto the governed
// legacy path while preserving an explicitly persisted false/non-ZDR mode.
func (r GoalModelRequest) EffectiveRetentionMode() string {
	if r.RetentionMode != "" {
		return strings.ToLower(strings.TrimSpace(r.RetentionMode))
	}
	if r.OpenRouterZDR {
		return GoalRetentionZDR
	}
	return GoalRetentionLegacy
}

func containsGoalControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
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

// Ledger exposes the loop's run ledger for adapters that record
// durable-scheduler events (approval waits, resolutions) alongside the
// loop's own audit trail.
func (l *Loop) Ledger() runledger.Store {
	return l.ledger
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
