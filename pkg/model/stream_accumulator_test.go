package model

import (
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
