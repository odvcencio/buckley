package tui

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/encoding/toon"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestStoredToolError_ModelResultEnvelope(t *testing.T) {
	for _, useToon := range []bool{false, true} {
		format := "JSON"
		if useToon {
			format = "TOON"
		}
		t.Run(format, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				result builtin.Result
				want   string
			}{
				{"success", builtin.Result{Success: true}, ""},
				{"failed without error", builtin.Result{Success: false}, "failed"},
				{"nested diagnostic", builtin.Result{Success: true, Data: map[string]any{"error": "nested diagnostic data"}}, ""},
				{"nested false result", builtin.Result{Success: true, Data: map[string]any{"success": false, "error": "nested result"}}, ""},
				{"table error fields", builtin.Result{Success: true, Data: map[string]any{"rows": []map[string]any{{"error": "first"}, {"error": "second"}}}}, ""},
				{"error only", builtin.Result{Error: "could not read the file"}, "could not read the file"},
				{"escaped error", builtin.Result{Error: "could not read \"café 日本語.txt\"\nretry later"}, "could not read \"café 日本語.txt\"\nretry later"},
				{"error with data", builtin.Result{Error: "outer error", Data: map[string]any{"error": "inner error"}}, "outer error"},
				{"explicit error wins", builtin.Result{Success: true, Error: "explicit error", Data: map[string]any{"error": "inner error"}}, "explicit error"},
				{"null text is an error", builtin.Result{Error: "null"}, "null"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					// Use the model-facing wire shape, including arbitrary nested data.
					payload := map[string]any{"success": tc.result.Success}
					if tc.result.Error != "" {
						payload["error"] = tc.result.Error
					}
					if len(tc.result.Data) > 0 {
						payload["data"] = tc.result.Data
					}
					encoded, err := toon.New(useToon).MarshalString(payload)
					if err != nil {
						t.Fatal(err)
					}
					if got := storedToolError(encoded); got != tc.want {
						t.Fatalf("storedToolError(%q) = %q, want %q", encoded, got, tc.want)
					}
					wantPrefix := "✓ fixture — "
					if tc.want != "" {
						wantPrefix = "✗ fixture — "
					}
					if got := storedToolResultProgressSummary("fixture", encoded); !strings.HasPrefix(got, wantPrefix) {
						t.Fatalf("restored tool progress = %q, want prefix %q", got, wantPrefix)
					}
				})
			}
		})
	}
}

func TestStoredToolError_LegacyText(t *testing.T) {
	for _, tc := range []struct {
		content string
		want    string
	}{
		{"Error: process failed\nretry later", "process failed\nretry later"},
		{" error: denied ", "denied"},
		{"read completed", ""},
		{"data:\n  error: payload text\n", ""},
	} {
		t.Run(tc.content, func(t *testing.T) {
			if got := storedToolError(tc.content); got != tc.want {
				t.Fatalf("storedToolError(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func BenchmarkStoredToolError_ModelEnvelope(b *testing.B) {
	for _, useToon := range []bool{false, true} {
		format := "JSON"
		if useToon {
			format = "TOON"
		}
		for _, size := range []struct {
			name  string
			lines int
		}{{"short", 4}, {"large", 1024}} {
			b.Run(format+"/"+size.name, func(b *testing.B) {
				encoded, err := toon.New(useToon).MarshalString(map[string]any{
					"success": true,
					"data":    map[string]any{"content": strings.Repeat("VALUE café 日本語\n", size.lines)},
				})
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.SetBytes(int64(len(encoded)))
				for b.Loop() {
					if got := storedToolError(encoded); got != "" {
						b.Fatalf("successful envelope reported an error: %q", got)
					}
				}
			})
		}
	}
}
