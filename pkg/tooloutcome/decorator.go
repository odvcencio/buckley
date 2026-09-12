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
}

// Begin records pre-execution workspace state when a non-read-only tool might
// mutate the workspace. It emits no file contents.
func Begin(ctx context.Context, workDir, effectClass string) Observation {
	return begin(ctx, workDir, strings.TrimSpace(effectClass), false)
}

func BeginWithMetadata(ctx context.Context, workDir string, metadata tool.ToolMetadata) Observation {
	return begin(ctx, workDir, string(metadata.Impact), metadata.Verification)
}

func begin(ctx context.Context, workDir, effectClass string, observeVerification bool) Observation {
	observation := Observation{
		EffectClass: effectClass,
		workDir:     strings.TrimSpace(workDir),
	}
	observation.observe = observation.workDir != "" &&
		observation.EffectClass != "" &&
		(observeVerification || observation.EffectClass != string(tool.ImpactReadOnly)) &&
		observation.EffectClass != "control"
	if observation.observe {
		observation.beforeState, observation.beforeErr = workspaceevidence.GitStateFingerprint(ctx, observation.workDir)
	}
	return observation
}

// Finish decorates outcome with effect, state-change, and verification facts.
// Verification comes only from typed tool metadata and actual Result.Success.
func (o Observation) Finish(ctx context.Context, outcome agentloop.ToolOutcome, metadata tool.ToolMetadata, result *builtin.Result, execErr error) agentloop.ToolOutcome {
	if strings.TrimSpace(outcome.EffectClass) == "" {
		outcome.EffectClass = o.EffectClass
	}
	if o.observe {
		afterState, afterErr := workspaceevidence.GitStateFingerprint(ctx, o.workDir)
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
