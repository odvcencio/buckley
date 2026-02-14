package execution

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/rlm"
)

type stubStreamHandler struct {
	starts    int
	ends      int
	completes int
	lastTool  string
}

type progressSpy struct {
	starts  int
	id      string
	title   string
	mode    ProgressMode
	total   int
	updates []int
	dones   int
}

func (s *progressSpy) Start(id, title string, mode ProgressMode, total int) {
	s.starts++
	s.id = id
	s.title = title
	s.mode = mode
	s.total = total
}

func (s *progressSpy) Update(id string, current int) {
	s.id = id
	s.updates = append(s.updates, current)
}

func (s *progressSpy) Done(id string) {
	s.id = id
	s.dones++
}

type toastSpy struct {
	warnings []string
	errors   []string
}

func (s *toastSpy) ShowWarning(title, message string) {
	s.warnings = append(s.warnings, title+": "+message)
}

func (s *toastSpy) ShowError(title, message string) {
	s.errors = append(s.errors, title+": "+message)
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
	reporter := &progressSpy{}
	adapter := NewRLMStreamAdapter(nil, reporter, nil)
	adapter.OnRLMEvent(rlm.IterationEvent{Iteration: 1, MaxIterations: 3})

	if reporter.starts != 1 {
		t.Fatalf("expected exactly one Start call, got %d", reporter.starts)
	}
	if reporter.id != rlmProgressID {
		t.Fatalf("expected progress ID %q, got %q", rlmProgressID, reporter.id)
	}
	if reporter.total != 3 {
		t.Fatalf("expected total 3, got %d", reporter.total)
	}
	if reporter.mode != ProgressModeSteps {
		t.Fatalf("expected steps progress mode, got %q", reporter.mode)
	}
	if len(reporter.updates) != 1 || reporter.updates[0] != 1 {
		t.Fatalf("expected update [1], got %v", reporter.updates)
	}

	adapter.OnRLMEvent(rlm.IterationEvent{Iteration: 3, MaxIterations: 3, Ready: true})
	if len(reporter.updates) != 2 || reporter.updates[1] != 3 {
		t.Fatalf("expected updates [1 3], got %v", reporter.updates)
	}
	if reporter.dones != 1 {
		t.Fatalf("expected exactly one Done call, got %d", reporter.dones)
	}
}

func TestRLMStreamAdapter_BudgetWarnings(t *testing.T) {
	notifier := &toastSpy{}
	adapter := NewRLMStreamAdapter(nil, nil, notifier)
	adapter.OnRLMEvent(rlm.IterationEvent{
		BudgetStatus: rlm.BudgetStatus{Warning: "low", TokensPercent: 80},
	})
	if len(notifier.warnings) != 1 {
		t.Fatalf("expected one warning toast, got %d", len(notifier.warnings))
	}
	firstCount := len(notifier.warnings)

	adapter.OnRLMEvent(rlm.IterationEvent{
		BudgetStatus: rlm.BudgetStatus{Warning: "low", TokensPercent: 85},
	})
	if len(notifier.warnings) != firstCount {
		t.Fatalf("expected no new warning for repeated low state")
	}

	adapter.OnRLMEvent(rlm.IterationEvent{
		BudgetStatus: rlm.BudgetStatus{Warning: "critical", TokensPercent: 95},
	})
	if len(notifier.errors) != 1 {
		t.Fatalf("expected one error toast for critical warning, got %d", len(notifier.errors))
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
