package tui

import (
	"maps"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/fluffyui/backend/sim"
)

type subagentCommandProbeTool struct {
	called bool
}

func (t *subagentCommandProbeTool) Name() string        { return "spawn_subagent" }
func (t *subagentCommandProbeTool) Description() string { return "test probe" }
func (t *subagentCommandProbeTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{}
}
func (t *subagentCommandProbeTool) Execute(map[string]any) (*builtin.Result, error) {
	t.called = true
	return &builtin.Result{Success: true}, nil
}

func TestParseSubagentCommand(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantAction string
		wantParams map[string]any
		wantErr    string
	}{
		{name: "list default", wantAction: "list", wantParams: map[string]any{"action": "list"}},
		{name: "list next", args: []string{"list", "next"}, wantAction: "list", wantParams: map[string]any{"action": "list", "cursor": "next"}},
		{name: "invalid list page", args: []string{"list", "other"}, wantErr: "usage"},
		{name: "generic spawn", args: []string{"spawn", "inspect", "the", "race"}, wantAction: "spawn", wantParams: map[string]any{"action": "spawn", "initial_task": "inspect the race"}},
		{name: "named spawn", args: []string{"spawn", "@reviewer", "inspect", "this"}, wantAction: "spawn", wantParams: map[string]any{"action": "spawn", "agent": "reviewer", "initial_task": "inspect this"}},
		{name: "group send", args: []string{"send", "agent:reviewer", "report", "blockers"}, wantAction: "send", wantParams: map[string]any{"action": "send", "id": "agent:reviewer", "message": "report blockers"}},
		{name: "all cancel", args: []string{"cancel", "all", "stop", "now"}, wantAction: "cancel", wantParams: map[string]any{"action": "cancel", "id": "all", "reason": "stop now"}},
		{name: "missing message", args: []string{"steer", "all"}, wantErr: "usage"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSubagentCommand(test.args)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSubagentCommand: %v", err)
			}
			if got.action != test.wantAction || !reflect.DeepEqual(got.params, test.wantParams) {
				t.Fatalf("command = %+v, want action=%s params=%v", got, test.wantAction, test.wantParams)
			}
		})
	}
}

func TestFormatSubagentCommandResult_GroupAndList(t *testing.T) {
	group, ok := formatSubagentCommandResult("send", &builtin.Result{Success: true, Data: map[string]any{
		"succeeded": 2,
		"targets":   []string{"run-1", "run-2"},
	}})
	if !ok || group != "Subagent send reached 2/2 target(s)." {
		t.Fatalf("group result = %q, %v", group, ok)
	}
	list, ok := formatSubagentCommandResult("list", &builtin.Result{Success: true, Data: map[string]any{
		"runs": []agentcoord.Run{{ID: "run-1", State: agentcoord.RunRunning, Task: agentcoord.TaskSpec{Agent: "reviewer", Task: "inspect"}}},
	}})
	if !ok || !strings.Contains(list, "run-1 [running] reviewer — inspect") {
		t.Fatalf("list result = %q, %v", list, ok)
	}
}

func TestHandleSubagentCommand_UsesGovernedRegistry(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.finalizeBackend)
	registry := tool.NewEmptyRegistry()
	probe := &subagentCommandProbeTool{}
	registry.Register(probe)
	hookCalled := false
	registry.Hooks().RegisterPreHook("spawn_subagent", func(*tool.ExecutionContext) tool.HookResult {
		hookCalled = true
		return tool.HookResult{Abort: true, AbortReason: "governed denial"}
	})
	controller := &Controller{
		app:            app,
		sessions:       []*SessionState{{ToolRegistry: registry}},
		currentSession: 0,
	}

	controller.handleSubagentCommand([]string{"list"})
	if !hookCalled || probe.called {
		t.Fatalf("registry governance hook=%v underlying tool called=%v", hookCalled, probe.called)
	}
}

type subagentPageProbeTool struct {
	name  string
	calls []map[string]any
}

func (t *subagentPageProbeTool) Name() string        { return "spawn_subagent" }
func (t *subagentPageProbeTool) Description() string { return "page probe" }
func (t *subagentPageProbeTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{}
}
func (t *subagentPageProbeTool) Execute(params map[string]any) (*builtin.Result, error) {
	t.calls = append(t.calls, maps.Clone(params))
	data := map[string]any{"runs": []agentcoord.Run{{ID: t.name, State: agentcoord.RunRunning}}}
	if params["cursor"] == nil {
		data["next_cursor"], data["has_more"] = t.name+"-next", true
	}
	return &builtin.Result{Success: true, Data: data}, nil
}

func TestHandleSubagentCommand_ListContinuationStaysInItsSession(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.finalizeBackend)
	controller := &Controller{app: app}
	probes := []*subagentPageProbeTool{{name: "first"}, {name: "second"}}
	for _, probe := range probes {
		registry := tool.NewEmptyRegistry()
		registry.Register(probe)
		controller.sessions = append(controller.sessions, &SessionState{ID: probe.name, ToolRegistry: registry})
	}
	controller.handleSubagentCommand(nil)
	controller.currentSession = 1
	controller.handleSubagentCommand([]string{"list", "next"})
	if len(probes[1].calls) != 0 {
		t.Fatal("second session used first session's cursor")
	}
	controller.handleSubagentCommand(nil)
	controller.currentSession = 0
	controller.handleSubagentCommand([]string{"list", "next"})
	if got := probes[0].calls[1]["cursor"]; got != "first-next" {
		t.Fatalf("first cursor=%v", got)
	}
	controller.handleSubagentCommand([]string{"list", "next"})
	if len(probes[0].calls) != 2 {
		t.Fatal("exhausted cursor was sent again")
	}
	controller.currentSession = 1
	controller.handleSubagentCommand([]string{"list", "next"})
	if got := probes[1].calls[1]["cursor"]; got != "second-next" {
		t.Fatalf("second cursor=%v", got)
	}
}

func TestFormatSubagentCommandResult_BoundsTasksAndShowsNextPage(t *testing.T) {
	message, ok := formatSubagentCommandResult("list", &builtin.Result{Success: true, Data: map[string]any{
		"runs":     []agentcoord.Run{{ID: "run", State: agentcoord.RunCompleted, Task: agentcoord.TaskSpec{Task: strings.Repeat("large task\n", 1000)}}},
		"has_more": true,
	}})
	if !ok || len(message) > 512 || !strings.Contains(message, "/agent list next") {
		t.Fatalf("unbounded or unreachable page: bytes=%d message=%q", len(message), message)
	}
}

func TestFormatSubagentCommandResult_StatusShowsBoundedFindings(t *testing.T) {
	message, ok := formatSubagentCommandResult("status", &builtin.Result{Success: true, Data: map[string]any{
		"run": agentcoord.Run{ID: "run", State: agentcoord.RunCompleted,
			Task:   agentcoord.TaskSpec{Task: strings.Repeat("large task\n", 1000)},
			Result: agentcoord.Result{Summary: "Verified finding: " + strings.Repeat("report detail\n", 1000)}},
	}})
	if !ok || len(message) > 4400 || !strings.Contains(message, "Verified finding:") {
		t.Fatalf("status lost findings or exceeded its budget: bytes=%d", len(message))
	}
}

func TestHandleCommand_SubagentInstructionsPreserveFormatting(t *testing.T) {
	tests := []struct {
		name, text string
		want       map[string]any
	}{
		{name: "multiline spawn", text: "/agent spawn @reviewer - Review:\n- first\n- second", want: map[string]any{"action": "spawn", "agent": "reviewer", "initial_task": "Review:\n- first\n- second"}},
		{name: "tabs and quotes", text: "/agent\tsend\tall\tRun \"go test\"\tthen check", want: map[string]any{"action": "send", "id": "all", "message": "Run \"go test\"\tthen check"}},
		{name: "unicode and leading whitespace", text: " \t/agent\u2003steer\u2003agent:reviewer\u2003Inspect café\n  λ", want: map[string]any{"action": "steer", "id": "agent:reviewer", "message": "Inspect café\n  λ"}},
		{name: "multiline cancel reason", text: "/agent cancel all Please stop:\n  task complete", want: map[string]any{"action": "cancel", "id": "all", "reason": "Please stop:\n  task complete"}},
		{name: "uppercase alias", text: "/AGENTS", want: map[string]any{"action": "list"}},
		{name: "unicode fold is not alias", text: "/agentſ"},
		{name: "blank", text: "/agent \t\n", want: map[string]any{"action": "list"}},
		{name: "missing body", text: "/agent send all\t"},
		{name: "invalid action", text: "/agent wat x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(app.finalizeBackend)
			registry := tool.NewEmptyRegistry()
			probe := &subagentPageProbeTool{}
			registry.Register(probe)
			controller := &Controller{app: app, sessions: []*SessionState{{ToolRegistry: registry}}, currentSession: 0}
			controller.handleCommand(tc.text)
			if tc.want == nil {
				if len(probe.calls) != 0 {
					t.Fatalf("unexpected calls: %#v", probe.calls)
				}
				return
			}
			if len(probe.calls) != 1 || !reflect.DeepEqual(probe.calls[0], tc.want) {
				t.Fatalf("calls = %#v, want %#v", probe.calls, tc.want)
			}
		})
	}
}
