package agentloop

import (
	"fmt"
	"strings"
	"time"
)

// This file is the section-20 progress controller (goal-loop design G5):
// the layer above the Governor that decides what kind of work happens
// next, not just whether the loop should stop. The Governor stays the
// per-turn safety backstop; the ProgressController consumes richer
// signals (evidence novelty, verification debt, budget slope, pressure)
// and emits a routed decision with a policy trace, so every controller
// decision is explainable from the ledger.

// ProgressDecision is the controller's routed outcome.
type ProgressDecision string

const (
	DecideContinue   ProgressDecision = "continue"
	DecideVerify     ProgressDecision = "verify"
	DecideCheckpoint ProgressDecision = "checkpoint"
	DecideReplan     ProgressDecision = "replan"
	DecideSynthesize ProgressDecision = "synthesize"
	DecidePark       ProgressDecision = "park"
	DecideStopSafety ProgressDecision = "stop_safety"
)

// ProgressState carries the signals one decision consumes. All ratios are
// 0..1. Zero means unknown for estimator-style signals (GoalProgress,
// Uncertainty, CostSlope); the deterministic policy only acts on signals
// that are actually present, so an unknown never triggers a route.
type ProgressState struct {
	// GoalProgress estimates completion of the current task (0 unknown).
	GoalProgress float64
	// EvidenceNovelty is the share of recent tool outcomes whose content
	// hash was new (0 = everything was a re-read, 1 = all new).
	EvidenceNovelty float64
	// EvidenceObserved reports whether enough outcomes exist for
	// EvidenceNovelty to mean anything.
	EvidenceObserved bool
	// StateChanged reports durable state change since the last decision
	// (files edited, tests flipped, checkpoint written).
	StateChanged bool
	// Uncertainty is the current uncertainty estimate (0 unknown).
	Uncertainty float64
	// VerificationDebt is the risk-weighted unresolved-check scalar
	// (taskstate.CheckpointState.VerificationDebt feeds it).
	VerificationDebt float64
	// Repetition is the Governor-derived repetition pressure (0 none,
	// 1 = at a stop threshold).
	Repetition float64
	// Pressure is provider-reported context usage over the window.
	Pressure float64
	// BudgetRemaining is the remaining fraction of the dollar budget
	// (1 = untouched, 0 = exhausted). Negative means overrun.
	BudgetRemaining float64
	// BudgetSet reports whether a budget applies at all.
	BudgetSet bool
	// TaskDone reports that the task's acceptance criteria are met and
	// only synthesis/reporting remains.
	TaskDone bool
	// ToolCalls, SuccessfulToolCalls, FailedToolCalls, and the yield fields
	// are the observed operation projection. They make completed empty
	// searches distinguishable from tool failures without asking a model to
	// infer it from prose.
	ToolCalls                 int
	SuccessfulToolCalls       int
	FailedToolCalls           int
	YieldObservedCalls        int
	ZeroYieldCalls            int
	ConsecutiveZeroYieldCalls int
}

// ProgressSnapshot is the provider-neutral operation summary accumulated by
// Controller. It is compact enough for a ledger event, TUI operation card,
// ACP projection, or partial-result artifact.
type ProgressSnapshot struct {
	ToolCalls                 int    `json:"tool_calls"`
	SuccessfulToolCalls       int    `json:"successful_tool_calls"`
	FailedToolCalls           int    `json:"failed_tool_calls"`
	YieldObservedCalls        int    `json:"yield_observed_calls"`
	ZeroYieldCalls            int    `json:"zero_yield_calls"`
	ConsecutiveZeroYieldCalls int    `json:"consecutive_zero_yield_calls"`
	LastToolName              string `json:"last_tool_name,omitempty"`
	LastYieldCount            int    `json:"last_yield_count,omitempty"`
	LastYieldUnit             string `json:"last_yield_unit,omitempty"`
}

type progressTracker struct {
	snapshot ProgressSnapshot
}

func (t *progressTracker) Observe(toolName string, outcome ToolOutcome) {
	if t == nil {
		return
	}
	t.snapshot.ToolCalls++
	if outcome.Success {
		t.snapshot.SuccessfulToolCalls++
	} else {
		t.snapshot.FailedToolCalls++
	}
	if !outcome.YieldObserved {
		return
	}
	t.snapshot.YieldObservedCalls++
	t.snapshot.LastToolName = strings.TrimSpace(toolName)
	t.snapshot.LastYieldCount = outcome.YieldCount
	t.snapshot.LastYieldUnit = strings.TrimSpace(outcome.YieldUnit)
	if outcome.YieldCount == 0 {
		t.snapshot.ZeroYieldCalls++
		t.snapshot.ConsecutiveZeroYieldCalls++
		return
	}
	t.snapshot.ConsecutiveZeroYieldCalls = 0
}

func (t *progressTracker) Snapshot() ProgressSnapshot {
	if t == nil {
		return ProgressSnapshot{}
	}
	return t.snapshot
}

// FuseCounters are the monotonic run totals the emergency fuses compare
// against (section 20.8). They bound a run regardless of policy.
type FuseCounters struct {
	ModelRequests  int
	ToolExecutions int
	Elapsed        time.Duration
	SpentUSD       float64
}

// Fuses are the distant emergency limits. Zero values disable the
// corresponding fuse; config supplies the defaults (500 requests, 2000
// tool executions, 6 hours).
type Fuses struct {
	ModelRequests  int
	ToolExecutions int
	WallTime       time.Duration
	BudgetUSD      float64
}

// Controller modes (config agent_controller.mode). Legacy means the
// controller is not consulted; shadow means decisions are recorded but
// not applied; dynamic means decisions route the loop.
const (
	ModeLegacy  = "legacy"
	ModeShadow  = "shadow"
	ModeDynamic = "dynamic"
)

// NewProgressController returns a configured controller only for the staged
// rollout modes. Legacy and unknown modes deliberately leave the shared turn
// engine unchanged, so a configuration typo cannot silently activate routing.
func NewProgressController(mode, policyVersion string, fuses Fuses) *ProgressController {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != ModeShadow && mode != ModeDynamic {
		return nil
	}
	return &ProgressController{
		Mode:          mode,
		PolicyVersion: strings.TrimSpace(policyVersion),
		Fuses:         fuses,
	}
}

// ProgressThresholds are the deterministic policy's cut points. The zero
// value takes the spec defaults.
type ProgressThresholds struct {
	// CheckpointPressure schedules a checkpoint/epoch at this usage
	// ratio (section 27 default 0.70 for compact; checkpoint fires
	// earlier via taskstate triggers at 0.65).
	CheckpointPressure float64
	// VerifyDebt routes to verification when debt exceeds this after a
	// state change (section 20.3 default 0.25).
	VerifyDebt float64
	// ReplanRepetition routes to replanning when repetition is at least
	// this and evidence novelty is low.
	ReplanRepetition float64
	// LowNovelty is the "nothing new is being learned" cut.
	LowNovelty float64
	// ParkUncertainty parks the task when uncertainty is at least this
	// and novelty is low: park-not-burn.
	ParkUncertainty float64
}

func (t ProgressThresholds) normalized() ProgressThresholds {
	if t.CheckpointPressure <= 0 {
		t.CheckpointPressure = 0.70
	}
	if t.VerifyDebt <= 0 {
		t.VerifyDebt = 0.25
	}
	if t.ReplanRepetition <= 0 {
		t.ReplanRepetition = 0.6
	}
	if t.LowNovelty <= 0 {
		t.LowNovelty = 0.2
	}
	if t.ParkUncertainty <= 0 {
		t.ParkUncertainty = 0.7
	}
	return t
}

// ProgressController evaluates the deterministic next-action policy
// (section 20.5). It holds no counters itself: callers own FuseCounters
// and ProgressState, so the controller stays a pure decision function and
// shadow mode cannot drift from dynamic mode.
type ProgressController struct {
	Mode          string
	PolicyVersion string
	Thresholds    ProgressThresholds
	Fuses         Fuses
}

// PolicyTraceStep is one evaluated rule: which rule, whether it fired,
// and the compact reason. The full trace makes every decision replayable
// from the ledger (event controller.decision).
type PolicyTraceStep struct {
	Rule  string `json:"rule"`
	Fired bool   `json:"fired"`
	Note  string `json:"note,omitempty"`
}

// ProgressResult is one decision plus its explanation.
type ProgressResult struct {
	Decision ProgressDecision  `json:"decision"`
	Reason   string            `json:"reason"`
	Trace    []PolicyTraceStep `json:"trace"`
	// Apply reports whether the caller should act on the decision:
	// true only in dynamic mode. Shadow callers record and continue.
	Apply bool `json:"apply"`
}

// Decide runs the deterministic priority order (section 20.5): fuses,
// budget, pressure, verification debt, repetition, park-not-burn,
// synthesis, continue. The first firing rule wins; every rule's
// evaluation lands in the trace.
func (c ProgressController) Decide(state ProgressState, counters FuseCounters) ProgressResult {
	thresholds := c.Thresholds.normalized()
	var trace []PolicyTraceStep
	decision := DecideContinue
	reason := "no routing rule fired"
	decided := false

	step := func(rule string, fired bool, note string, d ProgressDecision) {
		trace = append(trace, PolicyTraceStep{Rule: rule, Fired: fired && !decided, Note: note})
		if fired && !decided {
			decided = true
			decision = d
			reason = note
		}
	}

	fuseReason := c.fuseTripped(counters)
	step("emergency_fuse", fuseReason != "", fuseReason, DecideStopSafety)

	budgetExhausted := state.BudgetSet && state.BudgetRemaining <= 0
	step("budget_exhausted", budgetExhausted,
		"dollar budget exhausted; checkpoint and park", DecidePark)

	pressureHigh := state.Pressure >= thresholds.CheckpointPressure
	step("pressure_checkpoint", pressureHigh,
		fmt.Sprintf("context pressure %.2f at or above %.2f; schedule an epoch checkpoint", state.Pressure, thresholds.CheckpointPressure), DecideCheckpoint)

	verify := state.StateChanged && state.VerificationDebt > thresholds.VerifyDebt
	step("verification_debt", verify,
		fmt.Sprintf("verification debt %.2f above %.2f after state change", state.VerificationDebt, thresholds.VerifyDebt), DecideVerify)

	replan := state.Repetition >= thresholds.ReplanRepetition &&
		state.EvidenceObserved && state.EvidenceNovelty <= thresholds.LowNovelty
	step("repetition_replan", replan,
		"repeated actions with no new evidence; replan instead of retrying", DecideReplan)

	park := state.Uncertainty >= thresholds.ParkUncertainty &&
		state.EvidenceObserved && state.EvidenceNovelty <= thresholds.LowNovelty
	step("park_not_burn", park,
		"high uncertainty and low expected information gain; park for the morning report", DecidePark)

	step("synthesize", state.TaskDone,
		"acceptance criteria met; synthesize and report", DecideSynthesize)

	if !decided {
		trace = append(trace, PolicyTraceStep{Rule: "continue", Fired: true, Note: "default"})
		reason = "default: keep working"
	}

	return ProgressResult{
		Decision: decision,
		Reason:   reason,
		Trace:    trace,
		Apply:    c.Mode == ModeDynamic,
	}
}

// fuseTripped returns a non-empty reason when any emergency fuse is
// exceeded. Fuses are runaway protection, never task budgets.
func (c ProgressController) fuseTripped(counters FuseCounters) string {
	var tripped []string
	if c.Fuses.ModelRequests > 0 && counters.ModelRequests >= c.Fuses.ModelRequests {
		tripped = append(tripped, fmt.Sprintf("model requests %d/%d", counters.ModelRequests, c.Fuses.ModelRequests))
	}
	if c.Fuses.ToolExecutions > 0 && counters.ToolExecutions >= c.Fuses.ToolExecutions {
		tripped = append(tripped, fmt.Sprintf("tool executions %d/%d", counters.ToolExecutions, c.Fuses.ToolExecutions))
	}
	if c.Fuses.WallTime > 0 && counters.Elapsed >= c.Fuses.WallTime {
		tripped = append(tripped, fmt.Sprintf("wall time %s/%s", counters.Elapsed.Round(time.Minute), c.Fuses.WallTime))
	}
	if c.Fuses.BudgetUSD > 0 && counters.SpentUSD >= c.Fuses.BudgetUSD {
		tripped = append(tripped, fmt.Sprintf("spend $%.2f/$%.2f", counters.SpentUSD, c.Fuses.BudgetUSD))
	}
	if len(tripped) == 0 {
		return ""
	}
	return "emergency fuse: " + strings.Join(tripped, ", ")
}
