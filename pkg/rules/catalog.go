package rules

import (
	"reflect"
	"sort"
	"strings"
)

type FactField struct {
	Key  string `json:"key"`
	Type string `json:"type"`
}

type FactContract struct {
	Domain  string      `json:"domain"`
	Purpose string      `json:"purpose"`
	Facts   []FactField `json:"facts"`
}

type factContractSpec struct {
	domain  string
	purpose string
	facts   any
	fields  []FactField
}

var factContractSpecs = []factContractSpec{
	{domain: "complexity", purpose: "task complexity and plan/direct routing", facts: TaskFacts{}},
	{domain: "risk", purpose: "command and operation risk detection", facts: CommandFacts{}},
	{domain: "compaction", purpose: "legacy conversation compaction trigger", facts: ContextFacts{}},
	{domain: "retry", purpose: "retry and dead-end detection", facts: RetryFacts{}},
	{domain: "gts_context", purpose: "code intelligence context escalation", facts: GTSFacts{}},
	{domain: "approval", purpose: "tool and operation approval gate", fields: fields(
		field("approval.mode", "string"),
		field("risk.level", "string"),
	)},
	{domain: "routing", purpose: "model selection routing", fields: fields(
		field("task.phase", "string"),
		field("model.supports_reasoning", "bool"),
	)},
	{domain: "reasoning", purpose: "reasoning effort selection", fields: fields(
		field("reasoning.config", "string"),
		field("task.phase", "string"),
		field("model.supports_reasoning", "bool"),
	)},
	{domain: "oneshot", purpose: "one-shot command mode selection", facts: OneshotFacts{}},
	{domain: "spawning", purpose: "subagent spawn routing", facts: SpawningFacts{}},
	{domain: "coordinator", purpose: "coordinator budget tuning", facts: CoordinatorFacts{}},
	{domain: "tool_budget", purpose: "tool tier and call budget enforcement", facts: ToolBudgetFacts{}},
	{domain: "role_permissions", purpose: "role-based tool access", facts: RolePermissionFacts{}},
	{domain: "permissions/escalation", purpose: "permission escalation routing", facts: PermissionEscalationFacts{}},
	{domain: "permissions/sandbox", purpose: "sandbox level resolution", facts: SandboxFacts{}},
	{domain: "permissions/delegation", purpose: "delegation permission checks", facts: DelegationFacts{}},
	{domain: "cost/budgets", purpose: "session and account cost budget routing", facts: CostFacts{}},
	{domain: "cost/rate_limits", purpose: "provider rate-limit routing", facts: RateLimitFacts{}},
	{domain: "session/compaction", purpose: "session-aware compaction routing", facts: CompactionFacts{}},
	{domain: "session/memory", purpose: "session memory persistence routing", facts: SessionMemoryFacts{}},
	{domain: "session/prompt_assembly", purpose: "prompt assembly routing", facts: PromptAssemblyFacts{}},
	{domain: "bootstrap/phases", purpose: "workflow phase bootstrap", facts: PhaseFacts{}},
	{domain: "runtime/timeouts", purpose: "tool timeout resolution", facts: TimeoutFacts{}},
	{domain: "runtime/concurrency", purpose: "worker pool and concurrency routing", facts: PoolFacts{}},
	{domain: "runtime/conditions", purpose: "runtime condition handling", facts: ConditionFacts{}},
	{domain: "autonomous/modes", purpose: "autonomous mode resolution", facts: ModeFacts{}},
	{domain: "autonomous/sessions", purpose: "autonomous task intake routing", facts: TaskIntakeFacts{}},
	{domain: "autonomous/channels", purpose: "autonomous channel routing", facts: ChannelFacts{}},
	{domain: "session/interview", purpose: "interview mode routing", facts: InterviewFacts{}},
	{domain: "session/intelligence", purpose: "session intelligence routing", facts: IntelligenceFacts{}},
}

func FactContracts() []FactContract {
	contracts := make([]FactContract, 0, len(factContractSpecs))
	for _, spec := range factContractSpecs {
		contracts = append(contracts, FactContract{
			Domain:  spec.domain,
			Purpose: spec.purpose,
			Facts:   spec.contractFields(),
		})
	}
	sort.Slice(contracts, func(i, j int) bool {
		return contracts[i].Domain < contracts[j].Domain
	})
	return contracts
}

func FactContractsForDomain(domain string) []FactContract {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return FactContracts()
	}
	contracts := FactContracts()
	filtered := make([]FactContract, 0, 1)
	for _, contract := range contracts {
		if contract.Domain == domain {
			filtered = append(filtered, contract)
		}
	}
	return filtered
}

func (s factContractSpec) contractFields() []FactField {
	if len(s.fields) > 0 {
		out := append([]FactField(nil), s.fields...)
		sort.Slice(out, func(i, j int) bool {
			return out[i].Key < out[j].Key
		})
		return out
	}
	return factFields(s.facts)
}

func fields(values ...FactField) []FactField {
	return values
}

func field(key, typ string) FactField {
	return FactField{Key: key, Type: typ}
}

func factFields(facts any) []FactField {
	rt := reflect.TypeOf(facts)
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return nil
	}
	fields := make([]FactField, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		key := field.Tag.Get("arb")
		if key == "" {
			continue
		}
		fields = append(fields, FactField{
			Key:  key,
			Type: field.Type.String(),
		})
	}
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].Key < fields[j].Key
	})
	return fields
}
