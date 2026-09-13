package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/skill"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type acpStateTestTool struct {
	name     string
	metadata tool.ToolMetadata
	execute  func() error
}

func (t *acpStateTestTool) Name() string        { return t.name }
func (t *acpStateTestTool) Description() string { return t.name }
func (t *acpStateTestTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}
func (t *acpStateTestTool) Execute(map[string]any) (*builtin.Result, error) {
	if t.execute != nil {
		if err := t.execute(); err != nil {
			return nil, err
		}
	}
	return &builtin.Result{Success: true}, nil
}
func (t *acpStateTestTool) Metadata() tool.ToolMetadata { return t.metadata }
func (t *acpStateTestTool) TrustedVerification() bool   { return t.metadata.Verification }

func TestShouldNudgeForTools(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "search intent",
			input: "I'll search the codebase for the config.",
			want:  true,
		},
		{
			name:  "check intent",
			input: "Let me check the files and see.",
			want:  true,
		},
		{
			name:  "run intent",
			input: "I will run tests to verify.",
			want:  true,
		},
		{
			name:  "plain answer",
			input: "Here is the answer to your question.",
			want:  false,
		},
		{
			name:  "intent without action",
			input: "I'll be brief and direct.",
			want:  false,
		},
		{
			name:  "no intent",
			input: "This is a fast model.",
			want:  false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldNudgeForTools(tc.input); got != tc.want {
				t.Fatalf("shouldNudgeForTools(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestACPToolLoopGovernorUsesProtocolReadOnlyReserve(t *testing.T) {
	governor := newACPToolLoopGovernorWithLimits(config.DefaultConfig(), acpLoopLimits{
		ReadOnlyWarningAt: 3,
		ReadOnlyActionAt:  5,
		MaxReadOnlyCalls:  9,
	})

	for call := 1; call <= 9; call++ {
		got := governor.ObserveProgress(string(tool.ImpactReadOnly), true, false, false)
		switch call {
		case 3:
			if got.Stop || got.Kind != "read_only_budget_warning" {
				t.Fatalf("call %d = %+v, want read-only warning", call, got)
			}
		case 5:
			if got.Stop || got.Kind != "read_only_action_required" || !governor.ActionRequired() {
				t.Fatalf("call %d = %+v, actionRequired=%v, want action boundary", call, got, governor.ActionRequired())
			}
		case 9:
			if !got.Stop || got.Kind != "read_only_budget" {
				t.Fatalf("call %d = %+v, want read-only budget stop", call, got)
			}
		default:
			if got.Stop || got.Nudge != "" {
				t.Fatalf("call %d = %+v, want no intervention", call, got)
			}
		}
	}
}

func TestBuildACPToolTurnActionBoundaryNarrowsToModifyingTools(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	registry.Register(&acpStateTestTool{name: "read_file", metadata: tool.ToolMetadata{Impact: tool.ImpactReadOnly}})
	registry.Register(&acpStateTestTool{name: "run_shell", metadata: tool.ToolMetadata{Impact: tool.ImpactDestructive}})
	registry.Register(&acpStateTestTool{name: "write_file", metadata: tool.ToolMetadata{Impact: tool.ImpactModifying}})

	turn := buildACPToolTurn(registry, nil, nil, true, true, agentloop.UnknownIntent)
	if !turn.Enabled || !turn.UseTools {
		t.Fatalf("tool turn disabled: %+v", turn)
	}
	if !reflect.DeepEqual(turn.AllowedTools, []string{"write_file"}) {
		t.Fatalf("allowed tools = %v, want only modifying tool", turn.AllowedTools)
	}
	gotNames := make([]string, 0, len(turn.Tools))
	for _, candidate := range turn.Tools {
		fn, _ := candidate["function"].(map[string]any)
		if name, _ := fn["name"].(string); name != "" {
			gotNames = append(gotNames, name)
		}
	}
	if !reflect.DeepEqual(gotNames, []string{"write_file"}) {
		t.Fatalf("serialized tools = %v, want only write_file", gotNames)
	}
}

func TestBuildACPToolTurnReadOnlyIntentDoesNotEnterActionWindow(t *testing.T) {
	registry := newACPActionWindowRegistry()
	turn := buildACPToolTurn(registry, nil, nil, true, true, agentloop.ReadOnlyIntent)
	if turn.AllowedTools != nil {
		t.Fatalf("read-only intent narrowed tools to %v", turn.AllowedTools)
	}
	names := acpToolTurnFunctionNames(turn)
	for _, want := range []string{"read_file", "run_shell", "write_file"} {
		if !strings.Contains(strings.Join(names, "\x00"), want) {
			t.Fatalf("read-only intent tools = %v, missing %s", names, want)
		}
	}
}

func TestBuildACPToolTurnActionBoundaryRetainsControlAndVerificationTools(t *testing.T) {
	registry := newACPActionWindowRegistry()
	turn := buildACPToolTurn(registry, nil, nil, true, true, agentloop.UnknownIntent)
	want := []string{"exec_program", "git_diff", "git_status", "run_tests", "submit_artifact", "write_file"}
	if !reflect.DeepEqual(turn.AllowedTools, want) {
		t.Fatalf("allowed tools = %v, want action plus completion/verification tools %v", turn.AllowedTools, want)
	}
}

func TestACPActionRepairWindowKeepsActionToolsUntilObservedChange(t *testing.T) {
	registry := newACPActionWindowRegistry()
	governor := newACPToolLoopGovernorWithLimits(config.DefaultConfig(), acpLoopLimits{
		ReadOnlyWarningAt: 3,
		ReadOnlyActionAt:  5,
		MaxReadOnlyCalls:  9,
	})
	wantActionTools := []string{"exec_program", "git_diff", "git_status", "run_tests", "submit_artifact", "write_file"}

	for call := 1; call <= 5; call++ {
		got := governor.ObserveProgress(string(tool.ImpactReadOnly), true, false, false)
		if call == 5 {
			if got.Stop || got.Kind != "read_only_action_required" || !governor.ActionRequired() {
				t.Fatalf("call %d = %+v actionRequired=%v, want action boundary", call, got, governor.ActionRequired())
			}
		} else if got.Stop {
			t.Fatalf("discovery call %d stopped early: %+v", call, got)
		}
	}

	for call := 6; call <= 8; call++ {
		got := governor.ObserveProgress(string(tool.ImpactModifying), false, true, false)
		if got.Stop {
			t.Fatalf("failed action repair call %d stopped early: %+v", call, got)
		}
		if !governor.ActionRequired() {
			t.Fatalf("failed action repair call %d reset action boundary", call)
		}
		turn := buildACPToolTurn(registry, nil, nil, true, governor.ActionRequired(), agentloop.UnknownIntent)
		if !reflect.DeepEqual(turn.AllowedTools, wantActionTools) {
			t.Fatalf("repair call %d allowed tools = %v, want %v", call, turn.AllowedTools, wantActionTools)
		}
	}

	if got := governor.ObserveProgress(string(tool.ImpactModifying), true, true, true); got.Stop || got.Nudge != "" {
		t.Fatalf("successful observed edit = %+v, want reset without intervention", got)
	}
	if governor.ActionRequired() {
		t.Fatal("successful observed edit did not reset action boundary")
	}
	restored := buildACPToolTurn(registry, nil, nil, true, governor.ActionRequired(), agentloop.UnknownIntent)
	restoredTools := acpToolTurnFunctionNames(restored)
	for _, want := range []string{"read_file", "write_file", "run_tests", "git_diff", "git_status"} {
		if !strings.Contains(strings.Join(restoredTools, "\x00"), want) {
			t.Fatalf("restored tools = %v, missing %s", restoredTools, want)
		}
	}
}

func TestACPActionRepairWindowStopsWhenRepairsNeverChangeState(t *testing.T) {
	governor := newACPToolLoopGovernorWithLimits(config.DefaultConfig(), acpLoopLimits{
		ReadOnlyWarningAt: 3,
		ReadOnlyActionAt:  5,
		MaxReadOnlyCalls:  9,
	})

	for call := 1; call <= 5; call++ {
		got := governor.ObserveProgress(string(tool.ImpactReadOnly), true, false, false)
		if got.Stop {
			t.Fatalf("discovery call %d stopped early: %+v", call, got)
		}
	}
	for call := 6; call <= 8; call++ {
		got := governor.ObserveProgress(string(tool.ImpactModifying), true, true, false)
		if got.Stop {
			t.Fatalf("no-change repair call %d stopped early: %+v", call, got)
		}
		if !governor.ActionRequired() {
			t.Fatalf("no-change repair call %d reset action boundary", call)
		}
	}
	got := governor.ObserveProgress(string(tool.ImpactModifying), true, true, false)
	if !got.Stop || got.Kind != "read_only_budget" {
		t.Fatalf("ninth no-change call = %+v, want hard stop", got)
	}
}

func newACPActionWindowRegistry() *tool.Registry {
	registry := tool.NewEmptyRegistry()
	for _, candidate := range []*acpStateTestTool{
		{name: "read_file", metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactReadOnly}},
		{name: "run_shell", metadata: tool.ToolMetadata{Category: tool.CategoryShell, Impact: tool.ImpactDestructive}},
		{name: "write_file", metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactModifying}},
		{name: "run_tests", metadata: tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly, Verification: true}},
		{name: "git_diff", metadata: tool.ToolMetadata{Category: tool.CategoryGit, Impact: tool.ImpactReadOnly}},
		{name: "git_status", metadata: tool.ToolMetadata{Category: tool.CategoryGit, Impact: tool.ImpactReadOnly}},
		{name: "exec_program", metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactReadOnly}},
		{name: "submit_artifact", metadata: tool.ToolMetadata{Category: tool.CategoryPlanning, Impact: tool.ImpactReadOnly}},
	} {
		registry.Register(candidate)
	}
	return registry
}

func acpToolTurnFunctionNames(turn acpToolTurn) []string {
	names := make([]string, 0, len(turn.Tools))
	for _, candidate := range turn.Tools {
		fn, _ := candidate["function"].(map[string]any)
		if name, _ := fn["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func TestDispatchACPToolCallObservesWorkspaceChange(t *testing.T) {
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	runACPGit(t, root, "config", "user.name", "Buckley Test")
	runACPGit(t, root, "config", "user.email", "buckley@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	runACPGit(t, root, "add", "tracked.txt")
	runACPGit(t, root, "commit", "-qm", "base")

	registry := tool.NewEmptyRegistry()
	registry.Register(&acpStateTestTool{name: "read_file", metadata: tool.ToolMetadata{Impact: tool.ImpactReadOnly}})
	registry.Register(&acpStateTestTool{name: "noop_write", metadata: tool.ToolMetadata{Impact: tool.ImpactModifying}})
	registry.Register(&acpStateTestTool{
		name:     "write_file",
		metadata: tool.ToolMetadata{Impact: tool.ImpactModifying},
		execute: func() error {
			return os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("after\n"), 0o644)
		},
	})

	readOnly := dispatchACPToolCall(context.Background(), registry, nil, nil,
		model.ToolCall{ID: "call-read", Function: model.FunctionCall{Name: "read_file", Arguments: `{}`}},
		1, 1, &acpLoopState{}, root, "", nil, nil)
	if !readOnly.Success || readOnly.EffectClass != string(tool.ImpactReadOnly) || readOnly.StateObserved || readOnly.StateChanged {
		t.Fatalf("read-only outcome = %+v, want unobserved read-only success", readOnly)
	}

	noOp := dispatchACPToolCall(context.Background(), registry, nil, nil,
		model.ToolCall{ID: "call-noop", Function: model.FunctionCall{Name: "noop_write", Arguments: `{}`}},
		1, 1, &acpLoopState{}, root, "", nil, nil)
	if !noOp.Success || noOp.EffectClass != string(tool.ImpactModifying) || !noOp.StateObserved || noOp.StateChanged {
		t.Fatalf("no-op modifying outcome = %+v, want observed without change", noOp)
	}

	changed := dispatchACPToolCall(context.Background(), registry, nil, nil,
		model.ToolCall{ID: "call-change", Function: model.FunctionCall{Name: "write_file", Arguments: `{}`}},
		1, 1, &acpLoopState{}, root, "", nil, nil)
	if !changed.Success || changed.EffectClass != string(tool.ImpactModifying) || !changed.StateObserved || !changed.StateChanged {
		t.Fatalf("changed outcome = %+v, want observed workspace change", changed)
	}
}

func TestDispatchACPToolCallTagsTestingVerificationOnly(t *testing.T) {
	t.Parallel()

	registry := tool.NewEmptyRegistry()
	registry.Register(&acpStateTestTool{name: "read_file", metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactReadOnly}})
	registry.Register(&acpStateTestTool{name: "run_tests", metadata: tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly, Verification: true}})
	registry.Register(&acpStateTestTool{
		name:     "failing_tests",
		metadata: tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly, Verification: true},
		execute:  func() error { return nil },
	})

	readOnly := dispatchACPToolCall(context.Background(), registry, nil, nil,
		model.ToolCall{ID: "call-read", Function: model.FunctionCall{Name: "read_file", Arguments: `{}`}},
		1, 1, &acpLoopState{}, "", "", nil, nil)
	if readOnly.VerificationObserved || readOnly.VerificationPassed {
		t.Fatalf("ordinary read tagged as verification: %+v", readOnly)
	}

	passing := dispatchACPToolCall(context.Background(), registry, nil, nil,
		model.ToolCall{ID: "call-tests", Function: model.FunctionCall{Name: "run_tests", Arguments: `{}`}},
		1, 1, &acpLoopState{}, "", "", nil, nil)
	if !passing.VerificationObserved || !passing.VerificationPassed {
		t.Fatalf("passing test outcome not tagged: %+v", passing)
	}
}

func TestDispatchACPToolCallFallsBackToEffectWhenGitObservationUnavailable(t *testing.T) {
	root := t.TempDir()
	registry := tool.NewEmptyRegistry()
	registry.Register(&acpStateTestTool{
		name:     "write_file",
		metadata: tool.ToolMetadata{Impact: tool.ImpactModifying},
		execute: func() error {
			return os.WriteFile(filepath.Join(root, "created.txt"), []byte("created\n"), 0o644)
		},
	})

	outcome := dispatchACPToolCall(context.Background(), registry, nil, nil,
		model.ToolCall{ID: "call-change", Function: model.FunctionCall{Name: "write_file", Arguments: `{}`}},
		1, 1, &acpLoopState{}, root, "", nil, nil)
	if !outcome.Success || outcome.EffectClass != string(tool.ImpactModifying) || outcome.StateObserved || outcome.StateChanged {
		t.Fatalf("outcome = %+v, want successful modifying fallback without a claimed Git observation", outcome)
	}

	governor := agentloop.New(agentloop.Config{ReadOnlyWarningAt: 1, ReadOnlyActionAt: 2, MaxReadOnlyCalls: 3})
	_ = governor.ObserveProgress(string(tool.ImpactReadOnly), true, false, false)
	_ = governor.ObserveProgress(string(tool.ImpactReadOnly), true, false, false)
	if !governor.ActionRequired() {
		t.Fatal("test governor did not reach the action boundary")
	}
	_ = governor.ObserveProgress(outcome.EffectClass, outcome.Success, outcome.StateObserved, outcome.StateChanged)
	if governor.ActionRequired() {
		t.Fatal("successful modifying fallback did not reset the action boundary")
	}
}

func TestACPToolRiskImpactUnknownFailsClosed(t *testing.T) {
	if got := acpToolRiskImpact(nil, "missing"); got != tool.ImpactDestructive {
		t.Fatalf("nil-registry impact = %q, want destructive", got)
	}
	if got := acpToolRiskImpact(tool.NewEmptyRegistry(), "missing"); got != tool.ImpactDestructive {
		t.Fatalf("unknown-tool impact = %q, want destructive", got)
	}
}

func runACPGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func TestFormatACPToolResult_UsesModelOutputBoundary(t *testing.T) {
	fullOnly := strings.Repeat("full-only ", 4*1024)
	result := &builtin.Result{
		Success:       true,
		ShouldAbridge: true,
		Data:          map[string]any{"content": fullOnly},
		DisplayData:   map[string]any{"content": "display-only page"},
	}

	got := formatACPToolResult(result, nil)
	if strings.Contains(got, fullOnly) {
		t.Fatalf("model-facing output included full result (%d bytes)", len(got))
	}
	if !strings.Contains(got, "display-only page") {
		t.Fatalf("model-facing output = %q, want display page", got)
	}
}

func TestShouldNudgeACPToolUseHonorsLimitAndToolAvailability(t *testing.T) {
	t.Parallel()

	if !shouldNudgeACPToolUse(true, true, 0, "I'll search the repo first.") {
		t.Fatal("expected nudge when tools are available and model describes tool-like intent")
	}
	if !shouldNudgeACPToolUse(true, true, 0, "") {
		t.Fatal("expected nudge when a tool-enabled model returns an empty turn")
	}
	if shouldNudgeACPToolUse(false, true, 0, "I'll search the repo first.") {
		t.Fatal("did not expect nudge when tool use is disabled")
	}
	if shouldNudgeACPToolUse(true, false, 0, "I'll search the repo first.") {
		t.Fatal("did not expect nudge when no tools were exposed")
	}
	if shouldNudgeACPToolUse(true, true, acpMaxToolNudges, "I'll search the repo first.") {
		t.Fatal("did not expect nudge after max nudges")
	}
}

func TestSoleKnownACPToolInvocationMarkup(t *testing.T) {
	t.Parallel()

	registry := tool.NewRegistry()
	exactDeepSeek := `<search_text>
<query>reserved synthesis request Tools nil ToolChoice none</query>
<path>/home/draco/work/buckley</path>
</search_text>`
	proseExample := "A provider might return this example:\n" + exactDeepSeek
	fencedExample := "```xml\n" + exactDeepSeek + "\n```"
	malformedToolControl := `<tool_call>read_file|path=/tmp/work/adapter.js</think><tool_call>search_text|pattern=wasm.setButton</arg_value>`
	malformedToolControlWithFence := malformedToolControl + "\n```diff\n+not a safe answer\n```"

	tests := []struct {
		name     string
		text     string
		wantTool string
		want     bool
	}{
		{name: "deepseek xml invocation", text: exactDeepSeek, wantTool: "search_text", want: true},
		{name: "prose example remains prose", text: proseExample},
		{name: "fenced code example remains prose", text: fencedExample},
		{name: "ordinary answer", text: "The search_text tool accepts query and path parameters."},
		{name: "unknown tool", text: `<invented_tool><query>value</query></invented_tool>`},
		{name: "malformed particle tool control markup", text: malformedToolControl, wantTool: "read_file", want: true},
		{name: "malformed live tool control remains unsafe with later fence", text: malformedToolControlWithFence, wantTool: "read_file", want: true},
		{name: "malformed unknown tool control uses generic label", text: `<tool_call>` + strings.Repeat("x", 120) + `|arg=value`, wantTool: "tool_call", want: true},
		{name: "malformed markup prose example remains prose", text: "Example output: " + malformedToolControl},
		{name: "malformed markup fenced example remains prose", text: "```text\n" + malformedToolControl + "\n```"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			toolName, got := acpToolInvocationMarkup(tc.text, registry)
			if got != tc.want || toolName != tc.wantTool {
				t.Fatalf("acpToolInvocationMarkup() = (%q, %v), want (%q, %v)", toolName, got, tc.wantTool, tc.want)
			}
		})
	}
}

func TestBuildACPChatRequestAttachesTools(t *testing.T) {
	t.Parallel()

	conv := conversation.New("session-1")
	conv.AddUserMessage("hello")

	toolDef := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "read_file",
		},
	}
	req := buildACPChatRequest(nil, nil, nil, conv, "test/model", acpToolTurn{
		Tools:    []map[string]any{toolDef},
		UseTools: true,
		Enabled:  true,
	}, acpLoopLimits{})

	if req.Model != "test/model" {
		t.Fatalf("Model = %q, want test/model", req.Model)
	}
	if req.SessionID != "session-1" {
		t.Fatalf("SessionID = %q, want session-1", req.SessionID)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(req.Messages))
	}
	if len(req.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(req.Tools))
	}
	if req.ToolChoice != "auto" {
		t.Fatalf("ToolChoice = %q, want auto", req.ToolChoice)
	}
	if req.ParallelToolCalls != nil {
		t.Fatalf("ParallelToolCalls = %v, want omitted without catalog support", req.ParallelToolCalls)
	}
}

func TestBuildACPChatRequestOmitsDisabledTools(t *testing.T) {
	t.Parallel()

	conv := conversation.New("session-1")
	req := buildACPChatRequest(nil, nil, nil, conv, "test/model", acpToolTurn{
		Tools: []map[string]any{{"type": "function"}},
	}, acpLoopLimits{})

	if len(req.Tools) != 0 {
		t.Fatalf("tools = %d, want 0", len(req.Tools))
	}
	if req.ToolChoice != "" {
		t.Fatalf("ToolChoice = %q, want empty", req.ToolChoice)
	}
}

func TestBuildACPChatRequest_ProtocolReasoningPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		parameters []string
		policy     *model.ReasoningConfig
		want       *model.ReasoningConfig
	}{
		{name: "auto uses protocol effort without nested max tokens", configured: "auto", parameters: []string{"reasoning"}, policy: &model.ReasoningConfig{Effort: "medium", MaxTokens: 2048}, want: &model.ReasoningConfig{Effort: "medium"}},
		{name: "effort only omits nested max tokens", configured: "auto", parameters: []string{"reasoning_effort"}, policy: &model.ReasoningConfig{Effort: "medium", MaxTokens: 2048}, want: &model.ReasoningConfig{Effort: "medium"}},
		{name: "empty uses protocol effort without nested max tokens", parameters: []string{"reasoning"}, policy: &model.ReasoningConfig{Effort: "medium", MaxTokens: 2048}, want: &model.ReasoningConfig{Effort: "medium"}},
		{name: "token budget only is preserved", configured: "auto", parameters: []string{"reasoning"}, policy: &model.ReasoningConfig{MaxTokens: 2048}, want: &model.ReasoningConfig{MaxTokens: 2048}},
		{name: "explicit effort wins without nested max tokens", configured: "high", parameters: []string{"reasoning"}, policy: &model.ReasoningConfig{Effort: "medium", MaxTokens: 2048}, want: &model.ReasoningConfig{Effort: "high"}},
		{name: "legacy explicit effort unchanged", configured: "low", parameters: []string{"reasoning_effort"}, want: &model.ReasoningConfig{Effort: "low"}},
		{name: "unsupported omits reasoning", configured: "high", policy: &model.ReasoningConfig{Effort: "medium", MaxTokens: 2048}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, mgr, modelID := newACPReasoningTestManager(t, tt.parameters...)
			cfg.Models.Reasoning = tt.configured
			conv := conversation.New("reasoning-test")
			conv.AddUserMessage("hello")
			req := buildACPChatRequest(cfg, mgr, nil, conv, modelID, acpToolTurn{}, acpLoopLimits{ProtocolReasoning: tt.policy})
			if !reflect.DeepEqual(req.Reasoning, tt.want) {
				t.Fatalf("Reasoning = %+v, want %+v", req.Reasoning, tt.want)
			}
		})
	}
}

func TestBuildACPChatRequest_ExplicitOffDisablesReasoning(t *testing.T) {
	for _, configured := range []string{"off", "none"} {
		t.Run(configured, func(t *testing.T) {
			cfg, mgr, modelID := newACPReasoningTestManager(t, "reasoning_effort")
			cfg.Models.Reasoning = configured
			conv := conversation.New("reasoning-off")
			conv.AddUserMessage("hello")
			req := buildACPChatRequest(cfg, mgr, nil, conv, modelID, acpToolTurn{}, acpLoopLimits{ProtocolReasoning: &model.ReasoningConfig{Effort: "medium", MaxTokens: 2048}})
			if req.Reasoning == nil || req.Reasoning.Enabled == nil || *req.Reasoning.Enabled {
				t.Fatalf("Reasoning = %+v, want explicit enabled=false", req.Reasoning)
			}
			if req.Reasoning.Effort != "" || req.Reasoning.MaxTokens != 0 {
				t.Fatalf("disabled reasoning retained policy values: %+v", req.Reasoning)
			}
		})
	}
}

func newACPReasoningTestManager(t *testing.T, parameters ...string) (*config.Config, *model.Manager, string) {
	t.Helper()
	const localModel = "test-model"
	cfg := config.DefaultConfig()
	cfg.Providers = config.ProviderConfig{OpenAICompatible: config.OpenAICompatibleConfig{
		Enabled: true,
		BaseURL: "http://127.0.0.1:1/v1",
		Models:  []string{localModel},
	}}
	if len(parameters) > 0 {
		cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{localModel: parameters}
	}
	cfg.Models.DefaultProvider = "openai_compatible"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("model.NewManager: %v", err)
	}
	return cfg, mgr, "openai_compatible/" + localModel
}

// TestBuildACPChatRequestProjectionAloneBoundsLargeTranscript proves that
// buildACPChatRequest, now that it starts from the full transcript
// (conv.ToModelMessages) instead of running an independent
// ToEfficientModelMessages compaction pass first, still ends up bounded: the
// single CompactModelMessagesForRequest pass is enough on its own.
func TestBuildACPChatRequestProjectionAloneBoundsLargeTranscript(t *testing.T) {
	t.Parallel()

	conv := conversation.New("session-1")
	for i := 0; i < 400; i++ {
		conv.AddUserMessage(strings.Repeat("large transcript evidence ", 200))
		conv.AddAssistantMessage(strings.Repeat("assistant response evidence ", 200))
	}

	req := buildACPChatRequest(nil, nil, nil, conv, "test/model", acpToolTurn{}, acpLoopLimits{})

	full := conv.ToModelMessages()
	fullEstimate := model.EstimateRequestTokens(model.ChatRequest{Model: req.Model, Messages: full})
	projectedEstimate := model.EstimateRequestTokens(req)

	if projectedEstimate.Total >= fullEstimate.Total {
		t.Fatalf("projected request (%d tokens) is not smaller than the full transcript (%d tokens)", projectedEstimate.Total, fullEstimate.Total)
	}
	const boundedCeiling = 200_000 // generous upper bound for the default fallback budget
	if projectedEstimate.Total > boundedCeiling {
		t.Fatalf("projected request = %d tokens, want <= %d (projection alone should bound the request)", projectedEstimate.Total, boundedCeiling)
	}
}

func TestParseACPUserSkillCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		prompt      string
		wantHandled bool
		wantList    bool
		wantName    string
	}{
		{name: "plain prompt", prompt: "please inspect this", wantHandled: false},
		{name: "empty", prompt: "   ", wantHandled: false},
		{name: "skill list implicit", prompt: "/skill", wantHandled: true, wantList: true},
		{name: "skills list", prompt: "/skills", wantHandled: true, wantList: true},
		{name: "skill list explicit", prompt: "/skill list", wantHandled: true, wantList: true},
		{name: "skill activate", prompt: "/skill code-review", wantHandled: true, wantName: "code-review"},
		{name: "skill activate spaced name", prompt: "/skill release notes", wantHandled: true, wantName: "release notes"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, handled := parseACPUserSkillCommand(tc.prompt)
			if handled != tc.wantHandled {
				t.Fatalf("handled=%v want %v", handled, tc.wantHandled)
			}
			if !handled {
				return
			}
			if got.list != tc.wantList {
				t.Fatalf("list=%v want %v", got.list, tc.wantList)
			}
			if got.name != tc.wantName {
				t.Fatalf("name=%q want %q", got.name, tc.wantName)
			}
		})
	}
}

func TestHandleACPUserSkillCommandUnavailable(t *testing.T) {
	t.Parallel()

	handled, text := handleACPUserSkillCommand("/skill code-review", nil)
	if !handled {
		t.Fatalf("expected skill command to be handled")
	}
	if text != "Skill system unavailable in this session." {
		t.Fatalf("text=%q", text)
	}
}

func TestHandleACPUserSkillCommandListsSkills(t *testing.T) {
	t.Parallel()

	registry := skill.NewRegistry()
	mustRegisterSkill(t, registry, &skill.Skill{Name: "beta", Description: "Beta"})
	mustRegisterSkill(t, registry, &skill.Skill{Name: "alpha", Description: "Alpha"})

	handled, text := handleACPUserSkillCommand("/skills", &acpSessionState{skills: registry})
	if !handled {
		t.Fatalf("expected skills command to be handled")
	}
	want := "Available skills:\n- alpha\n- beta"
	if text != want {
		t.Fatalf("text=%q want %q", text, want)
	}
}

func TestHandleACPUserSkillCommandActivatesSkill(t *testing.T) {
	t.Parallel()

	registry := skill.NewRegistry()
	mustRegisterSkill(t, registry, &skill.Skill{
		Name:         "code-review",
		Description:  "Review code",
		Content:      "# Review\nInspect the diff.",
		AllowedTools: []string{"read_file"},
	})

	var injected []string
	runtime := skill.NewRuntimeState(func(content string) {
		injected = append(injected, content)
	})
	state := &acpSessionState{skills: registry, skillState: runtime}

	handled, text := handleACPUserSkillCommand("/skill code-review", state)
	if !handled {
		t.Fatalf("expected skill command to be handled")
	}
	if !strings.Contains(text, "Skill 'code-review' activated") {
		t.Fatalf("activation response missing message: %q", text)
	}
	if !strings.Contains(text, "# Skill Activated: code-review") {
		t.Fatalf("activation response missing content: %q", text)
	}
	if !registry.IsActive("code-review") {
		t.Fatalf("expected code-review to be active")
	}
	if len(injected) != 1 || !strings.Contains(injected[0], "# Skill Activated: code-review") {
		t.Fatalf("unexpected injected system messages: %#v", injected)
	}
	filter := runtime.ToolFilter()
	if len(filter) != 1 || filter[0] != "read_file" {
		t.Fatalf("tool filter=%v want [read_file]", filter)
	}
}

func mustRegisterSkill(t *testing.T, registry *skill.Registry, s *skill.Skill) {
	t.Helper()
	if err := registry.Register(s); err != nil {
		t.Fatalf("Register(%q): %v", s.Name, err)
	}
}
