package conversation

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestCompactModelMessages_DeduplicationEligibility(t *testing.T) {
	for _, tc := range []struct {
		name           string
		recent, pinned int
		content        any
		duplicate      bool
	}{
		{name: "all_recent", recent: 3, content: "same evidence"},
		{name: "pinned_old_result", recent: 2, pinned: 1, content: "same evidence"},
		{name: "eligible_old_result", recent: 2, content: "same evidence", duplicate: true},
		{name: "empty_old_result", recent: 2, content: ""},
		{name: "structured_old_result", recent: 2, content: []model.ContentPart{{Type: "text", Text: "structured evidence"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []model.Message{{Role: "tool", Name: "read_file", ToolCallID: "old", Content: tc.content}, {Role: "tool", Name: "read_file", ToolCallID: "new", Content: tc.content}, {Role: "user", Content: "Continue."}}
			before, err := json.Marshal(messages)
			if err != nil {
				t.Fatal(err)
			}
			opts := DefaultEfficientContextOptions()
			opts.RecentMessages = tc.recent
			opts.PinnedFromIndex = tc.pinned
			opts.MaxBytes = 1 << 20
			projected := CompactModelMessages(messages, opts)
			text, _ := projected[0].Content.(string)
			if duplicate := strings.Contains(text, "[duplicate "); duplicate != tc.duplicate {
				t.Fatalf("duplicate=%v want=%v content=%q", duplicate, tc.duplicate, text)
			}
			if !tc.duplicate && !reflect.DeepEqual(projected[0].Content, tc.content) {
				t.Fatal("ineligible old result changed")
			}
			if !reflect.DeepEqual(projected[1].Content, tc.content) {
				t.Fatal("newest result changed")
			}
			after, err := json.Marshal(messages)
			if err != nil || string(before) != string(after) {
				t.Fatalf("durable transcript changed: %v", err)
			}
		})
	}
}

func TestCompactModelMessages_RecentToolBurstStillFitsBudget(t *testing.T) {
	messages := []model.Message{}
	for range 4 {
		messages = append(messages, model.Message{Role: "tool", Name: "read_file", Content: strings.Repeat("observed source evidence\n", 1000)})
	}
	messages = append(messages, model.Message{Role: "user", Content: "Retain these source results."}, model.Message{Role: "user", Content: "Continue."})
	opts := DefaultEfficientContextOptions()
	opts.RecentMessages = 32
	opts.OldToolBytes = 256
	opts.MaxBytes = 1200
	projected := CompactModelMessages(messages, opts)
	if len(projected) != len(messages) || modelMessagesBytes(projected) > opts.MaxBytes {
		t.Fatal("recent burst did not fit without losing messages")
	}
	for i := range 4 {
		got := projected[i].Content.(string)
		if !strings.Contains(got, "compacted") || strings.Contains(got, "[duplicate ") {
			t.Fatalf("unexpected burst projection: %q", got)
		}
		if len(messages[i].Content.(string)) != len(strings.Repeat("observed source evidence\n", 1000)) {
			t.Fatal("source result changed")
		}
	}
	for i := 4; i < len(messages); i++ {
		if projected[i].Content != messages[i].Content {
			t.Fatal("immediate user steering changed")
		}
	}
}

func TestCompactModelMessages_ProtectsPendingReasoningWithOnlyRecentResults(t *testing.T) {
	for _, structured := range []bool{false, true} {
		name := "text"
		if structured {
			name = "structured"
		}
		t.Run(name, func(t *testing.T) {
			pending := model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "pending", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"pending.go"}`}}}}
			answered := model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "answered", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"done.go"}`}}}}
			if structured {
				signature := "retained-signature"
				pending.ReasoningDetails = []model.ReasoningDetail{{Type: "reasoning.text", Text: "pending thought", Signature: &signature}}
				answered.ReasoningDetails = []model.ReasoningDetail{{Type: "reasoning.text", Text: "old thought"}}
			} else {
				pending.Reasoning = "pending thought"
				answered.Reasoning = "old thought"
			}
			messages := []model.Message{pending, answered, {Role: "tool", Name: "read_file", ToolCallID: "answered", Content: "observed source"}, {Role: "user", Content: "Continue."}}
			opts := DefaultEfficientContextOptions()
			opts.RecentMessages = 2
			opts.KeepReasoningRecent = 0
			opts.MaxBytes = 1 << 20
			projected := CompactModelMessages(messages, opts)
			if projected[0].Reasoning != pending.Reasoning || !reflect.DeepEqual(projected[0].ReasoningDetails, pending.ReasoningDetails) {
				t.Fatal("pending reasoning changed")
			}
			if projected[1].Reasoning != "" || len(projected[1].ReasoningDetails) != 0 {
				t.Fatal("answered old reasoning retained")
			}
			if messages[1].Reasoning != answered.Reasoning || !reflect.DeepEqual(messages[1].ReasoningDetails, answered.ReasoningDetails) {
				t.Fatal("durable reasoning changed")
			}
			if structured {
				projected[0].ReasoningDetails[0].Text = "projected edit"
				if messages[0].ReasoningDetails[0].Text != "pending thought" {
					t.Fatal("projected reasoning aliases durable history")
				}
			}
		})
	}
}
