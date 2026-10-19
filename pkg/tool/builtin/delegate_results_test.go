package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/subagent"
)

func TestSubagentTool_ListPagesPreserveHostContractsAndSessionScope(t *testing.T) {
	manager := subagent.NewManager(builtinSubagentRunnerFunc(func(_ context.Context, req subagent.Request, _ func(int)) (string, error) {
		return "finding for " + req.ID, nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	c := subagent.NewCoordinator(manager)
	worker := &SubagentTool{manager: manager}
	worker.SetCoordinator(c)
	worker.SetTelemetry(nil, "owner")
	ctx := context.Background()
	task := strings.TrimSpace(strings.Repeat("full task contract ", 1000))
	const instructions = "host-only child instructions"
	spawn := func(id, session string) {
		t.Helper()
		if _, err := c.Spawn(ctx, agentcoord.TaskSpec{RunID: id, ParentSessionID: session, Task: task, SystemPrompt: instructions}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Wait(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 13; i++ {
		spawn(fmt.Sprintf("worker-%02d", i), "owner")
	}
	spawn("foreign", "other")
	page, err := worker.ExecuteWithContext(ctx, map[string]any{"action": "list"})
	if err != nil || !page.Success || !page.ShouldAbridge || page.Data["count"] != DefaultSubagentListLimit || page.Data["has_more"] != true {
		t.Fatalf("first page count=%v more=%v err=%v", page.Data["count"], page.Data["has_more"], err)
	}
	runs := page.Data["runs"].([]agentcoord.Run)
	var ids []string
	for _, run := range runs {
		ids = append(ids, run.ID)
		if run.Task.Task != task || run.Task.SystemPrompt != instructions || run.ParentSessionID != "owner" {
			t.Fatalf("host contract or session changed for %s", run.ID)
		}
	}
	display, err := json.Marshal(page.DisplayData)
	if err != nil || len(display) > subagentListPageBytes || strings.Contains(string(display), instructions) || strings.Contains(string(display), "foreign") {
		t.Fatalf("list display bytes=%d err=%v", len(display), err)
	}
	spawn("inserted-later", "owner")
	next, err := worker.ExecuteWithContext(ctx, map[string]any{"action": "list", "cursor": page.Data["next_cursor"]})
	if err != nil || !next.Success || next.Data["has_more"] != false || next.Data["next_cursor"] != nil {
		t.Fatalf("last page=%+v err=%v", next.DisplayData, err)
	}
	for _, run := range next.Data["runs"].([]agentcoord.Run) {
		ids = append(ids, run.ID)
	}
	want := []string{"worker-12", "worker-11", "worker-10", "worker-09", "worker-08", "worker-07", "worker-06", "worker-05", "worker-04", "worker-03", "worker-02", "worker-01", "worker-00"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("paged IDs=%v want=%v", ids, want)
	}
}

func TestSubagentTool_ListByteBudgetKeepsContinuation(t *testing.T) {
	manager := subagent.NewManager(builtinSubagentRunnerFunc(func(context.Context, subagent.Request, func(int)) (string, error) {
		return "failed child", errors.New(strings.Repeat("\x00", 2000))
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	c := subagent.NewCoordinator(manager)
	worker := &SubagentTool{manager: manager}
	worker.SetCoordinator(c)
	worker.SetTelemetry(nil, "owner")
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		run, err := c.Spawn(ctx, agentcoord.TaskSpec{RunID: fmt.Sprint(i), ParentSessionID: "owner", Task: "inspect"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Wait(ctx, run.ID); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]bool)
	params := map[string]any{"action": "list", "limit": MaxSubagentListLimit}
	for page := 0; page < 6; page++ {
		result, err := worker.ExecuteWithContext(ctx, params)
		if err != nil || !result.Success {
			t.Fatalf("list: %v %+v", err, result)
		}
		encoded, err := json.Marshal(result.DisplayData)
		if err != nil || len(encoded) > subagentListPageBytes {
			t.Fatalf("page bytes=%d err=%v", len(encoded), err)
		}
		for _, run := range result.Data["runs"].([]agentcoord.Run) {
			if seen[run.ID] {
				t.Fatalf("repeated %s", run.ID)
			}
			seen[run.ID] = true
		}
		if result.Data["has_more"] == false {
			break
		}
		params["cursor"] = result.Data["next_cursor"]
	}
	if len(seen) != 5 {
		t.Fatalf("byte pagination lost runs: %v", seen)
	}
}

func TestSubagentTool_ListRejectsInvalidPagination(t *testing.T) {
	manager := subagent.NewManager(builtinSubagentRunnerFunc(func(context.Context, subagent.Request, func(int)) (string, error) { return "", nil }), 1)
	t.Cleanup(func() { _ = manager.Close() })
	worker := &SubagentTool{manager: manager}
	worker.SetCoordinator(subagent.NewCoordinator(manager))
	for _, value := range []any{0, -1, MaxSubagentListLimit + 1, 1.5, "bad", true} {
		result, err := worker.Execute(map[string]any{"action": "list", "limit": value})
		if err != nil || result.Success || !strings.Contains(result.Error, "list limit") {
			t.Fatalf("limit %v: %+v err=%v", value, result, err)
		}
	}
	for _, value := range []any{true, "%invalid", strings.Repeat("a", 4097), base64.RawURLEncoding.EncodeToString([]byte(`{"run_id":"missing-time"}`))} {
		result, err := worker.Execute(map[string]any{"action": "list", "cursor": value})
		if err != nil || result.Success || !strings.Contains(result.Error, "list cursor") {
			t.Fatalf("cursor %T: %+v err=%v", value, result, err)
		}
	}
}

func TestSubagentRunResult_UsefulBoundedViewPreservesFullHostData(t *testing.T) {
	run := agentcoord.Run{ID: "worker", State: agentcoord.RunCompleted, StartedAt: time.Now(),
		Task:   agentcoord.TaskSpec{Task: strings.Repeat("📚", 1000), SystemPrompt: "private repeated instructions"},
		Result: agentcoord.Result{Summary: "Verified boundary: " + strings.Repeat("📚", 2000), EvidenceRefs: []string{"task", "mail", "raw-report", "typed-report"}}}
	result := subagentRunResult(run)
	view := result.DisplayData["run"].(map[string]any)
	if !strings.HasPrefix(view["summary"].(string), "Verified boundary:") || len(view["summary"].(string)) > 4096 || !utf8.ValidString(view["summary"].(string)) || !utf8.ValidString(view["task_preview"].(string)) {
		t.Fatalf("findings or UTF-8 lost in projection: %+v", view)
	}
	if refs := view["evidence_refs"].([]string); !reflect.DeepEqual(refs, []string{"raw-report", "typed-report"}) {
		t.Fatalf("terminal reports missing: %v", refs)
	}
	if got := result.Data["run"].(agentcoord.Run); !reflect.DeepEqual(got, run) {
		t.Fatal("bounded model view changed full host data")
	}
	view["evidence_refs"].([]string)[0] = "changed"
	if run.Result.EvidenceRefs[2] != "raw-report" {
		t.Fatal("model view aliases host evidence references")
	}
}
