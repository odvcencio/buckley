package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/tool"
)

func TestRunnerInitRuntimeWiresSubAgentTimeout(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		_, _ = io.ReadAll(req.Body)
		<-req.Context().Done()
	}))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.RLM.SubAgent.Timeout = 150 * time.Millisecond
	store := &recordingPlanStore{}
	r := New(nil, newRunnerTaskManager(t, server), tool.NewEmptyRegistry(), cfg, nil, store)
	installRunnerTestRuntime(t, r, cfg)
	plan := &orchestrator.Plan{ID: "plan-timeout", Tasks: []orchestrator.Task{{
		ID:          "task-timeout",
		Title:       "Timeout",
		Description: "block until configured timeout",
		Type:        orchestrator.TaskTypeAnalysis,
		Status:      orchestrator.TaskPending,
	}}}

	err := r.executeTaskBatch(plan, plan.Tasks)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "sub-agent task timeout") {
		t.Fatalf("executeTaskBatch error = %v, want configured sub-agent task timeout", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("model calls = %d, want one configured-timeout execution", calls.Load())
	}
	if plan.Tasks[0].Status != orchestrator.TaskFailed {
		t.Fatalf("task status = %v, want failed", plan.Tasks[0].Status)
	}
	if len(plan.Tasks[0].ExecutionRecords) != 1 || !strings.Contains(plan.Tasks[0].ExecutionRecords[0].Error, "sub-agent task timeout") {
		t.Fatalf("execution records = %+v, want timeout evidence retained", plan.Tasks[0].ExecutionRecords)
	}
	if store.saves == 0 {
		t.Fatal("plan was not saved after timeout result")
	}
}
