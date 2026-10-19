package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/skill"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestExecuteOneShot_CallerToolScope(t *testing.T) {
	scoped := []string{"edit_file", "read_file", "run_tests"}
	for _, tc := range []struct {
		name     string
		explicit []string
		spec     agentspec.ToolSpec
		want     []string
		noTools  bool
	}{
		{name: "explicit", explicit: scoped, want: scoped},
		{name: "profile", spec: agentspec.ToolSpec{Allow: scoped}, want: scoped},
		{name: "deny only", spec: agentspec.ToolSpec{Deny: []string{"run_shell"}}},
		{name: "none", explicit: scoped, spec: agentspec.ToolSpec{Tier: "none"}, want: []string{}, noTools: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("GOWORK", "off")
			t.Setenv("BUCKLEY_SKILLS_PATH", t.TempDir())
			for name, content := range map[string]string{
				"go.mod":       "module scopefixture\n\ngo 1.26\n",
				"calc.go":      "package fixture\nfunc Add(a,b int)int{return a-b}\n",
				"calc_test.go": "package fixture\nimport \"testing\"\nfunc TestAdd(t *testing.T){if Add(2,3)!=5{t.Fatal(\"wrong sum\")}}\n",
			} {
				if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var requests atomic.Int32
			var schemaBytes, requestBytes, toolCount atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `{"data":[{"id":"scope-test","supported_parameters":["tools"]}]}`)
					return
				}
				var req model.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				names := make([]string, 0, len(req.Tools))
				for _, definition := range req.Tools {
					function := definition["function"].(map[string]any)
					names = append(names, function["name"].(string))
				}
				sort.Strings(names)
				if tc.want != nil && !reflect.DeepEqual(names, tc.want) {
					t.Errorf("offered tools = %v, want %v", names, tc.want)
				}
				if tc.name == "deny only" && (len(names) == 0 || containsString(names, "run_shell") || !containsString(names, "read_file")) {
					t.Errorf("deny-only profile broke tool selection: %v", names)
				}
				if n := requests.Add(1); n == 1 {
					definitions, _ := json.Marshal(req.Tools)
					wire, _ := json.Marshal(req)
					schemaBytes.Store(int64(len(definitions)))
					toolCount.Store(int64(len(req.Tools)))
					requestBytes.Store(int64(len(wire)))
				}
				if tc.noTools || requests.Load() > 2 {
					writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": "Tool scope respected."}, "stop")
					return
				}
				name, args := "edit_file", map[string]any{"path": "calc.go", "old_string": "a-b", "new_string": "a+b"}
				if requests.Load() == 2 {
					name, args = "run_tests", map[string]any{}
				}
				arguments, _ := json.Marshal(args)
				writeOneShotArtifactRouteSSE(t, w, map[string]any{"tool_calls": []map[string]any{{
					"index": 0, "id": fmt.Sprintf("scope-%d", requests.Load()), "type": "function",
					"function": map[string]any{"name": name, "arguments": string(arguments)},
				}}}, "tool_calls")
			}))
			t.Cleanup(server.Close)
			cfg := config.DefaultConfig()
			cfg.Approval.Mode, cfg.Sandbox.Mode, cfg.Sandbox.AllowUnsafe = "yolo", "disabled", true
			cfg.Memory.HyphaeRecall = false
			cfg.Models.DefaultProvider = "openai_compatible"
			cfg.Providers.OpenAICompatible.Enabled = true
			cfg.Providers.OpenAICompatible.APIKey = "test-key"
			cfg.Providers.OpenAICompatible.BaseURL = server.URL
			cfg.Providers.OpenAICompatible.Models = []string{"scope-test"}
			cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{"scope-test": {"tools"}}
			profile := &agentspec.RuntimeProfile{Spec: &agentspec.Spec{Tools: tc.spec}}
			profile.ApplyToConfig(cfg)
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			intent := agentloop.MutationIntent
			if tc.noTools {
				intent = agentloop.ReadOnlyIntent
			}
			code := -1
			var stdout string
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() {
					code = executeOneShot("Fix Add and verify it if tools are available.", cfg, mgr, nil, nil, oneShotOptions{
						ModelOverride: "openai_compatible/scope-test", AgentProfile: profile, AllowedTools: tc.explicit,
						Limits: acpLoopLimits{TaskIntent: intent},
					})
				})
			})
			if code != 0 || !strings.Contains(stdout, "Tool scope respected.") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !tc.noTools {
				content, err := os.ReadFile("calc.go")
				if err != nil || !strings.Contains(string(content), "a+b") || requests.Load() != 3 {
					t.Fatalf("scoped mutation did not finish: content=%q requests=%d error=%v", content, requests.Load(), err)
				}
			}
			t.Logf("tools=%d schema_bytes=%d first_request_bytes=%d requests=%d", toolCount.Load(), schemaBytes.Load(), requestBytes.Load(), requests.Load())
		})
	}
}

func TestSkillActivation_CallerScopeAlsoBoundsDispatch(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	var shellCalls atomic.Int32
	registry.Register(&acpStateTestTool{name: "run_shell", metadata: tool.ToolMetadata{Impact: tool.ImpactDestructive}, execute: func() error {
		shellCalls.Add(1)
		return nil
	}})
	registry.Register(&builtin.ReadFileTool{})
	state := skill.NewRuntimeState(nil)
	state.SetToolCeiling([]string{"activate_skill", "read_file"})
	skills := skill.NewRegistry()
	if err := skills.Register(&skill.Skill{Name: "broad", Description: "Requests tools outside the caller scope", Content: "Inspect the task.", AllowedTools: []string{"activate_skill", "run_shell"}}); err != nil {
		t.Fatal(err)
	}
	activation := &builtin.SkillActivationTool{Registry: skills, Conversation: state}
	registry.Register(activation)
	for _, action := range []string{"activate", "deactivate"} {
		result, err := activation.Execute(map[string]any{"action": action, "skill": "broad"})
		if err != nil || !result.Success {
			t.Fatalf("skill %s: %+v, %v", action, result, err)
		}
		turn := buildACPToolTurn(registry, state, nil, true, true, false, agentloop.UnknownIntent, true)
		if containsString(turn.AllowedTools, "run_shell") {
			t.Fatalf("skill %s expanded the caller scope: %v", action, turn.AllowedTools)
		}
		outcome := dispatchACPToolCall(context.Background(), registry, nil, nil,
			model.ToolCall{ID: "outside-scope", Function: model.FunctionCall{Name: "run_shell", Arguments: `{}`}},
			1, 1, &acpLoopState{allowedTools: turn.AllowedTools, allowHostTools: true}, t.TempDir(), "", nil, nil)
		if outcome.Success || shellCalls.Load() != 0 {
			t.Fatalf("excluded shell executed after %s: %+v", action, outcome)
		}
	}
}
