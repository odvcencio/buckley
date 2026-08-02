package main

import (
	"testing"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/config"
)

// TestBuildACPModelConfigOptions_ListsCuratedModels locks S8: the model
// picker config option must list every curated model and default its
// currentValue to the first one when the session has not selected a model
// yet (session.Mode still "normal", the AgentSession zero-state default).
func TestBuildACPModelConfigOptions_ListsCuratedModels(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Models.Curated = []string{"openai/gpt-4o", "anthropic/claude"}
	cfg.Models.Execution = "openai/gpt-4o"

	session := &acp.AgentSession{Mode: "normal"}
	options := buildACPModelConfigOptions(cfg, nil, session)

	if len(options) != 1 {
		t.Fatalf("options = %+v, want 1 entry", options)
	}
	opt := options[0]
	if opt.ID != acpModelConfigID {
		t.Fatalf("ID = %q, want %q", opt.ID, acpModelConfigID)
	}
	if opt.Category != acp.SessionConfigCategoryModel {
		t.Fatalf("Category = %q, want %q", opt.Category, acp.SessionConfigCategoryModel)
	}
	if opt.Type != acp.SessionConfigKindSelect {
		t.Fatalf("Type = %q, want %q", opt.Type, acp.SessionConfigKindSelect)
	}
	if opt.CurrentValue != "openai/gpt-4o" {
		t.Fatalf("CurrentValue = %v, want openai/gpt-4o", opt.CurrentValue)
	}
	if len(opt.Options) != 2 {
		t.Fatalf("Options = %+v, want 2 entries", opt.Options)
	}
}

// TestBuildACPModelConfigOptions_ReflectsSessionMode locks the shared
// source of truth: when session.Mode already selects a model (e.g. via
// session/set_mode), the config option's currentValue must match it, so a
// client mixing modes and config options never sees a stale value.
func TestBuildACPModelConfigOptions_ReflectsSessionMode(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Models.Curated = []string{"openai/gpt-4o", "anthropic/claude"}

	session := &acp.AgentSession{Mode: acpModePrefix + "anthropic/claude"}
	options := buildACPModelConfigOptions(cfg, nil, session)

	if len(options) != 1 || options[0].CurrentValue != "anthropic/claude" {
		t.Fatalf("options = %+v, want currentValue anthropic/claude", options)
	}
}

// TestBuildACPModelConfigOptions_NoCuratedModelsReturnsNil matches
// buildACPModelModes' behavior: no curated models means no option is
// advertised at all, not an empty/broken one.
func TestBuildACPModelConfigOptions_NoCuratedModelsReturnsNil(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Models.Curated = nil
	cfg.Models.Execution = ""
	cfg.Models.Planning = ""
	cfg.Models.Review = ""

	if options := buildACPModelConfigOptions(cfg, nil, &acp.AgentSession{}); options != nil {
		t.Fatalf("options = %+v, want nil", options)
	}
}

// TestApplyACPSetModelConfigOption_UpdatesSessionMode locks S8's write
// path: picking a model via session/set_config_option must update
// session.Mode -- the same field runACPLoop resolves the active model
// from -- and echo back the updated option set with the new currentValue.
func TestApplyACPSetModelConfigOption_UpdatesSessionMode(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Models.Curated = []string{"openai/gpt-4o", "anthropic/claude"}

	session := &acp.AgentSession{Mode: "normal"}
	options, err := applyACPSetModelConfigOption(cfg, nil, session, acp.ConfigOptionValue{ValueID: "anthropic/claude"})
	if err != nil {
		t.Fatalf("applyACPSetModelConfigOption: %v", err)
	}

	wantMode := acpModePrefix + "anthropic/claude"
	if session.Mode != wantMode {
		t.Fatalf("session.Mode = %q, want %q", session.Mode, wantMode)
	}
	if len(options) != 1 || options[0].CurrentValue != "anthropic/claude" {
		t.Fatalf("options = %+v, want currentValue anthropic/claude", options)
	}

	if got := resolveACPModelOverride(cfg, nil, session.Mode); got != "anthropic/claude" {
		t.Fatalf("resolveACPModelOverride after set_config_option = %q, want anthropic/claude", got)
	}
}

// TestApplyACPSetModelConfigOption_RejectsEmptyValue guards against a
// malformed set_config_option request silently clearing the active model.
func TestApplyACPSetModelConfigOption_RejectsEmptyValue(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Models.Curated = []string{"openai/gpt-4o"}
	session := &acp.AgentSession{Mode: "normal"}

	if _, err := applyACPSetModelConfigOption(cfg, nil, session, acp.ConfigOptionValue{}); err == nil {
		t.Fatal("expected an error for an empty model value id")
	}
	if session.Mode != "normal" {
		t.Fatalf("session.Mode = %q, want unchanged (normal) after a rejected update", session.Mode)
	}
}
