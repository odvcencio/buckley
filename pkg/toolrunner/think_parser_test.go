package toolrunner

import (
	"strings"
	"testing"
)

func TestThinkTagParser_BasicThinkTag(t *testing.T) {
	var reasoning, text strings.Builder
	var reasoningEnded bool

	p := NewThinkTagParser(
		func(s string) { reasoning.WriteString(s) },
		func(s string) { text.WriteString(s) },
		func() { reasoningEnded = true },
	)

	p.Write("<think>I am thinking</think>Hello world")
	p.Flush()

	if reasoning.String() != "I am thinking" {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), "I am thinking")
	}
	if text.String() != "Hello world" {
		t.Errorf("text = %q, want %q", text.String(), "Hello world")
	}
	if !reasoningEnded {
		t.Error("OnReasoningEnd was not called")
	}
}

func TestThinkTagParser_StreamedChunks(t *testing.T) {
	var reasoning, text strings.Builder
	var reasoningEndCount int

	p := NewThinkTagParser(
		func(s string) { reasoning.WriteString(s) },
		func(s string) { text.WriteString(s) },
		func() { reasoningEndCount++ },
	)

	chunks := []string{"<thi", "nk>thinking", " content</th", "ink>response"}
	for _, chunk := range chunks {
		p.Write(chunk)
	}
	p.Flush()

	if reasoning.String() != "thinking content" {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), "thinking content")
	}
	if text.String() != "response" {
		t.Errorf("text = %q, want %q", text.String(), "response")
	}
	if reasoningEndCount != 1 {
		t.Errorf("reasoningEndCount = %d, want 1", reasoningEndCount)
	}
}

func TestThinkTagParser_NoThinkTags(t *testing.T) {
	var reasoning, text strings.Builder

	p := NewThinkTagParser(
		func(s string) { reasoning.WriteString(s) },
		func(s string) { text.WriteString(s) },
		func() {},
	)

	p.Write("Just normal text without tags")
	p.Flush()

	if reasoning.String() != "" {
		t.Errorf("reasoning = %q, want empty", reasoning.String())
	}
	if text.String() != "Just normal text without tags" {
		t.Errorf("text = %q, want %q", text.String(), "Just normal text without tags")
	}
}

func TestThinkTagParser_UnclosedTag(t *testing.T) {
	var reasoning, text strings.Builder
	var reasoningEnded bool

	p := NewThinkTagParser(
		func(s string) { reasoning.WriteString(s) },
		func(s string) { text.WriteString(s) },
		func() { reasoningEnded = true },
	)

	p.Write("<think>unclosed reasoning content")
	p.Flush()

	if reasoning.String() != "unclosed reasoning content" {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), "unclosed reasoning content")
	}
	if text.String() != "" {
		t.Errorf("text = %q, want empty", text.String())
	}
	if !reasoningEnded {
		t.Error("OnReasoningEnd should be called on Flush for unclosed tag")
	}
}

func TestThinkTagParser_MultipleThinkBlocks(t *testing.T) {
	var reasoning, text strings.Builder
	var reasoningEndCount int

	p := NewThinkTagParser(
		func(s string) { reasoning.WriteString(s) },
		func(s string) { text.WriteString(s) },
		func() { reasoningEndCount++ },
	)

	p.Write("<think>first</think>middle<think>second</think>end")
	p.Flush()

	if reasoning.String() != "firstsecond" {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), "firstsecond")
	}
	if text.String() != "middleend" {
		t.Errorf("text = %q, want %q", text.String(), "middleend")
	}
	if reasoningEndCount != 2 {
		t.Errorf("reasoningEndCount = %d, want 2", reasoningEndCount)
	}
}

func TestThinkTagParser_EmptyThinkTag(t *testing.T) {
	var reasoning, text strings.Builder
	var reasoningEnded bool

	p := NewThinkTagParser(
		func(s string) { reasoning.WriteString(s) },
		func(s string) { text.WriteString(s) },
		func() { reasoningEnded = true },
	)

	p.Write("<think></think>content")
	p.Flush()

	if reasoning.String() != "" {
		t.Errorf("reasoning = %q, want empty", reasoning.String())
	}
	if text.String() != "content" {
		t.Errorf("text = %q, want %q", text.String(), "content")
	}
	if reasoningEnded {
		t.Error("OnReasoningEnd should not be called for empty think tag")
	}
}
