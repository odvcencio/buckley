package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/subagent"
)

type builtinSubagentRunnerFunc func(context.Context, subagent.Request, func(int)) (string, error)

func (f builtinSubagentRunnerFunc) Run(ctx context.Context, request subagent.Request, started func(int)) (string, error) {
	return f(ctx, request, started)
}

func TestSubagentCommandArgs_GenericProfileIsScopedAndCleanedUp(t *testing.T) {
	args, cleanup, err := subagentCommandArgs(subagent.Request{Task: "inspect this"})
	if err != nil {
		t.Fatalf("subagentCommandArgs: %v", err)
	}
	if len(args) != 5 || args[0] != "agent" || args[1] != "run" || args[3] != "worker" || args[4] != "inspect this" {
		t.Fatalf("generic args = %v", args)
	}
	profilePath := args[2]
	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read generic profile: %v", err)
	}
	if !strings.Contains(string(data), "version: buckley.agent/v1") || !strings.Contains(string(data), "name: worker") {
		t.Fatalf("generic profile = %q", data)
	}
	cleanup()
	if _, err := os.Stat(profilePath); !os.IsNotExist(err) {
		t.Fatalf("generic profile should be removed, stat err=%v", err)
	}
}

func TestSubagentCommandArgs_NamedProjectProfileRemainsDirect(t *testing.T) {
	args, cleanup, err := subagentCommandArgs(subagent.Request{Agent: "reviewer", Spec: "daily", Task: "inspect this"})
	if err != nil {
		t.Fatalf("subagentCommandArgs: %v", err)
	}
	defer cleanup()
	if got, want := strings.Join(args, "|"), "agent|run|--project|--spec|daily|reviewer|inspect this"; got != want {
		t.Fatalf("named args = %q, want %q", got, want)
	}
}

func TestBuckleySubagentRunner_TransportsLiveCommandMailbox(t *testing.T) {
	script := filepath.Join(t.TempDir(), "mailbox-child.sh")
	content := `#!/bin/sh
i=0
while [ "$i" -lt 200 ]; do
  if [ -s "$BUCKLEY_SUBAGENT_MAILBOX_V1" ]; then
    cat "$BUCKLEY_SUBAGENT_MAILBOX_V1"
    exit 0
  fi
  i=$((i + 1))
  sleep 0.01
done
exit 2
`
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	manager := subagent.NewManager(&buckleySubagentRunner{command: script, workDir: t.TempDir()}, 1)
	t.Cleanup(func() { _ = manager.Close() })
	run, err := manager.Spawn("", "", "wait for command", 5)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	deliveryCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Deliver(deliveryCtx, run.ID, agentcoord.Message{ID: "msg-transport", RunID: run.ID, To: run.ID, From: "parent", Content: "inspect live"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer waitCancel()
	finished, err := manager.Wait(waitCtx, run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if finished.State != subagent.StateCompleted || !strings.Contains(finished.Output, "inspect live") {
		t.Fatalf("finished = %+v", finished)
	}
}

func TestSubagentTool_SetDurabilityBuildsReplayableCoordinator(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "subagents.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatalf("open evidence: %v", err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	manager := subagent.NewManager(builtinSubagentRunnerFunc(func(_ context.Context, _ subagent.Request, started func(int)) (string, error) {
		started(71)
		return "durable review", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })

	tool := &SubagentTool{manager: manager}
	tool.SetTelemetry(nil, "session-durable-tool")
	tool.SetDurability(ledger, evidenceStore)
	coordinator := tool.getCoordinator()
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID:           "run-durable-tool",
		ParentSessionID: "session-durable-tool",
		Task:            "inspect the patch",
		WorkspaceClaims: []string{"pkg/tool"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	completed, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if completed.State != agentcoord.RunCompleted || len(completed.Result.EvidenceRefs) < 2 {
		t.Fatalf("completed = %+v", completed)
	}
	durable, err := ledger.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if durable.Status != string(agentcoord.RunCompleted) || durable.Backend != "local-process" {
		t.Fatalf("durable projection = %+v", durable)
	}
}

func TestSplitOneShotOutput(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantOutput    string
		wantStatsNil  bool
		wantStatsKeys []string
	}{
		{
			name:         "empty input",
			input:        "",
			wantOutput:   "",
			wantStatsNil: true,
		},
		{
			name:         "whitespace only",
			input:        "   \n\t\n  ",
			wantOutput:   "",
			wantStatsNil: true,
		},
		{
			name:         "output with no stats",
			input:        "Just some regular output\nwith multiple lines",
			wantOutput:   "Just some regular output\nwith multiple lines",
			wantStatsNil: true,
		},
		{
			name:          "output with Session Statistics header",
			input:         "Response content here\n────────────────\nSession Statistics:\nModel: gpt-4\nTokens: 1234\nCost: $0.05",
			wantOutput:    "Response content here",
			wantStatsNil:  false,
			wantStatsKeys: []string{"model", "tokens", "cost"},
		},
		{
			name:          "output with stats prefix (Model:)",
			input:         "Some output\nModel: gpt-4o\nProvider: OpenAI\nTime: 2.5s",
			wantOutput:    "Some output",
			wantStatsNil:  false,
			wantStatsKeys: []string{"model", "provider", "time"},
		},
		{
			name:         "stats prefix with less than 2 entries",
			input:        "Some output\nModel: gpt-4o",
			wantOutput:   "Some output\nModel: gpt-4o",
			wantStatsNil: true,
		},
		{
			name:          "Session Statistics with separator before",
			input:         "Output\n────\nSession Statistics:\nTokens: 500",
			wantOutput:    "Output",
			wantStatsNil:  false,
			wantStatsKeys: []string{"tokens"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotOutput, gotStats := splitOneShotOutput(tc.input)
			if gotOutput != tc.wantOutput {
				t.Errorf("splitOneShotOutput(%q) output = %q, want %q", tc.input, gotOutput, tc.wantOutput)
			}
			if tc.wantStatsNil && gotStats != nil {
				t.Errorf("splitOneShotOutput(%q) stats = %v, want nil", tc.input, gotStats)
			}
			if !tc.wantStatsNil && gotStats == nil {
				t.Errorf("splitOneShotOutput(%q) stats = nil, want non-nil with keys %v", tc.input, tc.wantStatsKeys)
			}
			if !tc.wantStatsNil && gotStats != nil {
				for _, key := range tc.wantStatsKeys {
					if _, ok := gotStats[key]; !ok {
						t.Errorf("splitOneShotOutput(%q) stats missing key %q", tc.input, key)
					}
				}
			}
		})
	}
}

func TestHasOneShotStatPrefix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "empty string", input: "", want: false},
		{name: "whitespace only", input: "   ", want: false},
		{name: "model lowercase", input: "model: gpt-4", want: true},
		{name: "Model uppercase", input: "Model: claude-3", want: true},
		{name: "MODEL all caps", input: "MODEL: test", want: true},
		{name: "provider prefix", input: "provider: OpenAI", want: true},
		{name: "Provider prefix", input: "Provider: Anthropic", want: true},
		{name: "time prefix", input: "time: 2.5s", want: true},
		{name: "Time prefix", input: "Time: 1m30s", want: true},
		{name: "tokens prefix", input: "tokens: 1234", want: true},
		{name: "Tokens prefix", input: "Tokens: 5678", want: true},
		{name: "cost prefix", input: "cost: $0.05", want: true},
		{name: "Cost prefix", input: "Cost: $1.23", want: true},
		{name: "with leading whitespace", input: "  model: test", want: true},
		{name: "no colon", input: "model gpt-4", want: false},
		{name: "random text", input: "hello world", want: false},
		{name: "partial match", input: "tokenization", want: false},
		{name: "model in middle", input: "the model: is here", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hasOneShotStatPrefix(tc.input)
			if got != tc.want {
				t.Errorf("hasOneShotStatPrefix(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseOneShotStats(t *testing.T) {
	tests := []struct {
		name       string
		lines      []string
		wantNil    bool
		wantKeys   map[string]any
		wantTokens any
	}{
		{
			name:    "empty lines",
			lines:   []string{},
			wantNil: true,
		},
		{
			name:    "only whitespace lines",
			lines:   []string{"", "   ", "\t"},
			wantNil: true,
		},
		{
			name:    "no recognized prefixes",
			lines:   []string{"random line", "another line"},
			wantNil: true,
		},
		{
			name:  "model only",
			lines: []string{"Model: gpt-4o"},
			wantKeys: map[string]any{
				"model": "gpt-4o",
			},
		},
		{
			name:  "provider only",
			lines: []string{"Provider: OpenAI"},
			wantKeys: map[string]any{
				"provider": "OpenAI",
			},
		},
		{
			name:  "time only",
			lines: []string{"Time: 2.5s"},
			wantKeys: map[string]any{
				"time": "2.5s",
			},
		},
		{
			name:       "tokens as integer",
			lines:      []string{"Tokens: 1234"},
			wantTokens: 1234,
		},
		{
			name:       "tokens as string (non-numeric)",
			lines:      []string{"Tokens: unknown"},
			wantTokens: "unknown",
		},
		{
			name:  "cost with USD parsing",
			lines: []string{"Cost: $0.05"},
			wantKeys: map[string]any{
				"cost":     "$0.05",
				"cost_usd": 0.05,
			},
		},
		{
			name:  "cost without dollar sign",
			lines: []string{"Cost: 0.10"},
			wantKeys: map[string]any{
				"cost":     "0.10",
				"cost_usd": 0.10,
			},
		},
		{
			name:  "cost non-numeric",
			lines: []string{"Cost: unknown"},
			wantKeys: map[string]any{
				"cost": "unknown",
			},
		},
		{
			name:  "all fields",
			lines: []string{"Model: gpt-4", "Provider: OpenAI", "Time: 1s", "Tokens: 100", "Cost: $0.01"},
			wantKeys: map[string]any{
				"model":    "gpt-4",
				"provider": "OpenAI",
				"time":     "1s",
				"tokens":   100,
				"cost":     "$0.01",
				"cost_usd": 0.01,
			},
		},
		{
			name:  "stops at separator",
			lines: []string{"Model: gpt-4", "────────────", "Provider: ignored"},
			wantKeys: map[string]any{
				"model": "gpt-4",
			},
		},
		{
			name:  "empty values are skipped",
			lines: []string{"Model:", "Provider: test"},
			wantKeys: map[string]any{
				"provider": "test",
			},
		},
		{
			name:    "tokens with empty value",
			lines:   []string{"Tokens:"},
			wantNil: true,
		},
		{
			name:    "cost with empty value",
			lines:   []string{"Cost:"},
			wantNil: true,
		},
		{
			name:  "mixed case prefixes",
			lines: []string{"mOdEl: test1", "pRoViDeR: test2"},
			wantKeys: map[string]any{
				"model":    "test1",
				"provider": "test2",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOneShotStats(tc.lines)
			if tc.wantNil {
				if got != nil {
					t.Errorf("parseOneShotStats(%v) = %v, want nil", tc.lines, got)
				}
				return
			}
			if got == nil {
				t.Errorf("parseOneShotStats(%v) = nil, want non-nil", tc.lines)
				return
			}
			if tc.wantTokens != nil {
				if got["tokens"] != tc.wantTokens {
					t.Errorf("parseOneShotStats tokens = %v (%T), want %v (%T)", got["tokens"], got["tokens"], tc.wantTokens, tc.wantTokens)
				}
			}
			for key, wantVal := range tc.wantKeys {
				if gotVal, ok := got[key]; !ok {
					t.Errorf("parseOneShotStats missing key %q", key)
				} else if gotVal != wantVal {
					t.Errorf("parseOneShotStats[%q] = %v, want %v", key, gotVal, wantVal)
				}
			}
		})
	}
}

func TestCodexTool(t *testing.T) {
	tool := &CodexTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "invoke_codex" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "invoke_codex")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing prompt parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing prompt")
		}
	})
}

func TestClaudeTool(t *testing.T) {
	tool := &ClaudeTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "invoke_claude" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "invoke_claude")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing prompt parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing prompt")
		}
	})
}

func TestBuckleyTool(t *testing.T) {
	tool := &BuckleyTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "invoke_buckley" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "invoke_buckley")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing prompt parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing prompt")
		}
	})
}

func TestSubagentTool(t *testing.T) {
	tool := &SubagentTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "spawn_subagent" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "spawn_subagent")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing task parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing task")
		}
	})

	t.Run("spawn and wait for named agent", func(t *testing.T) {
		manager := subagent.NewManager(builtinSubagentRunnerFunc(func(_ context.Context, request subagent.Request, started func(int)) (string, error) {
			if request.Agent != "reviewer" || request.Spec != "daily" || request.Task != "inspect this" {
				t.Fatalf("unexpected request: %+v", request)
			}
			started(99)
			return "review complete", nil
		}), 1)
		t.Cleanup(func() { _ = manager.Close() })
		managedTool := &SubagentTool{manager: manager}
		spawned, err := managedTool.Execute(map[string]any{
			"action":       "spawn",
			"agent":        "reviewer",
			"spec":         "daily",
			"initial_task": "inspect this",
		})
		if err != nil || !spawned.Success {
			t.Fatalf("spawn result=%+v err=%v", spawned, err)
		}
		run := spawned.Data["run"].(agentcoord.Run)
		finished, err := managedTool.Execute(map[string]any{"action": "wait", "id": run.ID, "timeout_seconds": 5})
		if err != nil || !finished.Success {
			t.Fatalf("wait result=%+v err=%v", finished, err)
		}
		got := finished.Data["run"].(agentcoord.Run)
		if got.State != agentcoord.RunCompleted || got.Result.Summary != "review complete" || got.PID != 99 {
			t.Fatalf("unexpected completed run: %+v", got)
		}
	})
}

func TestSubagentTool_GroupSendAndCancel(t *testing.T) {
	manager := subagent.NewManager(builtinSubagentRunnerFunc(func(ctx context.Context, _ subagent.Request, started func(int)) (string, error) {
		started(101)
		<-ctx.Done()
		return "", ctx.Err()
	}), 3)
	t.Cleanup(func() { _ = manager.Close() })
	tool := &SubagentTool{manager: manager}
	tool.SetTelemetry(nil, "session-group-control")
	coordinator := tool.getCoordinator()

	spawn := func(agent, task string) string {
		run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{ParentSessionID: "session-group-control", Agent: agent, Task: task})
		if err != nil {
			t.Fatalf("spawn %s: %v", agent, err)
		}
		return run.ID
	}
	first := spawn("reviewer", "review one")
	second := spawn("reviewer", "review two")
	third := spawn("coder", "implement")

	sent, err := tool.Execute(map[string]any{"action": "send", "id": "agent:reviewer", "message": "report blockers now"})
	if err != nil || !sent.Success {
		t.Fatalf("group send: result=%+v err=%v", sent, err)
	}
	if got := sent.Data["succeeded"].(int); got != 2 {
		t.Fatalf("group send succeeded = %d, want 2", got)
	}
	for _, id := range []string{first, second} {
		messages, err := tool.Execute(map[string]any{"action": "messages", "id": id})
		if err != nil || !messages.Success || messages.Data["count"].(int) != 1 {
			t.Fatalf("messages %s: result=%+v err=%v", id, messages, err)
		}
	}
	coderMessages, err := tool.Execute(map[string]any{"action": "messages", "id": third})
	if err != nil || !coderMessages.Success || coderMessages.Data["count"].(int) != 0 {
		t.Fatalf("coder messages: result=%+v err=%v", coderMessages, err)
	}

	cancelled, err := tool.Execute(map[string]any{"action": "cancel", "id": "all", "reason": "test complete"})
	if err != nil || !cancelled.Success || cancelled.Data["succeeded"].(int) != 3 {
		t.Fatalf("cancel all: result=%+v err=%v", cancelled, err)
	}
}

func TestSubagentTool_UserSpawnBypassesModelCooldown(t *testing.T) {
	manager := subagent.NewManager(builtinSubagentRunnerFunc(func(ctx context.Context, _ subagent.Request, started func(int)) (string, error) {
		started(102)
		<-ctx.Done()
		return "", ctx.Err()
	}), 2)
	t.Cleanup(func() { _ = manager.Close() })
	tool := &SubagentTool{manager: manager}
	for _, task := range []string{"first explicit task", "second explicit task"} {
		result, err := tool.ExecuteUserCommand(context.Background(), map[string]any{"action": "spawn", "initial_task": task})
		if err != nil || !result.Success {
			t.Fatalf("explicit spawn %q: result=%+v err=%v", task, result, err)
		}
	}
}
