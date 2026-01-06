package builtin

import (
	"testing"
)

func TestGetStringParam(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		key    string
		want   string
	}{
		{
			name:   "nil map",
			params: nil,
			key:    "test",
			want:   "",
		},
		{
			name:   "empty map",
			params: map[string]any{},
			key:    "test",
			want:   "",
		},
		{
			name:   "key not present",
			params: map[string]any{"other": "value"},
			key:    "test",
			want:   "",
		},
		{
			name:   "key with string value",
			params: map[string]any{"test": "hello"},
			key:    "test",
			want:   "hello",
		},
		{
			name:   "key with empty string",
			params: map[string]any{"test": ""},
			key:    "test",
			want:   "",
		},
		{
			name:   "key with non-string value (int)",
			params: map[string]any{"test": 123},
			key:    "test",
			want:   "",
		},
		{
			name:   "key with non-string value (bool)",
			params: map[string]any{"test": true},
			key:    "test",
			want:   "",
		},
		{
			name:   "key with non-string value (nil)",
			params: map[string]any{"test": nil},
			key:    "test",
			want:   "",
		},
		{
			name:   "key with whitespace string",
			params: map[string]any{"test": "  spaces  "},
			key:    "test",
			want:   "  spaces  ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getStringParam(tc.params, tc.key)
			if got != tc.want {
				t.Errorf("getStringParam(%v, %q) = %q, want %q", tc.params, tc.key, got, tc.want)
			}
		})
	}
}

func TestGetIntParam(t *testing.T) {
	tests := []struct {
		name     string
		params   map[string]any
		key      string
		defValue int
		want     int
	}{
		{
			name:     "nil map returns default",
			params:   nil,
			key:      "test",
			defValue: 42,
			want:     42,
		},
		{
			name:     "empty map returns default",
			params:   map[string]any{},
			key:      "test",
			defValue: 10,
			want:     10,
		},
		{
			name:     "key not present returns default",
			params:   map[string]any{"other": 5},
			key:      "test",
			defValue: 20,
			want:     20,
		},
		{
			name:     "key with int value",
			params:   map[string]any{"test": 100},
			key:      "test",
			defValue: 0,
			want:     100,
		},
		{
			name:     "key with zero int",
			params:   map[string]any{"test": 0},
			key:      "test",
			defValue: 50,
			want:     0,
		},
		{
			name:     "key with negative int",
			params:   map[string]any{"test": -5},
			key:      "test",
			defValue: 10,
			want:     -5,
		},
		{
			name:     "key with float64 value (JSON numbers)",
			params:   map[string]any{"test": float64(25)},
			key:      "test",
			defValue: 0,
			want:     25,
		},
		{
			name:     "key with float64 with decimals (truncated)",
			params:   map[string]any{"test": float64(25.9)},
			key:      "test",
			defValue: 0,
			want:     25,
		},
		{
			name:     "key with negative float64",
			params:   map[string]any{"test": float64(-10.5)},
			key:      "test",
			defValue: 0,
			want:     -10,
		},
		{
			name:     "key with string value returns default",
			params:   map[string]any{"test": "123"},
			key:      "test",
			defValue: 99,
			want:     99,
		},
		{
			name:     "key with bool value returns default",
			params:   map[string]any{"test": true},
			key:      "test",
			defValue: 77,
			want:     77,
		},
		{
			name:     "key with nil value returns default",
			params:   map[string]any{"test": nil},
			key:      "test",
			defValue: 33,
			want:     33,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getIntParam(tc.params, tc.key, tc.defValue)
			if got != tc.want {
				t.Errorf("getIntParam(%v, %q, %d) = %d, want %d", tc.params, tc.key, tc.defValue, got, tc.want)
			}
		})
	}
}

func TestLookupContextTool(t *testing.T) {
	tool := &LookupContextTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "lookup_context" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "lookup_context")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing store returns error", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure when store is nil")
		}
		if result.Error != "code index is not available" {
			t.Errorf("unexpected error message: %s", result.Error)
		}
	})

	t.Run("lookup with query but nil store", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"query": "test function",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure when store is nil")
		}
	})
}
