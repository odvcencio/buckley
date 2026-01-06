package tool

import (
	"strings"
	"testing"
	"time"
)

func TestNewIntentBuilder(t *testing.T) {
	builder := NewIntentBuilder("planning")

	if builder == nil {
		t.Fatal("expected non-nil builder")
	}
	if builder.phase != "planning" {
		t.Errorf("expected phase 'planning', got %s", builder.phase)
	}
}

func TestIntentBuilder_Activity(t *testing.T) {
	builder := NewIntentBuilder("execution").
		Activity("Testing feature X")

	if builder.activity != "Testing feature X" {
		t.Errorf("expected activity 'Testing feature X', got %s", builder.activity)
	}
}

func TestIntentBuilder_AddTool(t *testing.T) {
	builder := NewIntentBuilder("planning").
		AddTool("read").
		AddTool("write")

	if len(builder.tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(builder.tools))
	}
	if builder.tools[0] != "read" {
		t.Errorf("expected first tool 'read', got %s", builder.tools[0])
	}
}

func TestIntentBuilder_AddParam(t *testing.T) {
	builder := NewIntentBuilder("planning").
		AddParam("file", "test.go").
		AddParam("count", 5)

	if len(builder.params) != 2 {
		t.Errorf("expected 2 params, got %d", len(builder.params))
	}
	if builder.params["file"] != "test.go" {
		t.Errorf("expected param 'file' to be 'test.go', got %v", builder.params["file"])
	}
}

func TestIntentBuilder_Build(t *testing.T) {
	intent := NewIntentBuilder("execution").
		Activity("Processing files").
		AddTool("read").
		Build()

	if intent.Phase != "execution" {
		t.Errorf("expected phase 'execution', got %s", intent.Phase)
	}
	if intent.Activity != "Processing files" {
		t.Errorf("expected activity 'Processing files', got %s", intent.Activity)
	}
	if len(intent.Tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(intent.Tools))
	}
	if intent.Timestamp.IsZero() {
		t.Error("expected timestamp to be set")
	}
}

func TestIntentBuilder_Build_WithParamSubstitution(t *testing.T) {
	intent := NewIntentBuilder("planning").
		Activity("Reading {file} for {reason}").
		AddParam("file", "test.go").
		AddParam("reason", "analysis").
		Build()

	expected := "Reading test.go for analysis"
	if intent.Activity != expected {
		t.Errorf("expected activity %q, got %q", expected, intent.Activity)
	}
}

func TestIntentBuilder_EstimateTime(t *testing.T) {
	tests := []struct {
		name      string
		toolCount int
		want      string
	}{
		{"no tools", 0, "~5 seconds"},
		{"one tool", 1, "~10 seconds"},
		{"two tools", 2, "~30 seconds"},
		{"three tools", 3, "~30 seconds"},
		{"four tools", 4, "~1 minute"},
		{"five tools", 5, "~1 minute"},
		{"many tools", 10, "~2 minutes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewIntentBuilder("planning")
			for i := 0; i < tt.toolCount; i++ {
				builder.AddTool("tool")
			}
			intent := builder.Build()

			if intent.ExpectedTime != tt.want {
				t.Errorf("expected time %q for %d tools, got %q", tt.want, tt.toolCount, intent.ExpectedTime)
			}
		})
	}
}

func TestIntentFormatter_Format(t *testing.T) {
	formatter := NewIntentFormatter()
	intent := Intent{
		Activity: "Testing feature",
	}

	result := formatter.Format(intent)

	if !strings.Contains(result, "[Intent]") {
		t.Error("expected result to contain '[Intent]'")
	}
	if !strings.Contains(result, "Testing feature") {
		t.Error("expected result to contain activity")
	}
}

func TestIntentFormatter_FormatWithDetails(t *testing.T) {
	formatter := NewIntentFormatter()
	intent := Intent{
		Activity:     "Testing feature",
		Tools:        []string{"read", "write"},
		ExpectedTime: "~30 seconds",
	}

	result := formatter.FormatWithDetails(intent)

	if !strings.Contains(result, "[Intent]") {
		t.Error("expected result to contain '[Intent]'")
	}
	if !strings.Contains(result, "Testing feature") {
		t.Error("expected result to contain activity")
	}
	if !strings.Contains(result, "~30 seconds") {
		t.Error("expected result to contain expected time")
	}
	if !strings.Contains(result, "read, write") {
		t.Error("expected result to contain tool list")
	}
}

func TestIntentFormatter_FormatCompact(t *testing.T) {
	formatter := NewIntentFormatter()

	tests := []struct {
		name     string
		intent   Intent
		contains []string
	}{
		{
			name:     "no tools",
			intent:   Intent{Activity: "Analyzing"},
			contains: []string{"[Intent]", "Analyzing"},
		},
		{
			name:     "one tool",
			intent:   Intent{Activity: "Reading", Tools: []string{"read"}},
			contains: []string{"[Intent]", "Reading", "using read"},
		},
		{
			name:     "multiple tools",
			intent:   Intent{Activity: "Processing", Tools: []string{"read", "write", "edit"}},
			contains: []string{"[Intent]", "Processing", "using 3 tools"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatter.FormatCompact(tt.intent)
			for _, substr := range tt.contains {
				if !strings.Contains(result, substr) {
					t.Errorf("expected result to contain %q, got %q", substr, result)
				}
			}
		})
	}
}

func TestIntentLibrary_AnalyzingCodebase(t *testing.T) {
	lib := NewIntentLibrary()
	intent := lib.AnalyzingCodebase("planning", "authentication module")

	if intent.Phase != "planning" {
		t.Errorf("expected phase 'planning', got %s", intent.Phase)
	}
	if !strings.Contains(intent.Activity, "authentication module") {
		t.Error("expected activity to mention target")
	}
	if len(intent.Tools) == 0 {
		t.Error("expected tools to be added")
	}
}

func TestIntentLibrary_ImplementingTask(t *testing.T) {
	lib := NewIntentLibrary()
	intent := lib.ImplementingTask(42, "Add user validation")

	if intent.Phase != "execution" {
		t.Errorf("expected phase 'execution', got %s", intent.Phase)
	}
	if !strings.Contains(intent.Activity, "Task 42") {
		t.Error("expected activity to mention task ID")
	}
	if !strings.Contains(intent.Activity, "Add user validation") {
		t.Error("expected activity to mention description")
	}
}

func TestIntentLibrary_RunningTests(t *testing.T) {
	lib := NewIntentLibrary()
	intent := lib.RunningTests("./pkg/auth")

	if intent.Phase != "execution" {
		t.Errorf("expected phase 'execution', got %s", intent.Phase)
	}
	if !strings.Contains(intent.Activity, "./pkg/auth") {
		t.Error("expected activity to mention test path")
	}
}

func TestIntentLibrary_ValidatingImplementation(t *testing.T) {
	lib := NewIntentLibrary()
	intent := lib.ValidatingImplementation("error handling")

	if intent.Phase != "review" {
		t.Errorf("expected phase 'review', got %s", intent.Phase)
	}
	if !strings.Contains(intent.Activity, "error handling") {
		t.Error("expected activity to mention aspect")
	}
}

func TestIntentLibrary_GeneratingArtifact(t *testing.T) {
	lib := NewIntentLibrary()
	intent := lib.GeneratingArtifact("planning")

	if intent.Phase != "planning" {
		t.Errorf("expected phase 'planning', got %s", intent.Phase)
	}
	if !strings.Contains(intent.Activity, "planning artifact") {
		t.Error("expected activity to mention artifact type")
	}
}

func TestIntentLibrary_SearchingForPattern(t *testing.T) {
	lib := NewIntentLibrary()
	intent := lib.SearchingForPattern("TODO", "codebase")

	if !strings.Contains(intent.Activity, "TODO") {
		t.Error("expected activity to mention pattern")
	}
	if !strings.Contains(intent.Activity, "codebase") {
		t.Error("expected activity to mention scope")
	}
}

func TestIntentLibrary_RefactoringCode(t *testing.T) {
	lib := NewIntentLibrary()
	intent := lib.RefactoringCode("extract helper function")

	if intent.Phase != "execution" {
		t.Errorf("expected phase 'execution', got %s", intent.Phase)
	}
	if !strings.Contains(intent.Activity, "extract helper function") {
		t.Error("expected activity to mention description")
	}
}

func TestIntentLibrary_FixingIssue(t *testing.T) {
	lib := NewIntentLibrary()
	intent := lib.FixingIssue(123, "Null pointer bug")

	if intent.Phase != "execution" {
		t.Errorf("expected phase 'execution', got %s", intent.Phase)
	}
	if !strings.Contains(intent.Activity, "#123") {
		t.Error("expected activity to mention issue ID")
	}
	if !strings.Contains(intent.Activity, "Null pointer bug") {
		t.Error("expected activity to mention title")
	}
}

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

func TestIntent_Timestamp(t *testing.T) {
	before := time.Now()
	intent := NewIntentBuilder("planning").
		Activity("Test").
		Build()
	after := time.Now()

	if intent.Timestamp.Before(before) || intent.Timestamp.After(after) {
		t.Error("expected timestamp to be set to current time")
	}
}
