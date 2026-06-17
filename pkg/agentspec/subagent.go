package agentspec

import (
	"fmt"
	"sort"
	"strings"
)

func SubagentNames(spec *Spec) []string {
	if spec == nil || len(spec.Subagents) == 0 {
		return nil
	}
	names := make([]string, 0, len(spec.Subagents))
	for _, sub := range spec.Subagents {
		if name := strings.TrimSpace(sub.Name); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func FindSubagent(spec *Spec, name string) (SubagentSpec, bool) {
	name = strings.TrimSpace(name)
	if spec == nil || name == "" {
		return SubagentSpec{}, false
	}
	for _, sub := range spec.Subagents {
		if strings.TrimSpace(sub.Name) == name {
			return sub, true
		}
	}
	return SubagentSpec{}, false
}

func (p *RuntimeProfile) SubagentProfile(name string) (*RuntimeProfile, error) {
	if p == nil || p.Spec == nil {
		return nil, fmt.Errorf("agent profile is required")
	}
	sub, ok := FindSubagent(p.Spec, name)
	if !ok {
		names := SubagentNames(p.Spec)
		if len(names) == 0 {
			return nil, fmt.Errorf("subagent %q not found; agent spec defines no subagents", strings.TrimSpace(name))
		}
		return nil, fmt.Errorf("subagent %q not found; available: %s", strings.TrimSpace(name), strings.Join(names, ", "))
	}

	spec := cloneSpec(p.Spec)
	spec.Name = strings.Trim(strings.TrimSpace(spec.Name)+"/"+strings.TrimSpace(sub.Name), "/")
	if strings.TrimSpace(sub.Persona) != "" {
		spec.Persona = strings.TrimSpace(sub.Persona)
	}
	if strings.TrimSpace(sub.Model) != "" {
		spec.Models.Chat = strings.TrimSpace(sub.Model)
		spec.Models.Execution = strings.TrimSpace(sub.Model)
	}
	if strings.TrimSpace(sub.ToolTier) != "" {
		spec.Tools.Tier = strings.TrimSpace(sub.ToolTier)
	}
	if len(sub.Skills) > 0 {
		spec.Skills = appendUnique(spec.Skills, sub.Skills...)
	}
	spec.Policies = mergePolicy(spec.Policies, sub.Policies)
	if strings.TrimSpace(sub.Instructions) != "" {
		spec.Instructions.Prompt = appendPrompt(spec.Instructions.Prompt, "Subagent Instructions:\n"+strings.TrimSpace(sub.Instructions))
	}
	spec.Subagents = nil

	return &RuntimeProfile{
		SourcePath:       p.SourcePath,
		Spec:             spec,
		InstructionFiles: append([]InstructionFileContent(nil), p.InstructionFiles...),
	}, nil
}

func cloneSpec(spec *Spec) *Spec {
	if spec == nil {
		return nil
	}
	out := *spec
	out.Instructions.Files = append([]string(nil), spec.Instructions.Files...)
	out.Runtime.Command = append([]string(nil), spec.Runtime.Command...)
	out.Runtime.Env = cloneStringMap(spec.Runtime.Env)
	out.Skills = append([]string(nil), spec.Skills...)
	out.Tools.Allow = append([]string(nil), spec.Tools.Allow...)
	out.Tools.Deny = append([]string(nil), spec.Tools.Deny...)
	out.Tools.MCP = append([]string(nil), spec.Tools.MCP...)
	out.Policies.Domains = append([]string(nil), spec.Policies.Domains...)
	out.Policies.RulePacks = append([]RulePackRef(nil), spec.Policies.RulePacks...)
	out.Sandbox.ReadPaths = append([]string(nil), spec.Sandbox.ReadPaths...)
	out.Sandbox.WritePaths = append([]string(nil), spec.Sandbox.WritePaths...)
	out.Sandbox.EnvPassthrough = append([]string(nil), spec.Sandbox.EnvPassthrough...)
	out.Subagents = append([]SubagentSpec(nil), spec.Subagents...)
	for i := range out.Subagents {
		out.Subagents[i].Skills = append([]string(nil), spec.Subagents[i].Skills...)
		out.Subagents[i].Policies.Domains = append([]string(nil), spec.Subagents[i].Policies.Domains...)
		out.Subagents[i].Policies.RulePacks = append([]RulePackRef(nil), spec.Subagents[i].Policies.RulePacks...)
	}
	out.Terminals = append([]TerminalSpec(nil), spec.Terminals...)
	for i := range out.Terminals {
		out.Terminals[i].Command = append([]string(nil), spec.Terminals[i].Command...)
		out.Terminals[i].Env = cloneStringMap(spec.Terminals[i].Env)
		out.Terminals[i].Sandbox.ReadPaths = append([]string(nil), spec.Terminals[i].Sandbox.ReadPaths...)
		out.Terminals[i].Sandbox.WritePaths = append([]string(nil), spec.Terminals[i].Sandbox.WritePaths...)
		out.Terminals[i].Sandbox.EnvPassthrough = append([]string(nil), spec.Terminals[i].Sandbox.EnvPassthrough...)
	}
	out.Labels = cloneStringMap(spec.Labels)
	out.Metadata = cloneStringMap(spec.Metadata)
	return &out
}

func mergePolicy(base, override PolicySpec) PolicySpec {
	if strings.TrimSpace(override.ApprovalMode) != "" {
		base.ApprovalMode = strings.TrimSpace(override.ApprovalMode)
	}
	if override.MaxToolCalls > 0 {
		base.MaxToolCalls = override.MaxToolCalls
	}
	if len(override.Domains) > 0 {
		base.Domains = appendUnique(base.Domains, override.Domains...)
	}
	if len(override.RulePacks) > 0 {
		base.RulePacks = append(base.RulePacks, override.RulePacks...)
	}
	return base
}

func appendPrompt(existing, addition string) string {
	existing = strings.TrimSpace(existing)
	addition = strings.TrimSpace(addition)
	switch {
	case existing == "":
		return addition
	case addition == "":
		return existing
	default:
		return existing + "\n\n" + addition
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
