package tooloutcome

import (
	"context"
	"errors"
	"strings"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/workspaceevidence"
)

// Observation captures the provider-neutral facts needed to decorate a raw
// tool execution result for agentloop's completion contract.
type Observation struct {
	EffectClass string
	workDir     string
	beforeState string
	beforeErr   error
	observe     bool
	bestEffort  bool
	warning     string
}

// Begin records pre-execution workspace state when a non-read-only tool might
// mutate the workspace. It emits no file contents.
func Begin(ctx context.Context, workDir, effectClass string) Observation {
	return begin(ctx, workDir, strings.TrimSpace(effectClass), false)
}

func BeginWithMetadata(ctx context.Context, workDir string, metadata tool.ToolMetadata) Observation {
	return begin(ctx, workDir, string(metadata.Impact), metadata.Verification)
}

func BeginBestEffortWithMetadata(ctx context.Context, workDir string, metadata tool.ToolMetadata) Observation {
	return beginWithPolicy(ctx, workDir, string(metadata.Impact), metadata.Verification, true)
}

func begin(ctx context.Context, workDir, effectClass string, observeVerification bool) Observation {
	return beginWithPolicy(ctx, workDir, effectClass, observeVerification, false)
}

func beginWithPolicy(ctx context.Context, workDir, effectClass string, observeVerification, bestEffort bool) Observation {
	observation := Observation{
		EffectClass: effectClass,
		workDir:     strings.TrimSpace(workDir),
		bestEffort:  bestEffort,
	}
	observation.observe = observation.workDir != "" &&
		observation.EffectClass != "" &&
		(observeVerification || observation.EffectClass != string(tool.ImpactReadOnly)) &&
		observation.EffectClass != "control"
	if observation.observe {
		observation.beforeState, observation.beforeErr = observation.fingerprint(ctx)
	}
	return observation
}

// Finish decorates outcome with effect, state-change, and verification facts.
// Verification comes from trusted typed checks or recognized foreground shell checks.
func (o Observation) Finish(ctx context.Context, outcome agentloop.ToolOutcome, metadata tool.ToolMetadata, result *builtin.Result, execErr error) agentloop.ToolOutcome {
	if strings.TrimSpace(outcome.EffectClass) == "" {
		outcome.EffectClass = o.EffectClass
	}
	if o.observe {
		afterState, afterErr := o.fingerprint(ctx)
		outcome.StateObservationError = o.warning
		if o.beforeErr != nil || afterErr != nil {
			outcome.StateObservationFailed = true
			outcome.StateObservationError = errors.Join(o.beforeErr, afterErr).Error()
		} else {
			outcome.StateObserved = true
			outcome.StateChanged = o.beforeState != afterState
		}
	}
	if metadata.Verification {
		outcome.VerificationObserved = true
		outcome.VerificationPassed = execErr == nil && result != nil && result.Success
		if outcome.StateObserved && outcome.StateChanged {
			outcome.Content += "\n\n[Buckley verification] Workspace state changed during this check. This result does not verify the final workspace state. Inspect the change and, if safe and within the task and remaining budget, run a relevant check again. If you cannot, report verification as incomplete."
		}
	}
	return outcome
}

func (o *Observation) fingerprint(ctx context.Context) (string, error) {
	if !o.bestEffort {
		return workspaceevidence.GitStateFingerprint(ctx, o.workDir)
	}
	state, err := workspaceevidence.GitStateFingerprintWithFallback(ctx, o.workDir)
	if state.Warning != "" {
		o.warning = state.Warning
	}
	return state.Digest, err
}
