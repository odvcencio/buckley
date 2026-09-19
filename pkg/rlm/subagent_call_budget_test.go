package rlm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type budgetProbeTool struct {
	fakeReadTool
	calls int
	fail  bool
}

func (f *budgetProbeTool) Execute(params map[string]any) (*builtin.Result, error) {
	f.calls++
	if f.fail {
		return &builtin.Result{Success: false, Error: "fixture read failed"}, nil
	}
	return f.fakeReadTool.Execute(params)
}

func TestSubAgentCallBudget(t *testing.T) {
	const valid = `{"path":"fixture.go"}`
	for _, tc := range []struct {
		name       string
		limit      int
		args       []string
		fail       bool
		executions int
	}{
		{"one malformed", 1, []string{"{", valid}, false, 0},
		{"malformed batch", 2, []string{"{", "[", valid}, false, 0},
		{"failed registry read", 2, []string{"{", valid, valid}, true, 1},
		{"unlimited", 0, []string{"{", valid}, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tool.NewEmptyRegistry()
			probe := &budgetProbeTool{fakeReadTool: fakeReadTool{name: "read_file", body: "fixture body"}, fail: tc.fail}
			registry.Register(probe)
			calls := make([]model.ToolCall, len(tc.args))
			for i, args := range tc.args {
				calls[i] = model.ToolCall{ID: fmt.Sprintf("budget-%d", i), Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: args}}
			}
			result := &SubAgentResult{}
			outcomes, err := (&SubAgent{maxToolCalls: tc.limit}).executeTools(context.Background(), calls, registry, map[string]struct{}{"read_file": {}}, result)
			if err != nil || len(outcomes) != len(calls) {
				t.Fatalf("outcomes=%+v err=%v", outcomes, err)
			}
			if probe.calls != tc.executions {
				t.Fatalf("executions=%d want=%d", probe.calls, tc.executions)
			}
			for i, outcome := range outcomes {
				if outcome.ID != calls[i].ID || outcome.Name != "read_file" || outcome.Arguments != tc.args[i] {
					t.Fatalf("attempt metadata changed: %+v", outcome)
				}
				if !json.Valid([]byte(tc.args[i])) && (outcome.Success || !strings.HasPrefix(outcome.Result, "invalid arguments:")) {
					t.Fatalf("malformed arguments not rejected: %+v", outcome)
				}
			}
			prefix := len(calls)
			if tc.limit > 0 {
				prefix = min(prefix, tc.limit)
				last := outcomes[len(outcomes)-1]
				if last.Success || !strings.Contains(last.Result, "tool call budget exhausted") {
					t.Fatalf("last attempt bypassed budget: %+v", last)
				}
			} else if !outcomes[len(outcomes)-1].Success {
				t.Fatal("unlimited budget blocked valid execution")
			}
			if len(result.ToolCalls) < prefix || !reflect.DeepEqual(result.ToolCalls[:prefix], outcomes[:prefix]) {
				t.Fatalf("slot-consuming attempts missing from audit: %+v", result.ToolCalls)
			}
		})
	}
}

func TestSubAgentRequestBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		limit     int
		malformed bool
		noTools   bool
	}{
		{"finite success", 2, false, false},
		{"finite malformed", 2, true, false},
		{"unlimited", 0, false, false},
		{"no tools", 2, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var bodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, string(body))
				round := len(bodies)
				mu.Unlock()
				message := map[string]any{"role": "assistant", "content": "done"}
				finish := "stop"
				if !tc.noTools && round <= 2 {
					args := `{"path":"fixture.go"}`
					if tc.malformed {
						args = "{"
					}
					message["content"] = nil
					message["tool_calls"] = []model.ToolCall{{ID: fmt.Sprintf("probe-%d", round), Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: args}}}
					finish = "tool_calls"
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("request-%d", round), "model": "gpt-4o", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			registry := tool.NewEmptyRegistry()
			probe := &budgetProbeTool{fakeReadTool: fakeReadTool{name: "read_file", body: "observed fixture body"}}
			if !tc.noTools {
				registry.Register(probe)
			}
			agent, err := NewSubAgent(SubAgentConfig{ID: "budget-probe", Model: "gpt-4o", SystemPrompt: "Caller output contract stays intact.", MaxIterations: 10, MaxToolCalls: tc.limit, AllowedTools: []string{"read_file"}}, SubAgentDeps{Models: newSubAgentTestManager(t, server), Registry: registry})
			if err != nil {
				t.Fatal(err)
			}
			original := agent.systemPrompt
			result, err := agent.Execute(context.Background(), "Read the supplied fixture and hand off observed results.")
			if err != nil || result == nil || result.Summary != "done" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if agent.systemPrompt != original {
				t.Fatal("request reminder mutated persistent instructions")
			}
			mu.Lock()
			requests := append([]string(nil), bodies...)
			mu.Unlock()
			wantRequests, wantExecutions := 3, 2
			if tc.noTools {
				wantRequests, wantExecutions = 1, 0
			} else if tc.malformed {
				wantExecutions = 0
			}
			if len(requests) != wantRequests || probe.calls != wantExecutions {
				t.Fatalf("requests=%d executions=%d want=%d/%d", len(requests), probe.calls, wantRequests, wantExecutions)
			}
			for i, raw := range requests {
				var req struct {
					Messages []struct{ Role, Content string }
					Tools    []any
				}
				if err := json.Unmarshal([]byte(raw), &req); err != nil {
					t.Fatal(err)
				}
				if len(req.Messages) == 0 || req.Messages[0].Role != "system" || !strings.HasPrefix(req.Messages[0].Content, original) {
					t.Fatal("caller system instructions lost")
				}
				finite := tc.limit > 0 && !tc.noTools
				if finite && i < 2 {
					marker := fmt.Sprintf("Tool-call slots remaining: %d of %d", tc.limit-i, tc.limit)
					if len(req.Tools) == 0 || strings.Count(raw, "Tool-call slots remaining:") != 1 || !strings.Contains(req.Messages[0].Content, marker) {
						t.Fatalf("missing, stale, or duplicate countdown: %s", raw)
					}
					guidance := strings.TrimPrefix(req.Messages[0].Content, original)
					if len(strings.Fields(guidance)) > 65 || !strings.Contains(guidance, "Failed attempts consume slots") || !strings.Contains(guidance, "honest incomplete status") {
						t.Fatalf("invalid budget guidance: %q", guidance)
					}
				} else if strings.Contains(raw, "Tool-call slots remaining:") {
					t.Fatal("unlimited/no-tool/final request retained a reminder")
				}
				if (finite && i == 2 || tc.noTools) && len(req.Tools) != 0 {
					t.Fatal("tool budget did not force no-tool synthesis")
				}
				if !tc.noTools && i > 0 {
					if !strings.Contains(raw, fmt.Sprintf("probe-%d", i)) || !strings.Contains(raw, `"role":"tool"`) {
						t.Fatal("countdown replaced tool-call history")
					}
				}
			}
		})
	}
}
