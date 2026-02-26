package model

import (
	"strings"
	"testing"
)

func TestStreamAccumulator_TextContent(t *testing.T) {
	acc := NewStreamAccumulator()

	// Simulate streaming text chunks
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{Role: "assistant", Content: "Hello"},
		}},
	})
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{Content: " world"},
		}},
	})
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{Content: "!"},
		}},
	})

	if got := acc.Content(); got != "Hello world!" {
		t.Errorf("Content() = %q, want %q", got, "Hello world!")
	}

	msg := acc.Message()
	if msg.Role != "assistant" {
		t.Errorf("Message().Role = %q, want %q", msg.Role, "assistant")
	}
}

func TestStreamAccumulator_Reasoning(t *testing.T) {
	acc := NewStreamAccumulator()

	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{Reasoning: "Let me think"},
		}},
	})
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{Reasoning: " about this..."},
		}},
	})

	if got := acc.Reasoning(); got != "Let me think about this..." {
		t.Errorf("Reasoning() = %q, want %q", got, "Let me think about this...")
	}
}

func TestStreamAccumulator_SingleToolCall(t *testing.T) {
	acc := NewStreamAccumulator()

	// First chunk: ID and function name start
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				ToolCalls: []ToolCallDelta{{
					Index: 0,
					ID:    "call_abc123",
					Type:  "function",
					Function: &FunctionCallDelta{
						Name: "get_weather",
					},
				}},
			},
		}},
	})

	// Second chunk: arguments start
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				ToolCalls: []ToolCallDelta{{
					Index: 0,
					Function: &FunctionCallDelta{
						Arguments: `{"city":`,
					},
				}},
			},
		}},
	})

	// Third chunk: arguments continue
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				ToolCalls: []ToolCallDelta{{
					Index: 0,
					Function: &FunctionCallDelta{
						Arguments: `"Beijing"}`,
					},
				}},
			},
		}},
	})

	if !acc.HasToolCalls() {
		t.Fatal("HasToolCalls() = false, want true")
	}

	calls := acc.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("len(ToolCalls()) = %d, want 1", len(calls))
	}

	tc := calls[0]
	if tc.ID != "call_abc123" {
		t.Errorf("ToolCalls()[0].ID = %q, want %q", tc.ID, "call_abc123")
	}
	if tc.Type != "function" {
		t.Errorf("ToolCalls()[0].Type = %q, want %q", tc.Type, "function")
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("ToolCalls()[0].Function.Name = %q, want %q", tc.Function.Name, "get_weather")
	}
	if tc.Function.Arguments != `{"city":"Beijing"}` {
		t.Errorf("ToolCalls()[0].Function.Arguments = %q, want %q", tc.Function.Arguments, `{"city":"Beijing"}`)
	}
}

func TestStreamAccumulator_MultipleToolCalls(t *testing.T) {
	acc := NewStreamAccumulator()

	// First tool call
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				ToolCalls: []ToolCallDelta{{
					Index: 0,
					ID:    "call_1",
					Function: &FunctionCallDelta{
						Name:      "read_file",
						Arguments: `{"path":"a.txt"}`,
					},
				}},
			},
		}},
	})

	// Second tool call
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				ToolCalls: []ToolCallDelta{{
					Index: 1,
					ID:    "call_2",
					Function: &FunctionCallDelta{
						Name:      "read_file",
						Arguments: `{"path":"b.txt"}`,
					},
				}},
			},
		}},
	})

	calls := acc.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("len(ToolCalls()) = %d, want 2", len(calls))
	}

	if calls[0].ID != "call_1" || calls[0].Function.Arguments != `{"path":"a.txt"}` {
		t.Errorf("First tool call mismatch: %+v", calls[0])
	}
	if calls[1].ID != "call_2" || calls[1].Function.Arguments != `{"path":"b.txt"}` {
		t.Errorf("Second tool call mismatch: %+v", calls[1])
	}
}

func TestStreamAccumulator_KimiK2IDFormat(t *testing.T) {
	// Test with Kimi K2's ID format: functions.{func_name}:{idx}
	acc := NewStreamAccumulator()

	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				ToolCalls: []ToolCallDelta{{
					Index: 0,
					ID:    "functions.get_weather:0",
					Function: &FunctionCallDelta{
						Name:      "get_weather",
						Arguments: `{"city":"Beijing"}`,
					},
				}},
			},
		}},
	})

	calls := acc.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("len(ToolCalls()) = %d, want 1", len(calls))
	}

	// ID should be preserved exactly as received
	if calls[0].ID != "functions.get_weather:0" {
		t.Errorf("ID = %q, want %q", calls[0].ID, "functions.get_weather:0")
	}
}

func TestStreamAccumulator_Usage(t *testing.T) {
	acc := NewStreamAccumulator()

	// Chunks without usage
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{Content: "Hello"},
		}},
	})

	if acc.Usage() != nil {
		t.Error("Usage() should be nil before final chunk")
	}

	// Final chunk with usage
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{Content: "!"},
		}},
		Usage: &Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	})

	usage := acc.Usage()
	if usage == nil {
		t.Fatal("Usage() should not be nil after final chunk")
	}
	if usage.TotalTokens != 15 {
		t.Errorf("Usage().TotalTokens = %d, want 15", usage.TotalTokens)
	}
}

func TestStreamAccumulator_Reset(t *testing.T) {
	acc := NewStreamAccumulator()

	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				Role:    "assistant",
				Content: "Hello",
				ToolCalls: []ToolCallDelta{{
					Index: 0,
					ID:    "call_1",
				}},
			},
		}},
		Usage: &Usage{TotalTokens: 10},
	})

	acc.Reset()

	if acc.Content() != "" {
		t.Errorf("Content() after Reset() = %q, want empty", acc.Content())
	}
	if acc.HasToolCalls() {
		t.Error("HasToolCalls() after Reset() = true, want false")
	}
	if acc.Usage() != nil {
		t.Error("Usage() after Reset() should be nil")
	}
}

func TestStreamAccumulator_EmptyChunks(t *testing.T) {
	acc := NewStreamAccumulator()

	// Empty choices
	acc.Add(StreamChunk{Choices: nil})
	acc.Add(StreamChunk{Choices: []StreamChoice{}})

	if acc.Content() != "" {
		t.Errorf("Content() = %q, want empty", acc.Content())
	}
	if acc.HasToolCalls() {
		t.Error("HasToolCalls() = true, want false")
	}
}

func TestStreamAccumulator_IncrementalID(t *testing.T) {
	// Some providers may send ID in multiple chunks
	acc := NewStreamAccumulator()

	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				ToolCalls: []ToolCallDelta{{
					Index: 0,
					ID:    "call_",
				}},
			},
		}},
	})

	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				ToolCalls: []ToolCallDelta{{
					Index: 0,
					ID:    "abc123",
					Function: &FunctionCallDelta{
						Name:      "test",
						Arguments: "{}",
					},
				}},
			},
		}},
	})

	calls := acc.ToolCalls()
	if calls[0].ID != "call_abc123" {
		t.Errorf("ID = %q, want %q", calls[0].ID, "call_abc123")
	}
}

func TestParseToolCallsFromContent_SingleToolCall(t *testing.T) {
	content := `I'll help you with that.
<|tool_calls_section_begin|><|tool_call_begin|>functions.get_weather:0<|tool_call_argument_begin|>{"city":"Beijing"}<|tool_call_end|><|tool_calls_section_end|>`

	calls, cleanContent := ParseToolCallsFromContent(content)

	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}

	tc := calls[0]
	if tc.ID != "functions.get_weather:0" {
		t.Errorf("ID = %q, want %q", tc.ID, "functions.get_weather:0")
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("Function.Name = %q, want %q", tc.Function.Name, "get_weather")
	}
	if tc.Function.Arguments != `{"city":"Beijing"}` {
		t.Errorf("Function.Arguments = %q, want %q", tc.Function.Arguments, `{"city":"Beijing"}`)
	}

	if cleanContent != "I'll help you with that." {
		t.Errorf("cleanContent = %q, want %q", cleanContent, "I'll help you with that.")
	}
}

func TestParseToolCallsFromContent_MultipleToolCalls(t *testing.T) {
	content := `<|tool_calls_section_begin|><|tool_call_begin|>functions.read_file:0<|tool_call_argument_begin|>{"path":"a.txt"}<|tool_call_end|><|tool_call_begin|>functions.read_file:1<|tool_call_argument_begin|>{"path":"b.txt"}<|tool_call_end|><|tool_calls_section_end|>`

	calls, cleanContent := ParseToolCallsFromContent(content)

	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2", len(calls))
	}

	if calls[0].Function.Name != "read_file" || calls[0].Function.Arguments != `{"path":"a.txt"}` {
		t.Errorf("First call mismatch: %+v", calls[0])
	}
	if calls[1].Function.Name != "read_file" || calls[1].Function.Arguments != `{"path":"b.txt"}` {
		t.Errorf("Second call mismatch: %+v", calls[1])
	}

	if cleanContent != "" {
		t.Errorf("cleanContent = %q, want empty", cleanContent)
	}
}

func TestParseToolCallsFromContent_NoToolCalls(t *testing.T) {
	content := "Just a regular response without tool calls."

	calls, cleanContent := ParseToolCallsFromContent(content)

	if len(calls) != 0 {
		t.Errorf("len(calls) = %d, want 0", len(calls))
	}
	if cleanContent != content {
		t.Errorf("cleanContent = %q, want %q", cleanContent, content)
	}
}

func TestParseToolCallsFromContent_ComplexArguments(t *testing.T) {
	content := `<|tool_calls_section_begin|><|tool_call_begin|>functions.search:0<|tool_call_argument_begin|>{"query":"test","options":{"case_sensitive":true,"limit":10}}<|tool_call_end|><|tool_calls_section_end|>`

	calls, _ := ParseToolCallsFromContent(content)

	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}

	expectedArgs := `{"query":"test","options":{"case_sensitive":true,"limit":10}}`
	if calls[0].Function.Arguments != expectedArgs {
		t.Errorf("Arguments = %q, want %q", calls[0].Function.Arguments, expectedArgs)
	}
}

func TestExtractFunctionName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"functions.get_weather:0", "get_weather"},
		{"functions.read_file:5", "read_file"},
		{"functions.complex_name_here:123", "complex_name_here"},
		{"get_weather:0", "get_weather"},
		{"get_weather", "get_weather"},
	}

	for _, tc := range tests {
		got := extractFunctionName(tc.input)
		if got != tc.expected {
			t.Errorf("extractFunctionName(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestFinalizeWithTokenParsing_StructuredToolCalls(t *testing.T) {
	// When structured tool calls are present, they should be used
	acc := NewStreamAccumulator()

	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				Role: "assistant",
				ToolCalls: []ToolCallDelta{{
					Index: 0,
					ID:    "call_123",
					Function: &FunctionCallDelta{
						Name:      "test_func",
						Arguments: `{"arg":"value"}`,
					},
				}},
			},
		}},
	})

	msg := acc.FinalizeWithTokenParsing()

	if len(msg.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].ID != "call_123" {
		t.Errorf("ToolCalls[0].ID = %q, want %q", msg.ToolCalls[0].ID, "call_123")
	}
}

func TestFinalizeWithTokenParsing_TokensInContent(t *testing.T) {
	// When no structured tool calls but tokens in content, parse them
	acc := NewStreamAccumulator()

	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				Role:    "assistant",
				Content: `<|tool_calls_section_begin|><|tool_call_begin|>functions.get_weather:0<|tool_call_argument_begin|>{"city":"Tokyo"}<|tool_call_end|><|tool_calls_section_end|>`,
			},
		}},
	})

	msg := acc.FinalizeWithTokenParsing()

	if len(msg.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("Function.Name = %q, want %q", msg.ToolCalls[0].Function.Name, "get_weather")
	}
	// Content should be cleaned
	if content, ok := msg.Content.(string); ok && content != "" {
		t.Errorf("Content = %q, want empty", content)
	}
}

func TestFinalizeWithTokenParsing_BothStructuredAndTokens(t *testing.T) {
	// When structured tool calls exist AND tokens leak into content, filter them
	acc := NewStreamAccumulator()

	// Simulate structured tool call
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				Role: "assistant",
				ToolCalls: []ToolCallDelta{{
					Index: 0,
					ID:    "call_123",
					Function: &FunctionCallDelta{
						Name:      "test_func",
						Arguments: `{"arg":"value"}`,
					},
				}},
			},
		}},
	})

	// Also add content with leaked tokens (some providers do both)
	acc.Add(StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				Content: `I'll help you. <|tool_calls_section_begin|><|tool_call_begin|>functions.Bash:0<|tool_call_argument_begin|>{"cmd":"ls"}<|tool_call_end|><|tool_calls_section_end|>`,
			},
		}},
	})

	msg := acc.FinalizeWithTokenParsing()

	// Should use the structured tool calls, not parse from content
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].ID != "call_123" {
		t.Errorf("ToolCalls[0].ID = %q, want %q", msg.ToolCalls[0].ID, "call_123")
	}

	// Content should be cleaned of tokens but keep the text
	content, ok := msg.Content.(string)
	if !ok {
		t.Fatalf("Content is not a string")
	}
	if strings.Contains(content, "<|tool_call") {
		t.Errorf("Content still contains tokens: %q", content)
	}
	if !strings.Contains(content, "I'll help you") {
		t.Errorf("Content should contain user text, got: %q", content)
	}
}

func TestFilterToolCallTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no tokens",
			input:    "Hello, how can I help you?",
			expected: "Hello, how can I help you?",
		},
		{
			name:     "full tool call section",
			input:    `<|tool_calls_section_begin|><|tool_call_begin|>functions.Bash:0<|tool_call_argument_begin|>{"cmd":"ls"}<|tool_call_end|><|tool_calls_section_end|>`,
			expected: `{"cmd":"ls"}`,
		},
		{
			name:     "partial tokens at boundary",
			input:    `_call_begin|> functions.Bash:1`,
			expected: "",
		},
		{
			name:     "tool call ID only",
			input:    "functions.read_file:0",
			expected: "",
		},
		{
			name:     "mixed content and tokens",
			input:    "Let me check <|tool_call_begin|>functions.test:0",
			expected: "Let me check",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterToolCallTokens(tc.input)
			if got != tc.expected {
				t.Errorf("FilterToolCallTokens(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}
