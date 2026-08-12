package tui

import (
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestParseSubagentCommand(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantAction string
		wantParams map[string]any
		wantErr    string
	}{
		{name: "list default", wantAction: "list", wantParams: map[string]any{"action": "list"}},
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
