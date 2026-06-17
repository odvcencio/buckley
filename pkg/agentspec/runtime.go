package agentspec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/config"
)

const MaxInstructionFileChars = 12000

type RuntimeProfile struct {
	SourcePath       string
	Spec             *Spec
	InstructionFiles []InstructionFileContent
}

type InstructionFileContent struct {
	Path    string
	Content string
}

func LoadRuntimeProfile(path string) (*RuntimeProfile, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("agent spec path is required")
	}
	spec, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	if diagnostics := spec.Validate(); hasErrors(diagnostics) {
		return nil, fmt.Errorf("invalid agent spec %s:\n%s", path, formatDiagnostics(diagnostics))
	}
	files, err := loadInstructionFiles(spec.Instructions.Files)
	if err != nil {
		return nil, err
	}
	return &RuntimeProfile{
		SourcePath:       path,
		Spec:             spec,
		InstructionFiles: files,
	}, nil
}

func (p *RuntimeProfile) ApplyToConfig(cfg *config.Config) {
	if p == nil {
		return
	}
	ApplyToConfig(cfg, p.Spec)
}

func ApplyToConfig(cfg *config.Config, spec *Spec) {
	if cfg == nil || spec == nil {
		return
	}
	if spec.Models.Chat != "" {
		applyModelOverride(cfg, &cfg.Models.Execution, spec.Models.Chat)
	}
	if spec.Models.Planning != "" {
		applyModelOverride(cfg, &cfg.Models.Planning, spec.Models.Planning)
	}
	if spec.Models.Execution != "" {
		applyModelOverride(cfg, &cfg.Models.Execution, spec.Models.Execution)
	}
	if spec.Models.Review != "" {
		applyModelOverride(cfg, &cfg.Models.Review, spec.Models.Review)
	}
	if strings.TrimSpace(spec.Models.Reasoning) != "" {
		cfg.Models.Reasoning = strings.TrimSpace(spec.Models.Reasoning)
	}

	if spec.Persona != "" {
		cfg.Personality.Enabled = true
		cfg.Personality.DefaultPersona = spec.Persona
	}
	if spec.Policies.ApprovalMode != "" {
		cfg.Approval.Mode = spec.Policies.ApprovalMode
	}
	if len(spec.Tools.Allow) > 0 {
		cfg.Approval.AllowedTools = appendUnique(cfg.Approval.AllowedTools, spec.Tools.Allow...)
	}
	if len(spec.Tools.Deny) > 0 {
		cfg.Approval.DeniedTools = appendUnique(cfg.Approval.DeniedTools, spec.Tools.Deny...)
	}
	if spec.Sandbox.Mode != "" {
		cfg.Sandbox.Mode = spec.Sandbox.Mode
		if spec.Sandbox.Mode == "disabled" {
			cfg.Sandbox.AllowUnsafe = true
		}
	}
	if spec.Sandbox.Network != nil {
		cfg.Sandbox.AllowNetwork = *spec.Sandbox.Network
		cfg.Approval.AllowNetwork = *spec.Sandbox.Network
	}
	if len(spec.Sandbox.ReadPaths) > 0 {
		cfg.Sandbox.AllowedPaths = appendUnique(cfg.Sandbox.AllowedPaths, spec.Sandbox.ReadPaths...)
	}
	if len(spec.Sandbox.WritePaths) > 0 {
		cfg.Sandbox.AllowedPaths = appendUnique(cfg.Sandbox.AllowedPaths, spec.Sandbox.WritePaths...)
		cfg.Approval.TrustedPaths = appendUnique(cfg.Approval.TrustedPaths, spec.Sandbox.WritePaths...)
	}
}

func (p *RuntimeProfile) PromptSection() string {
	if p == nil || p.Spec == nil {
		return ""
	}
	return PromptSection(p.Spec, p.InstructionFiles)
}

func PromptSection(spec *Spec, files []InstructionFileContent) string {
	if spec == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Follow this Buckley agent profile for this session.\n")
	fmt.Fprintf(&b, "- Agent: %s\n", emptyDefault(spec.Name, "(unnamed)"))
	if spec.Summary != "" {
		fmt.Fprintf(&b, "- Summary: %s\n", spec.Summary)
	}
	if spec.Persona != "" {
		fmt.Fprintf(&b, "- Persona: %s\n", spec.Persona)
	}
	if models := modelSummary(spec.Models); models != "" {
		fmt.Fprintf(&b, "- Models: %s\n", models)
	}
	if spec.Tools.Tier != "" {
		fmt.Fprintf(&b, "- Tool tier: %s\n", spec.Tools.Tier)
	}
	if spec.Policies.ApprovalMode != "" {
		fmt.Fprintf(&b, "- Approval mode: %s\n", spec.Policies.ApprovalMode)
	}
	if spec.Policies.MaxToolCalls > 0 {
		fmt.Fprintf(&b, "- Max tool calls: %d\n", spec.Policies.MaxToolCalls)
	}
	if spec.Sandbox.Mode != "" {
		fmt.Fprintf(&b, "- Sandbox mode: %s\n", spec.Sandbox.Mode)
	}
	if len(spec.Skills) > 0 {
		fmt.Fprintf(&b, "- Skills: %s\n", strings.Join(spec.Skills, ", "))
	}
	if spec.Instructions.Prompt != "" {
		b.WriteString("\nAgent Instructions:\n")
		b.WriteString(strings.TrimSpace(spec.Instructions.Prompt))
		b.WriteString("\n")
	}
	if len(files) > 0 {
		b.WriteString("\nAgent Instruction Files:\n")
		for _, file := range files {
			label := file.Path
			if base := filepath.Base(file.Path); base != "" {
				label = base
			}
			fmt.Fprintf(&b, "\n## %s\n", label)
			b.WriteString(strings.TrimSpace(file.Content))
			b.WriteString("\n")
		}
	}
	if len(spec.Tools.Allow) > 0 || len(spec.Tools.Deny) > 0 || len(spec.Tools.MCP) > 0 {
		b.WriteString("\nTool Policy:\n")
		writeStringList(&b, "Allow", spec.Tools.Allow)
		writeStringList(&b, "Deny", spec.Tools.Deny)
		writeStringList(&b, "MCP", spec.Tools.MCP)
	}
	if len(spec.Policies.Domains) > 0 || len(spec.Policies.RulePacks) > 0 {
		b.WriteString("\nPolicy Domains:\n")
		writeStringList(&b, "Domains", spec.Policies.Domains)
		if len(spec.Policies.RulePacks) > 0 {
			fmt.Fprintf(&b, "- Rule packs: %d configured\n", len(spec.Policies.RulePacks))
		}
	}
	return strings.TrimSpace(b.String())
}

func loadInstructionFiles(paths []string) ([]InstructionFileContent, error) {
	files := make([]InstructionFileContent, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading agent instruction file %s: %w", path, err)
		}
		content := strings.TrimSpace(string(data))
		if len(content) > MaxInstructionFileChars {
			content = content[:MaxInstructionFileChars] + "\n[truncated]"
		}
		files = append(files, InstructionFileContent{Path: path, Content: content})
	}
	return files, nil
}

func applyModelOverride(cfg *config.Config, target *string, modelID string) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || target == nil {
		return
	}
	normalized, effort := config.SplitReasoningSuffix(modelID)
	if normalized != "" {
		modelID = normalized
	}
	if effort != "" {
		cfg.Models.Reasoning = effort
	}
	*target = modelID
}

func appendUnique(existing []string, values ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(existing)+len(values))
	for _, value := range existing {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func writeStringList(b *strings.Builder, label string, values []string) {
	if len(values) == 0 {
		return
	}
	clean := append([]string{}, values...)
	sort.Strings(clean)
	fmt.Fprintf(b, "- %s: %s\n", label, strings.Join(clean, ", "))
}

func formatDiagnostics(diagnostics []Diagnostic) string {
	var b strings.Builder
	for _, diag := range diagnostics {
		fmt.Fprintf(&b, "- %s %s: %s\n", diag.Severity, diag.Path, diag.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}
