package model

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

// TestEnsureConfiguredModels_ExplicitRoleHardFailsWithCloseMatches covers
// H1's real incident: an explicitly configured model ID
// (openai_compatible/glm-5.3-flash, a renamed alias) is absent from the
// live catalog. The manager must fail closed and name a close match
// (openai_compatible/glm5.3flash) instead of silently running the whole
// task on an unrelated model/provider (the real incident: silently became
// cohere/command-a-plus).
func TestEnsureConfiguredModels_ExplicitRoleHardFailsWithCloseMatches(t *testing.T) {
	mgr := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{
				Planning:  "openai_compatible/glm5.3flash",
				Execution: "openai_compatible/glm-5.3-flash", // explicit, e.g. via -m; not the built-in default
				Review:    "openai_compatible/glm5.3flash",
			},
		},
		providers:     map[string]Provider{"openai_compatible": &stubProvider{id: "openai_compatible"}},
		providerOrder: []string{"openai_compatible"},
		catalog: map[string]ModelInfo{
			"openai_compatible/glm5.3flash":         {ID: "openai_compatible/glm5.3flash"},
			"cohere/command-a-plus":                 {ID: "cohere/command-a-plus"},
			"openai_compatible/deepseek-v4.1-flash": {ID: "openai_compatible/deepseek-v4.1-flash"},
		},
		providerModels: map[string][]string{
			"openai_compatible": {"openai_compatible/glm5.3flash", "openai_compatible/deepseek-v4.1-flash"},
		},
		modelProviders: map[string]string{
			"openai_compatible/glm5.3flash":         "openai_compatible",
			"openai_compatible/deepseek-v4.1-flash": "openai_compatible",
			"cohere/command-a-plus":                 "cohere",
		},
	}

	err := mgr.ensureConfiguredModels()
	if err == nil {
		t.Fatal("expected a hard failure for an unresolved explicit execution model")
	}
	if !strings.Contains(err.Error(), "openai_compatible/glm-5.3-flash") {
		t.Fatalf("error = %v, want it to name the requested model", err)
	}
	if !strings.Contains(err.Error(), "openai_compatible/glm5.3flash") {
		t.Fatalf("error = %v, want it to suggest the close match", err)
	}
	if mgr.config.Models.Execution != "openai_compatible/glm-5.3-flash" {
		t.Fatalf("execution model = %q, want it left untouched (no silent substitution)", mgr.config.Models.Execution)
	}
}

// TestEnsureConfiguredModels_ExplicitRoleHardFailsWithoutCatalogMatch covers
// the same hard-fail contract when the catalog has nothing close enough to
// suggest: the error must still name the requested model and must not
// silently substitute, even with no candidate to offer.
func TestEnsureConfiguredModels_ExplicitRoleHardFailsWithoutCatalogMatch(t *testing.T) {
	mgr := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{
				Planning:  "openai_compatible/deepseek-v4.1-flash",
				Execution: "openai_compatible/totally-unregistered-model-xyz",
				Review:    "openai_compatible/deepseek-v4.1-flash",
			},
		},
		providers:     map[string]Provider{"openai_compatible": &stubProvider{id: "openai_compatible"}},
		providerOrder: []string{"openai_compatible"},
		catalog: map[string]ModelInfo{
			"openai_compatible/deepseek-v4.1-flash": {ID: "openai_compatible/deepseek-v4.1-flash"},
		},
		providerModels: map[string][]string{
			"openai_compatible": {"openai_compatible/deepseek-v4.1-flash"},
		},
		modelProviders: map[string]string{
			"openai_compatible/deepseek-v4.1-flash": "openai_compatible",
		},
	}

	err := mgr.ensureConfiguredModels()
	if err == nil {
		t.Fatal("expected a hard failure for an unresolved explicit execution model")
	}
	if !strings.Contains(err.Error(), "openai_compatible/totally-unregistered-model-xyz") {
		t.Fatalf("error = %v, want it to name the requested model", err)
	}
	if mgr.config.Models.Execution != "openai_compatible/totally-unregistered-model-xyz" {
		t.Fatalf("execution model = %q, want it left untouched (no silent substitution)", mgr.config.Models.Execution)
	}
}

// TestEnsureConfiguredModels_ConfigRoleAcrossMultipleProvidersHardFails
// covers the coordinator's live repro: with several providers enabled
// (openrouter and codex here), a config role (planning/review) pointed at
// a valid-looking OpenRouter-namespaced model ID that this manager cannot
// resolve must hard-fail by name, not silently reroute to an unrelated
// provider's default model (the real incident: silently became
// codex/default).
func TestEnsureConfiguredModels_ConfigRoleAcrossMultipleProvidersHardFails(t *testing.T) {
	mgr := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{
				Planning:  "openai/gpt-6-luna-pro", // explicit config role; not resolvable by either provider below
				Execution: "openrouter/some-execution-model",
				Review:    "openai/gpt-6-luna-pro",
			},
		},
		providers: map[string]Provider{
			"openrouter": &stubProvider{id: "openrouter"},
			"codex":      &stubProvider{id: "codex"},
		},
		providerOrder: []string{"codex", "openrouter"},
		catalog: map[string]ModelInfo{
			"openrouter/some-execution-model": {ID: "openrouter/some-execution-model"},
			"codex/default":                   {ID: "codex/default"},
		},
		providerModels: map[string][]string{
			"openrouter": {"openrouter/some-execution-model"},
			"codex":      {"codex/default"},
		},
		modelProviders: map[string]string{
			"openrouter/some-execution-model": "openrouter",
			"codex/default":                   "codex",
		},
	}

	err := mgr.ensureConfiguredModels()
	if err == nil {
		t.Fatal("expected a hard failure for an unresolved planning role across multiple providers")
	}
	if !strings.Contains(err.Error(), "openai/gpt-6-luna-pro") {
		t.Fatalf("error = %v, want it to name the requested planning model", err)
	}
	if mgr.config.Models.Planning != "openai/gpt-6-luna-pro" {
		t.Fatalf("planning model = %q, want it left untouched, not rerouted to codex/default", mgr.config.Models.Planning)
	}
}

// TestEnsureConfiguredModels_BuiltInDefaultStillFallsBackLoudly is the
// control case: a role left at its compiled-in default (never explicitly
// configured) may still fall back automatically when unavailable, as long
// as it says so loudly (a warning), preserving today's zero-config UX.
func TestEnsureConfiguredModels_BuiltInDefaultStillFallsBackLoudly(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.Planning = config.DefaultPlanningModel
	cfg.Models.Execution = config.DefaultExecutionModel
	cfg.Models.Review = config.DefaultReviewModel
	mgr := &Manager{
		config:        cfg,
		providers:     map[string]Provider{"openai_compatible": &stubProvider{id: "openai_compatible"}},
		providerOrder: []string{"openai_compatible"},
		catalog: map[string]ModelInfo{
			"openai_compatible/deepseek-v4.1-flash": {ID: "openai_compatible/deepseek-v4.1-flash"},
		},
		providerModels: map[string][]string{
			"openai_compatible": {"openai_compatible/deepseek-v4.1-flash"},
		},
		modelProviders: map[string]string{
			"openai_compatible/deepseek-v4.1-flash": "openai_compatible",
		},
	}

	if err := mgr.ensureConfiguredModels(); err != nil {
		t.Fatalf("ensureConfiguredModels() = %v, want the built-in default to fall back without error", err)
	}
	if mgr.config.Models.Execution != "openai_compatible/deepseek-v4.1-flash" {
		t.Fatalf("execution model = %q, want the fallback applied", mgr.config.Models.Execution)
	}
}

// TestEnsureConfiguredModels_UnsetRoleStillFallsBackLoudly covers the other
// implicit-default shape: a role left entirely empty.
func TestEnsureConfiguredModels_UnsetRoleStillFallsBackLoudly(t *testing.T) {
	mgr := &Manager{
		config: &config.Config{},
		providers: map[string]Provider{
			"openai_compatible": &stubProvider{id: "openai_compatible"},
		},
		providerOrder: []string{"openai_compatible"},
		catalog: map[string]ModelInfo{
			"openai_compatible/deepseek-v4.1-flash": {ID: "openai_compatible/deepseek-v4.1-flash"},
		},
		providerModels: map[string][]string{
			"openai_compatible": {"openai_compatible/deepseek-v4.1-flash"},
		},
		modelProviders: map[string]string{
			"openai_compatible/deepseek-v4.1-flash": "openai_compatible",
		},
	}

	if err := mgr.ensureConfiguredModels(); err != nil {
		t.Fatalf("ensureConfiguredModels() = %v, want an unset role to fall back without error", err)
	}
	if mgr.config.Models.Planning == "" {
		t.Fatal("planning model was not filled in by the fallback")
	}
}

// TestModelAvailable_ResolvesThroughRoutingHookAlias covers a companion
// case to H1: an alias absent from the static catalog must not be treated
// as unresolvable when a routing hook maps it to a real, catalog-known
// model at dispatch time. Confusing "not literally in the catalog" with
// "cannot be dispatched at all" would make H1's hard-fail guard reject a
// perfectly working alias route.
func TestModelAvailable_ResolvesThroughRoutingHookAlias(t *testing.T) {
	mgr := &Manager{
		config: &config.Config{},
		providers: map[string]Provider{
			"openai_compatible": &stubProvider{id: "openai_compatible"},
		},
		providerOrder: []string{"openai_compatible"},
		catalog: map[string]ModelInfo{
			"openai_compatible/future-model": {ID: "openai_compatible/future-model"},
		},
		providerModels: map[string][]string{
			"openai_compatible": {"openai_compatible/future-model"},
		},
		modelProviders: map[string]string{
			"openai_compatible/future-model": "openai_compatible",
		},
		routingHooks: NewRoutingHooks(),
	}

	if mgr.modelAvailable("alias/future") {
		t.Fatal("modelAvailable(\"alias/future\") = true before any hook resolves it, want false")
	}

	mgr.RoutingHooks().Register(func(decision *RoutingDecision) *RoutingDecision {
		if decision != nil && decision.RequestedModel == "alias/future" {
			decision.SelectedModel = "openai_compatible/future-model"
		}
		return decision
	})

	if !mgr.modelAvailable("alias/future") {
		t.Fatal("modelAvailable(\"alias/future\") = false after a hook resolves it to a known model, want true")
	}

	mgr.config.Models.Execution = "alias/future"
	if err := mgr.ensureConfiguredModels(); err != nil {
		t.Fatalf("ensureConfiguredModels() = %v, want the hook-resolved alias accepted", err)
	}
	if mgr.config.Models.Execution != "alias/future" {
		t.Fatalf("execution model = %q, want the alias preserved (it resolves at dispatch time)", mgr.config.Models.Execution)
	}
}

func TestClosestModelMatches(t *testing.T) {
	mgr := &Manager{
		catalog: map[string]ModelInfo{
			"openai_compatible/glm5.3flash":         {ID: "openai_compatible/glm5.3flash"},
			"openai_compatible/deepseek-v4.1-flash": {ID: "openai_compatible/deepseek-v4.1-flash"},
			"cohere/command-a-plus":                 {ID: "cohere/command-a-plus"},
			"anthropic/claude-opus-5":               {ID: "anthropic/claude-opus-5"},
		},
	}

	matches := mgr.closestModelMatches("openai_compatible/glm-5.3-flash", 3)
	if len(matches) == 0 || matches[0] != "openai_compatible/glm5.3flash" {
		t.Fatalf("matches = %v, want openai_compatible/glm5.3flash first", matches)
	}
	for _, m := range matches {
		if m == "cohere/command-a-plus" {
			t.Fatalf("matches = %v, want the unrelated cohere model excluded", matches)
		}
	}

	if got := mgr.closestModelMatches("completely-unrelated-nonsense-id-zzz", 3); len(got) != 0 {
		t.Fatalf("matches = %v, want none for a wildly unrelated ID", got)
	}

	if got := mgr.closestModelMatches("", 3); got != nil {
		t.Fatalf("matches = %v, want nil for an empty request", got)
	}
}

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "", 3},
		{"glm-5.3-flash", "glm5.3flash", 2},
		{"kitten", "sitting", 3},
	}
	for _, tt := range tests {
		if got := levenshteinDistance(tt.a, tt.b); got != tt.want {
			t.Errorf("levenshteinDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

// TestEnsureModel_NoFallbackAvailableNamesTheRequestedModel keeps the
// pre-existing "no fallback available" hard-fail path intact and legible
// after the H1 change: even with zero candidates to suggest, the error
// must still name the requested model.
func TestEnsureModel_NoFallbackAvailableNamesTheRequestedModel(t *testing.T) {
	mgr := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{
				Execution: "openai_compatible/glm-5.3-flash",
			},
		},
		providers:      map[string]Provider{},
		providerOrder:  nil,
		catalog:        map[string]ModelInfo{},
		providerModels: map[string][]string{},
		modelProviders: map[string]string{},
	}
	_, err := mgr.ensureModel("execution")
	if err == nil {
		t.Fatal("expected a hard failure with no providers configured")
	}
	if !strings.Contains(err.Error(), "openai_compatible/glm-5.3-flash") {
		t.Fatalf("error = %v, want it to name the requested model", err)
	}
}
