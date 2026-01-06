package builtin

import (
	"encoding/json"
	"testing"
)

// FuzzTodoToolParameters fuzzes TODO tool parameter parsing
func FuzzTodoToolParameters(f *testing.F) {
	// Seed corpus with valid parameter combinations
	f.Add("create", "session-123", `[{"content":"task1","activeForm":"doing task1","status":"pending"}]`)
	f.Add("update", "session-456", `{"todo_id":1,"status":"completed"}`)
	f.Add("list", "session-789", "")
	f.Add("", "", "")
	f.Add("unknown_action", "session", "{}")

	f.Fuzz(func(t *testing.T, action string, sessionID string, todosJSON string) {
		params := map[string]any{
			"action":     action,
			"session_id": sessionID,
		}

		// Try to parse todos JSON if provided
		if todosJSON != "" {
			var todos any
			if err := json.Unmarshal([]byte(todosJSON), &todos); err == nil {
				params["todos"] = todos
			}
		}

		tool := &TodoTool{Store: nil} // Nil store to focus on parameter validation

		// Should not panic
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Execute panicked with params %v: %v", params, r)
			}
		}()

		result, err := tool.Execute(params)

		// Invariants:
		// 1. Result should never be nil
		if result == nil && err == nil {
			t.Error("Execute returned nil result and nil error")
		}

		// 2. Empty action should fail
		if action == "" {
			if result != nil && result.Success {
				t.Error("Execute should fail with empty action")
			}
		}

		// 3. Empty session_id should fail
		if sessionID == "" {
			if result != nil && result.Success {
				t.Error("Execute should fail with empty session_id")
			}
		}

		// 4. Nil store should fail gracefully
		if result != nil && result.Success {
			t.Error("Execute should fail with nil store")
		}
	})
}

// FuzzResultSerialization fuzzes Result struct serialization
func FuzzResultSerialization(f *testing.F) {
	// Seed corpus
	f.Add(true, "success message", "")
	f.Add(false, "", "error message")
	f.Add(true, "data", "{\"key\":\"value\"}")

	f.Fuzz(func(t *testing.T, success bool, dataStr string, errorMsg string) {
		result := &Result{
			Success: success,
			Error:   errorMsg,
		}

		// Try to parse data as JSON
		if dataStr != "" {
			var data any
			if err := json.Unmarshal([]byte(dataStr), &data); err == nil {
				// Type assert to map[string]any if possible
				if dataMap, ok := data.(map[string]any); ok {
					result.Data = dataMap
				}
			}
		}

		// Should not panic during JSON marshaling
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("JSON marshaling panicked: %v", r)
			}
		}()

		_, err := json.Marshal(result)
		if err != nil {
			// This is okay - some data types can't be marshaled
			// Just verify we don't panic
		}
	})
}
