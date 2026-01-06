package orchestrator

import (
	"strings"
	"testing"
	"time"
)

// BenchmarkPlanSerialization benchmarks plan JSON marshaling
func BenchmarkPlanSerialization(b *testing.B) {
	plans := []struct {
		name string
		plan *Plan
	}{
		{
			name: "small_plan",
			plan: &Plan{
				ID:          "bench-small",
				FeatureName: "Small Feature",
				Description: "A small test feature",
				CreatedAt:   time.Now(),
				Tasks: []Task{
					{ID: "1", Title: "Task 1", Description: "First task"},
					{ID: "2", Title: "Task 2", Description: "Second task"},
				},
			},
		},
		{
			name: "medium_plan",
			plan: &Plan{
				ID:          "bench-medium",
				FeatureName: "Medium Feature",
				Description: "A medium-sized test feature with more tasks",
				CreatedAt:   time.Now(),
				Tasks:       makeTasks(20),
			},
		},
		{
			name: "large_plan",
			plan: &Plan{
				ID:          "bench-large",
				FeatureName: "Large Feature",
				Description: "A large test feature with many tasks",
				CreatedAt:   time.Now(),
				Tasks:       makeTasks(100),
			},
		},
	}

	for _, tc := range plans {
		b.Run(tc.name, func(b *testing.B) {
			tempDir := b.TempDir()
			store := NewFilePlanStore(tempDir)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = store.SavePlan(tc.plan)
			}
		})
	}
}

// BenchmarkPlanDeserialization benchmarks plan JSON unmarshaling
func BenchmarkPlanDeserialization(b *testing.B) {
	tempDir := b.TempDir()
	store := NewFilePlanStore(tempDir)

	// Create test plans
	plans := map[string]*Plan{
		"small":  {ID: "bench-small", FeatureName: "Small", Tasks: makeTasks(5)},
		"medium": {ID: "bench-medium", FeatureName: "Medium", Tasks: makeTasks(20)},
		"large":  {ID: "bench-large", FeatureName: "Large", Tasks: makeTasks(100)},
	}

	for name, plan := range plans {
		if err := store.SavePlan(plan); err != nil {
			b.Fatalf("Failed to save %s plan: %v", name, err)
		}
	}

	for name, plan := range plans {
		b.Run(name+"_plan", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = store.LoadPlan(plan.ID)
			}
		})
	}
}

// BenchmarkPlanListOperations benchmarks listing plans
func BenchmarkPlanListOperations(b *testing.B) {
	counts := []int{1, 10, 50, 100}

	for _, count := range counts {
		b.Run(strings.ReplaceAll(b.Name(), "BenchmarkPlanListOperations/", "")+"/"+string(rune(count))+"_plans", func(b *testing.B) {
			tempDir := b.TempDir()
			store := NewFilePlanStore(tempDir)

			// Create plans
			for i := 0; i < count; i++ {
				plan := &Plan{
					ID:          "bench-plan-" + string(rune(i)),
					FeatureName: "Feature " + string(rune(i)),
					Tasks:       makeTasks(5),
				}
				if err := store.SavePlan(plan); err != nil {
					b.Fatalf("Failed to save plan: %v", err)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = store.ListPlans()
			}
		})
	}
}

// BenchmarkSanitizeIdentifier benchmarks identifier sanitization
func BenchmarkSanitizeIdentifier(b *testing.B) {
	testCases := []struct {
		name  string
		input string
	}{
		{
			name:  "simple",
			input: "my-feature-name",
		},
		{
			name:  "with_spaces",
			input: "My Feature Name With Spaces",
		},
		{
			name:  "with_special_chars",
			input: "Feature@#$%Name&*()With!Special?Chars",
		},
		{
			name:  "mixed_case_unicode",
			input: "Fëätürë Nämé with UNICODE",
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = SanitizeIdentifier(tc.input)
			}
		})
	}
}

// Helper function to create test tasks
func makeTasks(count int) []Task {
	tasks := make([]Task, count)
	for i := 0; i < count; i++ {
		tasks[i] = Task{
			ID:          string(rune(i)),
			Title:       "Task " + string(rune(i)),
			Description: "Description for task " + string(rune(i)),
			Type:        TaskTypeImplementation,
			Status:      TaskPending,
		}
	}
	return tasks
}
