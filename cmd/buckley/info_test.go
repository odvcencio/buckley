package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInfoCommandJSON(t *testing.T) {
	setupInfoTestEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "test-openrouter-key")

	out := captureStdout(t, func() {
		if err := runInfoCommand([]string{"--json"}); err != nil {
			t.Fatalf("runInfoCommand: %v", err)
		}
	})
	if strings.Contains(out, "test-openrouter-key") {
		t.Fatalf("info output leaked provider credential: %s", out)
	}

	var snapshot infoSnapshot
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatalf("unmarshal info json: %v\n%s", err, out)
	}
	if snapshot.Models.Execution != "z-ai/glm-5.2" {
		t.Fatalf("execution model = %q, want GLM default", snapshot.Models.Execution)
	}
	if snapshot.Config.ProjectTrust != "unknown" {
		t.Fatalf("project trust = %q, want unknown", snapshot.Config.ProjectTrust)
	}
	if !providerReady(snapshot.Providers, "openrouter") {
		t.Fatalf("expected openrouter provider to be ready: %+v", snapshot.Providers)
	}
	if snapshot.Skills.Count == 0 {
		t.Fatalf("expected bundled skills to be discovered")
	}
	if !toolPresent(snapshot.Tools.Available, "read_file") {
		t.Fatalf("expected read_file tool in manifest")
	}
	if !toolPresent(snapshot.Tools.Available, "activate_skill") {
		t.Fatalf("expected activate_skill tool in manifest")
	}
	if snapshot.ChatChecks.Found || snapshot.ChatChecks.ScenarioCount != 0 {
		t.Fatalf("expected no project chat checks in empty test env: %+v", snapshot.ChatChecks)
	}
}

func TestRunInfoCommandTextAndDispatch(t *testing.T) {
	setupInfoTestEnv(t)

	out := captureStdout(t, func() {
		handled, code := dispatchSubcommand([]string{"info"})
		if !handled || code != 0 {
			t.Fatalf("dispatch info handled=%v code=%d", handled, code)
		}
	})
	for _, want := range []string{"Buckley Info", "Project root:", "Agent specs:", "Chat checks:", "Tools:", "Use `buckley info --json`"} {
		if !strings.Contains(out, want) {
			t.Fatalf("info output missing %q:\n%s", want, out)
		}
	}
	if err := runInfoCommand([]string{"extra"}); err == nil {
		t.Fatalf("expected usage error for extra arg")
	}
}

func TestParseStartupOptionsLeavesInfoJSONFlag(t *testing.T) {
	opts, err := parseStartupOptions([]string{"info", "--json"})
	if err != nil {
		t.Fatalf("parseStartupOptions: %v", err)
	}
	if opts.encodingOverride != "" {
		t.Fatalf("encoding override = %q, want empty", opts.encodingOverride)
	}
	if got := strings.Join(opts.args, " "); got != "info --json" {
		t.Fatalf("args = %q, want info --json", got)
	}

	opts, err = parseStartupOptions([]string{"--json", "info"})
	if err != nil {
		t.Fatalf("parseStartupOptions global json: %v", err)
	}
	if opts.encodingOverride != "json" {
		t.Fatalf("global encoding override = %q, want json", opts.encodingOverride)
	}
	if got := strings.Join(opts.args, " "); got != "info" {
		t.Fatalf("args = %q, want info", got)
	}
}

func TestInfoConfigSourcesExplicitConfigIncludesEnv(t *testing.T) {
	home := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("HOME", home)

	oldConfigPath := configPath
	configPath = filepath.Join(workDir, "buckley.yaml")
	t.Cleanup(func() {
		configPath = oldConfigPath
	})

	sources := infoConfigSources(workDir)
	if len(sources) != 2 {
		t.Fatalf("sources = %+v, want env and explicit", sources)
	}
	if sources[0].Kind != "env" || sources[1].Kind != "explicit" {
		t.Fatalf("sources = %+v, want env then explicit", sources)
	}
}

func TestRunInfoCommandJSONIncludesProjectAgentSpecs(t *testing.T) {
	workDir := setupInfoTestEnv(t)
	agentDir := filepath.Join(workDir, ".buckley", "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir project agent specs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".buckley", "agent.yaml"), []byte(`
version: buckley.agent/v1
name: project-agent
summary: Default project runtime profile
subagents:
  - name: reviewer
  - name: builder
`), 0o644); err != nil {
		t.Fatalf("write root agent spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "strict-reviewer.yaml"), []byte(`
version: buckley.agent/v1
name: strict-reviewer
summary: Review-only profile
tools:
  tier: read_only
`), 0o644); err != nil {
		t.Fatalf("write named agent spec: %v", err)
	}
	nested := filepath.Join(workDir, "src", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested workdir: %v", err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested workdir: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runInfoCommand([]string{"--json"}); err != nil {
			t.Fatalf("runInfoCommand: %v", err)
		}
	})
	var snapshot infoSnapshot
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatalf("unmarshal info json: %v\n%s", err, out)
	}
	if !snapshot.AgentSpecs.Found || snapshot.AgentSpecs.Count != 2 {
		t.Fatalf("expected project agent specs to be discovered: %+v", snapshot.AgentSpecs)
	}
	if snapshot.AgentSpecs.Root != workDir {
		t.Fatalf("agent spec root=%q want %q", snapshot.AgentSpecs.Root, workDir)
	}
	if snapshot.AgentSpecs.Specs[0].Name != "project-agent" || !snapshot.AgentSpecs.Specs[0].Valid {
		t.Fatalf("unexpected root agent spec: %+v", snapshot.AgentSpecs.Specs[0])
	}
	if strings.Join(snapshot.AgentSpecs.Specs[0].Subagents, ",") != "builder,reviewer" {
		t.Fatalf("unexpected subagents: %+v", snapshot.AgentSpecs.Specs[0].Subagents)
	}
	if snapshot.AgentSpecs.Specs[1].Name != "strict-reviewer" || snapshot.AgentSpecs.Specs[1].Summary != "Review-only profile" {
		t.Fatalf("unexpected named agent spec: %+v", snapshot.AgentSpecs.Specs[1])
	}
}

func TestRunInfoCommandJSONIncludesProjectAgentSkills(t *testing.T) {
	workDir := setupInfoTestEnv(t)
	skillDir := filepath.Join(workDir, "agent", "skills", "triage")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir agent skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`
---
description: Triage ambiguous repo work before changing files.
---

# Triage

Inspect the repo state and identify the next smallest useful slice.
`), 0o644); err != nil {
		t.Fatalf("write agent skill: %v", err)
	}
	nested := filepath.Join(workDir, "pkg", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested workdir: %v", err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested workdir: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runInfoCommand([]string{"--json"}); err != nil {
			t.Fatalf("runInfoCommand: %v", err)
		}
	})
	var snapshot infoSnapshot
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatalf("unmarshal info json: %v\n%s", err, out)
	}
	if snapshot.Skills.BySource["agent"] != 1 {
		t.Fatalf("agent skill source count=%d, skills=%+v", snapshot.Skills.BySource["agent"], snapshot.Skills)
	}
	var found bool
	for _, entry := range snapshot.Skills.Available {
		if entry.Name == "triage" {
			found = true
			if entry.Source != "agent" || entry.Description != "Triage ambiguous repo work before changing files." {
				t.Fatalf("unexpected triage entry: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("triage skill not reported: %+v", snapshot.Skills.Available)
	}
}

func TestRunInfoCommandJSONIncludesProjectChatChecks(t *testing.T) {
	workDir := setupInfoTestEnv(t)
	projectDir := filepath.Join(workDir, ".buckley", "chatchecks", "tools")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project chat checks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "no-tools.json"), []byte(`{"tags":["smoke"],"turns":[{"user":"say READY","want_contains":["READY"],"max_tool_calls":0}]}`), 0o644); err != nil {
		t.Fatalf("write project chat check: %v", err)
	}
	nested := filepath.Join(workDir, "src", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested workdir: %v", err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested workdir: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runInfoCommand([]string{"--json"}); err != nil {
			t.Fatalf("runInfoCommand: %v", err)
		}
	})
	var snapshot infoSnapshot
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatalf("unmarshal info json: %v\n%s", err, out)
	}
	if !snapshot.ChatChecks.Found {
		t.Fatalf("expected project chat checks to be discovered: %+v", snapshot.ChatChecks)
	}
	if want := filepath.Join(workDir, ".buckley", "chatchecks"); snapshot.ChatChecks.Path != want {
		t.Fatalf("chat check path=%q want %q", snapshot.ChatChecks.Path, want)
	}
	if snapshot.ChatChecks.ScenarioCount != 1 || len(snapshot.ChatChecks.Scenarios) != 1 {
		t.Fatalf("unexpected chat check inventory: %+v", snapshot.ChatChecks)
	}
	scenario := snapshot.ChatChecks.Scenarios[0]
	if scenario.Name != "tools/no-tools" || scenario.Turns != 1 || scenario.MaxToolChecks != 1 {
		t.Fatalf("unexpected chat check scenario summary: %+v", scenario)
	}
}

func setupInfoTestEnv(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BUCKLEY_DATA_DIR", filepath.Join(home, ".buckley-data"))

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	oldConfigPath := configPath
	oldModelOverride := modelOverrideFlag
	oldAgentProfile := agentProfileFlag
	oldEncodingOverride := encodingOverrideFlag
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
		configPath = oldConfigPath
		modelOverrideFlag = oldModelOverride
		agentProfileFlag = oldAgentProfile
		encodingOverrideFlag = oldEncodingOverride
	})
	configPath = ""
	modelOverrideFlag = ""
	agentProfileFlag = ""
	encodingOverrideFlag = ""
	return workDir
}

func providerReady(providers []infoProvider, name string) bool {
	for _, provider := range providers {
		if provider.Name == name {
			return provider.Ready
		}
	}
	return false
}

func toolPresent(tools []infoToolEntry, name string) bool {
	for _, entry := range tools {
		if entry.Name == name {
			return true
		}
	}
	return false
}
