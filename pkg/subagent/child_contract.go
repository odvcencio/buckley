package subagent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ChildContractEnv carries the resolved, versioned execution contract from a
// local-process parent into its Buckley child. It is intentionally an
// environment value rather than a second command-line grammar: it can carry
// prompts and explicit-empty tool allowlists without shell quoting ambiguity.
const ChildContractEnv = "BUCKLEY_SUBAGENT_CONTRACT_V1"

const (
	childContractVersion  = "buckley.subagent-contract/v1"
	maxChildContractBytes = 64 * 1024
)

// ChildContract is the subset of a resolved Request that a Buckley child
// needs before it builds its own model and tool runtime. ToolsConstrained
// preserves the important nil-versus-empty distinction: an empty allowlist
// means no tools, while an unconstrained task inherits the child profile.
type ChildContract struct {
	SchemaVersion    string   `json:"schema_version"`
	Model            string   `json:"model,omitempty"`
	Tier             string   `json:"tier,omitempty"`
	Effort           string   `json:"effort,omitempty"`
	SystemPrompt     string   `json:"system_prompt,omitempty"`
	AllowedTools     []string `json:"allowed_tools,omitempty"`
	ToolsConstrained bool     `json:"tools_constrained,omitempty"`
	StepCap          int      `json:"step_cap,omitempty"`
	ApprovalPosture  string   `json:"approval_posture,omitempty"`
	OutputSchema     string   `json:"output_schema,omitempty"`
}

// ChildContractFromRequest selects the resolved fields that must survive the
// process boundary. Request.Task is deliberately excluded because it is
// already passed as the child command's task argument.
func ChildContractFromRequest(request Request) ChildContract {
	return ChildContract{
		SchemaVersion:    childContractVersion,
		Model:            strings.TrimSpace(request.Model),
		Tier:             strings.TrimSpace(string(request.Tier)),
		Effort:           strings.TrimSpace(request.Effort),
		SystemPrompt:     strings.TrimSpace(request.SystemPrompt),
		AllowedTools:     copyStrings(request.AllowedTools),
		ToolsConstrained: request.AllowedTools != nil,
		StepCap:          request.StepCap,
		ApprovalPosture:  strings.TrimSpace(request.ApprovalPosture),
		OutputSchema:     strings.TrimSpace(request.OutputSchema),
	}
}

// EncodeChildContract serializes a child contract for ChildContractEnv.
func EncodeChildContract(contract ChildContract) (string, error) {
	if contract.SchemaVersion == "" {
		contract.SchemaVersion = childContractVersion
	}
	if err := validateChildContract(contract); err != nil {
		return "", err
	}
	payload, err := json.Marshal(contract)
	if err != nil {
		return "", fmt.Errorf("marshal subagent child contract: %w", err)
	}
	if len(payload) > maxChildContractBytes {
		return "", fmt.Errorf("subagent child contract exceeds %d bytes", maxChildContractBytes)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecodeChildContract parses ChildContractEnv's value. present reports
// whether the caller supplied a contract at all; malformed contracts fail
// closed rather than allowing a child to run with looser settings.
func DecodeChildContract(value string) (contract ChildContract, present bool, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return ChildContract{}, false, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return ChildContract{}, true, fmt.Errorf("decode subagent child contract: %w", err)
	}
	if len(payload) > maxChildContractBytes {
		return ChildContract{}, true, fmt.Errorf("subagent child contract exceeds %d bytes", maxChildContractBytes)
	}
	if err := json.Unmarshal(payload, &contract); err != nil {
		return ChildContract{}, true, fmt.Errorf("unmarshal subagent child contract: %w", err)
	}
	if err := validateChildContract(contract); err != nil {
		return ChildContract{}, true, err
	}
	contract.AllowedTools = copyStrings(contract.AllowedTools)
	if contract.ToolsConstrained && contract.AllowedTools == nil {
		contract.AllowedTools = []string{}
	}
	return contract, true, nil
}

func validateChildContract(contract ChildContract) error {
	if contract.SchemaVersion != childContractVersion {
		return fmt.Errorf("unsupported subagent child contract version %q", contract.SchemaVersion)
	}
	if contract.StepCap < 0 {
		return fmt.Errorf("subagent child contract step_cap must not be negative")
	}
	if len(strings.TrimSpace(contract.OutputSchema)) > 256 {
		return fmt.Errorf("subagent child contract output_schema exceeds 256 bytes")
	}
	return nil
}
