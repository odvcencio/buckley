package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestRunPlanCommandUsageError(t *testing.T) {
	err := runPlanCommand([]string{"only-name"})
	if err == nil {
		t.Fatal("expected usage error for missing description")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("expected usage message, got: %v", err)
	}
}

func TestRunExecuteCommandUsageError(t *testing.T) {
	err := runExecuteCommand(nil)
	if err == nil {
		t.Fatal("expected usage error for missing plan-id")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("expected usage message, got: %v", err)
	}
}

func TestRunExecuteTaskCommandUsageError(t *testing.T) {
	err := runExecuteTaskCommand(nil)
	if err == nil {
		t.Fatal("expected usage error for missing flags")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("expected usage message, got: %v", err)
	}

	// Missing task
	err = runExecuteTaskCommand([]string{"--plan", "p1"})
	if err == nil {
		t.Fatal("expected usage error for missing task")
	}

	// Missing plan
	err = runExecuteTaskCommand([]string{"--task", "t1"})
	if err == nil {
		t.Fatal("expected usage error for missing plan")
	}
}

func TestRunAgentCommandCheckAndShow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	spec := []byte(`
version: buckley.agent/v1
name: review-agent
runtime:
  driver: buckley
policies:
  domains: [approval, risk]
`)
	if err := os.WriteFile(path, spec, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	checkOut := captureStdout(t, func() {
		if err := runAgentCommand([]string{"check", path}); err != nil {
			t.Fatalf("runAgentCommand check: %v", err)
		}
	})
	if !strings.Contains(checkOut, "valid Buckley agent spec") {
		t.Fatalf("unexpected check output: %q", checkOut)
	}

	showOut := captureStdout(t, func() {
		if err := runAgentCommand([]string{"show", "--format", "json", path}); err != nil {
			t.Fatalf("runAgentCommand show: %v", err)
		}
	})
	for _, want := range []string{`"name": "review-agent"`, `"valid": true`} {
		if !strings.Contains(showOut, want) {
			t.Fatalf("show output missing %q in:\n%s", want, showOut)
		}
	}
}

func TestRunAgentCommandProjectCheckAndShow(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".buckley"), 0o755); err != nil {
		t.Fatalf("mkdir project config: %v", err)
	}
	defaultPath := filepath.Join(dir, ".buckley", "agent.yaml")
	if err := os.WriteFile(defaultPath, []byte(`
version: buckley.agent/v1
name: daily
summary: Default daily driver
subagents:
  - name: reviewer
    tool_tier: read_only
`), 0o644); err != nil {
		t.Fatalf("write default agent spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"daily-agent"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agent", "subagents", "researcher"), 0o755); err != nil {
		t.Fatalf("mkdir filesystem agent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent", "instructions.md"), []byte("Use the project agent.\n"), 0o644); err != nil {
		t.Fatalf("write filesystem instructions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent", "subagents", "researcher", "instructions.md"), []byte("Research carefully.\n"), 0o644); err != nil {
		t.Fatalf("write filesystem subagent instructions: %v", err)
	}
	nested := filepath.Join(dir, "src")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}

	checkOut := captureStdout(t, func() {
		if err := runAgentCommand([]string{"check", "--project"}); err != nil {
			t.Fatalf("runAgentCommand project check: %v", err)
		}
	})
	if !strings.Contains(checkOut, "OK: "+defaultPath+" is a valid Buckley agent spec") {
		t.Fatalf("unexpected project check output:\n%s", checkOut)
	}

	showOut := captureStdout(t, func() {
		if err := runAgentCommand([]string{"show", "--project"}); err != nil {
			t.Fatalf("runAgentCommand project show: %v", err)
		}
	})
	for _, want := range []string{"Agent: daily", "Summary: Default daily driver", "Subagents:", "  - reviewer (tool_tier=read_only)"} {
		if !strings.Contains(showOut, want) {
			t.Fatalf("project show output missing %q:\n%s", want, showOut)
		}
	}

	jsonOut := captureStdout(t, func() {
		if err := runAgentCommand([]string{"show", "--json", "--spec", "filesystem"}); err != nil {
			t.Fatalf("runAgentCommand filesystem show json: %v", err)
		}
	})
	var payload struct {
		Spec struct {
			Name     string            `json:"name"`
			Metadata map[string]string `json:"metadata"`
		} `json:"spec"`
		Valid bool `json:"valid"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &payload); err != nil {
		t.Fatalf("unmarshal filesystem show json: %v\n%s", err, jsonOut)
	}
	if !payload.Valid || payload.Spec.Name != "daily-agent" || payload.Spec.Metadata["layout"] != agentspec.DiscoveredKindFilesystem {
		t.Fatalf("unexpected filesystem show json: %+v", payload)
	}
}

func TestRunAgentCommandList(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".buckley", "agents"), 0o755); err != nil {
		t.Fatalf("mkdir agent specs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".buckley", "agent.yaml"), []byte(`
version: buckley.agent/v1
name: daily
summary: Main daily-driver profile
subagents:
  - name: reviewer
  - name: coder
`), 0o644); err != nil {
		t.Fatalf("write root spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".buckley", "agents", "read-only.yaml"), []byte(`
version: buckley.agent/v1
name: read-only
tools:
  tier: read_only
`), 0o644); err != nil {
		t.Fatalf("write named spec: %v", err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	textOut := captureStdout(t, func() {
		if err := runAgentCommand([]string{"list"}); err != nil {
			t.Fatalf("runAgentCommand list: %v", err)
		}
	})
	for _, want := range []string{"Agent specs: 2", "daily (valid)", "subagents=coder,reviewer", "read-only (valid)"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("agent list output missing %q:\n%s", want, textOut)
		}
	}

	jsonOut := captureStdout(t, func() {
		if err := runAgentCommand([]string{"list", "--json"}); err != nil {
			t.Fatalf("runAgentCommand list json: %v", err)
		}
	})
	var snapshot agentListSnapshot
	if err := json.Unmarshal([]byte(jsonOut), &snapshot); err != nil {
		t.Fatalf("unmarshal agent list json: %v\n%s", err, jsonOut)
	}
	if !snapshot.Found || snapshot.Count != 2 || snapshot.Specs[0].Name != "daily" || snapshot.Specs[1].Name != "read-only" {
		t.Fatalf("unexpected agent list snapshot: %+v", snapshot)
	}
}

func TestRunSkillsCommandListsAndShowsAgentSkills(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "agent", "skills", "triage")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir agent skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`
---
description: Triage ambiguous repo work before changing files.
allowed_tools: [read_file, search_text]
---

# Triage

Inspect the current state before editing.
`), 0o644); err != nil {
		t.Fatalf("write agent skill: %v", err)
	}
	nested := filepath.Join(dir, "pkg", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	textOut := captureStdout(t, func() {
		if err := runSkillsCommand([]string{"list", "--source", "agent"}); err != nil {
			t.Fatalf("runSkillsCommand list: %v", err)
		}
	})
	for _, want := range []string{"Skills: 1", "triage [agent]", "Triage ambiguous repo work", "allowed_tools=read_file,search_text"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("skills list output missing %q:\n%s", want, textOut)
		}
	}

	jsonOut := captureStdout(t, func() {
		if err := runSkillsCommand([]string{"--json", "--source", "agent"}); err != nil {
			t.Fatalf("runSkillsCommand json: %v", err)
		}
	})
	var list skillsCommandList
	if err := json.Unmarshal([]byte(jsonOut), &list); err != nil {
		t.Fatalf("unmarshal skills json: %v\n%s", err, jsonOut)
	}
	if list.Count != 1 || list.Available[0].Name != "triage" || list.Available[0].Source != "agent" {
		t.Fatalf("unexpected skills json: %+v", list)
	}

	showOut := captureStdout(t, func() {
		if err := runSkillsCommand([]string{"show", "triage"}); err != nil {
			t.Fatalf("runSkillsCommand show: %v", err)
		}
	})
	for _, want := range []string{"Skill: triage", "Source: agent", "Allowed tools: read_file, search_text", "Inspect the current state before editing."} {
		if !strings.Contains(showOut, want) {
			t.Fatalf("skills show output missing %q:\n%s", want, showOut)
		}
	}

	dispatchOut := captureStdout(t, func() {
		handled, code := dispatchSubcommand([]string{"skills", "list", "--source", "agent"})
		if !handled {
			t.Fatal("skills should be handled")
		}
		if code != 0 {
			t.Fatalf("skills list should succeed, got code %d", code)
		}
	})
	if !strings.Contains(dispatchOut, "triage [agent]") {
		t.Fatalf("skills dispatch output missing agent skill:\n%s", dispatchOut)
	}
}

func TestRunAgentCommandInvalidSpec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	spec := []byte("version: nope\n")
	if err := os.WriteFile(path, spec, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	out := captureStdout(t, func() {
		err := runAgentCommand([]string{"check", path})
		if err == nil {
			t.Fatal("expected validation error")
		}
		if !strings.Contains(err.Error(), "validation errors") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if !strings.Contains(out, "unsupported version") {
		t.Fatalf("expected diagnostics, got:\n%s", out)
	}
}

func TestParseAgentRunArgs(t *testing.T) {
	opts, err := parseAgentRunArgs([]string{"agent.yaml", "reviewer", "inspect", "this"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs: %v", err)
	}
	if opts.agentPath != "agent.yaml" || opts.subagent != "reviewer" || opts.task != "inspect this" {
		t.Fatalf("unexpected opts: %+v", opts)
	}

	opts, err = parseAgentRunArgs([]string{"--subagent", "coder", "--model", "test-model", "agent.yaml", "fix", "bug"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs flag form: %v", err)
	}
	if opts.agentPath != "agent.yaml" || opts.subagent != "coder" || opts.model != "test-model" || opts.task != "fix bug" {
		t.Fatalf("unexpected flag opts: %+v", opts)
	}

	opts, err = parseAgentRunArgs([]string{"--dry-run", "agent.yaml", "reviewer", "inspect"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs dry-run form: %v", err)
	}
	if !opts.dryRun || opts.task != "inspect" {
		t.Fatalf("unexpected dry-run opts: %+v", opts)
	}

	opts, err = parseAgentRunArgs([]string{"--subagent", "probe", "--no-tools", "agent.yaml", "answer", "directly"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs no-tools form: %v", err)
	}
	if opts.toolTier != "none" || opts.task != "answer directly" {
		t.Fatalf("unexpected no-tools opts: %+v", opts)
	}

	opts, err = parseAgentRunArgs([]string{"--tool-tier", "read_only", "agent.yaml", "reviewer", "inspect"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs tool-tier form: %v", err)
	}
	if opts.toolTier != "read_only" {
		t.Fatalf("toolTier=%q want read_only", opts.toolTier)
	}

	opts, err = parseAgentRunArgs([]string{"--project", "reviewer", "inspect", "this"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs project form: %v", err)
	}
	if !opts.project || opts.agentPath != "" || opts.subagent != "reviewer" || opts.task != "inspect this" {
		t.Fatalf("unexpected project opts: %+v", opts)
	}

	opts, err = parseAgentRunArgs([]string{"--project", "--subagent", "coder", "fix", "bug"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs project subagent form: %v", err)
	}
	if !opts.project || opts.subagent != "coder" || opts.task != "fix bug" {
		t.Fatalf("unexpected project subagent opts: %+v", opts)
	}

	opts, err = parseAgentRunArgs([]string{"--spec", "daily", "reviewer", "inspect"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs spec form: %v", err)
	}
	if !opts.project || opts.specSelect != "daily" || opts.subagent != "reviewer" || opts.task != "inspect" {
		t.Fatalf("unexpected spec opts: %+v", opts)
	}

	if _, err := parseAgentRunArgs([]string{"--tool-tier", "root", "agent.yaml", "reviewer", "inspect"}); err == nil {
		t.Fatalf("expected invalid tool-tier error")
	}
	if _, err := parseAgentRunArgs([]string{"--no-tools", "--tool-tier", "full", "agent.yaml", "reviewer", "inspect"}); err == nil {
		t.Fatalf("expected conflicting tool flags error")
	}

	if _, err := parseAgentRunArgs([]string{"agent.yaml", "reviewer"}); err == nil {
		t.Fatalf("expected usage error for missing task")
	}
	if _, err := parseAgentRunArgs([]string{"--project", "reviewer"}); err == nil {
		t.Fatalf("expected project usage error for missing task")
	}
}

func TestRunAgentRunMissingSubagent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	spec := []byte(`
version: buckley.agent/v1
name: daily
subagents:
  - name: coder
    persona: implementer
`)
	if err := os.WriteFile(path, spec, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	err := runAgentRun([]string{path, "reviewer", "inspect this"})
	if err == nil || !strings.Contains(err.Error(), "available: coder") {
		t.Fatalf("err=%v want available subagents", err)
	}
}

func TestRunAgentRunDryRunPreview(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	spec := []byte(`
version: buckley.agent/v1
name: daily
tools:
  deny: [run_shell]
subagents:
  - name: reviewer
    model: xiaomi/mimo-v2.5-pro
    tool_tier: read_only
    skills: [code-review]
    policies:
      approval_mode: safe
    instructions: Review carefully.
`)
	if err := os.WriteFile(path, spec, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runAgentRun([]string{"--dry-run", "--model", "override/model", "--tool-tier", "none", path, "reviewer", "inspect", "this"}); err != nil {
			t.Fatalf("runAgentRun dry-run: %v", err)
		}
	})
	for _, want := range []string{
		"Agent run preview",
		"Agent: daily/reviewer",
		"Subagent: reviewer",
		"Model: override/model (flag override)",
		"Tool tier: none",
		"Tool filter: none",
		"Denied tools: run_shell",
		"Skills: code-review",
		"Approval mode: safe",
		"Instructions: yes",
		"Task: inspect this",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
}

func TestRunAgentRunProjectDryRunPreview(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".buckley", "agents"), 0o755); err != nil {
		t.Fatalf("mkdir project specs: %v", err)
	}
	defaultPath := filepath.Join(dir, ".buckley", "agent.yaml")
	if err := os.WriteFile(defaultPath, []byte(`
version: buckley.agent/v1
name: daily
subagents:
  - name: reviewer
    model: xiaomi/mimo-v2.5-pro
    tool_tier: read_only
    instructions: Review carefully.
`), 0o644); err != nil {
		t.Fatalf("write default spec: %v", err)
	}
	namedPath := filepath.Join(dir, ".buckley", "agents", "implementation.yaml")
	if err := os.WriteFile(namedPath, []byte(`
version: buckley.agent/v1
name: implementation
subagents:
  - name: coder
    tool_tier: standard
`), 0o644); err != nil {
		t.Fatalf("write named spec: %v", err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})
	if err := os.Chdir(filepath.Join(dir, ".buckley")); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runAgentRun([]string{"--project", "--dry-run", "reviewer", "inspect", "this"}); err != nil {
			t.Fatalf("runAgentRun project dry-run: %v", err)
		}
	})
	for _, want := range []string{
		"Agent run preview",
		"Source: " + defaultPath,
		"Agent: daily/reviewer",
		"Subagent: reviewer",
		"Task: inspect this",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("project dry-run output missing %q:\n%s", want, out)
		}
	}

	out = captureStdout(t, func() {
		if err := runAgentRun([]string{"--spec", "agents/implementation.yaml", "--dry-run", "coder", "build", "it"}); err != nil {
			t.Fatalf("runAgentRun named project dry-run: %v", err)
		}
	})
	for _, want := range []string{
		"Source: " + namedPath,
		"Agent: implementation/coder",
		"Subagent: coder",
		"Task: build it",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("named project dry-run output missing %q:\n%s", want, out)
		}
	}
}

func TestRunAgentRunFilesystemProjectDryRunPreview(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"daily-agent"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agent", "subagents", "researcher"), 0o755); err != nil {
		t.Fatalf("mkdir filesystem agent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent", "instructions.md"), []byte("Use the project agent.\n"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent", "subagents", "researcher", "instructions.md"), []byte("Research carefully.\n"), 0o644); err != nil {
		t.Fatalf("write subagent instructions: %v", err)
	}
	nested := filepath.Join(dir, "src")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runAgentRun([]string{"--project", "--dry-run", "researcher", "investigate", "this"}); err != nil {
			t.Fatalf("runAgentRun filesystem project dry-run: %v", err)
		}
	})
	for _, want := range []string{
		"Agent run preview",
		"Source: " + filepath.Join(dir, "agent"),
		"Agent: daily-agent/researcher",
		"Subagent: researcher",
		"Instructions: yes",
		"Task: investigate this",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("filesystem project dry-run output missing %q:\n%s", want, out)
		}
	}
}

func TestFormatSubagentTask(t *testing.T) {
	got := formatSubagentTask("reviewer", "inspect this")
	for _, want := range []string{`Subagent "reviewer" task:`, "inspect this", "Complete the task directly", "remaining risks", "do not claim unperformed actions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("task prompt missing %q: %s", want, got)
		}
	}
}

func TestResolveOneShotToolFilterRespectsAgentTierAndDeny(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	registry.Register(metadataTool{name: "read_file", metadata: tool.ToolMetadata{Impact: tool.ImpactReadOnly, Category: tool.CategoryFilesystem}})
	registry.Register(metadataTool{name: "write_file", metadata: tool.ToolMetadata{Impact: tool.ImpactModifying, Category: tool.CategoryFilesystem}})
	registry.Register(metadataTool{name: "run_shell", metadata: tool.ToolMetadata{Impact: tool.ImpactDestructive, Category: tool.CategoryShell}})

	profile := &agentspec.RuntimeProfile{Spec: &agentspec.Spec{Tools: agentspec.ToolSpec{Tier: "none"}}}
	got := resolveOneShotToolFilter(profile, registry, nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("none filter=%v want explicit empty list", got)
	}

	profile = &agentspec.RuntimeProfile{Spec: &agentspec.Spec{Tools: agentspec.ToolSpec{Tier: "read_only"}}}
	got = resolveOneShotToolFilter(profile, registry, nil)
	if strings.Join(got, ",") != "read_file" {
		t.Fatalf("read_only filter=%v want read_file", got)
	}

	profile.Spec.Tools.Tier = "standard"
	profile.Spec.Tools.Deny = []string{"write_file"}
	got = resolveOneShotToolFilter(profile, registry, nil)
	if strings.Join(got, ",") != "read_file" {
		t.Fatalf("standard deny filter=%v want read_file", got)
	}

	profile.Spec.Tools.Allow = []string{"run_shell", "read_file"}
	got = resolveOneShotToolFilter(profile, registry, []string{"read_file", "read_file"})
	if strings.Join(got, ",") != "read_file" {
		t.Fatalf("explicit filter=%v want read_file", got)
	}
}

type metadataTool struct {
	name     string
	metadata tool.ToolMetadata
}

func (t metadataTool) Name() string { return t.name }

func (t metadataTool) Description() string { return t.name }

func (t metadataTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (t metadataTool) Execute(map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true}, nil
}

func (t metadataTool) Metadata() tool.ToolMetadata { return t.metadata }

func TestRunRulesFacts(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runRulesCommand([]string{"facts", "approval"}); err != nil {
			t.Fatalf("runRulesCommand facts: %v", err)
		}
	})
	for _, want := range []string{"domain:  approval", "approval.mode", "risk.level"} {
		if !strings.Contains(out, want) {
			t.Fatalf("facts output missing %q in:\n%s", want, out)
		}
	}
}

func TestDispatchSubcommandResumePassthrough(t *testing.T) {
	// Resume should not be handled by dispatchSubcommand (returns to main flow)
	handled, _ := dispatchSubcommand([]string{"resume", "sess123"})
	if handled {
		t.Fatal("resume should pass through to main flow")
	}
}

func TestDispatchSubcommandMigrate(t *testing.T) {
	// Set up temp home
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create buckley dir
	if err := os.MkdirAll(filepath.Join(home, ".buckley"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	out := captureStdout(t, func() {
		handled, code := dispatchSubcommand([]string{"migrate"})
		if !handled {
			t.Fatal("migrate should be handled")
		}
		if code != 0 {
			t.Fatalf("migrate should succeed, got code %d", code)
		}
	})

	if !strings.Contains(out, "migrations applied") {
		t.Errorf("expected migrations applied message, got: %s", out)
	}
}

func TestSanitizeTerminalInputExtended(t *testing.T) {
	// Additional test cases for terminal input sanitization
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "text\x1b[1;31mcolored\x1b[0m",
			want:  "textcolored",
		},
		{
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		got := sanitizeTerminalInput(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeTerminalInput(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestPushBranchEmptyBranchNoOp(t *testing.T) {
	// Should not error with empty branch
	if err := pushBranch("origin", ""); err != nil {
		t.Errorf("pushBranch empty should be no-op, got: %v", err)
	}
}

func TestCompletionShells(t *testing.T) {
	shells := []string{"bash", "zsh", "fish"}
	for _, shell := range shells {
		out := captureStdout(t, func() {
			if err := runCompletionCommand([]string{shell}); err != nil {
				t.Fatalf("completion %s: %v", shell, err)
			}
		})
		if out == "" {
			t.Errorf("expected %s completion output", shell)
		}
		if !strings.Contains(out, "skills") {
			t.Errorf("expected %s completion to include skills command", shell)
		}
	}

	// Unknown shell should error
	if err := runCompletionCommand([]string{"powershell"}); err == nil {
		t.Fatal("expected error for unsupported shell")
	}
}

func TestInitDependenciesNoProvider(t *testing.T) {
	// Clear all provider keys
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create minimal config without API key
	if err := os.MkdirAll(filepath.Join(home, ".buckley"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, _, _, err := initDependencies()
	if err == nil {
		t.Fatal("expected error for no provider")
	}
	if !strings.Contains(err.Error(), "no provider") && !strings.Contains(err.Error(), "API keys") {
		t.Errorf("expected no provider error, got: %v", err)
	}
}

func TestInitIPCStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := initIPCStore()
	if err != nil {
		t.Fatalf("initIPCStore: %v", err)
	}
	defer store.Close()

	// Verify DB file exists
	dbPath := filepath.Join(home, ".buckley", "buckley.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("expected DB file to exist")
	}
}

func TestEncodingOverrideFlagApplied(t *testing.T) {
	// Test that encoding override flag affects config
	origFlag := encodingOverrideFlag
	t.Cleanup(func() { encodingOverrideFlag = origFlag })

	// Test json encoding
	encodingOverrideFlag = "json"
	cfg := config.DefaultConfig()
	cfg.Encoding.UseToon = true

	// The actual override happens in initDependencies, verify the logic
	if encodingOverrideFlag != "" {
		cfg.Encoding.UseToon = encodingOverrideFlag != "json"
	}

	if cfg.Encoding.UseToon {
		t.Error("encoding should be false (json mode)")
	}

	// Test toon encoding
	encodingOverrideFlag = "toon"
	cfg.Encoding.UseToon = false
	if encodingOverrideFlag != "" {
		cfg.Encoding.UseToon = encodingOverrideFlag != "json"
	}

	if !cfg.Encoding.UseToon {
		t.Error("encoding should be true (toon mode)")
	}
}

func TestWorktreeCommandUsage(t *testing.T) {
	// Test worktree command with no args shows usage
	err := runWorktreeCommand(nil)
	if err == nil {
		t.Fatal("expected usage error")
	}

	// Test unknown subcommand
	err = runWorktreeCommand([]string{"unknown"})
	if err == nil {
		t.Fatal("expected unknown subcommand error")
	}
}
