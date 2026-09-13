package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/modelprofile"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/protocol"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
)

func TestParseBoolEnv(t *testing.T) {
	t.Setenv("BUCKLEY_QUIET", "true")
	val, ok := parseBoolEnv("BUCKLEY_QUIET")
	if !ok || !val {
		t.Fatalf("expected true,true got %v,%v", val, ok)
	}

	t.Setenv("BUCKLEY_QUIET", "0")
	val, ok = parseBoolEnv("BUCKLEY_QUIET")
	if !ok || val {
		t.Fatalf("expected false,true got %v,%v", val, ok)
	}

	t.Setenv("BUCKLEY_QUIET", "maybe")
	_, ok = parseBoolEnv("BUCKLEY_QUIET")
	if ok {
		t.Fatalf("expected ok=false for invalid value")
	}
}

func TestCompileOneShotAdaptiveProtocolFromVersionedProfile(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AdaptiveProtocol.Mode = "dynamic"
	cfg.AdaptiveProtocol.PolicyVersion = "eval-policy-v1"
	cfg.AdaptiveProtocol.AutoCodeMode = true
	cfg.AdaptiveProtocol.Profiles = map[string]config.ModelBehaviorProfileConfig{"example/model": {
		Version:                     "eval-2026-08-11",
		Class:                       "frontier",
		SampleSize:                  100,
		Confidence:                  0.95,
		MeasuredAt:                  "2026-08-11T00:00:00Z",
		ToolCalls:                   true,
		ParallelToolCalls:           true,
		Continuation:                true,
		Reasoning:                   true,
		ReasoningEfforts:            []string{" HIGH ", "low", "medium"},
		CodeMode:                    true,
		SafeVisibleToolCount:        10,
		ToolReliability:             0.95,
		StructuredOutputReliability: 0.96,
		ParallelCallReliability:     0.96,
		ContinuationReliability:     0.96,
	}}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("NewDefaultEngine: %v", err)
	}
	compiled, ok := compileOneShotAdaptiveProtocol(cfg, nil, nil, engine, "example/model", tool.NewRegistry(), "")
	if !ok || compiled == nil {
		t.Fatal("expected configured profile to compile a protocol")
	}
	stage := adaptiveProtocolExecutionStage(*compiled)
	if compiled.Mode != "dynamic" || compiled.Receipt.PolicyVersion != "eval-policy-v1" || compiled.Receipt.PolicyOutcome != "frontier_horizon" || stage.MaxFanout != 1 || stage.CodeMode != "suggest" {
		t.Fatalf("unexpected one-shot protocol: %+v", compiled)
	}
	if stage.Request != (protocol.RequestPolicy{ReasoningEffort: "high", ReasoningMaxTokens: 8192, MaxOutputTokens: 16384}) || stage.MaxVerificationAttempts != 3 {
		t.Fatalf("unexpected one-shot request envelope: %+v", stage)
	}
	if stage.ReadOnlyWarningAt != 8 || stage.ReadOnlyActionAt != 14 || stage.MaxReadOnlyCalls != 20 {
		t.Fatalf("unexpected one-shot read-only reserve: %+v", stage)
	}
	profile, err := protocolProfileFromConfig("example/model", nil, cfg.AdaptiveProtocol.Profiles["example/model"])
	if err != nil {
		t.Fatalf("protocolProfileFromConfig: %v", err)
	}
	if want := []string{"low", "medium", "high"}; !reflect.DeepEqual(profile.Capabilities.ReasoningEfforts, want) {
		t.Fatalf("reasoning efforts = %v, want %v", profile.Capabilities.ReasoningEfforts, want)
	}
	wantTools := []string{"read_file", "search_text", "code_impact", "code_refs", "apply_patch", "run_tests", "git_diff", "git_status", "find_files", "code_callgraph"}
	if !reflect.DeepEqual(compiled.VisibleTools, wantTools) {
		t.Fatalf("one-shot protocol tools = %v, want %v", compiled.VisibleTools, wantTools)
	}
}

func TestApplyOneShotProtocolLimits_DynamicClampsModelAndChildCaps(t *testing.T) {
	compiled := &protocol.Protocol{Mode: protocol.ModeDynamic, Stages: []protocol.Stage{{
		MaxTurns:                20,
		VerificationDepth:       "focused",
		MaxVerificationAttempts: 2,
		ReadOnlyWarningAt:       3,
		ReadOnlyActionAt:        5,
		MaxReadOnlyCalls:        9,
		Request: protocol.RequestPolicy{
			ReasoningEffort:    "medium",
			ReasoningMaxTokens: 2048,
			MaxOutputTokens:    6144,
		},
	}}}
	mgr, modelID := newOneShotLimitTestManager(t, 4096)

	limits := applyOneShotProtocolLimits(acpLoopLimits{}, compiled, mgr, modelID)
	if limits.MaxOutputTokens != 4096 {
		t.Fatalf("model completion limit not applied: %+v", limits)
	}
	if limits.ReadOnlyWarningAt != 3 || limits.ReadOnlyActionAt != 5 || limits.MaxReadOnlyCalls != 9 {
		t.Fatalf("read-only reserve not applied: %+v", limits)
	}

	limits = applyOneShotProtocolLimits(acpLoopLimits{
		StepCap:           7,
		MaxOutputTokens:   2048,
		ReadOnlyWarningAt: 9,
		ReadOnlyActionAt:  10,
		MaxReadOnlyCalls:  11,
		ChildContract:     true,
	}, compiled, mgr, modelID)

	if limits.StepCap != 7 || limits.MaxOutputTokens != 2048 {
		t.Fatalf("explicit child limits expanded: %+v", limits)
	}
	if limits.ReadOnlyWarningAt != 3 || limits.ReadOnlyActionAt != 5 || limits.MaxReadOnlyCalls != 9 {
		t.Fatalf("protocol read-only reserve = %+v, want 3/5/9", limits)
	}
	if limits.VerificationDepth != "focused" || limits.MaxVerificationAttempts != 2 {
		t.Fatalf("protocol verification contract = %q/%d, want focused/2", limits.VerificationDepth, limits.MaxVerificationAttempts)
	}
	if limits.ProtocolReasoning == nil || limits.ProtocolReasoning.Effort != "medium" || limits.ProtocolReasoning.MaxTokens != 2048 {
		t.Fatalf("protocol reasoning = %+v", limits.ProtocolReasoning)
	}
}

func TestApplyOneShotProtocolLimits_ClampsReasoningToEffectiveOutput(t *testing.T) {
	compiled := &protocol.Protocol{Mode: protocol.ModeDynamic, Stages: []protocol.Stage{{
		Request: protocol.RequestPolicy{
			ReasoningEffort:    "medium",
			ReasoningMaxTokens: 4096,
			MaxOutputTokens:    8192,
		},
	}}}
	mgr, modelID := newOneShotLimitTestManager(t, 2048)

	limits := applyOneShotProtocolLimits(acpLoopLimits{}, compiled, mgr, modelID)
	if limits.MaxOutputTokens != 2048 {
		t.Fatalf("MaxOutputTokens = %d, want 2048", limits.MaxOutputTokens)
	}
	if limits.ProtocolReasoning == nil || limits.ProtocolReasoning.MaxTokens != 2048 {
		t.Fatalf("ProtocolReasoning = %+v, want max tokens 2048", limits.ProtocolReasoning)
	}
	if compiled.Stages[0].Request.ReasoningMaxTokens != 4096 {
		t.Fatalf("compiled protocol mutated: %+v", compiled.Stages[0].Request)
	}
}

func TestApplyOneShotProtocolLimits_OutputOnlyPolicyPreservesReasoningFallback(t *testing.T) {
	compiled := &protocol.Protocol{Mode: protocol.ModeDynamic, Stages: []protocol.Stage{{
		Request: protocol.RequestPolicy{MaxOutputTokens: 4096},
	}}}
	limits := applyOneShotProtocolLimits(acpLoopLimits{}, compiled, nil, "")
	if limits.MaxOutputTokens != 4096 {
		t.Fatalf("MaxOutputTokens = %d, want 4096", limits.MaxOutputTokens)
	}
	if limits.ProtocolReasoning != nil {
		t.Fatalf("ProtocolReasoning = %+v, want nil for output-only policy", limits.ProtocolReasoning)
	}
}

func newOneShotLimitTestManager(t *testing.T, maxCompletionTokens int) (*model.Manager, string) {
	t.Helper()
	const localModel = "test-model"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"test-model","max_completion_tokens":`+strconv.Itoa(maxCompletionTokens)+`}]}`)
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers = config.ProviderConfig{OpenAICompatible: config.OpenAICompatibleConfig{
		Enabled: true,
		BaseURL: server.URL,
		Models:  []string{localModel},
	}}
	cfg.Models.DefaultProvider = "openai_compatible"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("model.NewManager: %v", err)
	}
	return mgr, "openai_compatible/" + localModel
}

func TestApplyOneShotProtocolLimits_LegacyShadowAndZeroPolicyAreInert(t *testing.T) {
	mgr, modelID := newOneShotLimitTestManager(t, 4096)
	original := acpLoopLimits{StepCap: 5, MaxOutputTokens: 8192, ChildContract: true}
	for _, compiled := range []*protocol.Protocol{
		nil,
		{Mode: protocol.ModeLegacy, Stages: []protocol.Stage{{MaxTurns: 1, Request: protocol.RequestPolicy{MaxOutputTokens: 1}}}},
		{Mode: protocol.ModeShadow, Stages: []protocol.Stage{{MaxTurns: 1, Request: protocol.RequestPolicy{MaxOutputTokens: 1}}}},
		{Mode: protocol.ModeDynamic, Stages: []protocol.Stage{{}}},
	} {
		if got := applyOneShotProtocolLimits(original, compiled, mgr, modelID); !reflect.DeepEqual(got, original) {
			t.Fatalf("non-applicable protocol changed limits: got %+v want %+v", got, original)
		}
	}
}

func TestOneShotProtocolExecutionContract_DynamicOnly(t *testing.T) {
	t.Run("architect editor split is one gated loop", func(t *testing.T) {
		compiled := &protocol.Protocol{Mode: protocol.ModeDynamic, Stages: []protocol.Stage{{
			ArchitectEditorSplit:    true,
			MaxVerificationAttempts: 3,
			ReadOnlyWarningAt:       3,
			ReadOnlyActionAt:        5,
			MaxReadOnlyCalls:        9,
		}}}
		got := oneShotProtocolExecutionContract(compiled)
		for _, want := range []string{"one agent loop", "not separate stage calls", "plan before editing", "requested scope", "up to 3 attempts", "cheapest relevant checks", "verify material claims", "If checks are unavailable", "do not claim success"} {
			if !strings.Contains(got, want) {
				t.Fatalf("execution contract missing %q: %q", want, got)
			}
		}
		for _, removed := range []string{"Broad discovery is budgeted", "bounded edit-argument repair window", "not permission to broaden discovery again"} {
			if strings.Contains(got, removed) {
				t.Fatalf("execution contract retained duplicated reserve policy %q: %q", removed, got)
			}
		}
	})

	t.Run("non split stays concise", func(t *testing.T) {
		compiled := &protocol.Protocol{Mode: protocol.ModeDynamic, Stages: []protocol.Stage{{MaxVerificationAttempts: 2}}}
		got := oneShotProtocolExecutionContract(compiled)
		for _, want := range []string{"requested scope", "up to 2 attempts", "verify material claims"} {
			if !strings.Contains(got, want) {
				t.Fatalf("execution contract missing %q: %q", want, got)
			}
		}
		if strings.Contains(got, "parallel") || strings.Contains(got, "separate stage") {
			t.Fatalf("non-split contract claims unimplemented execution shape: %q", got)
		}
	})

	for _, mode := range []string{protocol.ModeLegacy, protocol.ModeShadow} {
		compiled := &protocol.Protocol{Mode: mode, Stages: []protocol.Stage{{MaxVerificationAttempts: 3}}}
		if got := oneShotProtocolExecutionContract(compiled); got != "" {
			t.Fatalf("%s execution contract = %q, want empty", mode, got)
		}
	}
}

func TestApplyOneShotTaskIntentDefaults_ReadOnlyIsBoundedSummary(t *testing.T) {
	got := applyOneShotTaskIntentDefaults(acpLoopLimits{
		TaskIntent:        agentloop.ReadOnlyIntent,
		ReadOnlyWarningAt: 3,
		ReadOnlyActionAt:  5,
		MaxReadOnlyCalls:  9,
	})
	if got.MaxToolCalls != defaultReadOnlyToolCalls || got.MaxElapsedSeconds != defaultReadOnlyElapsedSeconds || got.MaxOutputTokens != defaultReadOnlyOutputTokens {
		t.Fatalf("read-only defaults = %+v, want tool=%d elapsed=%d output=%d", got, defaultReadOnlyToolCalls, defaultReadOnlyElapsedSeconds, defaultReadOnlyOutputTokens)
	}
	if got.ReadOnlyWarningAt != 0 || got.ReadOnlyActionAt != 0 || got.MaxReadOnlyCalls != 0 {
		t.Fatalf("read-only reserve remained active: %+v", got)
	}

	explicit := acpLoopLimits{
		TaskIntent:        agentloop.ReadOnlyIntent,
		MaxToolCalls:      4,
		MaxElapsedSeconds: 30,
		MaxOutputTokens:   512,
		ReadOnlyWarningAt: 3,
		ReadOnlyActionAt:  5,
		MaxReadOnlyCalls:  9,
	}
	got = applyOneShotTaskIntentDefaults(explicit)
	if got.MaxToolCalls != 4 || got.MaxElapsedSeconds != 30 || got.MaxOutputTokens != 512 {
		t.Fatalf("explicit read-only limits were loosened: %+v", got)
	}
}

func TestOneShotTaskIntentInstruction_ReadOnlySummary(t *testing.T) {
	got := oneShotTaskIntentInstruction(agentloop.ReadOnlyIntent)
	for _, want := range []string{"relevant", "concise summary", "investigator or orchestrator", "paths", "uncertainty", "do not edit"} {
		if !strings.Contains(got, want) {
			t.Fatalf("read-only instruction missing %q: %q", want, got)
		}
	}
	if oneShotTaskIntentInstruction(agentloop.MutationIntent) != "" {
		t.Fatal("mutation intent unexpectedly received read-only instruction")
	}
}

func TestAgentRunTaskIntentInfersReadOnlyFromToolTier(t *testing.T) {
	for _, tt := range []struct {
		tier string
		want agentloop.TaskIntent
	}{
		{tier: "none", want: agentloop.ReadOnlyIntent},
		{tier: "read_only", want: agentloop.ReadOnlyIntent},
		{tier: "standard", want: agentloop.UnknownIntent},
		{tier: "full", want: agentloop.UnknownIntent},
	} {
		t.Run(tt.tier, func(t *testing.T) {
			profile := &agentspec.RuntimeProfile{Spec: &agentspec.Spec{
				Tools: agentspec.ToolSpec{Tier: tt.tier},
			}}
			if got := agentRunTaskIntent(agentRunOptions{}, profile); got != tt.want {
				t.Fatalf("tier %q inferred intent %q, want %q", tt.tier, got, tt.want)
			}
		})
	}
	explicit := agentRunOptions{taskIntent: agentloop.MutationIntent, taskIntentSet: true}
	profile := &agentspec.RuntimeProfile{Spec: &agentspec.Spec{
		Tools: agentspec.ToolSpec{Tier: "read_only"},
	}}
	if got := agentRunTaskIntent(explicit, profile); got != agentloop.MutationIntent {
		t.Fatalf("explicit intent was overridden: got %q", got)
	}
}

func TestOneShotProtocolToolFiltersPreserveControlTools(t *testing.T) {
	if got := applyProtocolToolFilter(nil, []string{"read_file", "find_files"}); !reflect.DeepEqual(got, []string{"read_file", "find_files"}) {
		t.Fatalf("unrestricted protocol filter = %v", got)
	}
	if got := applyProtocolToolFilter([]string{"read_file", "write_file"}, []string{"read_file", "find_files"}); !reflect.DeepEqual(got, []string{"read_file"}) {
		t.Fatalf("explicit protocol filter = %v", got)
	}
	if got := ensureRequiredOneShotTools([]string{}, true, true); !reflect.DeepEqual(got, []string{"submit_artifact", "exec_program"}) {
		t.Fatalf("required control tools = %v", got)
	}
}

func TestRemoveImplicitExternalCLIModelTools(t *testing.T) {
	t.Run("api one-shot stays on its model transport", func(t *testing.T) {
		registry := tool.NewRegistry()
		removeImplicitExternalCLIModelTools(registry, nil, nil)
		for _, name := range []string{"invoke_claude", "invoke_codex"} {
			if _, ok := registry.Get(name); ok {
				t.Fatalf("%s remains available without an explicit opt-in", name)
			}
		}
		if _, ok := registry.Get("invoke_buckley"); !ok {
			t.Fatal("API-native Buckley delegation was removed")
		}
	})

	t.Run("agent profile can opt into one exact CLI", func(t *testing.T) {
		registry := tool.NewRegistry()
		profile := &agentspec.RuntimeProfile{Spec: &agentspec.Spec{Tools: agentspec.ToolSpec{
			Allow: []string{"invoke_claude"},
		}}}
		removeImplicitExternalCLIModelTools(registry, profile, nil)
		if _, ok := registry.Get("invoke_claude"); !ok {
			t.Fatal("explicitly allowed invoke_claude was removed")
		}
		if _, ok := registry.Get("invoke_codex"); ok {
			t.Fatal("unrequested invoke_codex remains available")
		}
	})

	t.Run("child contract allowlist can opt in", func(t *testing.T) {
		registry := tool.NewRegistry()
		removeImplicitExternalCLIModelTools(registry, nil, []string{"invoke_codex"})
		if _, ok := registry.Get("invoke_codex"); !ok {
			t.Fatal("explicitly allowed invoke_codex was removed")
		}
		if _, ok := registry.Get("invoke_claude"); ok {
			t.Fatal("unrequested invoke_claude remains available")
		}
	})
}

func TestOneShotBehaviorProfileRequiresPromotionForDynamicMode(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AdaptiveProtocol.Mode = "dynamic"
	store, err := storage.New(":memory:")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()
	profile := protocol.BehaviorProfile{
		SchemaVersion: protocol.ProfileSchemaVersion,
		ModelID:       "stored/model",
		Version:       "eval-v3",
		Class:         protocol.ClassWeak,
		SampleSize:    30,
		Confidence:    0.9,
		MeasuredAt:    time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC),
		Capabilities:  protocol.Capabilities{ToolCalls: true},
		Metrics: protocol.BehaviorMetrics{
			ToolReliability:             0.8,
			ArgumentRepairReliability:   0.8,
			StructuredOutputReliability: 0.8,
			ParallelCallReliability:     0.8,
			EditFidelity:                0.8,
			VerificationPassRate:        0.8,
			ContinuationReliability:     0.8,
		},
		Review: &protocol.ReviewBehavior{
			Profile:                  modelprofile.ReviewProfileStructuredCodeReview,
			SupportingContextTokens:  45678,
			ReasoningMaxTokensBySize: map[string]int{"focused": 2222},
		},
	}
	if err := storage.NewBehaviorProfileStore(store).Put(context.Background(), profile); err != nil {
		t.Fatalf("persist profile: %v", err)
	}
	got, found, err := oneShotBehaviorProfile(cfg, nil, store, "stored/model")
	if err != nil || found {
		t.Fatalf("unpromoted dynamic profile = %+v, %v, %v; want unavailable", got, found, err)
	}
	if err := storage.NewBehaviorProfileStore(store).Promote(context.Background(), "stored/model", "eval-v3"); err != nil {
		t.Fatalf("promote profile: %v", err)
	}
	got, found, err = oneShotBehaviorProfile(cfg, nil, store, "stored/model")
	if err != nil || !found || got.Version != "eval-v3" ||
		got.Review == nil || got.Review.Profile != modelprofile.ReviewProfileStructuredCodeReview ||
		got.Review.SupportingContextTokens != 45678 ||
		got.Review.ReasoningMaxTokensBySize["focused"] != 2222 {
		t.Fatalf("oneShotBehaviorProfile = %+v, %v, %v", got, found, err)
	}

	newer := profile
	newer.Version = "eval-v4"
	newer.MeasuredAt = newer.MeasuredAt.Add(time.Hour)
	if err := storage.NewBehaviorProfileStore(store).Put(context.Background(), newer); err != nil {
		t.Fatalf("persist newer profile: %v", err)
	}
	got, found, err = oneShotBehaviorProfile(cfg, nil, store, "stored/model")
	if err != nil || !found || got.Version != "eval-v3" {
		t.Fatalf("dynamic profile followed unpromoted latest: %+v, %v, %v", got, found, err)
	}

	cfg.AdaptiveProtocol.Mode = "shadow"
	got, found, err = oneShotBehaviorProfile(cfg, nil, store, "stored/model")
	if err != nil || !found || got.Version != "eval-v4" {
		t.Fatalf("shadow profile = %+v, %v, %v; want latest candidate", got, found, err)
	}

	cfg.AdaptiveProtocol.Mode = "legacy"
	got, found, err = oneShotBehaviorProfile(cfg, nil, store, "stored/model")
	if err != nil || found {
		t.Fatalf("legacy durable profile = %+v, %v, %v; want unavailable", got, found, err)
	}

	cfg.AdaptiveProtocol.Mode = "dynamic"
	cfg.AdaptiveProtocol.Profiles = map[string]config.ModelBehaviorProfileConfig{
		"stored/model": {
			Version:          "config-v1",
			Class:            "weak",
			SampleSize:       1,
			Confidence:       0.5,
			MeasuredAt:       "2026-08-11T02:00:00Z",
			ToolCalls:        true,
			Reasoning:        true,
			ReasoningEfforts: []string{"low"},
			Review: config.ReviewBehaviorProfileConfig{
				Profile:                    modelprofile.ReviewProfileEvidenceFirst,
				WorkflowRiskSignals:        true,
				ReasoningMaxTokensBySize:   map[string]int{"focused": 2345},
				ReasoningMaxTokensByEffort: map[string]int{"low": 1234},
			},
		},
	}
	got, found, err = oneShotBehaviorProfile(cfg, nil, store, "stored/model")
	if err != nil || !found || got.Version != "config-v1" {
		t.Fatalf("explicit config did not outrank promoted profile: %+v, %v, %v", got, found, err)
	}
	if got.Review == nil || got.Review.Profile != modelprofile.ReviewProfileEvidenceFirst || !got.Review.WorkflowRiskSignals ||
		got.Review.ReasoningMaxTokensBySize["focused"] != 2345 ||
		got.Review.ReasoningMaxTokensByEffort["low"] != 1234 {
		t.Fatalf("explicit config review behavior = %+v", got.Review)
	}
	cfg.AdaptiveProtocol.Profiles["stored/model"] = config.ModelBehaviorProfileConfig{
		Version:    "bad-config-v1",
		Confidence: 0.5,
		Review: config.ReviewBehaviorProfileConfig{
			Profile: "family-name",
		},
	}
	if _, _, err := oneShotBehaviorProfile(cfg, nil, store, "stored/model"); err == nil {
		t.Fatalf("invalid configured review behavior was silently accepted")
	}
}

func TestReviewBehaviorProfileKeepsShadowCandidatesInert(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AdaptiveProtocol.Mode = "shadow"
	store, err := storage.New(":memory:")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()
	profile := protocol.BehaviorProfile{
		SchemaVersion: protocol.ProfileSchemaVersion,
		ModelID:       "stored/reviewer",
		Version:       "eval-v1",
		Confidence:    0.9,
		MeasuredAt:    time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC),
		Review: &protocol.ReviewBehavior{
			Profile: modelprofile.ReviewProfileEvidenceFirst,
		},
	}
	if err := storage.NewBehaviorProfileStore(store).Put(context.Background(), profile); err != nil {
		t.Fatalf("persist profile: %v", err)
	}
	if got, found, err := reviewBehaviorProfile(cfg, nil, store, "stored/reviewer", false); err != nil || found || got != nil {
		t.Fatalf("shadow review behavior = %+v, %v, %v; want inert", got, found, err)
	}

	cfg.AdaptiveProtocol.Mode = "dynamic"
	if got, found, err := reviewBehaviorProfile(cfg, nil, store, "stored/reviewer", false); err != nil || found || got != nil {
		t.Fatalf("unpromoted dynamic review behavior = %+v, %v, %v; want inert", got, found, err)
	}
	if err := storage.NewBehaviorProfileStore(store).Promote(context.Background(), "stored/reviewer", "eval-v1"); err != nil {
		t.Fatalf("promote profile: %v", err)
	}
	got, found, err := reviewBehaviorProfile(cfg, nil, store, "stored/reviewer", false)
	if err != nil || !found || got == nil || got.Profile != modelprofile.ReviewProfileEvidenceFirst {
		t.Fatalf("promoted dynamic review behavior = %+v, %v, %v", got, found, err)
	}
}

func TestReviewBehaviorProfileConfiguredOverridesRequireDynamicFixedModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AdaptiveProtocol.Profiles = map[string]config.ModelBehaviorProfileConfig{
		"local/reviewer": {
			Version:    "config-v1",
			Confidence: 0.9,
			Review: config.ReviewBehaviorProfileConfig{
				Profile:                  modelprofile.ReviewProfileEvidenceFirst,
				ReasoningMaxTokensBySize: map[string]int{"focused": 3456},
			},
		},
	}
	for _, mode := range []string{"legacy", "shadow", ""} {
		t.Run(mode, func(t *testing.T) {
			cfg.AdaptiveProtocol.Mode = mode
			if got, found, err := reviewBehaviorProfile(cfg, nil, nil, "local/reviewer", false); err != nil || found || got != nil {
				t.Fatalf("configured %q review behavior = %+v, %v, %v; want inert", mode, got, found, err)
			}
		})
	}
	cfg.AdaptiveProtocol.Mode = "dynamic"
	got, found, err := reviewBehaviorProfile(cfg, nil, nil, "local/reviewer", false)
	if err != nil || !found || got == nil || got.Profile != modelprofile.ReviewProfileEvidenceFirst ||
		got.ReasoningMaxTokensBySize["focused"] != 3456 {
		t.Fatalf("dynamic fixed-model review behavior = %+v, %v, %v", got, found, err)
	}
}

func TestReviewBehaviorProfileDoesNotApplyInitialProfileToAdaptiveCodexModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AdaptiveProtocol.Mode = "dynamic"
	cfg.AdaptiveProtocol.Profiles = map[string]config.ModelBehaviorProfileConfig{
		codexReviewModelStandard: {
			Version:    "config-v1",
			Confidence: 0.9,
			Review: config.ReviewBehaviorProfileConfig{
				Profile:                  modelprofile.ReviewProfileEvidenceFirst,
				WorkflowRiskSignals:      true,
				SupportingContextTokens:  65432,
				ReasoningMaxTokensBySize: map[string]int{"focused": 9999},
			},
		},
	}
	configured, found, err := reviewBehaviorProfile(cfg, nil, nil, codexReviewModelStandard, true)
	if err != nil || found || configured != nil {
		t.Fatalf("adaptive configured review behavior = %+v, %v, %v; want no stale inherited profile", configured, found, err)
	}
	options := automatedReviewOptions{
		modelID:            codexReviewModelStandard,
		adaptiveCodexModel: true,
		adaptiveReasoning:  true,
		reviewBehavior:     configured,
	}.withExecutionPlan(reviewExecutionPlan{
		sizeClass:          "focused",
		reasoningEffort:    "low",
		reasoningMaxTokens: 1024,
		explorationTimeout: 10 * time.Second,
	})
	if options.modelID != codexReviewModelFocused || options.reasoningMaxTokens != 1024 ||
		options.explorationTimeout != 10*time.Second {
		t.Fatalf("adaptive model inherited initial configured behavior: %#v", options)
	}
	providers := reviewContextProvidersForBehavior(codexReviewModelStandard, configured)
	if len(providers) != 1 || providers[0].Name() != "hyphae" {
		t.Fatalf("adaptive initial configured providers = %v", providers)
	}
	prompt := appendReviewExecutionPlan("review this", options)
	if strings.Contains(prompt, "## Review Behavior Profile") {
		t.Fatalf("adaptive alternate model prompt inherited initial configured behavior:\n%s", prompt)
	}
	if got := reviewSupportingContextBudgetForBehavior(codexReviewModelStandard, 0, configured); got != 0 {
		t.Fatalf("adaptive initial configured supporting context = %d, want 0", got)
	}

	store, err := storage.New(":memory:")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()
	profile := protocol.BehaviorProfile{
		SchemaVersion: protocol.ProfileSchemaVersion,
		ModelID:       codexReviewModelStandard,
		Version:       "eval-v1",
		Confidence:    0.9,
		MeasuredAt:    time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC),
		Review: &protocol.ReviewBehavior{
			Profile: modelprofile.ReviewProfileStructuredCodeReview,
		},
	}
	if err := storage.NewBehaviorProfileStore(store).Put(context.Background(), profile); err != nil {
		t.Fatalf("persist profile: %v", err)
	}
	if err := storage.NewBehaviorProfileStore(store).Promote(context.Background(), codexReviewModelStandard, "eval-v1"); err != nil {
		t.Fatalf("promote profile: %v", err)
	}
	if got, found, err := reviewBehaviorProfile(cfg, nil, store, codexReviewModelStandard, true); err != nil || found || got != nil {
		t.Fatalf("adaptive review behavior = %+v, %v, %v; want no stale inherited profile", got, found, err)
	}
}

func TestParseStartupOptionsFlagsAndFiltering(t *testing.T) {
	t.Setenv("BUCKLEY_QUIET", "1")
	raw := []string{"--encoding=json", "--model", "codex/gpt-5.4-mini", "--agent", "agent.yaml", "--task-intent", "mutation", "--code-mode", "-p", "hello", "--config=proj.yaml", "plan", "feat", "do", "thing"}
	opts, err := parseStartupOptions(raw)
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	if !opts.quiet {
		t.Fatalf("expected quiet from env")
	}
	if opts.encodingOverride != "json" {
		t.Fatalf("encodingOverride=%q want json", opts.encodingOverride)
	}
	if opts.prompt != "hello" {
		t.Fatalf("prompt=%q want hello", opts.prompt)
	}
	if opts.configPath != "proj.yaml" {
		t.Fatalf("configPath=%q want proj.yaml", opts.configPath)
	}
	if opts.modelOverride != "codex/gpt-5.4-mini" {
		t.Fatalf("modelOverride=%q want codex/gpt-5.4-mini", opts.modelOverride)
	}
	if opts.agentPath != "agent.yaml" {
		t.Fatalf("agentPath=%q want agent.yaml", opts.agentPath)
	}
	if !opts.taskIntentSet || opts.taskIntent != agentloop.MutationIntent {
		t.Fatalf("task intent set=%v value=%q, want mutation", opts.taskIntentSet, opts.taskIntent)
	}
	if !opts.codeMode {
		t.Fatal("expected --code-mode to enable code mode")
	}
	if got := opts.args; len(got) != 4 || got[0] != "plan" {
		t.Fatalf("args=%v want plan feat do thing", got)
	}
}

func TestLoadConfiguredConfigUsesStartupConfigPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "particle.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  openai_compatible:
    enabled: true
    base_url: https://particle.example/v1
    api_key: test-key
    models:
      - glm-5.3-flash
    stream_idle_timeout: 45s
    stream_first_content_timeout: 60s
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	previous := configPath
	configPath = path
	t.Cleanup(func() { configPath = previous })

	cfg, err := loadConfiguredConfig()
	if err != nil {
		t.Fatalf("loadConfiguredConfig: %v", err)
	}
	if got := cfg.Providers.OpenAICompatible.StreamIdleTimeout; got != 45*time.Second {
		t.Fatalf("stream idle timeout = %s, want 45s", got)
	}
	if got := cfg.Providers.OpenAICompatible.StreamFirstContentTimeout; got != time.Minute {
		t.Fatalf("stream first content timeout = %s, want 60s", got)
	}
}

func TestParseStartupOptionsMissingValues(t *testing.T) {
	_, err := parseStartupOptions([]string{"-p"})
	if err == nil {
		t.Fatalf("expected error for missing -p value")
	}
	_, err = parseStartupOptions([]string{"--encoding"})
	if err == nil {
		t.Fatalf("expected error for missing --encoding value")
	}
	_, err = parseStartupOptions([]string{"--config"})
	if err == nil {
		t.Fatalf("expected error for missing --config value")
	}
	_, err = parseStartupOptions([]string{"--model"})
	if err == nil {
		t.Fatalf("expected error for missing --model value")
	}
	_, err = parseStartupOptions([]string{"--model="})
	if err == nil {
		t.Fatalf("expected error for empty --model value")
	}
	_, err = parseStartupOptions([]string{"-p", "hello", "--model="})
	if err == nil {
		t.Fatalf("expected error for empty --model value after prompt")
	}
	_, err = parseStartupOptions([]string{"--agent"})
	if err == nil {
		t.Fatalf("expected error for missing --agent value")
	}
	_, err = parseStartupOptions([]string{"--agent="})
	if err == nil {
		t.Fatalf("expected error for empty --agent value")
	}
	_, err = parseStartupOptions([]string{"--task-intent"})
	if err == nil || !strings.Contains(err.Error(), "unknown, read_only, or mutation") {
		t.Fatalf("missing --task-intent error = %v", err)
	}
	_, err = parseStartupOptions([]string{"--task-intent="})
	if err == nil || !strings.Contains(err.Error(), "unknown, read_only, or mutation") {
		t.Fatalf("empty --task-intent error = %v", err)
	}
	_, err = parseStartupOptions([]string{"--task-intent", "question"})
	if err == nil || !strings.Contains(err.Error(), "unknown, read_only, or mutation") {
		t.Fatalf("invalid --task-intent error = %v", err)
	}
}

func TestParseStartupOptionsTaskIntentFormsAndSubcommandScope(t *testing.T) {
	opts, err := parseStartupOptions([]string{"--task-intent=read_only", "-p", "inspect"})
	if err != nil {
		t.Fatalf("parseStartupOptions equals form: %v", err)
	}
	if !opts.taskIntentSet || opts.taskIntent != agentloop.ReadOnlyIntent || opts.prompt != "inspect" {
		t.Fatalf("opts = %+v, want read_only one-shot prompt", opts)
	}
	opts, err = parseStartupOptions([]string{"goal", "run", "--task-intent", "mutation", "run-1"})
	if err != nil {
		t.Fatalf("parseStartupOptions subcommand form: %v", err)
	}
	if opts.taskIntentSet {
		t.Fatalf("subcommand-local --task-intent must not become a global launch flag: %+v", opts)
	}
	want := []string{"goal", "run", "--task-intent", "mutation", "run-1"}
	if !reflect.DeepEqual(opts.args, want) {
		t.Fatalf("args = %v, want %v", opts.args, want)
	}
}

func TestParseStartupOptionsPlainAndTUIFlags(t *testing.T) {
	opts, err := parseStartupOptions([]string{"--plain", "plan", "feat", "desc"})
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	if !opts.plainModeSet || !opts.plainMode {
		t.Fatalf("expected plain mode override true, got set=%v plain=%v", opts.plainModeSet, opts.plainMode)
	}
	if len(opts.args) != 3 || opts.args[0] != "plan" {
		t.Fatalf("expected args without --plain, got %v", opts.args)
	}

	opts, err = parseStartupOptions([]string{"--tui"})
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	if !opts.plainModeSet || opts.plainMode {
		t.Fatalf("expected tui override (plain=false), got set=%v plain=%v", opts.plainModeSet, opts.plainMode)
	}
}

func TestParseStartupOptionsLeavesSubcommandModelFlag(t *testing.T) {
	opts, err := parseStartupOptions([]string{"commit", "--model", "openai/gpt-5.4-mini", "--agent", "agent.yaml"})
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	if opts.modelOverride != "" {
		t.Fatalf("modelOverride=%q want empty", opts.modelOverride)
	}
	if opts.agentPath != "" {
		t.Fatalf("agentPath=%q want empty", opts.agentPath)
	}
	if got := opts.args; len(got) != 5 || got[0] != "commit" || got[1] != "--model" || got[2] != "openai/gpt-5.4-mini" || got[3] != "--agent" || got[4] != "agent.yaml" {
		t.Fatalf("args=%v want commit --model openai/gpt-5.4-mini --agent agent.yaml", got)
	}

	opts, err = parseStartupOptions([]string{"commit", "--model=openai/gpt-5.4-mini", "--agent=agent.yaml"})
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	if opts.modelOverride != "" {
		t.Fatalf("modelOverride=%q want empty", opts.modelOverride)
	}
	if opts.agentPath != "" {
		t.Fatalf("agentPath=%q want empty", opts.agentPath)
	}
	if got := opts.args; len(got) != 3 || got[0] != "commit" || got[1] != "--model=openai/gpt-5.4-mini" || got[2] != "--agent=agent.yaml" {
		t.Fatalf("args=%v want commit --model=openai/gpt-5.4-mini --agent=agent.yaml", got)
	}
}

func TestParseStartupOptionsLeavesSkillsFlags(t *testing.T) {
	opts, err := parseStartupOptions([]string{"skills", "list", "--source", "agent", "--format", "json"})
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	want := []string{"skills", "list", "--source", "agent", "--format", "json"}
	if strings.Join(opts.args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args=%v want %v", opts.args, want)
	}
}

func TestParseStartupOptionsAgentEnvDefault(t *testing.T) {
	t.Setenv("BUCKLEY_AGENT", "env-agent.yaml")
	opts, err := parseStartupOptions([]string{"--plain"})
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	if opts.agentPath != "env-agent.yaml" {
		t.Fatalf("agentPath=%q want env-agent.yaml", opts.agentPath)
	}

	opts, err = parseStartupOptions([]string{"--agent=flag-agent.yaml"})
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	if opts.agentPath != "flag-agent.yaml" {
		t.Fatalf("agentPath=%q want flag-agent.yaml", opts.agentPath)
	}
}

func TestParseStartupOptionsCodeModeEnvAndSubcommandBoundary(t *testing.T) {
	t.Setenv("BUCKLEY_CODE_MODE", "true")
	opts, err := parseStartupOptions([]string{"--plain"})
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	if !opts.codeMode {
		t.Fatal("expected BUCKLEY_CODE_MODE to enable code mode")
	}

	t.Setenv("BUCKLEY_CODE_MODE", "false")
	opts, err = parseStartupOptions([]string{"goal", "run", "--code-mode", "run-1"})
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	if opts.codeMode {
		t.Fatal("subcommand-local --code-mode must not become a global launch flag")
	}
	want := []string{"goal", "run", "--code-mode", "run-1"}
	if strings.Join(opts.args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args=%v want %v", opts.args, want)
	}
}

func TestConsumeResumeCommand(t *testing.T) {
	opts := &startupOptions{args: []string{"resume", "sess-123"}}
	if err := opts.consumeResumeCommand(); err != nil {
		t.Fatalf("consumeResumeCommand error: %v", err)
	}
	if opts.resumeSessionID != "sess-123" {
		t.Fatalf("resumeSessionID=%q want sess-123", opts.resumeSessionID)
	}
	if len(opts.args) != 0 {
		t.Fatalf("expected args cleared, got %v", opts.args)
	}

	opts = &startupOptions{args: []string{"resume"}}
	if err := opts.consumeResumeCommand(); err == nil {
		t.Fatalf("expected usage error for resume without id")
	}
}

func TestApplySandboxOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	t.Setenv("BUCKLEY_SANDBOX", "off")
	applySandboxOverride(cfg)
	if cfg.Worktrees.UseContainers {
		t.Fatalf("expected UseContainers=false")
	}

	cfg = config.DefaultConfig()
	t.Setenv("BUCKLEY_SANDBOX", "containers")
	applySandboxOverride(cfg)
	if !cfg.Worktrees.UseContainers {
		t.Fatalf("expected UseContainers=true")
	}
}

func TestApplyStartupModelOverrideEnablesCodex(t *testing.T) {
	cfg := config.DefaultConfig()

	applyStartupModelOverride(cfg, "codex/gpt-5.4-mini")

	if !cfg.Providers.Codex.Enabled {
		t.Fatalf("codex provider should be enabled")
	}
	if cfg.Models.DefaultProvider != "codex" {
		t.Fatalf("default provider=%q want codex", cfg.Models.DefaultProvider)
	}
	if cfg.Models.Execution != "codex/gpt-5.4-mini" {
		t.Fatalf("execution=%q want codex/gpt-5.4-mini", cfg.Models.Execution)
	}
	if cfg.Models.Planning != "codex/gpt-5.4-mini" || cfg.Models.Review != "codex/gpt-5.4-mini" {
		t.Fatalf("planning/review=%q/%q want codex/gpt-5.4-mini", cfg.Models.Planning, cfg.Models.Review)
	}
	if cfg.Models.Reasoning != "xhigh" {
		t.Fatalf("reasoning=%q want xhigh", cfg.Models.Reasoning)
	}
}

func TestApplyStartupModelOverrideDisablesConfiguredFallbacks(t *testing.T) {
	cfg := config.DefaultConfig()
	if len(cfg.Models.FallbackChains["z-ai/glm-5.2"]) == 0 {
		t.Fatal("default GLM fallback chain missing from test setup")
	}

	applyStartupModelOverride(cfg, "z-ai/glm-5.2")

	if cfg.Models.Execution != "z-ai/glm-5.2" {
		t.Fatalf("execution model = %q, want exact normalized override", cfg.Models.Execution)
	}
	if _, exists := cfg.Models.FallbackChains["z-ai/glm-5.2"]; exists {
		t.Fatal("explicit command model retained a configured fallback chain")
	}
}

func TestApplyCommandModelOverrideRestoresPreviousValue(t *testing.T) {
	previous := modelOverrideFlag
	modelOverrideFlag = "openai/gpt-5.4"
	defer func() {
		modelOverrideFlag = previous
	}()

	restore := applyCommandModelOverride(" codex/gpt-5.5 ")
	if modelOverrideFlag != "codex/gpt-5.5" {
		t.Fatalf("modelOverrideFlag=%q want codex/gpt-5.5", modelOverrideFlag)
	}

	restore()
	if modelOverrideFlag != "openai/gpt-5.4" {
		t.Fatalf("modelOverrideFlag=%q want previous value", modelOverrideFlag)
	}
}

func TestApplyCommandModelOverridePreservesReasoningSuffix(t *testing.T) {
	previous := modelOverrideFlag
	defer func() { modelOverrideFlag = previous }()

	restore := applyCommandModelOverride("codex/gpt-5.6-terra-high")
	if modelOverrideFlag != "codex/gpt-5.6-terra-high" {
		t.Fatalf("modelOverrideFlag=%q want suffix preserved until config load", modelOverrideFlag)
	}
	restore()
}

func TestNetworkHelpers(t *testing.T) {
	if !hasACPTLS(config.ACPConfig{TLSCertFile: "a", TLSKeyFile: "b", TLSClientCAFile: "c"}) {
		t.Fatalf("expected hasACPTLS true")
	}
	if hasACPTLS(config.ACPConfig{TLSCertFile: "a"}) {
		t.Fatalf("expected hasACPTLS false")
	}

	if !isLoopbackAddress("127.0.0.1:4488") {
		t.Fatalf("expected loopback true")
	}
	if isLoopbackAddress("0.0.0.0:4488") {
		t.Fatalf("expected loopback false for wildcard")
	}

	if url := humanReadableURL("0.0.0.0:4488"); url != "http://127.0.0.1:4488" {
		t.Fatalf("humanReadableURL=%q want http://127.0.0.1:4488", url)
	}
	if url := humanReadableURL("127.0.0.1"); url != "http://127.0.0.1" {
		t.Fatalf("humanReadableURL=%q want http://127.0.0.1", url)
	}
}

func TestACPEventStoreConfigHelpers(t *testing.T) {
	if got := acpEventStoreName(config.ACPConfig{}); got != "sqlite" {
		t.Fatalf("acpEventStoreName empty=%q want sqlite", got)
	}
	if got := acpEventStoreName(config.ACPConfig{EventStore: " NATS "}); got != "nats" {
		t.Fatalf("acpEventStoreName nats=%q want nats", got)
	}

	cfg := config.NATSConfig{
		URL:            "nats://127.0.0.1:4222",
		Username:       "user",
		Password:       "pass",
		Token:          "token",
		TLS:            true,
		StreamPrefix:   "stream",
		SnapshotBucket: "snapshots",
		ConnectTimeout: 2 * time.Second,
		RequestTimeout: 3 * time.Second,
	}
	opts := acpNATSOptions(cfg)
	if opts.URL != cfg.URL || opts.Username != cfg.Username || opts.Password != cfg.Password || opts.Token != cfg.Token {
		t.Fatalf("nats auth/options not copied: %+v", opts)
	}
	if !opts.TLS || opts.StreamPrefix != cfg.StreamPrefix || opts.SnapshotBucket != cfg.SnapshotBucket {
		t.Fatalf("nats stream options not copied: %+v", opts)
	}
	if opts.ConnectTimeout != cfg.ConnectTimeout || opts.RequestTimeout != cfg.RequestTimeout {
		t.Fatalf("nats timeouts not copied: %+v", opts)
	}
}

func TestChooseSecret(t *testing.T) {
	if got := chooseSecret("flag", "cfg"); got != "flag" {
		t.Fatalf("chooseSecret(flag,cfg)=%q want flag", got)
	}
	if got := chooseSecret("", "cfg"); got != "cfg" {
		t.Fatalf("chooseSecret(\"\",cfg)=%q want cfg", got)
	}
}

func TestDebugJSONWritesResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	debugJSON(rec, map[string]any{"ok": true}, 201)
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type=%q want application/json", ct)
	}
	if rec.Code != 201 {
		t.Fatalf("status=%d want 201", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	if !strings.Contains(body, "\"ok\":true") {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestRunBatchCommandErrors(t *testing.T) {
	if err := runBatchCommand(nil); err == nil {
		t.Fatal("expected usage error for missing batch subcommand")
	}
	if err := runBatchCommand([]string{"nope"}); err == nil {
		t.Fatal("expected error for unknown batch subcommand")
	}
}

func TestIsInteractiveTerminalDoesNotPanic(t *testing.T) {
	_ = isInteractiveTerminal()
}

func TestDispatchSubcommandUnknownCommandHandled(t *testing.T) {
	var handled bool
	var exitCode int
	errOut := captureStderr(t, func() {
		handled, exitCode = dispatchSubcommand([]string{"nope"})
	})
	if !handled || exitCode != 1 {
		t.Fatalf("handled=%v exitCode=%d want true,1", handled, exitCode)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Fatalf("expected unknown command message, got %q", errOut)
	}
}

func TestDispatchSubcommandUnknownFlagHandled(t *testing.T) {
	var handled bool
	var exitCode int
	errOut := captureStderr(t, func() {
		handled, exitCode = dispatchSubcommand([]string{"--nope"})
	})
	if !handled || exitCode != 1 {
		t.Fatalf("handled=%v exitCode=%d want true,1", handled, exitCode)
	}
	if !strings.Contains(errOut, "unknown flag") {
		t.Fatalf("expected unknown flag message, got %q", errOut)
	}
}

func TestRunCommandUsesExitCodeOverrides(t *testing.T) {
	errOut := captureStderr(t, func() {
		code := runCommand(func(_ []string) error {
			return withExitCode(errors.New("bad config"), 2)
		}, nil)
		if code != 2 {
			t.Fatalf("exitCode=%d want 2", code)
		}
	})
	if !strings.Contains(errOut, "bad config") {
		t.Fatalf("expected error output, got %q", errOut)
	}
}

func TestRunCommandTreatsFlagHelpAsSuccess(t *testing.T) {
	errOut := captureStderr(t, func() {
		code := runCommand(func(_ []string) error {
			return flag.ErrHelp
		}, nil)
		if code != 0 {
			t.Fatalf("exitCode=%d want 0", code)
		}
	})
	if strings.TrimSpace(errOut) != "" {
		t.Fatalf("expected no error output for help, got %q", errOut)
	}
}

func TestPrintOneShotFailure_PreservesIncompleteTurnOutput(t *testing.T) {
	err := &agentloop.IncompleteTurnError{
		FinishReason:      agentloop.FinishReasonStepCap,
		Reason:            "the child reached its explicit model-request limit",
		FinalizationError: "provider returned an empty synthesis",
	}
	var stdout string
	exitCode := 0
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			exitCode = printOneShotFailure("Buckley stopped after preserving three tool results.", err)
		})
	})

	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 for incomplete result", exitCode)
	}
	if !strings.Contains(stdout, "preserving three tool results") || !strings.Contains(stdout, "Incomplete result") {
		t.Fatalf("stdout = %q, want preserved content with an explicit incomplete marker", stdout)
	}
	if strings.Contains(strings.ToLower(stdout), "completed successfully") {
		t.Fatalf("stdout = %q, must not claim successful completion", stdout)
	}
	if !strings.Contains(stderr, "One-shot status: incomplete (exit=1") || strings.Contains(stderr, "provider returned an empty synthesis") {
		t.Fatalf("stderr = %q, want bounded incomplete status without raw error", stderr)
	}
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			exitCode = printOneShotFailure("", err)
		})
	})
	if exitCode != 1 || !strings.Contains(stdout, "Incomplete result") || !strings.Contains(stdout, "Next:") {
		t.Fatalf("empty incomplete output exit=%d stdout=%q, want actionable notice", exitCode, stdout)
	}
	if strings.Contains(stderr, "provider returned an empty synthesis") {
		t.Fatalf("stderr = %q, must not duplicate raw handled incomplete error", stderr)
	}
}

func TestOneShotProgressStream_PrivacySafeCountsOnly(t *testing.T) {
	var buf bytes.Buffer
	progress := &oneShotProgress{
		writer:      &buf,
		now:         func() time.Time { return time.Unix(100, 0) },
		minInterval: 0,
		phase:       "starting",
	}

	_ = progress.Stream(acp.NewAgentThoughtChunk("secret reasoning should never print"))
	_ = progress.Stream(acp.NewAgentMessageChunk("final text should stay stdout-only"))
	_ = progress.Stream(acp.SessionUpdate{
		SessionUpdate: acp.SessionUpdateToolCall,
		ToolCallID:    "call_1",
		Title:         "run_shell",
		RawInput:      map[string]any{"cmd": "secret command"},
	})

	out := buf.String()
	for _, forbidden := range []string{"secret reasoning", "final text", "run_shell", "call_1", "secret command"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("progress leaked %q in %q", forbidden, out)
		}
	}
	for _, want := range []string{
		"phase=thinking thought_chunks=1 message_chunks=0 tool_calls=0",
		"phase=receiving thought_chunks=1 message_chunks=1 tool_calls=0",
		"phase=tool thought_chunks=1 message_chunks=1 tool_calls=1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("progress output missing %q in %q", want, out)
		}
	}
}

func TestOneShotProgressStream_RespectsEventInterval(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(200, 0)
	progress := &oneShotProgress{
		writer:      &buf,
		now:         func() time.Time { return now },
		minInterval: 10 * time.Second,
		phase:       "starting",
	}

	_ = progress.Stream(acp.NewAgentThoughtChunk("one"))
	_ = progress.Stream(acp.NewAgentThoughtChunk("two"))
	now = now.Add(11 * time.Second)
	_ = progress.Stream(acp.NewAgentThoughtChunk("three"))

	if got := strings.Count(buf.String(), "One-shot progress:"); got != 2 {
		t.Fatalf("progress lines = %d, want 2; output=%q", got, buf.String())
	}
	if strings.Contains(buf.String(), "one") || strings.Contains(buf.String(), "two") || strings.Contains(buf.String(), "three") {
		t.Fatalf("progress leaked raw thought text: %q", buf.String())
	}
}

func TestNewOneShotProgressStream_QuietModeDisablesCallback(t *testing.T) {
	oldQuiet := quietMode
	quietMode = true
	t.Cleanup(func() { quietMode = oldQuiet })

	var buf bytes.Buffer
	if got := newOneShotProgressStream(&buf); got != nil {
		t.Fatal("quiet mode should disable one-shot progress callback")
	}
	if buf.Len() != 0 {
		t.Fatalf("quiet progress wrote output: %q", buf.String())
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out)
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestDispatchSubcommandHelpVersionAndBatch(t *testing.T) {
	helpOut := captureStdout(t, func() {
		handled, code := dispatchSubcommand([]string{"--help"})
		if !handled || code != 0 {
			t.Fatalf("help handled=%v code=%d", handled, code)
		}
	})
	if !strings.Contains(helpOut, "Buckley - Tool-First AI Agent Harness") {
		t.Fatalf("unexpected help output: %q", helpOut)
	}
	if !strings.Contains(helpOut, "commit [--dry-run]") {
		t.Fatalf("expected help to include commit command, got: %q", helpOut)
	}
	if !strings.Contains(helpOut, "pr [--dry-run]") {
		t.Fatalf("expected help to include pr command, got: %q", helpOut)
	}
	if !strings.Contains(helpOut, "experiment promote <model-id> <profile-version>") {
		t.Fatalf("expected help to include experiment promote command, got: %q", helpOut)
	}

	versionOut := captureStdout(t, func() {
		handled, code := dispatchSubcommand([]string{"--version"})
		if !handled || code != 0 {
			t.Fatalf("version handled=%v code=%d", handled, code)
		}
	})
	if !strings.Contains(versionOut, "Buckley") {
		t.Fatalf("unexpected version output: %q", versionOut)
	}

	handled, code := dispatchSubcommand([]string{"batch"})
	if !handled || code == 0 {
		t.Fatalf("expected batch to be handled with error code, got handled=%v code=%d", handled, code)
	}
}

type fakeOrchestrator struct {
	planFeatureCalled bool
	featureName       string
	description       string
	loadedPlanID      string
	executedPlan      bool
	executedTaskID    string
	plan              *orchestrator.Plan
	planErr           error
}

func (f *fakeOrchestrator) PlanFeature(featureName, description string) (*orchestrator.Plan, error) {
	f.planFeatureCalled = true
	f.featureName = featureName
	f.description = description
	if f.planErr != nil {
		return nil, f.planErr
	}
	if f.plan == nil {
		f.plan = &orchestrator.Plan{ID: "p1", FeatureName: featureName, CreatedAt: time.Now()}
	}
	return f.plan, nil
}

func (f *fakeOrchestrator) LoadPlan(planID string) (*orchestrator.Plan, error) {
	f.loadedPlanID = planID
	if f.plan == nil {
		f.plan = &orchestrator.Plan{ID: planID, FeatureName: "Feature", CreatedAt: time.Now()}
	}
	return f.plan, nil
}

func (f *fakeOrchestrator) ExecutePlan() error {
	f.executedPlan = true
	return nil
}

func (f *fakeOrchestrator) ExecuteTask(taskID string) error {
	f.executedTaskID = taskID
	return nil
}

func TestRunPlanAndExecuteCommandsViaHarness(t *testing.T) {
	origInit := initDependenciesFn
	origNewOrch := newOrchestratorFn
	t.Cleanup(func() {
		initDependenciesFn = origInit
		newOrchestratorFn = origNewOrch
	})

	tmpDB := filepath.Join(t.TempDir(), "cli.db")
	store, err := storage.New(tmpDB)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		return config.DefaultConfig(), nil, store, nil
	}

	fake := &fakeOrchestrator{}
	newOrchestratorFn = func(store *storage.Store, mgr *model.Manager, registry *tool.Registry, cfg *config.Config, workflow *orchestrator.WorkflowManager, planStore orchestrator.PlanStore) orchestratorRunner {
		return fake
	}

	out := captureStdout(t, func() {
		if err := runPlanCommand([]string{"feat", "do", "thing"}); err != nil {
			t.Fatalf("runPlanCommand: %v", err)
		}
	})
	if !fake.planFeatureCalled || fake.featureName != "feat" {
		t.Fatalf("expected PlanFeature called, got %+v", fake)
	}
	if !strings.Contains(out, "Plan created") {
		t.Fatalf("unexpected plan output: %q", out)
	}

	execOut := captureStdout(t, func() {
		if err := runExecuteCommand([]string{"p1"}); err != nil {
			t.Fatalf("runExecuteCommand: %v", err)
		}
	})
	if fake.loadedPlanID != "p1" || !fake.executedPlan {
		t.Fatalf("expected LoadPlan+ExecutePlan, got %+v", fake)
	}
	if !strings.Contains(execOut, "Plan execution complete") {
		t.Fatalf("unexpected execute output: %q", execOut)
	}
}

func TestRunPlanCommand_PrintsIncompletePublicDraftWithoutPrivateLeak(t *testing.T) {
	origInit := initDependenciesFn
	origNewOrch := newOrchestratorFn
	t.Cleanup(func() {
		initDependenciesFn = origInit
		newOrchestratorFn = origNewOrch
	})

	tmpDB := filepath.Join(t.TempDir(), "cli.db")
	store, err := storage.New(tmpDB)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		return config.DefaultConfig(), nil, store, nil
	}

	fake := &fakeOrchestrator{
		planErr: fmt.Errorf("failed to generate plan: %w", orchestrator.NewIncompletePlanError("PUBLIC_PLAN_DRAFT", "length", errors.New("RAW_PROVIDER_ERROR_SENTINEL"))),
	}
	newOrchestratorFn = func(store *storage.Store, mgr *model.Manager, registry *tool.Registry, cfg *config.Config, workflow *orchestrator.WorkflowManager, planStore orchestrator.PlanStore) orchestratorRunner {
		return fake
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = runPlanCommand([]string{"feat", "do", "thing"})
	})
	if runErr == nil {
		t.Fatal("runPlanCommand succeeded, want error")
	}
	if !strings.Contains(out, "PUBLIC_PLAN_DRAFT") {
		t.Fatalf("stdout did not include public draft: %q", out)
	}
	for _, sentinel := range []string{"RAW_PROVIDER_ERROR_SENTINEL", "PRIVATE_REASONING_SENTINEL"} {
		if strings.Contains(out, sentinel) || strings.Contains(runErr.Error(), sentinel) {
			t.Fatalf("CLI leaked sentinel %q: stdout=%q err=%q", sentinel, out, runErr.Error())
		}
	}
	if !strings.Contains(runErr.Error(), "planning response incomplete") {
		t.Fatalf("error = %q, want stable incomplete error", runErr.Error())
	}
}

func TestRunExecuteTaskCommandHarness(t *testing.T) {
	origInit := initDependenciesFn
	origNewOrch := newOrchestratorFn
	t.Cleanup(func() {
		initDependenciesFn = origInit
		newOrchestratorFn = origNewOrch
	})

	tmpDB := filepath.Join(t.TempDir(), "cli.db")
	store, err := storage.New(tmpDB)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		return config.DefaultConfig(), nil, store, nil
	}

	fake := &fakeOrchestrator{}
	newOrchestratorFn = func(store *storage.Store, mgr *model.Manager, registry *tool.Registry, cfg *config.Config, workflow *orchestrator.WorkflowManager, planStore orchestrator.PlanStore) orchestratorRunner {
		return fake
	}

	if err := runExecuteTaskCommand([]string{"--plan", "p1", "--task", "t1", "--push=false"}); err != nil {
		t.Fatalf("runExecuteTaskCommand: %v", err)
	}
	if fake.loadedPlanID != "p1" || fake.executedTaskID != "t1" {
		t.Fatalf("expected LoadPlan+ExecuteTask, got %+v", fake)
	}
}

func TestACPCompletionContractTaskIntent_Mutation(t *testing.T) {
	limits := acpLoopLimits{
		TaskIntent:        agentloop.MutationIntent,
		VerificationDepth: "focused",
	}
	contract := acpCompletionContract(limits)
	if contract == nil {
		t.Fatal("expected non-nil contract")
	}
	if !contract.RequireObservableChange {
		t.Errorf("RequireObservableChange = %v, want true", contract.RequireObservableChange)
	}
	if !contract.RequirePostChangeVerification {
		t.Errorf("RequirePostChangeVerification = %v, want true", contract.RequirePostChangeVerification)
	}
	if contract.TaskIntent != agentloop.MutationIntent {
		t.Errorf("TaskIntent = %q, want %q", contract.TaskIntent, agentloop.MutationIntent)
	}
}

func TestACPCompletionContractTaskIntent_MutationNoneRequiresChangeOnly(t *testing.T) {
	for _, depth := range []string{"none", "off"} {
		t.Run(depth, func(t *testing.T) {
			contract := acpCompletionContract(acpLoopLimits{
				TaskIntent:        agentloop.MutationIntent,
				VerificationDepth: depth,
			})
			if contract == nil {
				t.Fatal("expected mutation contract even when verification is disabled")
			}
			if !contract.RequireObservableChange {
				t.Errorf("RequireObservableChange = %v, want true", contract.RequireObservableChange)
			}
			if contract.RequirePostChangeVerification {
				t.Errorf("RequirePostChangeVerification = %v, want false", contract.RequirePostChangeVerification)
			}
		})
	}
}

func TestACPCompletionContractTaskIntent_LegacyDisablesContract(t *testing.T) {
	contract := acpCompletionContract(acpLoopLimits{
		TaskIntent:        agentloop.MutationIntent,
		VerificationDepth: "legacy",
	})
	if contract != nil {
		t.Fatalf("legacy contract = %+v, want nil", contract)
	}
}

func TestACPCompletionContractTaskIntent_ReadOnly(t *testing.T) {
	limits := acpLoopLimits{
		TaskIntent:        agentloop.ReadOnlyIntent,
		VerificationDepth: "focused",
	}
	contract := acpCompletionContract(limits)
	if contract == nil {
		t.Fatal("expected non-nil contract")
	}
	if contract.RequireObservableChange {
		t.Errorf("RequireObservableChange = %v, want false", contract.RequireObservableChange)
	}
	if !contract.RequirePostChangeVerification {
		t.Errorf("RequirePostChangeVerification = %v, want true for unexpected read-only mutations", contract.RequirePostChangeVerification)
	}
	if contract.TaskIntent != agentloop.ReadOnlyIntent {
		t.Errorf("TaskIntent = %q, want %q", contract.TaskIntent, agentloop.ReadOnlyIntent)
	}
}
