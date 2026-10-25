package model

import (
	"reflect"
	"testing"
)

func TestRepairToolMessageSequence_InterleavedSystemMessages(t *testing.T) {
	call := func(ids ...string) Message {
		msg := Message{Role: "assistant"}
		for _, id := range ids {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: id, Function: FunctionCall{Name: "activate_skill"}})
		}
		return msg
	}
	result := func(id string) Message {
		return Message{Role: "tool", ToolCallID: id, Name: "activate_skill", Content: "completed " + id}
	}
	system := Message{Role: "system", Content: "activated skill instructions"}
	secondSystem := Message{Role: "system", Content: "additional instructions"}
	user := Message{Role: "user", Content: "continue"}
	missing := result("two")
	missing.Content = missingToolResultContent
	orphan := orphanToolResultMessage(result("unknown"))
	for _, tc := range []struct {
		name string
		in   []Message
		want []Message
	}{
		{"activation", []Message{call("one"), system, result("one")}, []Message{call("one"), result("one"), system}},
		{"batch", []Message{call("one", "two"), system, result("two"), secondSystem, result("one"), user}, []Message{call("one", "two"), result("two"), result("one"), system, secondSystem, user}},
		{"missing result", []Message{call("one", "two"), result("one"), system}, []Message{call("one", "two"), result("one"), missing, system}},
		{"user boundary", []Message{call("two"), system, user, result("two")}, []Message{call("two"), missing, system, user, orphanToolResultMessage(result("two"))}},
		{"orphan boundary", []Message{call("two"), system, result("unknown")}, []Message{call("two"), missing, system, orphan}},
		{"normal instructions", []Message{system, user}, []Message{system, user}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]Message(nil), tc.in...)
			got := repairToolMessageSequence(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("repaired sequence=%+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.in, before) {
				t.Fatal("repair mutated original transcript")
			}
			if again := repairToolMessageSequence(got); !reflect.DeepEqual(again, got) {
				t.Fatalf("repair not idempotent: %+v", again)
			}
		})
	}
}
