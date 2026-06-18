package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/skill"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type infoSnapshot struct {
	Version     infoVersion    `json:"version"`
	Paths       infoPaths      `json:"paths"`
	Config      infoConfig     `json:"config"`
	Models      infoModels     `json:"models"`
	Providers   []infoProvider `json:"providers"`
	Agent       *infoAgent     `json:"agent,omitempty"`
	AgentSpecs  infoAgentSpecs `json:"agent_specs"`
	ChatChecks  infoChatChecks `json:"chat_checks"`
	Skills      infoSkills     `json:"skills"`
	Tools       infoTools      `json:"tools"`
	Diagnostics []string       `json:"diagnostics,omitempty"`
}

type infoVersion struct {
	Release   string `json:"release"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
}

type infoPaths struct {
	WorkDir     string             `json:"workdir"`
	ProjectRoot string             `json:"project_root"`
	DB          string             `json:"db"`
	Trust       string             `json:"trust,omitempty"`
	Config      []infoConfigSource `json:"config"`
}

type infoConfigSource struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Active bool   `json:"active"`
}

type infoConfig struct {
	Encoding       string `json:"encoding"`
	ExecutionMode  string `json:"execution_mode"`
	OneshotMode    string `json:"oneshot_mode"`
	ApprovalMode   string `json:"approval_mode"`
	TrustLevel     string `json:"trust_level"`
	ProjectTrust   string `json:"project_trust"`
	SandboxMode    string `json:"sandbox_mode"`
	SandboxNetwork bool   `json:"sandbox_network"`
	IPCEnabled     bool   `json:"ipc_enabled"`
	IPCBind        string `json:"ipc_bind"`
}

type infoModels struct {
	DefaultProvider string            `json:"default_provider"`
	Planning        string            `json:"planning"`
	Execution       string            `json:"execution"`
	Review          string            `json:"review"`
	Reasoning       string            `json:"reasoning,omitempty"`
	Utility         map[string]string `json:"utility"`
}

type infoProvider struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Ready      bool   `json:"ready"`
	Credential string `json:"credential"`
	BaseURL    string `json:"base_url,omitempty"`
	Command    string `json:"command,omitempty"`
}

type infoAgent struct {
	Path      string   `json:"path"`
	Name      string   `json:"name,omitempty"`
	Subagents []string `json:"subagents,omitempty"`
}

type infoAgentSpecs struct {
	Found bool                 `json:"found"`
	Root  string               `json:"root,omitempty"`
	Count int                  `json:"count"`
	Specs []infoAgentSpecEntry `json:"specs,omitempty"`
	Error string               `json:"error,omitempty"`
}

type infoAgentSpecEntry struct {
	Path        string                 `json:"path"`
	Name        string                 `json:"name,omitempty"`
	Summary     string                 `json:"summary,omitempty"`
	Subagents   []string               `json:"subagents,omitempty"`
	Valid       bool                   `json:"valid"`
	Error       string                 `json:"error,omitempty"`
	Diagnostics []agentspec.Diagnostic `json:"diagnostics,omitempty"`
}

type infoChatChecks struct {
	Found         bool                        `json:"found"`
	Path          string                      `json:"path,omitempty"`
	ScenarioCount int                         `json:"scenario_count"`
	Scenarios     []doctorChatScenarioSummary `json:"scenarios,omitempty"`
	Error         string                      `json:"error,omitempty"`
}

type infoSkills struct {
	Count     int              `json:"count"`
	BySource  map[string]int   `json:"by_source"`
	Available []infoSkillEntry `json:"available"`
}

type infoSkillEntry struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Source       string   `json:"source"`
	Path         string   `json:"path,omitempty"`
	Phase        string   `json:"phase,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
}

type infoTools struct {
	Count      int             `json:"count"`
	ByCategory map[string]int  `json:"by_category"`
	ByTier     map[string]int  `json:"by_tier"`
	Available  []infoToolEntry `json:"available"`
}

type infoToolEntry struct {
	Name         string `json:"name"`
	Kind         string `json:"kind,omitempty"`
	Category     string `json:"category"`
	Impact       string `json:"impact"`
	Cost         string `json:"cost"`
	RequiredTier string `json:"required_tier"`
}

func runInfoCommand(args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("usage: buckley info [--json|--format json]")
	}

	snapshot, err := buildInfoSnapshot()
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(*format)) {
	case "", "text":
	case "json":
		*jsonOutput = true
	default:
		return fmt.Errorf("unsupported info format: %s", *format)
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snapshot)
	}
	fmt.Print(renderInfoSnapshot(snapshot))
	return nil
}

func buildInfoSnapshot() (infoSnapshot, error) {
	cfg, err := loadInfoConfig()
	if err != nil {
		return infoSnapshot{}, err
	}

	agentProfile, err := loadStartupAgentProfile(agentProfileFlag)
	if err != nil {
		return infoSnapshot{}, fmt.Errorf("loading agent spec: %w", err)
	}
	if agentProfile != nil {
		agentProfile.ApplyToConfig(cfg)
	}
	applyStartupModelOverride(cfg, modelOverrideFlag)
	applySandboxOverride(cfg)

	cwd, err := os.Getwd()
	if err != nil {
		return infoSnapshot{}, err
	}
	projectRoot := config.ResolveProjectRoot(cfg)
	trustStatus, _, trustPath, trustErr := projectTrustStatusForPath(cwd)
	if trustErr != nil {
		trustStatus = projectTrustUnknown
	}
	dbPath, dbErr := resolveDBPath()
	if dbErr != nil {
		dbPath = fmt.Sprintf("error: %v", dbErr)
	}

	diagnostics := []string{}
	if trustErr != nil {
		diagnostics = append(diagnostics, fmt.Sprintf("project trust unavailable: %v", trustErr))
	}
	if dbErr != nil {
		diagnostics = append(diagnostics, fmt.Sprintf("database path unavailable: %v", dbErr))
	}

	skills, skillDiagnostics := inspectSkills()
	diagnostics = append(diagnostics, skillDiagnostics...)
	tools, toolDiagnostics := inspectTools(cfg, cwd, skills.registry)
	diagnostics = append(diagnostics, toolDiagnostics...)
	agentSpecs, agentSpecDiagnostics := inspectProjectAgentSpecs(cwd)
	diagnostics = append(diagnostics, agentSpecDiagnostics...)
	chatChecks, chatCheckDiagnostics := inspectProjectChatChecks(cwd)
	diagnostics = append(diagnostics, chatCheckDiagnostics...)

	return infoSnapshot{
		Version: infoVersion{
			Release:   version,
			Commit:    unknownAsEmpty(commit),
			BuildDate: unknownAsEmpty(buildDate),
		},
		Paths: infoPaths{
			WorkDir:     cwd,
			ProjectRoot: projectRoot,
			DB:          dbPath,
			Trust:       trustPath,
			Config:      infoConfigSources(cwd),
		},
		Config: infoConfig{
			Encoding:       infoEncoding(cfg),
			ExecutionMode:  cfg.Execution.Mode,
			OneshotMode:    cfg.Oneshot.Mode,
			ApprovalMode:   cfg.Approval.Mode,
			TrustLevel:     cfg.Orchestrator.TrustLevel,
			ProjectTrust:   trustStatus.String(),
			SandboxMode:    cfg.Sandbox.Mode,
			SandboxNetwork: cfg.Sandbox.AllowNetwork,
			IPCEnabled:     cfg.IPC.Enabled,
			IPCBind:        cfg.IPC.Bind,
		},
		Models:      inspectModels(cfg),
		Providers:   inspectProviders(cfg),
		Agent:       inspectAgent(agentProfile),
		AgentSpecs:  agentSpecs,
		ChatChecks:  chatChecks,
		Skills:      skills.snapshot,
		Tools:       tools,
		Diagnostics: diagnostics,
	}, nil
}

func loadInfoConfig() (*config.Config, error) {
	var (
		cfg *config.Config
		err error
	)
	if strings.TrimSpace(configPath) != "" {
		cfg, err = config.LoadFromPath(configPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if encodingOverrideFlag != "" {
		cfg.Encoding.UseToon = encodingOverrideFlag != "json"
	}
	return cfg, nil
}

func infoConfigSources(cwd string) []infoConfigSource {
	home, _ := os.UserHomeDir()
	activeExplicit := strings.TrimSpace(configPath) != ""
	sources := []infoConfigSource{}
	if home != "" {
		if !activeExplicit {
			sources = append(sources, newInfoConfigSource("user", filepath.Join(home, ".buckley", "config.yaml"), true))
		}
		sources = append(sources, newInfoConfigSource("env", filepath.Join(home, ".buckley", "config.env"), true))
	}
	if activeExplicit {
		sources = append(sources, newInfoConfigSource("explicit", configPath, true))
		return sources
	}
	sources = append(sources, newInfoConfigSource("project", filepath.Join(cwd, ".buckley", "config.yaml"), true))
	return sources
}

func newInfoConfigSource(kind, path string, active bool) infoConfigSource {
	path = strings.TrimSpace(path)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	_, err := os.Stat(path)
	return infoConfigSource{
		Kind:   kind,
		Path:   path,
		Exists: err == nil,
		Active: active,
	}
}

func infoEncoding(cfg *config.Config) string {
	if cfg != nil && cfg.Encoding.UseToon {
		return "toon"
	}
	return "json"
}

func inspectModels(cfg *config.Config) infoModels {
	return infoModels{
		DefaultProvider: cfg.Models.DefaultProvider,
		Planning:        cfg.Models.Planning,
		Execution:       cfg.Models.Execution,
		Review:          cfg.Models.Review,
		Reasoning:       cfg.Models.Reasoning,
		Utility: map[string]string{
			"commit":     cfg.GetUtilityCommitModel(),
			"pr":         cfg.GetUtilityPRModel(),
			"compaction": cfg.GetUtilityCompactionModel(),
			"todo_plan":  cfg.GetUtilityTodoPlanModel(),
		},
	}
}

func inspectProviders(cfg *config.Config) []infoProvider {
	ready := map[string]bool{}
	for _, provider := range cfg.Providers.ReadyProviders() {
		ready[provider] = true
	}
	providers := []infoProvider{
		providerWithAPIKey("openrouter", cfg.Providers.OpenRouter.Enabled, ready["openrouter"], cfg.Providers.OpenRouter.APIKey, cfg.Providers.OpenRouter.BaseURL),
		providerWithAPIKey("openai", cfg.Providers.OpenAI.Enabled, ready["openai"], cfg.Providers.OpenAI.APIKey, cfg.Providers.OpenAI.BaseURL),
		providerWithAPIKey("anthropic", cfg.Providers.Anthropic.Enabled, ready["anthropic"], cfg.Providers.Anthropic.APIKey, cfg.Providers.Anthropic.BaseURL),
		providerWithAPIKey("google", cfg.Providers.Google.Enabled, ready["google"], cfg.Providers.Google.APIKey, cfg.Providers.Google.BaseURL),
		{
			Name:       "ollama",
			Enabled:    cfg.Providers.Ollama.Enabled,
			Ready:      ready["ollama"],
			Credential: "not-required",
			BaseURL:    cfg.Providers.Ollama.BaseURL,
		},
		{
			Name:       "litellm",
			Enabled:    cfg.Providers.LiteLLM.Enabled,
			Ready:      ready["litellm"],
			Credential: optionalCredentialState(cfg.Providers.LiteLLM.APIKey),
			BaseURL:    cfg.Providers.LiteLLM.BaseURL,
		},
		{
			Name:       "codex",
			Enabled:    cfg.Providers.Codex.Enabled,
			Ready:      ready["codex"],
			Credential: "not-required",
			Command:    cfg.Providers.Codex.Command,
		},
	}
	return providers
}

func providerWithAPIKey(name string, enabled, ready bool, apiKey, baseURL string) infoProvider {
	return infoProvider{
		Name:       name,
		Enabled:    enabled,
		Ready:      ready,
		Credential: requiredCredentialState(apiKey),
		BaseURL:    baseURL,
	}
}

func requiredCredentialState(value string) string {
	if strings.TrimSpace(value) == "" {
		return "missing"
	}
	return "set"
}

func optionalCredentialState(value string) string {
	if strings.TrimSpace(value) == "" {
		return "not-set"
	}
	return "set"
}

func inspectAgent(profile *agentspec.RuntimeProfile) *infoAgent {
	if profile == nil || profile.Spec == nil {
		return nil
	}
	agent := &infoAgent{
		Path: strings.TrimSpace(profile.SourcePath),
		Name: strings.TrimSpace(profile.Spec.Name),
	}
	for _, subagent := range profile.Spec.Subagents {
		if name := strings.TrimSpace(subagent.Name); name != "" {
			agent.Subagents = append(agent.Subagents, name)
		}
	}
	sort.Strings(agent.Subagents)
	return agent
}

func inspectProjectAgentSpecs(cwd string) (infoAgentSpecs, []string) {
	discovery, err := agentspec.DiscoverProjectSpecs(cwd)
	if err != nil {
		return infoAgentSpecs{Error: err.Error()}, []string{fmt.Sprintf("project agent specs unavailable: %v", err)}
	}
	entries := make([]infoAgentSpecEntry, 0, len(discovery.Specs))
	diagnostics := []string{}
	for _, spec := range discovery.Specs {
		entry := infoAgentSpecEntry{
			Path:        spec.Path,
			Name:        spec.Name,
			Summary:     spec.Summary,
			Subagents:   append([]string(nil), spec.Subagents...),
			Valid:       spec.Valid,
			Error:       spec.Error,
			Diagnostics: append([]agentspec.Diagnostic(nil), spec.Diagnostics...),
		}
		entries = append(entries, entry)
		if spec.Error != "" {
			diagnostics = append(diagnostics, fmt.Sprintf("project agent spec unreadable: %s: %s", spec.Path, spec.Error))
			continue
		}
		if !spec.Valid {
			diagnostics = append(diagnostics, fmt.Sprintf("project agent spec invalid: %s", spec.Path))
		}
	}
	return infoAgentSpecs{
		Found: len(entries) > 0,
		Root:  discovery.Root,
		Count: len(entries),
		Specs: entries,
	}, diagnostics
}

func inspectProjectChatChecks(cwd string) (infoChatChecks, []string) {
	path, err := findProjectChatCheckScenarioDir(cwd)
	if err != nil {
		return infoChatChecks{Error: err.Error()}, nil
	}
	scenarios, err := resolveDoctorChatScenarios("", 0, path, false, false)
	if err != nil {
		return infoChatChecks{
			Found: true,
			Path:  path,
			Error: err.Error(),
		}, []string{fmt.Sprintf("project chat checks invalid: %v", err)}
	}
	inventory := buildDoctorChatScenarioInventory(scenarios)
	return infoChatChecks{
		Found:         true,
		Path:          path,
		ScenarioCount: inventory.ScenarioCount,
		Scenarios:     inventory.Scenarios,
	}, nil
}

type inspectedSkills struct {
	registry *skill.Registry
	snapshot infoSkills
}

func inspectSkills() (inspectedSkills, []string) {
	registry := skill.NewRegistry()
	diagnostics := []string{}
	if err := registry.LoadAll(); err != nil {
		diagnostics = append(diagnostics, fmt.Sprintf("skills partially loaded: %v", err))
	}
	entries := []infoSkillEntry{}
	bySource := map[string]int{}
	for _, s := range registry.List() {
		if s == nil {
			continue
		}
		allowedTools := cleanToolNames(s.AllowedTools)
		sort.Strings(allowedTools)
		entries = append(entries, infoSkillEntry{
			Name:         s.Name,
			Description:  s.Description,
			Source:       s.Source,
			Path:         s.FilePath,
			Phase:        s.Phase,
			AllowedTools: allowedTools,
		})
		bySource[s.Source]++
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return inspectedSkills{
		registry: registry,
		snapshot: infoSkills{
			Count:     len(entries),
			BySource:  bySource,
			Available: entries,
		},
	}, diagnostics
}

func inspectTools(cfg *config.Config, cwd string, skills *skill.Registry) (infoTools, []string) {
	registry := tool.NewRegistry()
	diagnostics := []string{}
	if err := registry.LoadDefaultPlugins(); err != nil {
		diagnostics = append(diagnostics, fmt.Sprintf("plugins partially loaded: %v", err))
	}
	registry.ConfigureContainers(cfg, cwd)
	registry.SetWorkDir(cwd)
	registry.Register(&builtin.SkillActivationTool{Registry: skills})
	createTool := &builtin.CreateSkillTool{Registry: skills}
	createTool.SetWorkDir(cwd)
	registry.Register(createTool)

	entries := []infoToolEntry{}
	byCategory := map[string]int{}
	byTier := map[string]int{}
	for _, t := range registry.List() {
		if t == nil {
			continue
		}
		meta := tool.GetMetadata(t)
		tier := tool.RequiredTierForTool(t).String()
		entry := infoToolEntry{
			Name:         t.Name(),
			Kind:         registry.ToolKind(t.Name()),
			Category:     string(meta.Category),
			Impact:       string(meta.Impact),
			Cost:         string(meta.Cost),
			RequiredTier: tier,
		}
		entries = append(entries, entry)
		byCategory[entry.Category]++
		byTier[tier]++
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return infoTools{
		Count:      len(entries),
		ByCategory: byCategory,
		ByTier:     byTier,
		Available:  entries,
	}, diagnostics
}

func renderInfoSnapshot(snapshot infoSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Buckley Info\n")
	fmt.Fprintf(&b, "Version:      %s\n", snapshot.Version.Release)
	fmt.Fprintf(&b, "Workdir:      %s\n", snapshot.Paths.WorkDir)
	fmt.Fprintf(&b, "Project root: %s\n", snapshot.Paths.ProjectRoot)
	fmt.Fprintf(&b, "Database:     %s\n", snapshot.Paths.DB)
	fmt.Fprintf(&b, "Model:        %s\n", snapshot.Models.Execution)
	fmt.Fprintf(&b, "Reasoning:    %s\n", defaultText(snapshot.Models.Reasoning, "auto"))
	fmt.Fprintf(&b, "Provider:     %s\n", snapshot.Models.DefaultProvider)
	fmt.Fprintf(&b, "Ready:        %s\n", strings.Join(readyProviderNames(snapshot.Providers), ", "))
	fmt.Fprintf(&b, "Encoding:     %s\n", snapshot.Config.Encoding)
	fmt.Fprintf(&b, "Approval:     %s\n", snapshot.Config.ApprovalMode)
	fmt.Fprintf(&b, "Sandbox:      %s (network=%t)\n", snapshot.Config.SandboxMode, snapshot.Config.SandboxNetwork)
	if snapshot.Agent != nil {
		fmt.Fprintf(&b, "Agent:        %s (%s)\n", defaultText(snapshot.Agent.Name, "unnamed"), snapshot.Agent.Path)
	}
	fmt.Fprintf(&b, "Agent specs:  %s\n", renderInfoAgentSpecs(snapshot.AgentSpecs))
	fmt.Fprintf(&b, "Chat checks:  %s\n", renderInfoChatChecks(snapshot.ChatChecks))
	fmt.Fprintf(&b, "Skills:       %d", snapshot.Skills.Count)
	if len(snapshot.Skills.BySource) > 0 {
		fmt.Fprintf(&b, " (%s)", renderCounts(snapshot.Skills.BySource))
	}
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "Tools:        %d", snapshot.Tools.Count)
	if len(snapshot.Tools.ByTier) > 0 {
		fmt.Fprintf(&b, " (%s)", renderCounts(snapshot.Tools.ByTier))
	}
	fmt.Fprintf(&b, "\n")
	if len(snapshot.Diagnostics) > 0 {
		fmt.Fprintf(&b, "Diagnostics:\n")
		for _, diagnostic := range snapshot.Diagnostics {
			fmt.Fprintf(&b, "  - %s\n", diagnostic)
		}
	}
	fmt.Fprintf(&b, "\nUse `buckley info --json` for the full manifest.\n")
	return b.String()
}

func renderInfoAgentSpecs(specs infoAgentSpecs) string {
	if specs.Found {
		if specs.Error != "" {
			return fmt.Sprintf("error (%s)", specs.Root)
		}
		invalid := 0
		for _, spec := range specs.Specs {
			if !spec.Valid || spec.Error != "" {
				invalid++
			}
		}
		if invalid > 0 {
			return fmt.Sprintf("%d (%s, invalid=%d)", specs.Count, specs.Root, invalid)
		}
		return fmt.Sprintf("%d (%s)", specs.Count, specs.Root)
	}
	return "0 (not found)"
}

func renderInfoChatChecks(checks infoChatChecks) string {
	if checks.Found {
		if checks.Error != "" {
			return fmt.Sprintf("error (%s)", checks.Path)
		}
		return fmt.Sprintf("%d (%s)", checks.ScenarioCount, checks.Path)
	}
	return "0 (not found)"
}

func readyProviderNames(providers []infoProvider) []string {
	names := []string{}
	for _, provider := range providers {
		if provider.Ready {
			names = append(names, provider.Name)
		}
	}
	if len(names) == 0 {
		return []string{"none"}
	}
	return names
}

func renderCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ", ")
}

func defaultText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func unknownAsEmpty(value string) string {
	if value == "unknown" {
		return ""
	}
	return value
}
