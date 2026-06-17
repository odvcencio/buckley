package interop

import (
	"strings"
	"testing"
)

func TestImportOmnigentPolicies_TopLevelAndGuardrails(t *testing.T) {
	data := []byte(`
policies:
  limit_tools:
    type: function
    handler: omnigent.policies.builtins.safety.max_tool_calls_per_session
    factory_params:
      limit: 42
guardrails:
  policies:
    deny_shell:
      type: function
      function:
        path: omnigent.policies.function.make_fixed_action_callable
        arguments:
          action: deny
          on_tools: [run_shell, write_file]
`)

	result, err := ImportOmnigentPolicies(data)
	if err != nil {
		t.Fatalf("ImportOmnigentPolicies: %v", err)
	}
	if result.Summary.Policies != 2 {
		t.Fatalf("policies=%d want 2", result.Summary.Policies)
	}
	if result.Summary.Supported != 1 || result.Summary.Partial != 1 {
		t.Fatalf("summary=%+v", result.Summary)
	}

	limit := result.Mappings[0]
	if limit.Status != StatusSupported {
		t.Fatalf("limit status=%q want supported", limit.Status)
	}
	if got := limit.Targets[0].Domain; got != "tool_budget" {
		t.Fatalf("limit domain=%q want tool_budget", got)
	}
	if !strings.Contains(strings.Join(limit.Notes, "\n"), "42") {
		t.Fatalf("expected limit note to mention 42: %+v", limit.Notes)
	}

	deny := result.Mappings[1]
	if deny.Policy.Source != "guardrails.policies" {
		t.Fatalf("source=%q want guardrails.policies", deny.Policy.Source)
	}
	if deny.Status != StatusPartial {
		t.Fatalf("deny status=%q want partial", deny.Status)
	}
	if got := deny.Targets[0].Domain; got != "risk" {
		t.Fatalf("deny domain=%q want risk", got)
	}
}

func TestImportOmnigentPolicies_HandlerShapesAndWarnings(t *testing.T) {
	data := []byte(`
policies:
  pii:
    type: function
    function: omnigent.policies.builtins.safety.deny_pii_in_llm_request
    condition:
      sensitivity: confidential
    set_labels: [sensitivity]
`)

	result, err := ImportOmnigentPolicies(data)
	if err != nil {
		t.Fatalf("ImportOmnigentPolicies: %v", err)
	}
	if result.Summary.Unsupported != 1 {
		t.Fatalf("summary=%+v", result.Summary)
	}
	mapping := result.Mappings[0]
	if mapping.Policy.Handler != "omnigent.policies.builtins.safety.deny_pii_in_llm_request" {
		t.Fatalf("handler=%q", mapping.Policy.Handler)
	}
	warnings := strings.Join(mapping.Warnings, "\n")
	if !strings.Contains(warnings, "prompt/security") {
		t.Fatalf("expected PII domain warning, got %q", warnings)
	}
	if !strings.Contains(warnings, "label-gated") || !strings.Contains(warnings, "label writes") {
		t.Fatalf("expected label warnings, got %q", warnings)
	}
}

func TestImportOmnigentPolicies_NoPolicies(t *testing.T) {
	_, err := ImportOmnigentPolicies([]byte("name: no_policies\n"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no policies") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRenderText(t *testing.T) {
	result, err := ImportOmnigentPolicies([]byte(`
policies:
  budget:
    type: function
    handler: omnigent.policies.builtins.cost.cost_budget
    factory_params:
      max_cost_usd: 5
`))
	if err != nil {
		t.Fatalf("ImportOmnigentPolicies: %v", err)
	}
	out := RenderText(result)
	for _, want := range []string{"Omnigent policy interop", "budget", "cost/budgets", "max_cost_usd=5"} {
		if !strings.Contains(out, want) {
			t.Fatalf("RenderText missing %q in:\n%s", want, out)
		}
	}
}
