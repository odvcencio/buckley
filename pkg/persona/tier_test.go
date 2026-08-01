package persona

import (
	"errors"
	"testing"
)

func TestTierFor(t *testing.T) {
	tests := []struct {
		name string
		p    Persona
		want Tier
	}{
		{"explicit tier pin wins", Persona{Tier: TierScrutiny, Model: "sonnet"}, TierScrutiny},
		{"model alias infers reason", Persona{Model: "fable"}, TierReason},
		{"model alias infers scrutiny", Persona{Model: "opus"}, TierScrutiny},
		{"model alias infers execute", Persona{Model: "sonnet"}, TierExecute},
		{"model alias infers execute (haiku)", Persona{Model: "haiku"}, TierExecute},
		{"unknown model resolves empty", Persona{Model: "some-bespoke-model"}, ""},
		{"no pin at all resolves empty", Persona{}, ""},
		{"tier pin is case-insensitive", Persona{Tier: "REASON"}, TierReason},
		{"invalid tier pin falls back to model", Persona{Tier: "bogus", Model: "opus"}, TierScrutiny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TierFor(tc.p); got != tc.want {
				t.Fatalf("TierFor(%+v) = %q, want %q", tc.p, got, tc.want)
			}
		})
	}
}

// TestValidateEscalation_PersonaPinnedSpawnResolvesPinnedModel covers item 3's
// first scenario: a persona-pinned spawn resolves the pinned model/tier
// regardless of parent context, as long as it does not exceed the parent's
// tier.
func TestValidateEscalation_PersonaPinnedSpawnResolvesPinnedModel(t *testing.T) {
	parent := Persona{Name: "root", Tier: TierReason}
	child := Persona{Name: "tiller-worker", Model: "sonnet"} // pinned, execute tier

	tier, err := ValidateEscalation(parent, child)
	if err != nil {
		t.Fatalf("ValidateEscalation: unexpected error: %v", err)
	}
	if tier != TierExecute {
		t.Fatalf("tier = %q, want %q", tier, TierExecute)
	}
}

// TestValidateEscalation_UnpinnedFallsBackToDefaultUnderReasonParent covers
// item 3's second scenario: an unpinned subagent under a reason-tier parent
// falls back to the configured default tier instead of inheriting reason.
func TestValidateEscalation_UnpinnedFallsBackToDefaultUnderReasonParent(t *testing.T) {
	parent := Persona{Name: "root", Model: "fable"} // reason tier
	child := Persona{Name: "general-purpose"}       // no explicit pin

	tier, err := ValidateEscalation(parent, child)
	if err != nil {
		t.Fatalf("ValidateEscalation: unexpected error: %v", err)
	}
	if tier != DefaultTier {
		t.Fatalf("tier = %q, want DefaultTier %q", tier, DefaultTier)
	}
	if TierFor(parent) != TierReason {
		t.Fatalf("test setup invariant broken: parent tier = %q, want reason", TierFor(parent))
	}
}

// TestValidateEscalation_UnpinnedFallsBackRegardlessOfParentTier confirms the
// implicit-inheritance guard applies even when the parent is not reason
// tier: an unpinned child always resolves to DefaultTier, never inherits.
func TestValidateEscalation_UnpinnedFallsBackRegardlessOfParentTier(t *testing.T) {
	parent := Persona{Name: "root", Tier: TierScrutiny}
	child := Persona{Name: "anonymous"}

	tier, err := ValidateEscalation(parent, child)
	if err != nil {
		t.Fatalf("ValidateEscalation: unexpected error: %v", err)
	}
	if tier != DefaultTier {
		t.Fatalf("tier = %q, want DefaultTier %q", tier, DefaultTier)
	}
}

// TestValidateEscalation_EscalationErrorSurfaces covers item 3's third
// scenario: a child persona explicitly pinning a tier higher than its
// parent's tier returns a typed *EscalationError instead of a resolved tier.
func TestValidateEscalation_EscalationErrorSurfaces(t *testing.T) {
	parent := Persona{Name: "tiller-worker", Model: "sonnet"}  // execute tier
	child := Persona{Name: "tiller-architect", Model: "fable"} // reason tier

	tier, err := ValidateEscalation(parent, child)
	if tier != "" {
		t.Fatalf("tier = %q, want empty on escalation error", tier)
	}
	var escalationErr *EscalationError
	if !errors.As(err, &escalationErr) {
		t.Fatalf("err = %v, want *EscalationError", err)
	}
	if escalationErr.ParentTier != TierExecute || escalationErr.ChildTier != TierReason {
		t.Fatalf("escalationErr = %+v, want ParentTier=execute ChildTier=reason", escalationErr)
	}
	if escalationErr.Error() == "" {
		t.Fatalf("expected a non-empty error message")
	}
}

// TestValidateEscalation_SameTierAllowed confirms a pinned child at the same
// tier as its parent is not treated as an escalation.
func TestValidateEscalation_SameTierAllowed(t *testing.T) {
	parent := Persona{Name: "tiller-investigator", Model: "opus"}
	child := Persona{Name: "tiller-reviewer", Model: "opus"}

	tier, err := ValidateEscalation(parent, child)
	if err != nil {
		t.Fatalf("ValidateEscalation: unexpected error: %v", err)
	}
	if tier != TierScrutiny {
		t.Fatalf("tier = %q, want %q", tier, TierScrutiny)
	}
}

// TestValidateEscalation_UnknownParentTierAllowsPinnedChild confirms that
// when the parent's own tier cannot be resolved, ValidateEscalation cannot
// judge an escalation and defers to the child's own pin.
func TestValidateEscalation_UnknownParentTierAllowsPinnedChild(t *testing.T) {
	parent := Persona{Name: "unknown-context"}
	child := Persona{Name: "tiller-architect", Model: "fable"}

	tier, err := ValidateEscalation(parent, child)
	if err != nil {
		t.Fatalf("ValidateEscalation: unexpected error: %v", err)
	}
	if tier != TierReason {
		t.Fatalf("tier = %q, want %q", tier, TierReason)
	}
}
