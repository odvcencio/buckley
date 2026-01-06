package builtin

import (
	"testing"
)

func TestSplitOneShotOutput(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantOutput    string
		wantStatsNil  bool
		wantStatsKeys []string
	}{
		{
			name:         "empty input",
			input:        "",
			wantOutput:   "",
			wantStatsNil: true,
		},
		{
			name:         "whitespace only",
			input:        "   \n\t\n  ",
			wantOutput:   "",
			wantStatsNil: true,
		},
		{
			name:         "output with no stats",
			input:        "Just some regular output\nwith multiple lines",
			wantOutput:   "Just some regular output\nwith multiple lines",
			wantStatsNil: true,
		},
		{
			name:          "output with Session Statistics header",
			input:         "Response content here\n────────────────\nSession Statistics:\nModel: gpt-4\nTokens: 1234\nCost: $0.05",
			wantOutput:    "Response content here",
			wantStatsNil:  false,
			wantStatsKeys: []string{"model", "tokens", "cost"},
		},
		{
			name:          "output with stats prefix (Model:)",
			input:         "Some output\nModel: gpt-4o\nProvider: OpenAI\nTime: 2.5s",
			wantOutput:    "Some output",
			wantStatsNil:  false,
			wantStatsKeys: []string{"model", "provider", "time"},
		},
		{
			name:         "stats prefix with less than 2 entries",
			input:        "Some output\nModel: gpt-4o",
			wantOutput:   "Some output\nModel: gpt-4o",
			wantStatsNil: true,
		},
		{
			name:          "Session Statistics with separator before",
			input:         "Output\n────\nSession Statistics:\nTokens: 500",
			wantOutput:    "Output",
			wantStatsNil:  false,
			wantStatsKeys: []string{"tokens"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotOutput, gotStats := splitOneShotOutput(tc.input)
			if gotOutput != tc.wantOutput {
				t.Errorf("splitOneShotOutput(%q) output = %q, want %q", tc.input, gotOutput, tc.wantOutput)
			}
			if tc.wantStatsNil && gotStats != nil {
				t.Errorf("splitOneShotOutput(%q) stats = %v, want nil", tc.input, gotStats)
			}
			if !tc.wantStatsNil && gotStats == nil {
				t.Errorf("splitOneShotOutput(%q) stats = nil, want non-nil with keys %v", tc.input, tc.wantStatsKeys)
			}
			if !tc.wantStatsNil && gotStats != nil {
				for _, key := range tc.wantStatsKeys {
					if _, ok := gotStats[key]; !ok {
						t.Errorf("splitOneShotOutput(%q) stats missing key %q", tc.input, key)
					}
				}
			}
		})
	}
}

func TestHasOneShotStatPrefix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "empty string", input: "", want: false},
		{name: "whitespace only", input: "   ", want: false},
		{name: "model lowercase", input: "model: gpt-4", want: true},
		{name: "Model uppercase", input: "Model: claude-3", want: true},
		{name: "MODEL all caps", input: "MODEL: test", want: true},
		{name: "provider prefix", input: "provider: OpenAI", want: true},
		{name: "Provider prefix", input: "Provider: Anthropic", want: true},
		{name: "time prefix", input: "time: 2.5s", want: true},
		{name: "Time prefix", input: "Time: 1m30s", want: true},
		{name: "tokens prefix", input: "tokens: 1234", want: true},
		{name: "Tokens prefix", input: "Tokens: 5678", want: true},
		{name: "cost prefix", input: "cost: $0.05", want: true},
		{name: "Cost prefix", input: "Cost: $1.23", want: true},
		{name: "with leading whitespace", input: "  model: test", want: true},
		{name: "no colon", input: "model gpt-4", want: false},
		{name: "random text", input: "hello world", want: false},
		{name: "partial match", input: "tokenization", want: false},
		{name: "model in middle", input: "the model: is here", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hasOneShotStatPrefix(tc.input)
			if got != tc.want {
				t.Errorf("hasOneShotStatPrefix(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseOneShotStats(t *testing.T) {
	tests := []struct {
		name       string
		lines      []string
		wantNil    bool
		wantKeys   map[string]any
		wantTokens any
	}{
		{
			name:    "empty lines",
			lines:   []string{},
			wantNil: true,
		},
		{
			name:    "only whitespace lines",
			lines:   []string{"", "   ", "\t"},
			wantNil: true,
		},
		{
			name:    "no recognized prefixes",
			lines:   []string{"random line", "another line"},
			wantNil: true,
		},
		{
			name:  "model only",
			lines: []string{"Model: gpt-4o"},
			wantKeys: map[string]any{
				"model": "gpt-4o",
			},
		},
		{
			name:  "provider only",
			lines: []string{"Provider: OpenAI"},
			wantKeys: map[string]any{
				"provider": "OpenAI",
			},
		},
		{
			name:  "time only",
			lines: []string{"Time: 2.5s"},
			wantKeys: map[string]any{
				"time": "2.5s",
			},
		},
		{
			name:       "tokens as integer",
			lines:      []string{"Tokens: 1234"},
			wantTokens: 1234,
		},
		{
			name:       "tokens as string (non-numeric)",
			lines:      []string{"Tokens: unknown"},
			wantTokens: "unknown",
		},
		{
			name:  "cost with USD parsing",
			lines: []string{"Cost: $0.05"},
			wantKeys: map[string]any{
				"cost":     "$0.05",
				"cost_usd": 0.05,
			},
		},
		{
			name:  "cost without dollar sign",
			lines: []string{"Cost: 0.10"},
			wantKeys: map[string]any{
				"cost":     "0.10",
				"cost_usd": 0.10,
			},
		},
		{
			name:  "cost non-numeric",
			lines: []string{"Cost: unknown"},
			wantKeys: map[string]any{
				"cost": "unknown",
			},
		},
		{
			name:  "all fields",
			lines: []string{"Model: gpt-4", "Provider: OpenAI", "Time: 1s", "Tokens: 100", "Cost: $0.01"},
			wantKeys: map[string]any{
				"model":    "gpt-4",
				"provider": "OpenAI",
				"time":     "1s",
				"tokens":   100,
				"cost":     "$0.01",
				"cost_usd": 0.01,
			},
		},
		{
			name:  "stops at separator",
			lines: []string{"Model: gpt-4", "────────────", "Provider: ignored"},
			wantKeys: map[string]any{
				"model": "gpt-4",
			},
		},
		{
			name:  "empty values are skipped",
			lines: []string{"Model:", "Provider: test"},
			wantKeys: map[string]any{
				"provider": "test",
			},
		},
		{
			name:    "tokens with empty value",
			lines:   []string{"Tokens:"},
			wantNil: true,
		},
		{
			name:    "cost with empty value",
			lines:   []string{"Cost:"},
			wantNil: true,
		},
		{
			name:  "mixed case prefixes",
			lines: []string{"mOdEl: test1", "pRoViDeR: test2"},
			wantKeys: map[string]any{
				"model":    "test1",
				"provider": "test2",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOneShotStats(tc.lines)
			if tc.wantNil {
				if got != nil {
					t.Errorf("parseOneShotStats(%v) = %v, want nil", tc.lines, got)
				}
				return
			}
			if got == nil {
				t.Errorf("parseOneShotStats(%v) = nil, want non-nil", tc.lines)
				return
			}
			if tc.wantTokens != nil {
				if got["tokens"] != tc.wantTokens {
					t.Errorf("parseOneShotStats tokens = %v (%T), want %v (%T)", got["tokens"], got["tokens"], tc.wantTokens, tc.wantTokens)
				}
			}
			for key, wantVal := range tc.wantKeys {
				if gotVal, ok := got[key]; !ok {
					t.Errorf("parseOneShotStats missing key %q", key)
				} else if gotVal != wantVal {
					t.Errorf("parseOneShotStats[%q] = %v, want %v", key, gotVal, wantVal)
				}
			}
		})
	}
}

func TestCodexTool(t *testing.T) {
	tool := &CodexTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "invoke_codex" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "invoke_codex")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing prompt parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing prompt")
		}
	})
}

func TestClaudeTool(t *testing.T) {
	tool := &ClaudeTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "invoke_claude" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "invoke_claude")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing prompt parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing prompt")
		}
	})
}

func TestBuckleyTool(t *testing.T) {
	tool := &BuckleyTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "invoke_buckley" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "invoke_buckley")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing prompt parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing prompt")
		}
	})
}

func TestSubagentTool(t *testing.T) {
	tool := &SubagentTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "spawn_subagent" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "spawn_subagent")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing task parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing task")
		}
	})
}
