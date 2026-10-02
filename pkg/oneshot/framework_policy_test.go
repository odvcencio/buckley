package oneshot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/rules"
)

type policyDefinition struct {
	retentionDefinition
	facts     map[string]any
	ctxCalls  int
	factCalls int
}

func (d *policyDefinition) PolicyFacts(*Context, json.RawMessage) (*PolicyRequest, error) {
	d.factCalls++
	return &PolicyRequest{
		Domain: "commit_message", Strategy: "commit_message_policy", Facts: d.facts,
		Fail: func(action, reason string) error { return errors.New("policy " + action + ": " + reason) },
	}, nil
}

func (d *policyDefinition) ValidateWithContext(*Context, json.RawMessage) error {
	d.ctxCalls++
	return errors.New("fallback check")
}

func policyFacts(mutate func(map[string]any)) map[string]any {
	f := map[string]any{
		"deny_hits": 0, "removed_echo": 0, "sensitive_hits": 0,
		"max_bullet_words": 5, "bullet_count": 1, "generated_ratio": 0.0, "diff_files": 1,
	}
	mutate(f)
	return f
}

func TestFrameworkFollowsPolicyOutcome(t *testing.T) {
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		mutate      func(map[string]any)
		wantErr     string
		wantInvokes int
	}{
		{"allow", func(map[string]any) {}, "", 1},
		{"repair retries to the limit", func(f map[string]any) { f["deny_hits"] = 1 }, "deny_list", 3},
		{"block stops at once", func(f map[string]any) { f["deny_hits"] = 3 }, "repeated_private_content", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cap := &promptCapture{}
			def := &policyDefinition{facts: policyFacts(tc.mutate)}
			_, err := NewFramework(cap, engine).Run(context.Background(), def, RunOpts{MaxRetries: 3})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if len(cap.prompts) != tc.wantInvokes {
				t.Fatalf("invokes = %d, want %d", len(cap.prompts), tc.wantInvokes)
			}
			if def.ctxCalls != 0 {
				t.Fatal("fallback ran although the engine decided")
			}
		})
	}
}

func TestFrameworkFallsBackWhenPolicyDomainMissing(t *testing.T) {
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	def := &policyDefinition{facts: policyFacts(func(map[string]any) {})}
	// An unknown strategy name makes EvalStrategy fail, so the Go check must run.
	bad := &badStrategyDefinition{policyDefinition: *def}
	_, err = NewFramework(&promptCapture{}, engine).Run(context.Background(), bad, RunOpts{MaxRetries: 1})
	if err == nil || !strings.Contains(err.Error(), "fallback check") {
		t.Fatalf("error = %v, want the fallback check to run", err)
	}
}

type badStrategyDefinition struct{ policyDefinition }

func (d *badStrategyDefinition) PolicyFacts(c *Context, r json.RawMessage) (*PolicyRequest, error) {
	req, err := d.policyDefinition.PolicyFacts(c, r)
	req.Strategy = "does_not_exist"
	return req, err
}
