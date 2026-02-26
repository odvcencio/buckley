package model

import "testing"

func TestRoutingHooks_AppliesInOrder(t *testing.T) {
	hooks := NewRoutingHooks()
	hooks.Register(func(decision *RoutingDecision) *RoutingDecision {
		decision.SelectedModel = "first"
		decision.Reason = "first"
		return decision
	})
	hooks.Register(func(decision *RoutingDecision) *RoutingDecision {
		decision.SelectedModel = decision.SelectedModel + ":second"
		return decision
	})

	decision := &RoutingDecision{RequestedModel: "base", SelectedModel: "base"}
	out := hooks.Apply(decision)
	if out.SelectedModel != "first:second" {
		t.Fatalf("expected selected model updated, got %q", out.SelectedModel)
	}
	if out.Reason != "first" {
		t.Fatalf("expected reason preserved, got %q", out.Reason)
	}
}

func TestRoutingHooks_IgnoresNilHooks(t *testing.T) {
	hooks := NewRoutingHooks()
	hooks.Register(nil)

	decision := &RoutingDecision{RequestedModel: "base", SelectedModel: "base"}
	out := hooks.Apply(decision)
	if out.SelectedModel != "base" {
		t.Fatalf("expected decision unchanged, got %q", out.SelectedModel)
	}
}
