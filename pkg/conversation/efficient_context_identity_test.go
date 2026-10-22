package conversation

import (
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestCompactModelMessages_PreservesLegacyToolIdentities(t *testing.T) {
	for _, tc := range []struct {
		name          string
		matched       bool
		names, bodies [2]string
		duplicate     bool
	}{
		{name: "same_operation_new_output", matched: true, names: [2]string{"read_file", "read_file"}, bodies: [2]string{`{"path":"same.go"}`, `{"path":"same.go"}`}, duplicate: true},
		{name: "different_operations_same_output", matched: true, names: [2]string{"read_file", "read_file"}, bodies: [2]string{`{"path":"one.go"}`, `{"path":"two.go"}`}},
		{name: "orphan_same_output", names: [2]string{"read_file", "read_file"}, bodies: [2]string{"same evidence", "same evidence"}, duplicate: true},
		{name: "orphan_different_names", names: [2]string{"read_file", "run_shell"}, bodies: [2]string{"same evidence", "same evidence"}},
		{name: "orphan_different_nul_bodies", names: [2]string{"read_file", "read_file"}, bodies: [2]string{"part\x00one", "part\x00two"}},
		{name: "matched_imported_nul_boundary", matched: true, names: [2]string{"read_file\x00part", "read_file"}, bodies: [2]string{"one", "part\x00one"}, duplicate: true},
		{name: "orphan_imported_nul_boundary", names: [2]string{"read_file\x00part", "read_file"}, bodies: [2]string{"one", "part\x00one"}, duplicate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []model.Message{{Role: "system", Content: "Retain exact observed evidence."}}
			indices := [2]int{}
			for i, id := range []string{"first", "second"} {
				if tc.matched {
					messages = append(messages, model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: id, Type: "function", Function: model.FunctionCall{Name: tc.names[i], Arguments: tc.bodies[i]}}}})
				}
				content := tc.bodies[i]
				if tc.matched {
					content = "same observed output"
				}
				indices[i] = len(messages)
				messages = append(messages, model.Message{Role: "tool", Name: tc.names[i], ToolCallID: id, Content: content})
			}
			messages = append(messages, model.Message{Role: "user", Content: "Continue."})
			original, err := json.Marshal(messages)
			if err != nil {
				t.Fatal(err)
			}
			projected := CompactModelMessages(messages, EfficientContextOptions{RecentMessages: 1, OldToolBytes: 1024, OldToolArgumentBytes: 512, OldAssistantBytes: 1024, KeepReasoningRecent: 1, MaxBytes: 1 << 20})
			first := projected[indices[0]].Content.(string)
			if got := strings.Contains(first, "[duplicate "); got != tc.duplicate {
				t.Fatalf("duplicate=%v want=%v content=%q", got, tc.duplicate, first)
			}
			if !tc.duplicate && first != messages[indices[0]].Content {
				t.Fatal("distinct tool evidence changed")
			}
			if projected[indices[1]].Content != messages[indices[1]].Content {
				t.Fatal("newest evidence changed")
			}
			after, err := json.Marshal(messages)
			if err != nil || string(after) != string(original) {
				t.Fatalf("durable transcript changed: %v", err)
			}
			if tc.matched {
				projected[indices[0]-1].ToolCalls[0].Function.Arguments = "projected-only edit"
				if messages[indices[0]-1].ToolCalls[0].Function.Arguments != tc.bodies[0] {
					t.Fatal("projected tool calls alias durable transcript")
				}
			}
		})
	}
}

func TestCompactModelMessages_UsesOriginalArgumentsForOutOfOrderResults(t *testing.T) {
	arguments := `{"content":"` + strings.Repeat("retained source ", 100) + `"}`
	call := func(id string) model.Message {
		return model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: id, Function: model.FunctionCall{Name: "write_file", Arguments: arguments}}}}
	}
	messages := []model.Message{
		{Role: "tool", ToolCallID: "first", Name: "write_file", Content: "old result"},
		call("first"),
		{Role: "tool", ToolCallID: "second", Name: "write_file", Content: "new result"},
		call("second"),
		{Role: "user", Content: "Continue."},
	}
	projected := CompactModelMessages(messages, EfficientContextOptions{RecentMessages: 3, OldToolBytes: 1024, OldToolArgumentBytes: 128, OldAssistantBytes: 1024, KeepReasoningRecent: 1, MaxBytes: 1 << 20})
	if !strings.Contains(projected[0].Content.(string), "[duplicate ") || projected[2].Content != "new result" {
		t.Fatal("deduplication used compacted arguments instead of the original operation")
	}
	if len(projected[1].ToolCalls[0].Function.Arguments) >= len(arguments) || projected[3].ToolCalls[0].Function.Arguments != arguments {
		t.Fatal("old arguments should compact while recent arguments remain exact")
	}
	if messages[1].ToolCalls[0].Function.Arguments != arguments || messages[0].Content != "old result" {
		t.Fatal("projection changed the durable transcript")
	}
}
