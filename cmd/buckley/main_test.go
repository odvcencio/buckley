package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
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
	wantTools := []string{"read_file", "search_text", "code_impact", "code_refs", "apply_patch", "run_tests", "git_diff", "git_status", "find_files", "code_callgraph"}
	if !reflect.DeepEqual(compiled.VisibleTools, wantTools) {
		t.Fatalf("one-shot protocol tools = %v, want %v", compiled.VisibleTools, wantTools)
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

func TestOneShotBehaviorProfileUsesDurableStoreWithoutConfigPin(t *testing.T) {
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
	}
	if err := storage.NewBehaviorProfileStore(store).Put(context.Background(), profile); err != nil {
		t.Fatalf("persist profile: %v", err)
	}
	got, found, err := oneShotBehaviorProfile(cfg, nil, store, "stored/model")
	if err != nil || !found || got.Version != "eval-v3" {
		t.Fatalf("oneShotBehaviorProfile = %+v, %v, %v", got, found, err)
	}
}

func TestParseStartupOptionsFlagsAndFiltering(t *testing.T) {
	t.Setenv("BUCKLEY_QUIET", "1")
	raw := []string{"--encoding=json", "--model", "codex/gpt-5.4-mini", "--agent", "agent.yaml", "--code-mode", "-p", "hello", "--config=proj.yaml", "plan", "feat", "do", "thing"}
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
	if !opts.codeMode {
		t.Fatal("expected --code-mode to enable code mode")
	}
	if got := opts.args; len(got) != 4 || got[0] != "plan" {
		t.Fatalf("args=%v want plan feat do thing", got)
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
	if !strings.Contains(stderr, "One-shot status: incomplete (exit=1") || !strings.Contains(stderr, "final synthesis failed") {
		t.Fatalf("stderr = %q, want non-zero incomplete status and cause", stderr)
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
}

func (f *fakeOrchestrator) PlanFeature(featureName, description string) (*orchestrator.Plan, error) {
	f.planFeatureCalled = true
	f.featureName = featureName
	f.description = description
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
