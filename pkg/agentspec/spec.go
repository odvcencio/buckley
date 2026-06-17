package agentspec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	Version = "buckley.agent/v1"

	SeverityError   = "error"
	SeverityWarning = "warning"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type Diagnostic struct {
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type Spec struct {
	Version      string            `yaml:"version" json:"version"`
	Name         string            `yaml:"name" json:"name"`
	Summary      string            `yaml:"summary,omitempty" json:"summary,omitempty"`
	Persona      string            `yaml:"persona,omitempty" json:"persona,omitempty"`
	Instructions InstructionSpec   `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	Models       ModelSpec         `yaml:"models,omitempty" json:"models,omitempty"`
	Runtime      RuntimeSpec       `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Skills       []string          `yaml:"skills,omitempty" json:"skills,omitempty"`
	Tools        ToolSpec          `yaml:"tools,omitempty" json:"tools,omitempty"`
	Policies     PolicySpec        `yaml:"policies,omitempty" json:"policies,omitempty"`
	Sandbox      SandboxSpec       `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
	Subagents    []SubagentSpec    `yaml:"subagents,omitempty" json:"subagents,omitempty"`
	Terminals    []TerminalSpec    `yaml:"terminals,omitempty" json:"terminals,omitempty"`
	Labels       map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Metadata     map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`
}

type InstructionSpec struct {
	Prompt string   `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	Files  []string `yaml:"files,omitempty" json:"files,omitempty"`
}

type ModelSpec struct {
	Chat      string `yaml:"chat,omitempty" json:"chat,omitempty"`
	Planning  string `yaml:"planning,omitempty" json:"planning,omitempty"`
	Execution string `yaml:"execution,omitempty" json:"execution,omitempty"`
	Review    string `yaml:"review,omitempty" json:"review,omitempty"`
	Reasoning string `yaml:"reasoning,omitempty" json:"reasoning,omitempty"`
}

type RuntimeSpec struct {
	Driver  string            `yaml:"driver,omitempty" json:"driver,omitempty"`
	Adapter string            `yaml:"adapter,omitempty" json:"adapter,omitempty"`
	Command []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

type ToolSpec struct {
	Tier  string   `yaml:"tier,omitempty" json:"tier,omitempty"`
	Allow []string `yaml:"allow,omitempty" json:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty" json:"deny,omitempty"`
	MCP   []string `yaml:"mcp,omitempty" json:"mcp,omitempty"`
}

type PolicySpec struct {
	ApprovalMode string        `yaml:"approval_mode,omitempty" json:"approval_mode,omitempty"`
	MaxToolCalls int           `yaml:"max_tool_calls,omitempty" json:"max_tool_calls,omitempty"`
	Domains      []string      `yaml:"domains,omitempty" json:"domains,omitempty"`
	RulePacks    []RulePackRef `yaml:"rule_packs,omitempty" json:"rule_packs,omitempty"`
}

type RulePackRef struct {
	Name    string   `yaml:"name,omitempty" json:"name,omitempty"`
	Path    string   `yaml:"path,omitempty" json:"path,omitempty"`
	Scope   string   `yaml:"scope,omitempty" json:"scope,omitempty"`
	Domains []string `yaml:"domains,omitempty" json:"domains,omitempty"`
}

type SandboxSpec struct {
	Mode           string   `yaml:"mode,omitempty" json:"mode,omitempty"`
	Network        *bool    `yaml:"network,omitempty" json:"network,omitempty"`
	ReadPaths      []string `yaml:"read_paths,omitempty" json:"read_paths,omitempty"`
	WritePaths     []string `yaml:"write_paths,omitempty" json:"write_paths,omitempty"`
	EnvPassthrough []string `yaml:"env_passthrough,omitempty" json:"env_passthrough,omitempty"`
}

type SubagentSpec struct {
	Name     string     `yaml:"name" json:"name"`
	Persona  string     `yaml:"persona,omitempty" json:"persona,omitempty"`
	Model    string     `yaml:"model,omitempty" json:"model,omitempty"`
	ToolTier string     `yaml:"tool_tier,omitempty" json:"tool_tier,omitempty"`
	Skills   []string   `yaml:"skills,omitempty" json:"skills,omitempty"`
	Policies PolicySpec `yaml:"policies,omitempty" json:"policies,omitempty"`
}

type TerminalSpec struct {
	Name    string            `yaml:"name" json:"name"`
	Command []string          `yaml:"command" json:"command"`
	WorkDir string            `yaml:"workdir,omitempty" json:"workdir,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Sandbox SandboxSpec       `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
}

func LoadFile(path string) (*Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading agent spec %s: %w", path, err)
	}
	spec, err := Parse(data)
	if err != nil {
		return nil, err
	}
	baseDir := filepath.Dir(path)
	spec.Instructions.Files = cleanRelativePaths(baseDir, spec.Instructions.Files)
	for i := range spec.Policies.RulePacks {
		if spec.Policies.RulePacks[i].Path != "" && !filepath.IsAbs(spec.Policies.RulePacks[i].Path) {
			spec.Policies.RulePacks[i].Path = filepath.Clean(filepath.Join(baseDir, spec.Policies.RulePacks[i].Path))
		}
	}
	return spec, nil
}

func Parse(data []byte) (*Spec, error) {
	var spec Spec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing agent spec: %w", err)
	}
	return &spec, nil
}

func (s *Spec) Validate() []Diagnostic {
	if s == nil {
		return []Diagnostic{{Severity: SeverityError, Path: "$", Message: "spec is nil"}}
	}
	var d diagnostics
	d.require(s.Version != "", "version", "version is required")
	if s.Version != "" {
		d.require(s.Version == Version, "version", fmt.Sprintf("unsupported version %q; expected %q", s.Version, Version))
	}
	validateName(&d, "name", s.Name)
	if s.Persona != "" {
		validateIdentifier(&d, "persona", s.Persona)
	}

	validateUniqueStrings(&d, "skills", s.Skills)
	validateStringList(&d, "tools.allow", s.Tools.Allow)
	validateStringList(&d, "tools.deny", s.Tools.Deny)
	validateStringList(&d, "tools.mcp", s.Tools.MCP)
	validateToolTier(&d, "tools.tier", s.Tools.Tier)
	validateRuntime(&d, s.Runtime)
	validatePolicy(&d, "policies", s.Policies)
	validateSandbox(&d, "sandbox", s.Sandbox)
	validateInstructionFiles(&d, s.Instructions.Files)
	validateLabels(&d, "labels", s.Labels)

	subagentNames := map[string]struct{}{}
	for i, sub := range s.Subagents {
		path := fmt.Sprintf("subagents[%d]", i)
		validateName(&d, path+".name", sub.Name)
		if sub.Name != "" {
			if _, ok := subagentNames[sub.Name]; ok {
				d.add(SeverityError, path+".name", fmt.Sprintf("duplicate subagent %q", sub.Name))
			}
			subagentNames[sub.Name] = struct{}{}
		}
		validateIdentifier(&d, path+".persona", sub.Persona)
		validateToolTier(&d, path+".tool_tier", sub.ToolTier)
		validateUniqueStrings(&d, path+".skills", sub.Skills)
		validatePolicy(&d, path+".policies", sub.Policies)
	}

	terminalNames := map[string]struct{}{}
	for i, terminal := range s.Terminals {
		path := fmt.Sprintf("terminals[%d]", i)
		validateName(&d, path+".name", terminal.Name)
		if terminal.Name != "" {
			if _, ok := terminalNames[terminal.Name]; ok {
				d.add(SeverityError, path+".name", fmt.Sprintf("duplicate terminal %q", terminal.Name))
			}
			terminalNames[terminal.Name] = struct{}{}
		}
		d.require(len(terminal.Command) > 0, path+".command", "terminal command is required")
		validateSandbox(&d, path+".sandbox", terminal.Sandbox)
	}
	return d.items
}

func (s *Spec) Valid() bool {
	for _, d := range s.Validate() {
		if d.Severity == SeverityError {
			return false
		}
	}
	return true
}

func RenderText(spec *Spec, diagnostics []Diagnostic) string {
	if spec == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Agent: %s\n", emptyDefault(spec.Name, "(unnamed)"))
	fmt.Fprintf(&b, "Version: %s\n", emptyDefault(spec.Version, "(missing)"))
	if spec.Summary != "" {
		fmt.Fprintf(&b, "Summary: %s\n", spec.Summary)
	}
	if spec.Persona != "" {
		fmt.Fprintf(&b, "Persona: %s\n", spec.Persona)
	}
	if runtime := runtimeSummary(spec.Runtime); runtime != "" {
		fmt.Fprintf(&b, "Runtime: %s\n", runtime)
	}
	if models := modelSummary(spec.Models); models != "" {
		fmt.Fprintf(&b, "Models: %s\n", models)
	}
	if len(spec.Skills) > 0 {
		fmt.Fprintf(&b, "Skills: %s\n", strings.Join(spec.Skills, ", "))
	}
	if spec.Tools.Tier != "" {
		fmt.Fprintf(&b, "Tool tier: %s\n", spec.Tools.Tier)
	}
	if len(spec.Policies.Domains) > 0 {
		fmt.Fprintf(&b, "Rule domains: %s\n", strings.Join(spec.Policies.Domains, ", "))
	}
	if len(spec.Policies.RulePacks) > 0 {
		fmt.Fprintf(&b, "Rule packs: %d\n", len(spec.Policies.RulePacks))
	}
	if len(spec.Subagents) > 0 {
		fmt.Fprintf(&b, "Subagents: %d\n", len(spec.Subagents))
	}
	if len(spec.Terminals) > 0 {
		fmt.Fprintf(&b, "Terminals: %d\n", len(spec.Terminals))
	}
	if len(diagnostics) > 0 {
		fmt.Fprintf(&b, "\nDiagnostics:\n")
		for _, diag := range diagnostics {
			fmt.Fprintf(&b, "- %s %s: %s\n", diag.Severity, diag.Path, diag.Message)
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func JSON(spec *Spec, diagnostics []Diagnostic) ([]byte, error) {
	payload := struct {
		Spec        *Spec        `json:"spec"`
		Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
		Valid       bool         `json:"valid"`
	}{
		Spec:        spec,
		Diagnostics: diagnostics,
		Valid:       !hasErrors(diagnostics),
	}
	return json.MarshalIndent(payload, "", "  ")
}

type diagnostics struct {
	items []Diagnostic
}

func (d *diagnostics) add(severity, path, message string) {
	d.items = append(d.items, Diagnostic{Severity: severity, Path: path, Message: message})
}

func (d *diagnostics) require(ok bool, path, message string) {
	if !ok {
		d.add(SeverityError, path, message)
	}
}

func validateName(d *diagnostics, path, value string) {
	d.require(value != "", path, "name is required")
	validateIdentifier(d, path, value)
}

func validateIdentifier(d *diagnostics, path, value string) {
	if value == "" {
		return
	}
	if !identifierPattern.MatchString(value) {
		d.add(SeverityError, path, "must start with a letter or digit and contain only letters, digits, dots, underscores, or dashes")
	}
}

func validateStringList(d *diagnostics, path string, values []string) {
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			d.add(SeverityError, fmt.Sprintf("%s[%d]", path, i), "value must not be empty")
		}
	}
}

func validateUniqueStrings(d *diagnostics, path string, values []string) {
	validateStringList(d, path, values)
	seen := map[string]struct{}{}
	for i, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			d.add(SeverityError, fmt.Sprintf("%s[%d]", path, i), fmt.Sprintf("duplicate value %q", value))
		}
		seen[value] = struct{}{}
	}
}

func validateToolTier(d *diagnostics, path, tier string) {
	if tier == "" {
		return
	}
	switch tier {
	case "read_only", "standard", "full":
	default:
		d.add(SeverityError, path, "tool tier must be read_only, standard, or full")
	}
}

func validateRuntime(d *diagnostics, runtime RuntimeSpec) {
	driver := emptyDefault(runtime.Driver, "buckley")
	switch driver {
	case "buckley":
		if runtime.Adapter != "" {
			d.add(SeverityWarning, "runtime.adapter", "adapter is ignored for buckley driver")
		}
	case "external":
		d.require(runtime.Adapter != "", "runtime.adapter", "external runtime requires an adapter name")
		d.require(len(runtime.Command) > 0, "runtime.command", "external runtime requires a command")
	default:
		d.add(SeverityError, "runtime.driver", "runtime driver must be buckley or external")
	}
}

func validatePolicy(d *diagnostics, path string, policy PolicySpec) {
	validateApprovalMode(d, path+".approval_mode", policy.ApprovalMode)
	if policy.MaxToolCalls < 0 {
		d.add(SeverityError, path+".max_tool_calls", "max tool calls must be non-negative")
	}
	validateStringList(d, path+".domains", policy.Domains)
	for i, pack := range policy.RulePacks {
		packPath := fmt.Sprintf("%s.rule_packs[%d]", path, i)
		if pack.Name == "" && pack.Path == "" {
			d.add(SeverityError, packPath, "rule pack requires name or path")
		}
		validateIdentifier(d, packPath+".name", pack.Name)
		validateStringList(d, packPath+".domains", pack.Domains)
		switch pack.Scope {
		case "", "session", "agent", "project", "user", "system":
		default:
			d.add(SeverityError, packPath+".scope", "rule pack scope must be session, agent, project, user, or system")
		}
	}
}

func validateApprovalMode(d *diagnostics, path, mode string) {
	if mode == "" {
		return
	}
	switch mode {
	case "ask", "safe", "auto", "yolo":
	default:
		d.add(SeverityError, path, "approval mode must be ask, safe, auto, or yolo")
	}
}

func validateSandbox(d *diagnostics, path string, sandbox SandboxSpec) {
	if sandbox.Mode == "" {
		return
	}
	switch sandbox.Mode {
	case "disabled", "readonly", "workspace", "strict":
	default:
		d.add(SeverityError, path+".mode", "sandbox mode must be disabled, readonly, workspace, or strict")
	}
}

func validateInstructionFiles(d *diagnostics, files []string) {
	for i, file := range files {
		if strings.TrimSpace(file) == "" {
			d.add(SeverityError, fmt.Sprintf("instructions.files[%d]", i), "instruction file must not be empty")
		}
	}
}

func validateLabels(d *diagnostics, path string, labels map[string]string) {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			d.add(SeverityError, path, "label key must not be empty")
		}
	}
}

func hasErrors(diagnostics []Diagnostic) bool {
	for _, diag := range diagnostics {
		if diag.Severity == SeverityError {
			return true
		}
	}
	return false
}

func cleanRelativePaths(baseDir string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || filepath.IsAbs(path) {
			out = append(out, path)
			continue
		}
		out = append(out, filepath.Clean(filepath.Join(baseDir, path)))
	}
	return out
}

func runtimeSummary(runtime RuntimeSpec) string {
	driver := emptyDefault(runtime.Driver, "buckley")
	if driver == "external" && runtime.Adapter != "" {
		return driver + "/" + runtime.Adapter
	}
	return driver
}

func modelSummary(models ModelSpec) string {
	parts := []string{}
	if models.Chat != "" {
		parts = append(parts, "chat="+models.Chat)
	}
	if models.Planning != "" {
		parts = append(parts, "planning="+models.Planning)
	}
	if models.Execution != "" {
		parts = append(parts, "execution="+models.Execution)
	}
	if models.Review != "" {
		parts = append(parts, "review="+models.Review)
	}
	if models.Reasoning != "" {
		parts = append(parts, "reasoning="+models.Reasoning)
	}
	return strings.Join(parts, ", ")
}

func emptyDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
