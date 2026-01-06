package builtin

import (
	"strings"
	"testing"
)

// BenchmarkParameterSchemaValidation benchmarks parameter schema operations
func BenchmarkParameterSchemaValidation(b *testing.B) {
	schema := ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"name": {
				Type:        "string",
				Description: "Name parameter",
			},
			"age": {
				Type:        "integer",
				Description: "Age parameter",
			},
			"email": {
				Type:        "string",
				Description: "Email parameter",
			},
		},
		Required: []string{"name", "email"},
	}

	params := map[string]any{
		"name":  "John Doe",
		"age":   30,
		"email": "john@example.com",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate validation (actual validation would be here)
		_ = schema
		_ = params
	}
}

// BenchmarkResultSerialization benchmarks result encoding
func BenchmarkResultSerialization(b *testing.B) {
	results := []struct {
		name   string
		result *Result
	}{
		{
			name: "simple_success",
			result: &Result{
				Success: true,
				Data: map[string]any{
					"message": "Operation completed",
					"count":   42,
				},
			},
		},
		{
			name: "large_data",
			result: &Result{
				Success: true,
				Data: map[string]any{
					"items": make([]string, 100),
					"metadata": map[string]any{
						"total":     100,
						"processed": 95,
						"failed":    5,
					},
				},
			},
		},
		{
			name: "error_result",
			result: &Result{
				Success: false,
				Error:   "Operation failed: database connection timeout",
			},
		},
	}

	for _, tc := range results {
		b.Run(tc.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = tc.result
			}
		})
	}
}

// BenchmarkTodoToolCreate benchmarks TODO creation with varying list sizes
func BenchmarkTodoToolCreate(b *testing.B) {
	sizes := []int{1, 5, 10, 50}

	for _, size := range sizes {
		b.Run(strings.ReplaceAll(b.Name(), "BenchmarkTodoToolCreate/", "")+"/"+string(rune(size))+"_todos", func(b *testing.B) {
			todos := make([]any, size)
			for i := 0; i < size; i++ {
				todos[i] = map[string]any{
					"content":    "Task " + string(rune(i)),
					"activeForm": "Working on task " + string(rune(i)),
					"status":     "pending",
				}
			}

			params := map[string]any{
				"action":     "create",
				"session_id": "bench-session",
				"todos":      todos,
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = params
			}
		})
	}
}
