package subagent

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/persona"
)

func TestChildContract_RoundTripsExplicitEmptyTools(t *testing.T) {
	encoded, err := EncodeChildContract(ChildContractFromRequest(Request{
		Model:           "openai/gpt-5.4-mini",
		Tier:            persona.TierExecute,
		Effort:          "medium",
		SystemPrompt:    "Follow the review protocol.",
		AllowedTools:    []string{},
		StepCap:         4,
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
