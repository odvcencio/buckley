package tui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/skill"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/fluffyui/backend/sim"
)

func TestBuildToolLoopRequest_SkillDiscoveryWorkingSet(t *testing.T) {
	for _, tc := range []struct {
		name     string
		filter   []string
		ceiling  []string
		useTools bool
		want     []string
	}{
		{name: "default stays small", useTools: true, want: []string{"discover_tools", "read_file"}},
		{name: "explicit specialized tool", filter: []string{"list_directory"}, useTools: true, want: []string{"list_directory"}},
		{name: "empty filter", filter: []string{}, useTools: true},
		{name: "caller ceiling", filter: []string{"list_directory"}, ceiling: []string{"read_file"}, useTools: true},
		{name: "tools disabled", filter: []string{"list_directory"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tool.NewRegistry()
			registry.EnableDynamicDiscovery([]string{"read_file"})
			state := skill.NewRuntimeState(nil)
			if tc.filter != nil {
				state.SetToolFilter(tc.filter)
			}
			state.SetToolCeiling(tc.ceiling)
			sess := &SessionState{ID: "working-set", Conversation: conversation.New("working-set"), ToolRegistry: registry, SkillState: state}
			ctrl := &Controller{}
			req, enabled := ctrl.buildToolLoopRequest(sess, "gpt-4o", tc.useTools, toolLoopAllowedTools(sess))
			names := skillDiscoveryToolNames(req.Tools)
			if !reflect.DeepEqual(names, tc.want) || enabled != (len(tc.want) > 0) {
				t.Fatalf("tools=%v enabled=%t, want %v", names, enabled, tc.want)
			}
		})
	}
}

func TestRunToolLoop_SkillChangesRefreshSchemasAndDispatch(t *testing.T) {
	for _, initialFilter := range []bool{false, true} {
		name := "unrestricted"
		if initialFilter {
			name = "initially restricted"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			const marker = "skill-discovery-evidence.txt"
			if err := os.WriteFile(filepath.Join(dir, marker), []byte("completed-work-survived"), 0600); err != nil {
				t.Fatal(err)
			}
			var requests []model.ChatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req model.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				requests = append(requests, req)
				round := len(requests)
				w.Header().Set("Content-Type", "text/event-stream")
				delta := map[string]any{"role": "assistant", "content": "finished"}
				finish := "stop"
				var name string
				var params map[string]any
				switch round {
				case 1:
					name, params = "activate_skill", map[string]any{"action": "activate", "skill": "directory-review"}
				case 2:
					name, params = "list_directory", map[string]any{"path": "."}
				case 3:
					name, params = "activate_skill", map[string]any{"action": "deactivate", "skill": "directory-review"}
				case 4:
					name, params = "read_file", map[string]any{"path": marker}
				}
				if name != "" {
					args, _ := json.Marshal(params)
					delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "skill-call-" + strconv.Itoa(round), "type": "function", "function": map[string]any{"name": name, "arguments": string(args)}}}}
					finish = "tool_calls"
				}
				payload, _ := json.Marshal(map[string]any{"id": "skill-flow", "model": "gpt-4o", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
				_, _ = io.WriteString(w, "data: "+string(payload)+"\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			cfg := newStreamIntegrationConfig(server.URL)
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
			if err != nil {
				t.Fatal(err)
			}
			conv := conversation.New("skill-flow")
			conv.AddUserMessage("Use the directory-review skill, inspect the directory, deactivate it, then read the evidence file.")
			state := skill.NewRuntimeState(conv.AddSystemMessage)
			if initialFilter {
				state.SetToolFilter([]string{"activate_skill", "read_file"})
			}
			skills := skill.NewRegistry()
			if err := skills.Register(&skill.Skill{Name: "directory-review", Description: "Directory inspection", Content: "Inspect the directory, then deactivate this skill.", AllowedTools: []string{"activate_skill", "list_directory"}}); err != nil {
				t.Fatal(err)
			}
			registry := tool.NewRegistry()
			registry.SetWorkDir(dir)
			registry.Register(&builtin.SkillActivationTool{Registry: skills, Conversation: state})
			registry.EnableDynamicDiscovery([]string{"activate_skill", "read_file"})
			sess := &SessionState{ID: "skill-flow", Conversation: conv, ToolRegistry: registry, SkillState: state, SkillRegistry: skills}
			ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: dir}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			result, err := ctrl.runToolLoopWithIntent(ctx, sess, "gpt-4o", agentloop.ReadOnlyIntent)
			if err != nil {
				t.Fatal(err)
			}
			if result.Text != "finished" || len(requests) != 5 {
				t.Fatalf("result=%q requests=%d, want finished in five requests", result.Text, len(requests))
			}
			for i, req := range requests {
				names := skillDiscoveryToolNames(req.Tools)
				if i == 1 || i == 2 {
					want := []string{"activate_skill", "list_directory"}
					if !reflect.DeepEqual(names, want) {
						t.Errorf("request %d tools=%v, want %v", i+1, names, want)
					}
				} else if !strings.Contains(strings.Join(names, ","), "read_file") {
					t.Errorf("request %d did not restore read_file: %v", i+1, names)
				}
			}
			var tools []conversation.Message
			for _, msg := range conv.Messages {
				if msg.Role == "tool" {
					tools = append(tools, msg)
				}
			}
			if len(tools) != 4 {
				t.Fatalf("completed tool results=%d, want 4", len(tools))
			}
			if !strings.Contains(conversation.GetContentAsString(tools[1].Content), marker) || !strings.Contains(conversation.GetContentAsString(tools[3].Content), "completed-work-survived") {
				t.Fatalf("actual directory/read evidence missing: %+v", tools)
			}
			last, _ := json.Marshal(requests[4].Messages)
			if !strings.Contains(string(last), marker) || !strings.Contains(string(last), "completed-work-survived") {
				t.Fatal("completed results missing from the next model request")
			}
		})
	}
}

func skillDiscoveryToolNames(tools []map[string]any) []string {
	var names []string
	for _, definition := range tools {
		function, _ := definition["function"].(map[string]any)
		name, _ := function["name"].(string)
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
