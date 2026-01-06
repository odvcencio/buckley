package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/personality"
)

// AgentsOptions configures AGENTS.md scaffolding.
type AgentsOptions struct {
	Path       string
	Force      bool
	Config     *config.Config
	Context    *projectcontext.ProjectContext
	Personas   *personality.PersonaProvider
	TaskPhases []orchestrator.TaskPhase
}

// PersonaOptions configures persona scaffolding.
type PersonaOptions struct {
	BaseDir          string
	Name             string
	Force            bool
	Tone             string
	QuirkProbability float64
}

// SkillOptions configures skill scaffolding.
type SkillOptions struct {
	BaseDir string
	Name    string
	Force   bool
}

// PluginOptions configures plugin scaffolding.
type PluginOptions struct {
	BaseDir string
	Name    string
	Force   bool
}

// GenerateAgents writes an AGENTS.md template using the provided snapshot.
func GenerateAgents(opts AgentsOptions) (string, error) {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		return "", fmt.Errorf("agents path required")
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}
	if fileExists(path) && !opts.Force {
		return "", fmt.Errorf("%s already exists", path)
	}

	content := renderAgentsContent(opts)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write agents file: %w", err)
	}
	return path, nil
}

// GeneratePersona writes a persona YAML file using project defaults.
func GeneratePersona(opts PersonaOptions) (string, error) {
	if strings.TrimSpace(opts.BaseDir) == "" {
		return "", fmt.Errorf("persona base directory required")
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return "", fmt.Errorf("persona name required")
	}
	slug := slugify(name)
	if slug == "" {
		slug = "custom-persona"
	}
	if err := os.MkdirAll(opts.BaseDir, 0o755); err != nil {
		return "", fmt.Errorf("create persona dir: %w", err)
	}
	path := filepath.Join(opts.BaseDir, slug+".yaml")
	if fileExists(path) && !opts.Force {
		return "", fmt.Errorf("persona %s already exists", path)
	}
	tone := strings.TrimSpace(opts.Tone)
	if tone == "" {
		tone = "friendly"
	}
	quirk := opts.QuirkProbability
	if quirk <= 0 {
		quirk = 0.15
	}
	content := renderPersonaBlueprint(name, slug, tone, quirk)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write persona file: %w", err)
	}
	return path, nil
}

// GenerateSkill scaffolds a SKILL.md file under the provided base directory.
func GenerateSkill(opts SkillOptions) (string, error) {
	if strings.TrimSpace(opts.BaseDir) == "" {
		return "", fmt.Errorf("skill base directory required")
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return "", fmt.Errorf("skill name required")
	}
	slug := slugify(name)
	if slug == "" {
		slug = "custom-skill"
	}
	targetDir := filepath.Join(opts.BaseDir, slug)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", fmt.Errorf("create skill directory: %w", err)
	}
	path := filepath.Join(targetDir, "SKILL.md")
	if fileExists(path) && !opts.Force {
		return "", fmt.Errorf("skill %s already exists", path)
	}
	content := renderSkillBlueprint(name, slug)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write skill file: %w", err)
	}
	return path, nil
}

// GeneratePlugin scaffolds a plugin manifest + shell entrypoint.
func GeneratePlugin(opts PluginOptions) (string, error) {
	if strings.TrimSpace(opts.BaseDir) == "" {
		return "", fmt.Errorf("plugin base directory required")
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return "", fmt.Errorf("plugin name required")
	}
	slug := slugify(name)
	if slug == "" {
		slug = "custom-plugin"
	}
	targetDir := filepath.Join(opts.BaseDir, slug)
	if fileExists(targetDir) && !opts.Force {
		return "", fmt.Errorf("plugin directory %s already exists", targetDir)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", fmt.Errorf("create plugin directory: %w", err)
	}
	manifestPath := filepath.Join(targetDir, "tool.yaml")
	scriptPath := filepath.Join(targetDir, "main.sh")
	if err := os.WriteFile(manifestPath, []byte(renderPluginManifest(slug)), 0o644); err != nil {
		return "", fmt.Errorf("write manifest: %w", err)
	}
	if err := os.WriteFile(scriptPath, []byte(renderPluginScript()), 0o755); err != nil {
		return "", fmt.Errorf("write plugin script: %w", err)
	}
	return targetDir, nil
}

func renderAgentsContent(opts AgentsOptions) string {
	var b strings.Builder
	b.WriteString("# Project Context for AI Agents\n\n")

	b.WriteString("## Project Summary\n\n")
	b.WriteString(strings.TrimSpace(summaryText(opts.Context)))
	b.WriteString("\n\n")

	b.WriteString("## Development Rules\n\n")
	for _, rule := range ruleList(opts.Context) {
		b.WriteString("- " + strings.TrimSpace(rule) + "\n")
	}
	b.WriteString("\n")

	b.WriteString("## Agent Guidelines\n\n")
	for _, guideline := range guidelineList(opts.Context) {
		b.WriteString("- " + strings.TrimSpace(guideline) + "\n")
	}
	b.WriteString("\n")

	b.WriteString(renderModelSection(opts.Config))
	b.WriteString("\n")
	b.WriteString(renderPersonaSection(opts.Personas))
	b.WriteString("\n")
	b.WriteString(renderTaskPhaseSection(opts.TaskPhases))
	b.WriteString("\n")
	b.WriteString(renderSubAgentSection(opts.Context))
	b.WriteString("\n")
	b.WriteString(renderTechStackSection(opts.Context))
	b.WriteString("\n")
	b.WriteString("## Commands & Checks\n\n")
	b.WriteString("- `./scripts/test.sh` – fast validation suite (preferred)\n")
	b.WriteString("- `GO_TEST_TARGET=all ./scripts/test.sh` – exhaustive Go tests when touching shared libs\n")
	b.WriteString("- `cd web && bun run build` – build Mission Control UI (if applicable)\n")
	b.WriteString("- `golangci-lint run` – static analysis for Go packages\n\n")

	b.WriteString("## TODO Workflow Expectations\n\n")
	b.WriteString("- Use the built-in TODO tool for any task beyond trivial queries\n")
	b.WriteString("- Keep exactly one TODO `in_progress` at a time (others pending/completed)\n")
	b.WriteString("- Checkpoint every 10 completed TODOs or sooner for 100+ step efforts\n\n")

	b.WriteString("---\n\n")
	b.WriteString("This file is auto-loaded by Buckley. Update it whenever conventions, personas, or sub-agents change.\n")
	b.WriteString("Use `/agents show` to inspect, `/agents reload` after edits, and `/generate persona|skill|plugin` to scaffold supporting assets.\n")

	return strings.TrimSpace(b.String()) + "\n"
}

func summaryText(ctx *projectcontext.ProjectContext) string {
	if ctx != nil && strings.TrimSpace(ctx.Summary) != "" {
		return ctx.Summary
	}
	return "[Describe what this repository builds and why it matters]"
}

func ruleList(ctx *projectcontext.ProjectContext) []string {
	if ctx != nil && len(ctx.Rules) > 0 {
		return ctx.Rules
	}
	return []string{
		"All code changes must include automated tests when feasible",
		"Follow action-style commits (action(scope): summary).",
		"Open PRs against non-main branches and request review",
	}
}

func guidelineList(ctx *projectcontext.ProjectContext) []string {
	if ctx != nil && len(ctx.Guidelines) > 0 {
		return ctx.Guidelines
	}
	return []string{
		"State intent before editing files and narrate validation steps",
		"Prefer small, composable functions with clear error handling",
		"Call ./scripts/test.sh before requesting review",
	}
}

func renderModelSection(cfg *config.Config) string {
	if cfg == nil {
		return "## Default Models\n\n- Configure planning/execution/review models in ~/.buckley/config.yaml\n"
	}
	var b strings.Builder
	b.WriteString("## Default Models\n\n")
	b.WriteString(fmt.Sprintf("- Planning: `%s`\n", cfg.Models.Planning))
	b.WriteString(fmt.Sprintf("- Execution: `%s`\n", cfg.Models.Execution))
	b.WriteString(fmt.Sprintf("- Review: `%s`\n", cfg.Models.Review))
	if len(cfg.Models.FallbackChains) > 0 {
		b.WriteString("\nFallback chains:\n")
		for modelID, chain := range cfg.Models.FallbackChains {
			b.WriteString(fmt.Sprintf("- `%s` → %s\n", modelID, strings.Join(chain, " → ")))
		}
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func renderPersonaSection(provider *personality.PersonaProvider) string {
	if provider == nil {
		return "## Workflow Personas\n\nNo personas discovered. Use `/generate persona <name>` to scaffold one.\n"
	}
	profiles := provider.Profiles()
	if len(profiles) == 0 {
		return "## Workflow Personas\n\nNo personas discovered. Use `/generate persona <name>` to scaffold one.\n"
	}
	sort.SliceStable(profiles, func(i, j int) bool {
		return profiles[i].Name < profiles[j].Name
	})

	var b strings.Builder
	b.WriteString("## Workflow Personas\n\n")
	b.WriteString("| ID | Name | Summary |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, profile := range profiles {
		b.WriteString(fmt.Sprintf("| %s | %s | %s |\n", profile.ID, profile.Name, profile.Summary))
	}
	b.WriteString("\nPhase assignments:\n")
	for _, stage := range orchestrator.PersonaStages {
		profile := provider.PersonaForPhase(stage)
		label := "default persona"
		if profile != nil {
			label = fmt.Sprintf("%s (%s)", profile.Name, profile.ID)
		}
		b.WriteString(fmt.Sprintf("- %s → %s\n", strings.Title(stage), label))
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func renderTaskPhaseSection(phases []orchestrator.TaskPhase) string {
	if len(phases) == 0 {
		return "## Task Phases\n\nBuilder → Verify → Review. Customize via `workflow.task_phases` in config.\n"
	}
	var b strings.Builder
	b.WriteString("## Task Phases\n\n")
	for _, phase := range phases {
		title := phase.Name
		if strings.TrimSpace(title) == "" {
			title = strings.Title(phase.Stage)
		}
		b.WriteString(fmt.Sprintf("### %s (%s)\n", title, strings.Title(phase.Stage)))
		if strings.TrimSpace(phase.Description) != "" {
			b.WriteString(phase.Description + "\n")
		}
		if len(phase.Targets) > 0 {
			for _, target := range phase.Targets {
				b.WriteString("- " + target + "\n")
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func renderSubAgentSection(ctx *projectcontext.ProjectContext) string {
	if ctx == nil || len(ctx.SubAgents) == 0 {
		return "## Sub-Agents\n\n[Define delegation targets here. Example:\n\n### reviewer\n- **Description:** Code review specialist\n- **Model:** anthropic/claude-3.5-sonnet\n- **Tools:** [read_file, git_diff]\n- **Max Cost:** $1.00\n- **Instructions:** Focus on security, performance, and regressions\n]\n"
	}
	var b strings.Builder
	b.WriteString("## Sub-Agents\n\n")
	names := make([]string, 0, len(ctx.SubAgents))
	for name := range ctx.SubAgents {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := ctx.SubAgents[name]
		b.WriteString(fmt.Sprintf("### %s\n", name))
		if spec.Description != "" {
			b.WriteString(fmt.Sprintf("- **Description:** %s\n", spec.Description))
		}
		if spec.Model != "" {
			b.WriteString(fmt.Sprintf("- **Model:** %s\n", spec.Model))
		}
		if len(spec.Tools) > 0 {
			b.WriteString(fmt.Sprintf("- **Tools:** %s\n", strings.Join(spec.Tools, ", ")))
		}
		if spec.MaxCost > 0 {
			b.WriteString(fmt.Sprintf("- **Max Cost:** $%.2f\n", spec.MaxCost))
		}
		if spec.Instructions != "" {
			b.WriteString(fmt.Sprintf("- **Instructions:** %s\n", spec.Instructions))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func renderTechStackSection(ctx *projectcontext.ProjectContext) string {
	if ctx == nil || len(ctx.TechStack) == 0 {
		return "## Tech Stack\n\n- **Language:** Go 1.25+\n- **UI:** TypeScript + React (web/ Mission Control)\n- **Database:** SQLite (WAL mode)\n- **Tooling:** Bubble Tea TUI, Tailwind, Connect/Protobuf\n"
	}
	var b strings.Builder
	b.WriteString("## Tech Stack\n\n")
	keys := make([]string, 0, len(ctx.TechStack))
	for key := range ctx.TechStack {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b.WriteString(fmt.Sprintf("- **%s:** %s\n", strings.Title(key), ctx.TechStack[key]))
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func renderPersonaBlueprint(displayName, slug, tone string, quirk float64) string {
	return fmt.Sprintf(`name: %s
summary: "%s persona focused on %s-type work."
description: |
  Provide a concise description of when to use this persona and what success looks like.
traits:
  - Calm under pressure
  - Explains trade-offs before acting
  - Shares validation evidence proactively
goals:
  - Keep the development loop predictable
  - Surface edge cases before they regress
directives:
  - Narrate your intent before editing files
  - Reference files and line numbers in all findings
  - Run ./scripts/test.sh before concluding work
voice:
  planning: Architectural strategist that inventories dependencies and risks.
  execution: Precise implementer who explains every filesystem or tool action.
  review: Thorough reviewer citing files, line numbers, and severity.
style:
  tone: %s
  quirk_probability: %.2f
  response_length: concise
`, displayName, strings.Title(slug), displayName, tone, quirk)
}

func renderSkillBlueprint(displayName, slug string) string {
	return fmt.Sprintf(`---
name: %s
description: "%s skill – describe when to activate it."
phase: execute
requires_todo: true
allowed_tools: [run_shell, read_file, write_file, search_text]
todo_template: |
  - Outline the desired outcome
  - Run ./scripts/test.sh
  - Summarize validation
---

# %s Skill Playbook

## When to Use

- Describe scenarios that benefit the most from this skill
- Mention any prerequisites or cautions

## Operating Procedure

1. Step-by-step outline of how the agent should execute the workflow
2. Include validation expectations and telemetry to capture
3. Encourage TODO updates after each meaningful step

## Exit Criteria

- [ ] Criteria 1
- [ ] Criteria 2
- [ ] Criteria 3
`, slug, displayName, strings.Title(displayName))
}

func renderPluginManifest(slug string) string {
	return fmt.Sprintf(`name: %s
description: "Describe what this plugin automates."
parameters:
  type: object
  properties:
    query:
      type: string
      description: Input to process
  required: [query]
executable: ./main.sh
timeout_ms: 60000
`, slug)
}

func renderPluginScript() string {
	return `#!/usr/bin/env bash
set -euo pipefail

PAYLOAD=$(cat)

cat <<'JSON'
{"success": true, "data": {"message": "Plugin skeleton created. Parse PAYLOAD and emit useful output."}}
JSON
`
}

var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = slugPattern.ReplaceAllString(value, "-")
	return strings.Trim(value, "-")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ResolveBaseDir chooses the correct root (.buckley/*) based on scope.
// ResolveBaseDir returns the project or global .buckley directory for scaffolds.
func ResolveBaseDir(projectRoot, rel string, global bool) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(home, rel), nil
	}
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		root = cwd
	}
	return filepath.Join(root, rel), nil
}
