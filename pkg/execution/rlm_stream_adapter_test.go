package execution

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/rlm"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/toast"
)

type stubStreamHandler struct {
	starts    int
	ends      int
	completes int
	lastTool  string
}

func (s *stubStreamHandler) OnText(string) {}

func (s *stubStreamHandler) OnReasoning(string) {}

func (s *stubStreamHandler) OnReasoningEnd() {}

func (s *stubStreamHandler) OnToolStart(name string, _ string) {
	s.starts++
	s.lastTool = name
}

func (s *stubStreamHandler) OnToolEnd(name string, _ string, _ error) {
	s.ends++
	s.lastTool = name
}

func (s *stubStreamHandler) OnError(error) {}

func (s *stubStreamHandler) OnComplete(_ *ExecutionResult) {
	s.completes++
}

func TestRLMStreamAdapter_ProgressLifecycle(t *testing.T) {
	manager := progress.NewProgressManager()
	var snapshots [][]progress.Progress
	manager.SetOnChange(func(items []progress.Progress) {
		copied := make([]progress.Progress, len(items))
		copy(copied, items)
		snapshots = append(snapshots, copied)
	})

	adapter := NewRLMStreamAdapter(nil, manager, nil)
	adapter.OnRLMEvent(rlm.IterationEvent{Iteration: 1, MaxIterations: 3})

	if len(snapshots) == 0 {
		t.Fatalf("expected progress snapshots")
	}
	latest := snapshots[len(snapshots)-1]
	if len(latest) != 1 {
		t.Fatalf("expected one progress entry, got %d", len(latest))
	}
	entry := latest[0]
	if entry.ID != rlmProgressID {
		t.Fatalf("expected progress ID %q, got %q", rlmProgressID, entry.ID)
	}
	if entry.Total != 3 || entry.Current != 1 {
		t.Fatalf("expected progress 1/3, got %d/%d", entry.Current, entry.Total)
	}
	if entry.Type != progress.ProgressSteps {
		t.Fatalf("expected steps progress type, got %q", entry.Type)
	}

	adapter.OnRLMEvent(rlm.IterationEvent{Iteration: 3, MaxIterations: 3, Ready: true})
	latest = snapshots[len(snapshots)-1]
	if len(latest) != 0 {
		t.Fatalf("expected progress to be cleared, got %d entries", len(latest))
	}
}

func TestRLMStreamAdapter_BudgetWarnings(t *testing.T) {
	manager := toast.NewToastManager()
	var snapshots [][]*toast.Toast
	manager.SetOnChange(func(items []*toast.Toast) {
		copied := make([]*toast.Toast, len(items))
		copy(copied, items)
		snapshots = append(snapshots, copied)
	})

	adapter := NewRLMStreamAdapter(nil, nil, manager)
	adapter.OnRLMEvent(rlm.IterationEvent{
		BudgetStatus: rlm.BudgetStatus{Warning: "low", TokensPercent: 80},
	})
	if len(snapshots) < 2 {
		t.Fatalf("expected toast snapshot after warning")
	}
	firstCount := len(snapshots)

	adapter.OnRLMEvent(rlm.IterationEvent{
		BudgetStatus: rlm.BudgetStatus{Warning: "low", TokensPercent: 85},
	})
	if len(snapshots) != firstCount {
		t.Fatalf("expected no new toast for repeated warning")
	}

	adapter.OnRLMEvent(rlm.IterationEvent{
		BudgetStatus: rlm.BudgetStatus{Warning: "critical", TokensPercent: 95},
	})
	if len(snapshots) <= firstCount {
		t.Fatalf("expected new toast for critical warning")
	}
}

func TestRLMStreamAdapter_ForwardsStreamEvents(t *testing.T) {
	handler := &stubStreamHandler{}
	adapter := NewRLMStreamAdapter(handler, nil, nil)
	adapter.OnRLMEvent(rlm.IterationEvent{Iteration: 2, MaxIterations: 4})
	adapter.OnComplete(&ExecutionResult{})

	if handler.starts == 0 || handler.ends == 0 {
		t.Fatalf("expected stream handler start/end callbacks")
	}
	if handler.lastTool != "rlm_iteration" {
		t.Fatalf("expected rlm_iteration tool name, got %q", handler.lastTool)
	}
	if handler.completes != 1 {
		t.Fatalf("expected completion callback once, got %d", handler.completes)
	}
}
