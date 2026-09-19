package tool

import (
	"testing"
)

func TestIntentHistory_Add(t *testing.T) {
	history := NewIntentHistory()
	intent := Intent{Activity: "Test"}

	history.Add(intent)

	if history.Count() != 1 {
		t.Errorf("expected count 1, got %d", history.Count())
	}
}

func TestIntentHistory_GetAll(t *testing.T) {
	history := NewIntentHistory()
	intent1 := Intent{Activity: "First"}
	intent2 := Intent{Activity: "Second"}

	history.Add(intent1)
	history.Add(intent2)

	all := history.GetAll()
	if len(all) != 2 {
		t.Errorf("expected 2 intents, got %d", len(all))
	}
}

func TestIntentHistory_GetByPhase(t *testing.T) {
	history := NewIntentHistory()
	history.Add(Intent{Phase: "planning", Activity: "Plan 1"})
	history.Add(Intent{Phase: "execution", Activity: "Execute 1"})
	history.Add(Intent{Phase: "planning", Activity: "Plan 2"})

	planningIntents := history.GetByPhase("planning")
	if len(planningIntents) != 2 {
		t.Errorf("expected 2 planning intents, got %d", len(planningIntents))
	}

	executionIntents := history.GetByPhase("execution")
	if len(executionIntents) != 1 {
		t.Errorf("expected 1 execution intent, got %d", len(executionIntents))
	}
}

func TestIntentHistory_GetRecent(t *testing.T) {
	history := NewIntentHistory()
	for i := 0; i < 5; i++ {
		history.Add(Intent{Activity: "Test"})
	}

	recent := history.GetRecent(3)
	if len(recent) != 3 {
		t.Errorf("expected 3 recent intents, got %d", len(recent))
	}

	// Request more than available
	allRecent := history.GetRecent(10)
	if len(allRecent) != 5 {
		t.Errorf("expected 5 intents when requesting more than available, got %d", len(allRecent))
	}
}

func TestIntentHistory_GetLatest(t *testing.T) {
	history := NewIntentHistory()

	// Empty history
	latest := history.GetLatest()
	if latest != nil {
		t.Error("expected nil for empty history")
	}

	// Add intent
	history.Add(Intent{Activity: "First"})
	history.Add(Intent{Activity: "Second"})

	latest = history.GetLatest()
	if latest == nil {
		t.Fatal("expected non-nil latest intent")
	}
	if latest.Activity != "Second" {
		t.Errorf("expected latest activity 'Second', got %s", latest.Activity)
	}
}

func TestIntentHistory_Clear(t *testing.T) {
	history := NewIntentHistory()
	history.Add(Intent{Activity: "Test"})
	history.Add(Intent{Activity: "Test2"})

	if history.Count() != 2 {
		t.Errorf("expected count 2 before clear, got %d", history.Count())
	}

	history.Clear()

	if history.Count() != 0 {
		t.Errorf("expected count 0 after clear, got %d", history.Count())
	}
}

func TestIntentHistory_Count(t *testing.T) {
	history := NewIntentHistory()

	if history.Count() != 0 {
		t.Errorf("expected count 0 initially, got %d", history.Count())
	}

	history.Add(Intent{Activity: "Test"})

	if history.Count() != 1 {
		t.Errorf("expected count 1 after add, got %d", history.Count())
	}
}
