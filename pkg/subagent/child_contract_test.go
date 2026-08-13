package subagent

import (
	"encoding/base64"
	"math"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/persona"
)

func TestChildContract_RoundTripsExplicitEmptyTools(t *testing.T) {
	encoded, err := EncodeChildContract(ChildContractFromRequest(Request{
		ID:              "run-child",
		ParentRunID:     "run-parent",
		ParentSessionID: "session-parent",
		TaskID:          "task-child",
		Model:           "openai/gpt-5.4-mini",
		Tier:            persona.TierExecute,
		Effort:          "medium",
		SystemPrompt:    "Follow the review protocol.",
		AllowedTools:    []string{},
		StepCap:         4,
		TimeoutSeconds:  90,
		Budget: agentcoord.Budget{
			MaxToolCalls:     17,
			MaxModelRequests: 9,
			MaxElapsedSecond: 75,
			MaxCostUSD:       1.25,
		},
		ApprovalPosture: "safe",
		OutputSchema:    "buckley.artifact/v1",
	}))
	if err != nil {
		t.Fatalf("EncodeChildContract: %v", err)
	}
	contract, present, err := DecodeChildContract(encoded)
	if err != nil {
		t.Fatalf("DecodeChildContract: %v", err)
	}
	if !present || !contract.ToolsConstrained || contract.AllowedTools == nil || len(contract.AllowedTools) != 0 {
		t.Fatalf("contract = %+v, want explicit empty tool allowlist", contract)
	}
	if contract.Model != "openai/gpt-5.4-mini" || contract.Tier != "execute" || contract.Effort != "medium" || contract.StepCap != 4 || contract.ApprovalPosture != "safe" || contract.OutputSchema != "buckley.artifact/v1" {
		t.Fatalf("contract = %+v", contract)
	}
	if contract.RunID != "run-child" || contract.ParentRunID != "run-parent" || contract.ParentSessionID != "session-parent" || contract.TaskID != "task-child" {
		t.Fatalf("contract lineage = %+v", contract)
	}
	if contract.TimeoutSeconds != 90 || contract.Budget.MaxToolCalls != 17 || contract.Budget.MaxModelRequests != 9 || contract.Budget.MaxElapsedSecond != 75 || contract.Budget.MaxCostUSD != 1.25 {
		t.Fatalf("contract limits = %+v", contract)
	}
}

func TestDecodeChildContract_RejectsMalformedAndUnsupportedValues(t *testing.T) {
	if _, present, err := DecodeChildContract("not-base64"); !present || err == nil {
		t.Fatalf("malformed contract = present %t err %v, want present error", present, err)
	}
	encoded, err := EncodeChildContract(ChildContract{SchemaVersion: "other/v1"})
	if err == nil || encoded != "" || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported version error = %v", err)
	}
}

func TestChildContract_RejectsNonFiniteCostBudgetsAtBothBoundaries(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		encoded, err := EncodeChildContract(ChildContract{
			SchemaVersion: childContractVersion,
			Budget:        agentcoord.Budget{MaxCostUSD: value},
		})
		if err == nil || encoded != "" || !strings.Contains(err.Error(), "must be finite") {
			t.Fatalf("EncodeChildContract(%v) = %q, %v", value, encoded, err)
		}
	}

	payload := []byte(`{"schema_version":"buckley.subagent-contract/v1","budget":{"max_cost_usd":1e1000}}`)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	if _, present, err := DecodeChildContract(encoded); !present || err == nil {
		t.Fatalf("DecodeChildContract(non-finite JSON) = present %t err %v", present, err)
	}
}
