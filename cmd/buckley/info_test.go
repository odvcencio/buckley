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
}

func TestRunInfoCommandTextAndDispatch(t *testing.T) {
	setupInfoTestEnv(t)

	out := captureStdout(t, func() {
		handled, code := dispatchSubcommand([]string{"info"})
		if !handled || code != 0 {
			t.Fatalf("dispatch info handled=%v code=%d", handled, code)
		}
	})
	for _, want := range []string{"Buckley Info", "Project root:", "Tools:", "Use `buckley info --json`"} {
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

func setupInfoTestEnv(t *testing.T) {
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
