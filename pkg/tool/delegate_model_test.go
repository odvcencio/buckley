package tool

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/subagent"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type delegateModelRunner struct{}

func (delegateModelRunner) Run(context.Context, subagent.Request, func(int)) (string, error) {
	return "Verified finding: keep the parser boundary intact", nil
}

func TestSubagentModelOutput_FindingsAndPagesSurviveEncoding(t *testing.T) {
	for _, useToon := range []bool{false, true} {
		t.Run(fmt.Sprintf("toon_%t", useToon), func(t *testing.T) {
			SetResultEncoding(useToon)
			t.Cleanup(func() { SetResultEncoding(true) })
			manager := subagent.NewManager(delegateModelRunner{}, 1)
			t.Cleanup(func() { _ = manager.Close() })
			coordinator := subagent.NewCoordinator(manager)
			worker := &builtin.SubagentTool{}
			worker.SetCoordinator(coordinator)
			worker.SetTelemetry(nil, "owner")
			t.Cleanup(func() { _ = worker.Close() })
			registry := NewEmptyRegistry()
			registry.Register(worker)
			registry.Use(ResultSizeLimit(64*1024, "..."))
			ctx := context.Background()
			for i := 0; i < 13; i++ {
				id := fmt.Sprintf("worker-%02d", i)
				_, err := coordinator.Spawn(ctx, agentcoord.TaskSpec{RunID: id, ParentSessionID: "owner", Task: strings.TrimSpace(strings.Repeat("full contract ", 1000)), SystemPrompt: "hidden child instructions"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := coordinator.Wait(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			for _, action := range []string{"status", "wait"} {
				result, err := registry.ExecuteWithContext(ctx, worker.Name(), map[string]any{"action": action, "id": "worker-00"})
				if err != nil || !result.Success {
					t.Fatalf("%s: %+v %v", action, result, err)
				}
				wire, err := ToModelOutput(result)
				if err != nil || !strings.Contains(wire, "Verified finding:") || !strings.Contains(wire, "worker-00") || strings.Contains(wire, "hidden child instructions") {
					t.Fatalf("%s handoff lost: %s err=%v", action, wire, err)
				}
			}
			result, err := registry.ExecuteWithContext(ctx, worker.Name(), map[string]any{"action": "list"})
			if err != nil || !result.Success {
				t.Fatalf("list: %+v %v", result, err)
			}
			wire, err := ToModelOutput(result)
			if err != nil || len(wire) > DefaultModelOutputBytes || !strings.Contains(wire, "next_cursor") || !strings.Contains(wire, result.Data["next_cursor"].(string)) || strings.Contains(wire, "output_head") || strings.Contains(wire, "hidden child instructions") {
				t.Fatalf("page cannot be consumed: %s err=%v", wire, err)
			}
			for _, run := range result.Data["runs"].([]agentcoord.Run) {
				if !strings.Contains(wire, run.ID) {
					t.Fatalf("model page lost run %s", run.ID)
				}
			}
		})
	}
}
