package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestStreamAccumulator_SSEAllocationProfile(t *testing.T) {
	arguments := `{"text":"` + strings.Repeat("x", 256<<10) + `"}`
	var wire bytes.Buffer
	chunks := 0
	for offset := 0; offset < len(arguments); offset += 64 {
		delta := ToolCallDelta{Index: 0, Function: &FunctionCallDelta{Arguments: arguments[offset:min(offset+64, len(arguments))]}}
		if offset == 0 {
			delta.ID, delta.Type, delta.Function.Name = "call_profile", "function", "record_note"
		}
		chunk := StreamChunk{Choices: []StreamChoice{{Delta: MessageDelta{Role: "assistant", ToolCalls: []ToolCallDelta{delta}}}}}
		encoded, err := json.Marshal(chunk)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&wire, "data: %s\n\n", encoded)
		chunks++
	}
	wire.WriteString("data: [DONE]\n\n")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	acc := NewStreamAccumulator()
	stream := make(chan StreamChunk, 16)
	complete := make(chan error, 1)
	go func() {
		complete <- ParseSSEStream(context.Background(), &wire, stream)
		close(stream)
	}()
	for chunk := range stream {
		acc.Add(chunk)
	}
	if err := <-complete; err != nil {
		t.Fatal(err)
	}
	message := acc.Message()
	runtime.ReadMemStats(&after)
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].ID != "call_profile" || message.ToolCalls[0].Function.Name != "record_note" || message.ToolCalls[0].Function.Arguments != arguments {
		t.Fatal("stream changed tool call bytes")
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Message
	if err := json.Unmarshal(encoded, &roundTrip); err != nil || roundTrip.ToolCalls[0].Function.Arguments != arguments {
		t.Fatal("message JSON changed tool arguments")
	}
	t.Logf("argument_bytes=%d sse_chunks=%d allocated_bytes=%d arguments_exact=true json_round_trip=true", len(arguments), chunks, after.TotalAlloc-before.TotalAlloc)
}

func TestStreamAccumulator_ResetRetentionProfile(t *testing.T) {
	acc := NewStreamAccumulator()
	large := strings.Repeat("x", 1<<20)
	acc.Add(StreamChunk{Choices: []StreamChoice{{Delta: MessageDelta{Content: large, Reasoning: large}}}})
	before := cap(acc.content) + cap(acc.reasoning)
	acc.Reset()
	after := cap(acc.content) + cap(acc.reasoning)
	t.Logf("text_bytes=%d reasoning_bytes=%d buffer_capacity_before=%d buffer_capacity_after_reset=%d", len(large), len(large), before, after)
	if after > 128<<10 {
		t.Error("reset retained oversized stream buffers")
	}
}
