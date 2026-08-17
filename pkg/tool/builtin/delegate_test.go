package builtin

import (
	"context"
	"math"
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

func installDelegationGuardForTest(t *testing.T) *DelegationGuard {
	t.Helper()
	guard := &DelegationGuard{
		delegationTimes: make([]time.Time, 0),
		lastDelegation:  make(map[string]time.Time),
	}
	previous := globalDelegationGuard
	globalDelegationGuard = guard
	t.Cleanup(func() { globalDelegationGuard = previous })
	return guard
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

func TestBuckleySubagentRunner_TransportsExecutionEnvelope(t *testing.T) {
	script := filepath.Join(t.TempDir(), "contract-child.sh")
	content := `#!/bin/sh
printf '%s' "$BUCKLEY_SUBAGENT_CONTRACT_V1"
`
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	runner := &buckleySubagentRunner{command: script, workDir: t.TempDir()}
	output, err := runner.Run(context.Background(), subagent.Request{
		ID:              "run-child",
		ParentRunID:     "run-parent",
		ParentSessionID: "session-parent",
		TaskID:          "task-child",
		Task:            "inspect",
		StepCap:         9,
		TimeoutSeconds:  80,
		Budget: agentcoord.Budget{
			MaxToolCalls:     15,
			MaxModelRequests: 12,
			MaxElapsedSecond: 60,
			MaxCostUSD:       2.25,
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	contract, present, err := subagent.DecodeChildContract(output)
	if err != nil || !present {
		t.Fatalf("DecodeChildContract: present=%t err=%v output=%q", present, err, output)
	}
	if contract.RunID != "run-child" || contract.ParentRunID != "run-parent" || contract.ParentSessionID != "session-parent" || contract.TaskID != "task-child" {
		t.Fatalf("transported lineage = %+v", contract)
	}
	if contract.StepCap != 9 || contract.TimeoutSeconds != 80 || contract.Budget.MaxToolCalls != 15 || contract.Budget.MaxModelRequests != 12 || contract.Budget.MaxElapsedSecond != 60 || contract.Budget.MaxCostUSD != 2.25 {
		t.Fatalf("transported limits = %+v", contract)
	}
}

func TestBuckleySubagentRunner_HighVolumeOutputUsesBoundedSpool(t *testing.T) {
	script := filepath.Join(t.TempDir(), "verbose-child.sh")
	content := "#!/bin/sh\ndd if=/dev/zero bs=1048576 count=8 2>/dev/null\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	const limit = int64(64 * 1024)
	runner := &buckleySubagentRunner{command: script, workDir: t.TempDir(), outputSpoolLimit: limit}
	capture, err := runner.RunCaptured(context.Background(), subagent.Request{Task: "emit lots of output"}, nil)
	if err != nil {
		t.Fatalf("RunCaptured: %v", err)
	}
	defer os.Remove(capture.SpoolPath)
	if !capture.Truncated || capture.ObservedBytes != 8*1024*1024 || capture.CapturedBytes != limit || capture.LimitBytes != limit {
		t.Fatalf("capture = %+v", capture)
	}
	if int64(len(capture.Preview)) > limit {
		t.Fatalf("preview retained %d bytes in memory, disk ceiling is %d", len(capture.Preview), limit)
	}
	info, err := os.Stat(capture.SpoolPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != limit {
		t.Fatalf("spool size = %d, want %d", info.Size(), limit)
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

func TestSubagentRunResult_OnlyActiveOrCompletedStatesSucceed(t *testing.T) {
	for _, tt := range []struct {
		state agentcoord.RunState
		want  bool
	}{
		{state: agentcoord.RunQueued, want: true},
		{state: agentcoord.RunRunning, want: true},
		{state: agentcoord.RunCompleted, want: true},
		{state: agentcoord.RunFailed, want: false},
		{state: agentcoord.RunBlocked, want: false},
		{state: agentcoord.RunCancelled, want: false},
		{state: agentcoord.RunResumable, want: false},
	} {
		result := subagentRunResult(agentcoord.Run{ID: "child", State: tt.state})
		if result.Success != tt.want {
			t.Fatalf("state %q success = %v, want %v", tt.state, result.Success, tt.want)
		}
	}
}

func TestSubagentTool_SpawnPropagatesExplicitEnvelopeWithoutDefaults(t *testing.T) {
	requests := make(chan subagent.Request, 2)
	manager := subagent.NewManager(builtinSubagentRunnerFunc(func(_ context.Context, request subagent.Request, _ func(int)) (string, error) {
		requests <- request
		return "done", nil
	}), 2)
	t.Cleanup(func() { _ = manager.Close() })
	tool := &SubagentTool{manager: manager}
	tool.SetTelemetry(nil, "session-parent")
	tool.SetExecutionContext("run-parent", "task-parent")
	tool.SetCoordinator(subagent.NewCoordinator(manager))

	result, err := tool.ExecuteUserCommand(context.Background(), map[string]any{
		"action":              "spawn",
		"initial_task":        "inspect",
		"step_cap":            12,
		"timeout_seconds":     90,
		"max_tool_calls":      21,
		"max_model_requests":  14,
		"max_elapsed_seconds": 75,
		"max_cost_usd":        1.5,
	})
	if err != nil || !result.Success {
		t.Fatalf("spawn result=%+v err=%v", result, err)
	}
	request := <-requests
	if request.ParentSessionID != "session-parent" || request.ParentRunID != "run-parent" || request.StepCap != 12 || request.TimeoutSeconds != 90 {
		t.Fatalf("request lineage/limits = %+v", request)
	}
	if request.Budget.MaxToolCalls != 21 || request.Budget.MaxModelRequests != 14 || request.Budget.MaxElapsedSecond != 75 || request.Budget.MaxCostUSD != 1.5 {
		t.Fatalf("request budget = %+v", request)
	}

	result, err = tool.ExecuteUserCommand(context.Background(), map[string]any{"action": "spawn", "initial_task": "unbounded"})
	if err != nil || !result.Success {
		t.Fatalf("unbounded spawn result=%+v err=%v", result, err)
	}
	request = <-requests
	if request.TimeoutSeconds != 0 || request.StepCap != 0 || request.Budget != (agentcoord.Budget{}) {
		t.Fatalf("unbounded spawn gained defaults: %+v", request)
	}
}

func TestSubagentTool_SpawnRejectsInvalidCostBeforeCoordinator(t *testing.T) {
	tool := &SubagentTool{}
	for _, value := range []any{math.NaN(), math.Inf(1), math.Inf(-1), "NaN", "+Inf", -0.01, "bogus"} {
		result, err := tool.ExecuteUserCommand(context.Background(), map[string]any{
			"action":       "spawn",
			"initial_task": "inspect",
			"max_cost_usd": value,
		})
		if err != nil {
			t.Fatalf("ExecuteUserCommand(%v): %v", value, err)
		}
		if result.Success || !strings.Contains(result.Error, "max_cost_usd") {
			t.Fatalf("ExecuteUserCommand(%v) = %+v", value, result)
		}
	}
}

func TestSubagentTool_DirectActionsCannotCrossSession(t *testing.T) {
	manager := subagent.NewManager(builtinSubagentRunnerFunc(func(ctx context.Context, _ subagent.Request, started func(int)) (string, error) {
		started(201)
		<-ctx.Done()
		return "", ctx.Err()
	}), 2)
	t.Cleanup(func() { _ = manager.Close() })
	tool := &SubagentTool{manager: manager}
	tool.SetTelemetry(nil, "session-local")
	coordinator := tool.getCoordinator()

	foreign, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{ParentSessionID: "session-foreign", Task: "foreign work"})
	if err != nil {
		t.Fatalf("spawn foreign run: %v", err)
	}
	if _, err := coordinator.Claim(context.Background(), agentcoord.ClaimRequest{RunID: foreign.ID, Resources: []string{"foreign/held"}}); err != nil {
		t.Fatalf("seed foreign claim: %v", err)
	}

	for _, tt := range []struct {
		name   string
		params map[string]any
	}{
		{name: "status", params: map[string]any{"action": "status", "id": foreign.ID}},
		{name: "wait", params: map[string]any{"action": "wait", "id": foreign.ID, "timeout_seconds": 1}},
		{name: "messages", params: map[string]any{"action": "messages", "id": foreign.ID}},
		{name: "send", params: map[string]any{"action": "send", "id": foreign.ID, "message": "cross-session command"}},
		{name: "steer", params: map[string]any{"action": "steer", "id": foreign.ID, "message": "cross-session priority"}},
		{name: "cancel", params: map[string]any{"action": "cancel", "id": foreign.ID, "reason": "cross-session cancel"}},
		{name: "claim", params: map[string]any{"action": "claim", "id": foreign.ID, "resources": []string{"foreign/new"}}},
		{name: "release", params: map[string]any{"action": "release", "id": foreign.ID, "resources": []string{"foreign/held"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(tt.params)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			wantError := "subagent not found: " + foreign.ID
			if result.Success || result.Error != wantError {
				t.Fatalf("result = %+v, want access denial %q", result, wantError)
			}
		})
	}

	foreignStatus, err := coordinator.Status(context.Background(), foreign.ID)
	if err != nil {
		t.Fatalf("foreign status: %v", err)
	}
	if foreignStatus.State.Terminal() {
		t.Fatalf("foreign run was mutated to terminal state: %+v", foreignStatus)
	}
	if got := strings.Join(foreignStatus.Claims, ","); got != "foreign/held" {
		t.Fatalf("foreign claims = %q, want only seeded claim", got)
	}
	foreignMessages, err := coordinator.Messages(context.Background(), foreign.ID)
	if err != nil {
		t.Fatalf("foreign messages: %v", err)
	}
	if len(foreignMessages) != 0 {
		t.Fatalf("foreign mailbox received cross-session messages: %+v", foreignMessages)
	}
}

func TestSubagentTool_CommaTargetsArePreflightedBeforeControl(t *testing.T) {
	for _, action := range []string{"send", "steer", "cancel"} {
		t.Run(action, func(t *testing.T) {
			manager := subagent.NewManager(builtinSubagentRunnerFunc(func(ctx context.Context, _ subagent.Request, started func(int)) (string, error) {
				started(202)
				<-ctx.Done()
				return "", ctx.Err()
			}), 2)
			t.Cleanup(func() { _ = manager.Close() })
			tool := &SubagentTool{manager: manager}
			tool.SetTelemetry(nil, "session-local")
			coordinator := tool.getCoordinator()
			local, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{ParentSessionID: "session-local", Task: "local work"})
			if err != nil {
				t.Fatalf("spawn local run: %v", err)
			}
			foreign, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{ParentSessionID: "session-foreign", Task: "foreign work"})
			if err != nil {
				t.Fatalf("spawn foreign run: %v", err)
			}
			params := map[string]any{
				"action":  action,
				"id":      local.ID + "," + foreign.ID,
				"message": "must not be partially delivered",
				"reason":  "must not be partially cancelled",
			}
			result, err := tool.Execute(params)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			wantError := "subagent not found: " + foreign.ID
			if result.Success || result.Error != wantError {
				t.Fatalf("result = %+v, want access denial %q", result, wantError)
			}
			localStatus, err := coordinator.Status(context.Background(), local.ID)
			if err != nil {
				t.Fatalf("local status: %v", err)
			}
			if localStatus.State.Terminal() {
				t.Fatalf("local run was partially cancelled: %+v", localStatus)
			}
			localMessages, err := coordinator.Messages(context.Background(), local.ID)
			if err != nil {
				t.Fatalf("local messages: %v", err)
			}
			if len(localMessages) != 0 {
				t.Fatalf("local mailbox was partially mutated: %+v", localMessages)
			}
		})
	}
}

func TestSubagentTool_EmptySessionPreservesDirectIDCompatibility(t *testing.T) {
	manager := subagent.NewManager(builtinSubagentRunnerFunc(func(_ context.Context, _ subagent.Request, started func(int)) (string, error) {
		started(203)
		return "done", nil
	}), 2)
	t.Cleanup(func() { _ = manager.Close() })
	tool := &SubagentTool{manager: manager}
	coordinator := tool.getCoordinator()
	first, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{ParentSessionID: "external-session", Task: "first"})
	if err != nil {
		t.Fatalf("spawn first run: %v", err)
	}
	second, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{ParentSessionID: "other-external-session", Task: "second"})
	if err != nil {
		t.Fatalf("spawn second run: %v", err)
	}

	status, err := tool.Execute(map[string]any{"action": "status", "id": first.ID})
	if err != nil || !status.Success {
		t.Fatalf("legacy status result=%+v err=%v", status, err)
	}
	sent, err := tool.Execute(map[string]any{"action": "send", "id": first.ID + "," + second.ID, "message": "legacy broadcast"})
	if err != nil || !sent.Success || sent.Data["succeeded"].(int) != 2 {
		t.Fatalf("legacy comma send result=%+v err=%v", sent, err)
	}
	claimed, err := tool.Execute(map[string]any{"action": "claim", "id": first.ID, "resources": []string{"legacy/path"}})
	if err != nil || !claimed.Success {
		t.Fatalf("legacy claim result=%+v err=%v", claimed, err)
	}
	released, err := tool.Execute(map[string]any{"action": "release", "id": first.ID, "resources": []string{"legacy/path"}})
	if err != nil || !released.Success {
		t.Fatalf("legacy release result=%+v err=%v", released, err)
	}
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
	guard := installDelegationGuardForTest(t)
	for _, task := range []string{"first explicit task", "second explicit task"} {
		result, err := tool.ExecuteUserCommand(context.Background(), map[string]any{"action": "spawn", "initial_task": task})
		if err != nil || !result.Success {
			t.Fatalf("explicit spawn %q: result=%+v err=%v", task, result, err)
		}
	}
	guard.mu.Lock()
	delegations := len(guard.delegationTimes)
	_, recordedTool := guard.lastDelegation["spawn_subagent"]
	guard.mu.Unlock()
	if delegations != 2 || !recordedTool {
		t.Fatalf("trusted spawns were not recorded: count=%d tool=%v", delegations, recordedTool)
	}

	result, err := tool.Execute(map[string]any{"action": "spawn", "initial_task": "model cooldown check"})
	if err != nil {
		t.Fatalf("model spawn: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "cooldown active") {
		t.Fatalf("unmarked spawn bypassed cooldown: %+v", result)
	}
}

func TestSubagentTool_UserSpawnStillEnforcesDelegationDepth(t *testing.T) {
	t.Setenv(DelegationDepthEnvVar, "3")
	guard := installDelegationGuardForTest(t)
	manager := subagent.NewManager(builtinSubagentRunnerFunc(func(context.Context, subagent.Request, func(int)) (string, error) {
		t.Fatal("depth-rejected invocation reached child runner")
		return "", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	tool := &SubagentTool{manager: manager}

	result, err := tool.ExecuteUserCommand(context.Background(), map[string]any{
		"action": "spawn", "initial_task": "must respect depth",
	})
	if err != nil {
		t.Fatalf("ExecuteUserCommand: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "depth limit exceeded") {
		t.Fatalf("depth-limited trusted spawn = %+v", result)
	}
	guard.mu.Lock()
	recorded := len(guard.delegationTimes)
	guard.mu.Unlock()
	if recorded != 0 {
		t.Fatalf("depth-rejected spawn recorded %d uses", recorded)
	}
}

func TestSubagentTool_UserSpawnStillEnforcesRollingRate(t *testing.T) {
	t.Setenv(DelegationDepthEnvVar, "0")
	guard := installDelegationGuardForTest(t)
	now := time.Now()
	guard.delegationTimes = make([]time.Time, MaxDelegationsPerWindow)
	for i := range guard.delegationTimes {
		guard.delegationTimes[i] = now
	}
	manager := subagent.NewManager(builtinSubagentRunnerFunc(func(context.Context, subagent.Request, func(int)) (string, error) {
		t.Fatal("rate-rejected invocation reached child runner")
		return "", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	tool := &SubagentTool{manager: manager}

	result, err := tool.ExecuteUserCommand(context.Background(), map[string]any{
		"action": "spawn", "initial_task": "must respect rolling rate",
	})
	if err != nil {
		t.Fatalf("ExecuteUserCommand: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "rate limit exceeded") {
		t.Fatalf("rate-limited trusted spawn = %+v", result)
	}
	guard.mu.Lock()
	recorded := len(guard.delegationTimes)
	guard.mu.Unlock()
	if recorded != MaxDelegationsPerWindow {
		t.Fatalf("rate-rejected spawn changed usage count to %d", recorded)
	}
}
