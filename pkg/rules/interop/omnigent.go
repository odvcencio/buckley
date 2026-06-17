package interop

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	StatusSupported   = "supported"
	StatusPartial     = "partial"
	StatusUnsupported = "unsupported"
)

type OmnigentPolicy struct {
	Source     string         `json:"source"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Handler    string         `json:"handler,omitempty"`
	Params     map[string]any `json:"params,omitempty"`
	On         []string       `json:"on,omitempty"`
	Condition  map[string]any `json:"condition,omitempty"`
	Actions    []string       `json:"actions,omitempty"`
	SetLabels  []string       `json:"set_labels,omitempty"`
	AskTimeout *int           `json:"ask_timeout,omitempty"`
}

type ArbiterTarget struct {
	Domain   string   `json:"domain"`
	Strategy string   `json:"strategy,omitempty"`
	Action   string   `json:"action,omitempty"`
	Facts    []string `json:"facts,omitempty"`
}

type PolicyMapping struct {
	Policy   OmnigentPolicy  `json:"policy"`
	Status   string          `json:"status"`
	Targets  []ArbiterTarget `json:"targets,omitempty"`
	Notes    []string        `json:"notes,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
}

type Summary struct {
	Policies    int `json:"policies"`
	Supported   int `json:"supported"`
	Partial     int `json:"partial"`
	Unsupported int `json:"unsupported"`
}

type ImportResult struct {
	Mappings []PolicyMapping `json:"mappings"`
	Summary  Summary         `json:"summary"`
}

func ImportOmnigentPolicies(data []byte) (*ImportResult, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing omnigent YAML: %w", err)
	}
	root := documentRoot(&doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("omnigent policy config must be a YAML mapping")
	}

	policies, err := extractPolicies(root)
	if err != nil {
		return nil, err
	}
	if len(policies) == 0 {
		return nil, fmt.Errorf("omnigent config has no policies or guardrails.policies block")
	}

	result := &ImportResult{Mappings: make([]PolicyMapping, 0, len(policies))}
	for _, policy := range policies {
		mapping := MapOmnigentPolicy(policy)
		result.Mappings = append(result.Mappings, mapping)
		result.Summary.Policies++
		switch mapping.Status {
		case StatusSupported:
			result.Summary.Supported++
		case StatusPartial:
			result.Summary.Partial++
		default:
			result.Summary.Unsupported++
		}
	}
	return result, nil
}

func MapOmnigentPolicy(policy OmnigentPolicy) PolicyMapping {
	mapping := PolicyMapping{Policy: policy}
	if strings.TrimSpace(policy.Type) != "function" {
		mapping.Status = StatusUnsupported
		mapping.Warnings = append(mapping.Warnings, "Buckley only maps Omnigent function policies into Arbiter")
		return mapping
	}
	if strings.TrimSpace(policy.Handler) == "" {
		mapping.Status = StatusUnsupported
		mapping.Warnings = append(mapping.Warnings, "policy has no handler or function path")
		return mapping
	}

	handler := normalizeHandler(policy.Handler)
	switch {
	case strings.HasSuffix(handler, "safety.max_tool_calls_per_session"):
		mapping.Status = StatusSupported
		mapping.Targets = append(mapping.Targets, ArbiterTarget{
			Domain: "tool_budget",
			Action: "halt",
			Facts:  []string{"agent.tool_calls", "agent.max_tool_calls"},
		})
		if limit, ok := intParam(policy.Params, "limit"); ok {
			mapping.Notes = append(mapping.Notes, fmt.Sprintf("set agent.max_tool_calls to %d before evaluating tool_budget", limit))
		}
	case strings.HasSuffix(handler, "safety.ask_on_os_tools"):
		mapping.Status = StatusPartial
		mapping.Targets = append(mapping.Targets, ArbiterTarget{
			Domain:   "approval",
			Strategy: "approval_gate",
			Action:   "ask",
			Facts:    []string{"approval.mode", "risk.level", "tool.name"},
		})
		mapping.Notes = append(mapping.Notes, "represent as an approval.arb override that asks on Buckley file and shell tool names")
	case strings.HasSuffix(handler, "safety.enforce_sandbox"):
		mapping.Status = StatusPartial
		mapping.Targets = append(mapping.Targets, ArbiterTarget{
			Domain:   "permissions/sandbox",
			Strategy: "sandbox_level",
			Facts:    []string{"tool", "role", "risk_score"},
		})
		mapping.Notes = append(mapping.Notes, "Buckley can choose sandbox level/network through Arbiter; path/env passthrough lists need runtime adapter support")
	case strings.HasSuffix(handler, "safety.block_skills"):
		mapping.Status = StatusUnsupported
		mapping.Targets = append(mapping.Targets, ArbiterTarget{
			Domain: "role_permissions",
			Action: "restrict",
			Facts:  []string{"role", "tier"},
		})
		mapping.Warnings = append(mapping.Warnings, "Buckley does not yet expose skill-load facts to Arbiter")
	case strings.HasSuffix(handler, "safety.deny_pii_in_llm_request"):
		mapping.Status = StatusUnsupported
		mapping.Warnings = append(mapping.Warnings, "requires a prompt/security Arbiter domain with PII detector facts")
	case strings.HasSuffix(handler, "cost.cost_budget"):
		mapping.Status = StatusPartial
		mapping.Targets = append(mapping.Targets, ArbiterTarget{
			Domain:   "cost/budgets",
			Strategy: "cost_policy",
			Action:   "halt|downgrade_model|warn|allow",
			Facts:    []string{"session_spend", "session_budget", "budget_util", "current_model_cost"},
		})
		mapping.Notes = append(mapping.Notes, "Buckley has a cost strategy, but Omnigent soft-threshold ASK semantics need an Arbiter override")
	case strings.HasSuffix(handler, "cost.user_daily_cost_budget"):
		mapping.Status = StatusPartial
		mapping.Targets = append(mapping.Targets, ArbiterTarget{
			Domain:   "cost/budgets",
			Strategy: "cost_policy",
			Facts:    []string{"daily_spend", "daily_budget", "budget_util", "current_model_cost"},
		})
		mapping.Notes = append(mapping.Notes, "Buckley already carries daily cost facts; the embedded cost strategy only gates session spend today")
	case strings.HasSuffix(handler, "github.github_policy"), strings.HasSuffix(handler, "google.gdrive_policy"):
		mapping.Status = StatusUnsupported
		mapping.Warnings = append(mapping.Warnings, "external service scope policies need service-specific Arbiter facts")
	case strings.HasSuffix(handler, "function.make_fixed_action_callable"):
		mapping = mapFixedActionPolicy(mapping)
	case strings.HasSuffix(handler, "inner.nessie.policies.blast_radius"):
		mapping.Status = StatusPartial
		mapping.Targets = append(mapping.Targets, ArbiterTarget{
			Domain: "risk",
			Action: "block|pause|allow",
			Facts:  []string{"command", "is_git_op", "is_rm_recursive"},
		})
		mapping.Notes = append(mapping.Notes, "Buckley risk.arb covers destructive git, rm -r, and dangerous database operations")
	case strings.HasSuffix(handler, "inner.nessie.policies.spawn_bounds"):
		mapping.Status = StatusPartial
		mapping.Targets = append(mapping.Targets, ArbiterTarget{
			Domain: "spawning",
			Facts:  []string{"task.subtask_count", "spawn.depth"},
		}, ArbiterTarget{
			Domain:   "runtime/concurrency",
			Strategy: "pool_policy",
		})
		mapping.Notes = append(mapping.Notes, "map dispatch bounds to spawning/runtime concurrency rules")
	case strings.HasSuffix(handler, "inner.nessie.policies.headless_subagent_purpose_guard"):
		mapping.Status = StatusPartial
		mapping.Targets = append(mapping.Targets, ArbiterTarget{
			Domain: "role_permissions",
			Action: "restrict",
			Facts:  []string{"role", "tier"},
		})
		mapping.Notes = append(mapping.Notes, "needs purpose facts if allowed purposes should be enforced exactly")
	default:
		mapping.Status = StatusUnsupported
		mapping.Warnings = append(mapping.Warnings, "custom Python policy; implement the equivalent as an Arbiter domain or user override")
	}

	if len(policy.Condition) > 0 {
		mapping.Warnings = append(mapping.Warnings, "Omnigent label-gated condition has no Buckley Arbiter fact bridge yet")
	}
	if len(policy.SetLabels) > 0 {
		mapping.Warnings = append(mapping.Warnings, "Omnigent label writes are not represented in Buckley session state yet")
	}
	if len(policy.Actions) > 0 && mapping.Status == StatusUnsupported {
		mapping.Notes = append(mapping.Notes, "declared action allow-list: "+strings.Join(policy.Actions, ", "))
	}
	return mapping
}

func RenderText(result *ImportResult) string {
	if result == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Omnigent policy interop: %d policies (%d supported, %d partial, %d unsupported)\n",
		result.Summary.Policies, result.Summary.Supported, result.Summary.Partial, result.Summary.Unsupported)
	for _, mapping := range result.Mappings {
		p := mapping.Policy
		fmt.Fprintf(&b, "\n- %s [%s] %s\n", p.Name, mapping.Status, p.Source)
		fmt.Fprintf(&b, "  handler: %s\n", p.Handler)
		for _, target := range mapping.Targets {
			line := "  arbiter: " + target.Domain
			if target.Strategy != "" {
				line += " / " + target.Strategy
			}
			if target.Action != "" {
				line += " -> " + target.Action
			}
			b.WriteString(line + "\n")
			if len(target.Facts) > 0 {
				fmt.Fprintf(&b, "  facts: %s\n", strings.Join(target.Facts, ", "))
			}
		}
		if len(p.Params) > 0 {
			fmt.Fprintf(&b, "  params: %s\n", formatParams(p.Params))
		}
		for _, note := range mapping.Notes {
			fmt.Fprintf(&b, "  note: %s\n", note)
		}
		for _, warning := range mapping.Warnings {
			fmt.Fprintf(&b, "  warning: %s\n", warning)
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func JSON(result *ImportResult) ([]byte, error) {
	return json.MarshalIndent(result, "", "  ")
}

func mapFixedActionPolicy(mapping PolicyMapping) PolicyMapping {
	action, _ := stringParam(mapping.Policy.Params, "action")
	onTools := stringSliceParam(mapping.Policy.Params, "on_tools")
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "deny":
		mapping.Status = StatusPartial
		mapping.Targets = append(mapping.Targets, ArbiterTarget{
			Domain: "risk",
			Action: "block",
			Facts:  []string{"command", "tool.name"},
		})
		mapping.Notes = append(mapping.Notes, "fixed DENY with tool filters maps to a custom risk.arb rule")
	case "ask":
		mapping.Status = StatusPartial
		mapping.Targets = append(mapping.Targets, ArbiterTarget{
			Domain:   "approval",
			Strategy: "approval_gate",
			Action:   "ask",
			Facts:    []string{"approval.mode", "risk.level", "tool.name"},
		})
		mapping.Notes = append(mapping.Notes, "fixed ASK with tool filters maps to an approval.arb override")
	case "allow":
		mapping.Status = StatusPartial
		mapping.Targets = append(mapping.Targets, ArbiterTarget{
			Domain: "risk",
			Action: "allow",
			Facts:  []string{"tool.name"},
		})
		mapping.Notes = append(mapping.Notes, "fixed ALLOW is usually unnecessary unless paired with label writes")
	default:
		mapping.Status = StatusUnsupported
		mapping.Warnings = append(mapping.Warnings, "fixed action policy missing action argument")
	}
	if len(onTools) > 0 {
		mapping.Notes = append(mapping.Notes, "tool filter: "+strings.Join(onTools, ", "))
	}
	return mapping
}

func extractPolicies(root *yaml.Node) ([]OmnigentPolicy, error) {
	var policies []OmnigentPolicy
	if node := mappingValue(root, "policies"); node != nil {
		extracted, err := decodePolicyBlock("policies", node)
		if err != nil {
			return nil, err
		}
		policies = append(policies, extracted...)
	}
	if guardrails := mappingValue(root, "guardrails"); guardrails != nil {
		if node := mappingValue(guardrails, "policies"); node != nil {
			extracted, err := decodePolicyBlock("guardrails.policies", node)
			if err != nil {
				return nil, err
			}
			policies = append(policies, extracted...)
		}
	}
	return policies, nil
}

func decodePolicyBlock(source string, node *yaml.Node) ([]OmnigentPolicy, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping", source)
	}
	policies := make([]OmnigentPolicy, 0, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		name := node.Content[i].Value
		var raw map[string]any
		if err := node.Content[i+1].Decode(&raw); err != nil {
			return nil, fmt.Errorf("decoding policy %q: %w", name, err)
		}
		policies = append(policies, normalizePolicy(source, name, raw))
	}
	return policies, nil
}

func normalizePolicy(source, name string, raw map[string]any) OmnigentPolicy {
	policy := OmnigentPolicy{
		Source:    source,
		Name:      name,
		Type:      stringValue(raw["type"]),
		Handler:   stringValue(raw["handler"]),
		Params:    stringMap(raw["factory_params"]),
		On:        stringSlice(raw["on"]),
		Condition: stringMap(raw["condition"]),
		Actions:   stringSlice(raw["action"]),
		SetLabels: stringSlice(raw["set_labels"]),
	}
	if policy.Params == nil {
		policy.Params = map[string]any{}
	}
	if timeout, ok := intValue(raw["ask_timeout"]); ok {
		policy.AskTimeout = &timeout
	}
	if function, ok := raw["function"]; ok {
		path, args := functionRef(function)
		if path != "" {
			policy.Handler = path
		}
		for k, v := range args {
			policy.Params[k] = v
		}
	}
	if len(policy.Params) == 0 {
		policy.Params = nil
	}
	return policy
}

func documentRoot(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}
	return node
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func functionRef(value any) (string, map[string]any) {
	switch v := value.(type) {
	case string:
		return v, nil
	case map[string]any:
		return stringValue(v["path"]), stringMap(v["arguments"])
	case map[any]any:
		m := map[string]any{}
		for k, val := range v {
			if s, ok := k.(string); ok {
				m[s] = val
			}
		}
		return stringValue(m["path"]), stringMap(m["arguments"])
	default:
		return "", nil
	}
}

func normalizeHandler(handler string) string {
	handler = strings.TrimSpace(handler)
	handler = strings.TrimPrefix(handler, "omnigent.policies.builtins.")
	handler = strings.TrimPrefix(handler, "omnigent.")
	return handler
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func stringMap(value any) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		if len(v) == 0 {
			return nil
		}
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = normalizeAny(val)
		}
		return out
	case map[any]any:
		if len(v) == 0 {
			return nil
		}
		out := make(map[string]any, len(v))
		for k, val := range v {
			if s, ok := k.(string); ok {
				out[s] = normalizeAny(val)
			}
		}
		return out
	default:
		return nil
	}
}

func normalizeAny(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return stringMap(v)
	case map[any]any:
		return stringMap(v)
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, normalizeAny(item))
		}
		return out
	default:
		return value
	}
}

func stringSlice(value any) []string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{strings.TrimSpace(v)}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := stringValue(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		i, err := strconv.Atoi(v.String())
		return i, err == nil
	default:
		return 0, false
	}
}

func intParam(params map[string]any, key string) (int, bool) {
	if params == nil {
		return 0, false
	}
	return intValue(params[key])
}

func stringParam(params map[string]any, key string) (string, bool) {
	if params == nil {
		return "", false
	}
	value := stringValue(params[key])
	return value, value != ""
}

func stringSliceParam(params map[string]any, key string) []string {
	if params == nil {
		return nil
	}
	return stringSlice(params[key])
}

func formatParams(params map[string]any) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+formatValue(params[key]))
	}
	return strings.Join(parts, ", ")
}

func formatValue(value any) string {
	switch v := value.(type) {
	case string:
		if strings.ContainsAny(v, "\n\r\t,") {
			data, err := json.Marshal(v)
			if err == nil {
				return string(data)
			}
		}
		return v
	case []any, map[string]any:
		data, err := json.Marshal(v)
		if err == nil {
			return string(data)
		}
	}
	return fmt.Sprint(value)
}
