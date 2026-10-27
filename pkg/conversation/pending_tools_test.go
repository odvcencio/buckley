package conversation

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func pendingProjectionHistory(reasoning bool) ([]model.Message, model.Message) {
	pending := model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "pending", Type: "function", Function: model.FunctionCall{Name: "write_file", Arguments: `{"path":"pending.go","content":"` + strings.Repeat("original source ", 4096) + `"}`}}}}
	if reasoning {
		signature := "exact-provider-signature"
		pending.Reasoning = "Exact pending thought"
		pending.ReasoningDetails = []model.ReasoningDetail{{Type: "reasoning.text", Text: "Pending signed reasoning", Signature: &signature}}
	}
	messages := []model.Message{{Role: "user", Content: "Finish the pending operation."}, pending}
	for range 20 {
		messages = append(messages, model.Message{Role: "user", Content: strings.Repeat("steering ", 128)}, model.Message{Role: "assistant", Content: strings.Repeat("completed other work ", 128)})
	}
	return messages, pending
}

func TestCompactModelMessages_PendingCallStaysExact(t *testing.T) {
	for _, reasoning := range []bool{false, true} {
		for _, budget := range []int{1 << 20, 4096, 512} {
			t.Run(fmt.Sprintf("reasoning=%t/budget=%d", reasoning, budget), func(t *testing.T) {
				messages, pending := pendingProjectionHistory(reasoning)
				before, err := json.Marshal(messages)
				if err != nil {
					t.Fatal(err)
				}
				opts := DefaultEfficientContextOptions()
				opts.RecentMessages, opts.KeepReasoningRecent = 2, 0
				opts.OldToolArgumentBytes, opts.MaxBytes = 128, budget
				got := CompactModelMessages(messages, opts)
				found := false
				for _, msg := range got {
					if assistantHasToolCall(msg, "pending") {
						found = true
						if !reflect.DeepEqual(msg, pending) {
							t.Fatal("pending message arguments, content or reasoning changed")
						}
					}
				}
				if !found {
					t.Fatal("pending call was dropped while folding history")
				}
				after, err := json.Marshal(messages)
				if err != nil || string(after) != string(before) {
					t.Fatal("canonical input changed")
				}
			})
		}
	}
}

func TestCompactModelMessages_CompletedCallStillCompacts(t *testing.T) {
	messages, _ := pendingProjectionHistory(false)
	messages = append(messages[:2:2], append([]model.Message{{Role: "tool", ToolCallID: "pending", Name: "write_file", Content: "Completed."}}, messages[2:]...)...)
	opts := DefaultEfficientContextOptions()
	opts.RecentMessages, opts.OldToolArgumentBytes, opts.MaxBytes = 2, 128, 1<<20
	got := CompactModelMessages(messages, opts)
	if len(got) != len(messages) || len(got[1].ToolCalls[0].Function.Arguments) >= len(messages[1].ToolCalls[0].Function.Arguments) {
		t.Fatal("completed write arguments were not compacted")
	}
	if !strings.Contains(got[1].ToolCalls[0].Function.Arguments, "_buckley_compacted") {
		t.Fatal("completed write summary is missing")
	}
}

func TestCompactModelMessages_PartialBatchStaysExact(t *testing.T) {
	messages, pending := pendingProjectionHistory(true)
	pending.ToolCalls = append(pending.ToolCalls, model.ToolCall{ID: "done", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"done.go"}`}})
	messages[1] = pending
	messages = append(messages[:2:2], append([]model.Message{{Role: "tool", ToolCallID: "done", Name: "read_file", Content: "Completed."}}, messages[2:]...)...)
	opts := DefaultEfficientContextOptions()
	opts.RecentMessages, opts.MaxBytes = 2, 4096
	got := CompactModelMessages(messages, opts)
	for _, msg := range got {
		if assistantHasToolCall(msg, "pending") {
			if !reflect.DeepEqual(msg, pending) {
				t.Fatal("partially completed tool batch changed")
			}
			return
		}
	}
	t.Fatal("partially completed tool batch was dropped")
}

func TestProjectModelMessagesForRequest_PendingBudgetOverflowIsVisible(t *testing.T) {
	messages, pending := pendingProjectionHistory(true)
	req := model.ChatRequest{Model: "small-fixture", Messages: messages, MaxTokens: 2048}
	got, stats := ProjectModelMessagesForRequest(messages, req, 8192, 1)
	if stats.ProjectedTokens <= stats.BudgetTokens {
		t.Fatal("unfittable pending data was silently squeezed into the budget")
	}
	for _, msg := range got {
		if assistantHasToolCall(msg, "pending") {
			if !reflect.DeepEqual(msg, pending) {
				t.Fatal("pending payload changed while reporting context pressure")
			}
			return
		}
	}
	t.Fatal("pending call was lost")
}
