package turntrace

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenBlankPathIsNoOp(t *testing.T) {
	tr, err := Open("")
	if err != nil {
		t.Fatalf("Open(\"\") error = %v", err)
	}
	if tr != nil {
		t.Fatalf("expected nil tracer for blank path")
	}
	// nil tracer methods must not panic
	tr.Record(TurnRecord{Iteration: 1})
	if err := tr.Close(); err != nil {
		t.Fatalf("nil Close() error = %v", err)
	}
}

func TestTracerRecordsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	tr, err := Open(path)
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	tr.Record(TurnRecord{
		Iteration:           0,
		Model:               "moonshotai/kimi-k3",
		NumMessages:         2,
		Roles:               []string{"system", "user"},
		UseTools:            false,
		SupportsTools:       false,
		SupportedParameters: []string{"reasoning", "tools"},
		ToolsOffered:        0,
		Branch:              "finalize",
		ContentPreview:      "I'll search for the files.",
	})
	tr.Record(TurnRecord{Iteration: 1, Model: "moonshotai/kimi-k3", Branch: "tool_calls",
		StructuredToolCalls: []ToolCallPreview{{Name: "list_files", ID: "c1"}}})
	if err := tr.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer f.Close()
	var recs []TurnRecord
	s := bufio.NewScanner(f)
	for s.Scan() {
		var r TurnRecord
		if err := json.Unmarshal(s.Bytes(), &r); err != nil {
			t.Fatalf("unmarshal line: %v", err)
		}
		recs = append(recs, r)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	// The diagnostic that matters: the trace shows tools were dropped even though
	// the model's catalog lists "tools".
	if recs[0].UseTools || recs[0].SupportsTools {
		t.Fatalf("expected tools-dropped record, got %+v", recs[0])
	}
	hasTools := false
	for _, p := range recs[0].SupportedParameters {
		if p == "tools" {
			hasTools = true
		}
	}
	if !hasTools {
		t.Fatalf("expected supported_parameters to include tools (the contradiction), got %v", recs[0].SupportedParameters)
	}
	if recs[1].Branch != "tool_calls" || len(recs[1].StructuredToolCalls) != 1 {
		t.Fatalf("second record malformed: %+v", recs[1])
	}
}

func TestInlineToolMarkupDetected(t *testing.T) {
	cases := map[string]bool{
		"plain text answer":                        false,
		"I'll help with that.":                     false,
		"<|tool_call_begin|>functions.read:0":      true,
		"prefix <|tool_calls_section_begin|> more": true,
		`<tool_call>run_shell(command="ls")`:       true,
		`<tool_call name="x">`:                     true,
	}
	for content, want := range cases {
		if got := InlineToolMarkupDetected(content); got != want {
			t.Fatalf("InlineToolMarkupDetected(%q) = %v, want %v", content, got, want)
		}
	}
}
