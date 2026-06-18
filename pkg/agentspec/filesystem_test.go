package agentspec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverProjectSpecsFindsFilesystemAgentLayout(t *testing.T) {
	root := t.TempDir()
	writeFilesystemFile(t, filepath.Join(root, "package.json"), `{"name":"@acme/weather-agent"}`)
	writeFilesystemFile(t, filepath.Join(root, "agent", "instructions.md"), "You are the weather agent.")
	writeFilesystemFile(t, filepath.Join(root, "agent", "skills", "forecast.md"), "Forecast carefully.")
	writeFilesystemFile(t, filepath.Join(root, "agent", "tools", "get_weather.ts"), "export default {}")
	writeFilesystemFile(t, filepath.Join(root, "agent", "subagents", "researcher", "instructions.md"), "Research before answering.")
	nested := filepath.Join(root, "src", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	discovery, err := DiscoverProjectSpecs(nested)
	if err != nil {
		t.Fatalf("DiscoverProjectSpecs: %v", err)
	}
	if discovery.Root != root {
		t.Fatalf("root=%q want %q", discovery.Root, root)
	}
	if len(discovery.Specs) != 1 {
		t.Fatalf("specs=%+v want 1", discovery.Specs)
	}
	spec := discovery.Specs[0]
	if spec.Kind != DiscoveredKindFilesystem || spec.Path != filepath.Join(root, "agent") {
		t.Fatalf("unexpected filesystem spec identity: %+v", spec)
	}
	if spec.Name != "weather-agent" || !spec.Valid {
		t.Fatalf("unexpected filesystem spec: %+v", spec)
	}
	if strings.Join(spec.Subagents, ",") != "agent,researcher" {
		t.Fatalf("subagents=%v", spec.Subagents)
	}
	var warned bool
	for _, diag := range spec.Diagnostics {
		if diag.Path == "tools" && strings.Contains(diag.Message, "does not execute this slot yet") {
			warned = true
			break
		}
	}
	if !warned {
		t.Fatalf("expected unsupported tools slot warning, got:%+v", spec.Diagnostics)
	}
}

func TestLoadFilesystemRuntimeProfile(t *testing.T) {
	root := t.TempDir()
	writeFilesystemFile(t, filepath.Join(root, "package.json"), `{"name":"daily-agent"}`)
	writeFilesystemFile(t, filepath.Join(root, "agent", "instructions.md"), "Use the root prompt.")
	writeFilesystemFile(t, filepath.Join(root, "agent", "instructions", "extra.md"), "Use the extra prompt.")
	writeFilesystemFile(t, filepath.Join(root, "agent", "skills", "forecast.md"), "Use weather forecasts carefully.")
	writeFilesystemFile(t, filepath.Join(root, "agent", "skills", "research", "SKILL.md"), "Research before answering.")
	writeFilesystemFile(t, filepath.Join(root, "agent", "skills", "research", "references", "checklist.md"), "This is package support material.")
	writeFilesystemFile(t, filepath.Join(root, "agent", "subagents", "coder", "instructions.md"), "Write focused patches.")
	writeFilesystemFile(t, filepath.Join(root, "agent", "subagents", "coder", "skills", "patching", "SKILL.md"), "Patch in small steps.")
	writeFilesystemFile(t, filepath.Join(root, "agent", "subagents", "coder", "skills", "patching", "references", "checklist.md"), "This is package support material.")

	profile, err := LoadRuntimeProfile(filepath.Join(root, "agent"))
	if err != nil {
		t.Fatalf("LoadRuntimeProfile filesystem layout: %v", err)
	}
	if profile.SourcePath != filepath.Join(root, "agent") {
		t.Fatalf("SourcePath=%q", profile.SourcePath)
	}
	if profile.Spec.Name != "daily-agent" {
		t.Fatalf("agent name=%q", profile.Spec.Name)
	}
	if len(profile.InstructionFiles) != 2 {
		t.Fatalf("instruction files=%+v want 2", profile.InstructionFiles)
	}
	if strings.Join(profile.Spec.Skills, ",") != "forecast,research" {
		t.Fatalf("skills=%v want forecast,research", profile.Spec.Skills)
	}
	sub, err := profile.SubagentProfile("coder")
	if err != nil {
		t.Fatalf("SubagentProfile coder: %v", err)
	}
	if sub.Spec.Name != "daily-agent/coder" {
		t.Fatalf("subagent name=%q", sub.Spec.Name)
	}
	if !strings.Contains(sub.Spec.Instructions.Prompt, "Write focused patches.") {
		t.Fatalf("subagent instructions missing:\n%s", sub.Spec.Instructions.Prompt)
	}
	if strings.Join(sub.Spec.Skills, ",") != "forecast,research,patching" {
		t.Fatalf("subagent skills=%v want forecast,research,patching", sub.Spec.Skills)
	}
}

func TestLoadFilesystemRuntimeProfileRequiresMarkdownInstructions(t *testing.T) {
	root := t.TempDir()
	writeFilesystemFile(t, filepath.Join(root, "agent", "agent.ts"), "export default {}")

	_, err := LoadRuntimeProfile(filepath.Join(root, "agent"))
	if err == nil {
		t.Fatal("expected missing markdown instructions error")
	}
	if !strings.Contains(err.Error(), "markdown instructions not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeFilesystemFile(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(data)+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
