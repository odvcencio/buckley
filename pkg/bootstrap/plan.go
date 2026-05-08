package bootstrap

import (
	"context"
	"fmt"

	"github.com/odvcencio/buckley/pkg/types"
)

// Phase represents a bootstrap initialization phase.
type Phase int

const (
	PhaseCliParse Phase = iota
	PhaseConfigLoad
	PhaseArbiterInit // rules engine loaded — governance begins
	PhaseAuthResolve
	PhaseModelInit
	PhaseStorageInit
	PhaseSessionResolve
	PhaseToolRegistry
	PhaseGTSInit
	PhaseModeRoute // arbiter decides: interactive / oneshot / daemon
	PhaseRuntimeInit
	PhaseReady
)

var phaseNames = [...]string{
	"cli_parse", "config_load", "arbiter_init", "auth_resolve",
	"model_init", "storage_init", "session_resolve", "tool_registry",
	"gts_init", "mode_route", "runtime_init", "ready",
}

func (p Phase) String() string {
	if int(p) < len(phaseNames) {
		return phaseNames[p]
	}
	return fmt.Sprintf("phase_%d", p)
}

// PhaseExecutor runs a single phase.
type PhaseExecutor func(ctx context.Context, phase Phase) error

// BootstrapPlan manages ordered initialization with arbiter gating.
type BootstrapPlan struct {
	phases    []Phase
	evaluator types.RuleEvaluator // nil before PhaseArbiterInit
	executor  PhaseExecutor
	results   map[Phase]any
}

// NewBootstrapPlan creates a plan with all phases in order.
func NewBootstrapPlan(evaluator types.RuleEvaluator) *BootstrapPlan {
	phases := make([]Phase, PhaseReady+1)
	for i := range phases {
		phases[i] = Phase(i)
	}
	return &BootstrapPlan{
		phases:    phases,
		evaluator: evaluator,
		results:   make(map[Phase]any),
	}
}

// SetExecutor sets the function that runs each phase.
func (b *BootstrapPlan) SetExecutor(exec PhaseExecutor) {
	b.executor = exec
}

// SetResult stores a phase result for later phases to consume.
func (b *BootstrapPlan) SetResult(phase Phase, result any) {
	b.results[phase] = result
}

// Result retrieves a stored phase result.
func (b *BootstrapPlan) Result(phase Phase) any {
	return b.results[phase]
}

// Run executes all phases in order with arbiter gating after PhaseArbiterInit.
func (b *BootstrapPlan) Run(ctx context.Context) error {
	for _, phase := range b.phases {
		// After arbiter init, check phase gate
		if b.evaluator != nil && phase > PhaseArbiterInit {
			result, err := b.evaluator.EvalStrategy("bootstrap/phases", "phase_gate", map[string]any{
				"phase":           phase.String(),
				"environment":     b.stringResult(PhaseConfigLoad, "environment"),
				"execution_mode":  b.stringResult(PhaseModeRoute, "execution_mode"),
				"gts_binary":      b.boolResult(PhaseConfigLoad, "gts_binary"),
				"permission_tier": b.stringResult(PhaseConfigLoad, "permission_tier"),
			})
			if err == nil {
				switch result.String("action") {
				case "skip":
					continue
				case "deny":
					return fmt.Errorf("bootstrap phase %s denied: %s", phase, result.String("reason"))
				}
			}
			// On eval error, proceed (don't block startup)
		}

		if b.executor != nil {
			if err := b.executor(ctx, phase); err != nil {
				return fmt.Errorf("bootstrap %s: %w", phase, err)
			}
		}
	}
	return nil
}

func (b *BootstrapPlan) stringResult(phase Phase, key string) string {
	if m, ok := b.results[phase].(map[string]any); ok {
		if v, ok := m[key].(string); ok {
			return v
		}
	}
	return ""
}

func (b *BootstrapPlan) boolResult(phase Phase, key string) bool {
	if m, ok := b.results[phase].(map[string]any); ok {
		if v, ok := m[key].(bool); ok {
			return v
		}
	}
	return false
}
